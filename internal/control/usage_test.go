package control

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/store"
)

type usageProbeConn struct {
	net.Conn
	closeWrites atomic.Int64
}

func (c *usageProbeConn) CloseWrite() error {
	c.closeWrites.Add(1)
	return nil
}

type eofConn struct{ net.Conn }

func (eofConn) Read([]byte) (int, error) { return 0, io.EOF }

func TestUsageConnZeroReadIsNotEvidence(t *testing.T) {
	called := false
	c := &usageConn{
		Conn: eofConn{}, exit: "x-a", started: time.Now(), now: time.Now,
		record: func(contract.ExitID, time.Duration) { called = true },
	}
	if n, err := c.Read(make([]byte, 4)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("read = %d, %v", n, err)
	}
	if called {
		t.Fatal("a zero-byte read must not record usage evidence")
	}
}

func TestUsageConnRecordsFirstDownlinkByteOnce(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	var mu sync.Mutex
	var calls []time.Duration
	inner := &usageProbeConn{Conn: client}
	c := &usageConn{
		Conn: inner, exit: "x-a", started: time.Now().Add(-100 * time.Millisecond),
		now: time.Now,
		record: func(exit contract.ExitID, latency time.Duration) {
			if exit != "x-a" {
				t.Errorf("exit = %q", exit)
			}
			mu.Lock()
			calls = append(calls, latency)
			mu.Unlock()
		},
	}

	writeErr := make(chan error, 1)
	go func() {
		_, err := server.Write([]byte("hello"))
		writeErr <- err
	}()
	buf := make([]byte, 5)
	if n, err := c.Read(buf); n != 5 || err != nil {
		t.Fatalf("read = %d, %v", n, err)
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
	// A second downlink byte on the same connection is still one sample.
	go func() {
		_, _ = server.Write([]byte("x"))
	}()
	if n, err := c.Read(buf[:1]); n != 1 || err != nil {
		t.Fatalf("second read = %d, %v", n, err)
	}
	mu.Lock()
	got := len(calls)
	mu.Unlock()
	if got != 1 {
		t.Fatalf("record calls = %d, want 1", got)
	}

	if err := c.CloseWrite(); err != nil || inner.closeWrites.Load() != 1 {
		t.Fatalf("CloseWrite promoted: err=%v calls=%d", err, inner.closeWrites.Load())
	}
	// net.Pipe has no CloseRead; the wrapper must not turn that into a failure.
	if err := c.CloseRead(); err != nil {
		t.Fatalf("CloseRead on a conn without it: %v", err)
	}
}

type usageFakePool struct {
	res contract.PoolDialResult
	err error
}

func (f *usageFakePool) DialPool(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
	return f.res, f.err
}

func TestUsagePoolOnlyWrapsPoolConns(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	svc.now = time.Now

	p := &usagePool{svc: svc, inner: &usageFakePool{res: contract.PoolDialResult{Conn: client, Exit: "x-a", Proof: contract.ProofAccepted}}}
	res, err := p.DialPool(context.Background(), contract.PoolDialRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Conn.(*usageConn); !ok {
		t.Fatalf("pool conn was not wrapped: %T", res.Conn)
	}
	_ = res.Conn.Close()

	failure := errors.New("no exit")
	if _, err := (&usagePool{svc: svc, inner: &usageFakePool{err: failure}}).DialPool(context.Background(), contract.PoolDialRequest{}); !errors.Is(err, failure) {
		t.Fatalf("error was not passed through: %v", err)
	}
}

func seedUsageRecord(t *testing.T, st *store.Store, id string) {
	t.Helper()
	_, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Records = append(state.Records, contract.Record{
			ID: contract.RecordID(id), DedupeKey: "sha256:" + id,
			Protocol: contract.ProtocolSOCKS5, Server: "127.0.0.1", Port: 1080,
			Transport: contract.TransportRaw, Security: contract.SecurityNone,
			Status: contract.RecordUnknown, AddedAt: time.Now().UTC(),
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRecordUsageIgnoresRemovedExit(t *testing.T) {
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	svc.now = time.Now

	svc.recordUsage("x-gone", 50*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	if h := svc.Snapshot().State.Health; len(h) != 0 {
		t.Fatalf("usage for a removed exit created health: %+v", h)
	}
	persisted, err := st.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.State.Health) != 0 {
		t.Fatalf("persisted health = %+v", persisted.State.Health)
	}
}

func TestRecordUsagePersistsEvidenceAndCoalesces(t *testing.T) {
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	svc.now = time.Now
	seedUsageRecord(t, st, "x-a")

	svc.recordUsage("x-a", 120*time.Millisecond)
	wait := time.Now().Add(2 * time.Second)
	for {
		snap := svc.Snapshot()
		for _, h := range snap.State.Health {
			if h.Exit == "x-a" && h.Samples == 1 {
				if h.Status != contract.HealthHealthy || h.EWMALatencyMS != 120 {
					t.Fatalf("health = %+v", h)
				}
				// The persisted file must agree with the published snapshot.
				again, err := st.Load(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, ph := range again.State.Health {
					if ph.Exit == "x-a" && ph.Samples == 1 && ph.Status == contract.HealthHealthy {
						found = true
					}
				}
				if !found {
					t.Fatalf("usage evidence was not persisted: %+v", again.State.Health)
				}
				// A second connection in the same second is coalesced.
				svc.recordUsage("x-a", 90*time.Millisecond)
				time.Sleep(100 * time.Millisecond)
				for _, h := range svc.Snapshot().State.Health {
					if h.Exit == "x-a" && h.Samples != 1 {
						t.Fatalf("coalescing failed: samples = %d", h.Samples)
					}
				}
				return
			}
		}
		if time.Now().After(wait) {
			t.Fatalf("usage evidence never appeared: %+v", snap.State.Health)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
