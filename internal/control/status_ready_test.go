package control

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"routelatch/internal/contract"
)

func freePair(t *testing.T) (string, string) {
	t.Helper()
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	return first.Addr().String(), second.Addr().String()
}

func configuredService(t *testing.T) (*Service, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, paths := newServiceStore(t)
	httpAddr, socksAddr := freePair(t)
	if _, err := st.MutateConfig(ctx, func(cfg *contract.Config) error {
		cfg.HTTPListen, cfg.SOCKSListen = httpAddr, socksAddr
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, st, paths)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc, ctx
}

// TestStatusReadyTracksManagementAndListeners pins §7.1: status answers before
// StartNetwork, so ready must stay false until both listeners are bound, and a
// stopped process is ready with listeners_open=false.
func TestStatusReadyTracksManagementAndListeners(t *testing.T) {
	svc, ctx := configuredService(t)
	before, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Ready || before.ListenersOpen {
		t.Fatalf("before StartNetwork: ready=%v listeners_open=%v", before.Ready, before.ListenersOpen)
	}
	if err := svc.StartNetwork(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Ready || !after.ListenersOpen {
		t.Fatalf("after StartNetwork: ready=%v listeners_open=%v", after.Ready, after.ListenersOpen)
	}
	raw, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["ready"] != true || decoded["listeners_open"] != true {
		t.Fatalf("status JSON = %s, want ready/listeners_open true", raw)
	}
}

func TestStatusReadyInStoppedModeHasNoListener(t *testing.T) {
	ctx := context.Background()
	st, paths := newServiceStore(t)
	httpAddr, socksAddr := freePair(t)
	if _, err := st.MutateConfig(ctx, func(cfg *contract.Config) error {
		cfg.HTTPListen, cfg.SOCKSListen = httpAddr, socksAddr
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Mutate(ctx, func(state *contract.State) error {
		state.Mode = contract.ModeStopped
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, st, paths)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if err := svc.StartNetwork(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.ListenersOpen {
		t.Fatalf("stopped mode: ready=%v listeners_open=%v", status.Ready, status.ListenersOpen)
	}
	for _, addr := range []string{httpAddr, socksAddr} {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			_ = conn.Close()
			t.Fatalf("stopped mode bound %s", addr)
		}
	}
}
