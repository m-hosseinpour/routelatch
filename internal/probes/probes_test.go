package probes

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"routelatch/internal/contract"
)

func rec(id string, transport contract.Transport, security contract.Security) contract.Record {
	return contract.Record{
		ID: contract.RecordID(id), Protocol: contract.ProtocolVLESS,
		Server: "127.0.0.1", Port: 443, Transport: transport, Security: security,
		Status: contract.RecordUnknown,
	}
}

type trackingConn struct {
	net.Conn
	writes int64
	reads  int64
}

func (c *trackingConn) Write(p []byte) (int, error) {
	atomic.AddInt64(&c.writes, 1)
	return c.Conn.Write(p)
}

func (c *trackingConn) Read(p []byte) (int, error) {
	atomic.AddInt64(&c.reads, 1)
	return c.Conn.Read(p)
}

type fakePool struct {
	fn func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error)
}

func (f fakePool) DialPool(ctx context.Context, req contract.PoolDialRequest) (contract.PoolDialResult, error) {
	return f.fn(ctx, req)
}

func baseOptions(records []contract.Record, dial func(context.Context, string, string) (net.Conn, error)) Options {
	byID := make(map[contract.ExitID]contract.Record, len(records))
	for _, r := range records {
		byID[contract.ExitID(r.ID)] = r
	}
	return Options{
		Record:     func(id contract.ExitID) (contract.Record, bool) { r, ok := byID[id]; return r, ok },
		AllRecords: func() []contract.Record { return records },
		Mode:       func() contract.Mode { return contract.ModeTargeted },
		Clock:      time.Now,
		Dial:       dial,
	}
}

func TestTransportProbeSuccessSendsNoData(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	tc := &trackingConn{Conn: client}
	e := NewEngine(baseOptions([]contract.Record{rec("x-a", contract.TransportRaw, contract.SecurityNone)},
		func(context.Context, string, string) (net.Conn, error) { return tc, nil }))
	defer e.Close()

	m, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{
		Kind: contract.ProbeTransport, Exit: "x-a", Trigger: contract.TriggerManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Result.Outcome != contract.OutcomeSuccess {
		t.Fatalf("outcome=%s failure=%s", m.Result.Outcome, m.Result.Failure)
	}
	if atomic.LoadInt64(&tc.writes) != 0 {
		t.Fatal("transport probe wrote application data")
	}
	if m.Result.Eligible {
		t.Fatal("transport probe must not be proposal-eligible")
	}
}

func TestTransportProbeFailureAttribution(t *testing.T) {
	records := []contract.Record{rec("x-a", contract.TransportRaw, contract.SecurityNone)}
	dialErr := func(context.Context, string, string) (net.Conn, error) { return nil, syscall.ECONNREFUSED }
	e := NewEngine(baseOptions(records, dialErr))
	defer e.Close()
	m, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{Kind: contract.ProbeTransport, Exit: "x-a", Trigger: contract.TriggerRotate})
	if err != nil {
		t.Fatal(err)
	}
	if m.Result.Outcome != contract.OutcomeRefused || m.Result.Failure != contract.ExitUnreachable || !m.Attributable {
		t.Fatalf("measurement=%+v", m)
	}

	// The same failure while the machine's own network is down is not
	// attributable to the exit.
	opts := baseOptions(records, dialErr)
	opts.LocalControl = func(context.Context) error { return errors.New("network unreachable") }
	e2 := NewEngine(opts)
	defer e2.Close()
	m2, err := e2.RunMeasurement(context.Background(), contract.ProbeRequest{Kind: contract.ProbeTransport, Exit: "x-a", Trigger: contract.TriggerRotate})
	if err != nil {
		t.Fatal(err)
	}
	if !m2.LocalOutage || m2.Attributable {
		t.Fatalf("local outage not separated: %+v", m2)
	}
}

