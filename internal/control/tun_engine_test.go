package control

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"routelatch/internal/contract"
	"routelatch/internal/egress"
	"routelatch/internal/store"
)

func newTunEngineService(t *testing.T, rt contract.TunRuntime, tunEnabled bool) (*Service, *store.Store, store.Paths, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, paths := newServiceStore(t)
	if tunEnabled {
		if _, err := st.MutateConfig(ctx, func(cfg *contract.Config) error {
			cfg.TunEnabled = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := NewService(ctx, Options{
		Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt,
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
	return svc, st, paths, ctx
}

func TestTunStartupAdoptsHealthyCoverage(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{
		State: contract.TunOn, Helper: contract.TunHelperInstalled,
		SocksAddress: "127.0.0.1:8318",
	}}
	svc, _, _, ctx := newTunEngineService(t, rt, true)
	if err := svc.prepareTunBypass(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.startTunCoverageLocked(ctx); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if enables, disables := rt.counts(); enables != 0 || disables != 0 {
		t.Fatalf("healthy coverage was touched: enable=%d disable=%d", enables, disables)
	}
	if !svc.bypass.Active() || svc.bypass.Interface() != "eth0" {
		t.Fatalf("bypass=%v iface=%q, want active eth0", svc.bypass.Active(), svc.bypass.Interface())
	}
}

func TestTunStartupRejectsStaleTarget(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{
		State: contract.TunOn, Helper: contract.TunHelperInstalled,
		SocksAddress: "127.0.0.1:1111",
	}, enableTarget: "127.0.0.1:1111"}
	svc, _, _, ctx := newTunEngineService(t, rt, true)
	if err := svc.prepareTunBypass(ctx); err != nil {
		t.Fatal(err)
	}
	err := svc.startTunCoverageLocked(ctx)
	if err == nil {
		t.Fatal("stale target was accepted")
	}
	var opErr *OpError
	if !errors.As(err, &opErr) || opErr.Kind != contract.StateLocked {
		t.Fatalf("stale target error=%v, want state_locked", err)
	}
	if enables, disables := rt.counts(); enables != 1 || disables < 1 {
		t.Fatalf("enable=%d disable=%d, want a disable/re-enable attempt", enables, disables)
	}
}

func TestTunEnableRetargetsOnlyAfterDisable(t *testing.T) {
	rt := &fakeTun{
		status:       contract.TunStatus{State: contract.TunOn, Helper: contract.TunHelperInstalled, SocksAddress: "127.0.0.1:1111"},
		enableTarget: "127.0.0.1:8318",
	}
	svc, _, _, ctx := newTunEngineService(t, rt, false)
	if _, err := svc.tunEnable(ctx); err != nil {
		t.Fatalf("enable: %v", err)
	}
	enables, disables := rt.counts()
	if enables != 1 || disables != 1 {
		t.Fatalf("enable=%d disable=%d, want a disable then enable", enables, disables)
	}
	st, _ := rt.Status(ctx)
	if st.SocksAddress != "127.0.0.1:8318" {
		t.Fatalf("target=%q, want the current listener", st.SocksAddress)
	}
	if !svc.bypass.Active() {
		t.Fatal("bypass is not active while TUN is on")
	}
}

func TestTunDisableClearsBypassAndKeepsDialBehavior(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperInstalled}}
	svc, st, _, ctx := newTunEngineService(t, rt, false)
	if _, err := svc.tunEnable(ctx); err != nil {
		t.Fatal(err)
	}
	if !svc.bypass.Active() {
		t.Fatal("bypass did not activate with TUN")
	}
	if _, err := svc.tunDisable(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.bypass.Active() {
		t.Fatal("bypass stayed active after tun off")
	}
	if snap, err := st.Load(ctx); err != nil || snap.Config.TunEnabled {
		t.Fatalf("tun_enabled=%v err=%v, want false after explicit off", snap.Config.TunEnabled, err)
	}
}

func TestTunCloseDisablesFirstAndPreservesPreference(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{
		State: contract.TunOn, Helper: contract.TunHelperInstalled, SocksAddress: "127.0.0.1:8318",
	}}
	svc, st, _, ctx := newTunEngineService(t, rt, true)
	if err := svc.prepareTunBypass(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, disables := rt.counts(); disables != 1 {
		t.Fatalf("disable calls=%d, want 1", disables)
	}
	if svc.bypass.Active() {
		t.Fatal("bypass stayed active after close")
	}
	if snap, err := st.Load(ctx); err != nil || !snap.Config.TunEnabled {
		t.Fatalf("tun_enabled=%v err=%v, Quit must keep the preference", snap.Config.TunEnabled, err)
	}
}

func TestTunCloseAbortsOnDisableFailure(t *testing.T) {
	rt := &fakeTun{
		status:     contract.TunStatus{State: contract.TunOn, Helper: contract.TunHelperInstalled},
		disableErr: &contract.TunError{Kind: contract.StateLocked, Reason: contract.TunReasonConflict},
	}
	svc, _, _, ctx := newTunEngineService(t, rt, true)
	if err := svc.prepareTunBypass(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Close(ctx); err == nil {
		t.Fatal("close succeeded while TUN could not be disabled")
	}
	if !svc.bypass.Active() {
		t.Fatal("bypass was cleared although coverage may still be on")
	}
}

func TestTunModeSwitchDoesNotRestartCoverage(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{
		State: contract.TunOn, Helper: contract.TunHelperInstalled, SocksAddress: "127.0.0.1:8318",
	}}
	svc, _, _, ctx := newTunEngineService(t, rt, true)
	if err := svc.prepareTunBypass(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.startTunCoverageLocked(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.setMode(ctx, string(contract.ModeProxyAll)); err != nil {
		t.Fatal(err)
	}
	if enables, disables := rt.counts(); enables != 0 || disables != 0 {
		t.Fatalf("mode switch restarted TUN: enable=%d disable=%d", enables, disables)
	}
}

func TestTunStatusStaleTargetIsConflictWarning(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{
		State: contract.TunOn, Helper: contract.TunHelperInstalled, SocksAddress: "127.0.0.1:1111",
	}}
	svc, _, _, ctx := newTunEngineService(t, rt, true)
	status, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun == nil || status.Tun.LastError != contract.TunReasonConflict {
		t.Fatalf("tun=%+v, want conflict for a stale target", status.Tun)
	}
	if !slices.Contains(status.Warnings, string(contract.TunWarningConflict)) {
		t.Fatalf("warnings=%v, want tun_conflict", status.Warnings)
	}
}

func TestTunBypassUnsupportedIsTruthful(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperInstalled}}
	svc, _, _, ctx := newTunEngineService(t, rt, false)
	svc.bindingCheck = func() error { return errors.New("kernel 5.6") }
	_, err := svc.tunEnable(ctx)
	if err == nil {
		t.Fatal("enable succeeded without a usable kernel")
	}
	env := errorEnvelope(t, OpTunEnable, err)
	if env.Error.Kind != contract.Unsupported || env.Error.Detail["reason"] != string(contract.TunReasonPlatformUnsupported) {
		t.Fatalf("enable error=%+v, want unsupported/platform", env.Error)
	}
	status, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun == nil || status.Tun.LastError != contract.TunReasonPlatformUnsupported {
		t.Fatalf("tun=%+v, want platform_unsupported", status.Tun)
	}
	if _, enables := rt.counts(); enables != 0 {
		t.Fatalf("runtime enable calls=%d, want 0", enables)
	}
}

func TestTunStoppedStartClosesCrashCoverage(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{State: contract.TunOn, Helper: contract.TunHelperInstalled}}
	svc, _, _, ctx := newTunEngineService(t, rt, true)
	svc.stopped = true
	svc.stopTunForStoppedStart(ctx)
	if _, disables := rt.counts(); disables != 1 {
		t.Fatalf("disable calls=%d, want 1 for crash-left coverage", disables)
	}
	if rt.state() != contract.TunOff {
		t.Fatalf("state=%s, want off", rt.state())
	}
}

func TestTunUnwantedCoverageIsClosed(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{State: contract.TunOn, Helper: contract.TunHelperInstalled}}
	svc, _, _, ctx := newTunEngineService(t, rt, false)
	if uncertain := svc.reconcileUnwantedTun(ctx); uncertain {
		t.Fatal("coverage was reported uncertain after a successful disable")
	}
	if _, disables := rt.counts(); disables != 1 {
		t.Fatalf("disable calls=%d, want 1 when coverage is not desired", disables)
	}
}

