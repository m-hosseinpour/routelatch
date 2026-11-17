package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"routelatch/internal/contract"
)

type fakeProber struct {
	mu          sync.Mutex
	calls       []contract.ProbeRequest
	fn          func(context.Context, contract.ProbeRequest) (contract.ProbeResult, error)
	cancelCalls int
	started     chan contract.ProbeRequest
	finished    chan contract.ProbeRequest
}

func newFakeProber() *fakeProber {
	return &fakeProber{
		started:  make(chan contract.ProbeRequest, 64),
		finished: make(chan contract.ProbeRequest, 64),
	}
}

func (f *fakeProber) Enqueue(context.Context, contract.ProbeRequest) error { return nil }

func (f *fakeProber) Run(ctx context.Context, req contract.ProbeRequest) (contract.ProbeResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	fn := f.fn
	f.mu.Unlock()
	f.started <- req
	var (
		res contract.ProbeResult
		err error
	)
	if fn != nil {
		res, err = fn(ctx, req)
	} else {
		res = contract.ProbeResult{
			Kind: req.Kind, Trigger: req.Trigger, Destination: req.Destination,
			StartedAt: time.Now(), Outcome: contract.OutcomeSuccess,
		}
	}
	f.finished <- req
	return res, err
}

func (f *fakeProber) CancelQueued(context.Context) (int, error) {
	f.mu.Lock()
	f.cancelCalls++
	f.mu.Unlock()
	return 0, nil
}

func (f *fakeProber) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeProber) requests() []contract.ProbeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contract.ProbeRequest(nil), f.calls...)
}

func (f *fakeProber) cancels() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelCalls
}

type fakeRouter struct {
	dec contract.Decision
	err error
}

func (r *fakeRouter) Decide(context.Context, contract.Destination) (contract.Decision, error) {
	return r.dec, r.err
}

func (r *fakeRouter) PolicyVersion() contract.PolicyVersion { return r.dec.PolicyVersion }

type fakeStore struct {
	mu       sync.Mutex
	byID     map[contract.ProposalID]contract.Proposal
	upserts  int
	upserted chan contract.Evidence
}

func newFakeStore() *fakeStore {
	return &fakeStore{byID: make(map[contract.ProposalID]contract.Proposal), upserted: make(chan contract.Evidence, 16)}
}

func fakeID(dst contract.Destination) contract.ProposalID {
	if dst.Kind == contract.DestDomain {
		return contract.ProposalID("p-" + dst.Host)
	}
	return contract.ProposalID("p-" + dst.Addr.String())
}

func (s *fakeStore) Upsert(dst contract.Destination, ev contract.Evidence, note string) (contract.Proposal, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upserts++
	id := fakeID(dst)
	p, ok := s.byID[id]
	if !ok {
		p = contract.Proposal{
			ID: id, Destination: dst, Status: contract.ProposalPending,
			CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(contract.ProposalRetention),
		}
	}
	p.Evidence = append(p.Evidence, ev)
	if note != "" {
		p.Note = note
	}
	s.byID[id] = p
	select {
	case s.upserted <- ev:
	default:
	}
	return p, !ok, nil
}

func (s *fakeStore) Pending() []contract.Proposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []contract.Proposal
	for _, p := range s.byID {
		if p.Status == contract.ProposalPending {
			out = append(out, p)
		}
	}
	return out
}

func (s *fakeStore) Get(id contract.ProposalID) (contract.Proposal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	return p, ok
}

func (s *fakeStore) Accept(id contract.ProposalID) (contract.Destination, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	if !ok {
		return contract.Destination{}, errors.New("proposal not found")
	}
	if p.Status != contract.ProposalPending {
		return contract.Destination{}, errors.New("proposal was already decided")
	}
	p.Status = contract.ProposalAccepted
	s.byID[id] = p
	return p.Destination, nil
}

func (s *fakeStore) Reject(id contract.ProposalID, note string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	if !ok {
		return errors.New("proposal not found")
	}
	p.Status = contract.ProposalRejected
	if note != "" {
		p.Note = note
	}
	s.byID[id] = p
	return nil
}

