package imports

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"routelatch/internal/contract"
)

// Stable, redacted per-record reasons. They contain no payload values and are
// safe for status, JSON errors and logs.
const (
	ReasonInvalidJSON          = "invalid_json"
	ReasonInvalidServer        = "invalid_server"
	ReasonInvalidPort          = "invalid_port"
	ReasonInvalidCredential    = "missing_or_invalid_credential"
	ReasonInvalidALPN          = "invalid_alpn"
	ReasonInvalidFingerprint   = "invalid_fingerprint"
	ReasonInvalidFlow          = "invalid_flow"
	ReasonInvalidPath          = "invalid_path"
	ReasonInvalidHostHeader    = "invalid_host_header"
	ReasonInvalidPin           = "invalid_pinned_peer_cert_sha256"
	ReasonInvalidVerifyName    = "invalid_verify_peer_cert_by_name"
	ReasonUnsupportedProtocol  = contract.ReasonProtocolUnsupported
	ReasonUnsupportedTransport = "transport_unsupported"
	ReasonUnsupportedSecurity  = "security_unsupported"
	ReasonRealityUnverified    = "reality_not_verified"
	ReasonHTTPExitUnverified   = contract.ReasonHTTPExitNotVerified
	ReasonUnknownExtraKey      = "xhttp_extra_key_rejected"
	ReasonInvalidExtra         = "xhttp_extra_invalid"
	ReasonRootScopeKey         = "root_scope_key_rejected"
	ReasonScriptField          = "script_field_rejected"
	ReasonLocalFilePath        = "local_file_path_rejected"
	ReasonOutboundDependency   = "outbound_dependency_rejected"
	ReasonRecordOversized      = "record_oversized"
	ReasonRecordTooDeep        = "record_too_deep"
	ReasonKeyTooLong           = "key_too_long"
	ReasonValueTooLong         = "value_too_long"
	ReasonDuplicateHeader      = "duplicate_or_invalid_header"
	ReasonNoCredential         = "missing_credential"
	ReasonInvalidObfs          = "invalid_obfs"
	ReasonMissingObfsPassword  = "missing_obfs_password"
	ReasonECHUnsupported       = "ech_unsupported"
	ReasonSingBoxField         = "singbox_field_rejected"
)

const (
	maxRecordBytes      = 256 << 10
	maxJSONDepth        = 8
	maxJSONKeyBytes     = 128
	maxJSONStringBytes  = 8 << 10
	maxXHTTPHeaders     = 16
	maxALPNEntries      = 8
	maxFieldStringBytes = 8 << 10
	maxHyMbps           = 100000
	maxHyObfsPassword   = 256
)

// validHyObfs is the allow-list of sing-box v1.14.4 hysteria2 obfuscators.
func validHyObfs(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "salamander", "gecko":
		return true
	}
	return false
}

// rootScopeKeys are whole-config sections that must never be accepted from a
// node import. They would change routing, DNS, the API, logging or inbound
// state outside the pool (docs/contracts.md §5.4, AGENTS.md invariants).
var rootScopeKeys = map[string]bool{
	"routing": true, "dns": true, "api": true, "env": true, "inbounds": true,
	"observatory": true, "policy": true, "stats": true, "reverse": true,
	"fakedns": true, "log": true,
	// sing-box application-config sections. A sing-box outbound object is
	// accepted, but a whole app config (route/DNS/inbounds/service) is not.
	"route": true, "ntp": true, "certificate": true, "experimental": true,
	"endpoints": true, "services": true,
}

// singboxHy2Keys is the whole top-level surface of a sing-box hysteria2
// outbound that RouteLatch will own. Any other key (realm, server_ports,
// hop_interval, transport/network, TLS files, dial fields, a hex pin) changes
// trust, routing or the endpoint and is refused, never carried over silently.
var singboxHy2Keys = map[string]bool{
	"type": true, "tag": true, "server": true, "server_port": true,
	"password": true, "up_mbps": true, "down_mbps": true,
	"obfs": true, "tls": true,
	// Metadata aliases used by v2rayN exports; not connection settings.
	"remarks": true, "name": true, "address": true, "port": true,
}

var singboxHy2TLSKeys = map[string]bool{
	"enabled": true, "server_name": true, "insecure": true,
}

var singboxHy2ObfsKeys = map[string]bool{
	"type": true, "password": true,
}

