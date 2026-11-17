package probes

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"routelatch/internal/contract"
)

func (e *Engine) measure(ctx context.Context, req contract.ProbeRequest, start time.Time) Measurement {
	switch req.Kind {
	case contract.ProbeTransport:
		return e.measureTransport(ctx, req, start)
	case contract.ProbeEgress:
		return e.measureEgress(ctx, req, start)
	case contract.ProbeDirectTCP:
		return e.measureDirectTCP(ctx, req, start)
	case contract.ProbeDirectTLS:
		return e.measureDirectTLS(ctx, req, start)
	default:
		return Measurement{Result: contract.ProbeResult{
			Kind: req.Kind, Trigger: req.Trigger, Exit: req.Exit,
			Destination: req.Destination, StartedAt: start,
			Outcome: contract.OutcomeRejected, Failure: contract.Unsupported,
			Duration: e.opts.Clock().Sub(start),
		}}
	}
}

func (e *Engine) measureTransport(ctx context.Context, req contract.ProbeRequest, start time.Time) Measurement {
	m := Measurement{Result: contract.ProbeResult{
		Kind: req.Kind, Trigger: req.Trigger, Exit: req.Exit,
		Destination: req.Destination, StartedAt: start,
	}}
	rec, err := e.record(req.Exit)
	if err != nil {
		m.Result.Outcome = contract.OutcomeRejected
		m.Result.Failure = contract.Unsupported
		m.Result.Duration = e.opts.Clock().Sub(start)
		return m
	}
	// A protocol v1 cannot dial is never probed, including a legacy unknown
	// record for http or an unknown scheme (pool.EffectiveStatus derives it as
	// unsupported). An empty protocol keeps its previous handling.
	if rec.Status == contract.RecordInvalid || rec.Status == contract.RecordUnsupported ||
		(rec.Protocol != "" && !rec.Protocol.Supported()) ||
		(rec.InsecureTLS && rec.PinnedPeerCertSHA256 == "") ||
		rec.Security == contract.SecurityReality {
		m.Result.Outcome = contract.OutcomeRejected
		m.Result.Failure = contract.Unsupported
		m.Result.Duration = e.opts.Clock().Sub(start)
		return m
	}
	address := net.JoinHostPort(rec.Server, fmt.Sprintf("%d", rec.Port))
	m.Phase = PhaseDial
	conn, err := e.opts.Dial(ctx, "tcp", address)
	if err != nil {
		m.Result.Outcome, m.Result.Failure = classifyDialError(err, true)
		m.Result.Duration = e.opts.Clock().Sub(start)
		return e.attribute(ctx, m)
	}
	defer conn.Close()
	if rec.Security == contract.SecurityTLS {
		m.Phase = PhaseHandshake
		cfg := tlsConfigFor(rec)
		tlsConn, err := e.handshake(ctx, conn, cfg)
		if err != nil {
			m.Result.Outcome = contract.OutcomeTLSFailure
			m.Result.Failure = contract.ExitUnreachable
			if errors.Is(err, context.DeadlineExceeded) {
				m.Result.Outcome, m.Result.Failure = contract.OutcomeTimeout, contract.Timeout
			}
			if errors.Is(err, context.Canceled) {
				m.Result.Outcome, m.Result.Failure = contract.OutcomeCanceled, contract.Canceled
			}
			m.Result.Duration = e.opts.Clock().Sub(start)
			return e.attribute(ctx, m)
		}
		_ = tlsConn.Close()
	} else {
		_ = conn.Close()
	}
	m.Phase = PhaseNone
	m.Result.Outcome = contract.OutcomeSuccess
	m.Result.Duration = e.opts.Clock().Sub(start)
	m.Attributable = rec.Status != contract.RecordUnsupported
	return m
}

