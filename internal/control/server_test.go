package control

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"routelatch/internal/contract"
)

type stubHandler struct {
	calls atomic.Int64
	err   error
}

func (h *stubHandler) Handle(ctx context.Context, req Request) (any, error) {
	h.calls.Add(1)
	if h.err != nil {
		return nil, h.err
	}
	return map[string]any{"op": req.Op, "value": req.Value}, nil
}

type hugeHandler struct{}

func (hugeHandler) Handle(context.Context, Request) (any, error) {
	return map[string]string{"blob": strings.Repeat("x", contract.MaxManagementResponse)}, nil
}

type deadlineHandler struct {
	remaining time.Duration
}

func (h *deadlineHandler) Handle(ctx context.Context, _ Request) (any, error) {
	if dl, ok := ctx.Deadline(); ok {
		h.remaining = time.Until(dl)
	}
	return nil, nil
}

func testServer(t *testing.T, opts ServerOptions) (*Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rlctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	opts.Path = filepath.Join(dir, "control.sock")
	if opts.Handler == nil {
		opts.Handler = &stubHandler{}
	}
	srv, err := NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Start binds synchronously, so the socket must already exist as a socket.
	// Check it here so a missing/wrong-type path is reported verbatim instead of
	// surfacing later as Client.Call's opaque ErrNotRunning.
	info, err := os.Lstat(opts.Path)
	if err != nil {
		t.Fatalf("management socket %q missing after Start: %v", opts.Path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("management socket path %q is not a socket: mode %s", opts.Path, info.Mode())
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, opts.Path
}

func TestServerRoundTripAndSocketPermissions(t *testing.T) {
	handler := &stubHandler{}
	_, path := testServer(t, ServerOptions{Handler: handler})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %04o, want 0600", info.Mode().Perm())
	}
	client := &Client{Path: path}
	env, err := client.Call(context.Background(), Request{Op: OpConfigSet, Key: "log_level", Value: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	if !env.OK || env.Command != "config set" || env.SchemaVersion != 1 {
		t.Fatalf("envelope = %+v", env)
	}
	data, _ := env.Data.(map[string]any)
	if data["value"] != "debug" {
		t.Fatalf("data = %+v", data)
	}
	if handler.calls.Load() != 1 {
		t.Fatalf("handler calls = %d", handler.calls.Load())
	}
}

func TestForeignPeerIsRejectedBeforeHandler(t *testing.T) {
	handler := &stubHandler{}
	_, path := testServer(t, ServerOptions{
		Handler: handler,
		PeerUID: func(*net.UnixConn) (uint32, error) { return uint32(os.Getuid()) + 1, nil },
	})
	// Client.Call collapses every dial error into ErrNotRunning, discarding the
	// real errno. Probe the socket directly and log any dial failure verbatim so
	// a CI transport failure (ENOENT/ECONNREFUSED/EACCES) is visible. This does
	// not gate the test on the probe; it only reports the underlying error.
	var dialer net.Dialer
	probe, probeErr := dialer.DialContext(context.Background(), "unix", path)
	if probeErr != nil {
		t.Logf("raw unix dial probe to %q failed: %v", path, probeErr)
	} else {
		_ = probe.Close()
	}

	client := &Client{Path: path}
	env, err := client.Call(context.Background(), Request{Op: OpStatus})
	if err != nil {
		// Dial failures map to ErrNotRunning, but write/peer-close errors do not.
		// Re-probe at the failure point: a real errno here means a transport
		// problem, while a healthy socket points at the peer-close/write race.
		// Diagnostic only.
		var retry net.Dialer
		conn, derr := retry.DialContext(context.Background(), "unix", path)
		if conn != nil {
			_ = conn.Close()
		}
		_, lerr := os.Lstat(path)
		t.Fatalf("client.Call: %v (raw unix dial probe to %q: %v; socket lstat: %v)", err, path, derr, lerr)
	}
	if env.OK || env.Error == nil || env.Error.Kind != contract.PeerRejected {
		t.Fatalf("envelope = %+v, want peer_rejected", env)
	}
	if handler.calls.Load() != 0 {
		t.Fatal("handler ran for a rejected peer")
	}
	if ExitCodeForEnvelope(env) != contract.ExitDenied {
		t.Fatalf("exit = %d, want 9", ExitCodeForEnvelope(env))
	}
}

func TestForeignPeerRejectionIsDeterministic(t *testing.T) {
	handler := &stubHandler{}
	_, path := testServer(t, ServerOptions{
		Handler: handler,
		PeerUID: func(*net.UnixConn) (uint32, error) { return uint32(os.Getuid()) + 1, nil },
	})
	// The server checks the peer before reading the request: on a foreign peer
	// it writes the reject envelope and closes immediately. The client's write
	// can therefore race with that close. Exercise the real client/server path
	// many times against one server so the write-after-close interleaving is
	// covered; before the client fix this intermittently surfaced as
	// ErrNotRunning ("socket not available") instead of peer_rejected.
	for i := 0; i < 5000; i++ {
		client := &Client{Path: path}
		env, err := client.Call(context.Background(), Request{Op: OpStatus})
		if err != nil {
			t.Fatalf("iteration %d: client.Call: %v", i, err)
		}
		if env.OK || env.Error == nil || env.Error.Kind != contract.PeerRejected {
			t.Fatalf("iteration %d: envelope = %+v, want peer_rejected", i, env)
		}
	}
	if handler.calls.Load() != 0 {
		t.Fatal("handler ran for a rejected peer")
	}
}

func TestRequestSchemaVersionIsEnforced(t *testing.T) {
	_, path := testServer(t, ServerOptions{})
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	raw, _ := json.Marshal(map[string]any{"schema_version": 2, "op": OpStatus})
	if _, err := conn.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := readLineLimit(bufio.NewReader(conn), contract.MaxManagementResponse)
	if err != nil {
		t.Fatal(err)
	}
	var env contract.Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		t.Fatal(err)
	}
	if env.OK || env.Error == nil || env.Error.Kind != contract.Unsupported {
		t.Fatalf("envelope = %+v, want unsupported", env)
	}
}

func TestOversizeResponseBecomesAnErrorEnvelope(t *testing.T) {
	_, path := testServer(t, ServerOptions{Handler: hugeHandler{}})
	client := &Client{Path: path}
	env, err := client.Call(context.Background(), Request{Op: OpStatus})
	if err != nil {
		t.Fatal(err)
	}
	if env.OK || env.Error == nil || env.Error.Kind != contract.Internal {
		t.Fatalf("envelope = %+v, want an internal size error", env)
	}
}

func TestClientReportsMissingSocketAsNotRunning(t *testing.T) {
	client := &Client{Path: filepath.Join(t.TempDir(), "absent.sock")}
	if _, err := client.Call(context.Background(), Request{Op: OpStatus}); err != ErrNotRunning {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}

func TestStaleSocketIsReplacedOnlyByItsOwner(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rlctl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "control.sock")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(ServerOptions{Path: path, Handler: &stubHandler{}, AllowStale: false}); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(ServerOptions{Path: path, Handler: &stubHandler{}, AllowStale: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("stale socket was not replaced by its owner: %v", err)
	}
	defer srv.Close()
	// A second server must not steal the live socket.
	second, _ := NewServer(ServerOptions{Path: path, Handler: &stubHandler{}, AllowStale: true})
	if err := second.Start(context.Background()); err == nil {
		_ = second.Close()
		t.Fatal("a second server replaced a live management socket")
	}
}

// TestTunAndModeBudgetsCoverTheFullOperation pins the request budget for the
// operations that can disable TUN: the former 5s default cut a slow but
// successful enable/disable (30s service budget plus sidecar re-sync) into a
// misleading timeout (docs/contracts.md §10.3).
func TestTunAndModeBudgetsCoverTheFullOperation(t *testing.T) {
	for _, op := range []string{OpTunEnable, OpTunDisable, OpMode} {
		handler := &deadlineHandler{}
		_, path := testServer(t, ServerOptions{Handler: handler})
		client := &Client{Path: path, Timeout: 65 * time.Second}
		if _, err := client.Call(context.Background(), Request{Op: op}); err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		if handler.remaining < 45*time.Second {
			t.Fatalf("%s deadline=%s, want it to cover the 30s TUN operation plus sidecar re-sync", op, handler.remaining)
		}
	}
}
