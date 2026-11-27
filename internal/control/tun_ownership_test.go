package control

import (
	"context"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/egress"
)

// recordingSidecar records the TUN default interface even before any
// hysteria2 exit exists. This models the production sing-box runtime, which
// stores the value and uses it for the next Sync (docs/contracts.md §10.6).
type recordingSidecar struct {
	stubSidecar
	mu      sync.Mutex
	iface   string
	setters int
}

func (r *recordingSidecar) SetDefaultInterface(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.iface = name
	r.setters++
	return nil
}

func (r *recordingSidecar) defaultInterface() (string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.iface, r.setters
}

// TestSecondEngineDoesNotDisableLivePeerCoverage pins finding #1 and §10.4:
// a second --data-dir engine with a different target must not disable the
// first engine's live TUN at startup or Quit. It keeps its own dials outside
// coverage and reports tun_conflict instead.
func TestSecondEngineDoesNotDisableLivePeerCoverage(t *testing.T) {
	peer, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })

	rt := &fakeTun{status: contract.TunStatus{
		State: contract.TunOn, Helper: contract.TunHelperInstalled,
		SocksAddress: peer.Addr().String(),
	}}
	st, paths := newServiceStore(t)
	freePortConfig(t, st)
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.TunEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(context.Background(), Options{
		Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt,
		Bypass:            egress.NewController(),
		PhysicalInterface: func() (string, error) { return "eth0", nil },
		TunBindingCheck:   func() error { return nil },
		TunLinkPresent:    func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	if err := svc.StartNetwork(context.Background()); err != nil {
		t.Fatalf("second engine must not fail for a peer-owned overlay: %v", err)
	}
	if _, disables := rt.counts(); disables != 0 {
		t.Fatalf("second engine disabled the live peer overlay %d time(s)", disables)
	}
	if !svc.listenersOpen || !svc.bypass.Active() {
		t.Fatalf("second engine listeners=%v bypass=%v, want both active and loop-safe", svc.listenersOpen, svc.bypass.Active())
	}
	status, err := svc.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun == nil || status.Tun.LastError != contract.TunReasonConflict {
		t.Fatalf("tun=%+v, want tun_conflict", status.Tun)
	}
	if !slices.Contains(status.Warnings, string(contract.TunWarningConflict)) {
		t.Fatalf("warnings=%v, want tun_conflict", status.Warnings)
	}

	// An explicit off from this instance must refuse the live peer, clear this
	// instance's wish, and leave the peer TUN plus our bypass intact.
	if _, err := svc.tunDisable(context.Background()); err == nil {
		t.Fatal("tun off disabled a live peer overlay")
	} else if env := errorEnvelope(t, OpTunDisable, err); env.Error.Kind != contract.StateLocked ||
		ExitCodeForEnvelope(env) != contract.ExitConflict {
		t.Fatalf("tun off error=%+v exit=%d, want state_locked/6", env.Error, ExitCodeForEnvelope(env))
	}
	if snap, err := st.Load(context.Background()); err != nil || snap.Config.TunEnabled {
		t.Fatalf("persisted tun_enabled=%v err=%v, want false after explicit off", snap.Config.TunEnabled, err)
	}
	if _, disables := rt.counts(); disables != 0 || rt.state() != contract.TunOn {
		t.Fatalf("tun off disabled/changed the live peer overlay: disable=%d state=%s", disables, rt.state())
	}
	if !svc.bypass.Active() {
		t.Fatal("tun off released the engine bypass while peer coverage is live")
	}
	if conn, err := net.DialTimeout("tcp", peer.Addr().String(), time.Second); err != nil {
		t.Fatalf("peer listener was disturbed: %v", err)
	} else {
		_ = conn.Close()
	}

	if err := svc.Close(context.Background()); err != nil {
		t.Fatalf("second engine Quit: %v", err)
	}
	if _, disables := rt.counts(); disables != 0 {
		t.Fatalf("second engine disabled the live peer overlay on Quit (%d calls)", disables)
	}
}

// TestStoppedSecondEngineLeavesPeerCoverage pins the stopped-start variant of
// finding #1: a stopped instance serves no captured traffic and must not close
// another engine's live coverage.
func TestStoppedSecondEngineLeavesPeerCoverage(t *testing.T) {
	peer, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })

	st, paths := newServiceStore(t)
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Mode = contract.ModeStopped
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rt := &fakeTun{status: contract.TunStatus{
		State: contract.TunOn, Helper: contract.TunHelperInstalled,
		SocksAddress: peer.Addr().String(),
	}}
	svc, err := NewService(context.Background(), Options{
		Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt,
		TunLinkPresent: func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if err := svc.StartNetwork(context.Background()); err != nil {
		t.Fatalf("stopped second engine: %v", err)
	}
	if _, disables := rt.counts(); disables != 0 {
		t.Fatalf("stopped second engine disabled the peer overlay %d time(s)", disables)
	}
	status, err := svc.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun == nil || status.Tun.LastError != contract.TunReasonConflict {
		t.Fatalf("tun=%+v, want tun_conflict", status.Tun)
	}
}

// TestMissingSocketWithLinkPresentKeepsBypass pins finding #2: stopping only the
// socket while rl-tun0 survives is not proof of off. Off/startup paths must not
// release the bypass or claim a clean off. With the link absent the same missing
// socket is the documented missing-helper case.
func TestMissingSocketWithLinkPresentKeepsBypass(t *testing.T) {
	missing := func() error {
		return &contract.TunError{
			Kind: contract.Internal, Reason: contract.TunReasonServiceUnavailable,
			Err: os.ErrNotExist,
		}
	}
	disableMissing := func() error {
		return &contract.TunError{
			Kind: contract.Unsupported, Reason: contract.TunReasonHelperUnavailable,
			Err: os.ErrNotExist,
		}
	}

	for _, present := range []bool{false, true} {
		name := "link-absent"
		if present {
			name = "link-present"
		}
		t.Run(name, func(t *testing.T) {
			link := present
			rt := &fakeTun{
				status:     contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperMissing},
				statusErr:  missing(),
				enableErr:  disableMissing(),
				disableErr: disableMissing(),
			}
			svc, st, _, ctx := newTunService(t, rt)
			svc.tunLinkPresent = func() bool { return link }
			if _, err := svc.tunEnable(ctx); err == nil {
				t.Fatal("enable without a reachable helper succeeded")
			}
			if err := svc.prepareTunBypass(ctx); err != nil {
				t.Fatalf("prepare bypass: %v", err)
			}
			if !svc.bypass.Active() {
				t.Fatal("bypass did not activate")
			}

			if uncertain := svc.reconcileUnwantedTun(ctx); uncertain != present {
				t.Fatalf("reconcile uncertain=%v, want %v", uncertain, present)
			}
			cleared := svc.clearTunBypassIfOff(ctx)
			if cleared == present {
				t.Fatalf("clearTunBypassIfOff=%v with link present=%v, want the fail-closed answer", cleared, present)
			}
			if present && !svc.bypass.Active() {
				t.Fatal("missing socket with rl-tun0 present released the bypass")
			}
			status, err := svc.status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if present {
				if status.Tun == nil || status.Tun.State != contract.TunUnavailable ||
					status.Tun.LastError != contract.TunReasonConflict {
					t.Fatalf("tun=%+v, want unavailable/tun_conflict", status.Tun)
				}
			} else if status.Tun == nil || status.Tun.LastError == contract.TunReasonConflict {
				t.Fatalf("tun=%+v, want no link-witness conflict when rl-tun0 is absent", status.Tun)
			}

			// `tun off` with the link present must not claim off or clear the
			// bypass; with no link it is the documented missing-helper off.
			tunStatus, err := svc.tunDisable(ctx)
			if err != nil {
				t.Fatalf("disable: %v", err)
			}
			if present {
				if tunStatus.State == contract.TunOff || tunStatus.LastError != contract.TunReasonConflict {
					t.Fatalf("disable status=%+v, want non-off tun_conflict", tunStatus)
				}
				if !svc.bypass.Active() || !svc.tunCoverageUncertain {
					t.Fatal("coverage could not be verified off but the bypass was released")
				}
			} else {
				if tunStatus.State != contract.TunOff {
					t.Fatalf("disable status=%+v, want off", tunStatus)
				}
				if svc.bypass.Active() {
					t.Fatal("bypass stayed active although no link can hold coverage")
				}
			}
			if snap, err := st.Load(ctx); err != nil || snap.Config.TunEnabled {
				t.Fatalf("persisted tun_enabled=%v err=%v, want false", snap.Config.TunEnabled, err)
			}
		})
	}
}

// TestTunEnableStoresSidecarInterfaceWithoutExits pins finding #3: enabling
// coverage before any hysteria2 exit exists must still record the physical
// interface, so a later AddExit/Sync regenerates with route.default_interface.
func TestTunEnableStoresSidecarInterfaceWithoutExits(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperInstalled}}
	sidecar := &recordingSidecar{}
	st, paths := newServiceStore(t)
	svc, err := NewService(context.Background(), Options{
		Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt, Sidecar: sidecar,
		Bypass:            egress.NewController(),
		PhysicalInterface: func() (string, error) { return "eth0", nil },
		TunBindingCheck:   func() error { return nil },
		TunTargetLive:     func(string) bool { return false },
		TunLinkPresent:    func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if _, err := svc.tunEnable(context.Background()); err != nil {
		t.Fatalf("enable without sidecar exits: %v", err)
	}
	iface, setters := sidecar.defaultInterface()
	if iface != "eth0" || setters == 0 {
		t.Fatalf("sidecar interface=%q setters=%d, want eth0 stored at enable", iface, setters)
	}
	if svc.hasSidecarExits() {
		t.Fatal("test setup unexpectedly has sidecar exits")
	}
}
