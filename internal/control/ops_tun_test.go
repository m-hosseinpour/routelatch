package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"routelatch/internal/contract"
	"routelatch/internal/egress"
	"routelatch/internal/store"
)

type fakeTun struct {
	mu         sync.Mutex
	status     contract.TunStatus
	statusErr  error
	enableErr  error
	disableErr error
	enables    int
	disables   int
	onEnable   func()
	onDisable  func()
	// disableKeepsState models a runtime whose Disable succeeds while a
	// follow-up status probe still reports a stale/unavailable state.
	disableKeepsState bool
	// enableTarget, when set, updates the reported SOCKS target on Enable. It
	// models a service whose configured listener is the current one.
	enableTarget string
}

func (f *fakeTun) Status(context.Context) (contract.TunStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.statusErr
}

func (f *fakeTun) Enable(context.Context) error {
	f.mu.Lock()
	f.enables++
	hook, err := f.onEnable, f.enableErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status.State == contract.TunOff || f.status.State == contract.TunUnavailable {
		f.status.State = contract.TunOn
		f.status.Helper = contract.TunHelperInstalled
		f.status.IPv6 = true
	}
	if f.enableTarget != "" {
		f.status.SocksAddress = f.enableTarget
	}
	return nil
}

func (f *fakeTun) Disable(context.Context) error {
	f.mu.Lock()
	f.disables++
	hook, err := f.onDisable, f.disableErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.disableKeepsState {
		f.status.State = contract.TunOff
	}
	return nil
}

func (f *fakeTun) counts() (enables, disables int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enables, f.disables
}

func (f *fakeTun) state() contract.TunState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status.State
}

