package trayui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"routelatch/internal/autostart"
	"routelatch/internal/contract"
	"routelatch/internal/control"
)

// Caller is the management boundary. *control.Client satisfies it directly.
// The implementation is responsible for the protocol schema version and the
// per-operation deadlines; the controller adds only the short read deadline
// documented for polling (§7.3).
type Caller interface {
	Call(ctx context.Context, req control.Request) (contract.Envelope, error)
}

// The production adapters satisfy these boundaries by construction.
var (
	_ Caller           = (*control.Client)(nil)
	_ AutostartManager = (*autostart.Manager)(nil)
)

// Lifecycle is the supervisor boundary for the two process-level requests and
// the supervisor-to-tray events. An internal/app adapter implements it; the
// controller never restarts the engine in-process (D26/R-4).
//
// Quit and RestartEngine enqueue a bounded request and return without waiting
// for the engine. Restart success is only the restarting-then-ready event
// sequence in Events, never the return value.
type Lifecycle interface {
	Quit() error
	RestartEngine() error
	// Events is a bounded channel. The binder owns reading it and forwards
	// each value to HandleEvent; the supervisor side must never block on it.
	Events() <-chan LifecycleEvent
}

// LifecycleEventKind is the closed supervisor event set (§7.5).
type LifecycleEventKind int

const (
	LifecycleRestarting LifecycleEventKind = iota + 1
	LifecycleReady
)

func (k LifecycleEventKind) String() string {
	switch k {
	case LifecycleRestarting:
		return "restarting"
	case LifecycleReady:
		return "ready"
	default:
		return "unknown"
	}
}

// LifecycleEvent is one supervisor->tray event. No payload is needed: these
// are ordering signals, not business data.
type LifecycleEvent struct {
	Kind LifecycleEventKind
}

// Notification is already sanitized and bounded. Body never contains a
// destination, URL, credential or raw caller message.
type Notification struct {
	Title string
	Body  string
}

// Notifier receives completed action/job outcomes only. Polling and rejected
// clicks do not notify.
type Notifier interface {
	Notify(Notification)
}

// Terminal is the terminal-menu boundary. Open starts `<exe> [--data-dir
// <dir>] menu` in one allowlisted terminal emulator; the implementation owns
// the fixed allowlist and the explicit argv. There is no shell, no $TERMINAL
// lookup and no user text in that argv (docs/contracts.md §7.3).
//
// ManualCommand is the exact argv to show the user when Open reports
// ErrNoTerminal. It never carries a destination, URL or credential.
type Terminal interface {
	Open(ctx context.Context) error
	ManualCommand() string
}

// AutostartManager is the local XDG-autostart boundary. *autostart.Manager
// satisfies it; no management socket operation is involved.
type AutostartManager interface {
	Status(ctx context.Context) (autostart.Status, error)
	Enable(ctx context.Context) (autostart.Status, error)
	Disable(ctx context.Context) (autostart.Status, error)
}

// Errors returned by Trigger/Refresh. They are safe to show as short reasons;
// none contains caller text.
var (
	ErrUnavailable = errors.New("the engine is unavailable")
	ErrBusy        = errors.New("another job is already running")
	ErrBlocked     = errors.New("the action is not available in this state")
	ErrNoPending   = errors.New("there is no pending confirmation")
	ErrNoLifecycle = errors.New("the lifecycle channel is not configured")

	// ErrNoTerminal means none of the allowlisted terminals is on PATH; the
	// controller reports the exact manual command instead of starting a
	// shell or a hidden fallback. ErrTerminalUnavailable is any other launch
	// failure (a terminal exists but the process could not start).
	ErrNoTerminal          = errors.New("no supported terminal was found")
	ErrTerminalUnavailable = errors.New("the terminal could not be started")
)

// EnvelopeError is a management result with OK=false. It is deliberately
// distinct from a transport error: the server answered, but the operation did
// not succeed (for example a StateLocked or Timeout kind). Message is kept for
// callers that need the documented, already-sanitized text, but Error() only
// names the kind so a log line cannot carry a payload.
type EnvelopeError struct {
	Kind    contract.ErrorKind
	Message string
}

func (e *EnvelopeError) Error() string {
	return "management operation failed: " + string(e.Kind)
}

func asEnvelopeError(err error, target **EnvelopeError) bool {
	return errors.As(err, target)
}

