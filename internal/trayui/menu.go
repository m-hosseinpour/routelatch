package trayui

import (
	"fmt"
	"strings"

	"routelatch/internal/contract"
)

// PageSize is the fixed local page size for pool, rules and proposals
// (docs/contracts.md §7.3 allows 20–25; the exact value is fixed and tested).
const PageSize = 20

// NodeKind is the limited node vocabulary the D-Bus binder needs.
type NodeKind int

const (
	NodeItem NodeKind = iota
	NodeSubmenu
)

// CheckKind lets a binder render radio/checkmark state when its host supports
// it; the checked state is also available for a textual fallback.
type CheckKind int

const (
	CheckNone CheckKind = iota
	CheckRadio
	CheckCheckbox
)

// Node is one menu item. Key is stable across rebuilds for the same logical
// item. Label is already sanitized and bounded. Action and Target are the only
// click inputs; labels are never parsed.
type Node struct {
	Key      string
	Label    string
	Kind     NodeKind
	Children []Node

	Action Action
	Target Target

	Enabled bool
	Visible bool
	Checked bool
	Check   CheckKind
}

func item(key, label string) Node {
	return Node{Key: key, Label: SanitizeLabel(label), Kind: NodeItem, Enabled: true, Visible: true}
}

func disabledItem(key, label string) Node {
	return Node{Key: key, Label: SanitizeLabel(label), Kind: NodeItem, Enabled: false, Visible: true}
}

func submenu(key, label string, children ...Node) Node {
	return Node{Key: key, Label: SanitizeLabel(label), Kind: NodeSubmenu, Children: children, Enabled: true, Visible: true}
}

func (c *Controller) actionItem(key, label string, action Action, target Target) Node {
	n := item(key, label)
	n.Action = action
	n.Target = target
	n.Enabled = c.allowedLocked(action, target) == nil
	return n
}

// buildMenuLocked builds the deterministic contract tree. The root order is
// fixed in every state; state-dependent items are disabled or invisible, never
// silently removed.
func (c *Controller) buildMenuLocked(engine EngineState) Node {
	if engine == EngineLoading {
		// Before the first snapshot the contents are unknown; the contract's
		// minimal menu is the header plus Quit, with no management actions.
		root := []Node{
			disabledItem("status", statusLabel(c.snap, engine)),
			c.quitMenuLocked(),
		}
		return submenu("root", "RouteLatch", root...)
	}
	root := make([]Node, 0, 18)
	root = append(root, disabledItem("status", statusLabel(c.snap, engine)))

	if c.pending != nil {
		confirm := item("pending.confirm", "Confirm: "+c.pending.Label)
		confirm.Action = ActionConfirm
		cancel := item("pending.cancel", "Cancel")
		cancel.Action = ActionCancel
		root = append(root, confirm, cancel)
	}
	if c.job != nil {
		label := "Job: " + c.job.Name + " running"
		if c.job.Interrupted {
			label = "Job: " + c.job.Name + " interrupted"
		}
		root = append(root, disabledItem("job", label))
	}

	root = append(root, c.modeMenuLocked(engine))
	root = append(root, c.proposalsMenuLocked(engine))
	root = append(root, c.rulesMenuLocked(engine))
	root = append(root, c.poolMenuLocked(engine))
	root = append(root, c.desktopMenuLocked(engine))
	root = append(root, c.diagnosticsMenuLocked(engine))
	root = append(root, c.dataMenuLocked())
	root = append(root, c.settingsMenuLocked())
	root = append(root, c.advancedMenuLocked())

	if engine == EngineStopped && c.resumeBlockedLocked() {
		// resumeBlocked means restart is the only way back. The extra root item
		// uses the same action as Advanced -> Restart engine.
		root = append(root, c.actionItem("restart.resume", "Restart engine to resume…", ActionRestartEngine, Target{}))
	}

	last := "Last action: none"
	if c.lastAction != "" {
		last = "Last action: " + c.lastAction
	}
	root = append(root, disabledItem("last_action", last))
	root = append(root, c.quitMenuLocked())
	return submenu("root", "RouteLatch", root...)
}