func (s *fakeStore) Recheck(id contract.ProposalID, ev contract.Evidence, note string) (contract.Proposal, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	if !ok {
		return contract.Proposal{}, false, errors.New("proposal not found")
	}
	p.Evidence = append(p.Evidence, ev)
	if note != "" {
		p.Note = note
	}
	s.byID[id] = p
	return p, true, nil
}

func (s *fakeStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upserts
}

func (s *fakeStore) all() []contract.Proposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]contract.Proposal, 0, len(s.byID))
	for _, p := range s.byID {
		out = append(out, p)
	}
	return out
}

type mutClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *mutClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *mutClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func defaultConfig() *contract.DiagnosticsConfig {
	return &contract.DiagnosticsConfig{Enabled: true, QueueLimit: 32, MaxConcurrent: 1, RetentionDays: 30}
}

func ptrMode(m contract.Mode) *contract.Mode { return &m }

func hostPort(t *testing.T, raw string) contract.Destination {
	t.Helper()
	host, portRaw, err := net.SplitHostPort(raw)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portRaw, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return contract.Destination{Kind: contract.DestIP, Addr: ip.Unmap(), Port: uint16(port)}
	}
	return contract.Destination{Kind: contract.DestDomain, Host: strings.ToLower(host), Port: uint16(port)}
}

func failure(kind contract.ErrorKind, dst contract.Destination) *contract.Failure {
	return &contract.Failure{
		Kind: kind, Path: contract.PathDirect, Op: "proxy.connect", Destination: dst,
		Message: "redacted failure",
	}
}

type harness struct {
	m      *Manager
	prober *fakeProber
	store  *fakeStore
	router *fakeRouter
	cfg    *contract.DiagnosticsConfig
	mode   *contract.Mode
	clock  *mutClock
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		prober: newFakeProber(),
		store:  newFakeStore(),
		router: &fakeRouter{dec: contract.Decision{
			Mode: contract.ModeTargeted, Class: contract.ClassOther,
			Route: contract.RouteDirect, PolicyVersion: 1,
		}},
		cfg:   defaultConfig(),
		mode:  ptrMode(contract.ModeTargeted),
		clock: &mutClock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)},
	}
	h.m = New(Options{
		Prober:        h.prober,
		Router:        h.router,
		Store:         h.store,
		Config:        func() contract.DiagnosticsConfig { return *h.cfg },
		Mode:          func() contract.Mode { return *h.mode },
		Clock:         h.clock.now,
		PolicyVersion: func() contract.PolicyVersion { return 1 },
	})
	t.Cleanup(func() { h.m.Close(context.Background()) })
	return h
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestEligibilityMatrix(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dst := hostPort(t, "example.com:443")

	for _, kind := range []contract.ErrorKind{contract.ConnectRefused, contract.Timeout, contract.DNSFailure} {
		ok, reason := h.m.Evaluate(ctx, FailureEvent{Failure: failure(kind, dst)})
		if !ok {
			t.Fatalf("%s should be eligible, refused: %s", kind, reason)
		}
	}

	all := []contract.ErrorKind{
		contract.InvalidDestination, contract.Unsupported, contract.PolicyDenied,
		contract.Stopped, contract.PoolUnavailable, contract.ExitUnreachable,
		contract.HandshakeRejected, contract.DestinationRefused,
		contract.DestinationUnconfirmed, contract.Timeout, contract.Canceled,
		contract.ConnectRefused, contract.DNSFailure, contract.TLSFailure,
		contract.SourceFetch, contract.ImportInvalid, contract.StateCorrupt,
		contract.StateLocked, contract.PeerRejected, contract.Internal,
	}
	eligible := map[contract.ErrorKind]bool{
		contract.ConnectRefused: true, contract.Timeout: true, contract.DNSFailure: true,
	}
	for _, kind := range all {
		if eligible[kind] {
			continue
		}
		ok, reason := h.m.Evaluate(ctx, FailureEvent{Failure: failure(kind, dst)})
		if ok || reason == "" {
			t.Fatalf("%s must be refused with a reason, got ok=%v reason=%q", kind, ok, reason)
		}
	}

	if ok, _ := h.m.Evaluate(ctx, FailureEvent{Failure: &contract.Failure{
		Kind: contract.ConnectRefused, Path: contract.PathPool, Destination: dst,
	}}); ok {
		t.Fatal("pool-path failure became eligible")
	}
	for name, ev := range map[string]FailureEvent{
		"http":      {Failure: failure(contract.ConnectRefused, dst), HTTPStatus: 403},
		"captcha":   {Failure: failure(contract.ConnectRefused, dst), Challenge: true},
		"ratelimit": {Failure: failure(contract.ConnectRefused, dst), RateLimited: true},
		"eof":       {Failure: failure(contract.ConnectRefused, dst), CleanEOF: true},
	} {
		if ok, _ := h.m.Evaluate(ctx, ev); ok {
			t.Fatalf("%s observation became eligible", name)
		}
	}

	*h.mode = contract.ModeStopped
	if ok, _ := h.m.Evaluate(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, dst)}); ok {
		t.Fatal("stopped mode became eligible")
	}
	*h.mode = contract.ModeDirectAll
	if ok, _ := h.m.Evaluate(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, dst)}); ok {
		t.Fatal("all-direct automatic diagnosis became eligible")
	}
	*h.mode = contract.ModeProxyAll
	if ok, _ := h.m.Evaluate(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, dst)}); ok {
		t.Fatal("all-proxy candidate became eligible")
	}

	*h.mode = contract.ModeTargeted
	h.router.dec.Route = contract.RoutePool
	if ok, _ := h.m.Evaluate(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, dst)}); ok {
		t.Fatal("pool-routed destination became a direct candidate")
	}
	h.router.dec.Route = contract.RouteDirect
	h.router.dec.Class = contract.ClassApproved
	if ok, _ := h.m.Evaluate(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, dst)}); ok {
		t.Fatal("already-approved destination became a candidate")
	}
	h.router.dec.Class = contract.ClassOther
	if ok, _ := h.m.Evaluate(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, contract.Destination{Kind: contract.DestCIDR})}); ok {
		t.Fatal("CIDR target became eligible")
	}
	if ok, _ := h.m.Evaluate(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, contract.Destination{Kind: contract.DestDomain, Host: "example.com"})}); ok {
		t.Fatal("port-less target became eligible")
	}
}

