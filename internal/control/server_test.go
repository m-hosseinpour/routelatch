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
	client := &Client{Path: path}
	env, err := client.Call(context.Background(), Request{Op: OpStatus})
	if err != nil {
		t.Fatal(err)
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