func (e *Engine) measureEgress(ctx context.Context, req contract.ProbeRequest, start time.Time) Measurement {
	m := Measurement{Result: contract.ProbeResult{
		Kind: req.Kind, Trigger: req.Trigger, Exit: req.Exit,
		Destination: req.Destination, StartedAt: start,
	}}
	if e.opts.Pool == nil {
		m.Result.Outcome = contract.OutcomeRejected
		m.Result.Failure = contract.Internal
		m.Result.Duration = e.opts.Clock().Sub(start)
		return m
	}
	version := req.PolicyVersion
	if version == 0 && e.opts.PolicyVersion != nil {
		version = e.opts.PolicyVersion()
	}
	m.Phase = PhaseDial
	dialResult, err := e.opts.Pool.DialPool(ctx, contract.PoolDialRequest{
		Destination: req.Destination, PolicyVersion: version, Exit: req.Exit,
	})
	if err != nil {
		m.Result.Outcome, m.Result.Failure = outcomeForPoolError(err)
		m.Result.Duration = e.opts.Clock().Sub(start)
		return e.attribute(ctx, m)
	}
	conn := dialResult.Conn
	if conn == nil {
		m.Result.Outcome = contract.OutcomeRejected
		m.Result.Failure = contract.Internal
		m.Result.Duration = e.opts.Clock().Sub(start)
		return m
	}
	if req.Exit != "" && dialResult.Exit != "" && dialResult.Exit != req.Exit {
		// A dialer may not substitute a different exit. Never attribute this
		// stream to the requested exit.
		_ = conn.Close()
		m.Result.Outcome = contract.OutcomeRejected
		m.Result.Failure = contract.Internal
		m.Result.Duration = e.opts.Clock().Sub(start)
		return m
	}
	if dialResult.Exit != "" {
		m.Result.Exit = dialResult.Exit
	}
	defer conn.Close()
	m.Phase = PhaseFirstByte
	_ = conn.SetReadDeadline(time.Now().Add(e.opts.FirstByte))
	var buf [1]byte
	n, readErr := conn.Read(buf[:])
	m.Result.Duration = e.opts.Clock().Sub(start)
	if n > 0 {
		m.Result.Outcome = contract.OutcomeSuccess
		m.Result.BytesDown = int64(n)
		m.Attributable = true
		m.Phase = PhaseNone
		return m
	}
	var netErr net.Error
	switch {
	case readErr == nil || errors.Is(readErr, io.EOF):
		m.Result.Outcome = contract.OutcomeUnconfirmed
		m.Result.Failure = contract.DestinationUnconfirmed
		m.Attributable = false
	case errors.As(readErr, &netErr) && netErr.Timeout():
		// The contract attributes only a dial-phase timeout to the exit. A
		// first-byte stall is recorded without cooling (R-10, §6.2).
		m.Result.Outcome = contract.OutcomeTimeout
		m.Result.Failure = contract.Timeout
		m.Attributable = false
	case errors.Is(readErr, context.Canceled):
		m.Result.Outcome = contract.OutcomeCanceled
		m.Result.Failure = contract.Canceled
		m.Attributable = false
	default:
		m.Result.Outcome = contract.OutcomeUnconfirmed
		m.Result.Failure = contract.DestinationUnconfirmed
		m.Attributable = false
	}
	return m
}

func (e *Engine) measureDirectTCP(ctx context.Context, req contract.ProbeRequest, start time.Time) Measurement {
	m := Measurement{Result: contract.ProbeResult{
		Kind: req.Kind, Trigger: req.Trigger, Exit: req.Exit,
		Destination: req.Destination, StartedAt: start,
	}}
	m.Phase = PhaseDial
	address := destinationAddress(req.Destination)
	conn, err := e.opts.Dial(ctx, "tcp", address)
	m.Result.Duration = e.opts.Clock().Sub(start)
	if err != nil {
		m.Result.Outcome, m.Result.Failure = classifyDirectError(err)
		m.Result.Eligible = directEvidenceEligible(m.Result.Failure)
		return e.attribute(ctx, m)
	}
	_ = conn.Close()
	m.Phase = PhaseNone
	m.Result.Outcome = contract.OutcomeSuccess
	return m
}

func (e *Engine) measureDirectTLS(ctx context.Context, req contract.ProbeRequest, start time.Time) Measurement {
	m := Measurement{Result: contract.ProbeResult{
		Kind: req.Kind, Trigger: req.Trigger, Exit: req.Exit,
		Destination: req.Destination, StartedAt: start,
	}}
	m.Phase = PhaseDial
	rawConn, err := e.opts.Dial(ctx, "tcp", destinationAddress(req.Destination))
	m.Result.Duration = e.opts.Clock().Sub(start)
	if err != nil {
		m.Result.Outcome, m.Result.Failure = classifyDirectError(err)
		m.Result.Eligible = directEvidenceEligible(m.Result.Failure)
		return e.attribute(ctx, m)
	}
	defer rawConn.Close()
	m.Phase = PhaseHandshake
	cfg := &tls.Config{ServerName: req.Destination.Host, MinVersion: tls.VersionTLS12}
	tlsConn, err := e.handshake(ctx, rawConn, cfg)
	m.Result.Duration = e.opts.Clock().Sub(start)
	if err != nil {
		m.Result.Outcome, m.Result.Failure = contract.OutcomeTLSFailure, contract.TLSFailure
		if errors.Is(err, context.DeadlineExceeded) {
			m.Result.Outcome, m.Result.Failure = contract.OutcomeTimeout, contract.Timeout
		}
		return e.attribute(ctx, m)
	}
	_ = tlsConn.Close()
	m.Phase = PhaseNone
	m.Result.Outcome = contract.OutcomeSuccess
	return m
}

