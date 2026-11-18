package control

import (
	"context"
	"io"
	"sync"
	"testing"

	"routelatch/internal/contract"
	"routelatch/internal/engine"
)

// stubSidecar records what the engine asked it to run. It never starts a real
// process; these tests cover the control wiring, not the sing-box runtime.
type stubSidecar struct {
	mu      sync.Mutex
	exits   []contract.SidecarExit
	synced  int
	stopped bool
}

func (f *stubSidecar) Sync(_ context.Context, exits []contract.SidecarExit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exits = append([]contract.SidecarExit(nil), exits...)
	f.synced++
	return nil
}

func (f *stubSidecar) Handoff(string) (contract.SidecarHandoff, bool) {
	return contract.SidecarHandoff{Address: "127.0.0.1:1", Username: "u", Password: "p"}, true
}

func (f *stubSidecar) Healthy() bool { return true }

func (f *stubSidecar) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return nil
}

func (f *stubSidecar) snapshot() ([]contract.SidecarExit, int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contract.SidecarExit(nil), f.exits...), f.synced, f.stopped
}

func clearSidecarEnv(t *testing.T) {
	t.Helper()
	t.Setenv(sidecarExecutableEnv, "")
	t.Setenv(sidecarExecutableAlt, "")
	t.Setenv(sidecarExecutableAlt2, "")
}

func hysteria2TestRecord(id string) contract.Record {
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

func vlessTestRecord(id string) contract.Record {
	return contract.Record{
		ID:          contract.RecordID(id),
		DedupeKey:   "sha256:" + id,
		Protocol:    contract.ProtocolVLESS,
		Server:      "127.0.0.1",
		Port:        443,
		Credentials: contract.Credentials{UserID: "11111111-1111-4111-8111-111111111111"},
		Transport:   contract.TransportRaw,
		Security:    contract.SecurityNone,
		Status:      contract.RecordUnknown,
	}
}

func storeTestRecords(t *testing.T, st interface {
	Mutate(context.Context, func(*contract.State) error) (contract.Snapshot, error)
}, records ...contract.Record) {
	t.Helper()
	if _, err := st.Mutate(context.Background(), func(st *contract.State) error {
		st.Records = append(st.Records, records...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func freePortConfig(t *testing.T, st interface {
	MutateConfig(context.Context, func(*contract.Config) error) (contract.Snapshot, error)
}) {
	t.Helper()
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.HTTPListen = freeLoopbackAddr(t)
		cfg.SOCKSListen = freeLoopbackAddr(t)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestInjectedSidecarReachesEngine proves the real service passes its sidecar
// to the engine at StartNetwork, not just in engine unit tests.
func TestInjectedSidecarReachesEngine(t *testing.T) {
	clearSidecarEnv(t)
	st, paths := newServiceStore(t)
	freePortConfig(t, st)
	storeTestRecords(t, st, hysteria2TestRecord("x-hy-injected"))

	fake := &stubSidecar{}
	svc, err := NewService(context.Background(), Options{
		Store: st, Paths: paths, LogOutput: io.Discard, Sidecar: fake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.StartNetwork(context.Background()); err != nil {
		t.Fatalf("StartNetwork: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	if !svc.engine.HasExit("x-hy-injected") {
		t.Fatalf("hysteria2 exit was not registered; excluded=%+v", svc.engine.Excluded())
	}
	if ex := svc.engine.Excluded(); len(ex) != 0 {
		t.Fatalf("unexpected exclusions: %+v", ex)
	}
	exits, synced, _ := fake.snapshot()
	if synced == 0 || len(exits) != 1 || exits[0].Tag != "rl-x-hy-injected" ||
		exits[0].Server != "hy.example.com" || exits[0].Password != "auth-secret" {
		t.Fatalf("engine synced=%d exits=%+v", synced, exits)
	}
	_ = svc.Close(context.Background())
	if _, _, stopped := fake.snapshot(); !stopped {
		t.Fatal("sidecar was not stopped with the engine")
	}
}

// TestNoSidecarFailsClosedForHysteria2Only proves an absent executable does not
// start a sidecar and does not disturb the Xray path.
func TestNoSidecarFailsClosedForHysteria2Only(t *testing.T) {
	clearSidecarEnv(t)
	st, paths := newServiceStore(t)
	freePortConfig(t, st)
	storeTestRecords(t, st, hysteria2TestRecord("x-hy-absent"), vlessTestRecord("x-vless-ok"))

	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if svc.sidecar != nil {
		t.Fatal("a sidecar was configured with no executable path set")
	}
	if err := svc.StartNetwork(context.Background()); err != nil {
		t.Fatalf("StartNetwork: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	excluded := svc.engine.Excluded()
	if len(excluded) != 1 || excluded[0].ID != "x-hy-absent" ||
		excluded[0].Reason != engine.ReasonSidecarUnavailable {
		t.Fatalf("exclusions=%+v, want one sidecar_unavailable for x-hy-absent", excluded)
	}
	if !svc.engine.HasExit("x-vless-ok") || svc.engine.HasExit("x-hy-absent") {
		t.Fatalf("xray exit=%v hysteria2 exit=%v", svc.engine.HasExit("x-vless-ok"), svc.engine.HasExit("x-hy-absent"))
	}
	rows, err := svc.listPool(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range rows.Records {
		if string(rec.ID) == "x-hy-absent" && (rec.Status != contract.RecordUnsupported || rec.Reason != engine.ReasonSidecarUnavailable) {
			t.Fatalf("pool view for hysteria2 = %s/%q", rec.Status, rec.Reason)
		}
	}
}

// TestSidecarExecutableFromEnvironment proves the environment variable is the
// configuration seam an operator (or acceptance harness) can set.
func TestSidecarExecutableFromEnvironment(t *testing.T) {
	clearSidecarEnv(t)
	t.Setenv(sidecarExecutableEnv, "/nonexistent/sing-box")
	st, paths := newServiceStore(t)
	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if svc.sidecar == nil {
		t.Fatal("a configured sidecar executable did not produce a runtime")
	}
}
