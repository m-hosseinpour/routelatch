package pool

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"routelatch/internal/contract"
)

// TestDialPoolHandsTheSessionLifetimeToTheExit pins the ownership rule the
// engine relies on: DialPool's context is the connection lifetime, so it must
// reach the exit dialer unwrapped. A dial-only deadline there becomes the
// tunnel's deadline (docs/contracts.md §2.1, §3.4; finding F1). The adapter's
// own handshake bound is applied by the exit dialer, not by this wrapper.
func TestDialPoolHandsTheSessionLifetimeToTheExit(t *testing.T) {
	now := t0()
	state := contract.State{
		PolicyVersion: 4,
		Records:       []contract.Record{record("x-a", 0, contract.RecordUnknown)},
	}
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	defer cancelLifetime()

	seen := make(chan context.Context, 1)
	var server net.Conn
	d := NewDialer(func() (contract.Snapshot, bool) { return contract.Snapshot{State: state}, true },
		func(ctx context.Context, _ contract.ExitID, _ contract.Destination) (net.Conn, contract.ProofClass, string, error) {
			seen <- ctx
			client, srv := net.Pipe()
			server = srv
			return client, contract.ProofAccepted, "tag-a", nil
		}, func() time.Time { return now })

	res, err := d.DialPool(lifetime, contract.PoolDialRequest{
		Destination:   contract.Destination{Kind: contract.DestDomain, Host: "example.com", Port: 443},
		PolicyVersion: 4,
	})
	if err != nil {
		t.Fatalf("DialPool: %v", err)
	}
	defer func() {
		_ = res.Conn.Close()
		if server != nil {
			_ = server.Close()
		}
	}()

	sessionCtx := <-seen
	if deadline, ok := sessionCtx.Deadline(); ok {
		t.Fatalf("the exit dial received a dial deadline (%v); the established tunnel would die at it", deadline)
	}
	if err := sessionCtx.Err(); err != nil {
		t.Fatalf("the session context was ended when the dial returned: %v", err)
	}

	// The session must end exactly when the connection lifetime does.
	cancelLifetime()
	if err := sessionCtx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("the session context is not owned by the caller's lifetime: %v", err)
	}
}
