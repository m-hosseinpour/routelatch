package trayui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"routelatch/internal/autostart"
	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/pool"
)

// ---------- fakes ----------

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type fakeNotifier struct {
	mu    sync.Mutex
	notes []Notification
}

func (n *fakeNotifier) Notify(v Notification) {
	n.mu.Lock()
	n.notes = append(n.notes, v)
	n.mu.Unlock()
}

func (n *fakeNotifier) all() []Notification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Notification(nil), n.notes...)
}

type fakeLifecycle struct {
	mu       sync.Mutex
	quits    int
	restarts int
	events   chan LifecycleEvent
	onQuit   func()
}

func newFakeLifecycle() *fakeLifecycle {
	return &fakeLifecycle{events: make(chan LifecycleEvent, 8)}
}

func (l *fakeLifecycle) Quit() error {
	l.mu.Lock()
	l.quits++
	hook := l.onQuit
	l.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (l *fakeLifecycle) RestartEngine() error {
	l.mu.Lock()
	l.restarts++
	l.mu.Unlock()
	return nil
}

func (l *fakeLifecycle) Events() <-chan LifecycleEvent { return l.events }

func (l *fakeLifecycle) counts() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.quits, l.restarts
}

type fakeAutostart struct {
	mu     sync.Mutex
	status autostart.Status
	err    error
}

func (a *fakeAutostart) Status(context.Context) (autostart.Status, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return autostart.Status{}, a.err
	}
	return a.status, nil
}

func (a *fakeAutostart) Enable(context.Context) (autostart.Status, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return autostart.Status{}, a.err
	}
	a.status.Enabled = true
	a.status.Managed = true
	return a.status, nil
}

func (a *fakeAutostart) Disable(context.Context) (autostart.Status, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return autostart.Status{}, a.err
	}
	a.status.Enabled = false
	a.status.Managed = false
	return a.status, nil
}

// ---------- fixture ----------

type fixture struct {
	status      map[string]any
	tun         contract.TunStatus
	tunOpResult *contract.TunStatus
	noTun       bool
	rules       []control.RuleView
	pool        []pool.Row
	counts      pool.Counts
	proposals   []control.ProposalView
	desktop     control.DesktopData
	config      contract.Config
	paths       control.ConfigPathData
	env         control.EnvData
	doctor      control.DoctorData
	backup      string
}

func newFixture() fixture {
	rows, counts := fixturePool(176)
	rules := fixtureRules(117)
	desktop := control.DesktopData{Environment: "GNOME", Supported: true, Applied: false, PreviousSet: false}
	cfg := contract.Config{
		HTTPListen: "127.0.0.1:18080", SOCKSListen: "127.0.0.1:18081", LogLevel: "info",
		Diagnostics: contract.DiagnosticsConfig{Enabled: true},
	}
	return fixture{
		status: map[string]any{
			"ready": true, "listeners_open": true, "mode": string(contract.ModeTargeted),
			"policy_version": 7, "selected_exit": "",
			"listeners":   map[string]any{"http": "127.0.0.1:18080", "socks": "127.0.0.1:18081"},
			"pool":        map[string]any{"total": counts.Total, "usable": counts.Usable, "healthy": counts.Healthy, "cooling": counts.Cooling},
			"connections": map[string]any{"open": 3, "dialing": 0, "proofs": map[string]any{"confirmed": 1, "accepted": 2}},
			"diagnostics": map[string]any{"enabled": true, "queued": 1},
			"warnings":    []string{"desktop proxy is not applied"},
		},
		rules: rules,
		pool:  rows, counts: counts,
		proposals: []control.ProposalView{
			{ID: "p-route-1", Kind: contract.ProposalRoute, Destination: contract.Destination{Kind: contract.DestDomain, Host: "routed.example", Port: 443}, Status: contract.ProposalPending, ExpiresAt: time.Date(2026, 11, 25, 13, 0, 0, 0, time.UTC)},
			{ID: "p-route-2", Kind: contract.ProposalRoute, Destination: contract.Destination{Kind: contract.DestDomain, Host: "other.example", Port: 443}, Status: contract.ProposalPending},
			{ID: "p-unroute-1", Kind: contract.ProposalUnroute, RuleID: "r-000", Destination: contract.Destination{Kind: contract.DestDomain, Host: "rule-000.example", Port: 443}, Status: contract.ProposalPending},
			{ID: "p-accepted", Kind: contract.ProposalRoute, Destination: contract.Destination{Kind: contract.DestDomain, Host: "accepted.example", Port: 443}, Status: contract.ProposalAccepted},
			{ID: "p-expired", Kind: contract.ProposalRoute, Destination: contract.Destination{Kind: contract.DestDomain, Host: "expired.example", Port: 443}, Status: contract.ProposalExpired},
		},
		desktop: desktop,
		config:  cfg,
		tun: contract.TunStatus{
			Desired: false, State: contract.TunOff, DNS: contract.TunDNSDirect,
			UDP: contract.TunUDPBlocked, Helper: contract.TunHelperInstalled,
		},
		paths: control.ConfigPathData{Config: "/data/config.json", State: "/data/state.json", Socket: "/data/run/control.sock"},
		env: control.EnvData{
			HTTPProxy: "http://127.0.0.1:18080", HTTPSProxy: "http://127.0.0.1:18080",
			ALLProxy: "socks5://127.0.0.1:18081", NoProxy: "localhost,127.0.0.1,::1",
		},
		doctor: control.DoctorData{Checks: []control.Check{
			{Name: "listeners", Status: "ok", Detail: "both loopback"},
			{Name: "pool", Status: "warn", Detail: "20 cooling records"},
		}},
		backup: "/data/backups/state.json",
	}
}