func newTunService(t *testing.T, rt contract.TunRuntime) (*Service, *store.Store, store.Paths, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, paths := newServiceStore(t)
	svc, err := NewService(ctx, Options{
		Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt,
		Bypass:            egress.NewController(),
		PhysicalInterface: func() (string, error) { return "eth0", nil },
		TunBindingCheck:   func() error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc, st, paths, ctx
}

func tunWarningsOf(warnings []string) []string {
	var out []string
	for _, w := range warnings {
		switch w {
		case string(contract.TunWarningDNSDirect), string(contract.TunWarningUDPBlocked),
			string(contract.TunWarningHelperMissing), string(contract.TunWarningServiceUnavailable),
			string(contract.TunWarningDomainUnavailable), string(contract.TunWarningDesktopProxyOverlap),
			string(contract.TunWarningAllDirect), string(contract.TunWarningConflict):
			out = append(out, w)
		}
	}
	return out
}

func errorEnvelope(t *testing.T, op string, err error) contract.Envelope {
	t.Helper()
	if err == nil {
		t.Fatalf("%s succeeded, want an error", op)
	}
	return EnvelopeFor(CommandName(op), nil, err)
}

// TestTunWithoutRuntimeIsHonest pins the R16-B seam: no runtime means off when
// not requested, unavailable when the persisted wish is on, and enable refuses
// with unsupported/7 instead of pretending TUN is active.
func TestTunWithoutRuntimeIsHonest(t *testing.T) {
	svc, st, _, ctx := newTunService(t, nil)
	status, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun == nil || status.Tun.State != contract.TunOff || status.Tun.Desired {
		t.Fatalf("initial tun=%+v, want off and not desired", status.Tun)
	}
	if got := tunWarningsOf(status.Warnings); len(got) != 0 {
		t.Fatalf("off TUN emitted warnings %v", got)
	}

	if _, err := svc.tunEnable(ctx); err == nil {
		t.Fatal("tun enable without a runtime succeeded")
	} else {
		env := errorEnvelope(t, OpTunEnable, err)
		if env.Error.Kind != contract.Unsupported || ExitCodeForEnvelope(env) != contract.ExitUnsupported {
			t.Fatalf("enable error = %+v exit=%d, want unsupported/7", env.Error, ExitCodeForEnvelope(env))
		}
		if env.Error.Detail["reason"] != string(contract.TunReasonHelperUnavailable) {
			t.Fatalf("enable detail = %+v, want helper_unavailable", env.Error.Detail)
		}
	}

	status, err = svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun == nil || !status.Tun.Desired || status.Tun.State != contract.TunUnavailable {
		t.Fatalf("after failed enable tun=%+v, want desired+unavailable", status.Tun)
	}
	if status.Tun.LastError != contract.TunReasonHelperUnavailable {
		t.Fatalf("last_error=%q, want helper_unavailable", status.Tun.LastError)
	}
	got := tunWarningsOf(status.Warnings)
	want := []string{
		string(contract.TunWarningHelperMissing),
		string(contract.TunWarningServiceUnavailable),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("warnings=%v, want %v", got, want)
	}
	if snap, err := st.Load(ctx); err != nil || !snap.Config.TunEnabled {
		t.Fatalf("persisted tun_enabled=%v err=%v, want true", snap.Config.TunEnabled, err)
	}

	// Disable is always allowed and clears the explicit wish, even with no
	// runtime to talk to.
	if _, err := svc.tunDisable(ctx); err != nil {
		t.Fatalf("disable without runtime: %v", err)
	}
	status, err = svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun == nil || status.Tun.Desired || status.Tun.State != contract.TunOff {
		t.Fatalf("after disable tun=%+v, want off and not desired", status.Tun)
	}
}

// TestTunStatusErrorReportsUnverifiedHelper pins the conservative status-error
// fallback: a failed Status call proves nothing about the helper, so the frozen
// `missing` value means "no installed+verified helper", and the helper-missing
// warning stays suppressed because tun_service_unavailable already explains it
// (§10.2).
func TestTunStatusErrorReportsUnverifiedHelper(t *testing.T) {
	rt := &fakeTun{
		status:    contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperInstalled},
		statusErr: errors.New("control socket unavailable"),
	}
	svc, _, _, ctx := newTunService(t, rt)
	if _, err := svc.tunEnable(ctx); err != nil {
		t.Fatalf("enable: %v", err)
	}
	status, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun == nil || status.Tun.State != contract.TunUnavailable ||
		status.Tun.LastError != contract.TunReasonServiceUnavailable {
		t.Fatalf("tun=%+v, want unavailable/service_unavailable", status.Tun)
	}
	if status.Tun.Helper != contract.TunHelperMissing {
		t.Fatalf("helper=%q, want the documented conservative missing value", status.Tun.Helper)
	}
	got := tunWarningsOf(status.Warnings)
	want := []string{string(contract.TunWarningServiceUnavailable)}
	if !slices.Equal(got, want) {
		t.Fatalf("warnings=%v, want only service_unavailable", got)
	}
}

func TestTunStatusNormalizesRuntimeFields(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{
		State: contract.TunOn, Helper: contract.TunHelperInstalled,
		Interface: "rl-\x1b[31mtun0", LastError: contract.TunReason("secret.example"),
		DNS: contract.TunDNSMode("secret.example"), UDP: contract.TunUDPMode("1.1.1.1"),
		Detail: "fixed\x1b[2J text",
	}}
	svc, _, _, ctx := newTunService(t, rt)
	if _, err := svc.setTunDesiredLocked(ctx, true); err != nil {
		t.Fatal(err)
	}
	status, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun == nil {
		t.Fatal("status has no tun object")
	}
	if status.Tun.LastError != "" {
		t.Fatalf("unknown last_error kept: %q", status.Tun.LastError)
	}
	if strings.Contains(status.Tun.Interface, "\x1b") || status.Tun.Interface != "rl-tun0" {
		t.Fatalf("interface not sanitized: %q", status.Tun.Interface)
	}
	if status.Tun.DNS != contract.TunDNSDirect || status.Tun.UDP != contract.TunUDPBlocked {
		t.Fatalf("dns/udp=%q/%q, want the fixed direct/blocked values", status.Tun.DNS, status.Tun.UDP)
	}
	if strings.Contains(status.Tun.Detail, "\x1b") {
		t.Fatalf("detail not sanitized: %q", status.Tun.Detail)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"secret.example", "1.1.1.1"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("hostile runtime text reached status JSON: %s", raw)
		}
	}

	rt.mu.Lock()
	rt.status.LastError = contract.TunReasonConflict
	rt.mu.Unlock()
	status, err = svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun.LastError != contract.TunReasonConflict {
		t.Fatalf("known last_error=%q, want conflict", status.Tun.LastError)
	}
}

