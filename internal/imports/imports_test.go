package imports

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"routelatch/internal/contract"
)

const testUUID = "b831381d-6324-4d53-ad4f-8cda48b30811"

func fixedNow() time.Time { return time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC) }

func newTestImporter() *Importer { return NewImporterWithClock(fixedNow) }

func testSource(kind contract.SourceKind, origin string) contract.Source {
	return contract.Source{ID: SourceIDFor(kind, origin), Kind: kind, Origin: origin, Label: "test"}
}

func TestParseVLESSTrojanSocks5(t *testing.T) {
	im := newTestImporter()
	vless := "vless://" + testUUID + "@Example.COM:443?type=xhttp&security=tls&sni=cdn.example.com&fp=chrome&flow=xtls-rprx-vision&alpn=h2,http/1.1&path=%2Fx&host=ws.example.com&mode=auto&extra=%7B%22scMaxEachPostBytes%22%3A1000%7D#Node%20A"
	recs, err := im.Import(context.Background(), testSource(contract.SourceManual, vless), []byte(vless))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("records=%d", len(recs))
	}
	r := recs[0]
	if r.Status != contract.RecordUnknown {
		t.Fatalf("status=%s reason=%s", r.Status, r.StatusReason)
	}
	if r.Server != "example.com" || r.Port != 443 || r.Transport != contract.TransportXHTTP || r.Security != contract.SecurityTLS {
		t.Fatalf("unexpected record: %+v", r)
	}
	if r.Credentials.UserID != testUUID {
		t.Fatal("uuid not preserved")
	}
	if r.DisplayName != "Node A" {
		t.Fatalf("display=%q", r.DisplayName)
	}
	if len(r.ALPN) != 2 || r.ALPN[0] != "h2" || r.ALPN[1] != "http/1.1" {
		t.Fatalf("alpn=%v", r.ALPN)
	}
	if r.XHTTPExtra["scMaxEachPostBytes"] == nil || r.XHTTPExtra["mode"] != "auto" {
		t.Fatalf("extra=%v", r.XHTTPExtra)
	}

	trojan := "trojan://secret@example.net:8443?security=tls&sni=example.net&type=ws&path=%2Ftr#T"
	recs, err = im.Import(context.Background(), testSource(contract.SourceManual, trojan), []byte(trojan))
	if err != nil {
		t.Fatal(err)
	}
	if recs[0].Protocol != contract.ProtocolTrojan || recs[0].Credentials.UserID != "secret" || recs[0].Transport != contract.TransportWS {
		t.Fatalf("trojan: %+v", recs[0])
	}

	socks := "socks5://user:p%40ss@127.0.0.1:1080#S"
	recs, err = im.Import(context.Background(), testSource(contract.SourceManual, socks), []byte(socks))
	if err != nil {
		t.Fatal(err)
	}
	if recs[0].Protocol != contract.ProtocolSOCKS5 || recs[0].Credentials.UserID != "user" || recs[0].Credentials.Password != "p@ss" || recs[0].Server != "127.0.0.1" {
		t.Fatalf("socks: %+v", recs[0])
	}
}

func TestDedupeKeyIdentity(t *testing.T) {
	im := newTestImporter()
	a := "vless://" + testUUID + "@example.com:443?security=tls&sni=example.com#Node%20A"
	b := "vless://" + testUUID + "@EXAMPLE.com:443?security=tls&sni=example.com#Different%20Name"
	c := "vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com#Node%20A"
	recs, err := im.Import(context.Background(), testSource(contract.SourceManual, "x"), []byte(a+"\n"+b+"\n"+c))
	if err != nil {
		t.Fatal(err)
	}
	if recs[0].DedupeKey != recs[1].DedupeKey || recs[0].ID != recs[1].ID {
		t.Fatalf("equivalent links differ: %s vs %s", recs[0].DedupeKey, recs[1].DedupeKey)
	}
	if recs[0].DedupeKey == recs[2].DedupeKey {
		t.Fatal("different uuid must produce a different key")
	}
	if len(recs[0].DedupeKey) != len("sha256:")+64 {
		t.Fatalf("bad key %q", recs[0].DedupeKey)
	}
}