func TestSubmitProbeFailureCreatesProposalWithoutRequestMaterial(t *testing.T) {
	h := newHarness(t)
	dst := hostPort(t, "example.com:443")
	h.prober.fn = func(_ context.Context, req contract.ProbeRequest) (contract.ProbeResult, error) {
		return contract.ProbeResult{
			Kind: req.Kind, Trigger: req.Trigger, Destination: req.Destination,
			StartedAt: time.Now(), Outcome: contract.OutcomeRefused,
			Failure: contract.ConnectRefused, Eligible: true,
		}, nil
	}
	f := failure(contract.ConnectRefused, dst)
	f.Message = "GET https://user:pass@example.com/private?token=SECRET Cookie: sid=SECRET body=SECRET"

	res, err := h.m.Submit(context.Background(), FailureEvent{Failure: f})
	if err != nil || !res.Accepted {
		t.Fatalf("submit = %#v err=%v", res, err)
	}
	waitFor(t, "proposal", func() bool { return h.store.count() == 1 })

	proposals := h.store.all()
	if len(proposals) != 1 || len(proposals[0].Evidence) != 1 {
		t.Fatalf("proposals = %#v", proposals)
	}
	ev := proposals[0].Evidence[0]
	if ev.Address != "example.com:443" || ev.ProbeKind != contract.ProbeDirectTCP ||
		ev.Kind != contract.EvidenceDirectRefused {
		t.Fatalf("evidence = %#v", ev)
	}
	requests := h.prober.requests()
	if len(requests) != 1 || requests[0].Kind != contract.ProbeDirectTCP {
		t.Fatalf("probe requests = %#v", requests)
	}
	raw, err := json.Marshal(h.store.all())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SECRET", "Cookie", "user:pass", "?token", "/private"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("serialized evidence contains %q: %s", forbidden, raw)
		}
	}
}

