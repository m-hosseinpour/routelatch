package trayui

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Runner drives the controller at the documented cadence and renders its View
// through a Backend. It is the R15-D1 binder/runner layer: no D-Bus import and
// no product tray code live here.
//
// Lifecycle:
//   - Start renders the initial (Loading) View and returns.
//   - A refresh worker performs the initial RefreshAll, then status every two
//     seconds and lists every fifteen seconds, plus core refreshes after an
//     action and after a LifecycleReady event. During restarting the status
//     cadence is 500ms. Refresh calls never block the main loop.
//   - A click worker calls Controller.Trigger for one backend click at a time,
//     so a slow management call cannot block the backend or the main loop.
//   - The main loop forwards LifecycleEvents to HandleEvent and applies the
//     View after every event or wake; the Binder suppresses no-op renders.
//   - Stop cancels the runner's workers, waits, and closes the backend item.
//     A Controller job started by a click is owned by the controller/app; Stop
//     does not cancel it or contact the supervisor.
type Runner struct {
	ctl    *Controller
	binder *Binder
	opts   RunnerOptions

	ctx     context.Context
	cancel  context.CancelFunc
	loop    chan struct{}
	wake    chan struct{}
	clicks  chan clickRequest
	refresh chan refreshKind
	wg      sync.WaitGroup

	mu       sync.Mutex
	startMu  sync.Mutex
	started  bool
	stopping bool
	stopped  bool
	stopDone chan struct{}
	stopErr  error
}

// RunnerOptions wires the runner's clock, cadence and the optional log sink.
// Zero values take the documented defaults. NewTicker exists for deterministic
// tests; production leaves it nil.
type RunnerOptions struct {
	Backend Backend

	// Logf receives short operational messages. It must not be used for node
	// labels or targets: the click path logs only Action strings.
	Logf func(format string, args ...any)

	Now       func() time.Time
	NewTicker func(time.Duration) Ticker

	StatusInterval  time.Duration
	ListInterval    time.Duration
	RestartInterval time.Duration
	ApplyTimeout    time.Duration
	CloseTimeout    time.Duration
	ClickQueue      int
}

// Ticker is the timer surface the runner needs. Production uses realTicker;
// tests inject a manual channel.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type realTicker struct{ t *time.Ticker }

func (t realTicker) C() <-chan time.Time { return t.t.C }
func (t realTicker) Stop()               { t.t.Stop() }

const (
	defaultStatusInterval  = 2 * time.Second
	defaultListInterval    = 15 * time.Second
	defaultRestartInterval = 500 * time.Millisecond
	defaultApplyTimeout    = 3 * time.Second
	defaultCloseTimeout    = 2 * time.Second
	defaultClickQueue      = 8
)

