package socksfront

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	gosocks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
)

func TestLibBindRefused(t *testing.T) {
	addr, _ := startLibServer(t)
	c := dialTest(t, addr)
	greetNoAuth(t, c)

	udpBefore := udpSockets(t)
	writeAll(t, c, rawRequest(0x02, atypIPv4))
	reply := readReply(t, c)
	t.Logf("BIND reply: %s", hex(reply.Raw))
	if got := hex(reply.Raw); got != "05 07 00 01 00 00 00 00 00 00" {
		t.Fatalf("BIND reply bytes: got %s want 05 07 00 01 00 00 00 00 00 00", got)
	}
	expectClosed(t, c, time.Second, "after BIND refusal")
	time.Sleep(100 * time.Millisecond) // bounded window for any late socket
	if after := udpSockets(t); after != udpBefore {
		t.Fatalf("BIND opened UDP sockets: before=%d after=%d", udpBefore, after)
	}
}

func TestLibUDPAssociateDefaultOpensListener(t *testing.T) {
	udpEcho := startUDPEcho(t)
	addr, _ := startLibServer(t)
	c := dialTest(t, addr)
	greetNoAuth(t, c)

	fdBefore := fdCount(t)
	udpBefore := udpSockets(t)
	writeAll(t, c, rawRequest(0x03, atypIPv4))
	reply := readReply(t, c)
	t.Logf("UDP ASSOCIATE reply: %s", hex(reply.Raw))
	if reply.Rep != 0x00 {
		t.Fatalf("UDP ASSOCIATE reply: got %#x want success", reply.Rep)
	}
	if reply.Port == 0 {
		t.Fatalf("UDP ASSOCIATE reply has no BND.PORT: %s", hex(reply.Raw))
	}
	if !waitUntil(time.Second, func() bool { return udpSockets(t) > udpBefore }) {
		t.Fatalf("no UDP socket was opened: before=%d after=%d", udpBefore, udpSockets(t))
	}
	t.Logf("fds before=%d after=%d; udp sockets before=%d after=%d",
		fdBefore, fdCount(t), udpBefore, udpSockets(t))

	// Exercise the relay: a datagram to the BND address must reach the local
	// UDP echo and be sent back with an RFC 1928 header.
	uc, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP(reply.Addr), Port: reply.Port})
	if err != nil {
		t.Fatalf("udp client: %v", err)
	}
	t.Cleanup(func() { _ = uc.Close() })
	payload := []byte("udp-echo-payload")
	if _, err := uc.Write(udpDatagram(net.IPv4(127, 0, 0, 1), uint16(udpEcho.Port), payload)); err != nil {
		t.Fatalf("udp write: %v", err)
	}
	_ = uc.SetReadDeadline(time.Now().Add(testTimeout))
	buf := make([]byte, 2048)
	n, err := uc.Read(buf)
	if err != nil {
		t.Fatalf("udp relay response: %v", err)
	}
	if got := parseUDPDatagram(t, buf[:n]); string(got) != string(payload) {
		t.Fatalf("udp relay payload: got %q want %q", got, payload)
	}

	// Closing the TCP control connection must release the UDP listener. Close
	// the test's own UDP client first so it is not counted.
	_ = uc.Close()
	_ = c.Close()
	if !waitUntil(testTimeout, func() bool { return udpSockets(t) <= udpBefore }) {
		t.Fatalf("UDP listener not released: before=%d after close=%d", udpBefore, udpSockets(t))
	}
	uc2, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP(reply.Addr), Port: reply.Port})
	if err != nil {
		t.Fatalf("udp client 2: %v", err)
	}
	defer uc2.Close()
	_, _ = uc2.Write(udpDatagram(net.IPv4(127, 0, 0, 1), uint16(udpEcho.Port), payload))
	_ = uc2.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, err := uc2.Read(buf); err == nil && n > 0 {
		t.Fatalf("closed associate still relayed %d bytes", n)
	}
	t.Log("associate relay stopped after control connection close (<=300ms negative window)")
}

