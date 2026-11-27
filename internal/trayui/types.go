package trayui

import (
	"encoding/json"
	"net"
	"strconv"
	"time"

	"routelatch/internal/autostart"
	"routelatch/internal/contract"
	"routelatch/internal/control"
)

// EngineState is the single state the menu shows for the engine. Ready and
// Stopped are valid service states; Starting is a transition; Unavailable
// means the management socket did not answer; Restarting is the supervisor
// event overlay and is never inferred from a stale status (docs/contracts.md
// §7.3, §7.5).
type EngineState int

const (
	EngineLoading EngineState = iota
	EngineReady
	EngineStarting
	EngineStopped
	EngineUnavailable
	EngineRestarting
)

func (s EngineState) String() string {
	switch s {
	case EngineLoading:
		return "loading"
	case EngineReady:
		return "ready"
	case EngineStarting:
		return "starting"
	case EngineStopped:
		return "stopped"
	case EngineUnavailable:
		return "unavailable"
	case EngineRestarting:
		return "restarting"
	default:
		return "unknown"
	}
}

// Snapshot is the typed, display-ready copy of everything the menu renders.
// Dynamic text is already sanitized; no credentials or share links are kept.
// A Snapshot returned by Controller.Snapshot is a deep copy.
type Snapshot struct {
	Engine       EngineState
	EngineDetail string // a safe kind such as "timeout" or "unavailable"
	Status       Status
	Rules        []Rule
	Pool         PoolView
	Proposals    []Proposal
	Desktop      DesktopView
	Autostart    AutostartView
	Env          EnvView
	Config       ConfigView
	Paths        Paths
	Doctor       []Check
	// ListsKnown is false after a status/list failure or before the first list
	// read. The menu then says the lists are refreshing instead of presenting
	// an empty list as current (§7.3).
	ListsKnown bool

	// LoadedAt is diagnostic only; it is excluded from fingerprints so a poll
	// that returned identical data never rebuilds the menu.
	LoadedAt time.Time
}

// Status is the menu subset of `status`.
type Status struct {
	Ready         bool
	ListenersOpen bool
	Mode          contract.Mode
	PolicyVersion contract.PolicyVersion
	SelectedExit  contract.ExitID
	// ResumeRequiresRestart is nil for an older payload without the field. Nil
	// means unknown and is treated conservatively by the stopped menu.
	ResumeRequiresRestart *bool
	Pool                  PoolCounts
	ConnectionsOpen       int
	DiagnosticsEnabled    bool
	// Tun is the additive status.tun object. Known=false means the engine did
	// not send it (an older payload) and must be treated as unknown, not off
	// (docs/contracts.md §10.2).
	Tun      TunView
	Warnings []string
}

// TunView is the display-ready copy of status.tun. It never carries a
// destination, domain, URL or credential (D21); Interface and Detail are
// sanitized because status fields are still untrusted input at this boundary.
type TunView struct {
	Known     bool
	Desired   bool
	State     contract.TunState
	Interface string
	Since     time.Time
	DNS       contract.TunDNSMode
	UDP       contract.TunUDPMode
	IPv6      bool
	Helper    contract.TunHelper
	LastError contract.TunReason
	Detail    string
	// Warnings are the known TUN warning identifiers emitted by status. The
	// raw status warnings stay in Status.Warnings for Diagnostics; this copy is
	// the coverage-only view and drops unknown identifiers.
	Warnings []contract.TunWarning
	// WarningsKnown is true only when Warnings came from a status response. A
	// TUN action response carries no warning set, so the menu must say
	// "refreshing" until the next status read instead of claiming zero.
	WarningsKnown bool
}

// PoolCounts mirrors the documented status/list counters.
type PoolCounts struct {
	Total       int
	Usable      int
	Healthy     int
	Cooling     int
	Unknown     int
	Unsupported int
	Invalid     int
}

// Rule is one approved rule rendered by the menu. Operand is host, addr or
// prefix; Details keeps the added time and a sanitized note.
type Rule struct {
	ID      contract.RuleID
	Kind    contract.RuleKind
	Operand string
	Note    string
	Origin  contract.RuleOrigin
	AddedAt time.Time
}

// PoolView is the complete `pool list` view (all pages, because the tray pages
// locally) plus the service counters.
type PoolView struct {
	Rows      []PoolRow
	Counts    PoolCounts
	Truncated bool
}

// PoolRow is one exit. Server, credentials and share links are deliberately
// not stored: the menu only needs the sanitized display name and status.
type PoolRow struct {
	ID          contract.ExitID
	Status      contract.RecordStatus
	Reason      string
	DisplayName string
	Protocol    contract.Protocol
	InsecureTLS bool
}

// Proposal is one pending or decided diagnostic proposal. Destination is a
// live memory view only and is never written anywhere (D21).
type Proposal struct {
	ID          contract.ProposalID
	Kind        contract.ProposalKind
	RuleID      contract.RuleID
	Destination string
	Status      contract.ProposalStatus
	ExpiresAt   time.Time
	Note        string
}

