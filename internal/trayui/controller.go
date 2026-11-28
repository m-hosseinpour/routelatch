package trayui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"routelatch/internal/autostart"
	"routelatch/internal/contract"
	"routelatch/internal/control"
)

// Options wires the controller to its boundaries. Caller is required;
// Lifecycle, Notifier, Autostart and Terminal may be nil in tests or in a
// headless controller, where the corresponding actions report a safe failure.
type Options struct {
	Caller    Caller
	Lifecycle Lifecycle
	Notifier  Notifier
	Autostart AutostartManager
	Terminal  Terminal

	// Paths are the canonical paths from store.Paths. config.path refreshes
	// Config/State/Socket; Log is the configured log path (Show log path).
	Paths Paths
	// CustomDataDir disables Start at login: XDG autostart only manages the
	// default instance (D26).
	CustomDataDir bool

	Now            func() time.Time
	CallTimeout    time.Duration
	ActionTimeout  time.Duration
	RestartTimeout time.Duration
}

const (
	defaultCallTimeout    = 1500 * time.Millisecond
	defaultActionTimeout  = 30 * time.Second
	defaultRestartTimeout = 45 * time.Second
)

// Job is the public view of the one long-running operation.
type Job struct {
	ID          uint64
	Name        string
	StartedAt   time.Time
	Interrupted bool
}

type job struct {
	Job
	ctx         context.Context
	cancel      context.CancelFunc
	action      Action
	target      Target
	restart     bool
	restartSeen bool
	deadline    time.Time
}

// ResultStatus describes what Trigger did.
type ResultStatus int

const (
	ResultCompleted ResultStatus = iota
	ResultJobStarted
	ResultNoop
)

// Result is a safe, short outcome. Message never contains a destination, URL,
// credential or raw caller message.
type Result struct {
	Status  ResultStatus
	Message string
}

// View is one immutable menu model for a binder. Root is cached and reused
// while Fingerprint is unchanged; callers must not mutate the tree.
type View struct {
	Root        Node
	Fingerprint string
	Engine      EngineState
	// Tun is the coverage state for the tray icon and tooltip. The menu nodes
	// carry the same data; this copy avoids a second traversal and is immutable
	// like Root.
	Tun        TunView
	Job        *Job
	LastAction string
	Loaded     bool
}

// Controller owns the single job slot, restart event ordering and the
// fingerprint cache. All methods are safe for concurrent use.
type Controller struct {
	opts Options

	refreshMu  sync.Mutex // serializes network/local refresh reads
	mu         sync.Mutex
	snap       Snapshot
	job        *job
	lastAction string
	restarting bool

	cachedRoot Node
	cachedFP   string
	builds     int
	nextJob    uint64
}

// New validates the required boundary and applies the documented defaults.
func New(opts Options) (*Controller, error) {
	if opts.Caller == nil {
		return nil, errors.New("trayui: a Caller is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = defaultCallTimeout
	}
	if opts.ActionTimeout <= 0 {
		opts.ActionTimeout = defaultActionTimeout
	}
	if opts.RestartTimeout <= 0 {
		opts.RestartTimeout = defaultRestartTimeout
	}
	return &Controller{
		opts: opts,
		snap: Snapshot{Engine: EngineLoading, Paths: opts.Paths},
	}, nil
}

// Snapshot returns a deep copy of the current typed data.
func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return copySnapshot(c.snap)
}

func copySnapshot(s Snapshot) Snapshot {
	out := s
	out.Status.Warnings = append([]string(nil), s.Status.Warnings...)
	out.Status.Tun.Warnings = append([]contract.TunWarning(nil), s.Status.Tun.Warnings...)
	if s.Status.ResumeRequiresRestart != nil {
		v := *s.Status.ResumeRequiresRestart
		out.Status.ResumeRequiresRestart = &v
	}
	out.Rules = append([]Rule(nil), s.Rules...)
	out.Pool.Rows = append([]PoolRow(nil), s.Pool.Rows...)
	out.Proposals = append([]Proposal(nil), s.Proposals...)
	out.Desktop.Conflict = append([]string(nil), s.Desktop.Conflict...)
	out.Desktop.Restored = append([]string(nil), s.Desktop.Restored...)
	out.Desktop.UserModified = append([]string(nil), s.Desktop.UserModified...)
	out.Desktop.Failed = append([]string(nil), s.Desktop.Failed...)
	out.Doctor = append([]Check(nil), s.Doctor...)
	return out
}