func TestUnsupportedKindsRejectedWithReason(t *testing.T) {
	im := newTestImporter()
	payload := "vmess://eyJhZGQiOiJleGFtcGxlLmNvbSJ9\nhysteria2://x@example.com:443\nvless://" + testUUID + "@example.com:443?security=reality&pbk=abc&sid=def"
	recs, err := im.Import(context.Background(), testSource(contract.SourceFile, "f"), []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("records=%d", len(recs))
	}
	for i, r := range recs {
		if r.Status != contract.RecordUnsupported {
			t.Fatalf("record %d status=%s", i, r.Status)
		}
		if r.StatusReason == "" {
			t.Fatalf("record %d has no reason", i)
		}
	}
	if recs[2].StatusReason != ReasonRealityUnverified {
		t.Fatalf("reality reason=%s", recs[2].StatusReason)
	}
}

func TestInsecureTLSQuarantineAndPin(t *testing.T) {
	im := newTestImporter()
	insecure := "vless://" + testUUID + "@example.com:443?security=tls&allowInsecure=1#bad"
	recs, err := im.Import(context.Background(), testSource(contract.SourceManual, insecure), []byte(insecure))
	if err != nil {
		t.Fatal(err)
	}
	r := recs[0]
	if r.Status != contract.RecordUnsupported || r.StatusReason != contract.ReasonInsecureTLSRemoved || !r.InsecureTLS {
		t.Fatalf("insecure record: %+v", r)
	}

	pin := strings.Repeat("ab", 32)
	pinned := "vless://" + testUUID + "@example.com:443?security=tls&allowInsecure=1&pinSHA256=" + pin + "#pinned"
	recs, err = im.Import(context.Background(), testSource(contract.SourceManual, pinned), []byte(pinned))
	if err != nil {
		t.Fatal(err)
	}
	r = recs[0]
	if r.Status != contract.RecordUnknown || r.PinnedPeerCertSHA256 != pin || !r.InsecureTLS {
		t.Fatalf("pinned record: %+v", r)
	}
	other := strings.Replace(pinned, pin, strings.Repeat("cd", 32), 1)
	recs2, _ := im.Import(context.Background(), testSource(contract.SourceManual, other), []byte(other))
	if recs2[0].DedupeKey == r.DedupeKey {
		t.Fatal("pin must be part of the identity")
	}
	bad := "vless://" + testUUID + "@example.com:443?security=tls&pinSHA256=nothex"
	recs, _ = im.Import(context.Background(), testSource(contract.SourceManual, bad), []byte(bad))
	if recs[0].Status != contract.RecordInvalid || recs[0].StatusReason != ReasonInvalidPin {
		t.Fatalf("bad pin: %+v", recs[0])
	}
}

func TestXHTTPExtraAllowList(t *testing.T) {
	im := newTestImporter()
	valid := "vless://" + testUUID + "@example.com:443?type=xhttp&security=tls&extra=%7B%22host%22%3A%22h%22%2C%22xmux%22%3A%7B%22maxConcurrency%22%3A5%7D%7D"
	recs, _ := im.Import(context.Background(), testSource(contract.SourceManual, valid), []byte(valid))
	if recs[0].Status != contract.RecordUnknown || recs[0].XHTTPExtra["host"] != "h" {
		t.Fatalf("valid extra: %+v", recs[0])
	}
	for _, extra := range []string{
		`{"downloadSettings":{"security":"tls"}}`,
		`{"h2Params":"abc"}`,
		`{"unknown":1}`,
	} {
		link := "vless://" + testUUID + "@example.com:443?type=xhttp&security=tls&extra=" + urlQueryEscape(extra)
		recs, _ := im.Import(context.Background(), testSource(contract.SourceManual, link), []byte(link))
		if recs[0].Status != contract.RecordInvalid {
			t.Fatalf("extra %s status=%s reason=%s", extra, recs[0].Status, recs[0].StatusReason)
		}
	}
}

func urlQueryEscape(s string) string {
	return strings.NewReplacer(`{`, "%7B", `}`, "%7D", `"`, "%22", `:`, "%3A", `,`, "%2C").Replace(s)
}

