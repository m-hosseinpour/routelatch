package engine_test

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/engine"
)

// fakeSidecar is a local stand-in for the separate executable. It implements
// the narrow runtime interface and exposes a minimal authenticated SOCKS5
// inbound so the real engine dial path can be exercised without a public exit.
type fakeSidecar struct {
	mu        sync.Mutex
	ln        net.Listener
	exits     []contract.SidecarExit
	handoffs  map[string]contract.SidecarHandoff
	healthy   bool
	stopped   bool
	syncErr   error
	noHandoff bool
	atyp      byte
	host      string
	// syncHook, when set, runs at the very start of Sync (before the fake's
	// lock is taken) so a test can hold Sync in flight. Set it before Sync
	// starts; it is not read/written concurrently.
	syncHook func()
}

func newFakeSidecar(t *testing.T) *fakeSidecar {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeSidecar{ln: ln, handoffs: map[string]contract.SidecarHandoff{}}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeSidecar) Sync(_ context.Context, exits []contract.SidecarExit) error {
	if f.syncHook != nil {
		f.syncHook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exits = append([]contract.SidecarExit(nil), exits...)
	f.handoffs = make(map[string]contract.SidecarHandoff, len(exits))
	for _, exit := range exits {
		f.handoffs[exit.Tag] = contract.SidecarHandoff{
			Address: f.ln.Addr().String(), Username: "u", Password: "p",
		}
	}
	f.healthy = f.syncErr == nil
	return f.syncErr
}

func (f *fakeSidecar) Handoff(tag string) (contract.SidecarHandoff, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.healthy || f.noHandoff {
		return contract.SidecarHandoff{}, false
	}
	h, ok := f.handoffs[tag]
	return h, ok
}

func (f *fakeSidecar) Healthy() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.healthy
}

func (f *fakeSidecar) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	f.healthy = false
	f.handoffs = nil
	return nil
}

func (f *fakeSidecar) syncedExits() []contract.SidecarExit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contract.SidecarExit(nil), f.exits...)
}

func (f *fakeSidecar) destination() (byte, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.atyp, f.host
}

func (f *fakeSidecar) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handshake(c)
	}
}

