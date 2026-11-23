package app

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tray only suggests the documented command; it must not promise or perform
// an automatic restore of desktop proxy settings.
func TestDesktopProxyRestoreHintNamesTheCommand(t *testing.T) {
	if !strings.Contains(desktopProxyRestoreHint, "routelatch desktop proxy restore") {
		t.Fatalf("hint = %q, want the documented restore command", desktopProxyRestoreHint)
	}
	if !strings.Contains(desktopProxyRestoreHint, "if the desktop proxy was enabled") {
		t.Fatalf("hint = %q, want the user-visible condition", desktopProxyRestoreHint)
	}
}

// A cancelled context must abort the probe before it dials the session bus.
func TestProbeTrayContextHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ProbeTrayContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ProbeTrayContext(cancelled) = %v, want context.Canceled", err)
	}
}

// A peer that accepts the socket but never answers AUTH must not hold the
// handshake past the caller's deadline. This is the regression that made the
// old unauthenticated probe hang on a live session bus.
func TestSessionBusHandshakeHonoursDeadline(t *testing.T) {
	calls := []struct {
		name string
		call func(context.Context) error
	}{
		{"tray probe", ProbeTrayContext},
		{"tray registration", func(ctx context.Context) error {
			return TrayRegisteredContext(ctx, os.Getpid())
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "rl-dbus-")
			if err != nil {
				t.Fatal(err)
			}
			sock := filepath.Join(dir, "bus")
			ln, err := net.Listen("unix", sock)
			if err != nil {
				os.RemoveAll(dir)
				t.Fatal(err)
			}
			var mu sync.Mutex
			var held []net.Conn
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					mu.Lock()
					held = append(held, conn)
					mu.Unlock()
				}
			}()
			t.Cleanup(func() {
				_ = ln.Close()
				mu.Lock()
				for _, conn := range held {
					_ = conn.Close()
				}
				mu.Unlock()
				_ = os.RemoveAll(dir)
			})
			t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+sock)

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- tc.call(ctx) }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("handshake returned %v, want context.DeadlineExceeded", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("handshake ignored the context deadline and is stuck")
			}
		})
	}
}
