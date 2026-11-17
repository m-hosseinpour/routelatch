package engine

import (
	"encoding/json"
	"net/netip"
	"strings"

	"github.com/xtls/xray-core/common/geodata"
	"github.com/xtls/xray-core/common/uuid"

	"routelatch/internal/contract"
)

// The core configuration is generated from typed fields only. Nothing from an
// imported share link reaches the core as configuration: Record is already a
// sanitized, typed value (docs/contracts.md §5.4) and every field below is
// named here, so a new key in an imported payload cannot appear in the config.
// The generated document has exactly two root keys: "log" and "outbounds".
//
// There is no routing, no balancer, no DNS app, no API, no environment, no
// inbound, no policy, no stats and no direct/freedom outbound. The first
// outbound is a blackhole named rl-blocked: it is the dispatcher's default
// handler, so an accidental unforced dial fails closed instead of reaching the
// destination directly (spike/xray/FINDINGS.md §6). Pool dials always force
// their exit's tag, so the default handler is never used by design.

const (
	// BlockedTag is the tag of the fail-closed default outbound.
	BlockedTag = "rl-blocked"
	tagPrefix  = "rl-"

	maxXHTTPHeaders = 16
)

// coreConfig is the whole document we hand to the pinned core.
type coreConfig struct {
	Log       *coreLog       `json:"log,omitempty"`
	Outbounds []coreOutbound `json:"outbounds"`
}

type coreLog struct {
	LogLevel string `json:"loglevel"`
	Access   string `json:"access"`
}

type coreOutbound struct {
	Protocol string          `json:"protocol"`
	Tag      string          `json:"tag"`
	Settings any             `json:"settings,omitempty"`
	Stream   *streamSettings `json:"streamSettings,omitempty"`
}

type streamSettings struct {
	Network  string         `json:"network"`
	Security string         `json:"security,omitempty"`
	TLS      *tlsSettings   `json:"tlsSettings,omitempty"`
	WS       *wsSettings    `json:"wsSettings,omitempty"`
	GRPC     *grpcSettings  `json:"grpcSettings,omitempty"`
	XHTTP    map[string]any `json:"xhttpSettings,omitempty"`
}

type tlsSettings struct {
	ServerName           string   `json:"serverName,omitempty"`
	ALPN                 []string `json:"alpn,omitempty"`
	Fingerprint          string   `json:"fingerprint,omitempty"`
	PinnedPeerCertSHA256 string   `json:"pinnedPeerCertSha256,omitempty"`
	VerifyPeerCertByName string   `json:"verifyPeerCertByName,omitempty"`
}

type wsSettings struct {
	Host string `json:"host,omitempty"`
	Path string `json:"path,omitempty"`
}

type grpcSettings struct {
	ServiceName string `json:"serviceName,omitempty"`
}

type vlessSettings struct {
	Vnext []vlessTarget `json:"vnext"`
}

type vlessTarget struct {
	Address string      `json:"address"`
	Port    uint16      `json:"port"`
	Users   []vlessUser `json:"users"`
}

type vlessUser struct {
	ID         string `json:"id"`
	Encryption string `json:"encryption"`
	Flow       string `json:"flow,omitempty"`
}

type trojanSettings struct {
	Servers []trojanServer `json:"servers"`
}

type trojanServer struct {
	Address  string `json:"address"`
	Port     uint16 `json:"port"`
	Password string `json:"password"`
	Flow     string `json:"flow,omitempty"`
}

// xhttpExtraKeys is the allow-list from docs/contracts.md §5.4. Keys outside it
// are refused rather than dropped: the importer is expected to have rejected
// them, so seeing one here is a bug or an attempt to widen the config surface.
var xhttpExtraKeys = map[string]bool{
	"host": true, "path": true, "mode": true, "headers": true,
	"xPaddingBytes": true, "noGRPCHeader": true, "noSSEHeader": true,
	"scMaxEachPostBytes": true, "scMinPostsIntervalMs": true,
	"scMaxBufferedPosts": true, "scStreamUpServerSecs": true,
	"xmux": true,
}