func (f *fakeSidecar) handshake(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	var greeting [2]byte
	if _, err := io.ReadFull(c, greeting[:]); err != nil || greeting[0] != 5 {
		return
	}
	methods := make([]byte, greeting[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	if _, err := c.Write([]byte{5, 2}); err != nil { // username/password
		return
	}
	var auth [2]byte
	if _, err := io.ReadFull(c, auth[:]); err != nil {
		return
	}
	if _, err := io.ReadFull(c, make([]byte, int(auth[1]))); err != nil {
		return
	}
	var passLen [1]byte
	if _, err := io.ReadFull(c, passLen[:]); err != nil {
		return
	}
	if _, err := io.ReadFull(c, make([]byte, int(passLen[0]))); err != nil {
		return
	}
	if _, err := c.Write([]byte{1, 0}); err != nil {
		return
	}
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil || req[0] != 5 || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		host = net.IP(buf).String()
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(c, length[:]); err != nil {
			return
		}
		buf := make([]byte, int(length[0]))
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		host = string(buf)
	case 4:
		if _, err := io.ReadFull(c, make([]byte, 16)); err != nil {
			return
		}
	default:
		return
	}
	var port [2]byte
	if _, err := io.ReadFull(c, port[:]); err != nil {
		return
	}
	f.mu.Lock()
	f.atyp, f.host = req[3], host
	f.mu.Unlock()
	reply := make([]byte, 10)
	reply[0], reply[1], reply[3] = 5, 0, 1
	binary.BigEndian.PutUint16(reply[8:], binary.BigEndian.Uint16(port[:]))
	if _, err := c.Write(reply); err != nil {
		return
	}
	_, _ = io.Copy(c, c)
}

func hysteria2Record(id string) contract.Record {
	return contract.Record{
		ID:          contract.RecordID(id),
		DedupeKey:   "sha256:" + id,
		Protocol:    contract.ProtocolHysteria2,
		Server:      "hy.example.com",
		Port:        443,
		Credentials: contract.Credentials{Password: "auth-secret"},
		Transport:   contract.TransportRaw,
		Security:    contract.SecurityTLS,
		Status:      contract.RecordUnknown,
	}
}

func TestHysteria2SidecarHandoff(t *testing.T) {
	if err := engine.CheckRecord(hysteria2Record("x-hy1")); err != nil {
		t.Fatalf("CheckRecord: %v", err)
	}
	fake := newFakeSidecar(t)
	rec := hysteria2Record("x-hy1")
	e, err := engine.Start(engine.Options{
		Exits: []contract.Record{rec}, PolicyVersion: 1, Sidecar: fake,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = e.Stop(context.Background()) }()
	if len(e.Excluded()) != 0 || !e.HasExit("x-hy1") {
		t.Fatalf("excluded=%+v hasExit=%v", e.Excluded(), e.HasExit("x-hy1"))
	}
	exits := fake.syncedExits()
	if len(exits) != 1 || exits[0].Tag != "rl-x-hy1" || exits[0].Server != "hy.example.com" ||
		exits[0].Port != 443 || exits[0].Password != "auth-secret" || exits[0].ServerName != "hy.example.com" {
		t.Fatalf("synced exits=%+v", exits)
	}
	if tag, ok := e.TagFor("x-hy1"); !ok || tag != "rl-x-hy1" {
		t.Fatalf("tag=%q ok=%v", tag, ok)
	}

	res, err := e.Dial(context.Background(), dialRequest("x-hy1", domainDestination("destination.example", 80), 1))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if res.Proof != contract.ProofConfirmed || res.Tag != "rl-x-hy1" {
		t.Fatalf("proof=%s tag=%s", res.Proof, res.Tag)
	}
	if _, err := res.Conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(res.Conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo=%q err=%v", buf, err)
	}
	_ = res.Conn.Close()
	if atyp, host := fake.destination(); atyp != 0x03 || host != "destination.example" {
		t.Fatalf("destination was resolved locally: atyp=%#x host=%q", atyp, host)
	}
}

func TestHysteria2WithoutSidecarFailsClosed(t *testing.T) {
	e, err := engine.Start(engine.Options{Exits: []contract.Record{hysteria2Record("x-hy2")}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = e.Stop(context.Background()) }()
	excluded := e.Excluded()
	if len(excluded) != 1 || excluded[0].Kind != contract.Unsupported ||
		excluded[0].Reason != engine.ReasonSidecarUnavailable || e.HasExit("x-hy2") {
		t.Fatalf("excluded=%+v hasExit=%v", excluded, e.HasExit("x-hy2"))
	}
	_, err = e.Dial(context.Background(), dialRequest("x-hy2", ipDestination(80), 1))
	if kind := failureKind(t, err); kind != contract.PoolUnavailable {
		t.Fatalf("dial kind=%s want pool_unavailable", kind)
	}
}

func TestHysteria2PolicyFenceAndUnhealthyFailClosed(t *testing.T) {
	fake := newFakeSidecar(t)
	e, err := engine.Start(engine.Options{
		Exits: []contract.Record{hysteria2Record("x-hy3")}, PolicyVersion: 2, Sidecar: fake,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Stop(context.Background()) }()
	_, err = e.Dial(context.Background(), dialRequest("x-hy3", ipDestination(80), 1))
	if kind := failureKind(t, err); kind != contract.Canceled {
		t.Fatalf("stale policy kind=%s want canceled", kind)
	}
	fake.mu.Lock()
	fake.noHandoff = true
	fake.mu.Unlock()
	_, err = e.Dial(context.Background(), dialRequest("x-hy3", ipDestination(80), 2))
	if kind := failureKind(t, err); kind != contract.PoolUnavailable {
		t.Fatalf("unhealthy kind=%s want pool_unavailable", kind)
	}
}

func TestHysteria2AddRemoveResyncsSidecar(t *testing.T) {
	fake := newFakeSidecar(t)
	rec := hysteria2Record("x-hy4")
	e, err := engine.Start(engine.Options{Sidecar: fake})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Stop(context.Background()) }()
	if _, err := e.AddExit(rec); err != nil {
		t.Fatalf("add: %v", err)
	}
	if exits := fake.syncedExits(); len(exits) != 1 || exits[0].Tag != "rl-x-hy4" {
		t.Fatalf("exits after add=%+v", exits)
	}
	if !e.RemoveExit("x-hy4") {
		t.Fatal("remove reported a missing exit")
	}
	if exits := fake.syncedExits(); len(exits) != 0 {
		t.Fatalf("exits after remove=%+v", exits)
	}
	if _, err := e.Dial(context.Background(), dialRequest("x-hy4", ipDestination(80), 1)); failureKind(t, err) != contract.PoolUnavailable {
		t.Fatal("removed sidecar exit is still dialable")
	}
}

func TestHysteria2PinAndInsecureRejectedByEngine(t *testing.T) {
	pinned := hysteria2Record("x-hy5")
	pinned.PinnedPeerCertSHA256 = strings.Repeat("ab", 32)
	expectExcluded(t, pinned, contract.Unsupported)

	insecure := hysteria2Record("x-hy6")
	insecure.InsecureTLS = true
	expectExcluded(t, insecure, contract.Unsupported)
}

// TestSidecarSyncDoesNotBlockDialsOrStop is the regression for the lifecycle
// finding: AddExit used to hold e.mu across a regeneration that can take up to
// sidecarSyncTimeout, blocking every dial and Stop. With the lock released, a
// failed engine start (or an in-flight add) can still be stopped promptly.
func TestSidecarSyncDoesNotBlockDialsOrStop(t *testing.T) {
	fake := newFakeSidecar(t)
	e, err := engine.Start(engine.Options{Sidecar: fake})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Stop(context.Background()) }()

	started := make(chan struct{})
	release := make(chan struct{})
	fake.syncHook = func() {
		close(started)
		<-release
	}

	addDone := make(chan error, 1)
	go func() {
		_, addErr := e.AddExit(hysteria2Record("x-hy-conc"))
		addDone <- addErr
	}()
	<-started

	// Locks used by Dial/status reads must be free while Sync is in flight.
	readsDone := make(chan struct{})
	go func() {
		_ = e.HasExit("x-hy-conc")
		_, _ = e.TagFor("x-hy-conc")
		_, _ = e.Dial(context.Background(), dialRequest("missing", ipDestination(80), 1))
		close(readsDone)
	}()
	select {
	case <-readsDone:
	case <-time.After(3 * time.Second):
		t.Fatal("dial/status reads were blocked by the sidecar Sync")
	}

	// Stop must not wait for the in-flight Sync either.
	stopDone := make(chan error, 1)
	go func() { stopDone <- e.Stop(context.Background()) }()
	select {
	case stopErr := <-stopDone:
		if stopErr != nil {
			t.Fatalf("stop: %v", stopErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop was blocked by the sidecar Sync")
	}

	close(release)
	if addErr := <-addDone; addErr == nil {
		t.Fatal("AddExit that raced a Stop must fail closed")
	}
}

// TestHysteria2IPLiteralServerNameRecordsDecision pins the documented fallback:
// an IP-literal server with no imported name uses the literal as tls.server_name
// (verified against sing-box v1.14.4 option/tls.go, where server_name is the
// SNI/verification name and insecure is a separate flag we always set false).
// This never disables verification; a mismatched certificate fails closed.
func TestHysteria2IPLiteralServerNameRecordsDecision(t *testing.T) {
	fake := newFakeSidecar(t)
	rec := hysteria2Record("x-hy-ip")
	rec.Server = "127.0.0.1"
	e, err := engine.Start(engine.Options{Sidecar: fake, Exits: []contract.Record{rec}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Stop(context.Background()) }()
	exits := fake.syncedExits()
	if len(exits) != 1 || exits[0].ServerName != "127.0.0.1" {
		t.Fatalf("server-name fallback: %+v", exits)
	}

	// An explicit SNI still wins over the IP literal.
	fake2 := newFakeSidecar(t)
	named := hysteria2Record("x-hy-ip-named")
	named.Server = "127.0.0.1"
	named.SNI = "hy.example.com"
	e2, err := engine.Start(engine.Options{Sidecar: fake2, Exits: []contract.Record{named}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e2.Stop(context.Background()) }()
	if exits := fake2.syncedExits(); len(exits) != 1 || exits[0].ServerName != "hy.example.com" {
		t.Fatalf("explicit SNI did not win: %+v", exits)
	}
}