func isUnavailable(err error) bool { return errors.Is(err, ErrUnavailable) }
func isCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}
func isTimeout(err error) bool { return errors.Is(err, context.DeadlineExceeded) }

// call sends one request and enforces the OK flag. Transport failures are
// wrapped with ErrUnavailable; an error envelope becomes *EnvelopeError.
func (c *Controller) call(ctx context.Context, req control.Request) (contract.Envelope, error) {
	env, err := c.opts.Caller.Call(ctx, req)
	if err != nil {
		return contract.Envelope{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !env.OK {
		kind := contract.Internal
		message := ""
		if env.Error != nil {
			if env.Error.Kind != "" {
				kind = env.Error.Kind
			}
			message = SanitizeHeader(env.Error.Message)
		}
		return env, &EnvelopeError{Kind: kind, Message: message}
	}
	return env, nil
}

func (c *Controller) readContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, c.opts.CallTimeout)
}

func (c *Controller) actionContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, c.opts.ActionTimeout)
}

// ---------- refresh ----------

type coreLoad struct {
	engine    EngineState
	detail    string
	status    Status
	rules     []Rule
	pool      PoolView
	proposals []Proposal
}

type statusLoad struct {
	engine EngineState
	detail string
	status Status
}

type listLoad struct {
	rules     []Rule
	pool      PoolView
	proposals []Proposal
}

func unavailableCore(detail string) coreLoad {
	return coreLoad{engine: EngineUnavailable, detail: SanitizeLabel(detail)}
}

// loadStatus reads only `status`. The two-second poll uses this; lists have a
// separate slower cadence (§7.3).
func (c *Controller) loadStatus(ctx context.Context) (statusLoad, error) {
	readCtx, cancel := c.readContext(ctx)
	env, err := c.call(readCtx, control.Request{Op: control.OpStatus})
	cancel()
	if err != nil {
		return statusLoad{engine: EngineUnavailable, detail: SanitizeLabel(errorKindLabel(err))}, err
	}
	var statusData control.StatusData
	if err := decodeData(env, &statusData); err != nil {
		return statusLoad{engine: EngineUnavailable, detail: SanitizeLabel(errorKindLabel(err))}, err
	}
	out := statusLoad{status: statusFromData(statusData)}
	switch {
	case statusData.Mode == contract.ModeStopped:
		out.engine = EngineStopped
	case !statusData.Ready:
		out.engine = EngineStarting
	default:
		out.engine = EngineReady
	}
	return out, nil
}

// loadLists reads the three list payloads. A status/list failure is the
// documented "engine unavailable" state; stale lists are never presented as
// current (§7.3).
func (c *Controller) loadLists(ctx context.Context) (listLoad, error) {
	readCtx, cancel := c.readContext(ctx)
	env, err := c.call(readCtx, control.Request{Op: control.OpRulesList})
	cancel()
	if err != nil {
		return listLoad{}, err
	}
	var rules control.RulesListData
	if err := decodeData(env, &rules); err != nil {
		return listLoad{}, err
	}
	pool, err := c.loadPool(ctx)
	if err != nil {
		return listLoad{}, err
	}
	readCtx, cancel = c.readContext(ctx)
	env, err = c.call(readCtx, control.Request{Op: control.OpProposalsList})
	cancel()
	if err != nil {
		return listLoad{}, err
	}
	var proposals control.ProposalsData
	if err := decodeData(env, &proposals); err != nil {
		return listLoad{}, err
	}
	return listLoad{rules: rulesFromData(rules), pool: pool, proposals: proposalsFromData(proposals)}, nil
}

// loadCore reads status and all list data for a full refresh.
func (c *Controller) loadCore(ctx context.Context) (coreLoad, error) {
	st, err := c.loadStatus(ctx)
	if err != nil {
		return unavailableCore(errorKindLabel(err)), err
	}
	lists, err := c.loadLists(ctx)
	if err != nil {
		return unavailableCore(errorKindLabel(err)), err
	}
	return coreLoad{
		engine: st.engine, detail: st.detail, status: st.status,
		rules: lists.rules, pool: lists.pool, proposals: lists.proposals,
	}, nil
}