// View evaluates restart timeouts, clamps pagination, rebuilds the tree only
// when the menu fingerprint changed, and returns the immutable model.
func (c *Controller) View() View {
	c.mu.Lock()
	notes := c.expireLocked(c.opts.Now())
	engine := c.engineLocked()
	fp := c.menuFingerprintLocked(engine)
	if fp != c.cachedFP {
		c.cachedRoot = c.buildMenuLocked(engine)
		c.cachedFP = fp
		c.builds++
	}
	view := View{
		Root:        c.cachedRoot,
		Fingerprint: fp,
		Engine:      engine,
		Tun:         copyTunView(c.snap.Status.Tun),
		Job:         copyJob(c.job),
		LastAction:  c.lastAction,
		Loaded:      c.snap.Engine != EngineLoading,
	}
	c.mu.Unlock()
	c.notify(notes)
	return view
}

func copyTunView(v TunView) TunView {
	out := v
	out.Warnings = append([]contract.TunWarning(nil), v.Warnings...)
	return out
}

func copyJob(j *job) *Job {
	if j == nil {
		return nil
	}
	out := j.Job
	return &out
}

func (c *Controller) engineLocked() EngineState {
	if c.restarting {
		return EngineRestarting
	}
	return c.snap.Engine
}

// needsFreshDesktopStatus reports whether a click must re-read desktop.status
// before it runs. A plain Quit must not rely on a stale "not applied" snapshot:
// quitting while the desktop proxy is applied leaves it pointing at a closed
// listener.
func needsFreshDesktopStatus(a Action) bool {
	return a == ActionQuit
}

// Trigger is the click entry point: an enabled menu item executes its action
// directly. Labels are never parsed.
func (c *Controller) Trigger(ctx context.Context, action Action, target Target) (Result, error) {
	if needsFreshDesktopStatus(action) {
		// The read is best effort and outside c.mu: a bounded management call
		// must not block the menu lock, and a failed read becomes the visible
		// unknown state instead of hiding the action.
		_ = c.RefreshDesktop(ctx)
	}

	c.mu.Lock()
	notes := c.expireLocked(c.opts.Now())

	if kindOf(action) == kindNone {
		c.mu.Unlock()
		c.notify(notes)
		return Result{}, ErrBlocked
	}
	if err := c.allowedLocked(action, target); err != nil {
		message := "Action unavailable: " + errorKindLabel(err)
		if action == ActionQuit && errors.Is(err, ErrBlocked) && c.desktopAppliedOrUnknownLocked() {
			// A stale plain-Quit click is rejected after the fresh desktop read.
			// Say why instead of leaving the user with a closed menu and no
			// visible outcome.
			message = "Quit needs an explicit desktop proxy choice: restore or keep"
			if c.allowedLocked(ActionQuitRestoreDesktop, Target{}) != nil {
				message = "Quit needs an explicit desktop proxy choice: keep (restore is unavailable here)"
			}
			notes = append(notes, Notification{Title: "RouteLatch", Body: message})
		}
		c.lastAction = message
		c.mu.Unlock()
		c.notify(notes)
		return Result{}, err
	}
	c.mu.Unlock()
	c.notify(notes)
	return c.dispatch(ctx, action, target)
}

// dispatch revalidates state, starts the single job slot or runs a synchronous
// management/lifecycle request.
func (c *Controller) dispatch(ctx context.Context, action Action, target Target) (Result, error) {
	c.mu.Lock()
	if err := c.allowedLocked(action, target); err != nil {
		c.lastAction = "Action unavailable: " + errorKindLabel(err)
		c.mu.Unlock()
		return Result{}, err
	}
	if name, ok := jobName(action); ok {
		j := c.startJobLocked(action, target, name)
		c.lastAction = "Job started: " + name
		c.mu.Unlock()
		go c.runJob(j)
		return Result{Status: ResultJobStarted, Message: "Job started: " + name}, nil
	}
	c.mu.Unlock()
	return c.runSync(ctx, action, target)
}

