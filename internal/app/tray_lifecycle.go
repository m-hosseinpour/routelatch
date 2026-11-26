package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"routelatch/internal/trayui"
)

// trayLifecycle is the real trayui.Lifecycle boundary: the supervisor control
// pipe carries `quit` and `restart-engine`, and the supervisor event pipe
// carries ordered `restarting <token>` / `ready <token>` lines. Writes are
// non-blocking raw pipe writes, reads are forwarded through a bounded channel
// so a wedged tray can never hold the supervisor.
type trayLifecycle struct {
	fd int

	mu     sync.Mutex
	closed bool

	eventFD   int
	events    chan trayui.LifecycleEvent
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	// event ordering: ready only follows the latest restarting token.
	lastToken   uint64
	restartSeen bool
}

func newTrayLifecycle(controlFD, eventFD int) (*trayLifecycle, error) {
	if controlFD < 3 {
		return nil, errors.New("invalid control descriptor")
	}
	if err := syscall.SetNonblock(controlFD, true); err != nil {
		return nil, fmt.Errorf("make the control descriptor non-blocking: %w", err)
	}
	l := &trayLifecycle{
		fd:     controlFD,
		events: make(chan trayui.LifecycleEvent, 8),
		done:   make(chan struct{}),
	}
	if eventFD < 3 {
		return l, nil
	}
	if err := syscall.SetNonblock(eventFD, true); err != nil {
		return nil, fmt.Errorf("make the event descriptor non-blocking: %w", err)
	}
	l.eventFD = eventFD
	l.wg.Add(1)
	go l.readEvents()
	return l, nil
}

// Send writes one control line. It never blocks: a full pipe or a dead
// supervisor returns an error instead of stalling the menu.
func (l *trayLifecycle) Send(line string) error {
	if strings.ContainsAny(line, "\r\n") {
		return errors.New("tray control line contains a newline")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("tray control pipe is closed")
	}
	n, err := syscall.Write(l.fd, []byte(line+"\n"))
	if err != nil {
		return err
	}
	if n != len(line)+1 {
		return errors.New("short write to the tray control pipe")
	}
	return nil
}

func (l *trayLifecycle) Quit() error          { return l.Send("quit") }
func (l *trayLifecycle) RestartEngine() error { return l.Send("restart-engine") }

// Events is the bounded supervisor->controller channel. A missing event pipe
// (an older supervisor) yields a live, never-signaling channel.
func (l *trayLifecycle) Events() <-chan trayui.LifecycleEvent {
	return l.events
}

func (l *trayLifecycle) readEvents() {
	defer l.wg.Done()
	defer close(l.events)
	// A blocking read would not be woken by closing the inherited descriptor
	// from another goroutine, so poll with a bounded timeout and read only
	// complete lines.
	buf := make([]byte, 4096)
	var pending []byte
	for {
		select {
		case <-l.done:
			return
		default:
		}
		poll := []unix.PollFd{{Fd: int32(l.eventFD), Events: unix.POLLIN}}
		n, err := unix.Poll(poll, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		n, err = unix.Read(l.eventFD, buf)
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil || n == 0 {
			return
		}
		pending = append(pending, buf[:n]...)
		for {
			index := -1
			for i, b := range pending {
				if b == '\n' {
					index = i
					break
				}
			}
			if index < 0 {
				break
			}
			line := string(pending[:index])
			pending = pending[index+1:]
			if !l.forward(strings.TrimSpace(line)) {
				return
			}
		}
	}
}

func (l *trayLifecycle) forward(line string) bool {
	event, ok := l.parseEvent(line)
	if !ok {
		return true
	}
	select {
	case l.events <- event:
		return true
	case <-l.done:
		return false
	default:
		trayLogf("tray: lifecycle event dropped kind=%s", event.Kind)
		return true
	}
}

// parseEvent applies the documented ordering rules: tokens never go backwards,
// and `ready` is only accepted for the latest `restarting` token. A stray ready
// therefore cannot be mistaken for restart success.
func (l *trayLifecycle) parseEvent(line string) (trayui.LifecycleEvent, bool) {
	if line == "" {
		return trayui.LifecycleEvent{}, false
	}
	name := line
	token := uint64(0)
	if before, after, found := strings.Cut(line, " "); found {
		name = before
		n, err := strconv.ParseUint(strings.TrimSpace(after), 10, 64)
		if err != nil {
			return trayui.LifecycleEvent{}, false
		}
		token = n
	}
	switch name {
	case "restarting":
		if token < l.lastToken {
			return trayui.LifecycleEvent{}, false
		}
		l.lastToken = token
		l.restartSeen = true
		return trayui.LifecycleEvent{Kind: trayui.LifecycleRestarting}, true
	case "ready":
		if !l.restartSeen || token != l.lastToken {
			return trayui.LifecycleEvent{}, false
		}
		l.restartSeen = false
		return trayui.LifecycleEvent{Kind: trayui.LifecycleReady}, true
	default:
		return trayui.LifecycleEvent{}, false
	}
}

// Close stops the event reader and closes both inherited descriptors.
func (l *trayLifecycle) Close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		_ = syscall.Close(l.fd)
		l.mu.Unlock()
		close(l.done)
		if l.eventFD > 0 {
			_ = syscall.Close(l.eventFD)
		}
	})
	l.wg.Wait()
	return nil
}

// trayNotifier adapts the controller's Notifier to the existing
// destination-free desktop notification call. Notify only replaces the one
// pending notification, so a rate-limited host can never block a click or grow
// an unbounded queue; duplicate bodies within five seconds are coalesced.
type trayNotifier struct {
	send func(ctx context.Context, title, body string) error
	now  func() time.Time

	ch        chan trayui.Notification
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	mu       sync.Mutex
	lastBody string
	lastAt   time.Time
}

func newTrayNotifier(send func(ctx context.Context, title, body string) error) *trayNotifier {
	n := &trayNotifier{
		send: send,
		now:  time.Now,
		ch:   make(chan trayui.Notification, 1),
		done: make(chan struct{}),
	}
	n.wg.Add(1)
	go n.loop()
	return n
}

func (n *trayNotifier) Notify(note trayui.Notification) {
	body := trayui.SanitizeHeader(note.Body)
	if body == "" {
		return
	}
	title := trayui.SanitizeLabel(note.Title)
	if title == "" {
		title = "RouteLatch"
	}
	note = trayui.Notification{Title: title, Body: body}
	select {
	case <-n.done:
		return
	default:
	}
	// Keep only the newest pending notification.
	select {
	case <-n.ch:
	default:
	}
	select {
	case n.ch <- note:
	default:
	}
}

func (n *trayNotifier) loop() {
	defer n.wg.Done()
	for {
		select {
		case <-n.done:
			return
		case note := <-n.ch:
			if n.duplicate(note.Body) {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := n.send(ctx, note.Title, note.Body)
			cancel()
			if err != nil {
				trayLogf("tray: notification failed")
			}
		}
	}
}

func (n *trayNotifier) duplicate(body string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	if body == n.lastBody && now.Sub(n.lastAt) < 5*time.Second {
		return true
	}
	n.lastBody = body
	n.lastAt = now
	return false
}

// Close stops the worker and waits for an in-flight bounded call.
func (n *trayNotifier) Close() error {
	n.closeOnce.Do(func() { close(n.done) })
	n.wg.Wait()
	return nil
}
