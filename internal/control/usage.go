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
	if err != nil || res.Conn == nil || res.Exit == "" {
		return res, err
	}
	res.Conn = &usageConn{Conn: res.Conn, exit: res.Exit, started: started, now: p.svc.now, record: p.svc.recordUsage}
	return res, nil
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