func fixtureRules(n int) []control.RuleView {
	out := make([]control.RuleView, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, control.RuleView{
			ID: contract.RuleID(fmt.Sprintf("r-%03d", i)), Kind: contract.RuleDomain,
			Host: fmt.Sprintf("rule-%03d.example", i), Origin: contract.RuleOriginUser,
			AddedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		})
	}
	return out
}

func fixturePool(n int) ([]pool.Row, pool.Counts) {
	rows := make([]pool.Row, 0, n)
	var c pool.Counts
	for i := 0; i < n; i++ {
		status := contract.RecordUnknown
		switch {
		case i < 100:
			status = contract.RecordHealthy
		case i < 140:
			status = contract.RecordUnknown
		case i < 160:
			status = contract.RecordCooling
		case i < 170:
			status = contract.RecordUnsupported
		default:
			status = contract.RecordInvalid
		}
		rows = append(rows, pool.Row{
			ID: contract.RecordID(fmt.Sprintf("x-%036d", i)), Status: status,
			DisplayName: fmt.Sprintf("exit %03d", i), Protocol: contract.ProtocolVLESS,
			Server: "exit.example", Port: 443, Transport: contract.TransportRaw,
		})
		c.Total++
		switch status {
		case contract.RecordHealthy:
			c.Healthy++
			c.Usable++
		case contract.RecordUnknown:
			c.Unknown++
			c.Usable++
		case contract.RecordCooling:
			c.Cooling++
		case contract.RecordUnsupported:
			c.Unsupported++
		case contract.RecordInvalid:
			c.Invalid++
		}
	}
	return rows, c
}

func poolZero() pool.Counts { return pool.Counts{} }

// fixtureCaller serves the fixture over the Caller boundary and records every
// request. gate can hold a chosen op until the test releases it.
type fixtureCaller struct {
	mu        sync.Mutex
	fx        fixture
	calls     []control.Request
	transport bool
	failOp    string
	failKind  contract.ErrorKind
	failMsg   string
	gateOp    string
	gate      chan struct{}
	entered   chan struct{}
}

func newFixtureCaller(fx fixture) *fixtureCaller {
	return &fixtureCaller{fx: fx}
}

func (f *fixtureCaller) Call(ctx context.Context, req control.Request) (contract.Envelope, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	transport, failOp, failKind, failMsg := f.transport, f.failOp, f.failKind, f.failMsg
	gateOp, gate, entered := f.gateOp, f.gate, f.entered
	f.mu.Unlock()

	if gate != nil && req.Op == gateOp {
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return contract.Envelope{}, ctx.Err()
		}
	}
	if transport {
		return contract.Envelope{}, errors.New("socket: connect: no such file")
	}
	if failOp == req.Op {
		return contract.Envelope{SchemaVersion: 1, OK: false, Command: req.Op, Error: &contract.ErrorBody{Kind: failKind, Message: failMsg}}, nil
	}
	return f.serve(req)
}