// NewRunner validates its boundaries and applies the defaults.
func NewRunner(ctl *Controller, opts RunnerOptions) (*Runner, error) {
	if ctl == nil {
		return nil, errors.New("trayui: a Controller is required")
	}
	binder, err := NewBinder(opts.Backend)
	if err != nil {
		return nil, err
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewTicker == nil {
		opts.NewTicker = func(d time.Duration) Ticker { return realTicker{t: time.NewTicker(d)} }
	}
	if opts.StatusInterval <= 0 {
		opts.StatusInterval = defaultStatusInterval
	}
	if opts.ListInterval <= 0 {
		opts.ListInterval = defaultListInterval
	}
	if opts.RestartInterval <= 0 {
		opts.RestartInterval = defaultRestartInterval
	}
	if opts.ApplyTimeout <= 0 {
		opts.ApplyTimeout = defaultApplyTimeout
	}
	if opts.CloseTimeout <= 0 {
		opts.CloseTimeout = defaultCloseTimeout
	}
	if opts.ClickQueue <= 0 {
		opts.ClickQueue = defaultClickQueue
	}
	return &Runner{
		ctl: ctl, binder: binder, opts: opts,
		wake:     make(chan struct{}, 1),
		clicks:   make(chan clickRequest, opts.ClickQueue),
		refresh:  make(chan refreshKind, 4),
		stopDone: make(chan struct{}),
	}, nil
}

type clickRequest struct {
	action Action
	target Target
}

type refreshKind int

const (
	refreshAll refreshKind = iota + 1
	refreshStatus
	refreshLists
	refreshCore
)

// Start performs the initial render and starts the workers. The initial
// RefreshAll itself runs in the refresh worker so a slow management socket
// never delays the Loading menu or tray registration.
func (r *Runner) Start(ctx context.Context) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.started || r.stopping || r.stopped {
		r.mu.Unlock()
		return errors.New("trayui: the runner cannot be started again")
	}
	r.ctx, r.cancel = context.WithCancel(ctx)
	r.loop = make(chan struct{})
	r.started = true
	r.mu.Unlock()

	view := r.ctl.View()
	applyCtx, cancel := context.WithTimeout(r.ctx, r.opts.ApplyTimeout)
	_, err := r.binder.Apply(applyCtx, view)
	cancel()
	if err != nil {
		r.cancel()
		r.logf("tray: initial render failed: %v", err)
		closeCtx, closeCancel := context.WithTimeout(context.Background(), r.opts.CloseTimeout)
		_ = r.binder.Close(closeCtx)
		closeCancel()
		r.mu.Lock()
		r.started = false
		r.mu.Unlock()
		r.markStopped(err)
		return err
	}

	r.wg.Add(2)
	go r.refreshLoop()
	go r.clickLoop()
	go r.mainLoop(view.Engine)
	r.requestRefresh(refreshAll)
	return nil
}

// Stop cancels the workers, waits for them, and closes the backend. It is safe
// to call more than once; a second call waits for the first and returns its
// result.
func (r *Runner) Stop() error {
	r.startMu.Lock()
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		r.startMu.Unlock()
		return nil
	}
	if r.stopping {
		done := r.stopDone
		r.mu.Unlock()
		r.startMu.Unlock()
		<-done
		r.mu.Lock()
		err := r.stopErr
		r.mu.Unlock()
		return err
	}
	r.stopping = true
	cancel := r.cancel
	r.mu.Unlock()
	r.startMu.Unlock()

	cancel()
	<-r.loop
	r.wg.Wait()

	closeCtx, closeCancel := context.WithTimeout(context.Background(), r.opts.CloseTimeout)
	err := r.binder.Close(closeCtx)
	closeCancel()

	r.mu.Lock()
	r.stopped = true
	r.stopErr = err
	close(r.stopDone)
	r.mu.Unlock()
	return err
}

// markStopped publishes the final state exactly once. Start failure and the
// normal Stop path both use it, so a repeated close cannot panic.
func (r *Runner) markStopped(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	r.stopErr = err
	select {
	case <-r.stopDone:
	default:
		close(r.stopDone)
	}
}

// Click is the backend click entry point. It resolves the stable key against
// the last successfully rendered tree and enqueues the stored action/target
// without waiting for management work. Unknown, disabled and hidden keys are
// rejected.
func (r *Runner) Click(key string) error {
	r.mu.Lock()
	stopped := r.stopping || r.stopped
	r.mu.Unlock()
	if stopped {
		return ErrRunnerStopped
	}
	target, ok := r.binder.Lookup(key)
	if !ok {
		return ErrUnknownNode
	}
	if !target.Enabled {
		return ErrDisabledNode
	}
	select {
	case r.clicks <- clickRequest{action: target.Action, target: target.Target}:
		return nil
	default:
		return ErrClickBusy
	}
}

// ErrClickBusy means the bounded click queue is full. The backend should let
// the user retry; no action was queued.
var ErrClickBusy = errors.New("the tray click queue is full")

// ErrRunnerStopped means the runner no longer accepts clicks.
var ErrRunnerStopped = errors.New("the tray runner is stopped")

// LastError is the last backend rendering error, or nil after a successful
// render. The controller's own unavailable state is shown in the menu instead.
func (r *Runner) LastError() error {
	return r.binder.LastError()
}

