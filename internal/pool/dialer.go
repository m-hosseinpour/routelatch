package pool

import (
	"context"
	"errors"
	"net"
	"time"

	"routelatch/internal/contract"
)

// SnapshotSource returns the current consistent state. The dial path must not
// call Store.Load: it uses the cached snapshot the service already owns.
type SnapshotSource func() (contract.Snapshot, bool)

// ExitDialFunc dials one destination through exactly one explicit exit. It is
// implemented by the engine adapter (T3); a tag is optional for the owned
// SOCKS5/HTTP adapter. Returning a nil conn with a nil error is a contract
// violation, not a usable dial.
type ExitDialFunc func(ctx context.Context, exit contract.ExitID, dst contract.Destination) (net.Conn, contract.ProofClass, string, error)

var _ contract.PoolDialer = (*Dialer)(nil)

// Dialer implements contract.PoolDialer. It owns exit selection and the policy
// fence only; it never falls back to a direct dial and never mutates the engine.
type Dialer struct {
	snapshot SnapshotSource
	selector *Selector
	dial     ExitDialFunc
	now      func() time.Time
}

// NewDialer wires selection to one exit dialer. now is optional.
func NewDialer(snapshot SnapshotSource, dial ExitDialFunc, now func() time.Time) *Dialer {
	if now == nil {
		now = time.Now
	}
	return &Dialer{snapshot: snapshot, selector: NewSelector(), dial: dial, now: now}
}

// Rerank recomputes the automatic exit selection without dialing or touching
// the engine. It is the `pool rerank` entry point; a pinned SelectedExit is
// reported as unchanged, and the policy version is never advanced.
func (d *Dialer) Rerank(now time.Time) (RerankResult, error) {
	if d.snapshot == nil {
		return RerankResult{}, &contract.Failure{Kind: contract.Internal, Path: contract.PathPool, Op: "pool.rerank", Message: "pool reranker is not configured"}
	}
	snap, ok := d.snapshot()
	if !ok {
		return RerankResult{}, selectFailure("pool state is not available")
	}
	return d.selector.Rerank(snap.State, now)
}

// DialPool implements contract.PoolDialer.
func (d *Dialer) DialPool(ctx context.Context, req contract.PoolDialRequest) (contract.PoolDialResult, error) {
	if d.snapshot == nil || d.dial == nil {
		return contract.PoolDialResult{}, internalFailure("pool dialer is not configured")
	}
	if req.Destination.Kind == contract.DestCIDR || req.Destination.Port == 0 {
		return contract.PoolDialResult{}, &contract.Failure{
			Kind: contract.InvalidDestination, Path: contract.PathLocal,
			Op: "pool.dial", Message: "destination is not dialable", Destination: req.Destination,
		}
	}
	snap, ok := d.snapshot()
	if !ok {
		return contract.PoolDialResult{}, selectFailure("pool state is not available")
	}
	if req.PolicyVersion != snap.State.PolicyVersion {
		// A dial admitted under an older policy must not silently use the new
		// exit set (docs/contracts.md §3, R-13 fence).
		return contract.PoolDialResult{}, &contract.Failure{
			Kind: contract.Canceled, Path: contract.PathLocal, Op: "pool.dial",
			Retryable: true, Message: "connection was admitted under a stale policy version",
			Destination: req.Destination,
		}
	}
	rec, fail := d.selector.SelectFor(snap.State, req.Exit, d.now())
	if fail != nil {
		fail.Destination = req.Destination
		return contract.PoolDialResult{}, fail
	}

	// ctx is the connection lifetime, not a dial scope: the returned session
	// must stay attached to it so only the caller's registry/close path can end
	// it (docs/contracts.md §2.6, §3.4). Each dial adapter bounds its own
	// blocking dial phase instead — the SOCKS5 adapter applies
	// contract.PoolDialDeadline when ctx has no earlier deadline, and the core
	// path has no observable dial phase to bound (§2.1). Deriving a dial
	// deadline here would hand the engine a context that kills the tunnel at
	// that deadline (finding F1).
	conn, proof, tag, err := d.dial(ctx, contract.ExitID(rec.ID), req.Destination)
	if err != nil {
		return contract.PoolDialResult{}, mapDialError(err, req.Destination, contract.ExitID(rec.ID), proof)
	}
	if conn == nil {
		return contract.PoolDialResult{}, internalFailure("pool dial returned no connection")
	}
	if proof != contract.ProofConfirmed && proof != contract.ProofAccepted {
		_ = conn.Close()
		return contract.PoolDialResult{}, internalFailure("pool dial returned a connection without a proof class")
	}
	return contract.PoolDialResult{
		Conn: conn, Exit: contract.ExitID(rec.ID), Tag: tag, Proof: proof, AcceptedAt: d.now(),
	}, nil
}

func mapDialError(err error, dst contract.Destination, exit contract.ExitID, proof contract.ProofClass) *contract.Failure {
	var f *contract.Failure
	if errors.As(err, &f) {
		if f.Path == "" {
			f.Path = contract.PathPool
		}
		if f.Exit == "" {
			f.Exit = exit
		}
		if f.Destination.Kind == "" {
			f.Destination = dst
		}
		if f.Proof == "" {
			f.Proof = proof
		}
		if f.Op == "" {
			f.Op = "pool.dial"
		}
		return f
	}
	kind := contract.ExitUnreachable
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		kind = contract.Timeout
	case errors.Is(err, context.Canceled):
		kind = contract.Canceled
	}
	return &contract.Failure{
		Kind: kind, Path: contract.PathPool, Op: "pool.dial", Exit: exit,
		Destination: dst, Proof: proof, Retryable: kind != contract.Canceled,
		Message: "the exit transport failed", Err: err,
	}
}

func internalFailure(message string) *contract.Failure {
	return &contract.Failure{Kind: contract.Internal, Path: contract.PathPool, Op: "pool.dial", Message: message}
}
