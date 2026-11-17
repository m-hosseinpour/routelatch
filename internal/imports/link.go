package imports

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"routelatch/internal/contract"
)

// parseLink parses one share link into a node plus an optional rejection.
// Unsupported schemes are rejected by name, never fetched or executed. Query
// parameters outside the allow-list are dropped; nothing from the link is
// stored except projected connection settings and a sanitized label.
func parseLink(raw string, srcID contract.SourceID, order int, now time.Time, audit *Audit) contract.Record {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return buildRecord(node{}, &rejection{contract.RecordInvalid, ReasonInvalidJSON}, srcID, order, now, audit)
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" {
		rec := buildRecord(node{name: fragmentName(trimmed)}, &rejection{contract.RecordInvalid, ReasonInvalidJSON}, srcID, order, now, audit)
		rec.DedupeKey = dedupeKeyForRaw([]byte(trimmed))
		rec.ID = RecordIDForKey(rec.DedupeKey)
		return rec
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "vless":
		return parseVLESSStyle(u, contract.ProtocolVLESS, srcID, order, now, false, audit)
	case "trojan":
		return parseVLESSStyle(u, contract.ProtocolTrojan, srcID, order, now, true, audit)
	case "socks5", "socks":
		return parseSOCKS5(u, srcID, order, now, audit)
	case "http", "https":
		// An HTTP(S) URL is not a share link. Import must not fetch it.
		rec := buildRecord(node{protocol: contract.ProtocolHTTP, name: u.Fragment},
			&rejection{contract.RecordUnsupported, ReasonHTTPExitUnverified}, srcID, order, now, audit)
		return rawKeyed(rec, trimmed)
	default:
		rec := buildRecord(node{name: u.Fragment},
			&rejection{contract.RecordUnsupported, ReasonUnsupportedProtocol}, srcID, order, now, audit)
		rec.Protocol = contract.Protocol(scheme)
		return rawKeyed(rec, trimmed)
	}
}

func rawKeyed(rec contract.Record, raw string) contract.Record {
	rec.DedupeKey = dedupeKeyForRaw([]byte(raw))
	rec.ID = RecordIDForKey(rec.DedupeKey)
	return rec
}

func fragmentName(raw string) string {
	if i := strings.LastIndexByte(raw, '#'); i >= 0 {
		if name, err := url.QueryUnescape(raw[i+1:]); err == nil {
			return name
		}
		return raw[i+1:]
	}
	return ""
}