func TestTransportTLSUsesVerificationAndPin(t *testing.T) {
	records := []contract.Record{rec("x-a", contract.TransportTLS, contract.SecurityTLS)}
	records[0].SNI = "exit.example.com"
	records[0].PinnedPeerCertSHA256 = strings.Repeat("ab", 32)
	var seen *tls.Config
	opts := baseOptions(records, func(context.Context, string, string) (net.Conn, error) {
		client, _ := net.Pipe()
		return client, nil
	})
	opts.TLSHandshake = func(_ context.Context, conn net.Conn, cfg *tls.Config) (net.Conn, error) {
		seen = cfg
		return conn, nil
	}
	e := NewEngine(opts)
	defer e.Close()
	m, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{Kind: contract.ProbeTransport, Exit: "x-a", Trigger: contract.TriggerStartup})
	if err != nil || m.Result.Outcome != contract.OutcomeSuccess {
		t.Fatalf("m=%+v err=%v", m, err)
	}
	// With a pin, Go's default chain check is replaced by the explicit pinned
	// verifier; verification is never skipped (VerifyConnection is mandatory).
	if seen == nil || !seen.InsecureSkipVerify || seen.ServerName != "exit.example.com" || seen.VerifyConnection == nil {
		t.Fatalf("tls config=%+v", seen)
	}
	plain := tlsConfigFor(rec("x-b", contract.TransportTLS, contract.SecurityTLS))
	if plain.InsecureSkipVerify || plain.VerifyConnection != nil {
		t.Fatal("a record without a pin must use standard verification")
	}
}

func TestPinnedCertificateVerification(t *testing.T) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)

	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "exit.example.com"},
		DNSNames:  []string{"exit.example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(leafDER)

	// An unrelated CA is presented but did not sign the leaf; pinning it must
	// not authenticate the chain.
	otherKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "Other CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}
	otherDER, _ := x509.CreateCertificate(rand.Reader, otherTemplate, otherTemplate, &otherKey.PublicKey, otherKey)
	other, _ := x509.ParseCertificate(otherDER)

	leafHash := sha256.Sum256(leaf.Raw)
	caHash := sha256.Sum256(ca.Raw)
	otherHash := sha256.Sum256(other.Raw)
	cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf, other, ca}}

	if err := verifyPinned(cs, [][]byte{leafHash[:]}, nil, "exit.example.com"); err != nil {
		t.Fatalf("leaf pin: %v", err)
	}
	if err := verifyPinned(cs, [][]byte{caHash[:]}, nil, "exit.example.com"); err != nil {
		t.Fatalf("CA pin: %v", err)
	}
	if err := verifyPinned(cs, [][]byte{otherHash[:]}, nil, "exit.example.com"); err == nil {
		t.Fatal("an unrelated presented CA satisfied the pin")
	}
	if err := verifyPinned(cs, [][]byte{caHash[:]}, []string{"wrong.example.com"}, "exit.example.com"); err == nil {
		t.Fatal("name constraint was not enforced")
	}
}

func TestTransportTLSFailureCools(t *testing.T) {
	records := []contract.Record{rec("x-a", contract.TransportTLS, contract.SecurityTLS)}
	opts := baseOptions(records, func(context.Context, string, string) (net.Conn, error) {
		client, _ := net.Pipe()
		return client, nil
	})
	opts.TLSHandshake = func(context.Context, net.Conn, *tls.Config) (net.Conn, error) {
		return nil, errors.New("bad certificate")
	}
	e := NewEngine(opts)
	defer e.Close()
	m, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{Kind: contract.ProbeTransport, Exit: "x-a", Trigger: contract.TriggerRotate})
	if err != nil {
		t.Fatal(err)
	}
	if m.Result.Outcome != contract.OutcomeTLSFailure || m.Result.Failure != contract.ExitUnreachable || !m.Attributable {
		t.Fatalf("m=%+v", m)
	}
}

