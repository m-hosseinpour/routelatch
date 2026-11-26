package app

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"routelatch/internal/trayui"
)

func readControlLine(t *testing.T, r *os.File) string {
	t.Helper()
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		t.Fatalf("read control line: %v", err)
	}
	return strings.TrimSpace(line)
}

func TestTrayLifecycleControlMessages(t *testing.T) {
	controlR, controlW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer controlR.Close()
	lifecycle, err := newTrayLifecycle(int(controlW.Fd()), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Send("ready"); err != nil {
		t.Fatal(err)
	}
	if got := readControlLine(t, controlR); got != "ready" {
		t.Fatalf("line = %q, want ready", got)
	}
	if err := lifecycle.Quit(); err != nil {
		t.Fatal(err)
	}
	if got := readControlLine(t, controlR); got != "quit" {
		t.Fatalf("line = %q, want quit", got)
	}
	if err := lifecycle.RestartEngine(); err != nil {
		t.Fatal(err)
	}
	if got := readControlLine(t, controlR); got != "restart-engine" {
		t.Fatalf("line = %q, want restart-engine", got)
	}
	if err := lifecycle.Send("bad\nline"); err == nil {
		t.Fatal("multiline control message was accepted")
	}
	if err := lifecycle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Quit(); err == nil {
		t.Fatal("Quit after Close succeeded")
	}
}

// A dead supervisor closes the read end; the lifecycle write must fail instead
// of blocking the tray.
func TestTrayLifecycleWriteDoesNotBlockWithoutReader(t *testing.T) {
	controlR, controlW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = controlR.Close()
	lifecycle, err := newTrayLifecycle(int(controlW.Fd()), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()
	done := make(chan error, 1)
	go func() { done <- lifecycle.Quit() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Quit on a closed pipe returned nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Quit blocked on a closed pipe")
	}
}

func TestTrayLifecycleEventOrdering(t *testing.T) {
	controlR, controlW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer controlR.Close()
	eventR, eventW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := newTrayLifecycle(int(controlW.Fd()), int(eventR.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()

	lines := []string{
		"restarting 1\n",
		"ready 1\n",
		"ready 2\n", // no restarting 2 yet: stale/unsolicited
		"ready 1\n", // duplicate
		"restarting 2\n",
		"restarting 1\n", // stale token
		"ready 1\n",      // does not match the latest token
		"garbage\n",
		"restarting nope\n",
		"ready 2\n",
	}
	go func() {
		for _, line := range lines {
			_, _ = eventW.WriteString(line)
		}
		_ = eventW.Close()
	}()

	want := []trayui.LifecycleEventKind{trayui.LifecycleRestarting, trayui.LifecycleReady, trayui.LifecycleRestarting, trayui.LifecycleReady}
	for i, kind := range want {
		select {
		case event, ok := <-lifecycle.Events():
			if !ok {
				t.Fatalf("event %d: channel closed early", i)
			}
			if event.Kind != kind {
				t.Fatalf("event %d = %v, want %v", i, event.Kind, kind)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("event %d timed out", i)
		}
	}
	select {
	case _, ok := <-lifecycle.Events():
		if ok {
			t.Fatal("unexpected extra event")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event channel did not close at EOF")
	}
}

func TestTrayLifecycleDropsWhenUnread(t *testing.T) {
	controlR, controlW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer controlR.Close()
	eventR, eventW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer eventW.Close()
	lifecycle, err := newTrayLifecycle(int(controlW.Fd()), int(eventR.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()

	// No reader: the bounded channel fills and later events are dropped rather
	// than blocking the reader goroutine.
	for i := 0; i < 32; i++ {
		_, _ = eventW.WriteString(fmt.Sprintf("restarting %d\n", i+1))
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(lifecycle.Events()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(lifecycle.Events()) == 0 {
		t.Fatal("no event reached the bounded channel")
	}
	if len(lifecycle.Events()) > cap(lifecycle.Events()) {
		t.Fatal("bounded channel overflowed")
	}
}

func TestTrayNotifierCoalescesAndDeduplicates(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	sent := make(chan string, 8)
	notifier := newTrayNotifier(func(ctx context.Context, title, body string) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		sent <- body
		return nil
	})
	notifier.Notify(trayui.Notification{Title: "RouteLatch", Body: "one"})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("notifier worker did not start")
	}
	// The worker is busy, so these two collapse into the newest pending item.
	notifier.Notify(trayui.Notification{Title: "RouteLatch", Body: "two"})
	close(release)
	if got := <-sent; got != "one" {
		t.Fatalf("first notification = %q", got)
	}
	select {
	case got := <-sent:
		if got != "two" {
			t.Fatalf("coalesced notification = %q, want two", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending notification was not delivered")
	}

	// Same body immediately afterwards is suppressed.
	notifier.Notify(trayui.Notification{Title: "RouteLatch", Body: "two"})
	select {
	case got := <-sent:
		t.Fatalf("duplicate %q was delivered", got)
	case <-time.After(100 * time.Millisecond):
	}
	_ = notifier.Close()
}

func TestTrayNotifierNotifyNeverBlocks(t *testing.T) {
	release := make(chan struct{})
	notifier := newTrayNotifier(func(ctx context.Context, title, body string) error {
		<-release
		return nil
	})
	defer notifier.Close()
	defer close(release)
	start := time.Now()
	for i := 0; i < 100; i++ {
		notifier.Notify(trayui.Notification{Title: "RouteLatch", Body: fmt.Sprintf("result %d", i)})
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Notify blocked for %s", elapsed)
	}
}

// Closing the tray components must not leak a reader/notifier goroutine or a
// descriptor. Counts are compared after allowing the runtime to settle.
func TestTrayLifecycleAndNotifierCleanShutdown(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	baselineFDs := countOpenFDs(t)

	controlR, controlW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer controlR.Close()
	eventR, eventW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := newTrayLifecycle(int(controlW.Fd()), int(eventR.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	notifier := newTrayNotifier(func(ctx context.Context, title, body string) error { return nil })
	notifier.Notify(trayui.Notification{Title: "RouteLatch", Body: "close"})
	if err := notifier.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Close(); err != nil {
		t.Fatal(err)
	}
	_ = eventW.Close()
	_ = controlR.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baselineGoroutines && countOpenFDs(t) <= baselineFDs {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("leak after close: goroutines %d -> %d, fds %d -> %d",
		baselineGoroutines, runtime.NumGoroutine(), baselineFDs, countOpenFDs(t))
}

func countOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot count descriptors: %v", err)
	}
	return len(entries)
}

func TestAnnounceTrayReadyWaitsForRegistration(t *testing.T) {
	var lines []string
	send := func(line string) error {
		lines = append(lines, line)
		return nil
	}
	registrationErr := fmt.Errorf("not registered")
	if err := announceTrayReady(func() error { return registrationErr }, send); err == nil {
		t.Fatal("registration failure did not fail readiness")
	}
	if len(lines) != 1 || lines[0] != "error tray_registration_failed" {
		t.Fatalf("lines = %v, want only the registration error", lines)
	}

	lines = nil
	if err := announceTrayReady(func() error { return nil }, send); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "ready" {
		t.Fatalf("lines = %v, want ready", lines)
	}

	sendErr := fmt.Errorf("write failed")
	if err := announceTrayReady(func() error { return nil }, func(string) error { return sendErr }); err == nil {
		t.Fatal("failed ready write reported success")
	}
}
