package proposals

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"routelatch/internal/contract"
)

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

func domain(host string, port uint16) contract.Destination {
	return contract.Destination{Kind: contract.DestDomain, Host: host, Port: port}
}

func refused(addr string, at time.Time) contract.Evidence {
	return contract.Evidence{
		Kind: contract.EvidenceDirectRefused, At: at, Address: addr,
		ProbeKind: contract.ProbeDirectTCP, Outcome: contract.OutcomeRefused,
	}
}

func newTestRegistry() (*Registry, *testClock) {
	c := &testClock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	return New(c.now), c
}

func TestStableIDIgnoresPortAndUpdatesOneProposal(t *testing.T) {
	r, c := newTestRegistry()
	p1, created, err := r.Upsert(domain("example.com", 80), refused("example.com:80", c.t), "")
	if err != nil || !created {
		t.Fatalf("first upsert: created=%v err=%v", created, err)
	}
	p2, created, err := r.Upsert(domain("example.com", 443), refused("example.com:443", c.t.Add(time.Minute)), "")
	if err != nil || created {
		t.Fatalf("second upsert: created=%v err=%v", created, err)
	}
	if p1.ID != p2.ID {
		t.Fatalf("port changed the stable ID: %s vs %s", p1.ID, p2.ID)
	}
	if p2.Destination.Port != 80 {
		t.Fatalf("proposal destination moved; want first choice, got %d", p2.Destination.Port)
	}
	if len(p2.Evidence) != 2 {
		t.Fatalf("evidence count = %d, want 2", len(p2.Evidence))
	}
	if got := len(r.List()); got != 1 {
		t.Fatalf("proposal count = %d, want 1", got)
	}
}

func TestAcceptReturnsExactDestinationAndCreatesNoRule(t *testing.T) {
	r, c := newTestRegistry()
	dst := domain("api.example.com", 8443)
	p, _, err := r.Upsert(dst, refused("api.example.com:8443", c.t), "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Accept(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != dst {
		t.Fatalf("accept returned %#v, want %#v", got, dst)
	}
	stored, _ := r.Get(p.ID)
	if stored.Status != contract.ProposalAccepted || stored.DecidedAt.IsZero() {
		t.Fatalf("status=%s decided_at=%v", stored.Status, stored.DecidedAt)
	}
	if _, err := r.Accept(p.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second accept error = %v, want ErrNotPending", err)
	}
	if _, err := r.Accept("p-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing accept error = %v, want ErrNotFound", err)
	}
}

func TestRejectThenRecheckKeepsRejected(t *testing.T) {
	r, c := newTestRegistry()
	p, _, _ := r.Upsert(domain("example.com", 443), refused("example.com:443", c.t), "")
	if err := r.Reject(p.ID, "not now"); err != nil {
		t.Fatal(err)
	}
	updated, added, err := r.Recheck(p.ID, refused("example.com:443", c.t.Add(2*time.Minute)), "")
	if err != nil {
		t.Fatal(err)
	}
	if !added || len(updated.Evidence) != 2 {
		t.Fatalf("recheck did not add evidence: added=%v count=%d", added, len(updated.Evidence))
	}
	if updated.Status != contract.ProposalRejected {
		t.Fatalf("recheck revived a rejected proposal: %s", updated.Status)
	}
	if _, err := r.Accept(p.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("accept after reject = %v, want ErrNotPending", err)
	}
}

func TestExpiryAndExplicitClear(t *testing.T) {
	r, c := newTestRegistry()
	p, _, _ := r.Upsert(domain("old.example", 443), refused("old.example:443", c.t), "")
	fresh, _, _ := r.Upsert(domain("new.example", 443), refused("new.example:443", c.t), "")

	c.t = c.t.Add(contract.ProposalRetention + time.Second)
	if n := r.Expire(); n != 2 {
		t.Fatalf("expired count = %d, want 2", n)
	}
	if got := len(r.Pending()); got != 0 {
		t.Fatalf("pending after expiry = %d, want 0", got)
	}
	if _, err := r.Accept(p.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("accept expired = %v, want ErrExpired", err)
	}
	if _, _, err := r.Recheck(p.ID, refused("old.example:443", c.t), ""); !errors.Is(err, ErrExpired) {
		t.Fatalf("recheck expired = %v, want ErrExpired", err)
	}

	// A pending proposal inside its window is never cleared implicitly.
	fresh2, _, _ := r.Upsert(domain("pending.example", 443), refused("pending.example:443", c.t), "")
	if n := r.Clear(); n != 2 {
		t.Fatalf("cleared = %d, want the two expired proposals", n)
	}
	if _, ok := r.Get(fresh2.ID); !ok {
		t.Fatal("clear removed a live pending proposal")
	}
	if _, ok := r.Get(fresh.ID); ok {
		t.Fatal("expired proposal was not cleared")
	}
}

func TestClearKeepsRetainedDecidedProposal(t *testing.T) {
	r, c := newTestRegistry()
	p, _, _ := r.Upsert(domain("example.com", 443), refused("example.com:443", c.t), "")
	if err := r.Reject(p.ID, ""); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(29 * 24 * time.Hour)
	if n := r.Clear(); n != 0 {
		t.Fatalf("cleared %d proposals inside the retention window", n)
	}
	c.t = c.t.Add(48 * time.Hour)
	if n := r.Clear(); n != 1 {
		t.Fatalf("cleared %d after retention, want 1", n)
	}
}