// allowedLocked is the one availability rule used by both the menu builder and
// Trigger, so a disabled item and a rejected click cannot disagree.
func (c *Controller) allowedLocked(action Action, target Target) error {
	engine := c.engineLocked()
	busy := c.job != nil

	switch action {
	case ActionTerminalMenu, ActionShowLogPath:
		if engine == EngineLoading {
			return ErrUnavailable
		}
		return nil
	case ActionQuit:
		if c.desktopAppliedOrUnknownLocked() {
			// Quit must be the explicit restore/keep choice while the desktop
			// proxy is applied or its state is unknown.
			return ErrBlocked
		}
		return nil
	case ActionQuitKeepDesktop:
		if engine == EngineLoading {
			return ErrUnavailable
		}
		return nil
	case ActionQuitRestoreDesktop:
		if busy {
			return ErrBusy
		}
		if engine == EngineLoading || engine == EngineUnavailable || engine == EngineRestarting || engine == EngineStarting {
			return ErrUnavailable
		}
		if c.snap.Desktop.Known && !c.snap.Desktop.Err && !c.snap.Desktop.Supported {
			return ErrBlocked
		}
		return nil
	case ActionRestartEngine:
		if busy {
			return ErrBusy
		}
		if engine != EngineReady && engine != EngineStopped {
			return ErrUnavailable
		}
		return nil
	case ActionAutostartToggle:
		if busy {
			return ErrBusy
		}
		if c.autostartDisabledLocked() != "" {
			return ErrBlocked
		}
		return nil
	}

	// Management operations need a live socket.
	if engine == EngineLoading || engine == EngineUnavailable || engine == EngineRestarting {
		return ErrUnavailable
	}
	// A starting engine serves status/lists but is not a safe mutation target.
	if engine == EngineStarting && action != ActionDesktopRefresh {
		return ErrUnavailable
	}
	// Desktop status is a read; it stays available while a job runs. An
	// unsupported desktop only shows its status and reason (§7.3), but a
	// failed read leaves Refresh enabled so the unknown state can recover.
	if action == ActionDesktopRefresh {
		if d := c.snap.Desktop; d.Known && !d.Err && !d.Supported {
			return ErrBlocked
		}
		return nil
	}
	if busy {
		return ErrBusy
	}
	if engine == EngineStopped {
		if !stoppedActionAllowed(action) {
			return ErrBlocked
		}
		if (action == ActionModeTargeted || action == ActionModeAllProxy || action == ActionModeAllDirect) && c.resumeBlockedLocked() {
			return ErrBlocked
		}
	}
	switch action {
	case ActionProposalAccept, ActionProposalAcceptSuffix, ActionProposalReject, ActionProposalRecheck:
		if !c.snap.Status.DiagnosticsEnabled {
			return ErrBlocked
		}
		// An unroute suggestion has no probe evidence to recheck. The menu
		// disables the item and a click must be rejected by the same rule
		// (§7.3).
		if action == ActionProposalRecheck {
			if p, ok := c.proposalByIDLocked(target.ProposalID); ok && p.Kind == contract.ProposalUnroute {
				return ErrBlocked
			}
		}
	case ActionDesktopSet:
		if !c.desktopSetAllowedLocked() {
			return ErrBlocked
		}
	case ActionDesktopRestore:
		if !c.desktopRestoreAllowedLocked() {
			return ErrBlocked
		}
	case ActionDesktopClear:
		if !c.desktopClearAllowedLocked() {
			return ErrBlocked
		}
	case ActionTunEnable, ActionTunDisable:
		if c.tunActionBlockedReasonLocked(action) != "" {
			return ErrBlocked
		}
	}
	return nil
}

func stoppedActionAllowed(action Action) bool {
	switch action {
	case ActionModeTargeted, ActionModeAllProxy, ActionModeAllDirect,
		ActionDesktopRefresh, ActionDesktopRestore, ActionDesktopClear, ActionBackup, ActionTunDisable:
		return true
	default:
		// Pool/probe/rules mutation, proposals and diagnostics do not run while
		// the listeners are closed (§7.3).
		return false
	}
}

func (c *Controller) autostartDisabledLocked() string {
	if c.opts.Autostart == nil {
		return "autostart manager is not configured"
	}
	if c.opts.CustomDataDir {
		return "autostart manages only the default data directory"
	}
	if c.snap.Autostart.Err {
		return "autostart status is unavailable"
	}
	if !c.snap.Autostart.Known {
		return "autostart status is not loaded"
	}
	if c.snap.Autostart.Conflict != "" {
		if c.snap.Autostart.Conflict == autostart.ConflictService {
			return "a managed RouteLatch service unit exists; remove it before using autostart"
		}
		return c.snap.Autostart.Conflict
	}
	if c.snap.Autostart.Enabled && !c.snap.Autostart.Managed {
		return "the existing autostart entry is not managed by RouteLatch"
	}
	return ""
}

func (c *Controller) desktopSetAllowedLocked() bool {
	d := c.snap.Desktop
	return d.Known && !d.Err && d.Supported && c.snap.Engine == EngineReady && c.snap.Status.ListenersOpen
}

func (c *Controller) desktopRestoreAllowedLocked() bool {
	d := c.snap.Desktop
	return d.Known && !d.Err && d.Supported
}

// desktopClearAllowedLocked makes Clear available whenever a supported
// desktop backend is known: with or without a previous marker, while the
// service is stopped, and even when the proxy already reports cleared. The
// operation is idempotent and reports an already-disabled setting as a notice.
func (c *Controller) desktopClearAllowedLocked() bool {
	d := c.snap.Desktop
	return d.Known && !d.Err && d.Supported
}

func (c *Controller) desktopClearBlockedReasonLocked() string {
	d := c.snap.Desktop
	switch {
	case !d.Known || d.Err:
		return "desktop proxy status is unknown"
	case !d.Supported:
		return "this desktop environment is not supported"
	default:
		return ""
	}
}