func statusLabel(s Snapshot, engine EngineState) string {
	switch engine {
	case EngineLoading:
		return "Loading…"
	case EngineUnavailable:
		return "Engine unavailable; retrying…"
	case EngineRestarting:
		return "Restarting engine…"
	case EngineStarting:
		return "Starting engine…"
	case EngineStopped:
		switch {
		case s.Status.ResumeRequiresRestart == nil:
			return "Stopped · listeners closed · restart recommended"
		case *s.Status.ResumeRequiresRestart:
			return "Stopped · listeners closed · restart engine"
		default:
			return "Stopped · choose a mode to resume"
		}
	case EngineReady:
		switch s.Status.Mode {
		case contract.ModeTargeted:
			return fmt.Sprintf("Targeted · %d usable · %d open", s.Status.Pool.Usable, s.Status.ConnectionsOpen)
		case contract.ModeProxyAll:
			return fmt.Sprintf("All proxy · %d usable · %d open", s.Status.Pool.Usable, s.Status.ConnectionsOpen)
		case contract.ModeDirectAll:
			return fmt.Sprintf("All direct · %d open", s.Status.ConnectionsOpen)
		default:
			return "Ready · " + string(s.Status.Mode)
		}
	default:
		return "Unknown state"
	}
}

func (c *Controller) modeMenuLocked(engine EngineState) Node {
	children := []Node{
		c.modeNode("mode.targeted", "Targeted", ActionModeTargeted, contract.ModeTargeted),
		c.modeNode("mode.all-proxy", "All proxy", ActionModeAllProxy, contract.ModeProxyAll),
		c.modeNode("mode.all-direct", "All direct", ActionModeAllDirect, contract.ModeDirectAll),
		c.actionItem("mode.stop", "Stop listening…", ActionStopListening, Target{}),
	}
	return submenu("mode", "Mode", children...)
}

func (c *Controller) modeNode(key, label string, action Action, mode contract.Mode) Node {
	n := c.actionItem(key, label, action, Target{Mode: mode})
	if c.snap.Engine == EngineReady && c.snap.Status.Mode == mode {
		n.Checked = true
		n.Check = CheckRadio
	}
	return n
}

func (c *Controller) proposalsMenuLocked(engine EngineState) Node {
	pending := make([]Proposal, 0)
	decided := make([]Proposal, 0)
	for _, p := range c.snap.Proposals {
		if p.Status == contract.ProposalPending {
			pending = append(pending, p)
		} else {
			decided = append(decided, p)
		}
	}
	children := make([]Node, 0, 8)
	page, pages := c.pageLocked(SectionProposalsPending)
	if !c.snap.ListsKnown {
		children = append(children, disabledItem("proposals.pending.loading", "Proposal list refreshing…"))
	} else if len(pending) == 0 {
		children = append(children, disabledItem("proposals.pending.empty", "No pending proposals"))
	} else {
		for _, p := range pageSlice(pending, page) {
			children = append(children, c.proposalNode(p))
		}
		children = append(children, c.pageNodes("proposals.pending", SectionProposalsPending, page, pages)...)
	}

	decidedChildren := make([]Node, 0, PageSize+3)
	dpage, dpages := c.pageLocked(SectionProposalsDecided)
	if !c.snap.ListsKnown {
		decidedChildren = append(decidedChildren, disabledItem("proposals.decided.loading", "Proposal list refreshing…"))
	} else if len(decided) == 0 {
		decidedChildren = append(decidedChildren, disabledItem("proposals.decided.empty", "No decided or expired proposals"))
	} else {
		for _, p := range pageSlice(decided, dpage) {
			decidedChildren = append(decidedChildren, disabledItem("proposals.decided."+stableKeyPart(string(p.ID)), proposalDecidedLabel(p)))
		}
		decidedChildren = append(decidedChildren, c.pageNodes("proposals.decided", SectionProposalsDecided, dpage, dpages)...)
	}
	decidedLabel := fmt.Sprintf("Show decided (%d)", len(decided))
	if !c.snap.ListsKnown {
		decidedLabel = "Show decided"
	}
	children = append(children, submenu("proposals.decided", decidedLabel, decidedChildren...))
	clear := c.actionItem("proposals.clear", "Clear decided/expired…", ActionProposalsClear, Target{})
	clear.Enabled = clear.Enabled && c.snap.ListsKnown
	children = append(children, clear)
	proposalsLabel := fmt.Sprintf("Proposals (%d)", len(pending))
	if !c.snap.ListsKnown {
		proposalsLabel = "Proposals"
	}
	return submenu("proposals", proposalsLabel, children...)
}

