package imports

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"routelatch/internal/contract"
)

// parseV2rayNJSON reads the exported profile shape (ProfileItem) and a plain
// outbound object. It never opens a SQLite database or a v2rayN application
// config: those carry inbound/routing/TUN state and need a driver v1 does not
// ship (docs/contracts.md §5.8).
func parseV2rayNJSON(payload []byte, srcID contract.SourceID, now time.Time, audit *Audit) ([]contract.Record, error) {
	dec := json.NewDecoder(strings.NewReader(string(payload)))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("invalid v2rayN JSON")
	}
	var items []any
	switch t := root.(type) {
	case []any:
		items = t
	case map[string]any:
		for key := range t {
			lower := strings.ToLower(key)
			if rootScopeKeys[lower] || lower == "proxysettings" || lower == "balancertag" {
				return nil, fmt.Errorf("v2rayN application configuration is not a node export; export the links instead")
			}
		}
		if list, ok := lookup(t, "profiles", "ProfileItems", "servers"); ok {
			if arr, ok := list.([]any); ok {
				items = arr
			}
		} else if list, ok := lookup(t, "outbounds"); ok {
			if arr, ok := list.([]any); ok {
				items = arr
			}
		}
		if items == nil {
			items = []any{t}
		}
	default:
		return nil, fmt.Errorf("invalid v2rayN JSON")
	}
	if len(items) > contract.MaxRecordsPerSource {
		return nil, fmt.Errorf("v2rayN payload exceeds the record limit")
	}

	records := make([]contract.Record, 0, len(items))
	for i, item := range items {
		records = append(records, v2rayNItem(item, srcID, i, now, audit))
	}
	return records, nil
}

func v2rayNItem(item any, srcID contract.SourceID, order int, now time.Time, audit *Audit) contract.Record {
	m, ok := item.(map[string]any)
	if !ok {
		return rawRecord(item, contract.RecordInvalid, ReasonInvalidJSON, "", srcID, order, now)
	}
	name := getString(m, "remarks", "Remarks", "name", "tag")
	if reason := validateRecordJSON(m); reason != "" {
		return rawRecord(item, contract.RecordInvalid, reason, name, srcID, order, now)
	}
	if serialized, err := json.Marshal(m); err == nil && recordTooLarge(serialized) {
		return rawRecord(item, contract.RecordInvalid, ReasonRecordOversized, name, srcID, order, now)
	}
	if typ := strings.ToLower(getString(m, "type")); typ == "hysteria2" || typ == "hy2" {
		if name == "" {
			name = getString(m, "tag")
		}
		return singBoxHysteria2(item, m, srcID, order, now, name, audit)
	}
	if proto := normalizeProtocol(getString(m, "protocol")); proto == contract.ProtocolHysteria2 {
		return singBoxHysteria2(item, m, srcID, order, now, name, audit)
	}

	if _, hasAddr := lookup(m, "address", "Address", "server"); hasAddr {
		return v2rayNProfile(m, srcID, order, now, name, audit)
	}
	if _, isOutbound := lookup(m, "settings", "streamSettings", "protocol"); isOutbound {
		return v2rayNOutbound(m, srcID, order, now, name, audit)
	}
	return rawRecord(item, contract.RecordInvalid, ReasonInvalidJSON, name, srcID, order, now)
}