// tunActionBlockedReasonLocked is the one rule behind both the disabled menu
// item and a rejected click. It returns a short, destination-free reason, or ""
// when the action may run. Enabling needs a ready, non-stopped engine, a known
// TUN view, a usable helper and no coverage already active or in transition;
// disabling is always allowed except when it is a proven no-op.
func (c *Controller) tunActionBlockedReasonLocked(action Action) string {
	if c.job != nil {
		return "another job is running"
	}
	switch c.snap.Engine {
	case EngineLoading:
		return "TUN status is loading"
	case EngineStarting:
		return "the engine is starting"
	case EngineUnavailable:
		return "the engine is unavailable"
	case EngineRestarting:
		return "the engine is restarting"
	case EngineStopped:
		if action == ActionTunEnable {
			return "listening is stopped; choose a mode first"
		}
	}
	t := c.snap.Status.Tun
	if !t.Known {
		return "TUN status is unavailable from this engine"
	}
	if action == ActionTunDisable {
		// Disable is idempotent and allowed in every mode (§10.3). Only a
		// proven off+unrequested state is a no-op; stopping must remain
		// actionable so a transition cannot strand the user.
		if t.State == contract.TunOff && !t.Desired {
			return "coverage is already off"
		}
		return ""
	}
	switch t.State {
	case contract.TunOn:
		return "coverage is already on"
	case contract.TunStarting:
		return "coverage is starting"
	case contract.TunStopping:
		return "coverage is stopping"
	case contract.TunOff, contract.TunStateError, contract.TunUnavailable:
		// Off, failed or unavailable is a safe retry target.
	default:
		return "the coverage state is unknown"
	}
	switch t.Helper {
	case contract.TunHelperInstalled:
		return ""
	case contract.TunHelperMissing:
		return "helper is missing; run 'sudo routelatch tun setup'"
	case contract.TunHelperIncompatible:
		return "helper is incompatible; rerun 'sudo routelatch tun setup'"
	default:
		return "the helper state is unknown"
	}
}

func (c *Controller) startJobLocked(action Action, target Target, name string) *job {
	c.nextJob++
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{
		Job:    Job{ID: c.nextJob, Name: name, StartedAt: c.opts.Now()},
		ctx:    ctx,
		cancel: cancel,
		action: action,
		target: target,
	}
	if action == ActionRestartEngine {
		j.restart = true
		j.deadline = c.opts.Now().Add(c.opts.RestartTimeout)
	}
	c.job = j
	return j
}

func (c *Controller) runJob(j *job) {
	var message string
	var err error
	switch kindOf(j.action) {
	case kindRestartJob:
		if c.opts.Lifecycle == nil {
			err = ErrNoLifecycle
		} else {
			err = c.opts.Lifecycle.RestartEngine()
			if err == nil {
				// Success is not claimed here: the job ends only after
				// restarting followed by ready (§7.5).
				return
			}
		}
	case kindQuitRestoreJob:
		message, err = c.jobQuitRestore(j.ctx)
	case kindAutostart:
		message, err = c.jobAutostart(j.ctx)
	case kindRequest:
		message, err = c.jobRequest(j.ctx, j, j.action, j.target)
	default:
		err = ErrBlocked
	}
	c.finishJob(j, message, err)
}

func (c *Controller) finishJob(j *job, message string, err error) {
	c.mu.Lock()
	if c.job != j {
		c.mu.Unlock()
		return
	}
	interrupted := j.Interrupted
	if err != nil && (errors.Is(err, context.Canceled) || j.ctx.Err() != nil) {
		interrupted = true
	}
	c.job = nil
	switch {
	case interrupted:
		message = "Interrupted"
	case err != nil:
		message = "Failed: " + errorKindLabel(err)
	case message == "":
		message = "Done"
	}
	message = SanitizeHeader(message)
	c.lastAction = message
	c.mu.Unlock()

	c.notify([]Notification{{Title: "RouteLatch", Body: message}})
	if err == nil && !interrupted && j.action != ActionAutostartToggle && j.action != ActionQuitRestoreDesktop {
		c.refreshAfterJob()
	}
}

// interruptLocked cancels the active non-restart job. The job slot stays
// occupied until the goroutine returns, so a second operation cannot overlap;
// the menu shows that the job was interrupted.
func (c *Controller) interruptLocked() {
	if c.job == nil || c.job.restart {
		return
	}
	c.job.Interrupted = true
	c.job.cancel()
	c.lastAction = "Interrupted"
}