func TestLibUDPAssociateRefusedWithHandler(t *testing.T) {
	addr, _ := startLibServer(t, gosocks5.WithAssociateHandle(
		func(_ context.Context, w io.Writer, _ *gosocks5.Request) error {
			return gosocks5.SendReply(w, statute.RepCommandNotSupported, nil)
		}))
	c := dialTest(t, addr)
	greetNoAuth(t, c)

	udpBefore := udpSockets(t)
	writeAll(t, c, rawRequest(0x03, atypIPv4))
	reply := readReply(t, c)
	t.Logf("refused UDP ASSOCIATE reply: %s", hex(reply.Raw))
	if got := hex(reply.Raw); got != "05 07 00 01 00 00 00 00 00 00" {
		t.Fatalf("refused reply bytes: got %s", got)
	}
	expectClosed(t, c, time.Second, "after associate refusal")
	time.Sleep(100 * time.Millisecond)
	if after := udpSockets(t); after != udpBefore {
		t.Fatalf("refusal opened a UDP socket: before=%d after=%d", udpBefore, after)
	}
}

func TestLibAuthIsNoAuthOnly(t *testing.T) {
	addr, _ := startLibServer(t)
	c := dialTest(t, addr)
	writeAll(t, c, []byte{0x05, 0x01, 0x02}) // offer user/pass only
	expectBytes(t, c, []byte{0x05, 0xff}, "method reply")
}

// udpSockets counts UDP sockets owned by this process: socket inodes from
// /proc/self/fd intersected with the inode column of /proc/self/net/udp.
// /proc/self/net/udp is namespace-wide, so the intersection is what makes it
// process-local.
func udpSockets(t *testing.T) int {
	t.Helper()
	owned := map[string]struct{}{}
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	for _, e := range ents {
		link, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err != nil || !strings.HasPrefix(link, "socket:[") {
			continue
		}
		owned[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = struct{}{}
	}
	f, err := os.Open("/proc/self/net/udp")
	if err != nil {
		t.Fatalf("open /proc/self/net/udp: %v", err)
	}
	defer f.Close()
	count := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		if _, ok := owned[fields[9]]; ok {
			count++
		}
	}
	return count
}

func udpDatagram(ip net.IP, port uint16, payload []byte) []byte {
	b := []byte{0x00, 0x00, 0x00, 0x01}
	b = append(b, ip.To4()...)
	b = binary.BigEndian.AppendUint16(b, port)
	return append(b, payload...)
}

func parseUDPDatagram(t *testing.T, b []byte) []byte {
	t.Helper()
	if len(b) < 4 || b[2] != 0x00 {
		t.Fatalf("udp datagram header: %s", hex(b))
	}
	var off int
	switch b[3] {
	case atypIPv4:
		off = 4 + net.IPv4len + 2
	case atypIPv6:
		off = 4 + net.IPv6len + 2
	case atypDomain:
		if len(b) < 5 {
			t.Fatalf("short domain datagram: %s", hex(b))
		}
		off = 5 + int(b[4]) + 2
	default:
		t.Fatalf("udp datagram atyp: %#x", b[3])
	}
	if len(b) < off {
		t.Fatalf("short udp datagram: %s", hex(b))
	}
	return b[off:]
}

// TestLibRefusalAfterResolutionOrder shows handleRequest resolves a domain
// before it looks at the command, so a domain-form BIND/UDP request is answered
// from the resolver (possibly a local DNS lookup) instead of reaching the
// explicit refusal handler.
func TestLibRefusalAfterResolutionOrder(t *testing.T) {
	cases := []struct {
		name string
		cmd  byte
	}{
		{"BIND", 0x02},
		{"UDP ASSOCIATE", 0x03},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, _ := startLibServer(t, gosocks5.WithAssociateHandle(
				func(_ context.Context, w io.Writer, _ *gosocks5.Request) error {
					return gosocks5.SendReply(w, statute.RepCommandNotSupported, nil)
				}))
			c := dialTest(t, addr)
			greetNoAuth(t, c)
			writeAll(t, c, domainRequest(tc.cmd, "socks-target.invalid", 80))
			reply := readReply(t, c)
			t.Logf("%s with unresolvable domain -> reply %s (0x07 handler was not reached)", tc.name, hex(reply.Raw))
			if reply.Rep != 0x04 {
				t.Fatalf("reply: got %#x, expected 0x04 from the resolver before command dispatch", reply.Rep)
			}
			expectClosed(t, c, time.Second, "after resolution failure")
		})
	}
}