func (e *Engine) record(exit contract.ExitID) (contract.Record, error) {
	if e.opts.Record == nil {
		return contract.Record{}, errors.New("no record source")
	}
	rec, ok := e.opts.Record(exit)
	if !ok {
		return contract.Record{}, fmt.Errorf("exit %s not found", exit)
	}
	return rec, nil
}

func (e *Engine) handshake(ctx context.Context, conn net.Conn, cfg *tls.Config) (net.Conn, error) {
	if e.opts.TLSHandshake != nil {
		return e.opts.TLSHandshake(ctx, conn, cfg)
	}
	tlsConn := tls.Client(conn, cfg)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return tlsConn, nil
}

// tlsConfigFor applies the record's SNI, ALPN, pin and name constraint while
// keeping verification on. With a pin, Go's default chain check is replaced by
// an equivalent explicit check so a CA pin can substitute the pinned CA as the
// trust anchor, matching the pinned core's semantics (tls/config.go verifyChain).
// A leaf pin is an exact-certificate constraint; it is never a blanket
// skip-verify switch.
func tlsConfigFor(rec contract.Record) *tls.Config {
	serverName := rec.SNI
	if serverName == "" {
		serverName = rec.Server
	}
	cfg := &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}
	if len(rec.ALPN) > 0 {
		cfg.NextProtos = append([]string(nil), rec.ALPN...)
	}
	pins := parsePins(rec.PinnedPeerCertSHA256)
	names := splitNames(rec.VerifyPeerCertByName)
	if len(pins) == 0 {
		if len(names) > 0 {
			cfg.VerifyConnection = func(cs tls.ConnectionState) error {
				return verifyNames(cs, names)
			}
		}
		return cfg
	}
	cfg.InsecureSkipVerify = true // replaced by verifyPinned below, never skipped
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		return verifyPinned(cs, pins, names, serverName)
	}
	return cfg
}

func verifyNames(cs tls.ConnectionState, names []string) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("no peer certificate")
	}
	leaf := cs.PeerCertificates[0]
	for _, name := range names {
		if leaf.VerifyHostname(name) == nil {
			return nil
		}
	}
	return errors.New("peer certificate name constraint did not match")
}

func verifyPinned(cs tls.ConnectionState, pins [][]byte, names []string, serverName string) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("no peer certificate")
	}
	leaf := cs.PeerCertificates[0]
	leafSum := sha256.Sum256(leaf.Raw)
	leafPinned := false
	for _, pin := range pins {
		if subtle.ConstantTimeCompare(leafSum[:], pin) == 1 {
			leafPinned = true
			break
		}
	}
	if leafPinned {
		if len(names) == 0 {
			return nil
		}
		return verifyNames(cs, names)
	}

	// A non-leaf pin is accepted only when it names a CA in the presented
	// chain; that CA becomes the trust anchor. An unrelated CA certificate that
	// merely appears in the chain cannot satisfy the pin.
	roots := x509.NewCertPool()
	intermediates := x509.NewCertPool()
	for i, cert := range cs.PeerCertificates {
		if i == 0 {
			continue
		}
		intermediates.AddCert(cert)
		if !cert.IsCA {
			continue
		}
		sum := sha256.Sum256(cert.Raw)
		for _, pin := range pins {
			if subtle.ConstantTimeCompare(sum[:], pin) == 1 {
				roots.AddCert(cert)
			}
		}
	}
	if len(roots.Subjects()) == 0 {
		return errors.New("peer certificate pin did not match")
	}
	targets := names
	if len(targets) == 0 {
		targets = []string{serverName}
	}
	for _, target := range targets {
		opts := x509.VerifyOptions{
			Roots: roots, Intermediates: intermediates, DNSName: target,
		}
		if _, err := leaf.Verify(opts); err == nil {
			return nil
		}
	}
	return errors.New("peer certificate did not verify against the pinned CA and name")
}

