package acceptance

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// destination peer: counts contacts and records when the far end tears down
// ---------------------------------------------------------------------------

type echoPeer struct {
	ln net.Listener

	accepts  atomic.Int64
	readEOFs atomic.Int64

	mu    sync.Mutex
	conns []net.Conn
	once  sync.Once
}

func newEchoPeer(t *testing.T) *echoPeer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo peer listen: %v", err)
	}
	p := &echoPeer{ln: ln}
	go p.serve()
	t.Cleanup(p.close)
	return p
}

// close stops the peer and every accepted connection. It is idempotent.
func (p *echoPeer) close() {
	p.once.Do(func() {
		_ = p.ln.Close()
		p.mu.Lock()
		conns := p.conns
		p.conns = nil
		p.mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
}

func (p *echoPeer) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.accepts.Add(1)
		p.mu.Lock()
		p.conns = append(p.conns, c)
		p.mu.Unlock()
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := c.Read(buf)
				if n > 0 {
					if _, werr := c.Write(buf[:n]); werr != nil {
						break
					}
				}
				if err != nil {
					if err == io.EOF {
						p.readEOFs.Add(1)
					}
					break
				}
			}
			_ = c.Close()
		}()
	}
}

func (p *echoPeer) port() uint16 { return uint16(p.ln.Addr().(*net.TCPAddr).Port) }
func (p *echoPeer) contacts() int64 {
	return p.accepts.Load()
}
func (p *echoPeer) eofs() int64 { return p.readEOFs.Load() }

// httpPeer is a loopback HTTP destination: status, header and body are fixed so
// the test can assert they reach the client byte for byte.
type httpPeer struct {
	ln       net.Listener
	srv      *http.Server
	requests atomic.Int64
	body     string
}

func newHTTPPeer(t *testing.T, body string) *httpPeer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("http peer listen: %v", err)
	}
	p := &httpPeer{ln: ln, body: body}
	p.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.requests.Add(1)
		w.Header().Set("X-RL-Destination", "loopback-fixture")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus) // 207: a status a fetcher would hide
		_, _ = io.WriteString(w, p.body)
	})}
	go func() { _ = p.srv.Serve(ln) }()
	t.Cleanup(func() { _ = p.srv.Close() })
	return p
}

func (p *httpPeer) port() uint16 { return uint16(p.ln.Addr().(*net.TCPAddr).Port) }

// ---------------------------------------------------------------------------
// SOCKS5 exit we run ourselves
// ---------------------------------------------------------------------------

// socksDial is one CONNECT request as the exit saw it. Addr is the untouched
// authority RouteLatch sent: the test asserts it is still a hostname/IP and
// that it is not a resolved loopback address.
type socksDial struct {
	Host string
	Port int
	At   time.Time
	OK   bool
}

type socksExit struct {
	ln net.Listener

	accepts atomic.Int64
	dials   atomic.Int64 // successfully connected destination dials

	mu     sync.Mutex
	seen   []socksDial
	stalls map[string]bool
	closes []time.Time // when the product closed the exit-side connection
	conns  []net.Conn
	once   sync.Once
}

func newSOCKSExit(t *testing.T) *socksExit {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("socks exit listen: %v", err)
	}
	x := &socksExit{ln: ln, stalls: map[string]bool{}}
	go x.serve()
	t.Cleanup(x.close)
	return x
}

// close stops the exit and every accepted connection. It is idempotent.
func (x *socksExit) close() {
	x.once.Do(func() {
		_ = x.ln.Close()
		x.mu.Lock()
		conns := x.conns
		x.conns = nil
		x.mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
}

func (x *socksExit) port() uint16 { return uint16(x.ln.Addr().(*net.TCPAddr).Port) }

func (x *socksExit) stallHost(host string) {
	x.mu.Lock()
	x.stalls[host] = true
	x.mu.Unlock()
}

func (x *socksExit) unstallHost(host string) {
	x.mu.Lock()
	delete(x.stalls, host)
	x.mu.Unlock()
}

func (x *socksExit) observations() []socksDial {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]socksDial(nil), x.seen...)
}