func TestTunEnableRejectsStoppedMode(t *testing.T) {
	st, paths := newServiceStore(t)
	ctx := context.Background()
	if _, err := st.Mutate(ctx, func(state *contract.State) error {
		state.Mode = contract.ModeStopped
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rt := &fakeTun{status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperInstalled}}
	svc, err := NewService(ctx, Options{Store: st, Paths: paths, LogOutput: io.Discard, Tun: rt})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	_, err = svc.tunEnable(ctx)
	env := errorEnvelope(t, OpTunEnable, err)
	if env.Error.Kind != contract.Stopped || ExitCodeForEnvelope(env) != contract.ExitNotRunning {
		t.Fatalf("stopped enable = %+v exit=%d, want stopped/3", env.Error, ExitCodeForEnvelope(env))
	}
	if snap := svc.Snapshot(); snap.Config.TunEnabled {
		t.Fatal("stopped enable persisted tun_enabled=true")
	}
	// Disable is still allowed in stopped mode.
	if _, err := svc.tunDisable(ctx); err != nil {
		t.Fatalf("stopped disable: %v", err)
	}
}

// TestTunRuntimeErrorsKeepFrozenKinds pins §10.3: no new ErrorKind or exit code;
// the safe reason travels in detail and a runtime message cannot leak.
func TestTunRuntimeErrorsKeepFrozenKinds(t *testing.T) {
	cases := []struct {
		name   string
		rtErr  error
		kind   contract.ErrorKind
		exit   contract.ExitCode
		reason contract.TunReason
	}{
		{
			name: "helper", rtErr: &contract.TunError{
				Kind: contract.Unsupported, Reason: contract.TunReasonHelperUnavailable,
				Message: "secret.example:443 unreachable",
			},
			kind: contract.Unsupported, exit: contract.ExitUnsupported,
			reason: contract.TunReasonHelperUnavailable,
		},
		{
			name: "platform", rtErr: &contract.TunError{
				Kind: contract.Unsupported, Reason: contract.TunReasonPlatformUnsupported,
				Message: "secret.example:443 unreachable",
			},
			kind: contract.Unsupported, exit: contract.ExitUnsupported,
			reason: contract.TunReasonPlatformUnsupported,
		},
		{
			name: "conflict", rtErr: &contract.TunError{
				Kind: contract.StateLocked, Reason: contract.TunReasonConflict,
				Message: "secret.example:443 unreachable",
			},
			kind: contract.StateLocked, exit: contract.ExitConflict,
			reason: contract.TunReasonConflict,
		},
		{
			name: "internal", rtErr: &contract.TunError{
				Kind: contract.ErrorKind("not-a-kind"), Reason: contract.TunReasonServiceUnavailable,
				Message: "secret.example:443 unreachable",
			},
			kind: contract.Internal, exit: contract.ExitError,
			reason: contract.TunReasonServiceUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &fakeTun{
				status:    contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperMissing},
				enableErr: tc.rtErr,
			}
			svc, st, _, ctx := newTunService(t, rt)
			_, err := svc.tunEnable(ctx)
			env := errorEnvelope(t, OpTunEnable, err)
			if env.Error.Kind != tc.kind || ExitCodeForEnvelope(env) != tc.exit {
				t.Fatalf("error = %+v exit=%d, want %s/%d", env.Error, ExitCodeForEnvelope(env), tc.kind, tc.exit)
			}
			if env.Error.Detail["reason"] != string(tc.reason) {
				t.Fatalf("detail = %+v, want reason %s", env.Error.Detail, tc.reason)
			}
			if strings.Contains(env.Error.Message, "secret.example") {
				t.Fatalf("runtime message leaked into the envelope: %q", env.Error.Message)
			}
			// The wish is persisted even when the runtime refuses, so the
			// missing helper stays visible after a restart.
			if snap, err := st.Load(ctx); err != nil || !snap.Config.TunEnabled {
				t.Fatalf("persisted tun_enabled=%v err=%v, want true", snap.Config.TunEnabled, err)
			}
		})
	}
}

// TestTunErrorRejectsUnknownReason pins §10.3's safe-reason rule: a runtime
// reason outside the allowlist never reaches detail or the message, and the
// unusable answer becomes the documented internal failure.
func TestTunErrorRejectsUnknownReason(t *testing.T) {
	rt := &fakeTun{
		status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperMissing},
		enableErr: &contract.TunError{
			Kind: contract.Unsupported, Reason: contract.TunReason("secret.example:443"),
			Message: "secret.example:443",
		},
	}
	svc, _, _, ctx := newTunService(t, rt)
	_, err := svc.tunEnable(ctx)
	env := errorEnvelope(t, OpTunEnable, err)
	if env.Error.Kind != contract.Internal || ExitCodeForEnvelope(env) != contract.ExitError {
		t.Fatalf("error = %+v exit=%d, want internal/1", env.Error, ExitCodeForEnvelope(env))
	}
	if _, ok := env.Error.Detail["reason"]; ok {
		t.Fatalf("detail = %+v, want no reason for an unknown runtime value", env.Error.Detail)
	}
	raw := env.Error.Message
	if strings.Contains(raw, "secret.example") {
		t.Fatalf("runtime reason leaked into the envelope: %q", raw)
	}
}

// TestTunDisablePersistenceFailureIsInternal pins §10.3: tun.disable documents
// only internal, so a config-store failure (here: corrupt JSON) must not escape
// as corrupt/10. The overlay is still closed before the preference write.
func TestTunDisablePersistenceFailureIsInternal(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperInstalled}}
	svc, _, paths, ctx := newTunService(t, rt)
	if _, err := svc.tunEnable(ctx); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := os.WriteFile(paths.Config, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := svc.tunDisable(ctx)
	env := errorEnvelope(t, OpTunDisable, err)
	if env.Error.Kind != contract.Internal || ExitCodeForEnvelope(env) != contract.ExitError {
		t.Fatalf("disable error = %+v exit=%d, want internal/1", env.Error, ExitCodeForEnvelope(env))
	}
	if !strings.Contains(env.Error.Message, "preference could not be saved") {
		t.Fatalf("disable message = %q, want the fixed persistence text", env.Error.Message)
	}
	if rt.state() != contract.TunOff {
		t.Fatalf("runtime state=%s, want the overlay closed before the failed write", rt.state())
	}
}