func (r *Runner) mainLoop(engine EngineState) {
	defer close(r.loop)
	defer r.logf("tray: runner stopped")

	events := r.ctl.LifecycleEvents()
	interval := r.opts.StatusInterval
	ticker := r.opts.NewTicker(interval)
	defer ticker.Stop()
	nextStatus := r.opts.Now().Add(interval)
	nextLists := r.opts.Now().Add(r.opts.ListInterval)
	var lastRenderErr string

	for {
		select {
		case <-r.ctx.Done():
			return
		case now := <-ticker.C():
			restarting := engine == EngineRestarting
			statusCadence := r.opts.StatusInterval
			if restarting {
				statusCadence = r.opts.RestartInterval
			}
			if !now.Before(nextStatus) {
				if r.requestRefresh(refreshStatus) {
					nextStatus = now.Add(statusCadence)
				}
			}
			if !now.Before(nextLists) {
				if r.requestRefresh(refreshLists) {
					nextLists = now.Add(r.opts.ListInterval)
				}
			}
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			r.ctl.HandleEvent(ev)
			if ev.Kind == LifecycleReady {
				r.requestRefresh(refreshCore)
			}
		case <-r.wake:
		}

		if r.ctx.Err() != nil {
			return
		}
		view := r.applyCurrent()
		// A cancellation that lands during the backend call is shutdown, not a
		// rendering failure; do not publish it as one.
		if r.ctx.Err() != nil {
			return
		}
		if err := r.binder.LastError(); err != nil {
			msg := err.Error()
			if msg != lastRenderErr {
				lastRenderErr = msg
				r.logf("tray: render failed: %v", err)
			}
		} else if lastRenderErr != "" {
			lastRenderErr = ""
			r.logf("tray: render recovered")
		}
		engine = view.Engine

		want := r.opts.StatusInterval
		if engine == EngineRestarting {
			want = r.opts.RestartInterval
		}
		if want != interval {
			ticker.Stop()
			interval = want
			ticker = r.opts.NewTicker(interval)
			now := r.opts.Now()
			nextStatus = now.Add(interval)
		}
	}
}

func (r *Runner) applyCurrent() View {
	view := r.ctl.View()
	ctx, cancel := context.WithTimeout(r.ctx, r.opts.ApplyTimeout)
	_, _ = r.binder.Apply(ctx, view)
	cancel()
	return view
}

func (r *Runner) refreshLoop() {
	defer r.wg.Done()
	for {
		select {
		case <-r.ctx.Done():
			return
		case kind := <-r.refresh:
			var err error
			switch kind {
			case refreshAll:
				err = r.ctl.RefreshAll(r.ctx)
			case refreshStatus:
				err = r.ctl.RefreshStatus(r.ctx)
			case refreshLists:
				err = r.ctl.RefreshLists(r.ctx)
			case refreshCore:
				err = r.ctl.RefreshCore(r.ctx)
			}
			if err != nil && r.ctx.Err() != nil {
				continue // shutdown, not a data state to publish
			}
			r.signalWake()
		}
	}
}

func (r *Runner) clickLoop() {
	defer r.wg.Done()
	for {
		select {
		case <-r.ctx.Done():
			return
		case req := <-r.clicks:
			res, err := r.ctl.Trigger(r.ctx, req.action, req.target)
			if err != nil {
				r.logf("tray: action %s failed: %s", req.action.String(), errorKindLabel(err))
			}
			r.signalWake()
			if actionNeedsRefresh(req.action, res) {
				r.requestRefresh(refreshCore)
			}
		}
	}
}

func actionNeedsRefresh(a Action, res Result) bool {
	switch res.Status {
	case ResultCompleted, ResultJobStarted:
	default:
		return false
	}
	switch a {
	case ActionTerminalMenu,
		ActionShowLogPath, ActionDesktopRefresh, ActionAutostartToggle,
		ActionQuit, ActionQuitKeepDesktop, ActionQuitRestoreDesktop:
		return false
	}
	return true
}

func (r *Runner) requestRefresh(kind refreshKind) bool {
	select {
	case <-r.ctx.Done():
		return false
	default:
	}
	select {
	case r.refresh <- kind:
		return true
	default:
		return false
	}
}

func (r *Runner) signalWake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Runner) logf(format string, args ...any) {
	if r.opts.Logf != nil {
		r.opts.Logf(format, args...)
	}
}