// HandleEvent applies the supervisor ordering signals. It never blocks on the
// socket: a binder refreshes after ready on its own tick.
func (c *Controller) HandleEvent(event LifecycleEvent) {
	now := c.opts.Now()
	c.mu.Lock()
	if event.Kind == LifecycleRestarting {
		c.restarting = true
		c.lastAction = "Restarting engine…"
		if c.job != nil {
			if c.job.action == ActionRestartEngine {
				c.job.restartSeen = true
				c.job.deadline = now.Add(c.opts.RestartTimeout)
			} else {
				c.job.Interrupted = true
				c.job.cancel()
				c.lastAction = "Interrupted"
			}
		}
		c.mu.Unlock()
		return
	}
	if event.Kind != LifecycleReady {
		c.mu.Unlock()
		return
	}
	c.restarting = false
	// A ready event proves a new engine generation only together with a fresh
	// status. Until the binder refreshes, do not let the previous snapshot's
	// ready state enable management actions or show its lists as current.
	c.snap.Engine = EngineStarting
	c.snap.ListsKnown = false
	var finished *job
	message := ""
	if c.job != nil && c.job.action == ActionRestartEngine && c.job.restartSeen {
		finished = c.job
		c.job = nil
		message = "Engine restart confirmed"
		c.lastAction = message
	} else if c.job == nil {
		c.lastAction = "Engine ready"
	}
	c.mu.Unlock()
	if finished != nil {
		c.notify([]Notification{{Title: "RouteLatch", Body: message}})
	}
}

// expireLocked evaluates the restart deadline. It returns the notifications
// the caller must deliver after releasing the mutex.
func (c *Controller) expireLocked(now time.Time) []Notification {
	var notes []Notification
	if c.job != nil && c.job.restart && !now.Before(c.job.deadline) {
		c.job = nil
		c.restarting = false
		// The generation may have changed. Do not let the old snapshot's ready
		// state enable management actions until a fresh status proves it.
		c.storeUnavailableLocked("restart unconfirmed")
		c.lastAction = "Engine restart unconfirmed"
		notes = append(notes, Notification{Title: "RouteLatch", Body: c.lastAction})
	}
	return notes
}

// LifecycleEvents exposes the supervisor channel so a binder can forward each
// value to HandleEvent without holding a second reference to the adapter.
func (c *Controller) LifecycleEvents() <-chan LifecycleEvent {
	if c.opts.Lifecycle == nil {
		return nil
	}
	return c.opts.Lifecycle.Events()
}

// Tick exists for a binder that wants restart-timeout processing without
// applying a tree. View itself performs the same evaluation.
func (c *Controller) Tick() {
	c.mu.Lock()
	notes := c.expireLocked(c.opts.Now())
	c.mu.Unlock()
	c.notify(notes)
}

// Close cancels a running job and marks it interrupted. It does not contact
// the supervisor.
func (c *Controller) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.job != nil {
		c.job.Interrupted = true
		c.job.cancel()
	}
}

func (c *Controller) notify(notes []Notification) {
	if c.opts.Notifier == nil {
		return
	}
	for _, n := range notes {
		c.opts.Notifier.Notify(Notification{Title: SanitizeLabel(n.Title), Body: SanitizeHeader(n.Body)})
	}
}

func (c *Controller) notifyNow(body string) {
	c.notify([]Notification{{Title: "RouteLatch", Body: body}})
}

// ---------- synchronous execution ----------

func (c *Controller) runSync(ctx context.Context, action Action, target Target) (Result, error) {
	switch kindOf(action) {
	case kindTerminal:
		return c.openTerminal(ctx, action)
	case kindLocal:
		if action == ActionShowLogPath {
			return c.showLogPath()
		}
		return Result{}, ErrBlocked
	case kindLifecycle:
		return c.runLifecycle(action)
	case kindRequest:
		return c.runRequest(ctx, action, target)
	default:
		return Result{}, ErrBlocked
	}
}

func (c *Controller) runLifecycle(action Action) (Result, error) {
	if c.opts.Lifecycle == nil {
		return c.failSync(action, ErrNoLifecycle)
	}
	c.mu.Lock()
	c.interruptLocked()
	c.mu.Unlock()
	if err := c.opts.Lifecycle.Quit(); err != nil {
		return c.failSync(action, err)
	}
	message := "Quit requested"
	if action == ActionQuitKeepDesktop {
		message = "Quit requested; desktop proxy kept"
	}
	c.mu.Lock()
	c.lastAction = SanitizeHeader(message)
	c.mu.Unlock()
	c.notifyNow(message)
	return Result{Status: ResultCompleted, Message: message}, nil
}

// openTerminal launches the CLI menu through the Terminal boundary. A missing
// terminal is an explicit, actionable outcome: the notification carries the
// exact manual command, and no shell or hidden fallback is attempted.
func (c *Controller) openTerminal(ctx context.Context, action Action) (Result, error) {
	if c.opts.Terminal == nil {
		return c.failSync(action, ErrTerminalUnavailable)
	}
	err := c.opts.Terminal.Open(ctx)
	if err == nil {
		message := "Terminal menu opened"
		c.mu.Lock()
		c.lastAction = message
		c.mu.Unlock()
		return Result{Status: ResultCompleted, Message: message}, nil
	}
	if !errors.Is(err, ErrNoTerminal) {
		return c.failSync(action, ErrTerminalUnavailable)
	}
	message := manualCommandNotification(c.opts.Terminal.ManualCommand())
	c.mu.Lock()
	c.lastAction = "No supported terminal found"
	c.mu.Unlock()
	c.notifyNow(message)
	return Result{}, ErrNoTerminal
}

