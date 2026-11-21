package imports

import (
	"context"
	"strings"
	"testing"

	"routelatch/internal/contract"
)

func importOne(t *testing.T, payload string) (contract.Record, Audit) {
	t.Helper()
	im := newTestImporter()
	recs, audit, err := im.ImportWithAudit(context.Background(), testSource(contract.SourceManual, "hy2"), []byte(payload))
	if err != nil {
		t.Fatalf("import %q: %v", payload, err)
	}
	if len(recs) != 1 {
		t.Fatalf("records=%d for %q", len(recs), payload)
	}
	return recs[0], audit
}

func TestHysteria2URIProjection(t *testing.T) {
	// The URI allow-list never reads bandwidth; RouteLatch applies its own.
	link := "hysteria2://secret@Example.COM:8443?obfs=salamander&obfs-password=cry_me_a_river&sni=hy.example.com&up_mbps=9999&down_mbps=9999#Hy%202"
	r, _ := importOne(t, link)
	if r.Status != contract.RecordUnknown || r.StatusReason != "" {
		t.Fatalf("status=%s reason=%s", r.Status, r.StatusReason)
	}
	if r.Protocol != contract.ProtocolHysteria2 || r.Server != "example.com" || r.Port != 8443 {
		t.Fatalf("identity: %+v", r)
	}
	if r.Credentials.Password != "secret" || r.Credentials.UserID != "" {
		t.Fatalf("credentials: %+v", r.Credentials)
	}
	if r.Transport != contract.TransportRaw || r.Security != contract.SecurityTLS {
		t.Fatalf("transport=%s security=%s", r.Transport, r.Security)
	}
	if r.SNI != "hy.example.com" || r.Hysteria2Obfs != "salamander" || r.Hysteria2ObfsPassword != "cry_me_a_river" {
		t.Fatalf("hysteria2 fields: %+v", r)
	}
	if r.Hysteria2UpMbps != 0 || r.Hysteria2DownMbps != 0 {
		t.Fatalf("bandwidth was read from the URI: %d/%d", r.Hysteria2UpMbps, r.Hysteria2DownMbps)
	}
	if r.DisplayName != "Hy 2" {
		t.Fatalf("display=%q", r.DisplayName)
	}

	// hy2 alias with no explicit port uses the hysteria2 default, 443.
	r, _ = importOne(t, "hy2://pw@example.com#d")
	if r.Port != 443 || r.Status != contract.RecordUnknown {
		t.Fatalf("default port: status=%s port=%d", r.Status, r.Port)
	}

	// The official userpass alias is carried as a single combined password.
	r, _ = importOne(t, "hysteria2://user:pass@example.com:443#u")
	if r.Credentials.Password != "user:pass" {
		t.Fatalf("userpass alias: %q", r.Credentials.Password)
	}
}

func TestHysteria2URIInvalidAndQuarantine(t *testing.T) {
	cases := []struct {
		link   string
		status contract.RecordStatus
		reason string
	}{
		{"hysteria2://example.com:443#no-password", contract.RecordInvalid, ReasonNoCredential},
		{"hysteria2://pw@example.com:notaport#bad-port", contract.RecordInvalid, ReasonInvalidJSON},
		{"hysteria2://pw@example.com:443?obfs=unknown#bad-obfs", contract.RecordInvalid, ReasonInvalidObfs},
		{"hysteria2://pw@example.com:443?obfs=salamander#no-obfs-pw", contract.RecordInvalid, ReasonInvalidObfs},
		{"hysteria2://pw@example.com:443?obfs=salamander&obfs-password=a%00b#ctrl", contract.RecordInvalid, ReasonInvalidObfs},
		{"hysteria2://pw@example.com:443?ech=1#ech", contract.RecordUnsupported, ReasonECHUnsupported},
		{"hysteria2://pw@example.com:443?insecure=1#insecure", contract.RecordUnsupported, contract.ReasonInsecureTLSRemoved},
		{"hysteria2://pw@example.com:443?insecure=1&pinSHA256=zz#badpin", contract.RecordInvalid, ReasonInvalidPin},
	}
	for _, tc := range cases {
		r, _ := importOne(t, tc.link)
		if r.Status != tc.status || r.StatusReason != tc.reason {
			t.Fatalf("%s: status=%s reason=%s want %s/%s", tc.link, r.Status, r.StatusReason, tc.status, tc.reason)
		}
	}

	// A downgrade paired with an SNI is ignored: verification stays on and the
	// record is not marked insecure (D18 as amended).
	r, audit := importOne(t, "hysteria2://pw@example.com:443?insecure=1&sni=hy.example.com#named")
	if r.Status != contract.RecordUnknown || r.InsecureTLS || r.VerifyPeerCertByName != "hy.example.com" {
		t.Fatalf("ignored insecure: %+v", r)
	}
	if !strings.Contains(strings.Join(audit.Notices, " "), "allow_insecure_ignored") {
		t.Fatalf("no notice for ignored insecure: %+v", audit.Notices)
	}
}

