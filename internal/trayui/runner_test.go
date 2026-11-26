package trayui

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"routelatch/internal/autostart"
	"routelatch/internal/contract"
	"routelatch/internal/control"
)

// ---------- manual clock/ticker ----------

type manualTicker struct {
	ch       chan time.Time
	interval time.Duration
	mu       sync.Mutex
	stopped  bool
}

func (t *manualTicker) C() <-chan time.Time { return t.ch }

func (t *manualTicker) Stop() {
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
}

func (t *manualTicker) send(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return false
	}
	select {
	case t.ch <- now:
		return true
	default:
		return false
	}
}

func (t *manualTicker) isStopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

type tickerFactory struct {
	mu      sync.Mutex
	tickers []*manualTicker
}

func newTickerFactory() *tickerFactory { return &tickerFactory{} }

func (f *tickerFactory) create(d time.Duration) Ticker {
	t := &manualTicker{ch: make(chan time.Time, 1), interval: d}
	f.mu.Lock()
	f.tickers = append(f.tickers, t)
	f.mu.Unlock()
	return t
}

func (f *tickerFactory) active() *manualTicker {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.tickers) - 1; i >= 0; i-- {
		if !f.tickers[i].isStopped() {
			return f.tickers[i]
		}
	}
	return nil
}

func (f *tickerFactory) activeInterval() time.Duration {
	t := f.active()
	if t == nil {
		return 0
	}
	return t.interval
}

func (f *tickerFactory) tick(now time.Time) bool {
	t := f.active()
	if t == nil {
		return false
	}
	return t.send(now)
}

// ---------- runner harness ----------

type runnerHarness struct {
	ctlSetup *controllerSetup
	backend  *fakeBackend
	tickers  *tickerFactory
	runner   *Runner
}