var xmuxKeys = map[string]bool{
	"maxConcurrency": true, "maxConnections": true, "cMaxReuseTimes": true,
	"hMaxRequestTimes": true, "hMaxReusableSecs": true, "hKeepAlivePeriod": true,
}

// tagFor is the stable engine tag of one record (docs/contracts.md §2.7). It
// depends only on the record id, never on the policy generation.
func tagFor(id contract.RecordID) string { return tagPrefix + string(id) }

// validateRecord checks the record shape and returns a typed failure. It does
// not consult health or status: exit selection and health belong to the pool,
// and the engine only answers "can this record be dialed at all".
func validateRecord(rec contract.Record) error {
	if rec.ID == "" {
		return invalidRecord("record id is empty")
	}
	if rec.DedupeKey == "" {
		return invalidRecord("record has no dedupe key")
	}
	if !validID(string(rec.ID)) {
		return invalidRecord("record id contains unsupported characters")
	}
	if tagFor(rec.ID) == BlockedTag {
		return invalidRecord("record id collides with the engine's fail-closed tag")
	}
	if !validServer(rec.Server) {
		return invalidRecord("exit server is not a usable host or IP address")
	}
	if rec.Port == 0 {
		return invalidRecord("exit port is missing")
	}
	if rec.InsecureTLS && rec.PinnedPeerCertSHA256 == "" && rec.VerifyPeerCertByName == "" {
		return &contract.Failure{
			Kind:    contract.Unsupported,
			Path:    contract.PathLocal,
			Op:      "engine.record",
			Message: "skip-verify TLS is not dialable in v1; use a pinned certificate",
		}
	}
	// Invalid and Unsupported are trust states, not health: the record is
	// quarantined and must not gain a handler even if its fields look dialable.
	// Health states (unknown/healthy/cooling) are selection's business and are
	// deliberately ignored here.
	switch rec.Status {
	case contract.RecordInvalid:
		return invalidRecord("record is marked invalid")
	case contract.RecordUnsupported:
		return &contract.Failure{
			Kind:    contract.Unsupported,
			Path:    contract.PathLocal,
			Op:      "engine.record",
			Message: "record is quarantined",
		}
	}

	switch rec.Protocol {
	case contract.ProtocolVLESS:
		if rec.Credentials.UserID == "" {
			return invalidRecord("vless record has no user id")
		}
		if !validUserID(rec.Credentials.UserID) {
			return invalidRecord("vless user id is not accepted by the pinned core")
		}
	case contract.ProtocolTrojan:
		if rec.Credentials.Password == "" {
			return invalidRecord("trojan record has no password")
		}
	case contract.ProtocolSOCKS5:
		// Credentials are optional; the adapter offers both no-auth and
		// username/password when a user id is present. RFC 1929 caps each
		// field at 255 bytes.
		if len(rec.Credentials.UserID) > 255 || len(rec.Credentials.Password) > 255 {
			return invalidRecord("socks5 credentials are too long")
		}
		// A SOCKS5 exit is dialed by our own plaintext client. Accepting any
		// other transport/security value here would leave the user, status and
		// audit trail claiming TLS while auth and traffic went out in the
		// clear, and would drop a configured pin silently. Refuse the record
		// instead of downgrading it.
		if rec.Transport != contract.TransportRaw || rec.Security != contract.SecurityNone {
			return &contract.Failure{
				Kind:    contract.Unsupported,
				Path:    contract.PathLocal,
				Op:      "engine.record",
				Message: "SOCKS5 exits support only the raw transport without TLS in v1",
			}
		}
		if rec.SNI != "" || len(rec.ALPN) > 0 || rec.Fingerprint != "" || rec.Flow != "" ||
			rec.GRPCService != "" || rec.Path != "" || rec.Host != "" || len(rec.XHTTPExtra) > 0 ||
			rec.PinnedPeerCertSHA256 != "" || rec.VerifyPeerCertByName != "" {
			return &contract.Failure{
				Kind:    contract.Unsupported,
				Path:    contract.PathLocal,
				Op:      "engine.record",
				Message: "SOCKS5 record carries transport options that would be ignored",
			}
		}
	case contract.ProtocolHTTP:
		return &contract.Failure{
			Kind:    contract.Unsupported,
			Path:    contract.PathLocal,
			Op:      "engine.record",
			Message: "HTTP proxy exits are not supported in v1",
		}
	default:
		return &contract.Failure{
			Kind:    contract.Unsupported,
			Path:    contract.PathLocal,
			Op:      "engine.record",
			Message: "exit protocol is outside v1",
		}
	}

	switch rec.Transport {
	case contract.TransportRaw, contract.TransportTLS, contract.TransportWS,
		contract.TransportGRPC, contract.TransportXHTTP:
	default:
		return &contract.Failure{
			Kind:    contract.Unsupported,
			Path:    contract.PathLocal,
			Op:      "engine.record",
			Message: "exit transport is outside v1",
		}
	}

	if rec.Transport == contract.TransportTLS && rec.Security != contract.SecurityTLS {
		return &contract.Failure{
			Kind:    contract.Unsupported,
			Path:    contract.PathLocal,
			Op:      "engine.record",
			Message: "TLS transport without TLS security",
		}
	}

	// The pinned core refuses plaintext VLESS/Trojan to a public address
	// (infra/conf/xray.go requiresTransportSecurity). That is a policy limit of
	// the engine, not a malformed record, so it is Unsupported rather than a
	// record-validation error. Loopback, private and non-public domains remain
	// usable, which is what the loopback tests exercise.
	if rec.Protocol != contract.ProtocolSOCKS5 && rec.Security != contract.SecurityTLS && publicServer(rec.Server) {
		return &contract.Failure{
			Kind:    contract.Unsupported,
			Path:    contract.PathLocal,
			Op:      "engine.record",
			Message: "the pinned core prohibits VLESS/Trojan without TLS to a public exit",
		}
	}

	switch rec.Security {
	case contract.SecurityNone:
		if rec.PinnedPeerCertSHA256 != "" || rec.VerifyPeerCertByName != "" {
			return &contract.Failure{
				Kind:    contract.Unsupported,
				Path:    contract.PathLocal,
				Op:      "engine.record",
				Message: "certificate pin without TLS security",
			}
		}
	case contract.SecurityTLS:
	case contract.SecurityReality:
		return &contract.Failure{
			Kind:    contract.Unsupported,
			Path:    contract.PathLocal,
			Op:      "engine.record",
			Message: "REALITY is outside v1",
		}
	default:
		return &contract.Failure{
			Kind:    contract.Unsupported,
			Path:    contract.PathLocal,
			Op:      "engine.record",
			Message: "exit security is outside v1",
		}
	}

	if rec.Transport == contract.TransportGRPC && rec.GRPCService == "" {
		return invalidRecord("gRPC record has no service name")
	}
	switch rec.Protocol {
	case contract.ProtocolVLESS:
		// The pinned core removed flow support everywhere except VLESS
		// vision; a Trojan record with a flow is a removed feature, not a
		// software defect.
		if rec.Flow != "" && rec.Flow != "xtls-rprx-vision" {
			return invalidRecord("flow is not supported by the pinned core")
		}
	case contract.ProtocolTrojan:
		if rec.Flow != "" {
			return &contract.Failure{
				Kind:    contract.Unsupported,
				Path:    contract.PathLocal,
				Op:      "engine.record",
				Message: "the pinned core removed flow support for Trojan",
			}
		}
	}
	if rec.PinnedPeerCertSHA256 != "" && !validSHA256(rec.PinnedPeerCertSHA256) {
		return invalidRecord("pinned certificate hash is not 64 hex characters")
	}
	if err := validName("server name", rec.SNI); err != nil {
		return err
	}
	if err := validName("certificate name", rec.VerifyPeerCertByName); err != nil {
		return err
	}
	for _, alpn := range rec.ALPN {
		if alpn == "" || len(alpn) > 255 || strings.ContainsAny(alpn, " \t\r\n") {
			return invalidRecord("ALPN value is not usable")
		}
	}
	if strings.ContainsAny(rec.Path, "\r\n") || strings.ContainsAny(rec.Host, "\r\n") {
		return invalidRecord("record path or host contains a line break")
	}
	if _, err := xhttpBlock(rec); err != nil {
		return err
	}
	return nil
}