// singBoxHysteria2 projects one sing-box hysteria2 outbound object. It is
// deliberately strict: a sing-box *application* config (route/DNS/inbounds/
// log/...) is already refused by validateRecordJSON and parseV2rayNJSON, and
// any field on the outbound that RouteLatch does not own is refused rather
// than silently ignored. Nothing here starts a core or reads a file named by
// the payload (docs/contracts.md §5.4, AGENTS.md invariants).
func singBoxHysteria2(_ any, m map[string]any, srcID contract.SourceID, order int, now time.Time, name string, audit *Audit) contract.Record {
	n := node{
		protocol:  contract.ProtocolHysteria2,
		name:      name,
		transport: contract.TransportRaw,
		security:  contract.SecurityTLS,
	}
	var rej *rejection
	reject := func(status contract.RecordStatus, reason string) {
		if rej == nil {
			rej = &rejection{status, reason}
		}
	}

	for key := range m {
		if !singboxHy2Keys[strings.ToLower(key)] {
			// A hex certificate pin, realm, port hopping, detour, TLS files
			// and dial fields all change trust or routing outside our control.
			reject(contract.RecordInvalid, ReasonSingBoxField)
		}
	}

	server, err := NormalizeServer(getString(m, "server", "address"))
	if err != nil || server == "" {
		reject(contract.RecordInvalid, ReasonInvalidServer)
	} else {
		n.server = server
	}
	if port, ok := getPort(m, "server_port", "port"); ok {
		n.port = port
	} else {
		reject(contract.RecordInvalid, ReasonInvalidPort)
	}

	password := getString(m, "password")
	if password == "" || !noControl(password) {
		reject(contract.RecordInvalid, ReasonNoCredential)
	} else {
		n.creds.Password = password
	}
	if raw, present := lookup(m, "up_mbps"); present {
		if up, ok := hyBandwidth(raw); ok {
			n.hyUpMbps = up
		} else {
			reject(contract.RecordInvalid, ReasonValueTooLong)
		}
	}
	if raw, present := lookup(m, "down_mbps"); present {
		if down, ok := hyBandwidth(raw); ok {
			n.hyDownMbps = down
		} else {
			reject(contract.RecordInvalid, ReasonValueTooLong)
		}
	}

	if raw, present := lookup(m, "obfs"); present {
		om, ok := raw.(map[string]any)
		if !ok {
			reject(contract.RecordInvalid, ReasonInvalidObfs)
		} else {
			for key := range om {
				if !singboxHy2ObfsKeys[strings.ToLower(key)] {
					reject(contract.RecordInvalid, ReasonSingBoxField)
				}
			}
			obfs := strings.ToLower(getString(om, "type"))
			if !validHyObfs(obfs) {
				reject(contract.RecordInvalid, ReasonInvalidObfs)
			} else {
				n.hyObfs = obfs
			}
			obfsPassword := getString(om, "password")
			if !noControl(obfsPassword) || len(obfsPassword) > maxHyObfsPassword ||
				(obfs == "salamander" && obfsPassword == "") {
				reject(contract.RecordInvalid, ReasonInvalidObfs)
			} else {
				n.hyObfsPassword = obfsPassword
			}
		}
	}

	if raw, present := lookup(m, "tls"); !present {
		// TLS is mandatory for hysteria2; its absence is not a silent downgrade.
		reject(contract.RecordUnsupported, ReasonUnsupportedSecurity)
	} else if tm, ok := raw.(map[string]any); !ok {
		reject(contract.RecordUnsupported, ReasonUnsupportedSecurity)
	} else {
		for key := range tm {
			if !singboxHy2TLSKeys[strings.ToLower(key)] {
				reject(contract.RecordInvalid, ReasonSingBoxField)
			}
		}
		n.sni = getString(tm, "server_name")
		if truthy(getString(tm, "insecure")) || boolValue(tm["insecure"]) {
			n.insecure = true
		}
	}
	if n.insecure && n.sni == "" {
		// No pin can be carried to a sidecar, and no name keeps verification
		// on: the downgrade is quarantined, never dialed (D18).
		reject(contract.RecordUnsupported, contract.ReasonInsecureTLSRemoved)
	} else if n.insecure && n.verify == "" && n.sni != "" {
		n.verify = n.sni
	}
	return buildRecord(n, rej, srcID, order, now, audit)
}

// hyBandwidth reads a bounded, non-negative Mbps value. It never reads a
// bandwidth parameter from a share-link URI: only trusted callers and an
// explicit outbound object may set it.
func hyBandwidth(v any) (uint32, bool) {
	if v == nil {
		return 0, true
	}
	n, ok := intValue(v)
	if !ok || n < 0 || n > maxHyMbps {
		return 0, false
	}
	return uint32(n), true
}