func TestOversizedLinkScalarRejected(t *testing.T) {
	im := newTestImporter()
	link := "vless://" + testUUID + "@example.com:443?security=tls&sni=" + strings.Repeat("a", 9000)
	recs, err := im.Import(context.Background(), testSource(contract.SourceManual, link), []byte(link))
	if err != nil {
		t.Fatal(err)
	}
	if recs[0].Status != contract.RecordInvalid || recs[0].StatusReason != ReasonInvalidHostHeader {
		t.Fatalf("oversized sni: %+v", recs[0])
	}
}

func TestMissingCredentialDoesNotPanic(t *testing.T) {
	im := newTestImporter()
	for _, link := range []string{
		"vless://example.com:443?security=tls",
		"trojan://example.com:443?security=tls",
	} {
		recs, err := im.Import(context.Background(), testSource(contract.SourceManual, link), []byte(link))
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		if recs[0].Status != contract.RecordInvalid {
			t.Fatalf("%s status=%s", link, recs[0].Status)
		}
	}
}

func TestPartialInvalidPayload(t *testing.T) {
	im := newTestImporter()
	payload := "vless://" + testUUID + "@example.com:443?security=tls#ok\nnot a link\nvless://bad"
	recs, err := im.Import(context.Background(), testSource(contract.SourceFile, "f"), []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("records=%d", len(recs))
	}
	valid, invalid, _ := DescribeCounts(recs)
	if valid != 1 || invalid != 2 {
		t.Fatalf("valid=%d invalid=%d", valid, invalid)
	}
	for _, r := range recs {
		if r.DedupeKey == "" || r.ID == "" {
			t.Fatal("every record needs an identity")
		}
		if r.Status == contract.RecordInvalid && r.StatusReason == "" {
			t.Fatal("invalid record needs a reason")
		}
	}
}

func TestSubscriptionBase64(t *testing.T) {
	im := newTestImporter()
	link := "vless://" + testUUID + "@example.com:443?security=tls#b64"
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		payload := enc.EncodeToString([]byte(link))
		recs, err := im.Import(context.Background(), testSource(contract.SourceSubscription, "https://example.com/sub"), []byte(payload))
		if err != nil {
			t.Fatalf("%v: %v", enc, err)
		}
		if len(recs) != 1 || recs[0].Status != contract.RecordUnknown {
			t.Fatalf("%v: %+v", enc, recs)
		}
	}
}

func TestHTMLAndEmptyRejected(t *testing.T) {
	im := newTestImporter()
	for _, payload := range []string{"  <html>no</html>", "", "   \n"} {
		if _, err := im.Import(context.Background(), testSource(contract.SourceSubscription, "u"), []byte(payload)); err == nil {
			t.Fatalf("payload %q should fail", payload)
		}
	}
	if recs, err := im.Import(context.Background(), contract.Source{Kind: contract.SourceSubscription, AllowEmpty: true}, nil); err != nil || len(recs) != 0 {
		t.Fatalf("allow-empty: %v %v", recs, err)
	}
}

func TestOversizedPayloadRejected(t *testing.T) {
	im := newTestImporter()
	payload := make([]byte, contract.MaxSourceBytes+1)
	for i := range payload {
		payload[i] = 'a'
	}
	if _, err := im.Import(context.Background(), testSource(contract.SourceFile, "f"), payload); err == nil {
		t.Fatal("oversized payload must fail")
	}
}

