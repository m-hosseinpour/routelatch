package diagnostics

import (
	"context"
	"time"

	"routelatch/internal/contract"
)

// probePlan runs the bounded independent check for one destination: TCP first,
// then, for a hostname on port 443 that accepted TCP, a TLS handshake with SNI
// and full verification. An IP literal never gets a TLS probe, so its evidence
// is labelled as the TCP stage only. No application byte is sent and the
// failed request is never replayed (D09, §6.1).
func (m *Manager) probePlan(ctx context.Context, dst contract.Destination, trigger contract.ProbeTrigger) ([]contract.ProbeResult, *contract.Evidence, error) {
	if m.opts.Prober == nil {
		return nil, nil, ErrNoProber
	}
	var results []contract.ProbeResult
	tcp, err := m.opts.Prober.Run(ctx, contract.ProbeRequest{
		Kind: contract.ProbeDirectTCP, Destination: dst,
		Trigger: trigger, PolicyVersion: m.policyVersion(),
	})
	if err != nil {
		return results, nil, err
	}
	results = append(results, tcp)
	if ev, ok := evidenceFor(dst, tcp, m.opts.Clock); ok {
		return results, &ev, nil
	}
	if dst.Kind != contract.DestDomain || dst.Port != 443 || tcp.Outcome != contract.OutcomeSuccess {
		return results, nil, nil
	}

	tlsResult, err := m.opts.Prober.Run(ctx, contract.ProbeRequest{
		Kind: contract.ProbeDirectTLS, Destination: dst,
		Trigger: trigger, PolicyVersion: m.policyVersion(),
	})
	if err != nil {
		return results, nil, err
	}
	results = append(results, tlsResult)
	if ev, ok := evidenceFor(dst, tlsResult, m.opts.Clock); ok {
		return results, &ev, nil
	}
	return results, nil, nil
}

func (m *Manager) policyVersion() contract.PolicyVersion {
	if m.opts.PolicyVersion == nil {
		return 0
	}
	return m.opts.PolicyVersion()
}

// evidenceFor is the only place a probe result becomes evidence. A probe that
// merely proved TCP (a successful connection) produces nothing; a TLS
// verification failure is refused by the eligibility matrix.
func evidenceFor(dst contract.Destination, r contract.ProbeResult, clock func() time.Time) (contract.Evidence, bool) {
	if !r.Eligible {
		return contract.Evidence{}, false
	}
	if r.Kind != contract.ProbeDirectTCP && r.Kind != contract.ProbeDirectTLS {
		return contract.Evidence{}, false
	}
	var kind contract.EvidenceKind
	switch r.Failure {
	case contract.ConnectRefused:
		if r.Outcome != contract.OutcomeRefused {
			return contract.Evidence{}, false
		}
		kind = contract.EvidenceDirectRefused
	case contract.Timeout:
		if r.Outcome != contract.OutcomeTimeout {
			return contract.Evidence{}, false
		}
		kind = contract.EvidenceDirectTimeout
	case contract.DNSFailure:
		if r.Outcome != contract.OutcomeDNSFailure {
			return contract.Evidence{}, false
		}
		kind = contract.EvidenceDirectDNS
	default:
		return contract.Evidence{}, false
	}
	addr := address(dst)
	if addr == "" {
		return contract.Evidence{}, false
	}
	at := r.StartedAt
	if at.IsZero() && clock != nil {
		at = clock()
	}
	return contract.Evidence{
		Kind:      kind,
		At:        at.UTC(),
		Address:   addr,
		ProbeKind: r.Kind,
		Outcome:   r.Outcome,
	}, true
}

// noteFor describes the stage in controlled text. It never includes a URL,
// header, query, body, cookie or anything from the original request.
func noteFor(dst contract.Destination, ev contract.Evidence) string {
	stage := "direct TCP"
	if ev.ProbeKind == contract.ProbeDirectTLS {
		stage = "direct TLS (SNI, full certificate verification)"
	}
	note := stage + " probe reproduced " + string(ev.Kind)
	if dst.Kind == contract.DestIP {
		note += "; IP literal: evidence covers the TCP stage only"
	}
	return note
}
