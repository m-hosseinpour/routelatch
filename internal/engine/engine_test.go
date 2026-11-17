package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/engine"
)

var errReadTimedOut = errors.New("test read timed out")

type readResult struct {
	data []byte
	err  error
}

func readWithin(conn net.Conn, d time.Duration) readResult {
	ch := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 512)
		n, err := conn.Read(buf)
		ch <- readResult{data: buf[:n], err: err}
	}()
	select {
	case r := <-ch:
		return r
	case <-time.After(d):
		return readResult{err: errReadTimedOut}
	}
}

// TestDialReturnsBeforeDestinationConnect encodes the T1 measurement that the
// whole proof-class design rests on: core.Dial returns in microseconds, the
// destination is contacted hundreds of milliseconds later, and a dead exit can
// look identical. The engine therefore never reports ProofConfirmed on the core
// path.
func TestDialReturnsBeforeDestinationConnect(t *testing.T) {
	echo := newPeer(t, "echo")
	uuid := randomUUID(t)
	exit := startExit(t, exitSpec{Protocol: "vless", Port: int(freePort(t)), UUID: uuid})
	e := startEngine(t, 1, vlessRecord("x-vless", "127.0.0.1", uint16(exit.port), uuid, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dst := ipDestination(echo.port())
	start := time.Now()
	res, err := e.Dial(ctx, dialRequest("x-vless", dst, 1))
	dialed := time.Since(start)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer res.Conn.Close()

	if res.Proof != contract.ProofAccepted {
		t.Fatalf("proof = %q, want accepted (the core confirms nothing)", res.Proof)
	}
	if res.Tag != "rl-x-vless" {
		t.Fatalf("tag = %q, want the stable per-record tag", res.Tag)
	}
	if res.AcceptedAt.IsZero() {
		t.Fatalf("accepted time is zero")
	}
	if dialed > 100*time.Millisecond {
		t.Fatalf("dial took %v; a core dial must not wait for the destination", dialed)
	}
	if n := echo.accepts(); n != 0 {
		t.Fatalf("destination had %d contacts when Dial returned; a dial return is not readiness", n)
	}

	if !echo.waitAccept(3 * time.Second) {
		t.Fatalf("destination was never contacted")
	}
	accepted := time.Unix(0, echo.first.Load())
	if !accepted.After(start.Add(dialed)) {
		t.Fatalf("destination accepted at %v, before Dial returned (%v)", accepted, start.Add(dialed))
	}
	t.Logf("dial returned in %v; destination contacted %v later; proof=accepted", dialed, accepted.Sub(start.Add(dialed)))

	if _, err := res.Conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readWithin(res.Conn, 2*time.Second)
	if got.err != nil {
		t.Fatalf("read echo: %v", got.err)
	}
	if string(got.data) != "ping" {
		t.Fatalf("echo = %q", got.data)
	}
}

// TestDeadExitAndSilentExitAreTyped covers the two shapes a broken exit takes:
// an accepted-but-silent peer (no evidence at all) and a refused exit port
// (the core reports a transport error through the tracked-error channel).
func TestDeadExitAndSilentExitAreTyped(t *testing.T) {
	silent := newPeer(t, "accept-silent")
	refusedPort := freePort(t)
	dst := newPeer(t, "echo")
	uuid := randomUUID(t)

	e := startEngine(t, 1,
		vlessRecord("x-silent", "127.0.0.1", silent.port(), uuid, nil),
		vlessRecord("x-refused", "127.0.0.1", refusedPort, uuid, nil),
	)

	t.Run("silent exit times out with no destination contact", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		defer cancel()
		res, err := e.Dial(ctx, dialRequest("x-silent", ipDestination(dst.port()), 1))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer res.Conn.Close()
		if res.Proof != contract.ProofAccepted {
			t.Fatalf("proof = %q", res.Proof)
		}
		start := time.Now()
		got := readWithin(res.Conn, 3*time.Second)
		if got.err == errReadTimedOut {
			t.Fatalf("read did not return after the session deadline")
		}
		if kind := failureKind(t, got.err); kind != contract.Timeout {
			t.Fatalf("read kind = %q, want timeout (got %v)", kind, got.err)
		}
		if waited := time.Since(start); waited > 2*time.Second {
			t.Fatalf("deadline surfaced after %v", waited)
		}
		if n := dst.accepts(); n != 0 {
			t.Fatalf("destination had %d contacts through a silent exit", n)
		}
	})

	t.Run("refused exit is exit_unreachable", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		res, err := e.Dial(ctx, dialRequest("x-refused", ipDestination(dst.port()), 1))
		if err != nil {
			// An implementation that surfaces the transport error at dial
			// time is acceptable only if it is typed the same way.
			if kind := failureKind(t, err); kind != contract.ExitUnreachable {
				t.Fatalf("dial error kind = %q, want exit_unreachable", kind)
			}
			return
		}
		defer res.Conn.Close()
		start := time.Now()
		got := readWithin(res.Conn, 6*time.Second)
		if got.err == errReadTimedOut {
			t.Fatalf("read never returned for a refused exit")
		}
		if kind := failureKind(t, got.err); kind != contract.ExitUnreachable {
			t.Fatalf("read kind = %q, want exit_unreachable (got %v)", kind, got.err)
		}
		// T1 measured the core's retry at ~2.0s; the assertion is only that the
		// error is attributable, not that it is instant.
		t.Logf("refused exit surfaced as exit_unreachable after %v", time.Since(start))
		if n := dst.accepts(); n != 0 {
			t.Fatalf("destination had %d contacts through a refused exit", n)
		}
	})
}

