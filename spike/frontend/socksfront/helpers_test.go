package socksfront

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const testTimeout = 2 * time.Second

// echoServer is a loopback TCP echo peer. peerGone is closed once a proxied
// peer closes its side (EOF or reset), which is how teardown is observed.
type echoServer struct {
	addr     string
	peerGone chan struct{}
	once     sync.Once
}

func startEcho(t *testing.T) *echoServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	es := &echoServer{addr: ln.Addr().String(), peerGone: make(chan struct{})}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_, _ = io.Copy(c, c)
				es.once.Do(func() { close(es.peerGone) })
				_ = c.Close()
			}(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return es
}

func startUDPEcho(t *testing.T) *net.UDPAddr {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp echo: %v", err)
	}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], from)
		}
	}()
	t.Cleanup(func() { _ = pc.Close() })
	return pc.LocalAddr().(*net.UDPAddr)
}

func dialTest(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, testTimeout)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func writeAll(t *testing.T, c net.Conn, b []byte) {
	t.Helper()
	if _, err := c.Write(b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readExact(t *testing.T, c net.Conn, n int) []byte {
	t.Helper()
	b, err := tryReadExact(c, n, testTimeout)
	if err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return b
}

func tryReadExact(c net.Conn, n int, d time.Duration) ([]byte, error) {
	_ = c.SetReadDeadline(time.Now().Add(d))
	b := make([]byte, n)
	_, err := io.ReadFull(c, b)
	return b, err
}

func expectBytes(t *testing.T, c net.Conn, want []byte, what string) {
	t.Helper()
	got := readExact(t, c, len(want))
	if string(got) != string(want) {
		t.Fatalf("%s: got %x, want %x", what, got, want)
	}
}

func greetNoAuth(t *testing.T, c net.Conn) {
	t.Helper()
	writeAll(t, c, []byte{0x05, 0x01, 0x00})
	expectBytes(t, c, []byte{0x05, 0x00}, "method reply")
}

func domainRequest(cmd byte, host string, port uint16) []byte {
	b := []byte{0x05, cmd, 0x00, 0x03, byte(len(host))}
	b = append(b, host...)
	return binary.BigEndian.AppendUint16(b, port)
}

func ipv4Request(cmd byte, ip net.IP, port uint16) []byte {
	b := []byte{0x05, cmd, 0x00, 0x01}
	b = append(b, ip.To4()...)
	return binary.BigEndian.AppendUint16(b, port)
}

func rawRequest(cmd, atyp byte) []byte {
	return []byte{0x05, cmd, 0x00, atyp, 0, 0, 0, 0, 0, 0}
}

type socksReply struct {
	Rep  byte
	ATYP byte
	Addr string
	Port int
	Raw  []byte
}

func readReply(t *testing.T, c net.Conn) socksReply {
	t.Helper()
	hdr := readExact(t, c, 4)
	if hdr[0] != 0x05 {
		t.Fatalf("reply version: got %#x", hdr[0])
	}
	var addrLen int
	switch hdr[3] {
	case atypIPv4:
		addrLen = net.IPv4len
	case atypIPv6:
		addrLen = net.IPv6len
	case atypDomain:
		addrLen = int(readExact(t, c, 1)[0])
	default:
		t.Fatalf("reply ATYP: got %#x", hdr[3])
	}
	rest := readExact(t, c, addrLen+2)
	raw := append(append([]byte{}, hdr...), rest...)
	r := socksReply{Rep: hdr[1], ATYP: hdr[3], Raw: raw}
	if addrLen > 0 {
		r.Addr = net.IP(rest[:addrLen]).String()
	}
	r.Port = int(binary.BigEndian.Uint16(rest[addrLen:]))
	return r
}

func expectClosed(t *testing.T, c net.Conn, d time.Duration, what string) {
	t.Helper()
	_, err := tryReadExact(c, 1, d)
	if err == nil {
		t.Fatalf("%s: expected close, read succeeded", what)
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("%s: expected close, got read timeout", what)
	}
}

func runtimeNumGoroutine() int { return runtime.NumGoroutine() }

func fdCount(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	return len(ents)
}

// socketFDCount is the leak metric. Total fds are not: io.Copy between two TCP
// connections uses splice(2) and Go keeps a process-wide pool of pipe pairs, so
// total fds can climb a few entries without any socket or goroutine leak.
func socketFDCount(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	n := 0
	for _, e := range ents {
		if link, err := os.Readlink("/proc/self/fd/" + e.Name()); err == nil && strings.HasPrefix(link, "socket:[") {
			n++
		}
	}
	return n
}

func waitUntil(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// logResources reports the measurements used by the leak-bound tests.
func logResources(t *testing.T, label string) (int, int) {
	t.Helper()
	g, sockets := runtime.NumGoroutine(), socketFDCount(t)
	t.Logf("%s: goroutines=%d socket_fds=%d total_fds=%d", label, g, sockets, fdCount(t))
	return g, sockets
}

func waitSettled(t *testing.T, label string, baseG, baseFD, slack int) {
	t.Helper()
	ok := waitUntil(testTimeout, func() bool {
		return runtime.NumGoroutine() <= baseG+slack && socketFDCount(t) <= baseFD+slack
	})
	g, fd := logResources(t, "after "+label)
	if !ok {
		t.Fatalf("%s: resources did not settle: goroutines=%d (base %d +%d), fds=%d (base %d +%d)",
			label, g, baseG, slack, fd, baseFD, slack)
	}
}

func hex(b []byte) string { return fmt.Sprintf("% x", b) }