func TestTunDisableErrorsStayInternal(t *testing.T) {
	cases := []struct {
		name   string
		kind   contract.ErrorKind
		reason contract.TunReason
	}{
		{"conflict", contract.StateLocked, contract.TunReasonConflict},
		{"helper", contract.Unsupported, contract.TunReasonHelperUnavailable},
		{"stopped", contract.Stopped, contract.TunReasonServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &fakeTun{status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperInstalled}}
			svc, st, _, ctx := newTunService(t, rt)
			if _, err := svc.tunEnable(ctx); err != nil {
				t.Fatal(err)
			}
			rt.mu.Lock()
			rt.disableErr = &contract.TunError{
				Kind: tc.kind, Reason: tc.reason,
				Message: "secret.example:443",
			}
			rt.mu.Unlock()
			_, err := svc.tunDisable(ctx)
			env := errorEnvelope(t, OpTunDisable, err)
			if env.Error.Kind != contract.Internal || ExitCodeForEnvelope(env) != contract.ExitError {
				t.Fatalf("disable error = %+v exit=%d, want internal/1", env.Error, ExitCodeForEnvelope(env))
			}
			if env.Error.Detail["reason"] != string(tc.reason) {
				t.Fatalf("disable detail = %+v, want reason %s", env.Error.Detail, tc.reason)
			}
			if strings.Contains(env.Error.Message, "secret.example") {
				t.Fatalf("runtime message leaked: %q", env.Error.Message)
			}
			// A failed disable of proven active coverage must not claim off,
			// but the explicit wish is still cleared so status shows the
			// conflict instead of forgetting the user's request (§10.1).
			if snap, err := st.Load(ctx); err != nil || snap.Config.TunEnabled {
				t.Fatalf("persisted tun_enabled=%v err=%v, want false", snap.Config.TunEnabled, err)
			}
			status, _ := svc.tunStatusOp(ctx)
			if status.State == contract.TunOff {
				t.Fatalf("failed disable claimed off: %+v", status)
			}
			if status.Desired || status.LastError != contract.TunReasonConflict {
				t.Fatalf("failed disable status=%+v, want not desired with tun_conflict", status)
			}
			if !svc.bypass.Active() || !svc.tunCoverageUncertain {
				t.Fatal("active coverage was reported but the bypass was released")
			}
		})
	}
}

// TestTunDisableClearsWishWhenHelperMissing pins §10.1: `tun off` clears the
// preference even when no helper/service exists to talk to, and stays
// idempotent instead of returning internal. The same holds in stopped mode,
// where stopTunForModeStopped already skipped the missing runtime.
func TestTunDisableClearsWishWhenHelperMissing(t *testing.T) {
	missing := &contract.TunError{
		Kind: contract.Unsupported, Reason: contract.TunReasonHelperUnavailable,
		Err: os.ErrNotExist,
	}
	setup := func(t *testing.T, stopped bool) (*Service, *store.Store, *fakeTun, context.Context) {
		t.Helper()
		rt := &fakeTun{
			status:    contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperMissing},
			statusErr: missing,
			enableErr: missing,
		}
		svc, st, _, ctx := newTunService(t, rt)
		if _, err := svc.tunEnable(ctx); err == nil {
			t.Fatal("enable without a helper succeeded")
		}
		if snap, err := st.Load(ctx); err != nil || !snap.Config.TunEnabled {
			t.Fatalf("persisted tun_enabled=%v err=%v, want the enable wish", snap.Config.TunEnabled, err)
		}
		if stopped {
			if _, err := svc.setMode(ctx, "stopped"); err != nil {
				t.Fatalf("stop with a missing helper: %v", err)
			}
		}
		return svc, st, rt, ctx
	}

	for _, stopped := range []bool{false, true} {
		name := "running"
		if stopped {
			name = "stopped"
		}
		t.Run(name, func(t *testing.T) {
			svc, st, rt, ctx := setup(t, stopped)
			status, err := svc.tunDisable(ctx)
			if err != nil {
				t.Fatalf("disable with a missing helper: %v", err)
			}
			if status.State != contract.TunOff || status.Desired {
				t.Fatalf("disable status=%+v, want off and not desired", status)
			}
			if enables, disables := rt.counts(); enables != 1 || disables != 0 {
				t.Fatalf("runtime calls enable=%d disable=%d, want no disable attempt", enables, disables)
			}
			if snap, err := st.Load(ctx); err != nil || snap.Config.TunEnabled {
				t.Fatalf("persisted tun_enabled=%v err=%v, want false", snap.Config.TunEnabled, err)
			}
			if svc.bypass.Active() {
				t.Fatal("bypass stayed active although the missing helper proves coverage is off")
			}
			// A second off is idempotent and keeps the same answer.
			again, err := svc.tunDisable(ctx)
			if err != nil || again.State != contract.TunOff || again.Desired {
				t.Fatalf("second disable status=%+v err=%v, want off", again, err)
			}
		})
	}
}