func v2rayNProfile(m map[string]any, srcID contract.SourceID, order int, now time.Time, name string, audit *Audit) contract.Record {
	n := node{name: name}
	proto, rej := profileProtocol(m)
	n.protocol = proto

	rawServer := getString(m, "address", "Address", "server")
	server, err := NormalizeServer(rawServer)
	if err != nil {
		if rej == nil {
			rej = &rejection{contract.RecordInvalid, ReasonInvalidServer}
		}
	} else {
		n.server = server
	}
	if port, ok := getPort(m, "port", "Port"); ok {
		n.port = port
	} else if rej == nil {
		rej = &rejection{contract.RecordInvalid, ReasonInvalidPort}
	}

	n.creds = profileCredentials(proto, m)
	n.transport, _ = transportFromString(getString(m, "network", "Network"))
	if n.transport == "" {
		n.transport = contract.TransportRaw
	}
	secRaw := getString(m, "streamSecurity", "StreamSecurity", "stream_security")
	n.security, _ = securityFromString(secRaw)
	if secRaw == "" && n.protocol == contract.ProtocolHysteria2 {
		// hysteria2 is always TLS; a missing v2rayN security field is not a
		// downgrade to plaintext.
		n.security = contract.SecurityTLS
	}
	n.sni = getString(m, "sni", "Sni", "serverName")
	if alpnRaw, ok := lookup(m, "alpn", "Alpn"); ok {
		n.alpn = alpnStrings(alpnRaw)
	}
	n.fp = getString(m, "fingerprint", "Fingerprint")
	n.flow = getString(m, "flow", "Flow")
	n.path = getString(m, "path", "Path")
	n.host = getString(m, "requestHost", "RequestHost", "host", "Host")

	// Nesting that v2rayN export also accepts.
	if ss, ok := lookup(m, "streamSettings"); ok {
		if sm, ok := ss.(map[string]any); ok {
			applyStreamSettings(&n, sm, &rej)
		}
	}
	if extraRaw, ok := lookup(m, "extra", "Extra"); ok {
		extra, reason := decodeJSONExtra(extraRaw)
		if reason != "" {
			if rej == nil {
				rej = &rejection{contract.RecordInvalid, reason}
			}
		}
		if len(extra) > 0 {
			n.xhttp = mergeExtra(n.xhttp, extra)
		}
	}
	n.publicKey = firstNonEmpty(n.publicKey, getString(m, "publicKey", "PublicKey"))
	n.shortID = firstNonEmpty(n.shortID, getString(m, "shortId", "ShortId"))
	n.spiderX = firstNonEmpty(n.spiderX, getString(m, "spiderX", "SpiderX"))
	if n.insecure || truthy(getString(m, "allowInsecure", "AllowInsecure")) || boolValue(m["AllowInsecure"]) {
		n.insecure = true
	}
	n.pin = getString(m, "pinnedPeerCertSha256", "pinSHA256")
	if n.pin == "" {
		if ss, ok := lookup(m, "tlsSettings"); ok {
			if tm, ok := ss.(map[string]any); ok {
				n.pin = getString(tm, "pinnedPeerCertSha256", "pinSHA256")
				n.verify = getString(tm, "verifyPeerCertByName")
			}
		}
	}
	n.verify = firstNonEmpty(n.verify, getString(m, "verifyPeerCertByName"))
	if n.pin != "" {
		if pin, ok := validatePin(n.pin); ok {
			n.pin = pin
		} else if rej == nil {
			rej = &rejection{contract.RecordInvalid, ReasonInvalidPin}
		}
	}
	if n.verify != "" {
		if vn, ok := validateVerifyName(n.verify); ok {
			n.verify = vn
		} else if rej == nil {
			rej = &rejection{contract.RecordInvalid, ReasonInvalidVerifyName}
		}
	}
	if n.transport != contract.TransportXHTTP && n.xhttp != nil {
		if rej == nil {
			rej = &rejection{contract.RecordInvalid, ReasonInvalidExtra}
		}
	}
	if alpn, ok := sanitizeALPN(n.alpn); ok {
		n.alpn = alpn
	} else if rej == nil {
		rej = &rejection{contract.RecordInvalid, ReasonInvalidALPN}
	}
	return buildRecord(n, rej, srcID, order, now, audit)
}

func v2rayNOutbound(m map[string]any, srcID contract.SourceID, order int, now time.Time, name string, audit *Audit) contract.Record {
	n := node{name: name}
	rawProto := getString(m, "protocol")
	n.protocol = normalizeProtocol(rawProto)
	var rej *rejection
	if n.protocol == contract.ProtocolHTTP {
		rej = &rejection{contract.RecordUnsupported, ReasonHTTPExitUnverified}
	} else if !supportedProtocol(n.protocol) {
		rej = &rejection{contract.RecordUnsupported, ReasonUnsupportedProtocol}
	}

	settings, _ := m["settings"].(map[string]any)
	server, user := outboundServer(settings)
	if user == nil && server != nil && (n.protocol == contract.ProtocolTrojan || n.protocol == contract.ProtocolSOCKS5) {
		user = server
	}
	if server == nil {
		if rej == nil {
			rej = &rejection{contract.RecordInvalid, ReasonInvalidServer}
		}
	} else {
		if normalized, err := NormalizeServer(getString(server, "address", "server")); err == nil {
			n.server = normalized
		} else if rej == nil {
			rej = &rejection{contract.RecordInvalid, ReasonInvalidServer}
		}
		if port, ok := getPort(server, "port"); ok {
			n.port = port
		} else if rej == nil {
			rej = &rejection{contract.RecordInvalid, ReasonInvalidPort}
		}
		n.creds = outboundCredentials(n.protocol, user)
	}

	n.transport, _ = transportFromString(getString(m, "network"))
	n.security = contract.SecurityNone
	if ss, ok := m["streamSettings"].(map[string]any); ok {
		applyStreamSettings(&n, ss, &rej)
	}
	if n.transport == "" {
		n.transport = contract.TransportRaw
	}
	if alpn, ok := sanitizeALPN(n.alpn); ok {
		n.alpn = alpn
	} else if rej == nil {
		rej = &rejection{contract.RecordInvalid, ReasonInvalidALPN}
	}
	return buildRecord(n, rej, srcID, order, now, audit)
}