func (f *fixtureCaller) serve(req control.Request) (contract.Envelope, error) {
	f.mu.Lock()
	fx := f.fx
	f.mu.Unlock()
	ok := func(data any) (contract.Envelope, error) {
		return contract.Envelope{SchemaVersion: 1, OK: true, Command: control.CommandName(req.Op), Data: data}, nil
	}
	switch req.Op {
	case control.OpStatus:
		status := make(map[string]any, len(fx.status)+1)
		for k, v := range fx.status {
			status[k] = v
		}
		if !fx.noTun {
			status["tun"] = fx.tun
		}
		return ok(status)
	case control.OpTunStatus:
		return ok(fx.tun)
	case control.OpTunEnable, control.OpTunDisable:
		f.mu.Lock()
		st := f.fx.tun
		st.Desired = req.Op == control.OpTunEnable
		if st.Desired {
			st.State = contract.TunOn
		} else {
			st.State = contract.TunOff
		}
		if f.fx.tunOpResult != nil {
			st = *f.fx.tunOpResult
		}
		f.fx.tun = st
		f.mu.Unlock()
		return ok(st)
	case control.OpRulesList:
		return ok(control.RulesListData{Rules: fx.rules})
	case control.OpPoolList:
		limit, offset := req.Limit, req.Offset
		if limit <= 0 {
			limit = 100
		}
		if offset < 0 {
			offset = 0
		}
		if offset > len(fx.pool) {
			offset = len(fx.pool)
		}
		end := offset + limit
		if end > len(fx.pool) {
			end = len(fx.pool)
		}
		return ok(control.PoolListData{Records: fx.pool[offset:end], Counts: fx.counts, Truncated: end < len(fx.pool)})
	case control.OpProposalsList:
		return ok(control.ProposalsData{Proposals: fx.proposals})
	case control.OpDesktopStatus:
		return ok(fx.desktop)
	case control.OpConfigGet:
		return ok(control.ConfigData{Path: fx.paths.Config, Values: fx.config})
	case control.OpConfigPath:
		return ok(fx.paths)
	case control.OpEnv:
		return ok(fx.env)
	case control.OpDoctor:
		return ok(fx.doctor)
	case control.OpMode:
		return ok(control.ModeData{Mode: contract.Mode(req.Mode)})
	case control.OpProposalsClear:
		return ok(control.ProposalStatusData{Cleared: 2})
	case control.OpRulesRemove:
		return ok(control.RuleRemoveData{Removed: contract.RuleID(req.ID), Closed: 1})
	case control.OpPoolRemove:
		return ok(control.PoolRemoveData{Removed: contract.RecordID(req.ID), Closed: 0})
	case control.OpPoolTest:
		return ok(control.PoolTestData{Results: []control.ProbeTestResult{{Exit: contract.ExitID(req.ID), Outcome: contract.OutcomeSuccess}}})
	case control.OpDesktopSet, control.OpDesktopRestore:
		data := fx.desktop
		data.Applied = req.Op == control.OpDesktopSet
		return ok(data)
	case control.OpBackup:
		return ok(control.BackupData{Ref: contract.BackupRef{Path: fx.backup, SchemaVersion: 1}})
	case control.OpPoolRefresh, control.OpPoolSelect, control.OpPoolRerank,
		control.OpProposalsAccept, control.OpProposalsReject, control.OpProposalsRecheck:
		return ok(map[string]any{})
	default:
		return contract.Envelope{SchemaVersion: 1, OK: false, Command: req.Op,
			Error: &contract.ErrorBody{Kind: contract.Unsupported, Message: "unexpected test op"}}, nil
	}
}

func (f *fixtureCaller) requests() []control.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]control.Request(nil), f.calls...)
}

func (f *fixtureCaller) countOp(op string) int {
	n := 0
	for _, req := range f.requests() {
		if req.Op == op {
			n++
		}
	}
	return n
}

