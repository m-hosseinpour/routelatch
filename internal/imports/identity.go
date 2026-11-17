// Package imports turns untrusted subscription payloads and v2rayN profiles
// into canonical contract.Record values. It never executes, fetches or opens a
// path named by a payload (docs/contracts.md §5).
package imports

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/idna"

	"routelatch/internal/contract"
)

// SourceIDFor implements the identity rule in docs/contracts.md §5.1:
// "s-" + the first 12 hex characters of sha256(kind + "\x00" + canonical
// origin). The canonical origin is secret-bearing input and is never returned.
func SourceIDFor(kind contract.SourceKind, origin string) contract.SourceID {
	canon := canonicalOrigin(kind, origin)
	sum := sha256.Sum256([]byte(string(kind) + "\x00" + canon))
	return contract.SourceID("s-" + hex.EncodeToString(sum[:])[:12])
}

func canonicalOrigin(kind contract.SourceKind, origin string) string {
	switch kind {
	case contract.SourceSubscription:
		u, err := url.Parse(strings.TrimSpace(origin))
		if err != nil {
			return strings.TrimSpace(origin)
		}
		u.Scheme = strings.ToLower(u.Scheme)
		host := strings.ToLower(u.Hostname())
		port := u.Port()
		if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
			port = ""
		}
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		if port != "" {
			u.Host = host + ":" + port
		} else {
			u.Host = host
		}
		u.Fragment = ""
		if q := u.Query(); len(q) > 0 {
			u.RawQuery = q.Encode() // url.Values.Encode sorts keys.
		}
		return u.String()
	case contract.SourceFile, contract.SourceV2rayN:
		p := strings.TrimSpace(origin)
		if abs, err := filepath.Abs(p); err == nil {
			return filepath.Clean(abs)
		}
		return filepath.Clean(p)
	default:
		return strings.TrimSpace(origin)
	}
}

// RecordIDForKey derives the stable record id from the dedupe key. It is not a
// second identity: changing the connection settings changes both.
func RecordIDForKey(key string) contract.RecordID {
	digest := strings.TrimPrefix(key, "sha256:")
	if len(digest) > 16 {
		digest = digest[:16]
	}
	return contract.RecordID("x-" + digest)
}