// dependencyKeys name another outbound or a dialer proxy. Accepting them would
// make the record depend on a configuration we do not own.
var dependencyKeys = map[string]bool{
	"proxysettings": true, "dialerproxy": true, "balancertag": true, "detour": true,
}

// xhttpExtraKeys is the only accepted XHTTP extra surface (contract §5.4).
var xhttpExtraKeys = map[string]bool{
	"host": true, "path": true, "mode": true, "headers": true,
	"xPaddingBytes": true, "noGRPCHeader": true, "noSSEHeader": true,
	"scMaxEachPostBytes": true, "scMinPostsIntervalMs": true,
	"scMaxBufferedPosts": true, "scStreamUpServerSecs": true, "xmux": true,
}

var xmuxKeys = map[string]bool{
	"maxConcurrency": true, "maxConnections": true, "cMaxReuseTimes": true,
	"hMaxRequestTimes": true, "hMaxReusableSecs": true, "hKeepAlivePeriod": true,
}

// validateRecordJSON walks one untrusted record object and reports the first
// structural violation. It never evaluates a value. Nested whole-config keys
// are rejected wherever they appear because a node has no legitimate use for
// them, and a successful core config build is not a sandbox proof.
func validateRecordJSON(v any) string {
	if reason := walkJSON(v, 1, ""); reason != "" {
		return reason
	}
	return ""
}

func walkJSON(v any, depth int, key string) string {
	if depth > maxJSONDepth {
		return ReasonRecordTooDeep
	}
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if len(k) > maxJSONKeyBytes {
				return ReasonKeyTooLong
			}
			lower := strings.ToLower(k)
			if rootScopeKeys[lower] {
				return ReasonRootScopeKey
			}
			if dependencyKeys[lower] && lower != "detour" {
				return ReasonOutboundDependency
			}
			if strings.HasSuffix(lower, "script") || strings.Contains(lower, "_script") {
				return ReasonScriptField
			}
			if strings.HasSuffix(lower, "file") {
				if s, ok := child.(string); ok && strings.TrimSpace(s) != "" {
					return ReasonLocalFilePath
				}
			}
			if lower == "detour" {
				if s, ok := child.(string); ok && strings.TrimSpace(s) != "" {
					return ReasonOutboundDependency
				}
			}
			if lower == "mux" {
				// v1 has no mux. The builder projects settings itself, so this
				// is dropped rather than treated as an outbound dependency.
				continue
			}
			if reason := walkJSON(child, depth+1, k); reason != "" {
				return reason
			}
		}
	case []any:
		if len(t) > 256 {
			return ReasonRecordOversized
		}
		for _, child := range t {
			if reason := walkJSON(child, depth+1, key); reason != "" {
				return reason
			}
		}
	case string:
		if len(t) > maxJSONStringBytes {
			return ReasonValueTooLong
		}
	case json.Number:
		if len(t) > 64 {
			return ReasonValueTooLong
		}
	case float64, bool, nil:
	default:
		return ReasonInvalidJSON
	}
	return ""
}

// sanitizeXHTTPExtra projects only the documented keys. Any unknown key,
// downloadSettings or h2Params rejects the record: they can contain a complete
// StreamConfig with its own security and sockopt.
func sanitizeXHTTPExtra(v any) (map[string]any, string) {
	if v == nil {
		return nil, ""
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, ReasonInvalidExtra
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		if !xhttpExtraKeys[k] {
			return nil, ReasonUnknownExtraKey
		}
		switch k {
		case "headers":
			headers, ok := val.(map[string]any)
			if !ok {
				return nil, ReasonInvalidExtra
			}
			out[k] = headers
		case "xmux":
			xmux, ok := val.(map[string]any)
			if !ok {
				return nil, ReasonInvalidExtra
			}
			clean := make(map[string]any, len(xmux))
			for xk, xv := range xmux {
				if !xmuxKeys[xk] {
					return nil, ReasonUnknownExtraKey
				}
				clean[xk] = xv
			}
			out[k] = clean
		default:
			out[k] = val
		}
	}
	return out, ""
}

// parseExtraJSON accepts a JSON object (already URL-decoded by the query
// parser). Anything else rejects the record.
func parseExtraJSON(raw string) (map[string]any, string) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, ReasonInvalidExtra
	}
	if reason := validateRecordJSON(v); reason != "" {
		return nil, reason
	}
	return sanitizeXHTTPExtra(v)
}

func validHeaderName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