// TestHysteria2ObfsPasswordRequired pins the import gate for the two accepted
// obfuscators. sing-box v1.14.4 rejects a hysteria2 obfs block without a
// password ("missing obfs password") for both salamander and gecko, so an
// accepted obfs type without one must never become a usable record.
func TestHysteria2ObfsPasswordRequired(t *testing.T) {
	// obfs stays optional: a link without it is unchanged.
	plain, _ := importOne(t, "hysteria2://pw@example.com:443#plain")
	if plain.Status != contract.RecordUnknown || plain.StatusReason != "" ||
		plain.Hysteria2Obfs != "" || plain.Hysteria2ObfsPassword != "" {
		t.Fatalf("no-obfs link: %+v", plain)
	}

	for _, tc := range []struct{ link, obfs string }{
		{"hysteria2://pw@example.com:443?obfs=salamander&obfs-password=river#salamander", "salamander"},
		{"hysteria2://pw@example.com:443?obfs=gecko&obfs-password=river#gecko", "gecko"},
		// The camelCase alias must be recognized, not silently read as empty.
		{"hysteria2://pw@example.com:443?obfs=gecko&obfsPassword=river#gecko-alias", "gecko"},
	} {
		r, _ := importOne(t, tc.link)
		if r.Status != contract.RecordUnknown || r.StatusReason != "" {
			t.Fatalf("%s: status=%s reason=%s", tc.obfs, r.Status, r.StatusReason)
		}
		if r.Hysteria2Obfs != tc.obfs || r.Hysteria2ObfsPassword != "river" {
			t.Fatalf("%s fields: %+v", tc.obfs, r)
		}
	}

	// gecko without a password (absent or empty) is unusable.
	for _, link := range []string{
		"hysteria2://pw@example.com:443?obfs=gecko#gecko-no-pw",
		"hysteria2://pw@example.com:443?obfs=gecko&obfs-password=#gecko-empty-pw",
		"hy2://pw@example.com?obfs=gecko#gecko-no-pw-hy2",
		"hysteria2://pw@example.com:443?obfs=gecko&obfsPassword=#gecko-empty-alias",
	} {
		r, _ := importOne(t, link)
		if r.Status != contract.RecordInvalid || r.StatusReason != ReasonMissingObfsPassword {
			t.Fatalf("%s: status=%s reason=%s want invalid/%s", link, r.Status, r.StatusReason, ReasonMissingObfsPassword)
		}
	}

	// salamander already required a password before this gate; its stable
	// reason stays invalid_obfs rather than the new gecko-specific reason.
	r, _ := importOne(t, "hysteria2://pw@example.com:443?obfs=salamander#salamander-no-pw")
	if r.Status != contract.RecordInvalid || r.StatusReason != ReasonInvalidObfs {
		t.Fatalf("salamander without password: status=%s reason=%s", r.Status, r.StatusReason)
	}

	// The sing-box hysteria2 outbound reader shares the same gate.
	json := `{"type":"hysteria2","server":"hy.example.com","server_port":443,"password":"pw","tls":{},"obfs":{"type":"gecko"}}`
	r, _ = importOne(t, json)
	if r.Status != contract.RecordInvalid || r.StatusReason != ReasonMissingObfsPassword {
		t.Fatalf("gecko outbound without password: status=%s reason=%s", r.Status, r.StatusReason)
	}
}

func TestHysteria2PinInsecureNoticeIsHonest(t *testing.T) {
	// A valid hex pin keeps verification on, so the record imports as unknown
	// and is not marked insecure. The engine later refuses every hysteria2 pin
	// (it cannot enforce a hex leaf pin), so the audit notice must not claim the
	// pin "is applied" to a dialable exit.
	pin := strings.Repeat("ab", 32)
	r, audit := importOne(t, "hysteria2://pw@example.com:443?insecure=1&pinSHA256="+pin+"#pinned")
	if r.Status != contract.RecordUnknown || r.InsecureTLS || r.PinnedPeerCertSHA256 != pin {
		t.Fatalf("pinned+insecure hysteria2 record: %+v", r)
	}
	joined := strings.Join(audit.Notices, " ")
	if !strings.Contains(joined, "allow_insecure_ignored") {
		t.Fatalf("no insecure-ignored notice: %v", audit.Notices)
	}
	if strings.Contains(joined, "pin or name constraint is applied") || !strings.Contains(joined, "not dialable") {
		t.Fatalf("dishonest notice for a rejected pinned hysteria2 record: %v", audit.Notices)
	}
}