// TestMissingTagAndEmptyPoolNeverTouchTheNetwork is the anti-fallback proof:
// an unknown exit id, a removed exit and an unset exit all fail before any
// socket is opened, so neither the exit nor the destination sees a contact.
func TestMissingTagAndEmptyPoolNeverTouchTheNetwork(t *testing.T) {
	exitSpy := newPeer(t, "accept-silent")
	dst := newPeer(t, "echo")
	uuid := randomUUID(t)
	e := startEngine(t, 1, vlessRecord("x-1", "127.0.0.1", exitSpy.port(), uuid, nil))
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		req  engine.DialRequest
	}{
		{"unknown exit", dialRequest("x-nope", ipDestination(dst.port()), 1)},
		{"no exit selected", dialRequest("", ipDestination(dst.port()), 1)},
	} {
		_, err := e.Dial(ctx, tc.req)
		if kind := failureKind(t, err); kind != contract.PoolUnavailable {
			t.Fatalf("%s: kind = %q, want pool_unavailable", tc.name, kind)
		}
	}

	if !e.RemoveExit("x-1") {
		t.Fatalf("RemoveExit reported a missing exit")
	}
	_, err := e.Dial(ctx, dialRequest("x-1", ipDestination(dst.port()), 1))
	if kind := failureKind(t, err); kind != contract.PoolUnavailable {
		t.Fatalf("removed exit: kind = %q, want pool_unavailable", kind)
	}

	time.Sleep(100 * time.Millisecond)
	if n := exitSpy.accepts(); n != 0 {
		t.Fatalf("exit was contacted %d times for a non-dialable exit", n)
	}
	if n := dst.accepts(); n != 0 {
		t.Fatalf("destination was contacted %d times without a usable exit", n)
	}
}

// TestPolicyVersionFence proves a stale decision cannot be dialed and that the
// fence only moves forward.
func TestPolicyVersionFence(t *testing.T) {
	exitSpy := newPeer(t, "accept-silent")
	dst := newPeer(t, "echo")
	uuid := randomUUID(t)
	e := startEngine(t, 1, vlessRecord("x-1", "127.0.0.1", exitSpy.port(), uuid, nil))

	_, err := e.Dial(context.Background(), dialRequest("x-1", ipDestination(dst.port()), 0))
	if kind := failureKind(t, err); kind != contract.Canceled {
		t.Fatalf("stale dial kind = %q, want canceled", kind)
	}

	e.SetPolicyVersion(2)
	_, err = e.Dial(context.Background(), dialRequest("x-1", ipDestination(dst.port()), 1))
	if kind := failureKind(t, err); kind != contract.Canceled {
		t.Fatalf("stale dial after bump: kind = %q, want canceled", kind)
	}

	e.SetPolicyVersion(1) // must be ignored
	if got := e.PolicyVersion(); got != 2 {
		t.Fatalf("policy version moved backwards to %d", got)
	}

	res, err := e.Dial(context.Background(), dialRequest("x-1", ipDestination(dst.port()), 2))
	if err != nil {
		t.Fatalf("current-version dial: %v", err)
	}
	if res.Tag != "rl-x-1" {
		t.Fatalf("tag = %q", res.Tag)
	}
	if !exitSpy.waitAccepts(1, 3*time.Second) {
		t.Fatalf("the accepted dial never reached the exit")
	}
	time.Sleep(100 * time.Millisecond)
	if n := exitSpy.accepts(); n != 1 {
		t.Fatalf("exit accepts = %d, want exactly the one accepted dial", n)
	}
	_ = res.Conn.Close()
	if n := dst.accepts(); n != 0 {
		t.Fatalf("destination contacted through a silent exit: %d", n)
	}
}