func (c *Controller) proposalNode(p Proposal) Node {
	key := "proposals.pending." + stableKeyPart(string(p.ID))
	target := Target{ProposalID: p.ID}
	children := []Node{
		c.actionItem(key+".accept", "Accept", ActionProposalAccept, target),
		c.actionItem(key+".reject", "Reject", ActionProposalReject, target),
	}
	children = append(children, c.actionItem(key+".recheck", "Recheck", ActionProposalRecheck, target))
	return submenu(key, proposalLabel(p), children...)
}

func proposalLabel(p Proposal) string {
	kind := string(p.Kind)
	if kind == "" {
		kind = "route"
	}
	label := kind
	if p.Destination != "" {
		label += " · " + p.Destination
	}
	if p.Note != "" {
		label += " · " + p.Note
	}
	return SanitizeLabel(label)
}

func proposalDecidedLabel(p Proposal) string {
	kind := string(p.Kind)
	if kind == "" {
		kind = "route"
	}
	label := fmt.Sprintf("%s · %s", p.Status, kind)
	if p.Destination != "" {
		label += " · " + p.Destination
	}
	return SanitizeLabel(label)
}

func (c *Controller) rulesMenuLocked(engine EngineState) Node {
	page, pages := c.pageLocked(SectionRules)
	children := make([]Node, 0, PageSize+5)
	if !c.snap.ListsKnown {
		children = append(children, disabledItem("rules.loading", "Rules list refreshing…"))
	} else if len(c.snap.Rules) == 0 {
		children = append(children, disabledItem("rules.empty", "No approved rules"))
	} else {
		for _, r := range pageSlice(c.snap.Rules, page) {
			children = append(children, c.ruleNode(r))
		}
		children = append(children, c.pageNodes("rules", SectionRules, page, pages)...)
	}
	children = append(children, c.actionItem("rules.add.terminal", "Add rule… (Terminal menu)", ActionTerminalMenu, Target{}))
	label := fmt.Sprintf("Approved rules (%d)", len(c.snap.Rules))
	if !c.snap.ListsKnown {
		label = "Approved rules"
	}
	return submenu("rules", label, children...)
}

func (c *Controller) ruleNode(r Rule) Node {
	key := "rules." + stableKeyPart(string(r.ID))
	target := Target{RuleID: r.ID}
	details := submenu(key+".details", "Details",
		disabledItem(key+".details.id", "ID: "+shortID(string(r.ID))),
		disabledItem(key+".details.kind", "Kind: "+string(r.Kind)),
		disabledItem(key+".details.operand", "Operand: "+r.Operand),
		disabledItem(key+".details.note", "Note: "+firstNonEmpty(r.Note, "—")),
		disabledItem(key+".details.added", "Added: "+r.AddedAt.UTC().Format("2006-01-02 15:04")),
	)
	children := []Node{
		c.actionItem(key+".remove", "Remove…", ActionRuleRemove, target),
		details,
	}
	return submenu(key, ruleLabel(r), children...)
}

func ruleLabel(r Rule) string {
	label := r.Operand
	if r.Kind != "" {
		label += " · " + string(r.Kind)
	}
	return SanitizeLabel(label)
}