func TestV2rayNJSON(t *testing.T) {
	im := newTestImporter()
	jsonPayload := `[
	  {"configType":5,"address":"one.example.com","port":443,"id":"` + testUUID + `","network":"ws","streamSecurity":"tls","sni":"one.example.com","path":"/ws","remarks":"One"},
	  {"configType":6,"address":"two.example.com","port":8443,"id":"trojanpass","streamSecurity":"tls","remarks":"Two"},
	  {"configType":4,"address":"127.0.0.1","port":1080,"id":"u","password":"p","remarks":"Socks"},
	  {"configType":1,"address":"vmess.example.com","port":443,"id":"` + testUUID + `","remarks":"VMess"}
	]`
	recs, err := im.Import(context.Background(), testSource(contract.SourceV2rayN, "v2.json"), []byte(jsonPayload))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 {
		t.Fatalf("records=%d", len(recs))
	}
	if recs[0].Protocol != contract.ProtocolVLESS || recs[0].Transport != contract.TransportWS || recs[0].DisplayName != "One" {
		t.Fatalf("vless profile: %+v", recs[0])
	}
	if recs[1].Protocol != contract.ProtocolTrojan || recs[1].Credentials.UserID != "trojanpass" {
		t.Fatalf("trojan profile: %+v", recs[1])
	}
	if recs[2].Protocol != contract.ProtocolSOCKS5 || recs[2].Credentials.UserID != "u" || recs[2].Credentials.Password != "p" {
		t.Fatalf("socks profile: %+v", recs[2])
	}
	if recs[3].Status != contract.RecordUnsupported || recs[3].StatusReason != ReasonUnsupportedProtocol {
		t.Fatalf("vmess: %+v", recs[3])
	}
}

func deepItem() string {
	prefix := `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","x":`
	return prefix + strings.Repeat(`{"a":`, 9) + `1` + strings.Repeat(`}`, 9) + `}`
}