// TestGeneratedConfigHasNoFallbackSurface asserts the structural half of the
// anti-fallback property: the document we hand the core has no routing, no
// balancer, no direct outbound and a fail-closed default.
func TestGeneratedConfigHasNoFallbackSurface(t *testing.T) {
	uuid := randomUUID(t)
	records := []contract.Record{
		vlessRecord("x-raw", "127.0.0.1", 41234, uuid, nil),
		vlessRecord("x-tls", "127.0.0.1", 41235, uuid, func(r *contract.Record) {
			r.Transport = contract.TransportTLS
			r.Security = contract.SecurityTLS
			r.SNI = "rl.test"
			r.ALPN = []string{"http/1.1"}
			r.PinnedPeerCertSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		}),
	}
	e := startEngine(t, 1, records...)
	raw := e.ConfigForTest()
	if len(raw) == 0 {
		t.Fatalf("no generated config recorded")
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("config is not JSON: %v", err)
	}
	for key := range doc {
		if key != "log" && key != "outbounds" {
			t.Fatalf("generated config has unexpected root key %q", key)
		}
	}
	lower := string(raw)
	for _, forbidden := range []string{`"freedom"`, `"routing"`, `"balancer"`, `"dns"`, `"api"`, `"env"`, `"inbounds"`, `"stats"`, `"policy"`, `"proxySettings"`, `"sockopt"`, `"mux"`, `"allowInsecure"`} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("generated config contains forbidden %s: %s", forbidden, raw)
		}
	}

	outbounds, ok := doc["outbounds"].([]any)
	if !ok || len(outbounds) != 3 {
		t.Fatalf("outbounds = %v", doc["outbounds"])
	}
	first := outbounds[0].(map[string]any)
	if first["protocol"] != "blackhole" || first["tag"] != engine.BlockedTag {
		t.Fatalf("first outbound is not the fail-closed guard: %v", first)
	}
	tags := map[string]string{}
	for _, item := range outbounds[1:] {
		ob := item.(map[string]any)
		tags[ob["tag"].(string)] = ob["protocol"].(string)
	}
	if tags["rl-x-raw"] != "vless" || tags["rl-x-tls"] != "vless" {
		t.Fatalf("exit tags = %v", tags)
	}

	// The TLS record keeps verification on with a real pin and never emits
	// allowInsecure (checked above); the raw record has no TLS block at all.
	if !strings.Contains(string(raw), `"pinnedPeerCertSha256": "0000000000000000`) {
		t.Fatalf("pinned certificate missing from config: %s", raw)
	}
}