func (c *Controller) poolMenuLocked(engine EngineState) Node {
	counts := c.snap.Pool.Counts
	summary := fmt.Sprintf("%d records · %d usable · %d healthy · %d cooling", counts.Total, counts.Usable, counts.Healthy, counts.Cooling)
	if !c.snap.ListsKnown {
		summary = "Pool list refreshing…"
	} else if counts.Total == 0 {
		summary = "No exits; add or refresh from the Terminal menu"
	}
	children := []Node{disabledItem("pool.summary", summary)}

	auto := c.actionItem("pool.auto", "Auto select", ActionPoolSelectAuto, Target{})
	if c.snap.Status.SelectedExit == "" {
		auto.Checked = true
		auto.Check = CheckRadio
	}
	children = append(children, auto)
	if c.snap.Status.SelectedExit != "" {
		pinned := c.actionItem("pool.pinned", "Pinned: "+shortID(string(c.snap.Status.SelectedExit)), ActionPoolSelect, Target{ExitID: c.snap.Status.SelectedExit})
		pinned.Checked = true
		pinned.Check = CheckRadio
		children = append(children, pinned)
	}

	for _, group := range []struct {
		section Section
		label   string
		count   int
	}{
		{SectionPoolHealthy, "Healthy", counts.Healthy},
		{SectionPoolUntested, "Untested", counts.Unknown},
		{SectionPoolCooling, "Cooling", counts.Cooling},
		{SectionPoolUnusable, "Unsupported/Invalid", counts.Unsupported + counts.Invalid},
	} {
		children = append(children, c.poolGroupMenu(group.section, group.label, group.count))
	}

	children = append(children,
		c.actionItem("pool.refresh", "Refresh all sources", ActionPoolRefresh, Target{}),
		c.actionItem("pool.rerank", "Re-rank now", ActionPoolRerank, Target{}),
	)
	if c.snap.Status.SelectedExit != "" {
		children = append(children, c.actionItem("pool.test.selected", "Test selected exit", ActionPoolTest, Target{ExitID: c.snap.Status.SelectedExit}))
	}
	// The label carries the service cap so the menu never claims to test more
	// than pool.test accepts (docs/contracts.md §7.3, §4.6).
	children = append(children, c.actionItem("pool.test.first20", fmt.Sprintf("Test first %d usable", contract.MaxPoolTestExits), ActionPoolTestFirst20, Target{}))
	children = append(children, c.actionItem("pool.import.terminal", "Add/import… (Terminal menu)", ActionTerminalMenu, Target{}))
	return submenu("pool", "Pool", children...)
}

func (c *Controller) poolGroupMenu(section Section, label string, count int) Node {
	page, pages := c.pageLocked(section)
	key := "pool.group." + string(section)
	children := make([]Node, 0, PageSize+4)
	rows := c.poolGroupLocked(section)
	title := fmt.Sprintf("%s (%d)", label, count)
	if !c.snap.ListsKnown {
		title = label
		children = append(children, disabledItem(key+".loading", "Pool list refreshing…"))
		return submenu(key, title, children...)
	}
	if len(rows) == 0 {
		children = append(children, disabledItem(key+".empty", "No "+strings.ToLower(label)+" exits"))
	} else {
		for _, row := range pageSlice(rows, page) {
			children = append(children, c.poolRowNode(row))
		}
		children = append(children, c.pageNodes(key, section, page, pages)...)
	}
	return submenu(key, title, children...)
}

func (c *Controller) poolRowNode(row PoolRow) Node {
	key := "pool.row." + stableKeyPart(string(row.ID))
	target := Target{ExitID: row.ID}
	selectable := !row.InsecureTLS && (row.Status == contract.RecordHealthy || row.Status == contract.RecordUnknown)
	children := make([]Node, 0, 4)
	selectNode := c.actionItem(key+".select", "Select exit", ActionPoolSelect, target)
	selectNode.Enabled = selectNode.Enabled && selectable
	children = append(children, selectNode)
	testNode := c.actionItem(key+".test", "Test this exit", ActionPoolTest, target)
	testNode.Enabled = testNode.Enabled && selectable
	children = append(children, testNode)
	children = append(children, c.actionItem(key+".remove", "Remove…", ActionPoolRemove, target))
	return submenu(key, poolRowLabel(row), children...)
}

func poolRowLabel(row PoolRow) string {
	name := row.DisplayName
	if name == "" {
		name = "exit " + shortID(string(row.ID))
	}
	label := fmt.Sprintf("%s · %s · %s", name, row.Protocol, row.Status)
	if row.Reason != "" {
		label += " · " + row.Reason
	}
	return SanitizeLabel(label)
}