func (c *Controller) showLogPath() (Result, error) {
	path := c.opts.Paths.Log
	if path == "" {
		c.mu.Lock()
		c.lastAction = "Log path unavailable"
		c.mu.Unlock()
		return Result{}, ErrBlocked
	}
	message := "Log: " + SanitizeHeader(path)
	c.mu.Lock()
	c.lastAction = "Log path shown"
	c.mu.Unlock()
	c.notifyNow(message)
	return Result{Status: ResultCompleted, Message: "Log path shown"}, nil
}

func (c *Controller) runRequest(ctx context.Context, action Action, target Target) (Result, error) {
	req, ok := requestFor(action, target)
	if !ok {
		return Result{}, ErrBlocked
	}
	callCtx, cancel := c.actionContext(ctx)
	defer cancel()
	env, err := c.call(callCtx, req)
	if err != nil {
		return c.failSync(action, err)
	}
	message := ""
	switch action {
	case ActionDesktopRefresh:
		var data control.DesktopData
		if err := decodeData(env, &data); err != nil {
			return c.failSync(action, err)
		}
		c.applyDesktop(desktopFromData(data))
		message = "Desktop status refreshed"
	case ActionProposalsClear:
		var data control.ProposalStatusData
		if err := decodeData(env, &data); err == nil {
			message = fmt.Sprintf("Cleared %d proposals", data.Cleared)
		}
	case ActionRuleRemove:
		var data control.RuleRemoveData
		if err := decodeData(env, &data); err == nil {
			message = fmt.Sprintf("Rule removed (%d connections closed)", data.Closed)
		}
	case ActionPoolRemove:
		var data control.PoolRemoveData
		if err := decodeData(env, &data); err == nil {
			message = fmt.Sprintf("Exit removed (%d connections closed)", data.Closed)
		}
	case ActionModeTargeted:
		message = "Mode: targeted"
	case ActionModeAllProxy:
		message = "Mode: all proxy"
	case ActionModeAllDirect:
		message = "Mode: all direct"
	case ActionStopListening:
		message = "Stopped listening"
	case ActionProposalAccept:
		message = "Proposal accepted"
		if p, ok := c.proposalSnapshot(target.ProposalID); ok && p.Kind == contract.ProposalUnroute {
			message = "Unroute accepted; rule removed"
		}
	case ActionProposalAcceptSuffix:
		message = "Proposal accepted as a domain suffix"
		if p, ok := c.proposalSnapshot(target.ProposalID); ok && p.Kind == contract.ProposalUnroute {
			message = "Unroute accepted; rule removed"
		}
	case ActionProposalReject:
		message = "Proposal rejected"
	case ActionPoolSelectAuto:
		message = "Automatic exit selection restored"
	case ActionPoolSelect:
		message = "Exit pinned"
	case ActionPoolRerank:
		message = "Pool re-ranked"
	}
	if message == "" {
		message = "Done: " + action.String()
	}
	message = SanitizeHeader(message)
	c.mu.Lock()
	c.lastAction = message
	c.mu.Unlock()
	c.notifyNow(message)
	return Result{Status: ResultCompleted, Message: message}, nil
}

func (c *Controller) proposalSnapshot(id contract.ProposalID) (Proposal, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.proposalByIDLocked(id)
}

func (c *Controller) failSync(action Action, err error) (Result, error) {
	message := "Failed: " + errorKindLabel(err)
	c.mu.Lock()
	c.lastAction = SanitizeHeader(message)
	c.mu.Unlock()
	c.notifyNow(message)
	return Result{}, err
}

// ---------- job execution ----------