// DesktopView is the desktop-proxy state. Known=false with Err=true means the
// last read failed; Quit then uses the explicit restore/keep choices instead
// of claiming the setting is absent.
type DesktopView struct {
	Known        bool
	Err          bool
	Supported    bool
	Applied      bool
	PreviousSet  bool
	Environment  string
	Conflict     []string
	Notice       string
	Restored     []string
	UserModified []string
	Failed       []string
}

// AutostartView is the local XDG autostart state. Conflict carries the
// service-vs-autostart conflict reason from autostart.Manager.
type AutostartView struct {
	Known          bool
	Err            bool
	Enabled        bool
	Managed        bool
	TrayAvailable  bool
	PIDFDAvailable bool
	Conflict       string
	Path           string
}

// EnvView is the launcher environment. Available=false is normal in stopped
// mode, where the env operation has no listener to describe.
type EnvView struct {
	Available  bool
	Reason     string
	HTTPProxy  string
	HTTPSProxy string
	ALLProxy   string
	NoProxy    string
}

// ConfigView is the three-field configuration summary the contract allows.
type ConfigView struct {
	Known       bool
	HTTPListen  string
	SOCKSListen string
	LogLevel    string
}

// Paths are the config/data locations shown in Settings and used for the log
// path notification. All values are sanitized before display.
type Paths struct {
	Config string
	State  string
	Socket string
	Log    string
}

// Check is one sanitized `doctor` result.
type Check struct {
	Name   string
	Status string
	Detail string
	Fix    string
}

// decodeData round-trips one success envelope payload into a typed value. A
// control.Client returns map[string]any while an in-process server may return
// a typed value; both decode identically through JSON.
func decodeData(env contract.Envelope, out any) error {
	if env.Data == nil {
		return &EnvelopeError{Kind: contract.Internal, Message: "management response has no data"}
	}
	raw, err := json.Marshal(env.Data)
	if err != nil {
		return &EnvelopeError{Kind: contract.Internal, Message: "management response cannot be decoded"}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &EnvelopeError{Kind: contract.Internal, Message: "management response does not match the documented payload"}
	}
	return nil
}

func statusFromData(d control.StatusData) Status {
	warnings := make([]string, 0, len(d.Warnings))
	for _, w := range d.Warnings {
		if clean := SanitizeHeader(w); clean != "" {
			warnings = append(warnings, clean)
		}
	}
	tun := TunView{}
	if d.Tun != nil {
		tun = tunFromStatus(*d.Tun)
		tun.Warnings = tunWarningsFromStatus(d.Warnings)
		tun.WarningsKnown = true
	}
	return Status{
		Ready:                 d.Ready,
		ListenersOpen:         d.ListenersOpen,
		Mode:                  d.Mode,
		PolicyVersion:         d.PolicyVersion,
		SelectedExit:          d.SelectedExit,
		ResumeRequiresRestart: d.ResumeRequiresRestart,
		Pool: PoolCounts{
			Total: d.Pool.Total, Usable: d.Pool.Usable, Healthy: d.Pool.Healthy, Cooling: d.Pool.Cooling,
		},
		ConnectionsOpen:    d.Connections.Open,
		DiagnosticsEnabled: d.Diagnostics.Enabled,
		Tun:                tun,
		Warnings:           warnings,
	}
}

// tunFromStatus copies the wire object into the menu view. The runtime enums
// are kept verbatim: the controller does not invent a second TUN state model,
// and an unknown value still renders as an explicit unknown state.
func tunFromStatus(st contract.TunStatus) TunView {
	return TunView{
		Known:     true,
		Desired:   st.Desired,
		State:     st.State,
		Interface: tunInterfaceName(st.Interface),
		Since:     st.Since,
		DNS:       st.DNS,
		UDP:       st.UDP,
		IPv6:      st.IPv6,
		Helper:    st.Helper,
		LastError: st.LastError,
		Detail:    SanitizeLabel(st.Detail),
	}
}

// tunInterfaceName keeps only a Linux interface-name-shaped value. The field is
// a local interface, never a destination, and this also stops a malformed
// runtime from smuggling arbitrary text (including a URL) into a label.
func tunInterfaceName(s string) string {
	if s == "" || len(s) > 15 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			continue
		}
		return ""
	}
	return s
}

// tunWarningsFromStatus keeps only the documented warning identifiers. A
// status string that is not one of them belongs to the general warnings list,
// not the coverage view; dropping unknown text also keeps arbitrary server
// text out of the coverage labels (D21).
func tunWarningsFromStatus(warnings []string) []contract.TunWarning {
	var out []contract.TunWarning
	for _, raw := range warnings {
		switch w := contract.TunWarning(raw); w {
		case contract.TunWarningDNSDirect, contract.TunWarningUDPBlocked,
			contract.TunWarningHelperMissing, contract.TunWarningServiceUnavailable,
			contract.TunWarningDomainUnavailable, contract.TunWarningDesktopProxyOverlap,
			contract.TunWarningAllDirect, contract.TunWarningConflict:
			out = append(out, w)
		}
	}
	return out
}