// TestTunDisableUnavailableRuntimeKeepsBypass pins the fail-closed rule shared
// with clearTunBypassIfOff: an unresponsive, refused or `unavailable` helper
// proves nothing about coverage. `tun off` still clears the wish, but it must
// keep the engine bypass active, mark coverage uncertain, and report
// unavailable/tun_conflict instead of a false off (§10.2, §10.6).
func TestTunDisableUnavailableRuntimeKeepsBypass(t *testing.T) {
	unavailable := &contract.TunError{
		Kind: contract.Internal, Reason: contract.TunReasonServiceUnavailable,
		Err: errors.New("connection refused"),
	}
	cases := []struct {
		name     string
		probeErr error
		state    contract.TunState
	}{
		{"probe fails", unavailable, contract.TunOff},
		{"reports unavailable", nil, contract.TunUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &fakeTun{status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperInstalled}}
			svc, st, _, ctx := newTunService(t, rt)
			if _, err := svc.tunEnable(ctx); err != nil {
				t.Fatalf("enable: %v", err)
			}
			if !svc.bypass.Active() {
				t.Fatal("bypass did not activate with TUN")
			}
			rt.mu.Lock()
			rt.status.State = tc.state
			rt.statusErr = tc.probeErr
			rt.disableErr = unavailable
			rt.mu.Unlock()

			status, err := svc.tunDisable(ctx)
			if err != nil {
				t.Fatalf("disable with an unavailable runtime: %v", err)
			}
			if status.State != contract.TunUnavailable || status.Desired {
				t.Fatalf("disable status=%+v, want unavailable and not desired", status)
			}
			if status.LastError != contract.TunReasonConflict {
				t.Fatalf("disable status=%+v, want tun_conflict", status)
			}
			if !svc.bypass.Active() {
				t.Fatal("bypass was released although coverage could not be proven off")
			}
			if !svc.tunCoverageUncertain {
				t.Fatal("coverage was not marked uncertain after a failed disable")
			}
			if _, disables := rt.counts(); disables != 1 {
				t.Fatalf("disable calls=%d, want one attempted idempotent disable", disables)
			}
			if snap, err := st.Load(ctx); err != nil || snap.Config.TunEnabled {
				t.Fatalf("persisted tun_enabled=%v err=%v, want false", snap.Config.TunEnabled, err)
			}
		})
	}
}

// TestTunDisableSuccessfulDisableIsProofOff pins the §10.3 success result: a
// nil Disable proves no coverage survives, so `tun off` reports off, releases
// the bypass and clears the wish even when the status probe failed or is
// stale/unavailable.
func TestTunDisableSuccessfulDisableIsProofOff(t *testing.T) {
	probeErr := errors.New("status endpoint timed out")
	cases := []struct {
		name     string
		probeErr error
		state    contract.TunState
		stale    bool
	}{
		{"probe fails", probeErr, contract.TunOff, false},
		{"stale unavailable", nil, contract.TunUnavailable, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &fakeTun{status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperInstalled}}
			svc, st, _, ctx := newTunService(t, rt)
			if _, err := svc.tunEnable(ctx); err != nil {
				t.Fatalf("enable: %v", err)
			}
			rt.mu.Lock()
			rt.status.State = tc.state
			rt.statusErr = tc.probeErr
			rt.disableKeepsState = tc.stale
			rt.mu.Unlock()

			status, err := svc.tunDisable(ctx)
			if err != nil {
				t.Fatalf("disable with a successful runtime: %v", err)
			}
			if status.State != contract.TunOff || status.Desired ||
				status.LastError != "" || status.Detail != "" {
				t.Fatalf("disable status=%+v, want a plain off", status)
			}
			if svc.bypass.Active() || svc.tunCoverageUncertain {
				t.Fatal("bypass/uncertainty survived a successful disable")
			}
			if snap, err := st.Load(ctx); err != nil || snap.Config.TunEnabled {
				t.Fatalf("persisted tun_enabled=%v err=%v, want false", snap.Config.TunEnabled, err)
			}
		})
	}
}

