package trayui

import (
	"routelatch/internal/contract"
	"routelatch/internal/control"
)

// Action is the closed menu action set. Labels never carry an action or id:
// the binder stores Node.Action and Node.Target and passes them back verbatim.
type Action int

const (
	ActionNone Action = iota
	ActionModeTargeted
	ActionModeAllProxy
	ActionModeAllDirect
	ActionStopListening
	ActionRestartEngine
	ActionProposalAccept
	ActionProposalReject
	ActionProposalRecheck
	ActionProposalsClear
	ActionRuleRemove
	ActionPoolSelectAuto
	ActionPoolSelect
	ActionPoolRemove
	ActionPoolRefresh
	ActionPoolRerank
	ActionPoolTest
	ActionPoolTestFirst20
	ActionDesktopRefresh
	ActionDesktopSet
	ActionDesktopRestore
	ActionBackup
	ActionDoctor
	ActionAutostartToggle
	ActionTerminalMenu
	ActionShowLogPath
	ActionQuit
	ActionQuitRestoreDesktop
	ActionQuitKeepDesktop
	ActionConfirm
	ActionCancel
	ActionPageNext
	ActionPagePrevious
)

// allActions is every public action, including ActionNone. The action mapping
// test walks this list so a newly added constant cannot silently lose its
// request, confirmation, or job classification.
var allActions = []Action{
	ActionNone,
	ActionModeTargeted,
	ActionModeAllProxy,
	ActionModeAllDirect,
	ActionStopListening,
	ActionRestartEngine,
	ActionProposalAccept,
	ActionProposalReject,
	ActionProposalRecheck,
	ActionProposalsClear,
	ActionRuleRemove,
	ActionPoolSelectAuto,
	ActionPoolSelect,
	ActionPoolRemove,
	ActionPoolRefresh,
	ActionPoolRerank,
	ActionPoolTest,
	ActionPoolTestFirst20,
	ActionDesktopRefresh,
	ActionDesktopSet,
	ActionDesktopRestore,
	ActionBackup,
	ActionDoctor,
	ActionAutostartToggle,
	ActionTerminalMenu,
	ActionShowLogPath,
	ActionQuit,
	ActionQuitRestoreDesktop,
	ActionQuitKeepDesktop,
	ActionConfirm,
	ActionCancel,
	ActionPageNext,
	ActionPagePrevious,
}

// String is the stable, log-safe action name. It is the only action detail a
// binder may log.
func (a Action) String() string {
	switch a {
	case ActionNone:
		return "none"
	case ActionModeTargeted:
		return "mode.targeted"
	case ActionModeAllProxy:
		return "mode.all-proxy"
	case ActionModeAllDirect:
		return "mode.all-direct"
	case ActionStopListening:
		return "mode.stop"
	case ActionRestartEngine:
		return "restart"
	case ActionProposalAccept:
		return "proposal.accept"
	case ActionProposalReject:
		return "proposal.reject"
	case ActionProposalRecheck:
		return "proposal.recheck"
	case ActionProposalsClear:
		return "proposals.clear"
	case ActionRuleRemove:
		return "rule.remove"
	case ActionPoolSelectAuto:
		return "pool.select-auto"
	case ActionPoolSelect:
		return "pool.select"
	case ActionPoolRemove:
		return "pool.remove"
	case ActionPoolRefresh:
		return "pool.refresh"
	case ActionPoolRerank:
		return "pool.rerank"
	case ActionPoolTest:
		return "pool.test"
	case ActionPoolTestFirst20:
		return "pool.test-first20"
	case ActionDesktopRefresh:
		return "desktop.refresh"
	case ActionDesktopSet:
		return "desktop.set"
	case ActionDesktopRestore:
		return "desktop.restore"
	case ActionBackup:
		return "backup"
	case ActionDoctor:
		return "doctor"
	case ActionAutostartToggle:
		return "autostart.toggle"
	case ActionTerminalMenu:
		return "terminal"
	case ActionShowLogPath:
		return "log-path"
	case ActionQuit:
		return "quit"
	case ActionQuitRestoreDesktop:
		return "quit.restore-desktop"
	case ActionQuitKeepDesktop:
		return "quit.keep-desktop"
	case ActionConfirm:
		return "confirm"
	case ActionCancel:
		return "cancel"
	case ActionPageNext:
		return "page.next"
	case ActionPagePrevious:
		return "page.previous"
	default:
		return "unknown"
	}
}