func TestProbeThatOnlyProvedTCPCreatesNoProposal(t *testing.T) {
	h := newHarness(t)
	dst := hostPort(t, "example.com:80")
	if _, err := h.m.Submit(context.Background(), FailureEvent{Failure: failure(contract.ConnectRefused, dst)}); err != nil {
		t.Fatal(err)
	}
	<-h.prober.finished
	waitFor(t, "idle", func() bool {
		s := h.m.Stats()
		return s.Queued == 0 && s.InFlight == 0
	})
	if got := h.store.count(); got != 0 {
		t.Fatalf("TCP-only success produced %d proposals", got)
	}
}

func TestIPEvidenceIsTCPStageOnly(t *testing.T) {
	h := newHarness(t)
	dst := hostPort(t, "192.0.2.10:80")
	h.prober.fn = func(_ context.Context, req contract.ProbeRequest) (contract.ProbeResult, error) {
		return contract.ProbeResult{
			Kind: req.Kind, Destination: req.Destination, StartedAt: time.Now(),
			Outcome: contract.OutcomeRefused, Failure: contract.ConnectRefused, Eligible: true,
		}, nil
	}
	if _, err := h.m.Submit(context.Background(), FailureEvent{Failure: failure(contract.ConnectRefused, dst)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "proposal", func() bool { return h.store.count() == 1 })
	p := h.store.all()[0]
	if p.Evidence[0].ProbeKind != contract.ProbeDirectTCP || !strings.Contains(p.Note, "TCP stage only") {
		t.Fatalf("IP evidence is not labelled as TCP-only: %#v note=%q", p.Evidence[0], p.Note)
	}
	if len(h.prober.requests()) != 1 {
		t.Fatalf("IP destination received a TLS probe: %#v", h.prober.requests())
	}
}

func TestHostnameTLSStageUsesVerificationAndKeepsStage(t *testing.T) {
	h := newHarness(t)
	dst := hostPort(t, "example.com:443")
	h.prober.fn = func(_ context.Context, req contract.ProbeRequest) (contract.ProbeResult, error) {
		res := contract.ProbeResult{
			Kind: req.Kind, Destination: req.Destination, StartedAt: time.Now(),
			Outcome: contract.OutcomeSuccess,
		}
		if req.Kind == contract.ProbeDirectTLS {
			res.Outcome = contract.OutcomeTimeout
			res.Failure = contract.Timeout
			res.Eligible = true
		}
		return res, nil
	}
	if _, err := h.m.Submit(context.Background(), FailureEvent{Failure: failure(contract.Timeout, dst)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "proposal", func() bool { return h.store.count() == 1 })
	reqs := h.prober.requests()
	if len(reqs) != 2 || reqs[0].Kind != contract.ProbeDirectTCP || reqs[1].Kind != contract.ProbeDirectTLS {
		t.Fatalf("probe plan = %#v", reqs)
	}
	p := h.store.all()[0]
	if p.Evidence[0].ProbeKind != contract.ProbeDirectTLS || !strings.Contains(p.Note, "full certificate verification") {
		t.Fatalf("TLS evidence is not labelled: %#v note=%q", p.Evidence[0], p.Note)
	}
}

func TestInvalidTLSProducesNoProposal(t *testing.T) {
	h := newHarness(t)
	dst := hostPort(t, "example.com:443")
	h.prober.fn = func(_ context.Context, req contract.ProbeRequest) (contract.ProbeResult, error) {
		res := contract.ProbeResult{Kind: req.Kind, Destination: req.Destination, StartedAt: time.Now(), Outcome: contract.OutcomeSuccess}
		if req.Kind == contract.ProbeDirectTLS {
			res.Outcome = contract.OutcomeTLSFailure
			res.Failure = contract.TLSFailure
			res.Eligible = false
		}
		return res, nil
	}
	if _, err := h.m.Submit(context.Background(), FailureEvent{Failure: failure(contract.ConnectRefused, dst)}); err != nil {
		t.Fatal(err)
	}
	<-h.prober.finished
	<-h.prober.finished
	waitFor(t, "idle", func() bool { s := h.m.Stats(); return s.Queued == 0 && s.InFlight == 0 })
	if got := h.store.count(); got != 0 {
		t.Fatalf("TLS verification failure produced %d proposals", got)
	}
}

func TestDisableCancelsQueueAndInFlight(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	defer close(release)
	h.prober.fn = func(ctx context.Context, req contract.ProbeRequest) (contract.ProbeResult, error) {
		select {
		case <-ctx.Done():
			return contract.ProbeResult{}, ctx.Err()
		case <-release:
			return contract.ProbeResult{Kind: req.Kind, Destination: req.Destination, Outcome: contract.OutcomeSuccess}, nil
		}
	}
	ctx := context.Background()
	for _, host := range []string{"a.example:80", "b.example:80", "c.example:80"} {
		if _, err := h.m.Submit(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, hostPort(t, host))}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "in-flight probe", func() bool { return h.m.Stats().InFlight == 1 })
	waitFor(t, "queued probes", func() bool { return h.m.Stats().Queued == 2 })

	queued, inflight := h.m.Disable(ctx)
	if queued != 2 || inflight != 1 {
		t.Fatalf("disable canceled queued=%d inflight=%d, want 2/1", queued, inflight)
	}
	if s := h.m.Stats(); s.Queued != 0 || s.InFlight != 0 {
		t.Fatalf("stats after disable = %#v", s)
	}
	if h.prober.cancels() == 0 {
		t.Fatal("Prober.CancelQueued was not called")
	}
	if res, err := h.m.Submit(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, hostPort(t, "d.example:80"))}); err != nil || res.Accepted || res.Reason != ReasonDisabled {
		t.Fatalf("submit while disabled = %#v err=%v", res, err)
	}
}