func (c *Controller) desktopMenuLocked(engine EngineState) Node {
	d := c.snap.Desktop
	children := []Node{disabledItem("desktop.status", desktopStatusLabel(d))}
	children = append(children, c.actionItem("desktop.refresh", "Refresh status", ActionDesktopRefresh, Target{}))
	children = append(children, c.actionItem("desktop.set", "Set/Re-apply desktop proxy…", ActionDesktopSet, Target{}))
	children = append(children, c.actionItem("desktop.restore", "Restore previous desktop proxy…", ActionDesktopRestore, Target{}))
	if d.Err {
		children = append(children, disabledItem("desktop.reason", "Desktop proxy status could not be read; restore/keep is an explicit choice at Quit"))
	} else if d.Known && !d.Supported {
		children = append(children, disabledItem("desktop.reason", "This desktop environment is not supported; only GNOME and KDE are changed"))
	}
	return submenu("desktop", "Desktop proxy", children...)
}

func desktopStatusLabel(d DesktopView) string {
	switch {
	case d.Err:
		return "Desktop proxy: unknown (status read failed)"
	case !d.Known:
		return "Desktop proxy: loading…"
	case !d.Supported:
		return "Desktop proxy: unsupported" + environmentSuffix(d)
	case d.Applied:
		label := "Desktop proxy: applied" + environmentSuffix(d)
		if len(d.Conflict) > 0 {
			label += " · conflict: " + strings.Join(d.Conflict, ", ")
		}
		return SanitizeLabel(label)
	default:
		return "Desktop proxy: not applied" + environmentSuffix(d)
	}
}

func environmentSuffix(d DesktopView) string {
	if d.Environment == "" {
		return ""
	}
	return " (" + d.Environment + ")"
}

func (c *Controller) diagnosticsMenuLocked(engine EngineState) Node {
	children := make([]Node, 0, 6)
	children = append(children, c.actionItem("diagnostics.doctor", "Run doctor", ActionDoctor, Target{}))

	results := make([]Node, 0, len(c.snap.Doctor)+1)
	if len(c.snap.Doctor) == 0 {
		results = append(results, disabledItem("diagnostics.results.empty", "No doctor results yet"))
	} else {
		for i, ch := range c.snap.Doctor {
			label := fmt.Sprintf("[%s] %s: %s", ch.Status, ch.Name, ch.Detail)
			results = append(results, disabledItem(fmt.Sprintf("diagnostics.check.%d", i), label))
		}
	}
	children = append(children, submenu("diagnostics.results", "Doctor results", results...))

	warnings := c.snap.Status.Warnings
	warnChildren := make([]Node, 0, len(warnings)+1)
	if len(warnings) == 0 {
		warnChildren = append(warnChildren, disabledItem("diagnostics.warnings.empty", "No warnings"))
	} else {
		for i, w := range warnings {
			warnChildren = append(warnChildren, disabledItem(fmt.Sprintf("diagnostics.warning.%d", i), w))
		}
	}
	children = append(children, submenu("diagnostics.warnings", fmt.Sprintf("Warnings (%d)", len(warnings)), warnChildren...))
	children = append(children, c.actionItem("diagnostics.logpath", "Show log path", ActionShowLogPath, Target{}))
	children = append(children, c.environmentMenu())
	return submenu("diagnostics", "Diagnostics", children...)
}

func (c *Controller) environmentMenu() Node {
	env := c.snap.Env
	children := make([]Node, 0, 5)
	if !env.Available {
		reason := "Environment unavailable"
		if env.Reason != "" {
			reason += " (" + env.Reason + ")"
		}
		children = append(children, disabledItem("diagnostics.env.unavailable", reason))
		return submenu("diagnostics.env", "Environment", children...)
	}
	for _, kv := range []struct{ key, value string }{
		{"HTTP_PROXY", env.HTTPProxy},
		{"HTTPS_PROXY", env.HTTPSProxy},
		{"ALL_PROXY", env.ALLProxy},
		{"NO_PROXY", env.NoProxy},
	} {
		children = append(children, disabledItem("diagnostics.env."+kv.key, kv.key+"="+kv.value))
	}
	return submenu("diagnostics.env", "Environment", children...)
}