// TestUnsupportedExitsAreRefused keeps HTTP exits out of v1 and rejects the
// gated REALITY and skip-verify shapes instead of building a config for them.
// With per-record isolation Start succeeds and reports the record as excluded.
func TestUnsupportedExitsAreRefused(t *testing.T) {
	base := func() contract.Record {
		return vlessRecord("x-1", "127.0.0.1", 443, randomUUID(t), nil)
	}

	http := base()
	http.Protocol = contract.ProtocolHTTP
	expectExcluded(t, http, contract.Unsupported)

	reality := base()
	reality.Security = contract.SecurityReality
	expectExcluded(t, reality, contract.Unsupported)

	insecure := base()
	insecure.InsecureTLS = true
	expectExcluded(t, insecure, contract.Unsupported)

	quarantined := base()
	quarantined.Status = contract.RecordUnsupported
	expectExcluded(t, quarantined, contract.Unsupported)

	badTransport := base()
	badTransport.Transport = "kcp"
	expectExcluded(t, badTransport, contract.Unsupported)

	extra := base()
	extra.Transport = contract.TransportXHTTP
	extra.XHTTPExtra = map[string]any{"downloadSettings": map[string]any{}}
	expectExcluded(t, extra, contract.Unsupported)

	// A valid record with an empty engine is fine; a malformed destination is
	// refused before any dial.
	e := startEngine(t, 1)
	_, err := e.Dial(context.Background(), dialRequest("x-1", contract.Destination{Kind: contract.DestCIDR, Port: 443}, 1))
	if kind := failureKind(t, err); kind != contract.InvalidDestination {
		t.Fatalf("cidr destination: kind = %q, want invalid_destination", kind)
	}
	_, err = e.Dial(context.Background(), dialRequest("x-1", contract.Destination{Kind: contract.DestDomain, Host: "example.test"}, 1))
	if kind := failureKind(t, err); kind != contract.InvalidDestination {
		t.Fatalf("port-less destination: kind = %q, want invalid_destination", kind)
	}
}

// TestOneBadRecordDoesNotAbortStart proves the service-level guarantee: a pool
// with one unbuildable record and one good record still starts, and only the bad
// record is excluded.
func TestOneBadRecordDoesNotAbortStart(t *testing.T) {
	good := vlessRecord("x-good", "127.0.0.1", 443, randomUUID(t), nil)
	bad := vlessRecord("x-bad", "127.0.0.1", 443, randomUUID(t), func(r *contract.Record) {
		r.InsecureTLS = true
	})
	e, err := engine.Start(engine.Options{Exits: []contract.Record{bad, good}, LogLevel: "none"})
	if err != nil {
		t.Fatalf("start with one bad record: %v", err)
	}
	defer func() { _ = e.Stop(context.Background()) }()
	if !e.HasExit("x-good") {
		t.Fatal("the good record was not registered")
	}
	if e.HasExit("x-bad") {
		t.Fatal("the bad record was registered")
	}
	excluded := e.Excluded()
	if len(excluded) != 1 || excluded[0].ID != "x-bad" || excluded[0].Kind != contract.Unsupported || excluded[0].Reason != engine.ReasonRecordRejected {
		t.Fatalf("excluded = %+v", excluded)
	}
	if tag, ok := e.TagFor("x-good"); !ok || tag == "" {
		t.Fatalf("good record tag = %q, %v", tag, ok)
	}
}

// TestDestinationFailuresCollapse proves the honest limit from T1 §3: on the
// VLESS path a refused destination, a reset and a clean FIN all collapse into
// DestinationUnconfirmed, while a caller deadline stays Timeout and a clean
// end after data stays io.EOF.
func TestDestinationFailuresCollapse(t *testing.T) {
	uuid := randomUUID(t)
	exit := startExit(t, exitSpec{Protocol: "vless", Port: int(freePort(t)), UUID: uuid})
	e := startEngine(t, 1, vlessRecord("x-vless", "127.0.0.1", uint16(exit.port), uuid, nil))

	t.Run("refused destination", func(t *testing.T) {
		dstPort := freePort(t) // nothing listens here
		res, err := e.Dial(context.Background(), dialRequest("x-vless", ipDestination(dstPort), 1))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer res.Conn.Close()
		got := readWithin(res.Conn, 5*time.Second)
		if got.err == errReadTimedOut {
			t.Fatalf("read never returned for a refused destination")
		}
		if kind := failureKind(t, got.err); kind != contract.DestinationUnconfirmed {
			t.Fatalf("refused destination kind = %q, want destination_unconfirmed", kind)
		}
	})

	t.Run("reset destination", func(t *testing.T) {
		dst := newPeer(t, "reset-on-data")
		res, err := e.Dial(context.Background(), dialRequest("x-vless", ipDestination(dst.port()), 1))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer res.Conn.Close()
		if !dst.waitAccept(3 * time.Second) {
			t.Fatalf("destination was never contacted")
		}
		if _, err := res.Conn.Write([]byte("x")); err != nil {
			t.Fatalf("write: %v", err)
		}
		got := readWithin(res.Conn, 5*time.Second)
		if got.err == errReadTimedOut {
			t.Fatalf("read never returned after a reset")
		}
		if kind := failureKind(t, got.err); kind != contract.DestinationUnconfirmed {
			t.Fatalf("reset destination kind = %q, want destination_unconfirmed", kind)
		}
	})

	t.Run("silent destination hits the caller deadline", func(t *testing.T) {
		dst := newPeer(t, "accept-silent")
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		res, err := e.Dial(ctx, dialRequest("x-vless", ipDestination(dst.port()), 1))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer res.Conn.Close()
		got := readWithin(res.Conn, 3*time.Second)
		if got.err == errReadTimedOut {
			t.Fatalf("read never returned after the deadline")
		}
		if kind := failureKind(t, got.err); kind != contract.Timeout {
			t.Fatalf("silent destination kind = %q, want timeout", kind)
		}
	})

	t.Run("clean end after data stays EOF", func(t *testing.T) {
		dst := newPeer(t, "echo")
		res, err := e.Dial(context.Background(), dialRequest("x-vless", ipDestination(dst.port()), 1))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer res.Conn.Close()
		if !dst.waitAccept(3 * time.Second) {
			t.Fatalf("destination was never contacted")
		}
		if _, err := res.Conn.Write([]byte("hi")); err != nil {
			t.Fatalf("write: %v", err)
		}
		got := readWithin(res.Conn, 2*time.Second)
		if got.err != nil || string(got.data) != "hi" {
			t.Fatalf("echo = %q, %v", got.data, got.err)
		}
		dst.closeAll()
		got = readWithin(res.Conn, 3*time.Second)
		if got.err == errReadTimedOut {
			t.Fatalf("read never returned after the destination closed")
		}
		if !errors.Is(got.err, io.EOF) {
			t.Fatalf("end-after-data = %v, want io.EOF", got.err)
		}
	})
}

