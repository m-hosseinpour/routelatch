package httpfront

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClientDisconnectDoesNotCancelDial(t *testing.T) {
	entered := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	release := make(chan struct{})

	p := newProxy(t, Config{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			cancelled <- struct{}{}
			return nil, ctx.Err()
		case <-release:
			return nil, errors.New("test released the dial")
		}
	}})
	proxyAddr, _ := startProxy(t, p)

	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	sendRaw(t, c, "CONNECT upstream.invalid:80 HTTP/1.1\r\nHost: upstream.invalid:80\r\n\r\n")
	<-entered
	_ = c.Close()

	select {
	case <-cancelled:
		t.Fatalf("dial context was cancelled by client disconnect (unexpected)")
	case <-time.After(400 * time.Millisecond):
		t.Log("OBSERVED: client close did not cancel the in-flight dial; the frontend must own cancellation")
	}
	close(release)
}

// TestOwnedCancelStopsDial is the control: with a context our dialer owns, a
// mode switch can abort the pending dial and still answer the client.
func TestOwnedCancelStopsDial(t *testing.T) {
	owned, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	p := newProxy(t, Config{Dial: func(_ context.Context, network, addr string) (net.Conn, error) {
		close(entered)
		<-owned.Done()
		return nil, fmt.Errorf("dial cancelled by policy change: %w", owned.Err())
	}})
	proxyAddr, _ := startProxy(t, p)

	c := dialTCP(t, proxyAddr)
	sendRaw(t, c, "CONNECT upstream.invalid:80 HTTP/1.1\r\nHost: upstream.invalid:80\r\n\r\n")
	<-entered
	start := time.Now()
	cancel()
	resp := readAllResponse(t, c)
	if !strings.HasPrefix(string(resp), "HTTP/1.1 502") {
		t.Fatalf("want 502 after owned cancellation, got %q", resp)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}

type connRegistry struct {
	mu    sync.Mutex
	conns []net.Conn
}

func (r *connRegistry) add(c net.Conn) {
	r.mu.Lock()
	r.conns = append(r.conns, c)
	r.mu.Unlock()
}

func (r *connRegistry) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conns {
		_ = c.Close()
	}
	r.conns = nil
}

// TestModeSwitchClosesLiveTunnel closes a live hijacked CONNECT from another
// goroutine (as a mode switch must) and checks that both peers see teardown and
// that goroutines/sockets do not accumulate over five rounds.
func TestModeSwitchClosesLiveTunnel(t *testing.T) {
	peerClosed := make(chan struct{}, 16)
	upstream := startTCPServer(t, func(c net.Conn) {
		buf := make([]byte, 256)
		for {
			n, err := c.Read(buf)
			if n > 0 {
				_, _ = c.Write(buf[:n])
			}
			if err != nil {
				peerClosed <- struct{}{}
				return
			}
		}
	})

	var up connRegistry
	p := newProxy(t, Config{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := directDial(ctx, network, upstream)
		if err == nil {
			up.add(c)
		}
		return c, err
	}})
	proxyAddr, tracker := startProxy(t, p)

	round := func() {
		c, err := net.Dial("tcp", proxyAddr)
		if err != nil {
			t.Fatalf("dial proxy: %v", err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		sendRaw(t, c, "CONNECT tunnel.invalid:80 HTTP/1.1\r\nHost: tunnel.invalid:80\r\n\r\n")
		br := bufio.NewReader(c)
		head := readHead(t, br)
		if !strings.HasPrefix(string(head), "HTTP/1.1 200") {
			t.Fatalf("CONNECT failed: %q", head)
		}
		sendRaw(t, c, "ping")
		echo := make([]byte, 4)
		if _, err := io.ReadFull(br, echo); err != nil || string(echo) != "ping" {
			t.Fatalf("echo = %q, err = %v", echo, err)
		}

		// Mode switch: close the live tunnel from another goroutine.
		tracker.CloseAll()
		up.closeAll()

		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err = c.Read(make([]byte, 1))
		requireTeardown(t, "client after mode-switch close", err)
		select {
		case <-peerClosed:
		case <-time.After(2 * time.Second):
			t.Fatal("upstream peer did not observe teardown")
		}
	}

	round() // warm-up so lazy goroutines are already running
	time.Sleep(150 * time.Millisecond)
	baseG, baseFD := runtime.NumGoroutine(), socketFDCount()
	t.Logf("after warm-up: goroutines=%d fds=%d", baseG, baseFD)

	var fds, gs []int
	for i := 0; i < 5; i++ {
		round()
		time.Sleep(100 * time.Millisecond)
		fds = append(fds, socketFDCount())
		gs = append(gs, runtime.NumGoroutine())
	}
	t.Logf("goroutines per round: %v; socket fds per round: %v (total fds now %d)", gs, fds, countFDs())

	// Compare consecutive late rounds: a leak per connection would keep growing;
	// a one-off process baseline shift is not attributed to the tunnel rounds.
	if fds[4] > fds[2]+2 || gs[4] > gs[2]+2 {
		t.Fatalf("resources grew across rounds (goroutines %v, fds %v)", gs, fds)
	}
}