func TestV2rayNMaliciousFixtures(t *testing.T) {
	im := newTestImporter()
	pin := strings.Repeat("ab", 32)
	cases := []struct {
		name   string
		item   string
		status contract.RecordStatus
		reason string
	}{
		{"script", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","script":"alert(1)"}`, contract.RecordInvalid, ReasonScriptField},
		{"root routing", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","routing":{"rules":[]}}`, contract.RecordInvalid, ReasonRootScopeKey},
		{"root dns", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","dns":{"servers":[]}}`, contract.RecordInvalid, ReasonRootScopeKey},
		{"root api", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","api":{"tag":"x"}}`, contract.RecordInvalid, ReasonRootScopeKey},
		{"root env", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","env":{"x":"y"}}`, contract.RecordInvalid, ReasonRootScopeKey},
		{"root inbounds", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","inbounds":[]}`, contract.RecordInvalid, ReasonRootScopeKey},
		{"root log", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","log":{"access":"/tmp/x"}}`, contract.RecordInvalid, ReasonRootScopeKey},
		{"local file", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","streamSettings":{"tlsSettings":{"certificates":[{"certificateFile":"/etc/passwd"}]}}}`, contract.RecordInvalid, ReasonLocalFilePath},
		{"outbound dependency", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","proxySettings":{"tag":"other"}}`, contract.RecordInvalid, ReasonOutboundDependency},
		{"dialer proxy", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","streamSettings":{"sockopt":{"dialerProxy":"other"}}}`, contract.RecordInvalid, ReasonOutboundDependency},
		{"unknown extra", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","network":"xhttp","streamSettings":{"security":"tls","xhttpSettings":{"downloadSettings":{"security":"tls"}}}}`, contract.RecordInvalid, ReasonUnknownExtraKey},
		{"oversized value", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","remarks":"` + strings.Repeat("x", maxJSONStringBytes+1) + `"}`, contract.RecordInvalid, ReasonValueTooLong},
		{"deep", deepItem(), contract.RecordInvalid, ReasonRecordTooDeep},
		{"insecure", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","streamSecurity":"tls","allowInsecure":"true"}`, contract.RecordUnsupported, contract.ReasonInsecureTLSRemoved},
		{"reality", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","streamSecurity":"reality","publicKey":"x","shortId":"y"}`, contract.RecordUnsupported, ReasonRealityUnverified},
		{"mux dropped", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","mux":{"enabled":true}}`, contract.RecordUnknown, ""},
		{"pinned insecure accepted", `{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","streamSecurity":"tls","allowInsecure":"true","pinnedPeerCertSha256":"` + pin + `"}`, contract.RecordUnknown, ""},
	}
	for _, tc := range cases {
		recs, err := im.Import(context.Background(), testSource(contract.SourceV2rayN, "v.json"), []byte("["+tc.item+"]"))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(recs) != 1 || recs[0].Status != tc.status || recs[0].StatusReason != tc.reason {
			t.Fatalf("%s: status=%s reason=%s want %s/%s", tc.name, recs[0].Status, recs[0].StatusReason, tc.status, tc.reason)
		}
	}

	// A whole application config is refused rather than read as a node set.
	appConfig := `{"routing":{"rules":[]},"inbounds":[],"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"a.example.com","port":443,"users":[{"id":"` + testUUID + `"}]}]}}]}`
	if _, err := im.Import(context.Background(), testSource(contract.SourceV2rayN, "v.json"), []byte(appConfig)); err == nil {
		t.Fatal("application configuration must be refused")
	}
}

// fakeStore is an in-memory contract.Store for refresh tests. It deep-copies
// through JSON so a failed callback never leaves partial state.
type fakeStore struct {
	mu        sync.Mutex
	snapshot  contract.Snapshot
	loadErr   error
	mutateErr error
}

func newFakeStore(state contract.State) *fakeStore {
	state.SchemaVersion = contract.SchemaVersionCurrent
	return &fakeStore{snapshot: contract.Snapshot{State: state, Config: contract.Config{SchemaVersion: contract.SchemaVersionCurrent}}}
}

func (f *fakeStore) Load(context.Context) (contract.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return contract.Snapshot{}, f.loadErr
	}
	return cloneSnapshot(f.snapshot), nil
}

func (f *fakeStore) Mutate(_ context.Context, fn func(*contract.State) error) (contract.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mutateErr != nil {
		return contract.Snapshot{}, f.mutateErr
	}
	next := cloneSnapshot(f.snapshot)
	if err := fn(&next.State); err != nil {
		return contract.Snapshot{}, err
	}
	f.snapshot = next
	return cloneSnapshot(f.snapshot), nil
}

func (f *fakeStore) MutateConfig(_ context.Context, fn func(*contract.Config) error) (contract.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	next := cloneSnapshot(f.snapshot)
	if err := fn(&next.Config); err != nil {
		return contract.Snapshot{}, err
	}
	f.snapshot = next
	return cloneSnapshot(f.snapshot), nil
}

func (f *fakeStore) Backup(context.Context) (contract.BackupRef, error) {
	return contract.BackupRef{}, errors.New("not implemented")
}

func (f *fakeStore) Restore(context.Context, contract.BackupRef) (contract.Snapshot, error) {
	return contract.Snapshot{}, errors.New("not implemented")
}

func cloneSnapshot(s contract.Snapshot) contract.Snapshot {
	out := s
	out.State.Records = append([]contract.Record(nil), s.State.Records...)
	out.State.Health = append([]contract.ExitHealth(nil), s.State.Health...)
	out.State.Sources = append([]contract.Source(nil), s.State.Sources...)
	for i := range out.State.Records {
		out.State.Records[i].SourceIDs = append([]contract.SourceID(nil), s.State.Records[i].SourceIDs...)
		if s.State.Records[i].XHTTPExtra != nil {
			out.State.Records[i].XHTTPExtra = map[string]any{}
			for k, v := range s.State.Records[i].XHTTPExtra {
				out.State.Records[i].XHTTPExtra[k] = v
			}
		}
	}
	return out
}

type staticFetcher struct {
	body []byte
	err  error
}

func (f staticFetcher) FetchSource(context.Context, contract.Source) ([]byte, error) {
	return f.body, f.err
}

func TestRefreshDuplicateAcrossSources(t *testing.T) {
	link := "vless://" + testUUID + "@example.com:443?security=tls#n"
	srcA := contract.Source{ID: SourceIDFor(contract.SourceSubscription, "https://a.example/sub"), Kind: contract.SourceSubscription, Origin: "https://a.example/sub", Order: 0}
	srcB := contract.Source{ID: SourceIDFor(contract.SourceSubscription, "https://b.example/sub"), Kind: contract.SourceSubscription, Origin: "https://b.example/sub", Order: 1}
	store := newFakeStore(contract.State{Mode: contract.ModeTargeted, Sources: []contract.Source{srcA, srcB}})
	r := NewRefresher(store, staticFetcher{body: []byte(link)})
	if _, err := r.Refresh(context.Background(), []contract.SourceID{srcA.ID, srcB.ID}, false); err != nil {
		t.Fatal(err)
	}
	snap, _ := store.Load(context.Background())
	if len(snap.State.Records) != 1 {
		t.Fatalf("records=%d", len(snap.State.Records))
	}
	if len(snap.State.Records[0].SourceIDs) != 2 {
		t.Fatalf("sources=%v", snap.State.Records[0].SourceIDs)
	}
}

func TestRefreshFailurePreservesLastGood(t *testing.T) {
	link := "vless://" + testUUID + "@example.com:443?security=tls#n"
	src := contract.Source{ID: SourceIDFor(contract.SourceSubscription, "https://a.example/sub"), Kind: contract.SourceSubscription, Origin: "https://a.example/sub", Order: 0}
	store := newFakeStore(contract.State{Mode: contract.ModeTargeted, Sources: []contract.Source{src}})
	r := NewRefresher(store, staticFetcher{body: []byte(link)})
	if _, err := r.Refresh(context.Background(), []contract.SourceID{src.ID}, false); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Load(context.Background())

	r.Fetcher = staticFetcher{err: sourceFailure("subscription returned HTTP 503", nil)}
	res, err := r.Refresh(context.Background(), []contract.SourceID{src.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.KeptLastGood || len(res.Sources) != 1 || res.Sources[0].OK {
		t.Fatalf("result=%+v", res)
	}
	after, _ := store.Load(context.Background())
	if len(after.State.Records) != len(before.State.Records) || after.State.Records[0].ID != before.State.Records[0].ID {
		t.Fatal("failed refresh destroyed the previous pool")
	}
	if after.State.Sources[0].LastError == "" {
		t.Fatal("last error not recorded")
	}
	if strings.Contains(after.State.Sources[0].LastError, "https://") {
		t.Fatalf("source URL leaked into error: %q", after.State.Sources[0].LastError)
	}
}

func TestRefreshReplacesOnlyItsSource(t *testing.T) {
	link1 := "vless://" + testUUID + "@one.example.com:443?security=tls#1"
	link2 := "vless://11111111-2222-3333-4444-555555555555@two.example.com:443?security=tls#2"
	srcA := contract.Source{ID: SourceIDFor(contract.SourceSubscription, "https://a.example/sub"), Kind: contract.SourceSubscription, Origin: "https://a.example/sub", Order: 0}
	srcB := contract.Source{ID: SourceIDFor(contract.SourceSubscription, "https://b.example/sub"), Kind: contract.SourceSubscription, Origin: "https://b.example/sub", Order: 1}
	store := newFakeStore(contract.State{Mode: contract.ModeTargeted, Sources: []contract.Source{srcA, srcB}})
	r := NewRefresher(store, staticFetcher{body: []byte(link1)})
	if _, err := r.Refresh(context.Background(), []contract.SourceID{srcA.ID}, false); err != nil {
		t.Fatal(err)
	}
	r.Fetcher = staticFetcher{body: []byte(link2)}
	if _, err := r.Refresh(context.Background(), []contract.SourceID{srcB.ID}, false); err != nil {
		t.Fatal(err)
	}
	snap, _ := store.Load(context.Background())
	if len(snap.State.Records) != 2 {
		t.Fatalf("records=%d", len(snap.State.Records))
	}
	byID := map[contract.RecordID]contract.Record{}
	for _, rec := range snap.State.Records {
		byID[rec.ID] = rec
	}
	for _, rec := range snap.State.Records {
		if len(rec.SourceIDs) != 1 {
			t.Fatalf("record %s sources=%v", rec.ID, rec.SourceIDs)
		}
	}
	// Refreshing A with an empty allowed payload must not remove B.
	r.Fetcher = staticFetcher{body: nil}
	if _, err := r.Refresh(context.Background(), []contract.SourceID{srcA.ID}, true); err != nil {
		t.Fatal(err)
	}
	snap, _ = store.Load(context.Background())
	if len(snap.State.Records) != 1 || snap.State.Records[0].ID == "" {
		t.Fatalf("records after empty refresh=%+v", snap.State.Records)
	}
	if snap.State.Records[0].SourceIDs[0] != srcB.ID {
		t.Fatalf("B record lost: %v", snap.State.Records[0].SourceIDs)
	}
}

func TestRefreshZeroValidWithoutAllowEmptyIsError(t *testing.T) {
	src := contract.Source{ID: SourceIDFor(contract.SourceSubscription, "https://a.example/sub"), Kind: contract.SourceSubscription, Origin: "https://a.example/sub"}
	store := newFakeStore(contract.State{Mode: contract.ModeTargeted, Sources: []contract.Source{src}})
	r := NewRefresher(store, staticFetcher{body: []byte("vless://bad")})
	res, err := r.Refresh(context.Background(), []contract.SourceID{src.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.KeptLastGood || res.Sources[0].OK {
		t.Fatalf("result=%+v", res)
	}
	snap, _ := store.Load(context.Background())
	if len(snap.State.Records) != 0 {
		t.Fatal("invalid-only payload must not replace the pool")
	}
}

func TestSourceIDShapeAndSecrecy(t *testing.T) {
	origin := "https://user:token@example.com/sub?token=secret"
	id := SourceIDFor(contract.SourceSubscription, origin)
	if len(id) != 14 || !strings.HasPrefix(string(id), "s-") {
		t.Fatalf("source id=%q", id)
	}
	if strings.Contains(string(id), "token") || strings.Contains(string(id), "example.com") {
		t.Fatal("source id leaks the origin")
	}
	if id != SourceIDFor(contract.SourceSubscription, "https://user:token@EXAMPLE.com:443/sub?token=secret") {
		t.Fatal("canonical origin must fold host case and default port")
	}
}

func TestFetchLimits(t *testing.T) {
	ctx := context.Background()
	src := contract.Source{Kind: contract.SourceSubscription}

	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>blocked</html>")
	}))
	defer html.Close()
	src.Origin = html.URL
	if _, err := NewFetcher().FetchSource(ctx, src); err == nil {
		t.Fatal("HTML must fail")
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer empty.Close()
	src.Origin = empty.URL
	if _, err := NewFetcher().FetchSource(ctx, src); err == nil {
		t.Fatal("empty must fail")
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	src.Origin = bad.URL
	if _, err := NewFetcher().FetchSource(ctx, src); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("non-2xx: %v", err)
	}

	// Three redirects succeed; a fourth is refused.
	var chain = func(n int) http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			step := strings.TrimPrefix(r.URL.Path, "/")
			if step == "" {
				http.Redirect(w, r, "/1", http.StatusFound)
				return
			}
			var i int
			fmt.Sscanf(step, "%d", &i)
			if i < n {
				http.Redirect(w, r, fmt.Sprintf("/%d", i+1), http.StatusFound)
				return
			}
			fmt.Fprint(w, "vless://ok")
		})
		return mux
	}
	three := httptest.NewServer(chain(3))
	defer three.Close()
	src.Origin = three.URL
	if _, err := NewFetcher().FetchSource(ctx, src); err != nil {
		t.Fatalf("three redirects should succeed: %v", err)
	}
	four := httptest.NewServer(chain(4))
	defer four.Close()
	src.Origin = four.URL
	if _, err := NewFetcher().FetchSource(ctx, src); err == nil {
		t.Fatal("four redirects must fail")
	}

	// Proxy environment variables must not be used.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "vless://ok") }))
	defer plain.Close()
	src.Origin = plain.URL
	if _, err := NewFetcher().FetchSource(ctx, src); err != nil {
		t.Fatalf("environment proxy was used: %v", err)
	}
}

func TestOversizedFetchRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("a", 64<<10)
		for i := 0; i < 65; i++ { // just over 4 MiB
			fmt.Fprint(w, chunk)
		}
	}))
	defer server.Close()
	src := contract.Source{Kind: contract.SourceSubscription, Origin: server.URL}
	if _, err := NewFetcher().FetchSource(context.Background(), src); err == nil {
		t.Fatal("oversized body must fail")
	}
}

func TestRedirectDropsCredentialsAndRejectsScheme(t *testing.T) {
	var mu sync.Mutex
	var authAtTarget, cookieAtTarget string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authAtTarget = r.Header.Get("Authorization")
		cookieAtTarget = r.Header.Get("Cookie")
		mu.Unlock()
		fmt.Fprint(w, "vless://ok")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sub", Value: "secret", Path: "/"})
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	// Put Basic credentials in the source URL; they must not reach the target.
	srcURL := strings.Replace(source.URL, "http://", "http://user:pass@", 1)
	src := contract.Source{Kind: contract.SourceSubscription, Origin: srcURL}
	if _, err := NewFetcher().FetchSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if authAtTarget != "" {
		t.Fatalf("Authorization leaked across redirect: %q", authAtTarget)
	}
	if cookieAtTarget != "" {
		t.Fatalf("Cookie leaked across redirect: %q", cookieAtTarget)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "ftp://example.com/file")
		w.WriteHeader(http.StatusFound)
	}))
	defer bad.Close()
	src.Origin = bad.URL
	if _, err := NewFetcher().FetchSource(context.Background(), src); err == nil {
		t.Fatal("redirect to ftp must fail")
	}
}

func TestRefreshMutateFailureLeavesLastGood(t *testing.T) {
	link := "vless://" + testUUID + "@example.com:443?security=tls#n"
	src := contract.Source{ID: SourceIDFor(contract.SourceSubscription, "https://a.example/sub"), Kind: contract.SourceSubscription, Origin: "https://a.example/sub"}
	store := newFakeStore(contract.State{Mode: contract.ModeTargeted, Sources: []contract.Source{src}})
	r := NewRefresher(store, staticFetcher{body: []byte(link)})
	if _, err := r.Refresh(context.Background(), []contract.SourceID{src.ID}, false); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Load(context.Background())
	store.mutateErr = errors.New("disk full")
	if _, err := r.Refresh(context.Background(), []contract.SourceID{src.ID}, false); err == nil {
		t.Fatal("mutate failure must be returned")
	}
	after, _ := store.Load(context.Background())
	if len(after.State.Records) != len(before.State.Records) || after.State.Records[0].ID != before.State.Records[0].ID {
		t.Fatal("failed mutation changed the pool")
	}
}

func TestV2rayNHTTPIsUnsupported(t *testing.T) {
	im := newTestImporter()
	for _, payload := range []string{
		`[{"configType":10,"address":"http.example.com","port":8080,"id":"u","password":"p"}]`,
		`[{"protocol":"http","address":"http.example.com","port":8080,"id":"u","password":"p"}]`,
	} {
		recs, err := im.Import(context.Background(), testSource(contract.SourceV2rayN, "v.json"), []byte(payload))
		if err != nil {
			t.Fatal(err)
		}
		if recs[0].Protocol != contract.ProtocolHTTP || recs[0].Status != contract.RecordUnsupported || recs[0].StatusReason != ReasonHTTPExitUnverified {
			t.Fatalf("http record: %+v", recs[0])
		}
	}
}

func TestAuditCountsDroppedMuxAndQuery(t *testing.T) {
	im := newTestImporter()
	link := "vless://" + testUUID + "@example.com:443?security=tls&unknownParam=1"
	_, audit, err := im.ImportWithAudit(context.Background(), testSource(contract.SourceManual, link), []byte(link))
	if err != nil {
		t.Fatal(err)
	}
	if audit.DroppedKeys != 1 {
		t.Fatalf("dropped=%d", audit.DroppedKeys)
	}
	payload := `[{"configType":5,"address":"a.example.com","port":443,"id":"` + testUUID + `","mux":{"enabled":true}}]`
	records, audit, err := im.ImportWithAudit(context.Background(), testSource(contract.SourceV2rayN, "v.json"), []byte(payload))
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%v err=%v", records, err)
	}
	if audit.DroppedKeys != 1 {
		t.Fatalf("mux dropped=%d", audit.DroppedKeys)
	}
}