func (c *Controller) dataMenuLocked() Node {
	children := []Node{
		c.actionItem("data.backup", "Back up state now", ActionBackup, Target{}),
		disabledItem("data.restore", "Restore backup… (CLI)"),
	}
	return submenu("data", "Data", children...)
}

func (c *Controller) settingsMenuLocked() Node {
	autostartLabel := "Start at login: unavailable"
	if c.snap.Autostart.Known && !c.snap.Autostart.Err {
		if c.snap.Autostart.Enabled {
			autostartLabel = "Start at login: On"
		} else {
			autostartLabel = "Start at login: Off"
		}
	}
	autostart := c.actionItem("settings.autostart", autostartLabel, ActionAutostartToggle, Target{})
	if c.snap.Autostart.Enabled {
		autostart.Checked = true
		autostart.Check = CheckCheckbox
	}
	children := []Node{autostart}
	if reason := c.autostartDisabledLocked(); reason != "" {
		children = append(children, disabledItem("settings.autostart.reason", "Start at login unavailable: "+reason))
	}

	configLabel := "Configuration unavailable"
	if c.snap.Config.Known {
		configLabel = fmt.Sprintf("HTTP %s · SOCKS %s · log %s", c.snap.Config.HTTPListen, c.snap.Config.SOCKSListen, c.snap.Config.LogLevel)
	}
	pathsLabel := "Data paths unavailable"
	if c.snap.Paths.State != "" || c.snap.Paths.Config != "" {
		pathsLabel = "State " + c.snap.Paths.State + " · config " + c.snap.Paths.Config
	}
	children = append(children,
		disabledItem("settings.config", configLabel),
		disabledItem("settings.paths", pathsLabel),
	)
	return submenu("settings", "Settings", children...)
}

func (c *Controller) advancedMenuLocked() Node {
	children := []Node{
		c.actionItem("advanced.restart", "Restart engine…", ActionRestartEngine, Target{}),
		c.actionItem("advanced.terminal", "Terminal menu…", ActionTerminalMenu, Target{}),
	}
	return submenu("advanced", "Advanced", children...)
}

func (c *Controller) quitMenuLocked() Node {
	children := make([]Node, 0, 4)
	appliedOrUnknown := c.desktopAppliedOrUnknownLocked()
	quit := c.actionItem("quit.now", "Quit RouteLatch", ActionQuit, Target{})
	if appliedOrUnknown {
		quit.Visible = false
	}
	children = append(children, quit)
	if appliedOrUnknown {
		children = append(children,
			c.actionItem("quit.restore", "Restore desktop proxy and quit…", ActionQuitRestoreDesktop, Target{}),
			c.actionItem("quit.keep", "Quit and keep desktop proxy… (internet may stay broken)", ActionQuitKeepDesktop, Target{}),
		)
		if c.snap.Desktop.Err || !c.snap.Desktop.Known {
			children = append(children, disabledItem("quit.warning", "Desktop proxy status is unknown; keeping it may leave the connection broken"))
		} else if c.snap.Desktop.Applied {
			children = append(children, disabledItem("quit.warning", "Desktop proxy stays applied if you keep it"))
		}
	}
	return submenu("quit", "Quit", children...)
}

func (c *Controller) pageNodes(prefix string, section Section, page, pages int) []Node {
	if pages <= 1 {
		return nil
	}
	status := disabledItem(prefix+".page", fmt.Sprintf("Page %d/%d", page, pages))
	prev := c.actionItem(prefix+".prev", "Previous page", ActionPagePrevious, Target{Section: section})
	prev.Enabled = prev.Enabled && page > 1
	next := c.actionItem(prefix+".next", "Next page", ActionPageNext, Target{Section: section})
	next.Enabled = next.Enabled && page < pages
	return []Node{status, prev, next}
}

func pageSlice[T any](rows []T, page int) []T {
	if len(rows) == 0 {
		return nil
	}
	start := (page - 1) * PageSize
	if start < 0 {
		start = 0
	}
	if start >= len(rows) {
		start = (len(rows) - 1) / PageSize * PageSize
	}
	end := start + PageSize
	if end > len(rows) {
		end = len(rows)
	}
	return rows[start:end]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