func (f *fixtureCaller) setTransport(v bool) {
	f.mu.Lock()
	f.transport = v
	f.mu.Unlock()
}

func (f *fixtureCaller) setFail(op string, kind contract.ErrorKind, msg string) {
	f.mu.Lock()
	f.failOp, f.failKind, f.failMsg = op, kind, msg
	f.mu.Unlock()
}

func (f *fixtureCaller) setDesktop(d control.DesktopData) {
	f.mu.Lock()
	f.fx.desktop = d
	f.mu.Unlock()
}

func (f *fixtureCaller) setTun(st contract.TunStatus) {
	f.mu.Lock()
	f.fx.tun = st
	f.mu.Unlock()
}

func (f *fixtureCaller) gateOn(op string) (entered chan struct{}, release func()) {
	gate := make(chan struct{})
	entered = make(chan struct{}, 1)
	f.mu.Lock()
	f.gateOp, f.gate, f.entered = op, gate, entered
	f.mu.Unlock()
	var once sync.Once
	return entered, func() { once.Do(func() { close(gate) }) }
}

type controllerSetup struct {
	ctl      *Controller
	caller   *fixtureCaller
	clock    *fakeClock
	notifier *fakeNotifier
	life     *fakeLifecycle
	auto     *fakeAutostart
}

func newSetup(t *testing.T, fx fixture, mutate func(*Options)) *controllerSetup {
	t.Helper()
	caller := newFixtureCaller(fx)
	clock := newFakeClock()
	notifier := &fakeNotifier{}
	life := newFakeLifecycle()
	auto := &fakeAutostart{status: autostart.Status{Enabled: false, Managed: false, TrayAvailable: true, PIDFDAvailable: true}}
	opts := Options{
		Caller: caller, Lifecycle: life, Notifier: notifier, Autostart: auto,
		Paths: Paths{Config: "/data/config.json", State: "/data/state.json", Socket: "/data/run/control.sock", Log: "/data/log/routelatch.log"},
		Now:   clock.Now,
	}
	if mutate != nil {
		mutate(&opts)
	}
	ctl, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctl.RefreshAll(context.Background()); err != nil {
		t.Fatalf("RefreshAll: %v", err)
	}
	return &controllerSetup{ctl: ctl, caller: caller, clock: clock, notifier: notifier, life: life, auto: auto}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func walk(n Node, fn func(Node)) {
	fn(n)
	for _, child := range n.Children {
		walk(child, fn)
	}
}

func findNode(root Node, key string) (Node, bool) {
	var found Node
	ok := false
	walk(root, func(n Node) {
		if !ok && n.Key == key {
			found, ok = n, true
		}
	})
	return found, ok
}

func allLabels(root Node) []string {
	var out []string
	walk(root, func(n Node) { out = append(out, n.Label) })
	return out
}

func containsControl(s string) bool {
	for _, r := range s {
		if r == 0x1b || r == 0x9b || r == 0x07 || r == 0x0d || r == 0x0a || r == 0x08 ||
			(r >= 0x00 && r <= 0x1f) || (r >= 0x80 && r <= 0x9f) ||
			(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) ||
			r == 0x200e || r == 0x200f || r == 0x061c {
			return true
		}
	}
	return false
}

// ---------- in-process management server ----------

type serverHandler struct {
	caller *fixtureCaller
}

func (h serverHandler) Handle(ctx context.Context, req control.Request) (any, error) {
	// Use the same fixture switch but return data or an error for EnvelopeFor.
	env, err := h.caller.Call(ctx, req)
	if err != nil {
		return nil, err
	}
	if !env.OK {
		return nil, &contract.Failure{Kind: env.Error.Kind, Path: contract.PathLocal, Op: req.Op, Message: env.Error.Message}
	}
	return env.Data, nil
}

func startServer(t *testing.T, caller *fixtureCaller) *control.Client {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rlui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	srv, err := control.NewServer(control.ServerOptions{
		Path: filepath.Join(dir, "control.sock"), Handler: serverHandler{caller: caller}, AllowStale: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return &control.Client{Path: srv.Path()}
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func controlStringContains(s, sub string) bool { return strings.Contains(s, sub) }