func profileProtocol(m map[string]any) (contract.Protocol, *rejection) {
	if raw := getString(m, "protocol", "Protocol"); raw != "" {
		p := normalizeProtocol(raw)
		if p == contract.ProtocolHTTP {
			return p, &rejection{contract.RecordUnsupported, ReasonHTTPExitUnverified}
		}
		if !supportedProtocol(p) {
			return p, &rejection{contract.RecordUnsupported, ReasonUnsupportedProtocol}
		}
		return p, nil
	}
	if ct, ok := lookup(m, "configType", "ConfigType"); ok {
		code, ok := intValue(ct)
		if ok {
			switch code {
			case 4:
				return contract.ProtocolSOCKS5, nil
			case 5:
				return contract.ProtocolVLESS, nil
			case 6:
				return contract.ProtocolTrojan, nil
			case 7:
				return contract.ProtocolHysteria2, nil
			case 10:
				return contract.ProtocolHTTP, &rejection{contract.RecordUnsupported, ReasonHTTPExitUnverified}
			case 1, 2, 3, 8, 9:
				return contract.Protocol(strings.ToLower(strings.TrimSpace(fmt.Sprint(ct)))), &rejection{contract.RecordUnsupported, ReasonUnsupportedProtocol}
			}
		}
	}
	return "", &rejection{contract.RecordInvalid, ReasonInvalidJSON}
}

func normalizeProtocol(raw string) contract.Protocol {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "socks", "socks5":
		return contract.ProtocolSOCKS5
	case "vless":
		return contract.ProtocolVLESS
	case "trojan":
		return contract.ProtocolTrojan
	case "http", "https":
		return contract.ProtocolHTTP
	case "hysteria2", "hy2":
		return contract.ProtocolHysteria2
	default:
		return contract.Protocol(strings.ToLower(strings.TrimSpace(raw)))
	}
}

func supportedProtocol(p contract.Protocol) bool {
	switch p {
	case contract.ProtocolVLESS, contract.ProtocolTrojan, contract.ProtocolSOCKS5,
		contract.ProtocolHysteria2, contract.ProtocolHTTP:
		return true
	default:
		return false
	}
}

func profileCredentials(proto contract.Protocol, m map[string]any) contract.Credentials {
	switch proto {
	case contract.ProtocolVLESS:
		return contract.Credentials{UserID: strings.ToLower(getString(m, "id", "Id", "userID", "uuid"))}
	case contract.ProtocolTrojan:
		return contract.Credentials{Password: getString(m, "password", "Password", "id", "Id")}
	case contract.ProtocolSOCKS5:
		return contract.Credentials{
			UserID:   getString(m, "username", "user", "id", "Id"),
			Password: getString(m, "password", "Password"),
		}
	case contract.ProtocolHysteria2:
		return contract.Credentials{Password: getString(m, "password", "Password", "id", "Id")}
	default:
		return contract.Credentials{}
	}
}

func outboundServer(settings map[string]any) (map[string]any, map[string]any) {
	if settings == nil {
		return nil, nil
	}
	for _, key := range []string{"vnext", "servers"} {
		if arr, ok := settings[key].([]any); ok && len(arr) > 0 {
			if server, ok := arr[0].(map[string]any); ok {
				if users, ok := server["users"].([]any); ok && len(users) > 0 {
					user, _ := users[0].(map[string]any)
					return server, user
				}
				return server, nil
			}
		}
	}
	return nil, nil
}

func outboundCredentials(proto contract.Protocol, user map[string]any) contract.Credentials {
	if user == nil {
		return contract.Credentials{}
	}
	switch proto {
	case contract.ProtocolVLESS:
		return contract.Credentials{UserID: strings.ToLower(getString(user, "id", "userID"))}
	case contract.ProtocolTrojan:
		return contract.Credentials{Password: getString(user, "password")}
	case contract.ProtocolSOCKS5:
		return contract.Credentials{UserID: getString(user, "user"), Password: getString(user, "pass", "password")}
	case contract.ProtocolHysteria2:
		return contract.Credentials{Password: getString(user, "password", "pass")}
	default:
		return contract.Credentials{}
	}
}

