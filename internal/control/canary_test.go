package control

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/diagnostics"
	"routelatch/internal/store"
)

// fixedProber answers every direct probe with a refusal, so the canary test
// exercises the real diagnostics and proposal paths without resolving or
// dialing any name.
type fixedProber struct{ now func() time.Time }

func (p fixedProber) Enqueue(context.Context, contract.ProbeRequest) error { return nil }
func (p fixedProber) Run(_ context.Context, req contract.ProbeRequest) (contract.ProbeResult, error) {
	return contract.ProbeResult{
		Kind: req.Kind, Trigger: req.Trigger, Destination: req.Destination,
		StartedAt: p.now(), Outcome: contract.OutcomeRefused, Failure: contract.ConnectRefused,
		Eligible: true,
	}, nil
}
func (p fixedProber) CancelQueued(context.Context) (int, error) { return 0, nil }

// failingDirect reports the same DNS-failure shape the production direct dialer
// reports, including the destination that must never be recorded.
type failingDirect struct{}

func (failingDirect) DialDirect(_ context.Context, dst contract.Destination) (net.Conn, error) {
	return nil, &contract.Failure{
		Kind: contract.DNSFailure, Path: contract.PathDirect, Op: "test.direct",
		Destination: dst, Message: "the local resolver could not resolve the destination",
	}
}

func canaryName(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return "rl-canary-" + hex.EncodeToString(b[:]) + ".invalid"
}

type canaryHarness struct {
	dir    string
	paths  store.Paths
	st     *store.Store
	svc    *Service
	srv    *Server
	client *Client
	logBuf *bytes.Buffer
}

func newCanaryHarness(t *testing.T, logBuf *bytes.Buffer) *canaryHarness {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rlcanary")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return newCanaryHarnessAt(t, dir, logBuf)
}

// newCanaryHarnessAt opens a service over an existing data root so the restart
// assertion runs against the files the first run left behind.
func newCanaryHarnessAt(t *testing.T, dir string, logBuf *bytes.Buffer) *canaryHarness {
	t.Helper()
	paths, err := store.ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: logBuf})
	if err != nil {
		t.Fatal(err)
	}
	// Wire the runtime pieces StartNetwork normally builds, with an offline
	// prober and direct dialer so the test is deterministic.
	svc.diag = diagnostics.New(diagnostics.Options{
		Prober: fixedProber{now: svc.now}, Router: routerView{s: svc}, Store: svc.props,
		Config:        func() contract.DiagnosticsConfig { return svc.Snapshot().Config.Diagnostics },
		ProbeConfig:   func() contract.ProbeConfig { return svc.Snapshot().Config.Probes },
		Mode:          func() contract.Mode { return svc.Snapshot().State.Mode },
		PolicyVersion: func() contract.PolicyVersion { return svc.policy.Load().PolicyVersion() },
		Clock:         svc.now,
	})
	svc.direct = &diagnosticDialer{inner: failingDirect{}, svc: svc}
	attachFrontend(t, svc, &fakePool{}, svc.direct)

	srv, err := NewServer(ServerOptions{Path: paths.Socket, Handler: svc, AllowStale: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := &canaryHarness{
		dir: dir, paths: paths, st: st, svc: svc, srv: srv, logBuf: logBuf,
		client: &Client{Path: paths.Socket, Timeout: 30 * time.Second},
	}
	t.Cleanup(h.close)
	return h
}

func (h *canaryHarness) close() {
	if h.srv != nil {
		_ = h.srv.Close()
		h.srv = nil
	}
	if h.svc != nil {
		_ = h.svc.Close(context.Background())
		h.svc = nil
	}
}

func (h *canaryHarness) call(t *testing.T, req Request) contract.Envelope {
	t.Helper()
	env, err := h.client.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("%s transport error: %v", req.Op, err)
	}
	return env
}