func rulesFromData(d control.RulesListData) []Rule {
	out := make([]Rule, 0, len(d.Rules))
	for _, r := range d.Rules {
		out = append(out, Rule{
			ID:      r.ID,
			Kind:    r.Kind,
			Operand: SanitizeHeader(ruleOperand(r)),
			Note:    SanitizeHeader(r.Note),
			Origin:  r.Origin,
			AddedAt: r.AddedAt,
		})
	}
	return out
}

func ruleOperand(r control.RuleView) string {
	switch {
	case r.Host != "":
		return r.Host
	case r.Addr != "":
		return r.Addr
	case r.Prefix != "":
		return r.Prefix
	case r.ID != "":
		return string(r.ID)
	default:
		return "rule"
	}
}

func poolFromData(d control.PoolListData) PoolView {
	rows := make([]PoolRow, 0, len(d.Records))
	for _, r := range d.Records {
		rows = append(rows, PoolRow{
			ID:          contract.ExitID(r.ID),
			Status:      r.Status,
			Reason:      SanitizeLabel(r.Reason),
			DisplayName: SanitizeLabel(r.DisplayName),
			Protocol:    r.Protocol,
			InsecureTLS: r.InsecureTLS,
		})
	}
	return PoolView{
		Rows: rows,
		Counts: PoolCounts{
			Total: d.Counts.Total, Usable: d.Counts.Usable, Healthy: d.Counts.Healthy,
			Cooling: d.Counts.Cooling, Unknown: d.Counts.Unknown,
			Unsupported: d.Counts.Unsupported, Invalid: d.Counts.Invalid,
		},
		Truncated: d.Truncated,
	}
}

func proposalsFromData(d control.ProposalsData) []Proposal {
	out := make([]Proposal, 0, len(d.Proposals))
	for _, p := range d.Proposals {
		dest := destinationText(p.Destination)
		out = append(out, Proposal{
			ID:          p.ID,
			Kind:        p.Kind,
			RuleID:      p.RuleID,
			Destination: SanitizeHeader(dest),
			Status:      p.Status,
			ExpiresAt:   p.ExpiresAt,
			Note:        SanitizeLabel(p.Note),
		})
	}
	return out
}

func destinationText(d contract.Destination) string {
	var host string
	switch d.Kind {
	case contract.DestDomain:
		host = d.Host
	case contract.DestIP:
		if d.Addr.IsValid() {
			host = d.Addr.String()
		}
	case contract.DestCIDR:
		if d.Prefix.IsValid() {
			host = d.Prefix.String()
		}
	}
	if host == "" {
		return ""
	}
	if d.Port != 0 {
		return net.JoinHostPort(host, strconv.Itoa(int(d.Port)))
	}
	return host
}

func desktopFromData(d control.DesktopData) DesktopView {
	return DesktopView{
		Known:        true,
		Supported:    d.Supported,
		Applied:      d.Applied,
		PreviousSet:  d.PreviousSet,
		Environment:  SanitizeLabel(d.Environment),
		Conflict:     sanitizeList(d.Conflict),
		Notice:       SanitizeHeader(d.Notice),
		Restored:     sanitizeList(d.Restored),
		UserModified: sanitizeList(d.UserModified),
		Failed:       sanitizeList(d.Failed),
	}
}

func autostartFromData(s autostart.Status) AutostartView {
	return AutostartView{
		Known:          true,
		Enabled:        s.Enabled,
		Managed:        s.Managed,
		TrayAvailable:  s.TrayAvailable,
		PIDFDAvailable: s.PIDFDAvailable,
		Conflict:       SanitizeLabel(s.Conflict),
		Path:           SanitizeHeader(s.Path),
	}
}

func envFromData(d control.EnvData) EnvView {
	return EnvView{
		Available:  true,
		HTTPProxy:  SanitizeHeader(d.HTTPProxy),
		HTTPSProxy: SanitizeHeader(d.HTTPSProxy),
		ALLProxy:   SanitizeHeader(d.ALLProxy),
		NoProxy:    SanitizeHeader(d.NoProxy),
	}
}

func configFromData(d control.ConfigData) ConfigView {
	return ConfigView{
		Known:       true,
		HTTPListen:  SanitizeLabel(d.Values.HTTPListen),
		SOCKSListen: SanitizeLabel(d.Values.SOCKSListen),
		LogLevel:    SanitizeLabel(d.Values.LogLevel),
	}
}

func pathsFromData(d control.ConfigPathData, logPath string) Paths {
	return Paths{
		Config: SanitizeHeader(d.Config),
		State:  SanitizeHeader(d.State),
		Socket: SanitizeHeader(d.Socket),
		Log:    SanitizeHeader(logPath),
	}
}

func checksFromData(d control.DoctorData) []Check {
	out := make([]Check, 0, len(d.Checks))
	for _, c := range d.Checks {
		out = append(out, Check{
			Name: SanitizeLabel(c.Name), Status: SanitizeLabel(c.Status),
			Detail: SanitizeLabel(c.Detail), Fix: SanitizeLabel(c.Fix),
		})
	}
	return out
}

func sanitizeList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		out = append(out, SanitizeLabel(s))
	}
	return out
}
