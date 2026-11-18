package contract

import (
	"context"
	"time"
)

// SourceID identifies the origin of imported records. It must be
// "s-" + the first 12 hex characters of sha256(kind + "\x00" + canonical
// origin): never the raw URL, path or label, because IDs appear in status,
// warnings and logs (docs/contracts.md §5.1).
type SourceID string

// SourceKind is how records entered the product (D08).
type SourceKind string

const (
	SourceManual       SourceKind = "manual"       // one link typed or pasted
	SourceFile         SourceKind = "file"         // a user-selected local file
	SourceSubscription SourceKind = "subscription" // a user-supplied URL
	SourceV2rayN       SourceKind = "v2rayn"       // a user-exported v2rayN file
)

// Source is one origin. Its fields are input, not identity: refreshing a
// subscription replaces its record set but keeps the Source.
type Source struct {
	ID          SourceID   `json:"id"`
	Kind        SourceKind `json:"kind"`
	Label       string     `json:"label"`            // user label, sanitized before display
	Origin      string     `json:"origin,omitempty"` // secret-bearing; never printed
	Order       int        `json:"order"`
	AddedAt     time.Time  `json:"added_at"`
	RefreshedAt time.Time  `json:"refreshed_at,omitzero"`
	RecordCount int        `json:"record_count"`
	LastError   string     `json:"last_error,omitempty"` // redacted
	LastErrorAt time.Time  `json:"last_error_at,omitzero"`
	AllowEmpty  bool       `json:"allow_empty"` // explicit user decision to accept an empty source
}

// ReasonInsecureTLSRemoved is the stable StatusReason for a record whose
// configuration disables TLS verification. It stays visible in pool list,
// status and diagnostics, with the remediation hint: replace the link with a
// valid certificate or a pinnedPeerCertSha256 link.
const ReasonInsecureTLSRemoved = "insecure_tls_removed"

// ReasonProtocolUnsupported and ReasonHTTPExitNotVerified are the stable
// reasons for a link whose scheme v1 can never dial. They are defined here, once,
// because both the importer (which stores them) and the pool view (which derives
// them for any stored status except invalid) must report the same literal
// (docs/contracts.md §5.3–5.4).
const (
	// ReasonProtocolUnsupported: the scheme is outside the dialable set
	// {vless, trojan, socks5, hysteria2}.
	ReasonProtocolUnsupported = "protocol_unsupported"
	// ReasonHTTPExitNotVerified: an HTTP(S) exit cannot be verified in v1 (R-2).
	ReasonHTTPExitNotVerified = "http_exit_not_verified"
)

// RecordID is stable while the record's connection settings are unchanged.
type RecordID string

// RecordStatus is the record's import/trust state combined with its cached
// health bucket. Only the pool package writes it, from import validity plus
// ExitHealth (docs/contracts.md §5.3).
type RecordStatus string

const (
	RecordInvalid     RecordStatus = "invalid"     // cannot be used; kept for reporting
	RecordUnsupported RecordStatus = "unsupported" // protocol/transport/security outside v1
	RecordUnknown     RecordStatus = "unknown"     // valid, never measured or no egress evidence yet
	RecordHealthy     RecordStatus = "healthy"     // positive egress evidence: bytes came back
	RecordCooling     RecordStatus = "cooling"     // transport failure; cooldown_until in the future
)

// Transport is the pinned core's stream transport. The v1 set is fixed; a
// value outside it makes the record unsupported, not fatal.
type Transport string

const (
	TransportRaw   Transport = "raw"
	TransportTLS   Transport = "tls"
	TransportWS    Transport = "ws"
	TransportGRPC  Transport = "grpc"
	TransportXHTTP Transport = "xhttp"
)

// Security is the transport security of a record. REALITY is representable
// but gated: the pinned core supports it, T1 did not exercise it, so imports
// stay Unsupported until the T3/T4 gate (R-13).
type Security string

const (
	SecurityNone    Security = "none"
	SecurityTLS     Security = "tls"
	SecurityReality Security = "reality"
)

// Credentials are protocol secrets. They are stored in state.json (0600) and
// must never appear in logs, CLI output, JSON errors, proposals or diagnostics.
type Credentials struct {
	UserID   string `json:"user_id,omitempty"` // VLESS UUID, Trojan/SOCKS/HTTP password
	Password string `json:"password,omitempty"`
}