func TestQueueCapDropsOldestDiagnostic(t *testing.T) {
	h := newHarness(t)
	h.cfg.QueueLimit = 2
	release := make(chan struct{})
	defer close(release)
	h.prober.fn = func(ctx context.Context, req contract.ProbeRequest) (contract.ProbeResult, error) {
		select {
		case <-ctx.Done():
			return contract.ProbeResult{}, ctx.Err()
		case <-release:
			return contract.ProbeResult{Kind: req.Kind, Destination: req.Destination, Outcome: contract.OutcomeSuccess}, nil
		}
	}
	ctx := context.Background()
	if _, err := h.m.Submit(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, hostPort(t, "a.example:80"))}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "in-flight", func() bool { return h.m.Stats().InFlight == 1 })
	for _, host := range []string{"b.example:80", "c.example:80"} {
		res, err := h.m.Submit(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, hostPort(t, host))})
		if err != nil || !res.Accepted || res.DroppedOldest {
			t.Fatalf("submit %s = %#v err=%v", host, res, err)
		}
	}
	res, err := h.m.Submit(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, hostPort(t, "d.example:80"))})
	if err != nil || !res.Accepted || !res.DroppedOldest {
		t.Fatalf("overflow submit = %#v err=%v", res, err)
	}
	if s := h.m.Stats(); s.Queued != 2 || s.InFlight != 1 {
		t.Fatalf("queue cap not enforced: %#v", s)
	}
	if got := h.prober.callCount(); got != 1 {
		t.Fatalf("probe calls = %d, want 1 (only the in-flight job)", got)
	}
}

func TestCooldownCap(t *testing.T) {
	h := newHarness(t)
	dst := hostPort(t, "example.com:80")
	h.prober.fn = func(_ context.Context, req contract.ProbeRequest) (contract.ProbeResult, error) {
		return contract.ProbeResult{
			Kind: req.Kind, Destination: req.Destination, StartedAt: time.Now(),
			Outcome: contract.OutcomeRefused, Failure: contract.ConnectRefused, Eligible: true,
		}, nil
	}
	ctx := context.Background()
	if _, err := h.m.Submit(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, dst)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "proposal", func() bool { return h.store.count() == 1 })
	res, err := h.m.Submit(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, dst)})
	if err != nil || res.Accepted || res.Reason != ReasonCooldown {
		t.Fatalf("second submit = %#v err=%v", res, err)
	}
	h.clock.advance(contract.ProbeCooldownBase + time.Second)
	res, err = h.m.Submit(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, dst)})
	if err != nil || !res.Accepted {
		t.Fatalf("submit after cooldown = %#v err=%v", res, err)
	}
}