func startRunner(t *testing.T, fx fixture) *runnerHarness {
	t.Helper()
	c := newSetup(t, fx, nil)
	backend := newFakeBackend()
	tickers := newTickerFactory()
	runner, err := NewRunner(c.ctl, RunnerOptions{
		Backend: backend, Now: c.clock.Now, NewTicker: tickers.create,
		ApplyTimeout: time.Second,
		Logf:         func(format string, args ...any) { t.Logf(format, args...) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(context.Background()); err != nil {
		t.Fatalf("runner start: %v", err)
	}
	t.Cleanup(func() { _ = runner.Stop() })
	h := &runnerHarness{ctlSetup: c, backend: backend, tickers: tickers, runner: runner}
	waitFor(t, "initial tree", func() bool {
		_, ok := backend.find("status")
		return ok
	})
	waitFor(t, "main loop ticker", func() bool { return tickers.active() != nil })
	waitFor(t, "initial RefreshAll", func() bool {
		return c.caller.countOp(control.OpRulesList) >= 2
	})
	return h
}

func (h *runnerHarness) tick(d time.Duration) {
	h.ctlSetup.clock.Advance(d)
	h.tickers.tick(h.ctlSetup.clock.Now())
}

func (h *runnerHarness) waitLabel(t *testing.T, key, want string) {
	t.Helper()
	waitFor(t, "label "+key, func() bool {
		label, ok := h.backend.label(key)
		return ok && label == want
	})
}

// waitLabelFor waits for a dynamic label that cannot be written exactly.
func (h *runnerHarness) waitLabelFor(t *testing.T, key string, cond func(string) bool) {
	t.Helper()
	waitFor(t, "label "+key, func() bool {
		label, ok := h.backend.label(key)
		return ok && cond(label)
	})
}

// waitClickable waits until the binder publishes the expected click state.
// Click reads that state rather than the backend's visible label, so a test
// that clicked as soon as the label changed could observe the previous tree.
func (h *runnerHarness) waitClickable(t *testing.T, key string, enabled bool) {
	t.Helper()
	waitFor(t, "clickable "+key, func() bool {
		target, ok := h.runner.binder.Lookup(key)
		return ok && target.Enabled == enabled
	})
}

// setStatus replaces the status map instead of mutating it, so a concurrent
// fixture read never observes a write.
func (f *fixtureCaller) setStatus(key string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	next := make(map[string]any, len(f.fx.status))
	for k, v := range f.fx.status {
		next[k] = v
	}
	next[key] = value
	f.fx.status = next
}

func TestRunnerInitialLoadingMenuThenRefreshAll(t *testing.T) {
	caller := newFixtureCaller(newFixture())
	clock := newFakeClock()
	life := newFakeLifecycle()
	entered, release := caller.gateOn(control.OpStatus)
	ctl, err := New(Options{
		Caller: caller, Lifecycle: life, Notifier: &fakeNotifier{},
		Autostart: &fakeAutostart{status: autostart.Status{TrayAvailable: true, PIDFDAvailable: true}},
		Now:       clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := newFakeBackend()
	tickers := newTickerFactory()
	runner, err := NewRunner(ctl, RunnerOptions{
		Backend: backend, Now: clock.Now, NewTicker: tickers.create, ApplyTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Stop() })

	// The Loading menu exists before any management data arrives.
	waitFor(t, "status row", func() bool {
		_, ok := backend.find("status")
		return ok
	})
	if label, _ := backend.label("status"); label != "Loading…" {
		t.Fatalf("initial status = %q", label)
	}
	if keys := backend.keysOf("root"); !equalKeys(keys, []string{"status", "quit"}) {
		t.Fatalf("initial root keys = %v", keys)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("initial RefreshAll never called status")
	}
	release()
	waitFor(t, "ready status", func() bool {
		label, ok := backend.label("status")
		return ok && label != "Loading…"
	})
	if label, _ := backend.label("status"); label != "Targeted · 140 usable · 3 open" {
		t.Fatalf("refreshed status = %q", label)
	}
}

func TestRunnerClickTriggersControllerAndConfirmation(t *testing.T) {
	h := startRunner(t, newFixture())

	rulesBefore := h.ctlSetup.caller.countOp(control.OpRulesList)
	if err := h.runner.Click("mode.all-direct"); err != nil {
		t.Fatalf("click: %v", err)
	}
	waitFor(t, "mode request", func() bool { return h.ctlSetup.caller.countOp(control.OpMode) == 1 })
	// The click path refreshes core data immediately after the action.
	waitFor(t, "core refresh after action", func() bool {
		return h.ctlSetup.caller.countOp(control.OpRulesList) > rulesBefore
	})

	// A destructive click uses the same path and renders a pending confirmation.
	if err := h.runner.Click("mode.stop"); err != nil {
		t.Fatalf("stop click: %v", err)
	}
	confirmKey := ""
	waitFor(t, "pending confirmation", func() bool {
		for _, key := range h.backend.keysOf("root") {
			if strings.HasPrefix(key, "pending.") && strings.HasSuffix(key, ".confirm") {
				confirmKey = key
				return true
			}
		}
		return false
	})
	if h.ctlSetup.caller.countOp(control.OpMode) != 1 {
		t.Fatal("pending action executed before confirmation")
	}
	// The tree can be visible a moment before the binder publishes its click
	// map; retry until the key resolves.
	waitFor(t, "confirm clickable", func() bool {
		err := h.runner.Click(confirmKey)
		if err == nil {
			return true
		}
		if errors.Is(err, ErrUnknownNode) {
			return false
		}
		t.Fatalf("confirm click: %v", err)
		return false
	})
	waitFor(t, "stop executed", func() bool { return h.ctlSetup.caller.countOp(control.OpMode) == 2 })
}

func TestRunnerRejectsDisabledAndUnknownClicks(t *testing.T) {
	fx := newFixture()
	fx.status["mode"] = string(contract.ModeStopped)
	fx.status["listeners_open"] = false
	h := startRunner(t, fx)

	if label, _ := h.backend.label("status"); !strings.HasPrefix(label, "Stopped") {
		t.Fatalf("stopped status = %q", label)
	}
	if tray := h.backend.trayState(); tray.Icon != "stopped" || tray.Engine != EngineStopped {
		t.Fatalf("stopped tray = %+v", tray)
	}
	if err := h.runner.Click("mode.stop"); !errors.Is(err, ErrDisabledNode) {
		t.Fatalf("disabled click = %v", err)
	}
	if err := h.runner.Click("no.such.node"); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("unknown click = %v", err)
	}
	if n := h.ctlSetup.caller.countOp(control.OpMode); n != 0 {
		t.Fatalf("rejected clicks executed %d mode requests", n)
	}
}

func TestRunnerSlowHandlerDoesNotBlockClicksOrTicks(t *testing.T) {
	h := startRunner(t, newFixture())
	entered, release := h.ctlSetup.caller.gateOn(control.OpMode)

	if err := h.runner.Click("mode.all-direct"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first action never reached the management caller")
	}

	// The click queue keeps accepting while the first handler is blocked...
	if err := h.runner.Click("pool.rerank"); err != nil {
		t.Fatalf("second click while handler blocked: %v", err)
	}
	if n := h.ctlSetup.caller.countOp(control.OpPoolRerank); n != 0 {
		t.Fatal("second click bypassed the single click worker")
	}

	// ...and the independent tick loop still refreshes status.
	before := h.ctlSetup.caller.countOp(control.OpStatus)
	h.tick(2 * time.Second)
	waitFor(t, "status tick during blocked handler", func() bool {
		return h.ctlSetup.caller.countOp(control.OpStatus) > before
	})

	release()
	waitFor(t, "blocked action", func() bool { return h.ctlSetup.caller.countOp(control.OpMode) == 1 })
	waitFor(t, "queued action", func() bool { return h.ctlSetup.caller.countOp(control.OpPoolRerank) == 1 })
}

func TestRunnerRestartingReadyAndCadence(t *testing.T) {
	h := startRunner(t, newFixture())
	h.ctlSetup.life.events <- LifecycleEvent{Kind: LifecycleRestarting}

	h.waitLabelFor(t, "status", func(label string) bool { return label == "Restarting engine…" })
	waitFor(t, "restart cadence", func() bool { return h.tickers.activeInterval() == 500*time.Millisecond })

	h.ctlSetup.life.events <- LifecycleEvent{Kind: LifecycleReady}
	h.waitLabelFor(t, "status", func(label string) bool {
		return label == "Targeted · 140 usable · 3 open"
	})
	waitFor(t, "normal cadence", func() bool { return h.tickers.activeInterval() == 2*time.Second })

	// ready triggered an immediate core refresh and the menu accepts actions.
	if n := h.ctlSetup.caller.countOp(control.OpStatus); n < 2 {
		t.Fatalf("ready did not refresh status (calls=%d)", n)
	}
	if err := h.runner.Click("mode.all-direct"); err != nil {
		t.Fatalf("click after ready: %v", err)
	}
	waitFor(t, "mode after ready", func() bool { return h.ctlSetup.caller.countOp(control.OpMode) == 1 })
}

func TestRunnerListCadenceIsFifteenSeconds(t *testing.T) {
	h := startRunner(t, newFixture())
	listsBefore := h.ctlSetup.caller.countOp(control.OpRulesList)
	statusBefore := h.ctlSetup.caller.countOp(control.OpStatus)

	h.tick(2 * time.Second)
	waitFor(t, "status tick", func() bool {
		return h.ctlSetup.caller.countOp(control.OpStatus) > statusBefore
	})
	if got := h.ctlSetup.caller.countOp(control.OpRulesList); got != listsBefore {
		t.Fatalf("lists refreshed on the status cadence: %d -> %d", listsBefore, got)
	}

	h.tick(13 * time.Second)
	waitFor(t, "list tick", func() bool {
		return h.ctlSetup.caller.countOp(control.OpRulesList) > listsBefore
	})
}

func TestRunnerUnavailableAndRecovery(t *testing.T) {
	h := startRunner(t, newFixture())
	h.ctlSetup.caller.setTransport(true)
	h.tick(2 * time.Second)
	h.waitLabel(t, "status", "Engine unavailable; retrying…")
	h.waitClickable(t, "mode.all-direct", false)

	if err := h.runner.Click("mode.all-direct"); !errors.Is(err, ErrDisabledNode) {
		t.Fatalf("click while unavailable = %v", err)
	}
	if n := h.ctlSetup.caller.countOp(control.OpMode); n != 0 {
		t.Fatal("unavailable click reached management")
	}

	h.ctlSetup.caller.setTransport(false)
	h.tick(2 * time.Second)
	h.waitLabel(t, "status", "Targeted · 140 usable · 3 open")
	h.waitClickable(t, "mode.all-direct", true)
	if err := h.runner.Click("mode.all-direct"); err != nil {
		t.Fatalf("click after recovery: %v", err)
	}
	waitFor(t, "mode after recovery", func() bool { return h.ctlSetup.caller.countOp(control.OpMode) == 1 })
}

func TestRunnerBackendErrorIsVisibleAndRecovers(t *testing.T) {
	h := startRunner(t, newFixture())

	// Change the status label and make the backend reject both update paths.
	h.ctlSetup.caller.setStatus("connections", map[string]any{"open": 4, "dialing": 0})
	backendErr := errors.New("host disappeared")
	h.backend.setApplyErr(backendErr)
	h.backend.setBuildErr(backendErr)
	h.tick(2 * time.Second)
	waitFor(t, "backend error", func() bool { return h.runner.LastError() != nil })

	// Clicks are rejected while the rendered tree is unknown.
	if err := h.runner.Click("mode.all-direct"); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("click during render failure = %v", err)
	}

	// Recovery forces a full rebuild and clears the error.
	h.backend.setApplyErr(nil)
	h.backend.setBuildErr(nil)
	buildsAtFailure, _, _ := h.backend.counts()
	h.tick(2 * time.Second)
	waitFor(t, "backend recovery", func() bool {
		builds, _, _ := h.backend.counts()
		return h.runner.LastError() == nil && builds > buildsAtFailure
	})
	if label, _ := h.backend.label("status"); label != "Targeted · 140 usable · 4 open" {
		t.Fatalf("recovered status = %q", label)
	}
	if err := h.runner.Click("mode.all-direct"); err != nil {
		t.Fatalf("click after recovery: %v", err)
	}
	waitFor(t, "mode after recovery", func() bool { return h.ctlSetup.caller.countOp(control.OpMode) == 1 })
}

func TestRunnerStartFailureAndShutdown(t *testing.T) {
	c := newSetup(t, newFixture(), nil)
	backend := newFakeBackend()
	backend.setBuildErr(errors.New("no StatusNotifierHost"))
	tickers := newTickerFactory()
	runner, err := NewRunner(c.ctl, RunnerOptions{Backend: backend, Now: c.clock.Now, NewTicker: tickers.create})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(context.Background()); err == nil {
		t.Fatal("start succeeded without a backend host")
	}
	if err := runner.Start(context.Background()); err == nil {
		t.Fatal("the runner restarted after a failed start")
	}
	if err := runner.Stop(); err != nil {
		t.Fatalf("stop after failed start: %v", err)
	}
	if _, _, closes := backend.counts(); closes != 1 {
		t.Fatalf("backend close calls = %d", closes)
	}
}

func TestRunnerShutdownLeavesNoGoroutinesOrFDs(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	baselineFDs, fdErr := countFDs()
	if fdErr != nil {
		t.Logf("fd baseline unavailable: %v", fdErr)
	}

	func() {
		h := startRunner(t, newFixture())
		if err := h.runner.Click("mode.all-direct"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "click before shutdown", func() bool { return h.ctlSetup.caller.countOp(control.OpMode) == 1 })
		if err := h.runner.Stop(); err != nil {
			t.Fatalf("stop: %v", err)
		}
		if _, _, closes := h.backend.counts(); closes != 1 {
			t.Fatalf("backend close calls = %d", closes)
		}
		if err := h.runner.Click("mode.all-direct"); !errors.Is(err, ErrRunnerStopped) {
			t.Fatalf("click after stop = %v", err)
		}
		if err := h.runner.Start(context.Background()); err == nil {
			t.Fatal("the runner restarted after Stop")
		}
		if tick := h.tickers.active(); tick != nil {
			t.Fatal("a ticker survived shutdown")
		}
	}()

	waitFor(t, "goroutines to settle", func() bool {
		return runtime.NumGoroutine() <= baselineGoroutines+2
	})
	if fdErr == nil {
		waitFor(t, "file descriptors to settle", func() bool {
			n, err := countFDs()
			return err == nil && n <= baselineFDs+2
		})
	}
}

func TestRunnerConcurrentClicks(t *testing.T) {
	h := startRunner(t, newFixture())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.runner.Click("mode.all-direct")
		}()
	}
	wg.Wait()
	waitFor(t, "concurrent click", func() bool { return h.ctlSetup.caller.countOp(control.OpMode) >= 1 })
}

func countFDs() (int, error) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}