// Record is one canonical imported exit. DisplayName, SourceIDs and Order are
// metadata; DedupeKey and the connection fields define identity.
type Record struct {
	ID          RecordID       `json:"id"`
	DedupeKey   string         `json:"dedupe_key"` // "sha256:<hex>" of the canonical settings (§5.1)
	SourceIDs   []SourceID     `json:"source_ids"`
	DisplayName string         `json:"display_name"` // untrusted text; sanitize at render time
	Protocol    Protocol       `json:"protocol"`
	Server      string         `json:"server"` // hostname or IP as imported; not a Destination
	Port        uint16         `json:"port"`
	Credentials Credentials    `json:"credentials"`
	Transport   Transport      `json:"transport"`
	Security    Security       `json:"security"`
	SNI         string         `json:"sni,omitempty"`
	ALPN        []string       `json:"alpn,omitempty"`
	Fingerprint string         `json:"fingerprint,omitempty"`
	Flow        string         `json:"flow,omitempty"`
	Path        string         `json:"path,omitempty"` // ws/xhttp path
	Host        string         `json:"host,omitempty"` // ws/xhttp Host header
	GRPCService string         `json:"grpc_service,omitempty"`
	PublicKey   string         `json:"public_key,omitempty"`  // REALITY
	ShortID     string         `json:"short_id,omitempty"`    // REALITY
	SpiderX     string         `json:"spider_x,omitempty"`    // REALITY
	XHTTPExtra  map[string]any `json:"xhttp_extra,omitempty"` // allow-listed extra only (§5.4)
	// PinnedPeerCertSHA256 and VerifyPeerCertByName keep certificate
	// verification on. They are a pin/name constraint, not an insecure
	// exception, and are accepted in v1.
	PinnedPeerCertSHA256 string `json:"pinned_peer_cert_sha256,omitempty"`
	VerifyPeerCertByName string `json:"verify_peer_cert_by_name,omitempty"`
	// InsecureTLS marks skip-verify input. Such a record is never dialable in
	// v1 (the pinned core removed skip-verify); it is reported as Unsupported
	// with ReasonInsecureTLSRemoved (D18, R-12).
	InsecureTLS bool `json:"insecure_tls,omitempty"`
	// Hysteria2* carry the sidecar-only settings of a hysteria2 exit. The auth
	// password lives in Credentials.Password. Bandwidth is normally left zero
	// so RouteLatch applies its own default; a trusted caller may set it.
	Hysteria2Obfs         string       `json:"hysteria2_obfs,omitempty"`          // "salamander" | "gecko" | ""
	Hysteria2ObfsPassword string       `json:"hysteria2_obfs_password,omitempty"` // salamander obfs password (secret)
	Hysteria2UpMbps       uint32       `json:"hysteria2_up_mbps,omitempty"`
	Hysteria2DownMbps     uint32       `json:"hysteria2_down_mbps,omitempty"`
	Status                RecordStatus `json:"status"`
	StatusReason          string       `json:"status_reason,omitempty"`
	Order                 int          `json:"order"` // global display rank; user order survives refresh
	AddedAt               time.Time    `json:"added_at"`
	ValidatedAt           time.Time    `json:"validated_at,omitzero"`
	LastError             string       `json:"last_error,omitempty"` // redacted
}

// Fetcher retrieves one source's raw payload under fixed limits. It never
// uses a proxy from the environment, never follows a redirect to a different
// scheme, and never executes the payload.
//
// Invariants:
//   - At most MaxSourceBytes are read and the request is bounded by
//     SourceConnectTimeout and SourceTotalTimeout (docs/contracts.md §5.6).
//   - At most SourceMaxRedirects redirects are followed; credentials are
//     stripped when the host changes.
//   - A non-2xx response, an oversized body, a timeout or a parse failure is
//     a *Failure with Kind SourceFetch; the caller keeps the previous records.
type Fetcher interface {
	FetchSource(ctx context.Context, src Source) ([]byte, error)
}

// Importer turns a payload into canonical records. It is pure with respect to
// the engine: it validates and sanitizes, but never builds or starts a core.
//
// Invariants:
//   - Every returned record has a valid DedupeKey and a non-zero Status.
//   - Invalid individual records are returned with Status Invalid and a
//     redacted reason; one bad record never discards the batch.
//   - Nothing outside the allow-list in §5.4 is retained, and no value from
//     the payload is executed, fetched, or used as a file path.
type Importer interface {
	Import(ctx context.Context, src Source, payload []byte) ([]Record, error)
}