func parsePins(raw string) [][]byte {
	if raw == "" {
		return nil
	}
	var out [][]byte
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(part)), "sha256:")
		part = strings.ReplaceAll(part, ":", "")
		if len(part) != 64 {
			continue
		}
		if decoded, err := hex.DecodeString(part); err == nil {
			out = append(out, decoded)
		}
	}
	return out
}

func splitNames(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func destinationAddress(dst contract.Destination) string {
	host := dst.Host
	if dst.Kind == contract.DestIP {
		host = dst.Addr.String()
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", dst.Port))
}

func classifyDialError(err error, transport bool) (contract.ProbeOutcome, contract.ErrorKind) {
	if errors.Is(err, context.DeadlineExceeded) {
		return contract.OutcomeTimeout, contract.Timeout
	}
	if errors.Is(err, context.Canceled) {
		return contract.OutcomeCanceled, contract.Canceled
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return contract.OutcomeDNSFailure, contract.DNSFailure
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return contract.OutcomeTimeout, contract.Timeout
	}
	if transport {
		return contract.OutcomeRefused, contract.ExitUnreachable
	}
	return contract.OutcomeRefused, contract.ConnectRefused
}

func classifyDirectError(err error) (contract.ProbeOutcome, contract.ErrorKind) {
	return classifyDialError(err, false)
}

func outcomeForPoolError(err error) (contract.ProbeOutcome, contract.ErrorKind) {
	var f *contract.Failure
	if !errors.As(err, &f) {
		if errors.Is(err, context.DeadlineExceeded) {
			return contract.OutcomeTimeout, contract.Timeout
		}
		if errors.Is(err, context.Canceled) {
			return contract.OutcomeCanceled, contract.Canceled
		}
		return contract.OutcomeRejected, contract.Internal
	}
	switch f.Kind {
	case contract.Canceled:
		return contract.OutcomeCanceled, contract.Canceled
	case contract.Timeout:
		return contract.OutcomeTimeout, contract.Timeout
	case contract.DestinationUnconfirmed:
		return contract.OutcomeUnconfirmed, contract.DestinationUnconfirmed
	case contract.DestinationRefused, contract.ConnectRefused:
		return contract.OutcomeRefused, f.Kind
	case contract.HandshakeRejected:
		return contract.OutcomeRejected, contract.HandshakeRejected
	case contract.ExitUnreachable:
		return contract.OutcomeRefused, contract.ExitUnreachable
	case contract.DNSFailure:
		return contract.OutcomeDNSFailure, contract.DNSFailure
	case contract.TLSFailure:
		return contract.OutcomeTLSFailure, contract.TLSFailure
	default:
		return contract.OutcomeRejected, f.Kind
	}
}

func directEvidenceEligible(kind contract.ErrorKind) bool {
	switch kind {
	case contract.ConnectRefused, contract.Timeout, contract.DNSFailure:
		return true
	default:
		return false
	}
}

func transportAttributable(kind contract.ErrorKind, phase Phase) bool {
	if phase != PhaseDial && phase != PhaseHandshake {
		return false
	}
	switch kind {
	case contract.ExitUnreachable, contract.HandshakeRejected, contract.Timeout:
		return true
	default:
		return false
	}
}

func (e *Engine) attribute(ctx context.Context, m Measurement) Measurement {
	if m.Result.Kind == contract.ProbeDirectTCP || m.Result.Kind == contract.ProbeDirectTLS {
		// A direct probe is diagnostic evidence about a destination, never
		// about an exit. It can identify a local outage but never cools pool
		// health (docs/contracts.md §6.2).
		m.Attributable = false
		if e.opts.LocalControl != nil && m.Result.Outcome != contract.OutcomeSuccess &&
			m.Result.Outcome != contract.OutcomeCanceled {
			if err := e.opts.LocalControl(ctx); err != nil && !errors.Is(err, context.Canceled) {
				m.LocalOutage = true
			}
		}
		return m
	}
	if !transportAttributable(m.Result.Failure, m.Phase) {
		m.Attributable = false
		return m
	}
	if e.opts.LocalControl == nil {
		m.Attributable = true
		return m
	}
	if err := e.opts.LocalControl(ctx); err != nil {
		if !errors.Is(err, context.Canceled) {
			m.LocalOutage = true
			m.Attributable = false
			return m
		}
	}
	m.Attributable = true
	return m
}