func (c *Controller) jobRequest(ctx context.Context, j *job, action Action, target Target) (string, error) {
	req, ok := requestFor(action, target)
	if !ok {
		return "", ErrBlocked
	}
	if action == ActionPoolScan {
		return c.jobPoolScan(ctx, j, req)
	}
	env, err := c.call(ctx, req)
	if err != nil {
		return "", err
	}
	switch action {
	case ActionDoctor:
		var data control.DoctorData
		if err := decodeData(env, &data); err != nil {
			return "", err
		}
		checks := checksFromData(data)
		warn, failed := 0, 0
		for _, ch := range checks {
			switch ch.Status {
			case "warn":
				warn++
			case "error":
				failed++
			}
		}
		c.mu.Lock()
		c.snap.Doctor = checks
		c.mu.Unlock()
		return fmt.Sprintf("Doctor finished: %d warnings, %d errors", warn, failed), nil
	case ActionDesktopSet, ActionDesktopRestore:
		var data control.DesktopData
		if err := decodeData(env, &data); err != nil {
			return "", err
		}
		c.applyDesktop(desktopFromData(data))
		if action == ActionDesktopSet {
			return "Desktop proxy set", nil
		}
		// A key the user changed is deliberately left untouched, so a plain
		// "restored" would overstate the result. The service notice means there
		// was nothing to restore; use fixed tray text rather than caller text.
		return desktopRestoreSummary(data, "Desktop proxy"), nil
	case ActionDesktopClear:
		var data control.DesktopData
		if err := decodeData(env, &data); err != nil {
			return "", err
		}
		c.applyDesktop(desktopFromData(data))
		switch {
		case len(data.UserModified) > 0:
			return "Desktop proxy not cleared; " + userModifiedSummary(len(data.UserModified)), nil
		case len(data.Failed) > 0:
			return "Desktop proxy could not be cleared", nil
		case len(data.ClearedKeys) > 0:
			return "Desktop proxy cleared", nil
		case data.Notice != "":
			// Already disabled or nothing to clear: say so instead of claiming
			// a write that did not happen.
			return "Desktop proxy: " + SanitizeLabel(data.Notice), nil
		case data.Cleared:
			return "Desktop proxy cleared", nil
		default:
			return "Desktop proxy could not be cleared", nil
		}
	case ActionPoolRefresh:
		return "Sources refreshed", nil
	case ActionPoolTest, ActionPoolTestFirst20:
		var data control.PoolTestData
		if err := decodeData(env, &data); err != nil {
			return "", err
		}
		return fmt.Sprintf("Pool test finished (%d results)", len(data.Results)), nil
	case ActionProposalRecheck:
		return "Proposal rechecked", nil
	case ActionTunEnable, ActionTunDisable:
		var data contract.TunStatus
		if err := decodeData(env, &data); err != nil {
			return "", err
		}
		c.applyTun(tunFromStatus(data))
		if action == ActionTunDisable {
			// A successful disable normally reports off, but the contract also
			// allows an unresolved helper to leave coverage unavailable. Never
			// turn that into an "off" claim.
			switch data.State {
			case contract.TunOff:
				return "System-wide coverage off", nil
			case contract.TunStopping:
				return "System-wide coverage stopping", nil
			default:
				return "System-wide coverage not confirmed off", nil
			}
		}
		switch data.State {
		case contract.TunOn:
			return "System-wide coverage on", nil
		case contract.TunStarting:
			return "System-wide coverage starting", nil
		default:
			return "System-wide coverage not active", nil
		}
	case ActionBackup:
		return "Backup created", nil
	default:
		return "Done: " + action.String(), nil
	}
}

func (c *Controller) jobPoolScan(ctx context.Context, j *job, req control.Request) (string, error) {
	cursor := 0
	tested := 0
	total := 0
	rows := 0
	canceled := 0
	for {
		callReq := req
		callReq.Offset = cursor
		env, err := c.call(ctx, callReq)
		if err != nil {
			return "", err
		}
		var data control.PoolScanData
		if err := decodeData(env, &data); err != nil {
			return "", err
		}
		for _, row := range data.Results {
			rows++
			if row.Outcome == contract.OutcomeCanceled {
				canceled++
			}
		}
		total = data.Total
		tested += data.Tested
		c.setJobName(j, fmt.Sprintf("pool check %d/%d", tested, total))
		if data.Next < 0 || data.Tested == 0 || data.Next <= cursor {
			break
		}
		cursor = data.Next
	}
	// A page that makes no progress or a scan whose every row was canceled did
	// no useful work; never report it as a completed check.
	if total > 0 && tested == 0 {
		return "", &contract.Failure{
			Kind: contract.PoolUnavailable, Path: contract.PathPool, Op: "pool.scan",
			Message: fmt.Sprintf("pool check stopped without testing any of %d exits", total),
		}
	}
	if rows > 0 && canceled == rows {
		return "", &contract.Failure{
			Kind: contract.Canceled, Path: contract.PathPool, Op: "pool.scan",
			Message: fmt.Sprintf("all %d probe results of the pool check were canceled", canceled),
		}
	}
	if canceled > 0 {
		return fmt.Sprintf("Pool checked (%d of %d exits; %d probes canceled)", tested, total, canceled), nil
	}
	return fmt.Sprintf("Pool checked (%d exits)", tested), nil
}

func (c *Controller) setJobName(j *job, name string) {
	c.mu.Lock()
	if c.job == j {
		c.job.Name = name
	}
	c.mu.Unlock()
}