// Target is the data attached to a node. It is never parsed from a label.
type Target struct {
	Mode       contract.Mode
	ProposalID contract.ProposalID
	RuleID     contract.RuleID
	ExitID     contract.ExitID
	Section    Section
}

// Section identifies one independently paginated menu list.
type Section string

const (
	SectionRules            Section = "rules"
	SectionProposalsPending Section = "proposals.pending"
	SectionProposalsDecided Section = "proposals.decided"
	SectionPoolHealthy      Section = "pool.healthy"
	SectionPoolUntested     Section = "pool.untested"
	SectionPoolCooling      Section = "pool.cooling"
	SectionPoolUnusable     Section = "pool.unsupported"
)

type actionKind int

const (
	kindNone actionKind = iota
	kindRequest
	kindLocal
	kindTerminal
	kindLifecycle
	kindAutostart
	kindRestartJob
	kindQuitRestoreJob
)

func kindOf(a Action) actionKind {
	switch a {
	case ActionModeTargeted, ActionModeAllProxy, ActionModeAllDirect, ActionStopListening,
		ActionProposalAccept, ActionProposalReject, ActionProposalRecheck, ActionProposalsClear,
		ActionRuleRemove, ActionPoolSelectAuto, ActionPoolSelect, ActionPoolRemove,
		ActionPoolRefresh, ActionPoolRerank, ActionPoolTest, ActionPoolTestFirst20,
		ActionDesktopRefresh, ActionDesktopSet, ActionDesktopRestore, ActionBackup, ActionDoctor:
		return kindRequest
	case ActionRestartEngine:
		return kindRestartJob
	case ActionQuitRestoreDesktop:
		return kindQuitRestoreJob
	case ActionAutostartToggle:
		return kindAutostart
	case ActionTerminalMenu:
		return kindTerminal
	case ActionQuit, ActionQuitKeepDesktop:
		return kindLifecycle
	case ActionShowLogPath, ActionConfirm, ActionCancel, ActionPageNext, ActionPagePrevious:
		return kindLocal
	default:
		return kindNone
	}
}

// requestFor is the explicit action -> management request table. Only the
// operations in docs/contracts.md §7.3 appear here; no new op is introduced.
func requestFor(a Action, t Target) (control.Request, bool) {
	switch a {
	case ActionModeTargeted:
		return control.Request{Op: control.OpMode, Mode: string(contract.ModeTargeted)}, true
	case ActionModeAllProxy:
		return control.Request{Op: control.OpMode, Mode: string(contract.ModeProxyAll)}, true
	case ActionModeAllDirect:
		return control.Request{Op: control.OpMode, Mode: string(contract.ModeDirectAll)}, true
	case ActionStopListening:
		return control.Request{Op: control.OpMode, Mode: string(contract.ModeStopped)}, true
	case ActionProposalAccept:
		return control.Request{Op: control.OpProposalsAccept, ID: string(t.ProposalID)}, true
	case ActionProposalReject:
		return control.Request{Op: control.OpProposalsReject, ID: string(t.ProposalID)}, true
	case ActionProposalRecheck:
		return control.Request{Op: control.OpProposalsRecheck, ID: string(t.ProposalID)}, true
	case ActionProposalsClear:
		return control.Request{Op: control.OpProposalsClear}, true
	case ActionRuleRemove:
		return control.Request{Op: control.OpRulesRemove, ID: string(t.RuleID)}, true
	case ActionPoolSelectAuto:
		return control.Request{Op: control.OpPoolSelect, ID: "auto"}, true
	case ActionPoolSelect:
		return control.Request{Op: control.OpPoolSelect, ID: string(t.ExitID)}, true
	case ActionPoolRemove:
		return control.Request{Op: control.OpPoolRemove, ID: string(t.ExitID)}, true
	case ActionPoolRefresh:
		return control.Request{Op: control.OpPoolRefresh, All: true}, true
	case ActionPoolRerank:
		return control.Request{Op: control.OpPoolRerank}, true
	case ActionPoolTest:
		return control.Request{Op: control.OpPoolTest, ID: string(t.ExitID)}, true
	case ActionPoolTestFirst20:
		return control.Request{Op: control.OpPoolTest}, true
	case ActionDesktopRefresh:
		return control.Request{Op: control.OpDesktopStatus}, true
	case ActionDesktopSet:
		return control.Request{Op: control.OpDesktopSet}, true
	case ActionDesktopRestore:
		return control.Request{Op: control.OpDesktopRestore}, true
	case ActionBackup:
		return control.Request{Op: control.OpBackup}, true
	case ActionDoctor:
		return control.Request{Op: control.OpDoctor}, true
	default:
		return control.Request{}, false
	}
}