// loadPool fetches every page the service has. The service's default is 100
// rows; the tray asks for 2000 per request so a fixture of a few hundred rows
// needs one round trip and larger pools still finish. Pagination for display
// is local and fixed at PageSize rows.
func (c *Controller) loadPool(ctx context.Context) (PoolView, error) {
	const fetchLimit = 2000
	var out PoolView
	for offset := 0; ; {
		readCtx, cancel := c.readContext(ctx)
		env, err := c.call(readCtx, control.Request{Op: control.OpPoolList, Limit: fetchLimit, Offset: offset})
		cancel()
		if err != nil {
			return PoolView{}, err
		}
		var page control.PoolListData
		if err := decodeData(env, &page); err != nil {
			return PoolView{}, err
		}
		view := poolFromData(page)
		out.Rows = append(out.Rows, view.Rows...)
		out.Counts = view.Counts
		out.Truncated = view.Truncated
		if !page.Truncated || len(page.Records) == 0 {
			break
		}
		offset += len(page.Records)
		if offset > 200000 {
			return PoolView{}, &EnvelopeError{Kind: contract.Internal, Message: "pool list exceeds the tray bound"}
		}
	}
	return out, nil
}

func (c *Controller) storeCore(core coreLoad) {
	now := c.opts.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap.LoadedAt = now
	if core.engine == EngineUnavailable {
		c.storeUnavailableLocked(core.detail)
		return
	}
	c.snap.Engine = core.engine
	c.snap.EngineDetail = core.detail
	c.snap.Status = core.status
	c.snap.Rules = core.rules
	c.snap.Pool = core.pool
	c.snap.Proposals = core.proposals
	c.snap.ListsKnown = true
}

func (c *Controller) storeStatus(st statusLoad) {
	now := c.opts.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap.LoadedAt = now
	if st.engine == EngineUnavailable {
		c.storeUnavailableLocked(st.detail)
		return
	}
	c.snap.Engine = st.engine
	c.snap.EngineDetail = st.detail
	c.snap.Status = st.status
}

func (c *Controller) storeLists(lists listLoad, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap.LoadedAt = c.opts.Now()
	if err != nil {
		c.storeUnavailableLocked(errorKindLabel(err))
		return
	}
	c.snap.Rules = lists.rules
	c.snap.Pool = lists.pool
	c.snap.Proposals = lists.proposals
	c.snap.ListsKnown = true
}

func (c *Controller) storeUnavailableLocked(detail string) {
	detail = SanitizeLabel(detail)
	c.snap.Engine = EngineUnavailable
	c.snap.EngineDetail = detail
	// Dynamic data is cleared: stale rows must not look current.
	c.snap.Status = Status{}
	c.snap.Rules = nil
	c.snap.Pool = PoolView{}
	c.snap.Proposals = nil
	c.snap.Doctor = nil
	c.snap.ListsKnown = false
	c.snap.Env = EnvView{Reason: detail}
	c.pending = nil
	c.interruptLocked()
}

func (c *Controller) loadEnv(ctx context.Context) (EnvView, error) {
	readCtx, cancel := c.readContext(ctx)
	defer cancel()
	env, err := c.call(readCtx, control.Request{Op: control.OpEnv})
	if err != nil {
		return EnvView{Reason: errorKindLabel(err)}, err
	}
	var data control.EnvData
	if err := decodeData(env, &data); err != nil {
		return EnvView{Reason: errorKindLabel(err)}, err
	}
	return envFromData(data), nil
}

func (c *Controller) loadConfig(ctx context.Context) (ConfigView, Paths, error) {
	readCtx, cancel := c.readContext(ctx)
	env, err := c.call(readCtx, control.Request{Op: control.OpConfigGet})
	cancel()
	if err != nil {
		return ConfigView{}, Paths{}, err
	}
	var get control.ConfigData
	if err := decodeData(env, &get); err != nil {
		return ConfigView{}, Paths{}, err
	}
	readCtx, cancel = c.readContext(ctx)
	env, err = c.call(readCtx, control.Request{Op: control.OpConfigPath})
	cancel()
	if err != nil {
		return ConfigView{}, Paths{}, err
	}
	var pathData control.ConfigPathData
	if err := decodeData(env, &pathData); err != nil {
		return ConfigView{}, Paths{}, err
	}
	paths := pathsFromData(pathData, c.opts.Paths.Log)
	if paths.Log == "" {
		paths.Log = SanitizeHeader(c.opts.Paths.Log)
	}
	return configFromData(get), paths, nil
}

