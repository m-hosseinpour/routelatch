package control

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/pool"
)

// errUsageExitGone aborts a usage write for an exit that was removed between
// the first downlink byte and the mutation; the removed record must not come
// back as a phantom health row.
var errUsageExitGone = errors.New("usage evidence for a removed exit")

// egressFailureTTL bounds how long a recent approved-egress dial failure stays
// visible in status. The signal is in-memory only and does not cool, rerank or
// take an exit out of service (docs/contracts.md R-28).
const egressFailureTTL = 60 * time.Second

// egressFailureWarning is fixed text: the signal deliberately carries no exit,
// destination, URL or engine detail (D21).
const egressFailureWarning = "recent approved egress dial failed; traffic remains fail-closed with no direct fallback"

// usagePool observes real user connections so positive egress evidence reaches
// health (docs/contracts.md §5.3, §6.2). Probes use the unwrapped dialer: their
// results already update health and must not be counted twice.
type usagePool struct {
	svc   *Service
	inner contract.PoolDialer
}

var _ contract.PoolDialer = (*usagePool)(nil)

func (p *usagePool) DialPool(ctx context.Context, req contract.PoolDialRequest) (contract.PoolDialResult, error) {
	started := p.svc.now()
	res, err := p.inner.DialPool(ctx, req)
	if err != nil {
		p.svc.recordEgressFailure(err)
		return res, err
	}
	if res.Conn == nil || res.Exit == "" {
		return res, err
	}
	// The approved dial reached the exit; clear an earlier failure signal.
	p.svc.clearEgressFailure()
	res.Conn = &usageConn{Conn: res.Conn, exit: res.Exit, started: started, now: p.svc.now, record: p.svc.recordUsage}
	return res, nil
}

// recordEgressFailure keeps a recent approved-egress dial failure visible to
// status without touching pool health, ranking or the data path. Only
// exit_unreachable counts: it is the one class the pool dial path attributes to
// the exit's own transport before a destination connection exists. The broader
// pool.IsTransportFailure set is deliberately not reused: handshake_rejected
// also covers SOCKS5 reply 0x03/0x04 (network/host unreachable reported by the
// exit), and timeout can be a destination-side stall that expires the exit
// handshake deadline. Neither proves the exit transport failed, so they stay a
// request error but never raise the warning (docs/contracts.md R-28).
func (s *Service) recordEgressFailure(err error) {
	var f *contract.Failure
	if !errors.As(err, &f) || f.Kind != contract.ExitUnreachable {
		return
	}
	now := s.now().UTC()
	s.egressMu.Lock()
	s.egressFailedAt = now
	s.egressMu.Unlock()
}

// clearEgressFailure drops the signal after a successful approved dial. The
// write is in-memory only; no persisted state changes.
func (s *Service) clearEgressFailure() {
	s.egressMu.Lock()
	s.egressFailedAt = time.Time{}
	s.egressMu.Unlock()
}

// egressFailureRecent reports whether an approved-egress dial failure happened
// within egressFailureTTL. Expiry needs no timer or watcher: the next status
// read simply stops reporting it.
func (s *Service) egressFailureRecent(now time.Time) bool {
	s.egressMu.Lock()
	defer s.egressMu.Unlock()
	return !s.egressFailedAt.IsZero() && now.Sub(s.egressFailedAt) < egressFailureTTL
}

// usageConn reports the first downlink byte as positive egress evidence: bytes
// came back through this exit. CloseWrite/CloseRead are preserved explicitly
// so the frontend's half-close handling does not lose optional interfaces.
type usageConn struct {
	net.Conn
	once    sync.Once
	exit    contract.ExitID
	started time.Time
	now     func() time.Time
	record  func(contract.ExitID, time.Duration)
}

func (c *usageConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.once.Do(func() {
			latency := c.now().Sub(c.started)
			if latency < 0 {
				latency = 0
			}
			c.record(c.exit, latency)
		})
	}
	return n, err
}

func (c *usageConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c *usageConn) CloseRead() error {
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return nil
}

// recordUsage persists one positive-evidence sample. Bursts are coalesced to
// at most one sample per exit per second: a sample is diagnostic evidence, and
// an fsync per connection would serialize the data path behind the state lock.
func (s *Service) recordUsage(exit contract.ExitID, latency time.Duration) {
	now := s.now().UTC()
	s.usageMu.Lock()
	if s.usageAt == nil {
		s.usageAt = make(map[contract.ExitID]time.Time)
	}
	if last, ok := s.usageAt[exit]; ok && now.Sub(last) < time.Second {
		s.usageMu.Unlock()
		return
	}
	s.usageAt[exit] = now
	s.usageMu.Unlock()

	go func() {
		s.pubMu.Lock()
		defer s.pubMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		snap, err := s.store.Mutate(ctx, func(st *contract.State) error {
			if !hasRecord(st, exit) {
				return errUsageExitGone
			}
			pool.RecordUsage(st, exit, latency, now)
			pool.SyncStatuses(st, now)
			return nil
		})
		if err != nil {
			return
		}
		s.snap.Store(&snap)
	}()
}

func hasRecord(st *contract.State, exit contract.ExitID) bool {
	for i := range st.Records {
		if contract.ExitID(st.Records[i].ID) == exit {
			return true
		}
	}
	return false
}

// recordTunnelFailure persists one pre-byte tunnel failure against the exit
// that accepted the request. It is deliberately not coalesced: every failed
// attempt is one conceded sample, and the cooldown ladder is built from the
// consecutive count. The write carries no destination (D21); the health row is
// the only artifact.
func (s *Service) recordTunnelFailure(exit contract.ExitID, kind contract.ErrorKind) {
	if exit == "" || kind != contract.DestinationUnconfirmed {
		return
	}
	now := s.now().UTC()
	go func() {
		s.pubMu.Lock()
		defer s.pubMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		snap, err := s.store.Mutate(ctx, func(st *contract.State) error {
			if !hasRecord(st, exit) {
				return errUsageExitGone
			}
			pool.RecordTunnelFailure(st, exit, now)
			pool.SyncStatuses(st, now)
			return nil
		})
		if err != nil {
			return
		}
		s.snap.Store(&snap)
	}()
}