func TestAllProxyCreatesNoComparisonProbe(t *testing.T) {
	h := newHarness(t)
	*h.mode = contract.ModeProxyAll
	h.router.dec = contract.Decision{
		Mode: contract.ModeProxyAll, Class: contract.ClassOther,
		Route: contract.RoutePool, PolicyVersion: 1,
	}
	res, err := h.m.Submit(context.Background(), FailureEvent{Failure: failure(contract.ConnectRefused, hostPort(t, "example.com:443"))})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted || res.Reason != ReasonAllProxy {
		t.Fatalf("all-proxy submit = %#v", res)
	}
	if got := h.prober.callCount(); got != 0 {
		t.Fatalf("all-proxy started %d probes", got)
	}
}

func TestDisabledDiagnosticsCreatesNoCandidate(t *testing.T) {
	h := newHarness(t)
	h.cfg.Enabled = false
	res, err := h.m.Submit(context.Background(), FailureEvent{Failure: failure(contract.ConnectRefused, hostPort(t, "example.com:80"))})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted || res.Reason != ReasonDisabled {
		t.Fatalf("disabled submit = %#v", res)
	}
	if got := h.prober.callCount(); got != 0 {
		t.Fatalf("disabled diagnostics started %d probes", got)
	}
}

func TestManualDiagnoseIsBoundedAndIndependentOfDisable(t *testing.T) {
	h := newHarness(t)
	h.m.Disable(context.Background())
	dst := hostPort(t, "example.com:443")
	h.prober.fn = func(_ context.Context, req contract.ProbeRequest) (contract.ProbeResult, error) {
		res := contract.ProbeResult{
			Kind: req.Kind, Destination: req.Destination, StartedAt: time.Now(),
			Outcome: contract.OutcomeSuccess,
		}
		if req.Kind == contract.ProbeDirectTLS {
			res.Outcome = contract.OutcomeTimeout
			res.Failure = contract.Timeout
			res.Eligible = true
		}
		return res, nil
	}
	res, err := h.m.Diagnose(context.Background(), dst)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Eligible || res.ProposalID == "" || len(res.Attempts) != 2 {
		t.Fatalf("manual diagnose = %#v", res)
	}
	if !strings.Contains(res.Scope, "one destination") {
		t.Fatalf("scope = %q", res.Scope)
	}
	if got := h.prober.callCount(); got != 2 {
		t.Fatalf("manual diagnose ran %d probes, want at most 2", got)
	}
	if _, err := h.m.Diagnose(context.Background(), dst); !errors.Is(err, ErrCooldown) {
		t.Fatalf("second manual diagnose error = %v, want ErrCooldown", err)
	}
	if got := h.prober.callCount(); got != 2 {
		t.Fatalf("cooldown did not bound manual diagnose: %d probes", got)
	}
}

func TestManualDiagnoseAllDirectAllowedAndStoppedRefused(t *testing.T) {
	h := newHarness(t)
	*h.mode = contract.ModeDirectAll
	if _, err := h.m.Diagnose(context.Background(), hostPort(t, "example.com:80")); err != nil {
		t.Fatalf("manual diagnose in all-direct: %v", err)
	}
	*h.mode = contract.ModeStopped
	_, err := h.m.Diagnose(context.Background(), hostPort(t, "example.com:80"))
	var f *contract.Failure
	if !errors.As(err, &f) || f.Kind != contract.Stopped {
		t.Fatalf("stopped diagnose error = %v", err)
	}
}