func TestHysteria2SingBoxOutbound(t *testing.T) {
	valid := `{"type":"hysteria2","tag":"hy2-out","server":"hy.example.com","server_port":443,` +
		`"password":"pw","up_mbps":100,"down_mbps":200,` +
		`"obfs":{"type":"salamander","password":"river"},` +
		`"tls":{"enabled":true,"server_name":"hy.example.com","insecure":false}}`
	r, _ := importOne(t, valid)
	if r.Status != contract.RecordUnknown || r.Protocol != contract.ProtocolHysteria2 {
		t.Fatalf("status=%s protocol=%s", r.Status, r.Protocol)
	}
	if r.Server != "hy.example.com" || r.Port != 443 || r.Credentials.Password != "pw" {
		t.Fatalf("projection: %+v", r)
	}
	if r.Hysteria2UpMbps != 100 || r.Hysteria2DownMbps != 200 || r.Hysteria2Obfs != "salamander" ||
		r.Hysteria2ObfsPassword != "river" || r.SNI != "hy.example.com" {
		t.Fatalf("hysteria2 fields: %+v", r)
	}
	if r.Transport != contract.TransportRaw || r.Security != contract.SecurityTLS {
		t.Fatalf("transport=%s security=%s", r.Transport, r.Security)
	}

	// A downgrade without a name is quarantined, exactly as for a URI.
	insecure := `{"type":"hysteria2","server":"hy.example.com","server_port":443,"password":"pw","tls":{"enabled":true,"insecure":true}}`
	r, _ = importOne(t, insecure)
	if r.Status != contract.RecordUnsupported || r.StatusReason != contract.ReasonInsecureTLSRemoved {
		t.Fatalf("insecure outbound: %+v", r)
	}

	// With a name, verification stays on and the ignored downgrade is visible
	// in the audit, just as it is for a URI.
	named := `{"type":"hysteria2","server":"hy.example.com","server_port":443,"password":"pw","tls":{"enabled":true,"insecure":true,"server_name":"hy.example.com"}}`
	r, audit := importOne(t, named)
	if r.Status != contract.RecordUnknown || r.InsecureTLS {
		t.Fatalf("named insecure outbound: %+v", r)
	}
	if !strings.Contains(strings.Join(audit.Notices, " "), "allow_insecure_ignored") {
		t.Fatalf("no notice for ignored insecure JSON: %+v", audit.Notices)
	}
}

func TestHysteria2RootConfigAndUnknownFieldsRefused(t *testing.T) {
	// A whole application config is never read as a node export.
	appConfigs := []string{
		`{"route":{"rules":[]},"inbounds":[],"outbounds":[{"type":"hysteria2","server":"hy.example.com","server_port":443,"password":"pw","tls":{}}]}`,
		`{"dns":{"servers":[]},"log":{"disabled":true},"outbounds":[{"type":"hysteria2","server":"hy.example.com","server_port":443,"password":"pw","tls":{}}]}`,
	}
	for _, payload := range appConfigs {
		im := newTestImporter()
		for _, kind := range []contract.SourceKind{contract.SourceV2rayN, contract.SourceFile} {
			if _, err := im.Import(context.Background(), testSource(kind, "app.json"), []byte(payload)); err == nil {
				t.Fatalf("application config was accepted as %s: %s", kind, payload)
			}
		}
	}

	// Unknown or trust-changing outbound fields are refused, not ignored.
	fields := []struct {
		payload string
		reason  string
	}{
		{`{"type":"hysteria2","server":"hy.example.com","server_port":443,"password":"pw","tls":{},"realm":{"server_url":"https://x"}}`, ReasonSingBoxField},
		{`{"type":"hysteria2","server":"hy.example.com","server_port":443,"password":"pw","tls":{},"transport":{"type":"ws"}}`, ReasonSingBoxField},
		{`{"type":"hysteria2","server":"hy.example.com","server_port":443,"password":"pw","tls":{"enabled":true,"certificate_path":"/etc/passwd"}}`, ReasonSingBoxField},
		{`{"type":"hysteria2","server":"hy.example.com","server_port":443,"password":"pw","tls":{},"detour":"other"}`, ReasonOutboundDependency},
		{`{"type":"hysteria2","server":"hy.example.com","server_port":443,"password":"pw","tls":{"enabled":true,"insecure":true,"ech":{}}}`, ReasonSingBoxField},
	}
	for _, tc := range fields {
		r, _ := importOne(t, tc.payload)
		if r.Status != contract.RecordInvalid || r.StatusReason != tc.reason {
			t.Fatalf("%s: status=%s reason=%s want invalid/%s", tc.payload, r.Status, r.StatusReason, tc.reason)
		}
	}
}

func TestHysteria2ControlCharactersSanitized(t *testing.T) {
	// Control and bidi characters in a name never survive into the record;
	// a control character in a credential or obfs password rejects the record.
	link := "hysteria2://pw@example.com:8443#bad%0A%1B%5D%202%0D%20name%E2%80%AE"
	r, _ := importOne(t, link)
	if r.DisplayName != "bad ] 2  name" {
		t.Fatalf("display=%q", r.DisplayName)
	}
	if strings.ContainsAny(r.DisplayName, "\r\n\x1b\x00") || strings.ContainsRune(r.DisplayName, '\u202e') {
		t.Fatalf("control character survived: %q", r.DisplayName)
	}
}
