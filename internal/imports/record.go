package imports

import (
	"strings"
	"time"

	"routelatch/internal/contract"
)

// noticeInsecureIgnored is the visible import notice when a link asked to
// skip verification but a pin/name kept verification on (D18 as amended).
const noticeInsecureIgnored = "allow_insecure_ignored: verification stays on; the certificate pin or name constraint is applied"

// noticeInsecureIgnoredHy2Pin is the honest notice for a hysteria2 link whose
// skip-verify was ignored only because of a hex pin. The pin is retained (never
// downgraded to insecure), but the pinned sing-box sidecar cannot enforce a hex
// leaf pin, so the engine rejects the record as engine_record_rejected. It would
// be misleading to claim the pin "is applied" when the exit is not dialable.
const noticeInsecureIgnoredHy2Pin = "allow_insecure_ignored: the skip-verify downgrade was ignored and the pin retained, but hysteria2 hex certificate pins cannot be enforced by the sidecar; the record is not dialable"

// node is the untrusted, already-projected intermediate between a share link
// or v2rayN profile and a canonical record. Fields not listed here never leave
// the parser.
type node struct {
	protocol  contract.Protocol
	server    string
	port      uint16
	creds     contract.Credentials
	transport contract.Transport
	security  contract.Security
	sni       string
	alpn      []string
	fp        string
	flow      string
	path      string
	host      string
	grpc      string
	xhttp     map[string]any
	pin       string
	verify    string
	insecure  bool
	publicKey string
	shortID   string
	spiderX   string
	name      string
	// hysteria2 (sidecar-dispatched) settings. The auth password is creds.Password.
	hyObfs         string
	hyObfsPassword string
	hyUpMbps       uint32
	hyDownMbps     uint32
}

// rejection carries a status and a stable reason for a record that must be
// reported rather than used.
type rejection struct {
	status contract.RecordStatus
	reason string
}

// insecureIgnored reports whether the link asked to skip verification but a
// pin or name constraint keeps verification on. D18 as amended: the pin wins
// and the downgrade is ignored, never persisted as an insecure record.
func insecureIgnored(n node) bool {
	return n.insecure && (n.pin != "" || n.verify != "")
}

// buildRecord validates a node and produces the contract record. A node may
// already carry a rejection from its parser (unsupported scheme, insecure TLS)
// while still keeping the settings needed for its dedupe key. audit may be nil;
// when present it receives the visible "allowInsecure ignored" notice.
func buildRecord(n node, rej *rejection, srcID contract.SourceID, order int, now time.Time, audit *Audit) contract.Record {
	ignored := insecureIgnored(n)
	rec := contract.Record{
		SourceIDs:             []contract.SourceID{srcID},
		DisplayName:           SanitizeDisplay(n.name),
		Protocol:              n.protocol,
		Server:                n.server,
		Port:                  n.port,
		Credentials:           n.creds,
		Transport:             n.transport,
		Security:              n.security,
		SNI:                   n.sni,
		ALPN:                  n.alpn,
		Fingerprint:           n.fp,
		Flow:                  n.flow,
		Path:                  n.path,
		Host:                  n.host,
		GRPCService:           n.grpc,
		XHTTPExtra:            n.xhttp,
		PublicKey:             n.publicKey,
		ShortID:               n.shortID,
		SpiderX:               n.spiderX,
		PinnedPeerCertSHA256:  n.pin,
		VerifyPeerCertByName:  n.verify,
		InsecureTLS:           n.insecure && !ignored,
		Hysteria2Obfs:         n.hyObfs,
		Hysteria2ObfsPassword: n.hyObfsPassword,
		Hysteria2UpMbps:       n.hyUpMbps,
		Hysteria2DownMbps:     n.hyDownMbps,
		Order:                 order,
		AddedAt:               now.UTC(),
		ValidatedAt:           now.UTC(),
	}

	status, reason := validateNode(n, rej)
	rec.Status = status
	rec.StatusReason = reason
	if ignored && audit != nil && status != contract.RecordInvalid && status != contract.RecordUnsupported {
		notice := noticeInsecureIgnored
		if n.protocol == contract.ProtocolHysteria2 && n.pin != "" {
			// The engine refuses every hysteria2 hex pin; say so instead of
			// claiming the pin is applied to a record that will be rejected.
			notice = noticeInsecureIgnoredHy2Pin
		}
		audit.Notices = append(audit.Notices, notice)
	}
	if reason != "" && status == contract.RecordInvalid {
		rec.LastError = reasonText(reason)
	}
	rec.DedupeKey = DedupeKey(rec)
	rec.ID = RecordIDForKey(rec.DedupeKey)
	return rec
}