func TestRestoreToStoppedDisablesCoverageFirst(t *testing.T) {
	ctx := context.Background()
	st, paths := newServiceStore(t)
	if _, err := st.Mutate(ctx, func(state *contract.State) error {
		state.Mode = contract.ModeStopped
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ref, err := st.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Mutate(ctx, func(state *contract.State) error {
		state.Mode = contract.ModeTargeted
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rt := &fakeTun{status: contract.TunStatus{State: contract.TunOn, Helper: contract.TunHelperInstalled}}
	svc, err := NewService(ctx, Options{
		Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt,
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
	if _, err := svc.restore(ctx, ref.Path); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, disables := rt.counts(); disables != 1 {
		t.Fatalf("disable calls=%d, want 1 before a stopped restore", disables)
	}
	if svc.Snapshot().State.Mode != contract.ModeStopped {
		t.Fatal("restored state is not stopped")
	}
}

func TestRestoreToStoppedAbortsWhenDisableFails(t *testing.T) {
	ctx := context.Background()
	st, paths := newServiceStore(t)
	if _, err := st.Mutate(ctx, func(state *contract.State) error {
		state.Mode = contract.ModeStopped
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ref, err := st.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Mutate(ctx, func(state *contract.State) error {
		state.Mode = contract.ModeTargeted
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rt := &fakeTun{
		status:     contract.TunStatus{State: contract.TunOn, Helper: contract.TunHelperInstalled},
		disableErr: &contract.TunError{Kind: contract.StateLocked, Reason: contract.TunReasonConflict},
	}
	svc, err := NewService(ctx, Options{
		Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt,
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
	if _, err := svc.restore(ctx, ref.Path); err == nil {
		t.Fatal("restore succeeded while TUN could not be disabled")
	}
	if svc.Snapshot().State.Mode == contract.ModeStopped {
		t.Fatal("stopped state was committed after a failed disable")
	}
}

func tunDoctorCheck(t *testing.T, data DoctorData) Check {
	t.Helper()
	for _, c := range data.Checks {
		if c.Name == "tun" {
			return c
		}
	}
	t.Fatalf("doctor has no tun check: %+v", data.Checks)
	return Check{}
}

// TestTunStartupKeepsBypassWhenCrashCoverageCannotClose pins the no-loop
// guarantee for finding R16-D #2: a crash-left TUN that cannot be disabled does
// not block the normal proxy; the egress bypass stays active so engine dials
// cannot re-enter coverage, and listeners open with a truthful warning.
func TestTunStartupKeepsBypassWhenCrashCoverageCannotClose(t *testing.T) {
	st, paths := newServiceStore(t)
	freePortConfig(t, st)
	rt := &fakeTun{
		status:     contract.TunStatus{State: contract.TunOn, Helper: contract.TunHelperInstalled},
		disableErr: &contract.TunError{Kind: contract.StateLocked, Reason: contract.TunReasonConflict},
	}
	svc, err := NewService(context.Background(), Options{
		Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt,
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
	if err := svc.StartNetwork(context.Background()); err != nil {
		t.Fatalf("proxy failed to start because crash-left TUN could not be closed: %v", err)
	}
	if !svc.listenersOpen {
		t.Fatal("traffic listeners stayed closed despite a usable bypass")
	}
	if !svc.bypass.Active() {
		t.Fatal("egress bypass is inactive while crash-left coverage may still capture dials")
	}
	status, err := svc.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun == nil || status.Tun.State != contract.TunOn || status.Tun.LastError != contract.TunReasonConflict {
		t.Fatalf("tun=%+v, want on with an unwanted-coverage conflict", status.Tun)
	}
	if !slices.Contains(status.Warnings, string(contract.TunWarningConflict)) {
		t.Fatalf("warnings=%v, want tun_conflict", status.Warnings)
	}
	doc, err := svc.doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	check := tunDoctorCheck(t, doc)
	if check.Status != "warn" || !strings.Contains(check.Detail, "not requested") {
		t.Fatalf("doctor tun=%+v, want a warning about unwanted coverage", check)
	}
}

// TestTunStartupAbortsOnlyWhenNoBypassIsPossible pins the narrow abort: if
// coverage may still be active and the platform cannot bind the engine's own
// dials, opening listeners would create the forbidden unbound loop.
func TestTunStartupAbortsOnlyWhenNoBypassIsPossible(t *testing.T) {
	st, paths := newServiceStore(t)
	freePortConfig(t, st)
	rt := &fakeTun{
		status:     contract.TunStatus{State: contract.TunOn, Helper: contract.TunHelperInstalled},
		disableErr: &contract.TunError{Kind: contract.StateLocked, Reason: contract.TunReasonConflict},
	}
	svc, err := NewService(context.Background(), Options{
		Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt,
		Bypass:            egress.NewController(),
		PhysicalInterface: func() (string, error) { return "eth0", nil },
		TunBindingCheck:   func() error { return errors.New("kernel 5.6") },
		TunTargetLive:     func(string) bool { return false },
		TunLinkPresent:    func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	err = svc.StartNetwork(context.Background())
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.StateLocked {
		t.Fatalf("startup err=%v, want state_locked", err)
	}
	if svc.listenersOpen {
		t.Fatal("traffic listeners opened into a possible unbound TUN loop")
	}
}

// TestTunUnreachableHelperDoesNotFailProxy pins finding D confirmation #1: a
// missing/wedged helper with no positive active coverage warns and leaves TUN
// off instead of failing the normal proxy.
func TestTunUnreachableHelperDoesNotFailProxy(t *testing.T) {
	st, paths := newServiceStore(t)
	freePortConfig(t, st)
	rt := &fakeTun{
		statusErr:  errors.New("control socket wedged"),
		disableErr: &contract.TunError{Kind: contract.Internal},
	}
	svc, err := NewService(context.Background(), Options{
		Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt,
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
	if err := svc.StartNetwork(context.Background()); err != nil {
		t.Fatalf("unreachable helper failed the proxy: %v", err)
	}
	if !svc.listenersOpen {
		t.Fatal("traffic listeners stayed closed for an unreachable helper")
	}
	if !svc.bypass.Active() {
		t.Fatal("egress bypass is inactive although coverage is unverified")
	}
	status, err := svc.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun == nil || status.Tun.LastError != contract.TunReasonConflict {
		t.Fatalf("tun=%+v, want an unverified-coverage conflict warning", status.Tun)
	}
	if !slices.Contains(status.Warnings, string(contract.TunWarningConflict)) {
		t.Fatalf("warnings=%v, want tun_conflict", status.Warnings)
	}
	doc, err := svc.doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	check := tunDoctorCheck(t, doc)
	if check.Status != "warn" || !strings.Contains(check.Detail, "could not be verified off") {
		t.Fatalf("doctor tun=%+v, want an unverified-coverage warning", check)
	}
}

// TestTunStoppedStartAbortsOnDisableFailure pins the contract exception: a
// stopped start has no listener or engine to serve captured traffic, so an
// active coverage that cannot be closed aborts readiness.
func TestTunStoppedStartAbortsOnDisableFailure(t *testing.T) {
	st, paths := newServiceStore(t)
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Mode = contract.ModeStopped
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rt := &fakeTun{
		status:     contract.TunStatus{State: contract.TunOn, Helper: contract.TunHelperInstalled},
		disableErr: &contract.TunError{Kind: contract.StateLocked, Reason: contract.TunReasonConflict},
	}
	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt,
		TunTargetLive: func(string) bool { return false }, TunLinkPresent: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	startErr := svc.StartNetwork(context.Background())
	var opErr *OpError
	if !errors.As(startErr, &opErr) || opErr.Kind != contract.StateLocked {
		t.Fatalf("stopped start err=%v, want state_locked", startErr)
	}
	env := errorEnvelope(t, OpMode, startErr)
	if ExitCodeForEnvelope(env) != contract.ExitConflict {
		t.Fatalf("stopped start exit=%d, want conflict/6", ExitCodeForEnvelope(env))
	}
	if svc.listenersOpen {
		t.Fatal("stopped start opened listeners")
	}
}

// TestTunStartupFailureKeepsStableReason pins finding R16-D #3: a stale-target
// disable/re-enable must not end with an empty last_error or a "reinstall the
// helper" hint.
func TestTunStartupFailureKeepsStableReason(t *testing.T) {
	st, paths := newServiceStore(t)
	freePortConfig(t, st)
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.TunEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rt := &fakeTun{
		status:       contract.TunStatus{State: contract.TunOn, Helper: contract.TunHelperInstalled, SocksAddress: "127.0.0.1:1111"},
		enableTarget: "127.0.0.1:1111",
	}
	svc, err := NewService(context.Background(), Options{
		Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt,
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
	if err := svc.StartNetwork(context.Background()); err != nil {
		t.Fatalf("start network: %v", err)
	}
	if !svc.listenersOpen {
		t.Fatal("desired TUN start failure must not close the normal proxy")
	}
	status, err := svc.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun == nil || status.Tun.LastError != contract.TunReasonConflict {
		t.Fatalf("tun=%+v, want the stale-target conflict reason", status.Tun)
	}
	if !slices.Contains(status.Warnings, string(contract.TunWarningConflict)) {
		t.Fatalf("warnings=%v, want tun_conflict", status.Warnings)
	}
	doc, err := svc.doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	check := tunDoctorCheck(t, doc)
	if check.Status != "warn" || strings.Contains(check.Fix, "reinstall") || !strings.Contains(check.Fix, "re-enable") {
		t.Fatalf("doctor tun=%+v, want the stale-target fix, not a helper reinstall", check)
	}
}

// TestTunDoctorStoppedReportsActualState pins finding R16-D #4: doctor must not
// claim coverage is off in stopped mode when the runtime says otherwise.
func TestTunDoctorStoppedReportsActualState(t *testing.T) {
	cases := []struct {
		name      string
		status    contract.TunStatus
		statusErr error
		want      string
		mustSay   string
	}{
		{name: "off", status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperInstalled}, want: "ok", mustSay: "coverage is off"},
		{name: "on", status: contract.TunStatus{State: contract.TunOn, Helper: contract.TunHelperInstalled}, want: "warn", mustSay: "still on"},
		{name: "unavailable", statusErr: errors.New("service down"), want: "warn", mustSay: "could not be verified off"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, paths := newServiceStore(t)
			if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
				state.Mode = contract.ModeStopped
				state.PolicyVersion++
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if tc.statusErr != nil {
				if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
					cfg.TunEnabled = true
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			rt := &fakeTun{status: tc.status, statusErr: tc.statusErr}
			svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt,
				TunTargetLive: func(string) bool { return false }, TunLinkPresent: func() bool { return false }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = svc.Close(context.Background()) })
			doc, err := svc.doctor(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			check := tunDoctorCheck(t, doc)
			if check.Status != tc.want || !strings.Contains(check.Detail, tc.mustSay) {
				t.Fatalf("doctor tun=%+v, want %s mentioning %q", check, tc.want, tc.mustSay)
			}
		})
	}
}

// TestTunSidecarLoopRiskKeepsStableReason pins finding R16-D #7: a sidecar that
// cannot bind the physical interface refuses coverage with the documented
// sidecar_loop_risk reason instead of a bare unsupported.
func TestTunSidecarLoopRiskKeepsStableReason(t *testing.T) {
	st, paths := newServiceStore(t)
	freePortConfig(t, st)
	storeTestRecords(t, st, hysteria2TestRecord("x-hy-loop-risk"))
	rt := &fakeTun{status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperInstalled}}
	svc, err := NewService(context.Background(), Options{
		Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt, Sidecar: &stubSidecar{},
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
	if _, err := svc.tunEnable(context.Background()); err == nil {
		t.Fatal("enable succeeded with a sidecar that cannot bind the interface")
	} else {
		env := errorEnvelope(t, OpTunEnable, err)
		if env.Error.Detail["reason"] != string(contract.TunReasonSidecarLoopRisk) {
			t.Fatalf("enable err=%T %v detail=%+v, want sidecar_loop_risk", err, err, env.Error.Detail)
		}
	}
	status, err := svc.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun == nil || status.Tun.LastError != contract.TunReasonSidecarLoopRisk {
		t.Fatalf("tun=%+v, want sidecar_loop_risk", status.Tun)
	}
}