func (c *Controller) loadDesktop(ctx context.Context) (DesktopView, error) {
	readCtx, cancel := c.readContext(ctx)
	defer cancel()
	env, err := c.call(readCtx, control.Request{Op: control.OpDesktopStatus})
	if err != nil {
		return DesktopView{}, err
	}
	var data control.DesktopData
	if err := decodeData(env, &data); err != nil {
		return DesktopView{}, err
	}
	return desktopFromData(data), nil
}

func (c *Controller) loadAutostart(ctx context.Context) (AutostartView, error) {
	if c.opts.Autostart == nil {
		return AutostartView{}, errors.New("autostart manager is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, c.opts.ActionTimeout)
	defer cancel()
	status, err := c.opts.Autostart.Status(ctx)
	if err != nil {
		return AutostartView{}, err
	}
	return autostartFromData(status), nil
}

// RefreshAll loads the complete snapshot. Management data is required; the
// local desktop/autostart/env/config reads are best-effort because a tray must
// still be able to quit when the socket is gone.
func (c *Controller) RefreshAll(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	core, coreErr := c.loadCore(ctx)
	c.storeCore(core)
	if desktop, err := c.loadDesktop(ctx); err != nil {
		c.applyDesktopError()
	} else {
		c.applyDesktop(desktop)
	}
	if autostart, err := c.loadAutostart(ctx); err != nil {
		c.applyAutostartError()
	} else {
		c.applyAutostart(autostart)
	}
	if env, err := c.loadEnv(ctx); err != nil {
		c.applyEnv(env)
	} else {
		c.applyEnv(env)
	}
	if cfg, paths, err := c.loadConfig(ctx); err == nil {
		c.applyConfig(cfg, paths)
	}
	return coreErr
}

// RefreshCore loads status and the three lists together. It is the immediate
// refresh after an action; the periodic poll uses RefreshStatus and
// RefreshLists separately.
func (c *Controller) RefreshCore(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	core, err := c.loadCore(ctx)
	c.storeCore(core)
	if err == nil {
		// The launcher environment follows listener readiness; refresh it with
		// the action so a resumed engine does not keep the old stopped reason.
		env, _ := c.loadEnv(ctx)
		c.applyEnv(env)
	}
	return err
}

// RefreshStatus is the two-second poll: status only. A failed status is the
// unavailable state and clears dynamic lists.
func (c *Controller) RefreshStatus(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	st, err := c.loadStatus(ctx)
	c.storeStatus(st)
	return err
}

// RefreshLists is the slower list poll (§7.3: status every 2s, lists every
// 15s). A list failure is the unavailable state, not a stale-list success.
func (c *Controller) RefreshLists(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	lists, err := c.loadLists(ctx)
	c.storeLists(lists, err)
	if err == nil {
		env, _ := c.loadEnv(ctx)
		c.applyEnv(env)
	}
	return err
}

// RefreshDesktop re-reads the expensive desktop-proxy status. It is called at
// start, after an action and before a Stop/Restart/Quit confirmation, not by
// the polling loop.
func (c *Controller) RefreshDesktop(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	desktop, err := c.loadDesktop(ctx)
	if err != nil {
		c.applyDesktopError()
		return err
	}
	c.applyDesktop(desktop)
	return nil
}

// RefreshAutostart re-reads the local autostart entry.
func (c *Controller) RefreshAutostart(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	autostart, err := c.loadAutostart(ctx)
	if err != nil {
		c.applyAutostartError()
		return err
	}
	c.applyAutostart(autostart)
	return nil
}

func (c *Controller) applyDesktop(v DesktopView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap.Desktop = v
}

func (c *Controller) applyDesktopError() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap.Desktop.Err = true
}

func (c *Controller) applyAutostart(v AutostartView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap.Autostart = v
}

func (c *Controller) applyAutostartError() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap.Autostart.Err = true
}

func (c *Controller) applyEnv(v EnvView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap.Env = v
}

func (c *Controller) applyConfig(v ConfigView, paths Paths) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap.Config = v
	c.snap.Paths = paths
}

// refreshAfterJob runs after a successful mutation so lists reflect it
// immediately without blocking the D-Bus handler (the job already runs in a
// background goroutine).
func (c *Controller) refreshAfterJob() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = c.RefreshCore(ctx)
}