func (x *socksExit) closeTimes() []time.Time {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]time.Time(nil), x.closes...)
}

func (x *socksExit) dialCount() int64 { return x.dials.Load() }

func (x *socksExit) drainCount() int64 { return x.accepts.Load() }

func (x *socksExit) serve() {
	for {
		c, err := x.ln.Accept()
		if err != nil {
			return
		}
		x.accepts.Add(1)
		x.mu.Lock()
		x.conns = append(x.conns, c)
		x.mu.Unlock()
		go x.handle(c)
	}
}

func (x *socksExit) handle(c net.Conn) {
	defer func() {
		_ = c.Close()
		x.mu.Lock()
		x.closes = append(x.closes, time.Now())
		x.mu.Unlock()
	}()

	br := bufio.NewReader(c)
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil || head[0] != 5 {
		return
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	// No-auth is offered and accepted; a username/password-only exit is not
	// needed for this flow.
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}

	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil {
		return
	}
	if req[0] != 5 {
		return
	}
	host, port, err := readSocksAddr(br, req[3])
	if err != nil {
		return
	}
	at := time.Now()
	dial := socksDial{Host: host, Port: port, At: at}
	if req[1] != 1 { // CONNECT only
		_, _ = c.Write(socksReply(0x07))
		x.appendDial(dial)
		return
	}

	x.mu.Lock()
	stall := x.stalls[host]
	x.mu.Unlock()
	if stall {
		x.appendDial(dial)
		// Accept the request and never answer: this is a pending pool dial that
		// only a mode-change cancel can end.
		_, _ = io.Copy(io.Discard, br)
		return
	}

	dest, derr := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 3*time.Second)
	if derr != nil {
		dial.OK = false
		x.appendDial(dial)
		_, _ = c.Write(socksReply(0x05))
		return
	}
	defer dest.Close()
	dial.OK = true
	x.appendDial(dial)
	x.dials.Add(1)
	if _, err := c.Write(socksReply(0x00)); err != nil {
		return
	}

	clientGone := make(chan struct{})
	go func() {
		_, _ = io.Copy(dest, br)
		close(clientGone)
		if tc, ok := dest.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	go func() {
		_, _ = io.Copy(c, dest)
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	<-clientGone
}

func (x *socksExit) appendDial(d socksDial) {
	x.mu.Lock()
	x.seen = append(x.seen, d)
	x.mu.Unlock()
}

func readSocksAddr(br *bufio.Reader, atyp byte) (string, int, error) {
	var host string
	switch atyp {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(br, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()
	case 0x04:
		b := make([]byte, 16)
		if _, err := io.ReadFull(br, b); err != nil {
			return "", 0, err
		}
		host = net.IP(b).String()
	case 0x03:
		n, err := br.ReadByte()
		if err != nil {
			return "", 0, err
		}
		b := make([]byte, int(n))
		if _, err := io.ReadFull(br, b); err != nil {
			return "", 0, err
		}
		host = string(b)
	default:
		return "", 0, fmt.Errorf("unknown atyp %d", atyp)
	}
	var pb [2]byte
	if _, err := io.ReadFull(br, pb[:]); err != nil {
		return "", 0, err
	}
	return host, int(binary.BigEndian.Uint16(pb[:])), nil
}

func socksReply(code byte) []byte {
	return []byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0}
}

// ---------------------------------------------------------------------------
// test clients: HTTP CONNECT, SOCKS5, plain HTTP forwarding
// ---------------------------------------------------------------------------

// connectHTTP opens a CONNECT tunnel through the product's HTTP listener and
// returns the status code. A non-200 reply is consumed and closed; the redacted
// reason text is available from connectHTTPStatus.
func connectHTTP(t *testing.T, proxyAddr, target string) (net.Conn, *bufio.Reader, int, error) {
	conn, br, code, _, err := connectHTTPStatus(t, proxyAddr, target)
	return conn, br, code, err
}

// connectHTTPStatus is connectHTTP plus the redacted error reason, which the
// product writes into the response body (never into the status line).
func connectHTTPStatus(t *testing.T, proxyAddr, target string) (net.Conn, *bufio.Reader, int, string, error) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		return nil, nil, 0, "", err
	}
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\nProxy-Connection: keep-alive\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		_ = conn.Close()
		return nil, nil, 0, "", err
	}
	br := bufio.NewReader(conn)
	statusLine, err := readLineWithin(t, br, 20*time.Second)
	if err != nil {
		_ = conn.Close()
		return nil, nil, 0, "", err
	}
	parts := strings.SplitN(strings.TrimSpace(statusLine), " ", 3)
	if len(parts) < 2 {
		_ = conn.Close()
		return nil, nil, 0, "", fmt.Errorf("malformed status line %q", statusLine)
	}
	code, _ := strconv.Atoi(parts[1])
	for {
		line, err := readLineWithin(t, br, 20*time.Second)
		if err != nil {
			_ = conn.Close()
			return nil, nil, code, "", err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	if code == 200 {
		return conn, br, code, "", nil
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	reason, _ := io.ReadAll(br)
	_ = conn.Close()
	return nil, nil, code, strings.TrimSpace(string(reason)), nil
}

// tunnelEcho writes payload through an established tunnel and reads the echo.
func tunnelEcho(t *testing.T, conn net.Conn, br *bufio.Reader, payload string) (string, error) {
	t.Helper()
	if _, err := io.WriteString(conn, payload); err != nil {
		return "", err
	}
	buf := make([]byte, len(payload))
	deadline := time.Now().Add(10 * time.Second)
	_ = conn.SetReadDeadline(deadline)
	if _, err := io.ReadFull(br, buf); err != nil {
		return "", err
	}
	_ = conn.SetReadDeadline(time.Time{})
	return string(buf), nil
}

// socksConnect performs a full SOCKS5 CONNECT and returns the wire reply code
// (0x00 success, 0x04 pool/policy failure, 0x05 direct refused, ...).
func socksConnect(t *testing.T, proxyAddr, host string, port int) (net.Conn, byte, error) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		return nil, 0, err
	}
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		_ = conn.Close()
		return nil, 0, err
	}
	br := bufio.NewReader(conn)
	var greet [2]byte
	if _, err := io.ReadFull(br, greet[:]); err != nil {
		_ = conn.Close()
		return nil, 0, err
	}
	if greet[0] != 5 || greet[1] != 0 {
		_ = conn.Close()
		return nil, 0, fmt.Errorf("unexpected greeting %v", greet)
	}
	req := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req = append(req, 0x01)
			req = append(req, ip4...)
		} else {
			req = append(req, 0x04)
			req = append(req, ip.To16()...)
		}
	} else {
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	var pb [2]byte
	binary.BigEndian.PutUint16(pb[:], uint16(port))
	req = append(req, pb[:]...)
	if _, err := conn.Write(req); err != nil {
		_ = conn.Close()
		return nil, 0, err
	}
	var head [4]byte
	if _, err := io.ReadFull(br, head[:]); err != nil {
		_ = conn.Close()
		return nil, 0, err
	}
	code := head[1]
	if _, _, err := readSocksAddr(br, head[3]); err != nil {
		_ = conn.Close()
		return nil, code, err
	}
	if code != 0 {
		_ = conn.Close()
		return nil, code, nil
	}
	return conn, code, nil
}

// httpForwardThroughProxy sends one absolute-form request and returns the
// response. The client never follows redirects and never retries.
func httpForwardThroughProxy(t *testing.T, proxyAddr, rawRequest string, timeout time.Duration) (*http.Response, string, error) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		return nil, "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := io.WriteString(conn, rawRequest); err != nil {
		return nil, "", err
	}
	req, _ := http.NewRequest("GET", "http://fixture.invalid/", nil)
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return nil, "", err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(body), nil
}