// buildOutbound returns the core outbound for a core-backed record, or nil for
// a SOCKS5 record, which is dialed by our own adapter (no core handler and no
// engine tag, docs/contracts.md §2.1).
func buildOutbound(rec contract.Record) (*coreOutbound, error) {
	if rec.Protocol == contract.ProtocolSOCKS5 {
		return nil, nil
	}

	ob := &coreOutbound{Tag: tagFor(rec.ID)}
	ss := &streamSettings{}
	switch rec.Transport {
	case contract.TransportRaw, contract.TransportTLS:
		ss.Network = "tcp"
	case contract.TransportWS:
		ss.Network = "ws"
		ss.WS = &wsSettings{Host: rec.Host, Path: rec.Path}
	case contract.TransportGRPC:
		ss.Network = "grpc"
		ss.GRPC = &grpcSettings{ServiceName: rec.GRPCService}
	case contract.TransportXHTTP:
		ss.Network = "xhttp"
		block, err := xhttpBlock(rec)
		if err != nil {
			return nil, err
		}
		ss.XHTTP = block
	}

	switch rec.Security {
	case contract.SecurityTLS:
		ss.Security = "tls"
		ss.TLS = &tlsSettings{
			ServerName:           rec.SNI,
			ALPN:                 rec.ALPN,
			Fingerprint:          rec.Fingerprint,
			PinnedPeerCertSHA256: rec.PinnedPeerCertSHA256,
			VerifyPeerCertByName: rec.VerifyPeerCertByName,
		}
	}
	ob.Stream = ss

	switch rec.Protocol {
	case contract.ProtocolVLESS:
		ob.Protocol = "vless"
		ob.Settings = vlessSettings{Vnext: []vlessTarget{{
			Address: rec.Server,
			Port:    rec.Port,
			Users: []vlessUser{{
				ID:         rec.Credentials.UserID,
				Encryption: "none",
				Flow:       rec.Flow,
			}},
		}}}
	case contract.ProtocolTrojan:
		ob.Protocol = "trojan"
		ob.Settings = trojanSettings{Servers: []trojanServer{{
			Address:  rec.Server,
			Port:     rec.Port,
			Password: rec.Credentials.Password,
			Flow:     rec.Flow,
		}}}
	default:
		return nil, &contract.Failure{
			Kind:    contract.Unsupported,
			Path:    contract.PathLocal,
			Op:      "engine.record",
			Message: "exit protocol is outside v1",
		}
	}
	return ob, nil
}