// TestTunDisableActiveFailureWinsOverPersistFailure pins the ordering when both
// the overlay and the wish write fail: the safety-critical runtime error stays
// the reported one, and the unchanged snapshot keeps desired=true truthful.
func TestTunDisableActiveFailureWinsOverPersistFailure(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperInstalled}}
	svc, _, paths, ctx := newTunService(t, rt)
	if _, err := svc.tunEnable(ctx); err != nil {
		t.Fatalf("enable: %v", err)
	}
	rt.mu.Lock()
	rt.disableErr = &contract.TunError{Kind: contract.StateLocked, Reason: contract.TunReasonConflict}
	rt.mu.Unlock()
	if err := os.WriteFile(paths.Config, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, err := svc.tunDisable(ctx)
	env := errorEnvelope(t, OpTunDisable, err)
	if env.Error.Kind != contract.Internal || ExitCodeForEnvelope(env) != contract.ExitError {
		t.Fatalf("disable error = %+v exit=%d, want internal/1", env.Error, ExitCodeForEnvelope(env))
	}
	if env.Error.Detail["reason"] != string(contract.TunReasonConflict) {
		t.Fatalf("disable detail = %+v, want tun_conflict", env.Error.Detail)
	}
	if status.State != contract.TunOn || !status.Desired {
		t.Fatalf("disable status=%+v, want on with the wish retained after a failed write", status)
	}
	if !svc.bypass.Active() || !svc.tunCoverageUncertain {
		t.Fatal("active coverage was reported but the bypass was released")
	}
}

func TestTunEnableDisableIdempotent(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperInstalled}}
	svc, _, _, ctx := newTunService(t, rt)
	for i := 0; i < 2; i++ {
		status, err := svc.tunEnable(ctx)
		if err != nil {
			t.Fatalf("enable #%d: %v", i+1, err)
		}
		if status.State != contract.TunOn || !status.Desired {
			t.Fatalf("enable #%d status=%+v, want on+desired", i+1, status)
		}
	}
	for i := 0; i < 2; i++ {
		status, err := svc.tunDisable(ctx)
		if err != nil {
			t.Fatalf("disable #%d: %v", i+1, err)
		}
		if status.State != contract.TunOff || status.Desired {
			t.Fatalf("disable #%d status=%+v, want off and not desired", i+1, status)
		}
	}
	enables, disables := rt.counts()
	if enables != 2 || disables != 2 {
		t.Fatalf("runtime calls enable=%d disable=%d, want 2/2", enables, disables)
	}
}

// TestTunModeStoppedDisablesFirst pins §10.1 ordering and fail-closed stop.
func TestTunModeStoppedDisablesFirst(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{
		State: contract.TunOn, Helper: contract.TunHelperInstalled, IPv6: true,
	}}
	svc, _, _, ctx := newTunService(t, rt)
	if _, err := svc.tunEnable(ctx); err != nil {
		t.Fatal(err)
	}
	if !svc.Snapshot().Config.TunEnabled {
		t.Fatal("tun_enabled not published")
	}
	rt.mu.Lock()
	rt.onDisable = func() {
		if svc.Snapshot().State.Mode == contract.ModeStopped {
			t.Error("TUN was disabled after the mode was committed; ordering is wrong")
		}
	}
	rt.mu.Unlock()

	if _, err := svc.setMode(ctx, "stopped"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, disables := rt.counts(); disables != 1 {
		t.Fatalf("disable calls=%d, want 1", disables)
	}
	if rt.state() != contract.TunOff || !svc.Snapshot().Config.TunEnabled {
		t.Fatalf("state=%s desired=%v, want off with the preference retained",
			rt.state(), svc.Snapshot().Config.TunEnabled)
	}
	status, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Tun == nil || status.Tun.State != contract.TunOff || !status.Tun.Desired {
		t.Fatalf("stopped status tun=%+v, want off with desired retained", status.Tun)
	}
	if got := tunWarningsOf(status.Warnings); len(got) != 0 {
		t.Fatalf("stopped TUN emitted warnings %v", got)
	}
}

func TestTunModeStoppedFailsClosedWhenDisableFails(t *testing.T) {
	rt := &fakeTun{
		status: contract.TunStatus{State: contract.TunOn, Helper: contract.TunHelperInstalled},
		disableErr: &contract.TunError{
			Kind: contract.StateLocked, Reason: contract.TunReasonConflict,
			Message: "secret.example:443",
		},
	}
	svc, _, _, ctx := newTunService(t, rt)
	if _, err := svc.setMode(ctx, "stopped"); err == nil {
		t.Fatal("stop succeeded while TUN could not be disabled")
	} else {
		env := errorEnvelope(t, OpMode, err)
		if env.Error.Kind != contract.StateLocked || ExitCodeForEnvelope(env) != contract.ExitConflict {
			t.Fatalf("stop error = %+v exit=%d, want state_locked/6", env.Error, ExitCodeForEnvelope(env))
		}
		if env.Error.Detail["reason"] != string(contract.TunReasonConflict) {
			t.Fatalf("stop detail = %+v, want tun_conflict", env.Error.Detail)
		}
	}
	if svc.Snapshot().State.Mode == contract.ModeStopped {
		t.Fatal("state was committed stopped even though TUN disable failed")
	}
}