func TestEgressProbeFirstByte(t *testing.T) {
	records := []contract.Record{rec("x-a", contract.TransportRaw, contract.SecurityNone)}
	dst := contract.Destination{Kind: contract.DestDomain, Host: "target.example.com", Port: 443}
	newEngine := func(fn func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error)) *Engine {
		opts := baseOptions(records, nil)
		opts.Pool = fakePool{fn: fn}
		opts.FirstByte = 50 * time.Millisecond
		return NewEngine(opts)
	}
	req := contract.ProbeRequest{Kind: contract.ProbeEgress, Exit: "x-a", Trigger: contract.TriggerManual, Destination: dst, PolicyVersion: 1}

	t.Run("bytes", func(t *testing.T) {
		client, server := net.Pipe()
		tc := &trackingConn{Conn: client}
		go func() { _, _ = server.Write([]byte("x")); server.Close() }()
		e := newEngine(func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			return contract.PoolDialResult{Conn: tc, Exit: "x-a", Proof: contract.ProofAccepted}, nil
		})
		defer e.Close()
		m, err := e.RunMeasurement(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if m.Result.Outcome != contract.OutcomeSuccess || m.Result.BytesDown != 1 || !m.Attributable {
			t.Fatalf("m=%+v", m)
		}
		if atomic.LoadInt64(&tc.writes) != 0 {
			t.Fatal("egress probe wrote user data")
		}
	})

	t.Run("eof", func(t *testing.T) {
		client, server := net.Pipe()
		server.Close()
		e := newEngine(func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			return contract.PoolDialResult{Conn: client, Exit: "x-a", Proof: contract.ProofAccepted}, nil
		})
		defer e.Close()
		m, err := e.RunMeasurement(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if m.Result.Outcome != contract.OutcomeUnconfirmed || m.Result.Failure != contract.DestinationUnconfirmed || m.Result.Eligible || m.Attributable {
			t.Fatalf("m=%+v", m)
		}
	})

	t.Run("first byte timeout", func(t *testing.T) {
		client, server := net.Pipe()
		defer server.Close()
		e := newEngine(func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			return contract.PoolDialResult{Conn: client, Exit: "x-a", Proof: contract.ProofAccepted}, nil
		})
		defer e.Close()
		m, err := e.RunMeasurement(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if m.Result.Outcome != contract.OutcomeTimeout || m.Attributable || m.Phase != PhaseFirstByte {
			t.Fatalf("m=%+v", m)
		}
	})

	t.Run("wrong exit rejected", func(t *testing.T) {
		client, server := net.Pipe()
		defer server.Close()
		e := newEngine(func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			return contract.PoolDialResult{Conn: client, Exit: "x-other", Proof: contract.ProofAccepted}, nil
		})
		defer e.Close()
		m, err := e.RunMeasurement(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if m.Result.Outcome != contract.OutcomeRejected || m.Result.Failure != contract.Internal || m.Attributable {
			t.Fatalf("m=%+v", m)
		}
	})

	t.Run("pool transport failure", func(t *testing.T) {
		e := newEngine(func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			return contract.PoolDialResult{}, &contract.Failure{Kind: contract.ExitUnreachable, Message: "dead"}
		})
		defer e.Close()
		m, err := e.RunMeasurement(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if m.Result.Failure != contract.ExitUnreachable || !m.Attributable {
			t.Fatalf("m=%+v", m)
		}
	})

	t.Run("destination unconfirmed", func(t *testing.T) {
		e := newEngine(func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			return contract.PoolDialResult{}, &contract.Failure{Kind: contract.DestinationUnconfirmed, Message: "no ack"}
		})
		defer e.Close()
		m, _ := e.RunMeasurement(context.Background(), req)
		if m.Result.Outcome != contract.OutcomeUnconfirmed || m.Attributable {
			t.Fatalf("m=%+v", m)
		}
	})
}

func TestEgressRequiresPoolAndTarget(t *testing.T) {
	records := []contract.Record{rec("x-a", contract.TransportRaw, contract.SecurityNone)}
	opts := baseOptions(records, nil)
	e := NewEngine(opts)
	defer e.Close()
	if _, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{
		Kind: contract.ProbeEgress, Trigger: contract.TriggerManual, Exit: "x-a",
		Destination: contract.Destination{Kind: contract.DestDomain, Host: "target.example.com", Port: 443}, PolicyVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{
		Kind: contract.ProbeEgress, Trigger: contract.TriggerRotate, Exit: "x-a",
		Destination: contract.Destination{Kind: contract.DestDomain, Host: "other.example.com", Port: 443}, PolicyVersion: 1,
	}); err == nil {
		t.Fatal("automatic egress to an unconfigured target must be rejected")
	}
}

func TestDirectProbes(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	opts := baseOptions(nil, func(context.Context, string, string) (net.Conn, error) { return client, nil })
	e := NewEngine(opts)
	defer e.Close()

	m, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{
		Kind: contract.ProbeDirectTCP, Trigger: contract.TriggerManual,
		Destination: contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
	})
	if err != nil || m.Result.Outcome != contract.OutcomeSuccess {
		t.Fatalf("direct tcp m=%+v err=%v", m, err)
	}
	if _, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{
		Kind: contract.ProbeDirectTLS, Trigger: contract.TriggerManual,
		Destination: contract.Destination{Kind: contract.DestIP, Host: "", Port: 443},
	}); err == nil {
		t.Fatal("direct TLS on an IP literal must be rejected")
	}
}

