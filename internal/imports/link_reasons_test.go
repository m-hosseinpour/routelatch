package imports

import (
	"context"
	"testing"

	"routelatch/internal/contract"
)

// TestLinkReasonsForNeverDialableSchemes pins the importer's reasons, which the
// pool view reuses when it derives a legacy unknown record (docs/contracts.md
// §5.3–5.4).
func TestLinkReasonsForNeverDialableSchemes(t *testing.T) {
	im := newTestImporter()
	cases := []struct {
		name     string
		link     string
		protocol contract.Protocol
		reason   string
	}{
		{"http exit", "http://exit.example.com:8080/#legacy-http", contract.ProtocolHTTP, contract.ReasonHTTPExitNotVerified},
		{"https exit", "https://exit.example.com/#legacy-https", contract.ProtocolHTTP, contract.ReasonHTTPExitNotVerified},
		{"unknown scheme", "vmess://eyJhZGQiOiJleGFtcGxlLmNvbSJ9#legacy-vmess", contract.Protocol("vmess"), contract.ReasonProtocolUnsupported},
	}
	for _, tc := range cases {
		recs, err := im.Import(context.Background(), testSource(contract.SourceManual, tc.link), []byte(tc.link))
		if err != nil {
			t.Fatalf("%s: import: %v", tc.name, err)
		}
		if len(recs) != 1 {
			t.Fatalf("%s: records=%d", tc.name, len(recs))
		}
		rec := recs[0]
		if rec.Status != contract.RecordUnsupported || rec.StatusReason != tc.reason || rec.Protocol != tc.protocol {
			t.Fatalf("%s: got status=%s reason=%s protocol=%s, want unsupported/%s/%s",
				tc.name, rec.Status, rec.StatusReason, rec.Protocol, tc.reason, tc.protocol)
		}
	}
	// Pin the wire-visible literals; the importer and view share these constants.
	if contract.ReasonProtocolUnsupported != "protocol_unsupported" ||
		contract.ReasonHTTPExitNotVerified != "http_exit_not_verified" {
		t.Fatal("wire-visible reason literals changed")
	}
}