// DedupeKey computes "sha256:<hex>" over the canonical connection settings in
// docs/contracts.md §5.1. Display name, source, order and timestamps are not
// inputs. Empty/default fields are omitted so equivalent links agree.
func DedupeKey(r contract.Record) string {
	var b strings.Builder
	field := func(name, value string) {
		if value == "" {
			return
		}
		fmt.Fprintf(&b, "%s=%d:%s\n", name, len(value), value)
	}
	field("protocol", string(r.Protocol))
	field("server", strings.ToLower(r.Server))
	field("transport", string(r.Transport))
	field("security", string(r.Security))
	if r.Port != 0 {
		fmt.Fprintf(&b, "port=%d\n", r.Port)
	}
	field("sni", strings.ToLower(r.SNI))
	if len(r.ALPN) > 0 {
		alpn := append([]string(nil), r.ALPN...)
		sort.Strings(alpn)
		field("alpn", strings.Join(alpn, "\x1f"))
	}
	field("fingerprint", r.Fingerprint)
	field("flow", r.Flow)
	field("path", r.Path)
	field("host", strings.ToLower(r.Host))
	field("grpc", r.GRPCService)
	field("pinned_peer_cert_sha256", strings.ToLower(r.PinnedPeerCertSHA256))
	field("verify_peer_cert_by_name", strings.ToLower(r.VerifyPeerCertByName))
	if extra := canonicalExtra(r.XHTTPExtra); extra != "" {
		field("xhttp_extra", extra)
	}
	if r.Credentials.UserID != "" || r.Credentials.Password != "" {
		var credentialBytes string
		switch r.Protocol {
		case contract.ProtocolVLESS:
			credentialBytes = r.Credentials.UserID
		case contract.ProtocolTrojan:
			credentialBytes = r.Credentials.UserID // Trojan password lives in UserID
		default:
			credentialBytes = r.Credentials.UserID + "\x00" + r.Credentials.Password
		}
		auth := sha256.Sum256([]byte(credentialBytes))
		field("auth", hex.EncodeToString(auth[:]))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// dedupeKeyForRaw is the fallback for records whose connection settings could
// not be parsed at all. The digest of the untrusted bytes is safe to keep as an
// identity; the bytes themselves are never stored in the key.
func dedupeKeyForRaw(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func canonicalExtra(extra map[string]any) string {
	if len(extra) == 0 {
		return ""
	}
	var b strings.Builder
	appendCanonicalMap(&b, extra)
	return b.String()
}

func appendCanonicalMap(b *strings.Builder, m map[string]any) {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if isEmptyValue(v) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		appendCanonicalString(b, k)
		b.WriteByte(':')
		appendCanonicalValue(b, m[k])
	}
	b.WriteByte('}')
}

func appendCanonicalValue(b *strings.Builder, v any) {
	switch t := v.(type) {
	case map[string]any:
		appendCanonicalMap(b, t)
	case []any:
		b.WriteByte('[')
		n := 0
		for _, item := range t {
			if isEmptyValue(item) {
				continue
			}
			if n > 0 {
				b.WriteByte(',')
			}
			appendCanonicalValue(b, item)
			n++
		}
		b.WriteByte(']')
	case string:
		appendCanonicalString(b, t)
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case json.Number:
		b.WriteString(string(t))
	case float64:
		b.WriteString(strconv.FormatFloat(t, 'g', -1, 64))
	case int:
		b.WriteString(strconv.Itoa(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case nil:
		b.WriteString("null")
	default:
		// The allow-list produces only the types above; this branch exists so
		// a future field cannot silently serialize an arbitrary value.
		appendCanonicalString(b, fmt.Sprint(t))
	}
}

func appendCanonicalString(b *strings.Builder, s string) {
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
}

func isEmptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case json.Number:
		return t == "0" || t == "0.0"
	case float64:
		return t == 0
	case int:
		return t == 0
	case int64:
		return t == 0
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

// NormalizeServer accepts an IP literal or an A-label hostname exactly as the
// import path needs it. It is deliberately narrower than routing.NormalizeDestination
// (which T3 owns): imports must not invent destination classification or
// resolve names. The result is lowercase, bracket-free and has no trailing dot.
func NormalizeServer(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 2048 {
		return "", fmt.Errorf("empty or oversized server")
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	}
	if strings.ContainsAny(s, "\x00\r\n\t /@?#*[]") {
		return "", fmt.Errorf("server contains a forbidden character")
	}
	if ip, err := netip.ParseAddr(s); err == nil {
		if ip.Zone() != "" {
			return "", fmt.Errorf("zoned address")
		}
		ip = ip.Unmap()
		if !ip.IsValid() || ip.IsUnspecified() {
			return "", fmt.Errorf("unspecified address")
		}
		return ip.String(), nil
	}
	s = strings.TrimSuffix(s, ".")
	if s == "" {
		return "", fmt.Errorf("empty hostname")
	}
	ascii, err := idna.Lookup.ToASCII(strings.ToLower(s))
	if err != nil {
		return "", fmt.Errorf("idna: %w", err)
	}
	if len(ascii) > 253 {
		return "", fmt.Errorf("hostname too long")
	}
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 {
			return "", fmt.Errorf("invalid label")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid label boundary")
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return "", fmt.Errorf("invalid hostname character")
			}
		}
	}
	return ascii, nil
}

// SanitizeDisplay makes an untrusted name safe to store and to render again.
// Callers must still pass every display string through the terminal sanitizer
// before printing (docs/contracts.md §7.4); this only removes control and
// bidi characters and enforces the stored length bound.
func SanitizeDisplay(raw string) string {
	raw = strings.TrimSpace(raw)
	var b strings.Builder
	count := 0
	for _, r := range raw {
		if count >= contract.MaxNodeNameRunes {
			break
		}
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			r = ' '
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			continue
		case r >= 0x202a && r <= 0x202e:
			continue
		case r >= 0x2066 && r <= 0x2069:
			continue
		case r == 0x200e || r == 0x200f:
			continue
		}
		b.WriteRune(r)
		count++
	}
	return strings.TrimSpace(b.String())
}