// xhttpBlock returns the allow-listed xhttpSettings block for a record. Both
// host and path come from typed fields; extra is copied key by key only after
// its keys are checked, and header names/values are validated so they cannot
// inject a line break into the HTTP request.
func xhttpBlock(rec contract.Record) (map[string]any, error) {
	if strings.ContainsAny(rec.Host, "\r\n") || strings.ContainsAny(rec.Path, "\r\n") {
		return nil, invalidRecord("record path or host contains a line break")
	}
	for key := range rec.XHTTPExtra {
		if !xhttpExtraKeys[key] {
			return nil, &contract.Failure{
				Kind:    contract.Unsupported,
				Path:    contract.PathLocal,
				Op:      "engine.record",
				Message: "xhttp extra field is not allowed",
			}
		}
	}
	block := make(map[string]any, len(rec.XHTTPExtra)+2)
	if rec.Host != "" {
		block["host"] = rec.Host
	}
	if rec.Path != "" {
		block["path"] = rec.Path
	}
	for key, value := range rec.XHTTPExtra {
		switch key {
		case "headers":
			headers, ok := value.(map[string]any)
			if !ok || len(headers) > maxXHTTPHeaders {
				return nil, invalidRecord("xhttp headers are not a usable map")
			}
			out := make(map[string]string, len(headers))
			for name, raw := range headers {
				if !validHeaderName(name) {
					return nil, invalidRecord("xhttp header name is invalid")
				}
				text, ok := raw.(string)
				if !ok || strings.ContainsAny(text, "\r\n") {
					return nil, invalidRecord("xhttp header value is invalid")
				}
				out[name] = text
			}
			block["headers"] = out
		case "xmux":
			xmux, ok := value.(map[string]any)
			if !ok {
				return nil, invalidRecord("xhttp xmux is not a map")
			}
			out := make(map[string]any, len(xmux))
			for name, raw := range xmux {
				if !xmuxKeys[name] {
					return nil, &contract.Failure{
						Kind:    contract.Unsupported,
						Path:    contract.PathLocal,
						Op:      "engine.record",
						Message: "xhttp xmux field is not allowed",
					}
				}
				out[name] = raw
			}
			block["xmux"] = out
		default:
			block[key] = value
		}
	}
	return block, nil
}