func TestDirectTLSSuccessAndFailure(t *testing.T) {
	var seen *tls.Config
	opts := baseOptions(nil, func(context.Context, string, string) (net.Conn, error) {
		client, _ := net.Pipe()
		return client, nil
	})
	opts.TLSHandshake = func(_ context.Context, conn net.Conn, cfg *tls.Config) (net.Conn, error) {
		seen = cfg
		return conn, nil
	}
	e := NewEngine(opts)
	defer e.Close()
	m, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{
		Kind: contract.ProbeDirectTLS, Trigger: contract.TriggerManual,
		Destination: contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
	})
	if err != nil || m.Result.Outcome != contract.OutcomeSuccess {
		t.Fatalf("m=%+v err=%v", m, err)
	}
	if seen == nil || seen.InsecureSkipVerify || seen.ServerName != "example.com" {
		t.Fatalf("tls config=%+v", seen)
	}

	opts.TLSHandshake = func(context.Context, net.Conn, *tls.Config) (net.Conn, error) {
		return nil, errors.New("bad certificate")
	}
	e2 := NewEngine(opts)
	defer e2.Close()
	m, err = e2.RunMeasurement(context.Background(), contract.ProbeRequest{
		Kind: contract.ProbeDirectTLS, Trigger: contract.TriggerManual,
		Destination: contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Result.Outcome != contract.OutcomeTLSFailure || m.Result.Failure != contract.TLSFailure || m.Result.Eligible {
		t.Fatalf("tls failure m=%+v", m)
	}
}

func TestDirectFailureEligibility(t *testing.T) {
	opts := baseOptions(nil, func(context.Context, string, string) (net.Conn, error) {
		return nil, syscall.ECONNREFUSED
	})
	e := NewEngine(opts)
	defer e.Close()
	m, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{
		Kind: contract.ProbeDirectTCP, Trigger: contract.TriggerFailure,
		Destination: contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Result.Failure != contract.ConnectRefused || !m.Result.Eligible {
		t.Fatalf("m=%+v", m)
	}
	if m.Attributable {
		t.Fatal("direct failure must not cool a pool exit")
	}
}

func TestModePolicy(t *testing.T) {
	records := []contract.Record{rec("x-a", contract.TransportRaw, contract.SecurityNone)}
	mode := contract.ModeTargeted
	opts := baseOptions(records, func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		server.Close()
		return client, nil
	})
	opts.Mode = func() contract.Mode { return mode }
	opts.DiagnosticsEnabled = func() bool { return false }
	e := NewEngine(opts)
	defer e.Close()

	mode = contract.ModeDirectAll
	if err := e.Enqueue(context.Background(), contract.ProbeRequest{Kind: contract.ProbeTransport, Exit: "x-a", Trigger: contract.TriggerStartup}); err == nil {
		t.Fatal("automatic probe must not run in all-direct")
	}
	if _, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{Kind: contract.ProbeTransport, Exit: "x-a", Trigger: contract.TriggerManual}); err != nil {
		t.Fatalf("manual probe in all-direct: %v", err)
	}
	mode = contract.ModeStopped
	if _, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{Kind: contract.ProbeTransport, Exit: "x-a", Trigger: contract.TriggerManual}); err == nil {
		t.Fatal("probe must not run while stopped")
	}
	mode = contract.ModeTargeted
	if _, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{
		Kind: contract.ProbeDirectTCP, Trigger: contract.TriggerFailure,
		Destination: contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
	}); err == nil {
		t.Fatal("failure-triggered probe must respect disabled diagnostics")
	}
	if _, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{
		Kind: contract.ProbeEgress, Trigger: contract.TriggerStartup,
		Destination: contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
	}); err == nil {
		t.Fatal("egress startup trigger is outside the table")
	}
}