// TestWarmTransportsAreVisible documents what survives a close and a stop for
// transports the pinned core keeps warm (T1 §5, R-4). It asserts only the
// bookkeeping: gRPC/XHTTP warm transports are not exercised end to end.
func TestWarmTransportsAreVisible(t *testing.T) {
	uuid := randomUUID(t)
	raw := newPeer(t, "accept-silent")
	grpc := newPeer(t, "accept-silent")
	e := startEngine(t, 1,
		vlessRecord("x-raw", "127.0.0.1", raw.port(), uuid, nil),
		vlessRecord("x-grpc", "127.0.0.1", grpc.port(), uuid, func(r *contract.Record) {
			r.Transport = contract.TransportGRPC
			r.Security = contract.SecurityTLS
			r.GRPCService = "rl"
			r.SNI = "rl.test"
		}),
	)
	if got := e.WarmTransports(); len(got) != 0 {
		t.Fatalf("warm transports before any dial = %v", got)
	}
	dst := newPeer(t, "echo")

	res, err := e.Dial(context.Background(), dialRequest("x-grpc", ipDestination(dst.port()), 1))
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	_ = res.Conn.Close()
	warm := e.WarmTransports()
	if len(warm) != 1 || warm[0].Exit != "x-grpc" || warm[0].Transport != contract.TransportGRPC {
		t.Fatalf("warm transports = %+v", warm)
	}

	res, err = e.Dial(context.Background(), dialRequest("x-raw", ipDestination(dst.port()), 1))
	if err != nil {
		t.Fatalf("raw dial: %v", err)
	}
	_ = res.Conn.Close()
	if got := e.WarmTransports(); len(got) != 1 {
		t.Fatalf("raw transport was reported as warm: %+v", got)
	}
}

