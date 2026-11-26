package app

import (
	"context"
	"errors"
	"image/color"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

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

func TestTrayOptionsFromEnvIncludesSocketAndDataDir(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "run", "control.sock")
	t.Setenv("ROUTELATCH_TRAY_CONTROL_FD", "4")
	t.Setenv("ROUTELATCH_TRAY_PARENT_PIDFD", "3")
	t.Setenv("ROUTELATCH_TRAY_EVENT_FD", "5")
	t.Setenv("ROUTELATCH_PARENT_PID", "4242")
	t.Setenv("ROUTELATCH_TRAY_SOCKET", socket)
	t.Setenv("ROUTELATCH_DATA_DIR", dir)
	t.Setenv("ROUTELATCH_LOG_PATH", filepath.Join(dir, "log", "app.log"))

	opts, err := TrayOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if opts.ControlFD != 4 || opts.ParentFD != 3 || opts.EventFD != 5 || opts.ExpectedPID != 4242 {
		t.Fatalf("descriptors = %+v", opts)
	}
	if opts.Socket != socket || opts.DataDir != dir {
		t.Fatalf("socket/data dir = %q / %q", opts.Socket, opts.DataDir)
	}
	if opts.LogPath != filepath.Join(dir, "log", "app.log") {
		t.Fatalf("log path = %q", opts.LogPath)
	}
}

func TestTrayOptionsFromEnvRequiresAbsoluteSocket(t *testing.T) {
	t.Setenv("ROUTELATCH_TRAY_CONTROL_FD", "4")
	t.Setenv("ROUTELATCH_TRAY_PARENT_PIDFD", "3")
	for _, socket := range []string{"", "relative/control.sock"} {
		t.Setenv("ROUTELATCH_TRAY_SOCKET", socket)
		if _, err := TrayOptionsFromEnv(); err == nil {
			t.Fatalf("socket %q was accepted", socket)
		}
	}
}

func TestTrayStatusIconDistinguishesStates(t *testing.T) {
	states := []string{"ready", "starting", "loading", "stopped", "restarting", "unavailable"}
	seen := make(map[color.RGBA]bool)
	for _, state := range states {
		img := trayStatusIcon(state)
		bounds := img.Bounds()
		if bounds.Dx() != 22 || bounds.Dy() != 22 {
			t.Fatalf("%s bounds = %v", state, bounds)
		}
		fill := color.RGBAModel.Convert(img.At(11, 11)).(color.RGBA)
		if state == "ready" || state == "restarting" || state == "unavailable" {
			// The centre carries the white mark; sample the coloured rim.
			fill = color.RGBAModel.Convert(img.At(1, 11)).(color.RGBA)
		}
		seen[fill] = true
	}
	if len(seen) < 5 {
		t.Fatalf("status icons share too many colours: %v", seen)
	}
}