func TestTunModeStoppedSkipsDisableWhenAlreadyOff(t *testing.T) {
	rt := &fakeTun{
		status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperMissing},
		disableErr: &contract.TunError{
			Kind: contract.Unsupported, Reason: contract.TunReasonHelperUnavailable,
		},
	}
	svc, _, _, ctx := newTunService(t, rt)
	if _, err := svc.setMode(ctx, "stopped"); err != nil {
		t.Fatalf("stop with TUN off must not fail on a missing helper: %v", err)
	}
	if _, disables := rt.counts(); disables != 0 {
		t.Fatalf("disable calls=%d, want 0 when TUN already reports off", disables)
	}
	if svc.Snapshot().State.Mode != contract.ModeStopped {
		t.Fatal("mode was not committed stopped")
	}
}

// TestTunModeStoppedDisablesWhenUnavailable pins the fail-closed seam: only a
// positive `off` report may skip Disable. `unavailable` is not proof that no
// overlay exists, so it takes the Disable path, and a failure aborts the stop
// (§10.1, §10.3).
func TestTunModeStoppedDisablesWhenUnavailable(t *testing.T) {
	t.Run("disable succeeds", func(t *testing.T) {
		rt := &fakeTun{status: contract.TunStatus{
			State: contract.TunUnavailable, Helper: contract.TunHelperMissing,
		}}
		svc, _, _, ctx := newTunService(t, rt)
		if _, err := svc.setMode(ctx, "stopped"); err != nil {
			t.Fatalf("stop: %v", err)
		}
		if _, disables := rt.counts(); disables != 1 {
			t.Fatalf("disable calls=%d, want 1 when TUN reports unavailable", disables)
		}
		if svc.Snapshot().State.Mode != contract.ModeStopped {
			t.Fatal("mode was not committed stopped")
		}
	})

	t.Run("disable fails", func(t *testing.T) {
		rt := &fakeTun{
			status: contract.TunStatus{State: contract.TunUnavailable, Helper: contract.TunHelperMissing},
			disableErr: &contract.TunError{
				Kind: contract.Unsupported, Reason: contract.TunReasonHelperUnavailable,
				Message: "secret.example:443",
			},
		}
		svc, _, _, ctx := newTunService(t, rt)
		_, err := svc.setMode(ctx, "stopped")
		env := errorEnvelope(t, OpMode, err)
		if env.Error.Kind != contract.Unsupported || ExitCodeForEnvelope(env) != contract.ExitUnsupported {
			t.Fatalf("stop error = %+v exit=%d, want unsupported/7", env.Error, ExitCodeForEnvelope(env))
		}
		if env.Error.Detail["reason"] != string(contract.TunReasonHelperUnavailable) {
			t.Fatalf("stop detail = %+v, want tun_helper_unavailable", env.Error.Detail)
		}
		if svc.Snapshot().State.Mode == contract.ModeStopped {
			t.Fatal("state was committed stopped although unavailable TUN could not be disabled")
		}
	})
}