func TestEvidenceValidationRejectsRequestMaterial(t *testing.T) {
	r, c := newTestRegistry()
	bad := refused("https://user:pass@example.com/p?token=secret", c.t)
	if _, _, err := r.Upsert(domain("example.com", 443), bad, ""); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("URL-shaped address error = %v", err)
	}
	mismatched := refused("example.com:443", c.t)
	mismatched.Outcome = contract.OutcomeTimeout
	if _, _, err := r.Upsert(domain("example.com", 443), mismatched, ""); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("mismatched outcome error = %v", err)
	}
}

func TestSnapshotLoadRoundTrip(t *testing.T) {
	r, c := newTestRegistry()
	p, _, _ := r.Upsert(domain("example.com", 443), refused("example.com:443", c.t), "note")
	snap := r.Snapshot()

	other, _ := newTestRegistry()
	if err := other.Load(snap); err != nil {
		t.Fatal(err)
	}
	got, ok := other.Get(p.ID)
	if !ok || got.Status != contract.ProposalPending || len(got.Evidence) != 1 {
		t.Fatalf("round trip lost proposal: %#v ok=%v", got, ok)
	}

	broken := append([]contract.Proposal(nil), snap...)
	broken[0].ID = "p-wrong"
	if err := other.Load(broken); !errors.Is(err, ErrInvalidProposal) {
		t.Fatalf("bad id load error = %v", err)
	}
	if _, ok := other.Get(p.ID); !ok {
		t.Fatal("failed load changed the registry")
	}
}

func TestSerializedProposalHasNoRequestMaterial(t *testing.T) {
	r, c := newTestRegistry()
	dst := domain("example.com", 443)
	if _, _, err := r.Upsert(dst, refused("example.com:443", c.t), "direct TCP probe reproduced direct_refused"); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r.List())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"http", "token", "Cookie", "secret", "?q=", "/path"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("serialized proposal contains %q: %s", forbidden, raw)
		}
	}
}

func TestUnrouteSuggestionRequiresConfirmation(t *testing.T) {
	r, _ := newTestRegistry()
	dst := domain("example.com", 443)
	s, created, err := r.SuggestUnroute("rule-1", dst, "repeated pool failures")
	if err != nil || !created {
		t.Fatalf("suggest = %v created=%v", err, created)
	}
	if got := len(r.PendingUnroutes()); got != 1 {
		t.Fatalf("pending unroutes = %d", got)
	}
	ruleID, err := r.AcceptUnroute(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ruleID != "rule-1" {
		t.Fatalf("accept returned rule %q", ruleID)
	}
	if _, err := r.AcceptUnroute(s.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second accept = %v, want ErrNotPending", err)
	}
	// The registry has no rule store: accepting only names the rule for the
	// caller's confirmed removal.
	if got := len(r.ListUnroutes()); got != 1 {
		t.Fatalf("unroute list = %d", got)
	}
}

func TestUnrouteRejectExpiryAndClear(t *testing.T) {
	r, c := newTestRegistry()
	dst := domain("example.com", 443)
	a, _, _ := r.SuggestUnroute("rule-a", dst, "")
	b, _, _ := r.SuggestUnroute("rule-b", dst, "")
	if err := r.RejectUnroute(a.ID, "keep it"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.SuggestUnroute("rule-a", dst, ""); err != nil {
		t.Fatal(err)
	}
	if got := r.ListUnroutes(); got[0].ID != b.ID {
		t.Fatalf("rejected suggestion was revived or reordered: %#v", got)
	}

	c.t = c.t.Add(contract.ProposalRetention + time.Second)
	if n := r.Expire(); n != 1 {
		t.Fatalf("expired = %d, want the one pending suggestion", n)
	}
	if n := r.Clear(); n != 2 {
		t.Fatalf("cleared = %d, want 2", n)
	}
	if got := len(r.ListUnroutes()); got != 0 {
		t.Fatalf("unroutes after clear = %d", got)
	}
}

func TestExpiredProposalStartsFreshButRejectedStays(t *testing.T) {
	r, c := newTestRegistry()
	p, _, _ := r.Upsert(domain("example.com", 443), refused("example.com:443", c.t), "")
	c.t = c.t.Add(contract.ProposalRetention + time.Second)
	if n := r.Expire(); n != 1 {
		t.Fatalf("expired = %d", n)
	}
	fresh, created, err := r.Upsert(domain("example.com", 443), refused("example.com:443", c.t), "")
	if err != nil || !created {
		t.Fatalf("refresh after expiry: created=%v err=%v", created, err)
	}
	if fresh.ID != p.ID || fresh.Status != contract.ProposalPending {
		t.Fatalf("expired proposal refresh = %#v", fresh)
	}
	if !fresh.ExpiresAt.After(c.t) || len(fresh.Evidence) != 1 {
		t.Fatalf("fresh window/evidence = %#v", fresh)
	}

	r2, c2 := newTestRegistry()
	rej, _, _ := r2.Upsert(domain("example.com", 443), refused("example.com:443", c2.t), "")
	if err := r2.Reject(rej.ID, ""); err != nil {
		t.Fatal(err)
	}
	c2.t = c2.t.Add(contract.ProposalRetention + time.Second)
	again, created, err := r2.Upsert(domain("example.com", 443), refused("example.com:443", c2.t), "")
	if err != nil || created || again.Status != contract.ProposalRejected {
		t.Fatalf("rejected proposal revived: %#v created=%v err=%v", again, created, err)
	}
}