func TestManualInvalidTLSProducesNoProposal(t *testing.T) {
	h := newHarness(t)
	h.prober.fn = func(_ context.Context, req contract.ProbeRequest) (contract.ProbeResult, error) {
		res := contract.ProbeResult{Kind: req.Kind, Destination: req.Destination, StartedAt: time.Now(), Outcome: contract.OutcomeSuccess}
		if req.Kind == contract.ProbeDirectTLS {
			res.Outcome = contract.OutcomeTLSFailure
			res.Failure = contract.TLSFailure
		}
		return res, nil
	}
	res, err := h.m.Diagnose(context.Background(), hostPort(t, "example.com:443"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Eligible || res.ProposalID != "" || h.store.count() != 0 {
		t.Fatalf("invalid TLS produced a proposal: %#v", res)
	}
}

func TestRecheckRejectedProposalKeepsRejected(t *testing.T) {
	h := newHarness(t)
	dst := hostPort(t, "example.com:80")
	h.store.byID[fakeID(dst)] = contract.Proposal{
		ID: fakeID(dst), Destination: dst, Status: contract.ProposalRejected,
		CreatedAt: h.clock.now(), ExpiresAt: h.clock.now().Add(contract.ProposalRetention),
	}
	h.prober.fn = func(_ context.Context, req contract.ProbeRequest) (contract.ProbeResult, error) {
		return contract.ProbeResult{
			Kind: req.Kind, Destination: req.Destination, StartedAt: time.Now(),
			Outcome: contract.OutcomeRefused, Failure: contract.ConnectRefused, Eligible: true,
		}, nil
	}
	res, err := h.m.Recheck(context.Background(), fakeID(dst))
	if err != nil {
		t.Fatal(err)
	}
	if !res.EvidenceAdded || res.Proposal.Status != contract.ProposalRejected {
		t.Fatalf("recheck = %#v", res)
	}
	if len(res.Proposal.Evidence) != 1 {
		t.Fatalf("evidence = %#v", res.Proposal.Evidence)
	}
}

func TestAcceptReturnsExactChosenDestination(t *testing.T) {
	h := newHarness(t)
	dst := hostPort(t, "api.example.com:8443")
	h.store.byID[fakeID(dst)] = contract.Proposal{
		ID: fakeID(dst), Destination: dst, Status: contract.ProposalPending,
		CreatedAt: h.clock.now(), ExpiresAt: h.clock.now().Add(contract.ProposalRetention),
	}
	got, err := h.m.Accept(fakeID(dst))
	if err != nil {
		t.Fatal(err)
	}
	if got != dst {
		t.Fatalf("accept returned %#v, want %#v", got, dst)
	}
	p, _ := h.store.Get(fakeID(dst))
	if p.Status != contract.ProposalAccepted {
		t.Fatalf("status = %s", p.Status)
	}
	if len(h.store.all()) != 1 {
		t.Fatalf("accept touched other state: %#v", h.store.all())
	}
}

func TestStatsReportsDisabled(t *testing.T) {
	h := newHarness(t)
	h.m.Disable(context.Background())
	if s := h.m.Stats(); !s.Disabled {
		t.Fatalf("stats = %#v", s)
	}
	h.m.Enable()
	if s := h.m.Stats(); s.Disabled {
		t.Fatalf("stats after enable = %#v", s)
	}
}

func TestModeChangeDropsQueuedCandidateBeforeProbe(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	h.prober.fn = func(ctx context.Context, req contract.ProbeRequest) (contract.ProbeResult, error) {
		select {
		case <-ctx.Done():
			return contract.ProbeResult{}, ctx.Err()
		case <-release:
			return contract.ProbeResult{Kind: req.Kind, Destination: req.Destination, Outcome: contract.OutcomeSuccess}, nil
		}
	}
	ctx := context.Background()
	if _, err := h.m.Submit(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, hostPort(t, "a.example:80"))}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "in-flight", func() bool { return h.m.Stats().InFlight == 1 })
	if _, err := h.m.Submit(ctx, FailureEvent{Failure: failure(contract.ConnectRefused, hostPort(t, "b.example:80"))}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "queued", func() bool { return h.m.Stats().Queued == 1 })
	*h.mode = contract.ModeProxyAll
	close(release)
	waitFor(t, "idle", func() bool { s := h.m.Stats(); return s.Queued == 0 && s.InFlight == 0 })
	if got := h.prober.callCount(); got != 1 {
		t.Fatalf("queued candidate probed after mode change: %d calls", got)
	}
}