func TestQueueCancellationAndDrop(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	blockingDial := func(context.Context, string, string) (net.Conn, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		client, server := net.Pipe()
		server.Close()
		return client, nil
	}
	opts := baseOptions(nil, blockingDial)
	var sunk int32
	var sinkMu sync.Mutex
	opts.Sink = func(contract.ProbeResult) { sinkMu.Lock(); sunk++; sinkMu.Unlock() }
	opts.QueueLimit = 8
	e := NewEngine(opts)
	defer e.Close()

	running := make(chan struct{})
	go func() {
		_, _ = e.RunMeasurement(context.Background(), contract.ProbeRequest{
			Kind: contract.ProbeDirectTCP, Trigger: contract.TriggerManual,
			Destination: contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
		})
		close(running)
	}()
	<-started
	for i := 0; i < 3; i++ {
		if err := e.Enqueue(context.Background(), contract.ProbeRequest{
			Kind: contract.ProbeDirectTCP, Trigger: contract.TriggerManual,
			Destination: contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
		}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := e.CancelQueued(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("canceled=%d err=%v", n, err)
	}
	close(release)
	<-running
	time.Sleep(20 * time.Millisecond)
	sinkMu.Lock()
	defer sinkMu.Unlock()
	if sunk != 0 {
		t.Fatalf("canceled probes were delivered: %d", sunk)
	}
}

func TestQueueFullDropsOldestDiagnostic(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	blockingDial := func(context.Context, string, string) (net.Conn, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		client, server := net.Pipe()
		server.Close()
		return client, nil
	}
	opts := baseOptions(nil, blockingDial)
	opts.QueueLimit = 1
	e := NewEngine(opts)
	defer e.Close()
	running := make(chan struct{})
	go func() {
		_, _ = e.RunMeasurement(context.Background(), contract.ProbeRequest{
			Kind: contract.ProbeDirectTCP, Trigger: contract.TriggerManual,
			Destination: contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
		})
		close(running)
	}()
	<-started
	req := contract.ProbeRequest{
		Kind: contract.ProbeDirectTCP, Trigger: contract.TriggerManual,
		Destination: contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
	}
	if err := e.Enqueue(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := e.Enqueue(context.Background(), req); err != nil {
		t.Fatalf("a diagnostic should be dropped, not fail: %v", err)
	}
	if n, _ := e.CancelQueued(context.Background()); n != 1 {
		t.Fatalf("queued after drop=%d", n)
	}
	close(release)
	<-running
}

func TestRotationPolicy(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	records := []contract.Record{
		rec("x-a", contract.TransportRaw, contract.SecurityNone),
		rec("x-b", contract.TransportRaw, contract.SecurityNone),
		rec("x-c", contract.TransportRaw, contract.SecurityNone),
	}
	mode := contract.ModeDirectAll
	opts := baseOptions(records, nil)
	opts.Mode = func() contract.Mode { return mode }
	opts.Clock = func() time.Time { return now }
	e := NewEngine(opts)
	defer e.Close()
	if batch := e.Rotate(now); batch != nil {
		t.Fatal("rotation in all-direct")
	}
	mode = contract.ModeTargeted
	batch := e.Rotate(now)
	if len(batch) != 3 {
		t.Fatalf("batch=%d", len(batch))
	}
	if batch := e.Rotate(now.Add(contract.HealthProbeInterval - time.Second)); batch != nil {
		t.Fatal("rotation before the interval")
	}
	batch = e.Rotate(now.Add(contract.HealthProbeInterval))
	if len(batch) != 3 || batch[0].Kind != contract.ProbeTransport {
		t.Fatalf("second batch=%+v", batch)
	}
	mode = contract.ModeStopped
	if batch := e.Rotate(now.Add(2 * contract.HealthProbeInterval)); batch != nil {
		t.Fatal("rotation while stopped")
	}
}

func TestProbeCooldownAndInflight(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	opts := baseOptions(nil, func(context.Context, string, string) (net.Conn, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		client, server := net.Pipe()
		server.Close()
		return client, nil
	})
	opts.Clock = func() time.Time { return now }
	e := NewEngine(opts)
	defer e.Close()
	req := contract.ProbeRequest{
		Kind: contract.ProbeDirectTCP, Trigger: contract.TriggerManual,
		Destination: contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
	}
	done := make(chan struct{})
	go func() {
		_, _ = e.RunMeasurement(context.Background(), req)
		close(done)
	}()
	<-started
	if _, err := e.RunMeasurement(context.Background(), req); !errors.Is(err, ErrProbeInFlight) {
		t.Fatalf("second inflight probe: %v", err)
	}
	close(release)
	<-done
	if _, err := e.RunMeasurement(context.Background(), req); !errors.Is(err, ErrProbeCooldown) {
		t.Fatalf("cooldown: %v", err)
	}
}

func TestRunRecordsRetention(t *testing.T) {
	client, server := net.Pipe()
	server.Close()
	opts := baseOptions([]contract.Record{rec("x-a", contract.TransportRaw, contract.SecurityNone)},
		func(context.Context, string, string) (net.Conn, error) { return client, nil })
	e := NewEngine(opts)
	defer e.Close()
	if _, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{Kind: contract.ProbeTransport, Exit: "x-a", Trigger: contract.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	samples := e.Retention().Samples("x-a")
	if len(samples) != 1 || samples[0].Kind != contract.ProbeTransport || samples[0].Outcome != contract.OutcomeSuccess {
		t.Fatalf("samples=%+v", samples)
	}
}

func TestRotationIncludesConfiguredEgressTarget(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	records := []contract.Record{rec("x-a", contract.TransportRaw, contract.SecurityNone)}
	opts := baseOptions(records, nil)
	opts.Targets = []string{"target.example.com:443"}
	opts.Clock = func() time.Time { return now }
	e := NewEngine(opts)
	defer e.Close()
	batch := e.Rotate(now)
	sawEgress := false
	for _, req := range batch {
		if req.Kind == contract.ProbeEgress && req.Destination.Host == "target.example.com" && req.Trigger == contract.TriggerRotate && req.Exit != "" {
			sawEgress = true
		}
	}
	if !sawEgress {
		t.Fatalf("no egress rotation: %+v", batch)
	}
}

func TestRetentionBounds(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	r := NewRetention(func() time.Time { return now })
	for i := 0; i < 250; i++ {
		r.Add(Sample{Exit: "x-a", At: now.Add(-time.Duration(249-i) * time.Second), Outcome: contract.OutcomeSuccess})
	}
	samples := r.Samples("x-a")
	if len(samples) != contract.HealthSamplesPerExit {
		t.Fatalf("samples=%d", len(samples))
	}
	if samples[len(samples)-1].At != now {
		t.Fatal("newest sample missing")
	}
	r.Add(Sample{Exit: "x-a", At: now.Add(-8 * 24 * time.Hour), Outcome: contract.OutcomeSuccess})
	if got := r.Samples("x-a"); len(got) != contract.HealthSamplesPerExit {
		t.Fatalf("expired sample not pruned: %d", len(got))
	}
	// A fresh old-only exit is pruned entirely.
	r.Add(Sample{Exit: "x-b", At: now.Add(-8 * 24 * time.Hour)})
	if got := r.Samples("x-b"); len(got) != 0 {
		t.Fatalf("old-only samples=%d", len(got))
	}
}

func TestUnsupportedRecordNeverDialed(t *testing.T) {
	record := rec("x-a", contract.TransportRaw, contract.SecurityNone)
	record.Status = contract.RecordUnsupported
	called := false
	opts := baseOptions([]contract.Record{record}, func(context.Context, string, string) (net.Conn, error) {
		called = true
		return nil, io.EOF
	})
	e := NewEngine(opts)
	defer e.Close()
	m, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{Kind: contract.ProbeTransport, Exit: "x-a", Trigger: contract.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if called || m.Result.Outcome != contract.OutcomeRejected || m.Result.Failure != contract.Unsupported {
		t.Fatalf("called=%t m=%+v", called, m)
	}
}

func TestMissingRecordIsRejected(t *testing.T) {
	opts := baseOptions(nil, nil)
	e := NewEngine(opts)
	defer e.Close()
	m, err := e.RunMeasurement(context.Background(), contract.ProbeRequest{Kind: contract.ProbeTransport, Exit: "x-missing", Trigger: contract.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if m.Result.Outcome != contract.OutcomeRejected || m.Result.Failure != contract.Unsupported {
		t.Fatalf("m=%+v", m)
	}
}

func TestProbeKeySeparatesEgressTargets(t *testing.T) {
	a := contract.ProbeRequest{Kind: contract.ProbeEgress, Exit: "x-a", Destination: contract.Destination{Kind: contract.DestDomain, Host: "a.example.com", Port: 443}}
	b := a
	b.Destination.Host = "b.example.com"
	if probeKey(a) == probeKey(b) {
		t.Fatal("two egress targets on one exit must not share an in-flight key")
	}
	if probeKey(a) == probeKey(contract.ProbeRequest{Kind: contract.ProbeEgress, Exit: "x-b", Destination: a.Destination}) {
		t.Fatal("two exits must not share an in-flight key")
	}
}

func TestParseTarget(t *testing.T) {
	for _, raw := range []string{"example.com:443", "203.0.113.8:443", "[2001:db8::1]:8443"} {
		if _, ok := ParseTarget(raw); !ok {
			t.Fatalf("target %q rejected", raw)
		}
	}
	for _, raw := range []string{"example.com", "example.com:0", "a b:443", ":443"} {
		if _, ok := ParseTarget(raw); ok {
			t.Fatalf("target %q accepted", raw)
		}
	}
}