func parseVLESSStyle(u *url.URL, proto contract.Protocol, srcID contract.SourceID, order int, now time.Time, trojan bool, audit *Audit) contract.Record {
	q := u.Query()
	n := node{
		protocol: proto,
		name:     u.Fragment,
		server:   u.Hostname(),
		security: contract.SecurityNone,
	}
	if port, ok := parsePort(u.Port()); ok {
		n.port = port
	}
	if trojan {
		// The engine builds Trojan auth from Credentials.Password; the URI's
		// userinfo is the password (an optional user:password pair keeps the
		// password part).
		if u.User != nil {
			if pw, ok := u.User.Password(); ok {
				n.creds.Password = pw
			} else {
				n.creds.Password = u.User.Username()
			}
		}
	} else if u.User != nil {
		// VLESS authenticates with a UUID only; a stray password is not part of
		// the protocol and is deliberately not retained.
		n.creds.UserID = strings.ToLower(u.User.Username())
	}

	var rej *rejection
	if server, err := NormalizeServer(u.Hostname()); err != nil || u.Hostname() == "" {
		rej = &rejection{contract.RecordInvalid, ReasonInvalidServer}
	} else {
		n.server = server
	}
	if n.port == 0 {
		rej = &rejection{contract.RecordInvalid, ReasonInvalidPort}
	}

	transport, ok := transportFromString(first(q, "type", "network"))
	if !ok {
		if rej == nil {
			rej = &rejection{contract.RecordUnsupported, ReasonUnsupportedTransport}
		}
	}
	n.transport = transport

	sec, secOK := securityFromString(first(q, "security"))
	if !secOK && rej == nil {
		rej = &rejection{contract.RecordUnsupported, ReasonUnsupportedSecurity}
	}
	n.security = sec

	n.sni = first(q, "sni", "serverName")
	n.fp = first(q, "fp", "fingerprint")
	n.flow = first(q, "flow")
	n.path = first(q, "path")
	n.host = first(q, "host")
	n.grpc = first(q, "serviceName")
	if alpn, ok := sanitizeALPN(q["alpn"]); ok {
		n.alpn = alpn
	} else if rej == nil {
		rej = &rejection{contract.RecordInvalid, ReasonInvalidALPN}
	}

	if pinRaw := first(q, "pinSHA256", "pinnedPeerCertSha256"); pinRaw != "" {
		if pin, ok := validatePin(pinRaw); ok {
			n.pin = pin
		} else if rej == nil {
			rej = &rejection{contract.RecordInvalid, ReasonInvalidPin}
		}
	}
	if vn := first(q, "verifyPeerCertByName"); vn != "" {
		if name, ok := validateVerifyName(vn); ok {
			n.verify = name
		} else if rej == nil {
			rej = &rejection{contract.RecordInvalid, ReasonInvalidVerifyName}
		}
	}
	if truthy(first(q, "allowInsecure", "insecure", "skipCertVerify")) {
		n.insecure = true
	}

	if n.transport == contract.TransportXHTTP {
		extra := make(map[string]any)
		if rawExtra := first(q, "extra"); rawExtra != "" {
			parsed, reason := parseExtraJSON(rawExtra)
			if reason != "" && rej == nil {
				rej = &rejection{contract.RecordInvalid, reason}
			}
			for k, v := range parsed {
				extra[k] = v
			}
		}
		if v := first(q, "mode"); v != "" {
			if _, exists := extra["mode"]; !exists {
				extra["mode"] = v
			}
		}
		if v := first(q, "path"); v != "" {
			if _, exists := extra["path"]; !exists {
				extra["path"] = v
			}
		}
		if v := first(q, "host"); v != "" {
			if _, exists := extra["host"]; !exists {
				extra["host"] = v
			}
		}
		if len(extra) > 0 {
			if reason := validateRecordJSON(extra); reason != "" && rej == nil {
				rej = &rejection{contract.RecordInvalid, reason}
			}
			n.xhttp = extra
			// Keep the identity stable whether the path/host arrived as a
			// query parameter or inside the extra object.
			if n.path == "" {
				if v, ok := extra["path"].(string); ok {
					n.path = v
				}
			}
			if n.host == "" {
				if v, ok := extra["host"].(string); ok {
					n.host = v
				}
			}
		}
	}

	if n.security == contract.SecurityReality {
		// The fields are projected as data for reporting, but the record is
		// never dialable in v1 (R-13).
		n.fp = first(q, "fp", "fingerprint")
		n.publicKey = first(q, "pbk", "publicKey")
		n.shortID = first(q, "sid", "shortId")
		n.spiderX = first(q, "spx", "spiderX")
		if rej == nil {
			rej = &rejection{contract.RecordUnsupported, ReasonRealityUnverified}
		}
	}

	return buildRecord(n, rej, srcID, order, now, audit)
}

func parseSOCKS5(u *url.URL, srcID contract.SourceID, order int, now time.Time, audit *Audit) contract.Record {
	n := node{
		protocol:  contract.ProtocolSOCKS5,
		name:      u.Fragment,
		server:    u.Hostname(),
		transport: contract.TransportRaw,
		security:  contract.SecurityNone,
	}
	if port, ok := parsePort(u.Port()); ok {
		n.port = port
	}
	var rej *rejection
	if server, err := NormalizeServer(u.Hostname()); err != nil || u.Hostname() == "" {
		rej = &rejection{contract.RecordInvalid, ReasonInvalidServer}
	} else {
		n.server = server
	}
	if n.port == 0 {
		rej = &rejection{contract.RecordInvalid, ReasonInvalidPort}
	}
	if u.User != nil {
		n.creds.UserID = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			n.creds.Password = pw
		}
	}
	if !noControl(n.creds.UserID) || !noControl(n.creds.Password) {
		if rej == nil {
			rej = &rejection{contract.RecordInvalid, ReasonInvalidCredential}
		}
	}
	return buildRecord(n, rej, srcID, order, now, audit)
}

func first(q url.Values, names ...string) string {
	for _, name := range names {
		if v, ok := q[name]; ok && len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
	}
	return ""
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// decodeJSONExtra is used by the v2rayN reader where Extra is a JSON string.
func decodeJSONExtra(v any) (map[string]any, string) {
	if v == nil {
		return nil, ""
	}
	if s, ok := v.(string); ok {
		if strings.TrimSpace(s) == "" {
			return nil, ""
		}
		var parsed any
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		if err := dec.Decode(&parsed); err != nil {
			return nil, ReasonInvalidExtra
		}
		return sanitizeXHTTPExtra(parsed)
	}
	return sanitizeXHTTPExtra(v)
}
