package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/engine"
	"routelatch/internal/frontend"
	"routelatch/internal/imports"
	"routelatch/internal/pool"
	"routelatch/internal/probes"
	"routelatch/internal/store"
)

func newServiceStore(t *testing.T) (*store.Store, store.Paths) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rlctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	paths, err := store.ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	return st, paths
}

func resolveTestPaths(dir string) (store.Paths, error) {
	return store.ResolvePathsFor(dir, "", "", "", "")
}

func openTestStore(paths store.Paths) (*store.Store, error) {
	return store.Open(store.Options{Paths: paths})
}

func newTestService(t *testing.T, st *store.Store, paths store.Paths) *Service {
	t.Helper()
	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

type fakePool struct {
	requests atomic.Int64
}

func (f *fakePool) DialPool(ctx context.Context, req contract.PoolDialRequest) (contract.PoolDialResult, error) {
	f.requests.Add(1)
	return contract.PoolDialResult{}, &contract.Failure{
		Kind: contract.PoolUnavailable, Path: contract.PathPool, Op: "test.pool",
		Destination: req.Destination, Message: "no usable exit: test pool is empty",
	}
}

type countingDirect struct {
	inner contract.Dialer
	calls atomic.Int64
}

func (c *countingDirect) DialDirect(ctx context.Context, dst contract.Destination) (net.Conn, error) {
	c.calls.Add(1)
	return c.inner.DialDirect(ctx, dst)
}

// attachFrontend gives a core-only service the real two listeners with a
// caller-supplied pool. Production StartNetwork does the same wiring.
func attachFrontend(t *testing.T, s *Service, pl contract.PoolDialer, direct contract.Dialer) {
	t.Helper()
	fe, err := frontend.New(frontend.Options{
		Router: routerView{s: s}, Pool: pl, Direct: direct, Registry: s.reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fe.Start("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	s.fe = fe
	s.pool = pl
	s.direct = direct
	s.httpAddr = fe.HTTPAddr().String()
	s.socksAddr = fe.SOCKSAddr().String()
	s.listenersOpen = true
	t.Cleanup(func() { _ = fe.Stop(context.Background()) })
}

func TestModeSwitchClosesLiveAndPendingThenReportsSuccess(t *testing.T) {
	st, paths := newServiceStore(t)
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Rules = []contract.Rule{{
			ID: "r-example", Kind: contract.RuleDomainSuffix, Host: "example.com",
			Origin: contract.RuleOriginUser,
		}}
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, st, paths)
	snap := svc.Snapshot()
	version := snap.State.PolicyVersion
	decision := func(host string) contract.Decision {
		return contract.Decision{
			Mode: contract.ModeTargeted, Class: contract.ClassApproved, Route: contract.RoutePool,
			Reason: contract.ReasonApprovedRule, RuleID: "r-example", PolicyVersion: version,
		}
	}
	liveDst := contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443}
	live, err := svc.reg.Register(contract.ConnSpec{ID: "c-live", Kind: contract.ConnSOCKS5Connect, Destination: liveDst, Decision: decision("example.com")})
	if err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	defer server.Close()
	if err := live.SetUpstream(client); err != nil {
		t.Fatal(err)
	}
	pendingDst := contract.Destination{Kind: contract.DestDomain, Host: "api.example.com", Port: 443}
	pending, err := svc.reg.Register(contract.ConnSpec{ID: "c-pending", Kind: contract.ConnHTTPConnect, Destination: pendingDst, Decision: decision("api.example.com")})
	if err != nil {
		t.Fatal(err)
	}

	data, err := svc.setMode(context.Background(), string(contract.ModeDirectAll))
	if err != nil {
		t.Fatal(err)
	}
	if data.Mode != contract.ModeDirectAll {
		t.Fatalf("mode = %s", data.Mode)
	}
	if data.Closed.Count != 1 || data.Closed.Pending != 1 {
		t.Fatalf("closed=%+v, want 1 live and 1 pending", data.Closed)
	}
	// The sweep completed before success was returned: both handles are closed
	// and their contexts canceled at this point.
	if live.Context().Err() == nil || pending.Context().Err() == nil {
		t.Fatal("a conflicting connection survived the mode change")
	}
	if got := len(svc.reg.Snapshot()); got != 0 {
		t.Fatalf("registry still holds %d connections", got)
	}
	// A stale decision may not enter after the sweep.
	if _, err := svc.reg.Register(contract.ConnSpec{ID: "c-stale", Kind: contract.ConnSOCKS5Connect, Destination: liveDst, Decision: decision("example.com")}); err == nil {
		t.Fatal("stale policy version was accepted")
	}
}

func TestAllDirectKeepsListenerAndStoppedClosesIt(t *testing.T) {
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	pl := &fakePool{}
	direct := &countingDirect{inner: newDirectDialer(nil)}
	attachFrontend(t, svc, pl, direct)
	echoAddr, stopEcho := startEcho(t)
	defer stopEcho()

	if _, err := svc.setMode(context.Background(), string(contract.ModeDirectAll)); err != nil {
		t.Fatal(err)
	}
	if !svc.listenersOpen {
		t.Fatal("all-direct closed the traffic listener")
	}
	addr := svc.httpAddr
	conn, br, resp := connectThrough(t, addr, echoAddr)
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("direct CONNECT failed: %+v", resp)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo read = %q, err=%v", buf, err)
	}
	_ = conn.Close()

	if _, err := svc.setMode(context.Background(), string(contract.ModeStopped)); err != nil {
		t.Fatal(err)
	}
	if svc.listenersOpen {
		t.Fatal("stopped mode left a traffic listener open")
	}
	if conn, err := net.Dial("tcp", addr); err == nil {
		_ = conn.Close()
		t.Fatal("stopped mode still accepted a connection")
	}
	if _, err := svc.setMode(context.Background(), string(contract.ModeTargeted)); err == nil {
		t.Fatal("leaving stopped mode without a restart was accepted")
	}
}

func TestEmptyPoolDirectWorksApprovedFailsWithoutDirectDial(t *testing.T) {
	st, paths := newServiceStore(t)
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Rules = []contract.Rule{{
			ID: "r-example", Kind: contract.RuleDomainSuffix, Host: "example.com",
			Origin: contract.RuleOriginUser,
		}}
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, st, paths)
	pl := &fakePool{}
	direct := &countingDirect{inner: newDirectDialer(nil)}
	attachFrontend(t, svc, pl, direct)
	echoAddr, stopEcho := startEcho(t)
	defer stopEcho()

	// A local destination is ClassLocal and goes direct even with an empty pool.
	conn, br, resp := connectThrough(t, svc.httpAddr, echoAddr)
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("local CONNECT failed: %+v", resp)
	}
	_ = conn.Close()
	_ = br

	before := direct.calls.Load()
	conn2, br2, resp2 := connectThrough(t, svc.httpAddr, "example.com:443")
	if conn2 != nil {
		_ = conn2.Close()
	}
	_ = br2
	if resp2 == nil || resp2.StatusCode != http.StatusBadGateway {
		t.Fatalf("approved destination status = %+v, want 502", resp2)
	}
	if got := direct.calls.Load(); got != before {
		t.Fatalf("approved destination made %d direct dials", got-before)
	}
	if pl.requests.Load() != 1 {
		t.Fatalf("pool was asked %d times, want 1", pl.requests.Load())
	}
}

