package socksfront

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"

	gosocks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
)

// countingResolver records that the library asked for a name but performs no
// lookup at all. This is the resolver the recommended configuration pins.
type countingResolver struct{ calls atomic.Int32 }

func (r *countingResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	r.calls.Add(1)
	return ctx, nil, nil
}

// TestLibStubResolverRefusesBindAndUDPWithoutLookup closes the ordering gap
// reported with the default resolver: with the no-lookup resolver the library
// never fails resolution, so domain-form BIND and UDP ASSOCIATE reach our
// handlers and get the explicit 0x07 refusal. No UDP socket is opened.
func TestLibStubResolverRefusesBindAndUDPWithoutLookup(t *testing.T) {
	var res countingResolver
	var dials atomic.Int32
	refuse := func(_ context.Context, w io.Writer, _ *gosocks5.Request) error {
		return gosocks5.SendReply(w, statute.RepCommandNotSupported, nil)
	}
	addr, _ := startLibServer(t,
		gosocks5.WithResolver(&res),
		gosocks5.WithDialAndRequest(func(_ context.Context, _, _ string, _ *gosocks5.Request) (net.Conn, error) {
			dials.Add(1)
			return nil, io.ErrClosedPipe
		}),
		gosocks5.WithAssociateHandle(refuse),
		gosocks5.WithBindHandle(refuse),
	)

	cases := []struct {
		name string
		cmd  byte
	}{
		{"BIND", 0x02},
		{"UDP ASSOCIATE", 0x03},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			udpBefore := udpSockets(t)
			c := dialTest(t, addr)
			greetNoAuth(t, c)

			host := "refused.invalid"
			req := []byte{0x05, tc.cmd, 0x00, 0x03, byte(len(host))}
			req = append(req, host...)
			req = binary.BigEndian.AppendUint16(req, 443)
			writeAll(t, c, req)

			reply := readReply(t, c)
			t.Logf("%s reply: %s (code %#x), resolver hook calls=%d, dials=%d",
				tc.name, hex(reply.Raw), reply.Rep, res.calls.Load(), dials.Load())
			if reply.Rep != statute.RepCommandNotSupported {
				t.Fatalf("%s: command-not-supported (0x07) expected; spec: %s", tc.name, hex(reply.Raw))
			}
			expectClosed(t, c, testTimeout, tc.name+" refusal")
			// The library calls the resolver hook because the request carries a
			// domain; our hook does no I/O. Nothing dials and no UDP socket opens.
			if res.calls.Load() == 0 {
				t.Fatalf("%s: expected the library to consult the configured resolver", tc.name)
			}
			if dials.Load() != 0 {
				t.Fatalf("%s: a dial was attempted", tc.name)
			}
			if after := udpSockets(t); after != udpBefore {
				t.Fatalf("%s: UDP sockets changed before=%d after=%d", tc.name, udpBefore, after)
			}
		})
	}
}