// TestTrojanRawDials keeps the second core protocol in the suite.
func TestTrojanRawDials(t *testing.T) {
	echo := newPeer(t, "echo")
	password := randomHex(t, 16)
	exit := startExit(t, exitSpec{Protocol: "trojan", Port: int(freePort(t)), Password: password})
	e := startEngine(t, 1, trojanRecord("x-trojan", "127.0.0.1", uint16(exit.port), password, nil))

	res, err := e.Dial(context.Background(), dialRequest("x-trojan", ipDestination(echo.port()), 1))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer res.Conn.Close()
	if res.Proof != contract.ProofAccepted {
		t.Fatalf("proof = %q", res.Proof)
	}
	if !echo.waitAccept(3 * time.Second) {
		t.Fatalf("destination was never contacted")
	}
	if _, err := res.Conn.Write([]byte("pong")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readWithin(res.Conn, 2*time.Second)
	if got.err != nil || string(got.data) != "pong" {
		t.Fatalf("echo = %q, %v", got.data, got.err)
	}
}

// TestTLSPinnedExitAndRuntimeAdd covers the TLS transport with a pinned leaf
// (verification stays on), the pin-failure path T1 could only source-read, and
// AddExit without restarting the core.
func TestTLSPinnedExitAndRuntimeAdd(t *testing.T) {
	certFile, keyFile, pin := tlsFixture(t)
	uuid := randomUUID(t)
	exit := startExit(t, exitSpec{
		Protocol: "vless",
		Port:     int(freePort(t)),
		UUID:     uuid,
		CertFile: certFile,
		KeyFile:  keyFile,
		SNI:      "rl.test",
	})
	echo := newPeer(t, "echo")

	// The core starts with no exits at all; the record arrives at runtime.
	e := startEngine(t, 1)

	good := vlessRecord("x-tls", "127.0.0.1", uint16(exit.port), uuid, func(r *contract.Record) {
		r.Transport = contract.TransportTLS
		r.Security = contract.SecurityTLS
		r.SNI = "rl.test"
		r.PinnedPeerCertSHA256 = pin
	})
	if _, err := e.AddExit(good); err != nil {
		t.Fatalf("add tls exit: %v", err)
	}
	res, err := e.Dial(context.Background(), dialRequest("x-tls", ipDestination(echo.port()), 1))
	if err != nil {
		t.Fatalf("dial tls exit: %v", err)
	}
	defer res.Conn.Close()
	if res.Proof != contract.ProofAccepted {
		t.Fatalf("proof = %q", res.Proof)
	}
	if !echo.waitAccept(5 * time.Second) {
		t.Fatalf("destination was never contacted over the TLS exit")
	}
	if _, err := res.Conn.Write([]byte("tls")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readWithin(res.Conn, 2*time.Second)
	if got.err != nil || string(got.data) != "tls" {
		t.Fatalf("echo = %q, %v", got.data, got.err)
	}

	bad := vlessRecord("x-bad-pin", "127.0.0.1", uint16(exit.port), uuid, func(r *contract.Record) {
		r.Transport = contract.TransportTLS
		r.Security = contract.SecurityTLS
		r.SNI = "rl.test"
		r.PinnedPeerCertSHA256 = strings.Repeat("0", 64)
	})
	if _, err := e.AddExit(bad); err != nil {
		t.Fatalf("add bad-pin exit: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	badRes, err := e.Dial(ctx, dialRequest("x-bad-pin", ipDestination(echo.port()), 1))
	if err != nil {
		if kind := failureKind(t, err); kind != contract.ExitUnreachable {
			t.Fatalf("pin failure at dial: kind = %q", kind)
		}
		return
	}
	defer badRes.Conn.Close()
	got = readWithin(badRes.Conn, 10*time.Second)
	if got.err == errReadTimedOut {
		t.Fatalf("pin failure never surfaced")
	}
	if kind := failureKind(t, got.err); kind != contract.ExitUnreachable {
		t.Fatalf("pin failure kind = %q, want exit_unreachable", kind)
	}
}

// TestSocksRecordWithTransportIsRefused is the regression for a silent
// downgrade: a SOCKS5 record that declares TLS/WS/gRPC/XHTTP used to be dialed
// as plaintext TCP while the exit table still said "tls".
func TestSocksRecordWithTransportIsRefused(t *testing.T) {
	base := func() contract.Record {
		return socksRecord("x-socks", "127.0.0.1", 1080, "", "")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*contract.Record)
	}{
		{"tls transport", func(r *contract.Record) {
			r.Transport = contract.TransportTLS
			r.Security = contract.SecurityTLS
		}},
		{"tls security only", func(r *contract.Record) { r.Security = contract.SecurityTLS }},
		{"ws transport", func(r *contract.Record) { r.Transport = contract.TransportWS }},
		{"pin without transport", func(r *contract.Record) {
			r.PinnedPeerCertSHA256 = strings.Repeat("a", 64)
		}},
		{"sni", func(r *contract.Record) { r.SNI = "rl.test" }},
	} {
		rec := base()
		tc.mutate(&rec)
		expectExcluded(t, rec, contract.Unsupported)
	}
	// The plain shape still starts.
	plain, err := engine.Start(engine.Options{Exits: []contract.Record{base()}, LogLevel: "none"})
	if err != nil {
		t.Fatalf("plain socks record: %v", err)
	}
	bad := vlessRecord("x-bad", "127.0.0.1", 443, strings.Repeat("z", 31), nil)
	if _, err := plain.AddExit(bad); failureKind(t, err) != contract.ImportInvalid {
		t.Fatalf("AddExit with a bad record: kind = %q, want import_invalid", failureKind(t, err))
	}
	_ = plain.Stop(context.Background())
}

// TestRemoveThenReAddExitReusesTheHandler: the pinned core cannot remove a
// handler or accept a duplicate tag, so a restored record must reuse it.
func TestRemoveThenReAddExitReusesTheHandler(t *testing.T) {
	spy := newPeer(t, "accept-silent")
	uuid := randomUUID(t)
	rec := vlessRecord("x-1", "127.0.0.1", spy.port(), uuid, nil)
	e := startEngine(t, 1, rec)

	if !e.RemoveExit("x-1") {
		t.Fatalf("remove exit returned false")
	}
	if _, err := e.Dial(context.Background(), dialRequest("x-1", ipDestination(9999), 1)); failureKind(t, err) != contract.PoolUnavailable {
		t.Fatalf("dial after remove: kind = %q, want pool_unavailable", failureKind(t, err))
	}
	exit, err := e.AddExit(rec)
	if err != nil {
		t.Fatalf("re-add after remove: %v", err)
	}
	if exit.Tag != "rl-x-1" {
		t.Fatalf("restored tag = %q", exit.Tag)
	}
	res, err := e.Dial(context.Background(), dialRequest("x-1", ipDestination(spy.port()), 1))
	if err != nil {
		t.Fatalf("dial restored exit: %v", err)
	}
	defer res.Conn.Close()
	if !spy.waitAccepts(1, 3*time.Second) {
		t.Fatalf("restored exit was never contacted")
	}
}

// TestInvalidRecordFieldsAreRejected keeps record defects out of the "internal"
// kind: they are actionable input errors, not software defects.
func TestInvalidRecordFieldsAreRejected(t *testing.T) {
	uuid := randomUUID(t)
	cases := []struct {
		name   string
		mutate func(*contract.Record)
		kind   contract.ErrorKind
	}{
		{"bad uuid", func(r *contract.Record) { r.Credentials.UserID = strings.Repeat("z", 31) }, contract.ImportInvalid},
		{"bad flow", func(r *contract.Record) { r.Flow = "xtls-rprx-nonesuch" }, contract.ImportInvalid},
		{"bad pin", func(r *contract.Record) {
			r.Transport = contract.TransportTLS
			r.Security = contract.SecurityTLS
			r.PinnedPeerCertSHA256 = "zz"
		}, contract.ImportInvalid},
		{"reserved id", func(r *contract.Record) { r.ID = "blocked" }, contract.ImportInvalid},
		{"line break path", func(r *contract.Record) {
			r.Transport = contract.TransportWS
			r.Path = "/x\r\nHost: evil"
		}, contract.ImportInvalid},
		{"server with a port", func(r *contract.Record) { r.Server = "127.0.0.1:443" }, contract.ImportInvalid},
		{"server with a path", func(r *contract.Record) { r.Server = "example.com/x" }, contract.ImportInvalid},
		{"unknown fingerprint", func(r *contract.Record) {
			r.Transport = contract.TransportTLS
			r.Security = contract.SecurityTLS
			r.SNI = "rl.test"
			r.Fingerprint = "nonesuch"
		}, contract.ImportInvalid},
	}
	for _, tc := range cases {
		rec := vlessRecord("x-1", "127.0.0.1", 443, uuid, tc.mutate)
		expectExcluded(t, rec, tc.kind)
	}

	// Plaintext VLESS/Trojan to a public exit is prohibited by the pinned core;
	// it is a v1 limit, not a malformed record, so it must not be
	// "import_invalid" (which would tell the user to fix a well-formed link).
	for _, server := range []string{"example.com", "8.8.8.8"} {
		expectExcluded(t, vlessRecord("x-pub", server, 443, uuid, nil), contract.Unsupported)
	}
	// A private domain with the same shape is still a valid loopback-style
	// fixture and must start.
	privateExit, err := engine.Start(engine.Options{
		Exits:         []contract.Record{vlessRecord("x-private", "exit.test", 443, uuid, nil)},
		PolicyVersion: 1,
		LogLevel:      "none",
	})
	if err != nil {
		t.Fatalf("plaintext private domain: %v", err)
	}
	_ = privateExit.Stop(context.Background())

	// Trojan flow is a feature the pinned core removed, not a defect.
	trojan := trojanRecord("x-trojan", "127.0.0.1", 443, "pw", func(r *contract.Record) { r.Flow = "xtls-rprx-vision" })
	expectExcluded(t, trojan, contract.Unsupported)

	// The pinned core also accepts a 32-hex id and a short legacy id; rejecting
	// them here would block a whole subscription over one valid record.
	for _, id := range []string{strings.Repeat("a", 32), "legacy-id"} {
		e, err := engine.Start(engine.Options{
			Exits:         []contract.Record{vlessRecord("x-uuid", "127.0.0.1", 443, id, nil)},
			PolicyVersion: 1,
			LogLevel:      "none",
		})
		if err != nil {
			t.Fatalf("user id %q rejected: %v", id, err)
		}
		if err := e.Stop(context.Background()); err != nil {
			t.Fatalf("stop: %v", err)
		}
	}
}

// TestRetiredTagKeepsItsIdentity: after RemoveExit the tag stays reserved, so a
// different record must not quietly inherit the old handler.
func TestRetiredTagKeepsItsIdentity(t *testing.T) {
	spy := newPeer(t, "accept-silent")
	other := newPeer(t, "accept-silent")
	uuid := randomUUID(t)
	rec := vlessRecord("x-1", "127.0.0.1", spy.port(), uuid, nil)
	e := startEngine(t, 1, rec)

	if !e.RemoveExit("x-1") {
		t.Fatalf("remove exit returned false")
	}
	// A failed re-add of the original identity must not consume the
	// reservation: the handler is still the old record's.
	broken := rec
	broken.Flow = "xtls-rprx-nonesuch"
	if _, err := e.AddExit(broken); failureKind(t, err) != contract.ImportInvalid {
		t.Fatalf("broken restore: kind = %q, want import_invalid", failureKind(t, err))
	}
	changed := vlessRecord("x-1", "127.0.0.1", other.port(), randomUUID(t), func(r *contract.Record) {
		r.DedupeKey = "sha256:changed-settings"
	})
	if _, err := e.AddExit(changed); failureKind(t, err) != contract.Internal {
		t.Fatalf("changed record after removal: kind = %q, want internal", failureKind(t, err))
	}
	time.Sleep(100 * time.Millisecond)
	if spy.accepts() != 0 || other.accepts() != 0 {
		t.Fatalf("a rejected record contacted an exit: old=%d new=%d", spy.accepts(), other.accepts())
	}
	// Restoring the original record still works.
	if _, err := e.AddExit(rec); err != nil {
		t.Fatalf("restore original record: %v", err)
	}
}

// TestWarmTransportsSurviveStop documents that a warm transport is not part of
// the engine's close path; a mode change must show it rather than claim it is
// gone.
func TestWarmTransportsSurviveStop(t *testing.T) {
	uuid := randomUUID(t)
	spy := newPeer(t, "accept-silent")
	e, err := engine.Start(engine.Options{
		Exits: []contract.Record{vlessRecord("x-grpc", "127.0.0.1", spy.port(), uuid, func(r *contract.Record) {
			r.Transport = contract.TransportGRPC
			r.Security = contract.SecurityTLS
			r.GRPCService = "rl"
			r.SNI = "rl.test"
		})},
		PolicyVersion: 1,
		LogLevel:      "none",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res, err := e.Dial(context.Background(), dialRequest("x-grpc", ipDestination(9111), 1))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = res.Conn.Close()
	if err := e.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	warm := e.WarmTransports()
	if len(warm) != 1 || warm[0].Exit != "x-grpc" {
		t.Fatalf("warm transports after stop = %+v, want the surviving gRPC exit", warm)
	}
}