func TestTunModeStoppedDisablesWhenStatusFails(t *testing.T) {
	rt := &fakeTun{
		status:    contract.TunStatus{State: contract.TunOn, Helper: contract.TunHelperInstalled},
		statusErr: errors.New("status endpoint down"),
	}
	svc, _, _, ctx := newTunService(t, rt)
	if _, err := svc.setMode(ctx, "stopped"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, disables := rt.counts(); disables != 1 {
		t.Fatalf("disable calls=%d, want 1 after a failed status probe", disables)
	}
	if svc.Snapshot().State.Mode != contract.ModeStopped {
		t.Fatal("mode was not committed stopped")
	}
}

func TestTunWarningsOnlyWhenTruthful(t *testing.T) {
	dns := string(contract.TunWarningDNSDirect)
	udp := string(contract.TunWarningUDPBlocked)
	helper := string(contract.TunWarningHelperMissing)
	service := string(contract.TunWarningServiceUnavailable)
	desktop := string(contract.TunWarningDesktopProxyOverlap)
	allDirect := string(contract.TunWarningAllDirect)
	conflict := string(contract.TunWarningConflict)

	cases := []struct {
		name    string
		tun     *contract.TunStatus
		mode    contract.Mode
		desktop bool
		want    []string
	}{
		{
			name: "on", tun: &contract.TunStatus{Desired: true, State: contract.TunOn, Helper: contract.TunHelperInstalled},
			mode: contract.ModeTargeted, want: []string{dns, udp},
		},
		{
			name: "on-all-direct-overlap",
			tun:  &contract.TunStatus{Desired: true, State: contract.TunOn, Helper: contract.TunHelperInstalled},
			mode: contract.ModeDirectAll, desktop: true,
			want: []string{dns, udp, desktop, allDirect},
		},
		{
			name: "desired-unavailable",
			tun:  &contract.TunStatus{Desired: true, State: contract.TunUnavailable, Helper: contract.TunHelperMissing},
			mode: contract.ModeTargeted, want: []string{helper, service},
		},
		{
			name: "desired-off",
			tun:  &contract.TunStatus{Desired: true, State: contract.TunOff, Helper: contract.TunHelperIncompatible},
			mode: contract.ModeTargeted, want: []string{service},
		},
		{
			name: "conflict",
			tun: &contract.TunStatus{
				State: contract.TunUnavailable, Helper: contract.TunHelperInstalled,
				LastError: contract.TunReasonConflict,
			},
			mode: contract.ModeTargeted, want: []string{conflict},
		},
		{
			name: "stopped",
			tun:  &contract.TunStatus{Desired: true, State: contract.TunOff, Helper: contract.TunHelperMissing},
			mode: contract.ModeStopped,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tunWarnings(tc.tun, tc.mode, tc.desktop)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("warnings=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestStatusTunAdditiveJSONCompatibility(t *testing.T) {
	svc, _, _, ctx := newTunService(t, nil)
	status, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	env := EnvelopeFor("status", status, nil)
	if env.SchemaVersion != 1 {
		t.Fatalf("management schema_version=%d, TUN is additive in 1", env.SchemaVersion)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if tun, ok := decoded["tun"].(map[string]any); !ok || tun["state"] == nil {
		t.Fatalf("status JSON = %s, want a tun object", raw)
	}
	if decoded["ready"] != false {
		t.Fatalf("status ready=%v, TUN must not change ready semantics", decoded["ready"])
	}

	var old StatusData
	if err := json.Unmarshal([]byte(`{"mode":"targeted","ready":true,"listeners_open":true}`), &old); err != nil {
		t.Fatalf("old status payload without tun: %v", err)
	}
	if old.Tun != nil {
		t.Fatalf("old payload decoded tun=%+v, want nil/unknown", old.Tun)
	}
	if !old.Ready || !old.ListenersOpen {
		t.Fatalf("old payload ready=%v listeners=%v changed", old.Ready, old.ListenersOpen)
	}
}

func TestConfigTunEnabledAdditiveNoMigration(t *testing.T) {
	st, paths := newServiceStore(t)
	ctx := context.Background()
	old := []byte(`{"schema_version":1,"http_listen":"127.0.0.1:8317","socks_listen":"127.0.0.1:8318","log_level":"info","diagnostics":{"enabled":true,"queue_limit":32,"max_concurrent":1,"retention_days":30},"probes":{"targets":[],"timeout_ms":5000,"cooldown_base":30,"cooldown_max":600}}`)
	if err := os.WriteFile(paths.Config, old, 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := st.Load(ctx)
	if err != nil {
		t.Fatalf("load old config: %v", err)
	}
	if snap.Config.TunEnabled || snap.Config.SchemaVersion != 1 || snap.State.SchemaVersion != 1 {
		t.Fatalf("old config decoded cfg=%+v state schema=%d", snap.Config, snap.State.SchemaVersion)
	}
	if _, err := st.MutateConfig(ctx, func(cfg *contract.Config) error {
		cfg.TunEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	var encoded struct {
		SchemaVersion contract.SchemaVersion `json:"schema_version"`
		TunEnabled    *bool                  `json:"tun_enabled"`
	}
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	if encoded.SchemaVersion != 1 {
		t.Fatalf("config schema_version=%d, want 1 (no migration)", encoded.SchemaVersion)
	}
	if encoded.TunEnabled == nil || !*encoded.TunEnabled {
		t.Fatalf("config JSON = %s, want explicit tun_enabled=true", raw)
	}
}

func TestTunNeverPersistsRuntimeState(t *testing.T) {
	rt := &fakeTun{status: contract.TunStatus{State: contract.TunOff, Helper: contract.TunHelperInstalled}}
	svc, st, paths, ctx := newTunService(t, rt)
	if _, err := st.Mutate(ctx, func(state *contract.State) error {
		state.UpdatedAt = svc.now().UTC()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.tunEnable(ctx); err != nil {
		t.Fatal(err)
	}
	stateRaw, err := os.ReadFile(paths.State)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"tun_enabled", "rl-tun0", `"tun"`, "interface"} {
		if strings.Contains(string(stateRaw), banned) {
			t.Fatalf("state.json contains %q: %s", banned, stateRaw)
		}
	}
	cfg, err := st.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Config.TunEnabled {
		t.Fatal("config did not keep the explicit TUN preference")
	}
}