func generatedConfig(logLevel string, outbounds []coreOutbound) ([]byte, error) {
	doc := coreConfig{
		Log:       &coreLog{LogLevel: logLevel, Access: "none"},
		Outbounds: append([]coreOutbound{{Protocol: "blackhole", Tag: BlockedTag}}, outbounds...),
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, &contract.Failure{
			Kind:    contract.Internal,
			Path:    contract.PathLocal,
			Op:      "engine.config",
			Message: "generated configuration could not be encoded",
			Err:     err,
		}
	}
	return raw, nil
}

func invalidRecord(message string) error {
	return &contract.Failure{
		Kind:    contract.ImportInvalid,
		Path:    contract.PathLocal,
		Op:      "engine.record",
		Message: message,
	}
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// validUserID uses the pinned core's own parser, so accepted forms (dashed
// UUID, 32 hex characters, or a 1-30 byte legacy id) match what the core will
// dial instead of a stricter local guess that would block a whole subscription.
func validUserID(value string) bool {
	_, err := uuid.ParseString(value)
	return err == nil
}

// validServer accepts an IP literal (optionally bracketed IPv6) or a hostname.
// The record's Server is an address, never a URL or host:port pair; those shapes
// used to reach the core and come back as an "internal" error.
func validServer(value string) bool {
	if value == "" || len(value) > 253 || strings.ContainsAny(value, " \t\r\n/@?#") {
		return false
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		_, err := netip.ParseAddr(value[1 : len(value)-1])
		return err == nil
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return true
	}
	if strings.HasSuffix(value, ".") {
		value = strings.TrimSuffix(value, ".")
	}
	if value == "" || len(value) > 253 || strings.Contains(value, "..") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			default:
				return false
			}
		}
	}
	return true
}

// validName rejects an empty-or-unusable TLS name. It is a shape check; the
// certificate itself is still verified against it by the core.
func validName(what, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 253 || strings.ContainsAny(value, " \t\r\n/") {
		return invalidRecord(what + " is not usable")
	}
	return nil
}

// publicServer reports whether the pinned core would demand transport security
// for this exit address. It mirrors requiresTransportSecurity in the pinned
// config layer, including its private-domain list.
func publicServer(server string) bool {
	host := server
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		return !(addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast())
	}
	domain := strings.TrimSuffix(strings.ToLower(host), ".")
	return !geodata.GetPrivateDomainMatcher().MatchAny(domain)
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

// logLevelOrDefault maps an empty or unknown level to the engine default. The
// warning level keeps startup problems visible without logging per-request
// traffic (access is disabled in the generated config).
func logLevelOrDefault(level string) string {
	switch level {
	case "debug", "info", "warning", "error", "none":
		return level
	default:
		return "warning"
	}
}

// describeConfigError keeps core build errors (which may carry record fields)
// out of the user-facing message. It exists so callers cannot accidentally
// format a config error that mentions a credential.
func describeConfigError(err error) error {
	if err == nil {
		return nil
	}
	return &contract.Failure{
		Kind:    contract.Internal,
		Path:    contract.PathLocal,
		Op:      "engine.config",
		Message: "the pinned core rejected the engine configuration",
		Err:     err,
	}
}

// describeRecordError is the same for a document built from one record: the
// pinned core rejecting it means the record shape got past our validation, so
// the honest classification is a rejected record, not a software defect.
func describeRecordError(err error) error {
	if err == nil {
		return nil
	}
	return &contract.Failure{
		Kind:    contract.ImportInvalid,
		Path:    contract.PathLocal,
		Op:      "engine.record",
		Message: "the pinned core rejected the record configuration",
		Err:     err,
	}
}
