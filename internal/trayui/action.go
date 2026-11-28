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
	ActionTunEnable
	ActionTunDisable
	ActionProposalAccept
	ActionProposalAcceptSuffix
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
	ActionPoolScan
	ActionDesktopRefresh
	ActionDesktopSet
	ActionDesktopRestore
	ActionDesktopClear
	ActionBackup
	ActionDoctor
	ActionAutostartToggle
	ActionTerminalMenu
	ActionShowLogPath
	ActionQuit
	ActionQuitRestoreDesktop
	ActionQuitKeepDesktop
)

// allActions is every public action, including ActionNone. The action mapping
// test walks this list so a newly added constant cannot silently lose its
// request or job classification.
var allActions = []Action{
	ActionNone,
	ActionModeTargeted,
	ActionModeAllProxy,
	ActionModeAllDirect,
	ActionStopListening,
	ActionRestartEngine,
	ActionTunEnable,
	ActionTunDisable,
	ActionProposalAccept,
	ActionProposalAcceptSuffix,
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
	ActionPoolScan,
	ActionDesktopRefresh,
	ActionDesktopSet,
	ActionDesktopRestore,
	ActionDesktopClear,
	ActionBackup,
	ActionDoctor,
	ActionAutostartToggle,
	ActionTerminalMenu,
	ActionShowLogPath,
	ActionQuit,
	ActionQuitRestoreDesktop,
	ActionQuitKeepDesktop,
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
	case ActionTunEnable:
		return "tun.enable"
	case ActionTunDisable:
		return "tun.disable"
	case ActionProposalAccept:
		return "proposal.accept"
	case ActionProposalAcceptSuffix:
		return "proposal.accept-suffix"
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
	case ActionPoolScan:
		return "pool.scan"
	case ActionDesktopRefresh:
		return "desktop.refresh"
	case ActionDesktopSet:
		return "desktop.set"
	case ActionDesktopRestore:
		return "desktop.restore"
	case ActionDesktopClear:
		return "desktop.clear"
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
}

// Section identifies one pool group in the menu. It is not pagination state:
// every page is rendered as a nested submenu inside the same list.
type Section string

const (
	SectionPoolHealthy   Section = "pool.healthy"
	SectionPoolReachable Section = "pool.reachable"
	SectionPoolUntested  Section = "pool.untested"
	SectionPoolCooling   Section = "pool.cooling"
	SectionPoolUnusable  Section = "pool.unsupported"
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
		ActionProposalAccept, ActionProposalAcceptSuffix, ActionProposalReject, ActionProposalRecheck, ActionProposalsClear,
		ActionRuleRemove, ActionPoolSelectAuto, ActionPoolSelect, ActionPoolRemove,
		ActionPoolRefresh, ActionPoolRerank, ActionPoolTest, ActionPoolTestFirst20, ActionPoolScan,
		ActionDesktopRefresh, ActionDesktopSet, ActionDesktopRestore, ActionDesktopClear, ActionBackup, ActionDoctor,
		ActionTunEnable, ActionTunDisable:
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
	case ActionShowLogPath:
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
	case ActionTunEnable:
		return control.Request{Op: control.OpTunEnable}, true
	case ActionTunDisable:
		return control.Request{Op: control.OpTunDisable}, true
	case ActionProposalAccept:
		return control.Request{Op: control.OpProposalsAccept, ID: string(t.ProposalID)}, true
	case ActionProposalAcceptSuffix:
		return control.Request{Op: control.OpProposalsAccept, ID: string(t.ProposalID), RuleKind: string(contract.RuleDomainSuffix)}, true
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
	case ActionPoolScan:
		return control.Request{Op: control.OpPoolScan, Limit: contract.PoolScanBatch}, true
	case ActionDesktopRefresh:
		return control.Request{Op: control.OpDesktopStatus}, true
	case ActionDesktopSet:
		return control.Request{Op: control.OpDesktopSet}, true
	case ActionDesktopRestore:
		return control.Request{Op: control.OpDesktopRestore}, true
	case ActionDesktopClear:
		return control.Request{Op: control.OpDesktopClear}, true
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
	case ActionTunEnable:
		return "tun enable", true
	case ActionTunDisable:
		return "tun disable", true
	case ActionQuitRestoreDesktop:
		return "quit and restore desktop proxy", true
	case ActionPoolRefresh:
		return "pool refresh", true
	case ActionPoolTest, ActionPoolTestFirst20:
		return "pool test", true
	case ActionPoolScan:
		return "pool check", true
	case ActionProposalRecheck:
		return "proposal recheck", true
	case ActionDesktopSet:
		return "desktop set", true
	case ActionDesktopRestore:
		return "desktop restore", true
	case ActionDesktopClear:
		return "desktop clear", true
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

// proposalByIDLocked finds the live view of one proposal.
func (c *Controller) proposalByIDLocked(id contract.ProposalID) (Proposal, bool) {
	for _, p := range c.snap.Proposals {
		if p.ID == id {
			return p, true
		}
	}
	return Proposal{}, false
}

// desktopAppliedOrUnknownLocked reports whether a plain Quit could leave an
// applied desktop proxy behind.
func (c *Controller) desktopAppliedOrUnknownLocked() bool {
	d := c.snap.Desktop
	// Never-read is unknown, not "not applied": the initial menu must not offer
	// a plain Quit that could leave an applied desktop proxy behind. A cleared
	// proxy is disabled, but only while its recorded value still holds: a
	// conflict means the user changed the setting, so it can be live again and
	// is treated like an applied proxy.
	return d.Err || !d.Known || (d.Applied && (!d.Cleared || len(d.Conflict) > 0))
}

// desktopCutWarningLocked is the visible warning under Mode when stopping or
// restarting would cut an applied desktop proxy. It replaces the warning that
// the old confirmation label carried; unknown is warned about explicitly.
func (c *Controller) desktopCutWarningLocked() string {
	d := c.snap.Desktop
	switch {
	case d.Err || !d.Known:
		return "Desktop proxy state unknown; stopping or restarting may cut it"
	case d.Applied && (!d.Cleared || len(d.Conflict) > 0):
		return "Desktop proxy applied; stopping or restarting cuts it until listening resumes"
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
