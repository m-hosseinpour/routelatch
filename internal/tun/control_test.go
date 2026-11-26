package tun

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"routelatch/internal/contract"
)

func startServed(t *testing.T, svc *Service) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Serve(ctx, ln) }()
	return path, cancel, done
}

func newControlService(t *testing.T, allowedUID int, exitAfterDisable bool) (*Service, *fakeFactory, *fakeNetwork) {
	t.Helper()
	dir := t.TempDir()
	var calls []string
	netw := &fakeNetwork{}
	factory := &fakeFactory{calls: &calls}
	factory.onStart = func() { netw.present = true }
	factory.calls = &netw.calls
	svc, err := NewService(Options{
		Config:           ServiceConfig{AllowedUID: allowedUID, SocksAddress: "127.0.0.1:1080"},
		CoreFactory:      factory,
		Network:          netw,
		LeasePath:        filepath.Join(dir, "lease.json"),
		Now:              func() time.Time { return testNow },
		BootID:           func() string { return "boot-test" },
		ExitAfterDisable: exitAfterDisable,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, factory, netw
}

func TestControlEnableStatusDisable(t *testing.T) {
	svc, factory, netw := newControlService(t, os.Getuid(), false)
	path, cancel, done := startServed(t, svc)
	defer func() { cancel(); <-done }()
	client := &Client{Path: path, Timeout: 5 * time.Second}

	st, err := client.Status(context.Background())
	if err != nil || st.State != contract.TunOff {
		t.Fatalf("status=%+v err=%v", st, err)
	}
	if err := client.Enable(context.Background()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if len(factory.cores) != 1 || factory.cores[0].starts != 1 || !netw.present {
		t.Fatalf("cores=%d present=%v", len(factory.cores), netw.present)
	}
	st, err = client.Status(context.Background())
	if err != nil || st.State != contract.TunOn || !st.Desired {
		t.Fatalf("status after enable=%+v err=%v", st, err)
	}
	if err := client.Disable(context.Background()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	st, err = client.Status(context.Background())
	if err != nil || st.State != contract.TunOff || st.Desired {
		t.Fatalf("status after disable=%+v err=%v", st, err)
	}
}

func TestControlRejectsPeerUID(t *testing.T) {
	svc, _, _ := newControlService(t, os.Getuid()+1, false)
	path, cancel, done := startServed(t, svc)
	defer func() { cancel(); <-done }()

	// The service rejects on the peer credential before reading, so a loaded
	// machine can deliver EPIPE to our write. The rejection response still
	// follows; retry a fresh connection until it is read.
	var resp controlResponse
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = conn.Write([]byte(`{"cmd":"status"}` + "\n"))
		decodeErr := json.NewDecoder(conn).Decode(&resp)
		_ = conn.Close()
		if decodeErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no rejection response: %v", decodeErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if resp.OK || resp.Error != "peer_uid_rejected" {
		t.Fatalf("resp=%+v", resp)
	}
	// The client maps an unrecognized private error to internal.
	client := &Client{Path: path, Timeout: 2 * time.Second}
	_, err := client.Status(context.Background())
	var te *contract.TunError
	if !errors.As(err, &te) || te.Kind != contract.Internal {
		t.Fatalf("client err=%v", err)
	}
}

func TestControlRejectsUnknownAndMalformedRequests(t *testing.T) {
	svc, _, _ := newControlService(t, os.Getuid(), false)
	path, cancel, done := startServed(t, svc)
	defer func() { cancel(); <-done }()

	cases := []struct {
		body string
		code string
	}{
		{`{"cmd":"add-route"}`, "unknown_command"},
		{`{"cmd":"enable","interface":"evil0"}`, "bad_request"},
		{`{"cmd":""}`, "unknown_command"},
		{`not-json`, "bad_request"},
	}
	for _, tc := range cases {
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write([]byte(tc.body + "\n")); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		var resp controlResponse
		if err := json.NewDecoder(conn).Decode(&resp); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		conn.Close()
		if resp.OK || resp.Error != tc.code {
			t.Fatalf("%q -> %+v", tc.body, resp)
		}
	}
}

func TestControlDisableExitsTheService(t *testing.T) {
	svc, _, _ := newControlService(t, os.Getuid(), true)
	path, _, done := startServed(t, svc)
	client := &Client{Path: path, Timeout: 5 * time.Second}
	if err := client.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Disable(context.Background()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the service did not exit after disable")
	}
}

func TestControlDisableExitsEvenIfTheClientDisconnects(t *testing.T) {
	svc, _, _ := newControlService(t, os.Getuid(), true)
	path, _, done := startServed(t, svc)
	client := &Client{Path: path, Timeout: 5 * time.Second}
	if err := client.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Send the disable request and close immediately, before the service can
	// encode its response. A stopped service must still exit; otherwise a
	// timed-out client would leave a process that can never enable again.
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(`{"cmd":"disable"}` + "\n")); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	_ = conn.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the service did not exit after a disconnected disable")
	}
}

func TestClientTimeoutOutlivesTheControlBudget(t *testing.T) {
	// A slow but successful enable/disable takes up to controlTimeout on the
	// service side; the client must not report that as a timeout.
	if defaultClientTimeout <= controlOpTimeout {
		t.Fatalf("client timeout %s does not cover the operation timeout %s", defaultClientTimeout, controlOpTimeout)
	}
	if defaultClientTimeout <= controlTimeout {
		t.Fatalf("client timeout %s does not cover the connection deadline %s", defaultClientTimeout, controlTimeout)
	}
}

func TestListenControlPermissionsAndReuse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control.sock")
	ln, owned, err := listenControl(path, os.Getuid())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if !owned {
		t.Fatal("the service did not own its freshly created socket")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode=%o", fi.Mode().Perm())
	}
	if err := verifyControlSocket(path, os.Getuid()); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, _, err := listenControl(path, os.Getuid()); err == nil {
		t.Fatal("a live control socket was replaced")
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket file was left behind: %v", err)
	}
}

func TestListenControlReplacesAStaleSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	ln, owned, err := listenControl(path, os.Getuid())
	if err != nil {
		t.Fatalf("a stale socket was not replaced: %v", err)
	}
	if !owned {
		t.Fatal("not owned")
	}
	_ = ln.Close()
}

func TestListenControlRefusesANonSocketPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control.sock")
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listenControl(path, os.Getuid()); err == nil {
		t.Fatal("a regular file was replaced")
	}
}

func TestVerifyControlSocketRejectsLooseMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := verifyControlSocket(path, os.Getuid()); err == nil {
		t.Fatal("a group/world-accessible socket was accepted")
	}
}

func TestClientDialFailureMapping(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.sock")
	client := &Client{Path: missing, Timeout: time.Second}
	var te *contract.TunError
	if err := client.Enable(context.Background()); !errors.As(err, &te) ||
		te.Kind != contract.Unsupported || te.Reason != contract.TunReasonHelperUnavailable {
		t.Fatalf("enable err=%v", err)
	}
	if _, err := client.Status(context.Background()); !errors.As(err, &te) ||
		te.Kind != contract.Internal || te.Reason != contract.TunReasonServiceUnavailable {
		t.Fatalf("status err=%v", err)
	}
}

func TestServiceFailureMapping(t *testing.T) {
	cases := []struct {
		code   string
		reason contract.TunReason
		kind   contract.ErrorKind
		want   contract.TunReason
	}{
		{"unsupported", contract.TunReasonPlatformUnsupported, contract.Unsupported, contract.TunReasonPlatformUnsupported},
		{"state_locked", contract.TunReasonConflict, contract.StateLocked, contract.TunReasonConflict},
		{"unsupported", "", contract.Internal, ""},
		{"unsupported", "canary.invalid", contract.Internal, ""},
		{"weird", contract.TunReasonConflict, contract.Internal, contract.TunReasonConflict},
	}
	for _, tc := range cases {
		var te *contract.TunError
		err := serviceFailure(tc.code, tc.reason)
		if !errors.As(err, &te) || te.Kind != tc.kind || te.Reason != tc.want {
			t.Fatalf("%s/%s -> %#v", tc.code, tc.reason, err)
		}
	}
}