func (h *canaryHarness) proposals(t *testing.T) ProposalsData {
	t.Helper()
	env := h.call(t, Request{Op: OpProposalsList})
	if !env.OK {
		t.Fatalf("proposals list failed: %+v", env.Error)
	}
	raw, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data ProposalsData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func (h *canaryHarness) waitForProposal(t *testing.T) ProposalView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data := h.proposals(t)
		if len(data.Proposals) > 0 {
			return data.Proposals[0]
		}
		if time.Now().After(deadline) {
			t.Fatal("the failed direct request never produced an in-memory proposal")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *canaryHarness) diagnose(t *testing.T, input string) DiagnoseData {
	t.Helper()
	env := h.call(t, Request{Op: OpDiagnose, Input: input})
	if !env.OK {
		t.Fatalf("diagnose failed: %+v", env.Error)
	}
	raw, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data DiagnoseData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

// TestVisitedDestinationsNeverReachDiskAndProposalsDieAtRestart is the D21
// canary test: one unique destination per exercised path, a failed direct
// request, a manual probe, a rejected and an accepted proposal, a mode switch,
// a backup and a restart. No file under the data root and no captured service
// output may contain an unapproved canary.
func TestVisitedDestinationsNeverReachDiskAndProposalsDieAtRestart(t *testing.T) {
	canary := canaryName(t)
	diagnosed := canaryName(t)
	var logBuf bytes.Buffer
	h := newCanaryHarness(t, &logBuf)

	// 1. A failed direct request through the HTTP listener: targeted mode
	// routes the unapproved public destination directly, the failure becomes a
	// diagnostic candidate, and the in-memory registry gets a proposal.
	proxyURL, err := url.Parse("http://" + h.svc.httpAddr)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}
	resp, err := client.Get("http://" + canary + ":80/")
	if err != nil {
		t.Fatalf("request through the proxy failed to return a response: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("failed direct request status = %d, want 502", resp.StatusCode)
	}
	first := h.waitForProposal(t)

	// The proposal survives in memory across separate management calls.
	again := h.proposals(t)
	if len(again.Proposals) != 1 || again.Proposals[0].ID != first.ID {
		t.Fatalf("proposal did not survive a second call: %+v", again.Proposals)
	}
	if !strings.Contains(hostOfProposal(t, first), canary) {
		t.Fatalf("proposal does not name the canary destination: %+v", first)
	}

	// 2. Reject it: still in memory, and it must never reach a file.
	if env := h.call(t, Request{Op: OpProposalsReject, ID: string(first.ID)}); !env.OK {
		t.Fatalf("reject failed: %+v", env.Error)
	}
	rejected := h.proposals(t)
	if len(rejected.Proposals) != 1 || rejected.Proposals[0].Status != contract.ProposalRejected {
		t.Fatalf("rejected proposal missing from memory: %+v", rejected.Proposals)
	}

	// 3. An explicit diagnostic probe for a second canary: the reply may name
	// it, but its evidence must stay in memory.
	diagnosis := h.diagnose(t, diagnosed+":80")
	if diagnosis.ProposalID == "" {
		t.Fatal("diagnose did not create a proposal")
	}
	if env := h.call(t, Request{Op: OpProposalsReject, ID: string(diagnosis.ProposalID)}); !env.OK {
		t.Fatalf("second reject failed: %+v", env.Error)
	}

	// 4. Accept a proposal for a destination that is not a canary, so the
	// accepted-rule path runs while the canary stays unapproved.
	accepted := h.diagnose(t, "approved-"+strings.TrimPrefix(canary, "rl-canary-")+".example.com:80")
	if accepted.ProposalID == "" {
		t.Fatal("diagnose did not create an acceptable proposal")
	}
	acceptEnv := h.call(t, Request{Op: OpProposalsAccept, ID: string(accepted.ProposalID)})
	if !acceptEnv.OK {
		t.Fatalf("accept failed: %+v", acceptEnv.Error)
	}

	// 5. A mode switch rewrites persisted state; a backup copies it.
	if env := h.call(t, Request{Op: OpMode, Mode: string(contract.ModeDirectAll)}); !env.OK {
		t.Fatalf("mode all-direct failed: %+v", env.Error)
	}
	if env := h.call(t, Request{Op: OpMode, Mode: string(contract.ModeTargeted)}); !env.OK {
		t.Fatalf("mode targeted failed: %+v", env.Error)
	}
	if env := h.call(t, Request{Op: OpBackup}); !env.OK {
		t.Fatalf("backup failed: %+v", env.Error)
	}

	// Read-only replies that are not the explicit diagnose/proposals answer
	// must not echo a visited destination either.
	for _, op := range []Request{{Op: OpStatus}, {Op: OpDoctor}, {Op: OpPoolList}} {
		env := h.call(t, op)
		raw, _ := json.Marshal(env)
		if strings.Contains(string(raw), canary) || strings.Contains(string(raw), diagnosed) {
			t.Fatalf("%s echoed a visited destination", op.Op)
		}
	}

	// 6. Restart over the same data root: proposals are gone and nothing is
	// resurrected from disk.
	h.close()
	restarted := newCanaryHarnessAt(t, h.dir, &logBuf)
	if data := restarted.proposals(t); len(data.Proposals) != 0 {
		t.Fatalf("restart resurrected %d proposal(s): %+v", len(data.Proposals), data.Proposals)
	}
	restarted.close()

	// 7. No file under the data root may contain an unapproved canary, and the
	// file log must have been exercised.
	for _, want := range []string{canary, diagnosed} {
		assertAbsentFromTree(t, h.dir, want)
	}
	logPath := filepath.Join(h.paths.LogDir, "routelatch.log")
	info, err := os.Stat(logPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("operational log was not written: %v", err)
	}
	for _, want := range []string{canary, diagnosed} {
		if strings.Contains(logBuf.String(), want) {
			t.Fatalf("foreground service log contains %q", want)
		}
	}
	raw, err := os.ReadFile(h.paths.State)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk struct {
		contract.State
		Proposals []json.RawMessage `json:"proposals"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if len(onDisk.Proposals) != 0 {
		t.Fatalf("state.json still stores %d proposal(s)", len(onDisk.Proposals))
	}
}

func hostOfProposal(t *testing.T, p ProposalView) string {
	t.Helper()
	if p.Destination.Kind == contract.DestDomain {
		return p.Destination.Host
	}
	if p.Destination.Kind == contract.DestIP {
		return p.Destination.Addr.String()
	}
	return ""
}

func assertAbsentFromTree(t *testing.T, root, canary string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(raw, []byte(canary)) {
			t.Errorf("%s contains the visited destination %q", path, canary)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
