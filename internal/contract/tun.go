package contract

import (
	"context"
	"time"
)

// TunState is the TUN layer's lifecycle state. It is independent of Mode:
// TUN is an overlay on targeted|all-proxy|all-direct, never a fifth mode
// (docs/contracts.md §10.1).
type TunState string

const (
	TunOff         TunState = "off"
	TunStarting    TunState = "starting"
	TunOn          TunState = "on"
	TunStopping    TunState = "stopping"
	TunStateError  TunState = "error"
	TunUnavailable TunState = "unavailable"
)

// TunHelper is what the engine can tell about the installed privileged helper.
type TunHelper string

const (
	TunHelperInstalled    TunHelper = "installed"
	TunHelperMissing      TunHelper = "missing"
	TunHelperIncompatible TunHelper = "incompatible"
)

// TunDNSMode is the first-phase DNS behavior. Plain DNS is sent directly, never
// through the pool (docs/contracts.md §10.5, D30).
type TunDNSMode string

const TunDNSDirect TunDNSMode = "direct"

// TunUDPMode is the first-phase non-DNS UDP behavior. It is blocked so no
// UDP/QUIC traffic leaks around the pool (docs/contracts.md §10.5, D30).
type TunUDPMode string

const TunUDPBlocked TunUDPMode = "blocked"

// TunReason is a stable, destination-free reason safe to show in status and
// error details. It is not an ErrorKind: TUN operations reuse the frozen
// ErrorKind set and map these reasons into its detail (docs/contracts.md §10.3).
type TunReason string

const (
	TunReasonHelperUnavailable   TunReason = "tun_helper_unavailable"
	TunReasonPlatformUnsupported TunReason = "tun_platform_unsupported"
	TunReasonConflict            TunReason = "tun_conflict"
	TunReasonServiceUnavailable  TunReason = "tun_service_unavailable"
	// TunReasonSidecarLoopRisk is a safe reason for excluding a hysteria2 exit
	// when its sidecar cannot bind to the physical interface under TUN, and for
	// refusing coverage when the configured sidecar runtime cannot express that
	// binding at all (docs/contracts.md §10.6). It never implies coverage on.
	TunReasonSidecarLoopRisk TunReason = "sidecar_loop_risk"
)

// TunReasons returns the stable reasons allowed in TunStatus.LastError and in
// error.detail.reason. Warning identifiers have their own TunWarning type; a
// new reason must be added here in the same change (docs/contracts.md §10.2–10.3).
func TunReasons() []TunReason {
	return []TunReason{
		TunReasonHelperUnavailable,
		TunReasonPlatformUnsupported,
		TunReasonConflict,
		TunReasonServiceUnavailable,
		TunReasonSidecarLoopRisk,
	}
}

// TunWarning values are the stable warning identifiers a TUN-aware status
// reports (docs/contracts.md §10.2). They carry no destination.
type TunWarning string

const (
	TunWarningDNSDirect           TunWarning = "tun_dns_direct"
	TunWarningUDPBlocked          TunWarning = "tun_udp_blocked"
	TunWarningHelperMissing       TunWarning = "tun_helper_missing"
	TunWarningServiceUnavailable  TunWarning = "tun_service_unavailable"
	TunWarningDomainUnavailable   TunWarning = "tun_domain_unavailable"
	TunWarningDesktopProxyOverlap TunWarning = "tun_desktop_proxy_overlap"
	TunWarningAllDirect           TunWarning = "tun_all_direct"
	TunWarningConflict            TunWarning = "tun_conflict"
)

// TunStatus is the additive `status.tun` object. Desired is the persisted user
// preference (Config.TunEnabled), not proof that TUN is active. The object
// contains no destination, domain, interface address or flow history (D21).
type TunStatus struct {
	Desired   bool       `json:"desired"`
	State     TunState   `json:"state"`
	Interface string     `json:"interface"`
	Since     time.Time  `json:"since,omitzero"`
	DNS       TunDNSMode `json:"dns"`
	UDP       TunUDPMode `json:"udp"`
	IPv6      bool       `json:"ipv6"`
	Helper    TunHelper  `json:"helper"`
	// LastError is one of the stable TunReason values. It is omitted when the
	// last operation left no error.
	LastError TunReason `json:"last_error,omitempty"`
	// Detail is a short, fixed explanation. It must never contain a
	// destination, domain, URL or credential.
	Detail string `json:"detail,omitempty"`
	// SocksAddress is the loopback SOCKS target the running service is
	// configured to hand captured TCP to. It is a loopback IP literal, never a
	// destination, and stays out of public status JSON (it travels only on the
	// private service socket) so D21's status surface is unchanged. The engine
	// uses it only to detect a stale target after the listener changes
	// (docs/contracts.md §10.6).
	SocksAddress string `json:"-"`
}

// TunError is a safe, typed failure returned by a TunRuntime. Kind stays inside
// the frozen ErrorKind set; Reason is one of the stable TunReason values.
// Message is ignored by the management layer, which uses fixed text per reason,
// so an implementation defect cannot leak a destination through an envelope.
type TunError struct {
	Kind    ErrorKind
	Reason  TunReason
	Message string
	Err     error
}

func (e *TunError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return string(e.Reason)
}

func (e *TunError) Unwrap() error { return e.Err }

// TunRuntime is the narrow seam between the management service and the TUN
// helper. R16-C supplies the service/client and R16-D wires it into the engine;
// the management service keeps this data model and its operations usable
// without one. Implementations must be idempotent: Enable while on and Disable
// while off succeed without changing policy. Status may leave Desired zero; the
// service fills it from config.
type TunRuntime interface {
	Status(context.Context) (TunStatus, error)
	Enable(context.Context) error
	Disable(context.Context) error
}