// jobName returns the contract's job label. The empty string means the action
// runs synchronously. Restart and quit-with-restore are jobs for different
// reasons (event wait, two operations) but use the same single-job slot.
func jobName(a Action) (string, bool) {
	switch a {
	case ActionRestartEngine:
		return "restart engine", true
	case ActionQuitRestoreDesktop:
		return "quit and restore desktop proxy", true
	case ActionPoolRefresh:
		return "pool refresh", true
	case ActionPoolTest, ActionPoolTestFirst20:
		return "pool test", true
	case ActionProposalRecheck:
		return "proposal recheck", true
	case ActionDesktopSet:
		return "desktop set", true
	case ActionDesktopRestore:
		return "desktop restore", true
	case ActionBackup:
		return "backup", true
	case ActionDoctor:
		return "doctor", true
	case ActionAutostartToggle:
		return "autostart", true
	default:
		return "", false
	}
}

// requiresConfirmationLocked is the two-step table. Proposal accept is
// state-dependent: accepting an unroute suggestion deletes a rule, so only the
// route kind is confirmed by the click itself.
func (c *Controller) requiresConfirmationLocked(a Action, t Target) bool {
	switch a {
	case ActionStopListening, ActionRestartEngine, ActionProposalsClear,
		ActionRuleRemove, ActionPoolRemove, ActionDesktopSet, ActionDesktopRestore,
		ActionQuitRestoreDesktop, ActionQuitKeepDesktop:
		return true
	case ActionProposalAccept:
		if p, ok := c.proposalByIDLocked(t.ProposalID); ok {
			return p.Kind == contract.ProposalUnroute
		}
		return true // conservative: an unknown proposal is never treated as safe
	case ActionQuit:
		return c.desktopAppliedOrUnknownLocked() || c.job != nil
	default:
		return false
	}
}

// actionLabel is the sanitized short label shown in "Confirm: <label>". It is
// derived from the action and target, never parsed back.
func (c *Controller) actionLabelLocked(a Action, t Target) string {
	var label string
	switch a {
	case ActionStopListening:
		label = "Stop listening" + c.desktopCutWarningLocked()
	case ActionRestartEngine:
		label = "Restart engine" + c.desktopCutWarningLocked()
	case ActionProposalsClear:
		label = "Clear decided/expired proposals"
	case ActionRuleRemove:
		label = "Remove rule " + shortID(string(t.RuleID))
	case ActionPoolRemove:
		label = "Remove exit " + shortID(string(t.ExitID))
	case ActionDesktopSet:
		label = "Set desktop proxy"
	case ActionDesktopRestore:
		label = "Restore previous desktop proxy"
	case ActionQuitRestoreDesktop:
		label = "Restore desktop proxy and quit"
	case ActionQuitKeepDesktop:
		label = "Quit and keep desktop proxy"
	case ActionQuit:
		label = "Quit RouteLatch"
	case ActionProposalAccept:
		if p, ok := c.proposalByIDLocked(t.ProposalID); ok && p.Kind == contract.ProposalUnroute {
			label = "Accept unroute (removes rule " + shortID(string(p.RuleID)) + ")"
			break
		}
		label = "Accept proposal " + shortID(string(t.ProposalID))
	default:
		label = a.String()
	}
	return SanitizeLabel(label)
}

func (c *Controller) proposalByIDLocked(id contract.ProposalID) (Proposal, bool) {
	for _, p := range c.snap.Proposals {
		if p.ID == id {
			return p, true
		}
	}
	return Proposal{}, false
}

func (c *Controller) desktopAppliedOrUnknownLocked() bool {
	d := c.snap.Desktop
	// Never-read is unknown, not "not applied": the initial menu must not offer
	// a plain Quit that could leave an applied desktop proxy behind.
	return d.Err || !d.Known || d.Applied
}

// desktopCutWarningLocked is appended to the confirmation label for actions
// that close the listeners. Unknown is warned about explicitly instead of
// being treated as "not applied".
func (c *Controller) desktopCutWarningLocked() string {
	d := c.snap.Desktop
	switch {
	case d.Err || !d.Known:
		return " (desktop proxy state unknown; may be cut)"
	case d.Applied:
		return " (desktop proxy applied; cut until listeners return)"
	default:
		return ""
	}
}

func (c *Controller) resumeBlockedLocked() bool {
	if c.snap.Engine != EngineStopped {
		return false
	}
	return c.snap.Status.ResumeRequiresRestart == nil || *c.snap.Status.ResumeRequiresRestart
}