func applyStreamSettings(n *node, ss map[string]any, rej **rejection) {
	if net, ok := lookup(ss, "network"); ok {
		if t, ok := transportFromString(fmt.Sprint(net)); ok {
			n.transport = t
		} else if *rej == nil {
			*rej = &rejection{contract.RecordUnsupported, ReasonUnsupportedTransport}
		}
	}
	if sec, ok := lookup(ss, "security"); ok {
		if s, ok := securityFromString(fmt.Sprint(sec)); ok {
			n.security = s
		} else if *rej == nil {
			*rej = &rejection{contract.RecordUnsupported, ReasonUnsupportedSecurity}
		}
	}
	if ts, ok := ss["tlsSettings"].(map[string]any); ok {
		n.sni = firstNonEmpty(n.sni, getString(ts, "serverName", "sni"))
		n.fp = firstNonEmpty(n.fp, getString(ts, "fingerprint"))
		if alpn, ok := ts["alpn"].([]any); ok {
			n.alpn = append(n.alpn, alpnStrings(alpn)...)
		}
		if truthy(getString(ts, "allowInsecure")) || boolValue(ts["allowInsecure"]) {
			n.insecure = true
		}
		if pin := getString(ts, "pinnedPeerCertSha256", "pinSHA256"); pin != "" {
			n.pin = pin
		}
		if vn := getString(ts, "verifyPeerCertByName"); vn != "" {
			n.verify = vn
		}
	}
	if rs, ok := ss["realitySettings"].(map[string]any); ok {
		n.sni = firstNonEmpty(n.sni, getString(rs, "serverName"))
		n.fp = firstNonEmpty(n.fp, getString(rs, "fingerprint"))
		n.publicKey = getString(rs, "publicKey")
		n.shortID = getString(rs, "shortId")
		n.spiderX = getString(rs, "spiderX")
	}
	if ws, ok := ss["wsSettings"].(map[string]any); ok {
		n.path = firstNonEmpty(n.path, getString(ws, "path"))
		if headers, ok := ws["headers"].(map[string]any); ok {
			n.host = firstNonEmpty(n.host, getString(headers, "Host", "host"))
			if reason := validateHeaders(headers); reason != "" && *rej == nil {
				*rej = &rejection{contract.RecordInvalid, reason}
			}
		}
	}
	if gs, ok := ss["grpcSettings"].(map[string]any); ok {
		n.grpc = firstNonEmpty(n.grpc, getString(gs, "serviceName"))
	}
	if xs, ok := ss["xhttpSettings"].(map[string]any); ok {
		extra, reason := sanitizeXHTTPExtra(xs)
		if reason != "" {
			if *rej == nil {
				*rej = &rejection{contract.RecordInvalid, reason}
			}
		} else {
			n.xhttp = mergeExtra(n.xhttp, extra)
		}
	}
}

func mergeExtra(dst, src map[string]any) map[string]any {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]any, len(src))
	}
	for k, v := range src {
		if _, exists := dst[k]; !exists {
			dst[k] = v
		}
	}
	return dst
}

func rawRecord(item any, status contract.RecordStatus, reason, name string, srcID contract.SourceID, order int, now time.Time) contract.Record {
	raw, _ := json.Marshal(item)
	rec := buildRecord(node{name: name}, &rejection{status, reason}, srcID, order, now, nil)
	rec.DedupeKey = dedupeKeyForRaw(raw)
	rec.ID = RecordIDForKey(rec.DedupeKey)
	return rec
}

func alpnStrings(v any) []string {
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func lookup(m map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			return v, true
		}
	}
	for k, v := range m {
		for _, key := range keys {
			if strings.EqualFold(k, key) {
				return v, true
			}
		}
	}
	return nil, false
}

func getString(m map[string]any, keys ...string) string {
	v, ok := lookup(m, keys...)
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func getPort(m map[string]any, keys ...string) (uint16, bool) {
	v, ok := lookup(m, keys...)
	if !ok {
		return 0, false
	}
	if n, ok := intValue(v); ok && n > 0 && n <= 65535 {
		return uint16(n), true
	}
	return 0, false
}

func intValue(v any) (int64, bool) {
	switch t := v.(type) {
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			f, ferr := t.Float64()
			if ferr != nil {
				return 0, false
			}
			return int64(f), true
		}
		return n, true
	case float64:
		return int64(t), true
	case int:
		return int64(t), true
	case int64:
		return t, true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func boolValue(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return truthy(t)
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