// validateHeaders enforces the XHTTP header bound: up to 16 token pairs, no
// CR/LF, no oversized values. Duplicate names in a JSON object cannot occur
// after decoding, so a duplicate is only visible as a repeated query value and
// is handled by the caller.
func validateHeaders(m map[string]any) string {
	if len(m) > maxXHTTPHeaders {
		return ReasonDuplicateHeader
	}
	seen := make(map[string]bool, len(m))
	for k, v := range m {
		if !validHeaderName(k) {
			return ReasonDuplicateHeader
		}
		lower := strings.ToLower(k)
		if seen[lower] {
			return ReasonDuplicateHeader
		}
		seen[lower] = true
		s, ok := v.(string)
		if !ok || len(s) > maxJSONStringBytes || strings.ContainsAny(s, "\r\n\x00") {
			return ReasonDuplicateHeader
		}
	}
	return ""
}

// parsePort accepts the decimal form only. Zero and values above 65535 are
// invalid; a defaulted port is never invented.
func parsePort(raw string) (uint16, bool) {
	if raw == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || n == 0 {
		return 0, false
	}
	return uint16(n), true
}

// sanitizeALPN sorts, de-duplicates and validates the ALPN list.
func sanitizeALPN(raw []string) ([]string, bool) {
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, item := range raw {
		for _, piece := range strings.Split(item, ",") {
			piece = strings.TrimSpace(piece)
			if piece == "" {
				continue
			}
			if len(piece) > 64 || strings.ContainsAny(piece, " \t\r\n\x00") {
				return nil, false
			}
			if !seen[piece] {
				seen[piece] = true
				out = append(out, piece)
			}
		}
	}
	if len(out) > maxALPNEntries {
		return nil, false
	}
	sort.Strings(out)
	return out, true
}

// validatePin accepts a comma-separated list of SHA-256 leaf hashes in hex.
// It canonicalizes to lowercase and never treats the pin as an insecure
// exception: certificate verification stays on.
func validatePin(raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", true
	}
	if len(raw) > maxFieldStringBytes {
		return "", false
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		p = strings.TrimPrefix(p, "sha256:")
		p = strings.ReplaceAll(p, ":", "")
		if len(p) != 64 {
			return "", false
		}
		for _, r := range p {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				return "", false
			}
		}
		out = append(out, p)
	}
	return strings.Join(out, ","), true
}

// validateVerifyName accepts one or more DNS names for an additional name
// constraint. It is part of the dedupe key and only meaningful with TLS.
func validateVerifyName(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	if len(raw) > maxFieldStringBytes {
		return "", false
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		name, err := NormalizeServer(strings.TrimSpace(p))
		if err != nil {
			return "", false
		}
		out = append(out, name)
	}
	return strings.Join(out, ","), true
}

// safePathField rejects control characters in a transport path while keeping
// the path a value, never a filesystem target. A leading slash is not required
// by every transport parser, so it is not imposed here.
func safePathField(raw string) bool {
	return len(raw) <= maxFieldStringBytes && !strings.ContainsAny(raw, "\r\n\x00")
}

func safeHostField(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > 255 || strings.ContainsAny(raw, "\r\n\x00 \t") {
		return false
	}
	if strings.HasPrefix(raw, "/") {
		return false
	}
	if _, err := url.Parse("http://" + raw); err != nil {
		return false
	}
	return true
}

func noControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// recordTooLarge bounds one serialized input record before any projection.
func recordTooLarge(raw []byte) bool { return len(raw) > maxRecordBytes }

func reasonText(reason string) string {
	// Reasons are codes; this switch exists only to keep user-facing text
	// stable and free of payload values.
	switch reason {
	case contract.ReasonInsecureTLSRemoved:
		return "TLS verification is disabled; replace the link with a valid certificate or a pinnedPeerCertSha256 link"
	case ReasonRealityUnverified:
		return "REALITY is not verified in this build; the record is kept for reporting only"
	case ReasonHTTPExitUnverified:
		return "HTTP CONNECT exits are not verified in this build; use VLESS, Trojan or SOCKS5"
	case ReasonECHUnsupported:
		return "ECH is not verified in this build; remove the ech parameter"
	case ReasonInvalidObfs:
		return "hysteria2 obfs must be salamander or gecko"
	case ReasonMissingObfsPassword:
		return "hysteria2 gecko obfs requires a non-empty obfs password"
	case ReasonSingBoxField:
		return "the sing-box outbound carries a field RouteLatch does not own"
	default:
		return fmt.Sprintf("record rejected: %s", reason)
	}
}