func validateNode(n node, rej *rejection) (contract.RecordStatus, string) {
	if rej != nil {
		return rej.status, rej.reason
	}
	if n.server == "" {
		return contract.RecordInvalid, ReasonInvalidServer
	}
	if _, err := NormalizeServer(n.server); err != nil {
		return contract.RecordInvalid, ReasonInvalidServer
	}
	if n.port == 0 {
		return contract.RecordInvalid, ReasonInvalidPort
	}
	switch n.protocol {
	case contract.ProtocolVLESS:
		if !validUUID(n.creds.UserID) {
			return contract.RecordInvalid, ReasonInvalidCredential
		}
	case contract.ProtocolTrojan:
		if strings.TrimSpace(n.creds.Password) == "" || !noControl(n.creds.Password) {
			return contract.RecordInvalid, ReasonNoCredential
		}
	case contract.ProtocolSOCKS5:
		if !noControl(n.creds.UserID) || !noControl(n.creds.Password) {
			return contract.RecordInvalid, ReasonInvalidCredential
		}
	case contract.ProtocolHysteria2:
		if strings.TrimSpace(n.creds.Password) == "" || !noControl(n.creds.Password) {
			return contract.RecordInvalid, ReasonNoCredential
		}
		if !validHyObfs(n.hyObfs) || !noControl(n.hyObfsPassword) ||
			len(n.hyObfsPassword) > maxHyObfsPassword ||
			(n.hyObfs == "salamander" && n.hyObfsPassword == "") {
			return contract.RecordInvalid, ReasonInvalidObfs
		}
		if n.hyUpMbps > maxHyMbps || n.hyDownMbps > maxHyMbps {
			return contract.RecordInvalid, ReasonValueTooLong
		}
		if n.transport != contract.TransportRaw || n.security != contract.SecurityTLS {
			return contract.RecordUnsupported, ReasonUnsupportedTransport
		}
	default:
		return contract.RecordUnsupported, ReasonUnsupportedProtocol
	}
	switch n.transport {
	case contract.TransportRaw, contract.TransportTLS, contract.TransportWS,
		contract.TransportGRPC, contract.TransportXHTTP:
	default:
		return contract.RecordUnsupported, ReasonUnsupportedTransport
	}
	switch n.security {
	case contract.SecurityNone:
	case contract.SecurityTLS:
	case contract.SecurityReality:
		return contract.RecordUnsupported, ReasonRealityUnverified
	default:
		return contract.RecordUnsupported, ReasonUnsupportedSecurity
	}
	if n.security != contract.SecurityTLS && n.pin != "" {
		return contract.RecordInvalid, ReasonInvalidPin
	}
	if n.security != contract.SecurityTLS && n.verify != "" {
		return contract.RecordInvalid, ReasonInvalidVerifyName
	}
	if n.pin != "" {
		if _, ok := validatePin(n.pin); !ok {
			return contract.RecordInvalid, ReasonInvalidPin
		}
	}
	if n.verify != "" {
		if _, ok := validateVerifyName(n.verify); !ok {
			return contract.RecordInvalid, ReasonInvalidVerifyName
		}
	}
	if n.insecure {
		// D18 as amended: a pin or name constraint keeps verification on and
		// authenticates the peer, so the record is not insecure. Without one it
		// is quarantined and never dialed.
		if n.pin == "" && n.verify == "" {
			return contract.RecordUnsupported, contract.ReasonInsecureTLSRemoved
		}
	}
	if len(n.alpn) > 0 {
		if _, ok := sanitizeALPN(n.alpn); !ok {
			return contract.RecordInvalid, ReasonInvalidALPN
		}
	}
	if n.flow != "" && n.protocol == contract.ProtocolVLESS && n.flow != "xtls-rprx-vision" {
		return contract.RecordInvalid, ReasonInvalidFlow
	}
	if n.sni != "" && (len(n.sni) > 255 || !safeHostField(n.sni)) {
		return contract.RecordInvalid, ReasonInvalidHostHeader
	}
	if n.fp != "" && (len(n.fp) > 64 || !fingerprintToken(n.fp)) {
		return contract.RecordInvalid, ReasonInvalidFingerprint
	}
	if len(n.publicKey) > 256 || len(n.shortID) > 64 {
		return contract.RecordInvalid, ReasonValueTooLong
	}
	if n.spiderX != "" && !safePathField(n.spiderX) {
		return contract.RecordInvalid, ReasonInvalidPath
	}
	if n.path != "" && !safePathField(n.path) {
		return contract.RecordInvalid, ReasonInvalidPath
	}
	if n.host != "" && !safeHostField(n.host) {
		return contract.RecordInvalid, ReasonInvalidHostHeader
	}
	if n.grpc != "" && !safePathField(n.grpc) {
		return contract.RecordInvalid, ReasonInvalidPath
	}
	if n.xhttp != nil {
		if _, reason := sanitizeXHTTPExtra(map[string]any(n.xhttp)); reason != "" {
			return contract.RecordInvalid, reason
		}
		if headers, ok := n.xhttp["headers"].(map[string]any); ok {
			if reason := validateHeaders(headers); reason != "" {
				return contract.RecordInvalid, reason
			}
		}
	}
	return contract.RecordUnknown, ""
}

func fingerprintToken(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
				return false
			}
		}
	}
	return true
}

func transportFromString(raw string) (contract.Transport, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "tcp", "raw":
		return contract.TransportRaw, true
	case "tls":
		return contract.TransportTLS, true
	case "ws", "websocket":
		return contract.TransportWS, true
	case "grpc", "gun":
		return contract.TransportGRPC, true
	case "xhttp", "splithttp":
		return contract.TransportXHTTP, true
	default:
		return contract.Transport(strings.ToLower(raw)), false
	}
}

func securityFromString(raw string) (contract.Security, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "none":
		return contract.SecurityNone, true
	case "tls":
		return contract.SecurityTLS, true
	case "reality":
		return contract.SecurityReality, true
	default:
		return contract.Security(strings.ToLower(raw)), false
	}
}