func startEcho(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close(); <-done }
}

func connectThrough(t *testing.T, proxyAddr, destination string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", destination, destination)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return conn, br, resp
	}
	return conn, br, resp
}

func TestSanitizerIsUsedForPoolDynamicStrings(t *testing.T) {
	st, paths := newServiceStore(t)
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Records = []contract.Record{{
			ID: "x-1", DedupeKey: "sha256:x", Status: contract.RecordUnknown,
			DisplayName: "node\x1b[2Jname", Protocol: contract.ProtocolTrojan,
			Server: "example.com", Port: 443, Transport: contract.TransportRaw,
			Security: contract.SecurityTLS,
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, st, paths)
	data, err := svc.listPool(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Records) != 1 {
		t.Fatalf("records = %d", len(data.Records))
	}
	if strings.ContainsRune(data.Records[0].DisplayName, 0x1b) {
		t.Fatalf("pool list leaked an escape sequence: %q", data.Records[0].DisplayName)
	}
}

func TestSecretFixtureNeverAppearsInOutput(t *testing.T) {
	st, paths := newServiceStore(t)
	secret := "SUPERSECRETTOKEN-7f3a9c"
	svc := newTestService(t, st, paths)
	link := "trojan://" + secret + "@example.com:443?security=tls&type=raw&sni=example.com#node-one"
	if _, err := svc.refresher.AddLink(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	if err := svc.reloadAfterImport(context.Background()); err != nil {
		t.Fatal(err)
	}
	poolData, err := svc.listPool(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	statusData, err := svc.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"pool": poolData, "status": statusData} {
		blob, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(blob), secret) {
			t.Fatalf("%s output contains the fixture credential", name)
		}
	}
	// The secret is persisted (state is 0600) but never returned.
	raw, err := os.ReadFile(filepath.Join(paths.StateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), secret) {
		t.Fatal("fixture credential was not persisted in the private state file")
	}
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }

func TestNotFoundUsesContractKind(t *testing.T) {
	_, err := NewService(context.Background(), Options{})
	if err == nil {
		t.Fatal("service without a store was accepted")
	}
	got := NotFound("rule r-x")
	var failure *contract.Failure
	if !errors.As(got, &failure) || failure.Kind != contract.NotFound {
		t.Fatalf("NotFound kind = %v", got)
	}
}

func TestStartNetworkWithEmptyPoolServesDirectOnly(t *testing.T) {
	st, paths := newServiceStore(t)
	httpAddr := freeLoopbackAddr(t)
	socksAddr := freeLoopbackAddr(t)
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.HTTPListen = httpAddr
		cfg.SOCKSListen = socksAddr
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Rules = []contract.Rule{{
			ID: "r-example", Kind: contract.RuleDomainSuffix, Host: "example.com",
			Origin: contract.RuleOriginUser,
		}}
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	direct := &countingDirect{inner: newDirectDialer([]uint16{portOf(t, httpAddr), portOf(t, socksAddr)})}
	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, Direct: direct, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.StartNetwork(context.Background()); err != nil {
		t.Fatalf("StartNetwork with an empty pool: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	echoAddr, stopEcho := startEcho(t)
	defer stopEcho()

	conn, br, resp := connectThrough(t, svc.httpAddr, echoAddr)
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("direct destination through run wiring failed: %+v", resp)
	}
	_ = conn.Close()
	_ = br
	before := direct.calls.Load()
	conn2, _, resp2 := connectThrough(t, svc.httpAddr, "example.com:443")
	if conn2 != nil {
		_ = conn2.Close()
	}
	if resp2 == nil || resp2.StatusCode != http.StatusBadGateway {
		t.Fatalf("approved destination status = %+v, want 502", resp2)
	}
	if direct.calls.Load() != before {
		t.Fatal("an approved destination was dialed directly instead of failing with pool_unavailable")
	}
}

// TestPinnedLinkWithAllowInsecureRunsEndToEnd reproduces the review defect: a
// link carrying both allowInsecure and a pin must import as a verified pinned
// record, pool list/status must agree, and a real service start must register
// the exit instead of aborting on the insecure flag.
func TestPinnedLinkWithAllowInsecureRunsEndToEnd(t *testing.T) {
	st, paths := newServiceStore(t)
	httpAddr := freeLoopbackAddr(t)
	socksAddr := freeLoopbackAddr(t)
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.HTTPListen = httpAddr
		cfg.SOCKSListen = socksAddr
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pin := strings.Repeat("ab", 32)
	link := "vless://11111111-1111-4111-8111-111111111111@127.0.0.1:443?encryption=none&security=tls&allowInsecure=1&type=tcp&pinSHA256=" + pin + "#pinned"
	data, err := svc.importSource(context.Background(), Request{Op: OpImport, Kind: "link", Input: link})
	if err != nil {
		t.Fatalf("import pinned link: %v", err)
	}
	if data.Records.Valid != 1 || data.Records.Unsupported != 0 || len(data.Notices) == 0 {
		t.Fatalf("import result = %+v, want one valid record and a notice", data)
	}
	rows, err := svc.listPool(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Counts.Total != 1 || rows.Counts.Usable != 1 || rows.Counts.Unsupported != 0 || len(rows.Records) != 1 {
		t.Fatalf("pool list counts = %+v", rows.Counts)
	}
	if rows.Records[0].InsecureTLS {
		t.Fatalf("pinned record kept insecure_tls: %+v", rows.Records[0])
	}
	status, err := svc.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Pool.Total != 1 || status.Pool.Usable != 1 {
		t.Fatalf("status pool = %+v", status.Pool)
	}
	if err := svc.StartNetwork(context.Background()); err != nil {
		t.Fatalf("run with the pinned record: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if !svc.engine.HasExit(contract.ExitID(rows.Records[0].ID)) {
		t.Fatalf("engine did not register the pinned exit; excluded=%+v", svc.engine.Excluded())
	}
	if ex := svc.engine.Excluded(); len(ex) != 0 {
		t.Fatalf("pinned record was excluded: %+v", ex)
	}
}

// TestImportRollbackLeavesNoRecord forces the post-commit reload to fail and
// asserts the import reports an error while the record is not left stored.
func TestImportRollbackLeavesNoRecord(t *testing.T) {
	st, paths := newServiceStore(t)
	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	// A stopped engine makes the post-commit reconcile fail deterministically.
	eng, err := engine.Start(engine.Options{LogLevel: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc.engine = eng

	link := "vless://11111111-1111-4111-8111-111111111111@127.0.0.1:443?encryption=none&security=tls&type=tcp#rollback"
	if _, err := svc.importSource(context.Background(), Request{Op: OpImport, Kind: "link", Input: link}); err == nil {
		t.Fatal("import succeeded although the reload failed")
	}
	after, err := st.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.State.Records) != 0 || len(after.State.Sources) != 0 {
		t.Fatalf("failed import persisted state: records=%+v sources=%+v", after.State.Records, after.State.Sources)
	}
}

// TestStartNetworkIsolatesOneBadRecord proves the service starts with a pool
// holding one unbuildable record and one good record, registers only the good
// exit, and makes pool/status report the excluded record as unsupported.
func TestStartNetworkIsolatesOneBadRecord(t *testing.T) {
	st, paths := newServiceStore(t)
	httpAddr := freeLoopbackAddr(t)
	socksAddr := freeLoopbackAddr(t)
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.HTTPListen = httpAddr
		cfg.SOCKSListen = socksAddr
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	good := contract.Record{
		ID: "x-good", DedupeKey: "sha256:good", Protocol: contract.ProtocolVLESS,
		Server: "127.0.0.1", Port: 443, Credentials: contract.Credentials{UserID: "11111111-1111-4111-8111-111111111111"},
		Transport: contract.TransportRaw, Security: contract.SecurityNone, Status: contract.RecordUnknown,
	}
	bad := contract.Record{
		ID: "x-bad", DedupeKey: "sha256:bad", Protocol: contract.ProtocolVLESS,
		Server: "127.0.0.1", Port: 443, Credentials: contract.Credentials{UserID: strings.Repeat("z", 31)},
		Transport: contract.TransportRaw, Security: contract.SecurityNone, Status: contract.RecordUnknown,
	}
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Records = []contract.Record{bad, good}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.StartNetwork(context.Background()); err != nil {
		t.Fatalf("start with one bad record: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if !svc.engine.HasExit("x-good") || svc.engine.HasExit("x-bad") {
		t.Fatalf("engine exits: good=%v bad=%v", svc.engine.HasExit("x-good"), svc.engine.HasExit("x-bad"))
	}
	snap := svc.Snapshot()
	for _, rec := range snap.State.Records {
		if rec.ID == "x-bad" && (rec.Status != contract.RecordUnsupported || rec.StatusReason != engine.ReasonRecordRejected) {
			t.Fatalf("excluded record status = %q/%q", rec.Status, rec.StatusReason)
		}
	}
	p := pool.Build(snap.State, svc.now().UTC())
	if counts := p.Counts(); counts.Total != 2 || counts.Usable != 1 || counts.Unsupported != 1 {
		t.Fatalf("pool counts = %+v", counts)
	}
}

// TestRestoreWithExcludedRecordDoesNotDeadlock is the regression for the
// restore path that holds pubMu while starting the network: persisting an
// engine exclusion must not re-acquire pubMu.
func TestRestoreWithExcludedRecordDoesNotDeadlock(t *testing.T) {
	st, paths := newServiceStore(t)
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.HTTPListen = freeLoopbackAddr(t)
		cfg.SOCKSListen = freeLoopbackAddr(t)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	bad := contract.Record{
		ID: "x-bad", DedupeKey: "sha256:bad", Protocol: contract.ProtocolVLESS,
		Server: "127.0.0.1", Port: 443, Credentials: contract.Credentials{UserID: strings.Repeat("z", 31)},
		Transport: contract.TransportRaw, Security: contract.SecurityNone, Status: contract.RecordUnknown,
	}
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Mode = contract.ModeTargeted
		state.PolicyVersion++
		state.Records = []contract.Record{bad}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ref, err := st.Backup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Mode = contract.ModeStopped
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	done := make(chan error, 1)
	go func() {
		_, restoreErr := svc.restore(context.Background(), ref.Path)
		done <- restoreErr
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("restore with an excluded record: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("restore deadlocked while persisting the engine exclusion")
	}
	snap := svc.Snapshot()
	if len(snap.State.Records) != 1 || snap.State.Records[0].Status != contract.RecordUnsupported {
		t.Fatalf("restored records = %+v", snap.State.Records)
	}
}

// TestImportRollbackSurvivesCanceledRequest: the commit can succeed and the
// request context can end before the reload; the rollback must still remove the
// record so the non-zero exit never persists it.
func TestImportRollbackSurvivesCanceledRequest(t *testing.T) {
	st, paths := newServiceStore(t)
	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	link := "vless://11111111-1111-4111-8111-111111111111@127.0.0.1:443?encryption=none&security=tls&type=tcp#canceled"
	ctx, cancel := context.WithCancel(context.Background())
	do := func() (imports.AddResult, error) {
		result, addErr := svc.refresher.AddLink(context.Background(), link)
		cancel()
		return result, addErr
	}
	if _, err := svc.addAndReload(ctx, do); err == nil {
		t.Fatal("import succeeded even though the request was canceled before the reload")
	}
	after, err := st.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.State.Records) != 0 || len(after.State.Sources) != 0 {
		t.Fatalf("canceled import persisted state: records=%+v sources=%+v", after.State.Records, after.State.Sources)
	}
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func portOf(t *testing.T, addr string) uint16 {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(n)
}

func TestListenerPortCollisionIsAClearFailure(t *testing.T) {
	st, paths := newServiceStore(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	socksAddr := freeLoopbackAddr(t)
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.HTTPListen = occupied.Addr().String()
		cfg.SOCKSListen = socksAddr
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.StartNetwork(context.Background())
	if err == nil {
		t.Fatal("a bind collision was accepted")
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.StateLocked {
		t.Fatalf("err = %v, want a conflict-kind failure", err)
	}
	if !strings.Contains(failure.Message, "could not bind") || !strings.Contains(failure.Message, "no fallback") {
		t.Fatalf("message is not actionable: %q", failure.Message)
	}
	if runes := len([]rune(failure.Message)); runes > contract.MaxNodeNameRunes {
		t.Fatalf("the message is %d runes; EnvelopeFor would truncate it at %d: %q", runes, contract.MaxNodeNameRunes, failure.Message)
	}
	_ = svc.Close(context.Background())
}

// TestListenerCollisionMessageSurvivesLongLoopbackLiterals covers the address
// shape that made the old message overflow the 120-rune display bound: a
// fully written IPv6 loopback literal is accepted by the config validator, so
// the actionable tail must not depend on the addresses' length.
func TestListenerCollisionMessageSurvivesLongLoopbackLiterals(t *testing.T) {
	st, paths := newServiceStore(t)
	occupied, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback is unavailable: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	long := func(p int) string {
		return fmt.Sprintf("[0000:0000:0000:0000:0000:0000:0000:0001]:%d", p)
	}
	socksProbe, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback is unavailable: %v", err)
	}
	socksPort := socksProbe.Addr().(*net.TCPAddr).Port
	_ = socksProbe.Close()
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.HTTPListen = long(port)
		cfg.SOCKSListen = long(socksPort)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.StartNetwork(context.Background())
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.StateLocked {
		t.Fatalf("err = %v, want a conflict-kind failure", err)
	}
	if !strings.Contains(failure.Message, "no fallback port exists") {
		t.Fatalf("message lost its actionable tail: len=%d %q", len([]rune(failure.Message)), failure.Message)
	}
	if runes := len([]rune(failure.Message)); runes > contract.MaxNodeNameRunes {
		t.Fatalf("the message is %d runes (%q); EnvelopeFor would truncate it at %d", runes, failure.Message, contract.MaxNodeNameRunes)
	}
	_ = svc.Close(context.Background())
}

func TestFreshProcessMayLeavePersistedStoppedMode(t *testing.T) {
	st, paths := newServiceStore(t)
	httpAddr := freeLoopbackAddr(t)
	socksAddr := freeLoopbackAddr(t)
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.HTTPListen = httpAddr
		cfg.SOCKSListen = socksAddr
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Mode = contract.ModeStopped
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if !svc.stopped || svc.stoppedByThisProcess {
		t.Fatalf("service state stopped=%v byProcess=%v", svc.stopped, svc.stoppedByThisProcess)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if _, err := svc.setMode(context.Background(), string(contract.ModeTargeted)); err != nil {
		t.Fatalf("fresh process could not leave stopped: %v", err)
	}
	if !svc.listenersOpen {
		t.Fatal("listeners did not start after leaving persisted stopped mode")
	}
	// In the same process, stopping again is final until a restart.
	if _, err := svc.setMode(context.Background(), string(contract.ModeStopped)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.setMode(context.Background(), string(contract.ModeTargeted)); err == nil {
		t.Fatal("same process restarted the engine after stopping")
	}
}

func TestHealthPublicationCannotRegressTheCommittedMode(t *testing.T) {
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 40; i++ {
			svc.applyMeasurement(probes.Measurement{Result: contract.ProbeResult{
				Kind: contract.ProbeTransport, Exit: "x-health", Outcome: contract.OutcomeSuccess,
			}})
		}
	}()
	for i := 0; i < 20; i++ {
		mode := contract.ModeDirectAll
		if i%2 == 1 {
			mode = contract.ModeProxyAll
		}
		if _, err := svc.setMode(context.Background(), string(mode)); err != nil {
			t.Fatal(err)
		}
	}
	<-done
	disk, err := st.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := svc.Snapshot()
	if got.State.Mode != disk.State.Mode || got.State.PolicyVersion != disk.State.PolicyVersion {
		t.Fatalf("in-memory snapshot regressed: memory %s/%d, disk %s/%d",
			got.State.Mode, got.State.PolicyVersion, disk.State.Mode, disk.State.PolicyVersion)
	}
	// A mode command must not report success from a stale "already in mode".
	if got.State.Mode == contract.ModeProxyAll {
		if _, err := svc.setMode(context.Background(), string(contract.ModeDirectAll)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestImportThatLosesAnExitClosesItsConnections(t *testing.T) {
	st, paths := newServiceStore(t)
	rec := contract.Record{
		ID: "x-old", DedupeKey: "sha256:old", Protocol: contract.ProtocolTrojan,
		Server: "example.com", Port: 443, Transport: contract.TransportRaw,
		Security: contract.SecurityTLS, Status: contract.RecordUnknown,
	}
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Records = []contract.Record{rec}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, st, paths)
	snap := svc.Snapshot()
	dst := contract.Destination{Kind: contract.DestDomain, Host: "approved.example", Port: 443}
	h, err := svc.reg.Register(contract.ConnSpec{
		ID: "c-exit", Kind: contract.ConnSOCKS5Connect, Destination: dst,
		Decision: contract.Decision{
			Mode: contract.ModeTargeted, Class: contract.ClassApproved, Route: contract.RoutePool,
			Reason: contract.ReasonApprovedRule, PolicyVersion: snap.State.PolicyVersion,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.SetExit(contract.ExitID(rec.ID))
	client, server := net.Pipe()
	defer server.Close()
	if err := h.SetUpstream(client); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Records = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.reloadAfterImport(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.Context().Err() == nil {
		t.Fatal("a connection on a removed exit survived an import")
	}
}

func TestStoppedServiceRejectsNetworkWork(t *testing.T) {
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	if _, err := svc.setMode(context.Background(), string(contract.ModeStopped)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Handle(context.Background(), Request{Op: OpPoolRefresh, All: true}); err == nil {
		t.Fatal("pool refresh ran while stopped")
	} else {
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Kind != contract.Stopped {
			t.Fatalf("refresh err = %v, want stopped", err)
		}
	}
	if _, err := svc.Handle(context.Background(), Request{Op: OpImport, Kind: "subscription", Input: "https://example.com/sub"}); err == nil {
		t.Fatal("subscription import ran while stopped")
	}
}

func rerankRecord(id string, order int) contract.Record {
	return contract.Record{
		ID: contract.RecordID(id), DedupeKey: "sha256:" + id, Status: contract.RecordUnknown, Order: order,
		Protocol: contract.ProtocolSOCKS5, Server: "127.0.0.1", Port: 1080,
		Transport: contract.TransportRaw, Security: contract.SecurityNone,
	}
}

func attachReranker(s *Service) *pool.Dialer {
	d := pool.NewDialer(func() (contract.Snapshot, bool) { return s.Snapshot(), true }, nil, s.now)
	s.reranker = d
	return d
}

func TestRerankAllUnknownPicksRankedExit(t *testing.T) {
	st, paths := newServiceStore(t)
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Records = []contract.Record{rerankRecord("x-a", 0), rerankRecord("x-b", 1)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, st, paths)
	attachReranker(svc)
	raw, err := svc.Handle(context.Background(), Request{Op: OpPoolRerank})
	if err != nil {
		t.Fatal(err)
	}
	data := raw.(RerankData)
	if data.SelectedBefore != "" || data.SelectedAfter != "x-a" {
		t.Fatalf("before=%q after=%q", data.SelectedBefore, data.SelectedAfter)
	}
	if data.PolicyVersion != svc.Snapshot().State.PolicyVersion {
		t.Fatalf("policy version = %d", data.PolicyVersion)
	}
	found := false
	for _, skip := range data.Skipped {
		if skip.Exit == "x-b" && skip.Reason == pool.RerankSkipUnknown {
			found = true
		}
	}
	if !found {
		t.Fatalf("skipped = %+v, want x-b/unknown", data.Skipped)
	}
}

func TestRerankPromotesHealthyExit(t *testing.T) {
	st, paths := newServiceStore(t)
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Records = []contract.Record{rerankRecord("x-a", 0), rerankRecord("x-b", 1)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, st, paths)
	attachReranker(svc)
	if _, err := svc.Handle(context.Background(), Request{Op: OpPoolRerank}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		for i := range state.Records {
			if state.Records[i].ID == "x-b" {
				state.Records[i].Status = contract.RecordHealthy
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.reloadAfterImport(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := svc.Handle(context.Background(), Request{Op: OpPoolRerank})
	if err != nil {
		t.Fatal(err)
	}
	data := raw.(RerankData)
	if data.SelectedBefore != "x-a" || data.SelectedAfter != "x-b" {
		t.Fatalf("before=%q after=%q, want x-a -> x-b", data.SelectedBefore, data.SelectedAfter)
	}
}

func TestRerankPinnedSelectionIsReportedUnchanged(t *testing.T) {
	st, paths := newServiceStore(t)
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Records = []contract.Record{rerankRecord("x-a", 0), rerankRecord("x-b", 1)}
		state.SelectedExit = "x-b"
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, st, paths)
	before, err := st.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := svc.Handle(context.Background(), Request{Op: OpPoolRerank})
	if err != nil {
		t.Fatal(err)
	}
	data := raw.(RerankData)
	if data.SelectedBefore != "x-b" || data.SelectedAfter != "x-b" {
		t.Fatalf("pinned rerank = %+v", data)
	}
	if len(data.Skipped) != 1 || data.Skipped[0].Reason != pool.RerankSkipPinned {
		t.Fatalf("skipped = %+v", data.Skipped)
	}
	after, err := st.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.State.SelectedExit != before.State.SelectedExit ||
		after.State.PolicyVersion != before.State.PolicyVersion ||
		!reflect.DeepEqual(after.State.Rules, before.State.Rules) {
		t.Fatal("rerank mutated the pinned selection, policy version or rules")
	}
}

func TestRerankNoUsableExitIsPoolUnavailable(t *testing.T) {
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	_, err := svc.Handle(context.Background(), Request{Op: OpPoolRerank})
	if err == nil {
		t.Fatal("rerank accepted an empty pool")
	}
	var opErr *OpError
	if !errors.As(err, &opErr) || opErr.Kind != contract.PoolUnavailable || opErr.Exit != contract.ExitPoolUnavailable {
		t.Fatalf("err = %v, want pool_unavailable/11", err)
	}
}

func TestRerankLeavesLiveTunnelAndEngineUntouched(t *testing.T) {
	st, paths := newServiceStore(t)
	httpAddr := freeLoopbackAddr(t)
	socksAddr := freeLoopbackAddr(t)
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.HTTPListen = httpAddr
		cfg.SOCKSListen = socksAddr
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Records = []contract.Record{rerankRecord("x-a", 0), rerankRecord("x-b", 1)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, st, paths)
	if err := svc.StartNetwork(context.Background()); err != nil {
		t.Fatalf("StartNetwork: %v", err)
	}
	defer svc.Close(context.Background())
	eng := svc.engine
	snap := svc.Snapshot()
	dst := contract.Destination{Kind: contract.DestDomain, Host: "approved.example", Port: 443}
	h, err := svc.reg.Register(contract.ConnSpec{
		ID: "c-live", Kind: contract.ConnSOCKS5Connect, Destination: dst,
		Decision: contract.Decision{
			Mode: contract.ModeTargeted, Class: contract.ClassApproved, Route: contract.RoutePool,
			Reason: contract.ReasonApprovedRule, PolicyVersion: snap.State.PolicyVersion,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	defer server.Close()
	if err := h.SetUpstream(client); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Handle(context.Background(), Request{Op: OpPoolRerank}); err != nil {
		t.Fatal(err)
	}
	if svc.engine != eng {
		t.Fatal("rerank replaced the engine instance")
	}
	if h.Context().Err() != nil {
		t.Fatal("rerank closed a live tunnel")
	}
	if len(svc.reg.Snapshot()) != 1 {
		t.Fatal("rerank removed a registered connection")
	}
	if svc.Snapshot().State.PolicyVersion != snap.State.PolicyVersion {
		t.Fatal("rerank advanced the policy version")
	}
}