func (c *Controller) jobAutostart(ctx context.Context) (string, error) {
	c.mu.Lock()
	enabled := c.snap.Autostart.Enabled
	disabled := c.autostartDisabledLocked()
	c.mu.Unlock()
	if disabled != "" {
		return "", ErrBlocked
	}
	ctx, cancel := context.WithTimeout(ctx, c.opts.ActionTimeout)
	defer cancel()
	var status autostart.Status
	var err error
	if enabled {
		status, err = c.opts.Autostart.Disable(ctx)
	} else {
		status, err = c.opts.Autostart.Enable(ctx)
	}
	if err != nil {
		return "", err
	}
	c.applyAutostart(autostartFromData(status))
	if enabled {
		return "Start at login disabled", nil
	}
	return "Start at login enabled", nil
}

func (c *Controller) jobQuitRestore(ctx context.Context) (string, error) {
	callCtx, cancel := c.actionContext(ctx)
	env, err := c.call(callCtx, control.Request{Op: control.OpDesktopRestore})
	cancel()
	if err != nil {
		return "", err
	}
	outcome := "desktop proxy restore result unknown"
	var data control.DesktopData
	if err := decodeData(env, &data); err == nil {
		c.applyDesktop(desktopFromData(data))
		outcome = desktopRestoreSummary(data, "desktop proxy")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.opts.Lifecycle == nil {
		return "", ErrNoLifecycle
	}
	if err := c.opts.Lifecycle.Quit(); err != nil {
		return "", err
	}
	return "Quit requested; " + outcome, nil
}

// desktopRestoreSummary describes exactly what a restore result did: a no-op
// reported by the service, a partial or absent restore that left user-modified
// settings untouched, a full restore, or a success that touched no key. It never
// turns a partial or absent restore into a full-restore claim
// (docs/contracts.md §7.3).
func desktopRestoreSummary(data control.DesktopData, subject string) string {
	switch {
	case data.Notice != "":
		return subject + ": nothing to restore"
	case len(data.UserModified) > 0 && len(data.Restored) == 0:
		return subject + ": no setting restored; " + userModifiedSummary(len(data.UserModified))
	case len(data.UserModified) > 0:
		return subject + " restored; " + userModifiedSummary(len(data.UserModified))
	case len(data.Restored) > 0:
		return subject + " restored"
	default:
		return subject + ": no setting was restored"
	}
}

func userModifiedSummary(n int) string {
	if n == 1 {
		return "1 user-modified setting left unchanged"
	}
	return fmt.Sprintf("%d user-modified settings left unchanged", n)
}

// ---------- pool groups ----------

// poolGroupLocked returns one group in display order. Healthy and reachable
// rows are ordered by measured delay (zero/unknown last), so a checked list
// shows its best exit first; the other groups keep the stable user order.
func (c *Controller) poolGroupLocked(section Section) []PoolRow {
	var out []PoolRow
	for _, row := range c.snap.Pool.Rows {
		if poolGroupOf(row) == section {
			out = append(out, row)
		}
	}
	if section == SectionPoolHealthy || section == SectionPoolReachable {
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i].DelayMS, out[j].DelayMS
			if (a == 0) != (b == 0) {
				return a != 0
			}
			if a != b {
				return a < b
			}
			return false
		})
	}
	return out
}

// poolGroupOf maps one row to its menu group. Reachable is a display view of
// endpoint evidence; it does not change the record status or the selection.
func poolGroupOf(row PoolRow) Section {
	switch row.Status {
	case contract.RecordHealthy:
		return SectionPoolHealthy
	case contract.RecordCooling:
		return SectionPoolCooling
	case contract.RecordUnsupported, contract.RecordInvalid:
		return SectionPoolUnusable
	default:
		if row.Reachable {
			return SectionPoolReachable
		}
		return SectionPoolUntested
	}
}

// ---------- fingerprint ----------

type fingerprintPayload struct {
	Engine     EngineState
	Detail     string
	Status     Status
	Rules      []Rule
	Pool       PoolView
	Proposals  []Proposal
	Desktop    DesktopView
	Autostart  AutostartView
	Env        EnvView
	Config     ConfigView
	Paths      Paths
	Doctor     []Check
	Job        *Job
	LastAction string
	Restarting bool
	IsLoaded   bool
	ListsKnown bool
}

func (c *Controller) menuFingerprintLocked(engine EngineState) string {
	payload := fingerprintPayload{
		Engine:     engine,
		Detail:     c.snap.EngineDetail,
		Status:     c.snap.Status,
		Rules:      c.snap.Rules,
		Pool:       c.snap.Pool,
		Proposals:  c.snap.Proposals,
		Desktop:    c.snap.Desktop,
		Autostart:  c.snap.Autostart,
		Env:        c.snap.Env,
		Config:     c.snap.Config,
		Paths:      c.snap.Paths,
		Doctor:     c.snap.Doctor,
		Job:        copyJob(c.job),
		LastAction: c.lastAction,
		Restarting: c.restarting,
		IsLoaded:   c.snap.Engine != EngineLoading,
		ListsKnown: c.snap.ListsKnown,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "marshal-error"
	}
	return hashBytes(raw)
}

func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
