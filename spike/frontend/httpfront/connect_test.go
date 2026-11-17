package httpfront

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/elazarl/goproxy"
)

func directDial(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// TestConnectIsNotMITM drives a real TLS handshake through the CONNECT tunnel
// and compares the certificate the client sees with the origin's certificate.
// MITM would substitute a goproxy CA certificate here.
func TestConnectIsNotMITM(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "origin")
	}))
	defer backend.Close()
	originCert := backend.Certificate()
	backendAddr := backend.Listener.Addr().String()

	p := newProxy(t, Config{Dial: directDial})
	proxyAddr, _ := startProxy(t, p)

	c := dialTCP(t, proxyAddr)
	// Extra headers try to look like something a proxy might act on; MITM in
	// goproxy is reachable only through a configured HttpsHandler, so none of
	// them can change the path.
	sendRaw(t, c, fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: keep-alive\r\nUpgrade: h2c\r\nX-Mitm: true\r\n\r\n", backendAddr, backendAddr))
	head := readHeadConn(t, c)
	if !strings.HasPrefix(string(head), "HTTP/1.1 200") {
		t.Fatalf("CONNECT failed: %q", head)
	}

	tlsConn := tls.Client(c, &tls.Config{InsecureSkipVerify: true}) // test-only: comparing raw certs
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake through tunnel: %v", err)
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("no peer certificate")
	}
	if !bytes.Equal(state.PeerCertificates[0].Raw, originCert.Raw) {
		t.Fatalf("client did not see the origin certificate: proxy intercepted TLS (MITM)")
	}
	sendRaw(t, tlsConn, "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	body, err := io.ReadAll(tlsConn)
	if err != nil {
		t.Fatalf("read through tunnel: %v", err)
	}
	if !strings.HasSuffix(strings.TrimRight(string(body), "\x00"), "origin") {
		t.Fatalf("unexpected tunnel body: %q", body)
	}
}

// TestConnectKeepsHostnameUnresolved proves the CONNECT authority reaches the
// custom dialer verbatim and that the proxy did not try to resolve it.
func TestConnectKeepsHostnameUnresolved(t *testing.T) {
	var rec addrRecorder
	var calls atomic.Int32
	sentinel := errors.New("T1 sentinel: dial refused by test")
	p := newProxy(t, Config{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		calls.Add(1)
		rec.add(addr)
		return nil, sentinel
	}})
	proxyAddr, _ := startProxy(t, p)

	const target = "no-such-name.invalid:443"
	c := dialTCP(t, proxyAddr)
	sendRaw(t, c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	resp := readAllResponse(t, c)

	if got := rec.last(); got != target {
		t.Fatalf("dialer received %q, want unresolved %q", got, target)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("CONNECT dial was attempted %d times; a failed dial must not be retried", n)
	}
	if !strings.HasPrefix(string(resp), "HTTP/1.1 502") {
		t.Fatalf("dial failure did not produce a 502: %q", resp)
	}
	if !strings.Contains(string(resp), sentinel.Error()) {
		t.Fatalf("client did not receive the proxy-level error: %q", resp)
	}
	if strings.Contains(string(resp), "no such host") || strings.Contains(string(resp), "lookup ") {
		t.Fatalf("response indicates the frontend resolved the name itself: %q", resp)
	}
}

// TestAbsoluteFormKeepsHostnameUnresolved proves the same for absolute-form
// plain HTTP forwarded through http.Transport.
func TestAbsoluteFormKeepsHostnameUnresolved(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "plain-ok")
	}))
	defer backend.Close()

	var rec addrRecorder
	p := newProxy(t, Config{Dial: mapDial(&rec, backend.Listener.Addr().String())})
	proxyAddr, _ := startProxy(t, p)

	c := dialTCP(t, proxyAddr)
	sendRaw(t, c, "GET http://plain-name.invalid:80/probe HTTP/1.1\r\nHost: plain-name.invalid:80\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufioReader(c), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if got := rec.last(); got != "plain-name.invalid:80" {
		t.Fatalf("dialer received %q, want unresolved %q", got, "plain-name.invalid:80")
	}
	if string(body) != "plain-ok" {
		t.Fatalf("body = %q, want plain-ok", body)
	}
}

func bufioReader(c net.Conn) *bufio.Reader { return bufio.NewReader(c) }

// TestConnectReplyWaitsForDial shows the 200 is not written while the upstream
// dial is still pending, and that a dial error produces 502 instead of 200.
func TestConnectReplyWaitsForDial(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	backendAddr := backend.Listener.Addr().String()

	t.Run("success", func(t *testing.T) {
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		p := newProxy(t, Config{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			entered <- struct{}{}
			<-release
			return directDial(ctx, network, backendAddr)
		}})
		proxyAddr, _ := startProxy(t, p)
		c := dialTCP(t, proxyAddr)
		sendRaw(t, c, "CONNECT upstream.invalid:80 HTTP/1.1\r\nHost: upstream.invalid:80\r\n\r\n")
		<-entered

		// No reply while the dial is blocked.
		_ = c.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
		one := make([]byte, 1)
		if n, err := c.Read(one); n != 0 || err == nil {
			t.Fatalf("client received bytes before the upstream dial finished: %q", one[:n])
		}
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		close(release)
		head := readHeadConn(t, c)
		if !strings.HasPrefix(string(head), "HTTP/1.1 200") {
			t.Fatalf("expected 200 after dial, got %q", head)
		}
	})

	t.Run("failure", func(t *testing.T) {
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		poolErr := errors.New("pool unavailable: no healthy outbound")
		p := newProxy(t, Config{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			entered <- struct{}{}
			<-release
			return nil, poolErr
		}})
		proxyAddr, _ := startProxy(t, p)
		c := dialTCP(t, proxyAddr)
		sendRaw(t, c, "CONNECT upstream.invalid:80 HTTP/1.1\r\nHost: upstream.invalid:80\r\n\r\n")
		<-entered
		close(release)
		resp := readAllResponse(t, c)
		if !strings.HasPrefix(string(resp), "HTTP/1.1 502") {
			t.Fatalf("want 502 before any 200, got %q", resp)
		}
		if !strings.Contains(string(resp), poolErr.Error()) {
			t.Fatalf("want the pool error in the reply, got %q", resp)
		}
	})
}

// TestConnectReplyTriggerIsDialReturnValue shows the reply trigger is the
// returned net.Conn itself: a net.Pipe that never reaches a destination is
// enough to produce 200. No post-dial readiness proof is consulted; the gate
// can only live inside the dialer (source: https.go:401 writes the 200 right
// after connectDial returns, before any copy starts).
func TestConnectReplyTriggerIsDialReturnValue(t *testing.T) {
	proxyEnd, destinationEnd := net.Pipe()
	defer destinationEnd.Close()

	p := newProxy(t, Config{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return proxyEnd, nil // not a connection to any destination
	}})
	proxyAddr, _ := startProxy(t, p)
	c := dialTCP(t, proxyAddr)
	sendRaw(t, c, "CONNECT upstream.invalid:80 HTTP/1.1\r\nHost: upstream.invalid:80\r\n\r\n")

	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	head := readHeadConn(t, c)
	if !strings.HasPrefix(string(head), "HTTP/1.1 200") {
		t.Fatalf("expected 200 triggered by the dial return value, got %q", head)
	}
	t.Logf("OBSERVED: 200 sent for a net.Pipe that never reaches a destination; reply trigger is the dial return value")
}

// TestConnectHijackDropsEarlyClientBytes: a client may write the CONNECT request
// and its first protocol bytes in one write. net/http buffers the surplus in the
// hijacked bufio.Reader. ConnectAccept replays it; ConnectHijack receives only
// the raw conn, so those bytes are lost.
func TestConnectHijackDropsEarlyClientBytes(t *testing.T) {
	first := make(chan string, 1)
	late := make(chan string, 1)
	p := newStockProxy(t)
	p.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		return &goproxy.ConnectAction{
			Action: goproxy.ConnectHijack,
			Hijack: func(req *http.Request, client net.Conn, ctx *goproxy.ProxyCtx) {
				defer client.Close()
				buf := make([]byte, 64)
				_ = client.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
				n, _ := client.Read(buf)
				first <- string(buf[:n])
				_, _ = io.WriteString(client, "HTTP/1.1 200 OK\r\n\r\n")
				_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
				n, _ = client.Read(buf)
				late <- string(buf[:n])
			},
		}, host
	}))
	proxyAddr, _ := startProxy(t, p)
	c := dialTCP(t, proxyAddr)

	// One write: CONNECT plus six bytes of "client hello" payload.
	const early = "EARLY!"
	if _, err := io.WriteString(c, "CONNECT target.invalid:443 HTTP/1.1\r\nHost: target.invalid:443\r\n\r\n"+early); err != nil {
		t.Fatalf("write: %v", err)
	}
	head := readHeadConn(t, c)
	if !strings.HasPrefix(string(head), "HTTP/1.1 200") {
		t.Fatalf("hijack handler did not produce 200: %q", head)
	}
	sendRaw(t, c, "LATER!")
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))

	if got := <-first; got != "" {
		t.Fatalf("ConnectHijack delivered early bytes %q; expected them to be lost", got)
	}
	if got := <-late; got != "LATER!" {
		t.Fatalf("bytes written after the reply were %q, want LATER!", got)
	}
}

// TestConnectAcceptReplaysEarlyClientBytes is the control for the previous test.
func TestConnectAcceptReplaysEarlyClientBytes(t *testing.T) {
	got := make(chan string, 1)
	upstream := startTCPServer(t, func(c net.Conn) {
		buf := make([]byte, 64)
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _ := c.Read(buf)
		got <- string(buf[:n])
	})

	p := newProxy(t, Config{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return directDial(ctx, network, upstream)
	}})
	proxyAddr, _ := startProxy(t, p)
	c := dialTCP(t, proxyAddr)
	if _, err := io.WriteString(c, "CONNECT target.invalid:443 HTTP/1.1\r\nHost: target.invalid:443\r\n\r\nEARLY!"); err != nil {
		t.Fatalf("write: %v", err)
	}
	head := readHeadConn(t, c)
	if !strings.HasPrefix(string(head), "HTTP/1.1 200") {
		t.Fatalf("CONNECT failed: %q", head)
	}
	select {
	case b := <-got:
		if b != "EARLY!" {
			t.Fatalf("upstream got %q, want EARLY!", b)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream never received the early client bytes")
	}
}

// TestAfterReplyFailureClosesTunnel: once 200 has been sent the proxy cannot
// report a proxy-level error inside the tunnel; the only signal is TCP teardown.
func TestAfterReplyFailureClosesTunnel(t *testing.T) {
	upstream := startTCPServer(t, func(c net.Conn) { _ = c.Close() })
	p := newProxy(t, Config{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return directDial(ctx, network, upstream)
	}})
	proxyAddr, _ := startProxy(t, p)
	c := dialTCP(t, proxyAddr)
	sendRaw(t, c, "CONNECT upstream.invalid:80 HTTP/1.1\r\nHost: upstream.invalid:80\r\n\r\n")
	head := readHeadConn(t, c)
	if !strings.HasPrefix(string(head), "HTTP/1.1 200") {
		t.Fatalf("want 200 (dial succeeded), got %q", head)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := c.Read(make([]byte, 64))
	if err == nil {
		t.Fatalf("read %d bytes after upstream close; want EOF without an injected error page", n)
	}
	requireTeardown(t, "client after upstream close", err)
	t.Logf("OBSERVED: after 2xx the only failure signal is teardown (%v); no proxy-level error can follow", err)
}

// TestConnectToUDPPortIsNotUDP: CONNECT always dials TCP. A destination that
// only has a UDP socket cannot be reached, so goproxy offers no UDP path.
func TestConnectToUDPPortIsNotUDP(t *testing.T) {
	uc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer uc.Close()

	p := newProxy(t, Config{Dial: directDial})
	proxyAddr, _ := startProxy(t, p)
	c := dialTCP(t, proxyAddr)
	target := uc.LocalAddr().String()
	sendRaw(t, c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	resp := readAllResponse(t, c)
	if !strings.HasPrefix(string(resp), "HTTP/1.1 502") {
		t.Fatalf("CONNECT to a UDP-only port should fail as TCP, got %q", resp)
	}
	_ = uc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := uc.ReadFromUDP(make([]byte, 16)); err == nil {
		t.Fatal("a datagram reached the UDP socket; CONNECT must not become UDP")
	}
}

// hijackCapture is only used by the workaround test below: it records the
// bufio.ReadWriter that net/http hands to Hijack so the surplus request bytes
// can be replayed. goproxy discards that reader on the ConnectHijack path.
type hijackCapture struct {
	http.ResponseWriter
	rw chan *bufio.ReadWriter
}

func (w *hijackCapture) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := w.ResponseWriter.(http.Hijacker).Hijack()
	if err == nil {
		w.rw <- rw
	}
	return c, rw, err
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// TestConnectHijackWorkaroundRecoversEarlyBytes shows the only way to combine
// ConnectHijack's reply control with early client bytes: wrap the
// ResponseWriter, steal the hijacked bufio.Reader before goproxy drops it, and
// replay it ourselves. This depends on plumbing outside goproxy's documented
// contract; the library's own early-bytes tests only cover ConnectAccept.
func TestConnectHijackWorkaroundRecoversEarlyBytes(t *testing.T) {
	got := make(chan string, 1)
	capture := &hijackCapture{rw: make(chan *bufio.ReadWriter, 1)}
	p := newStockProxy(t)
	p.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		return &goproxy.ConnectAction{
			Action: goproxy.ConnectHijack,
			Hijack: func(req *http.Request, client net.Conn, ctx *goproxy.ProxyCtx) {
				defer client.Close()
				bc := &bufferedConn{Conn: client}
				select {
				case rw := <-capture.rw:
					bc.r = rw.Reader
				case <-time.After(time.Second):
				}
				_, _ = io.WriteString(client, "HTTP/1.1 200 OK\r\n\r\n")
				buf := make([]byte, 64)
				_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
				n, _ := bc.Read(buf)
				got <- string(buf[:n])
			},
		}, host
	}))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.ServeHTTP(&hijackCapture{ResponseWriter: w, rw: capture.rw}, r)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { _ = srv.Close() })

	c := dialTCP(t, ln.Addr().String())
	if _, err := io.WriteString(c, "CONNECT target.invalid:443 HTTP/1.1\r\nHost: target.invalid:443\r\n\r\nEARLY!"); err != nil {
		t.Fatalf("write: %v", err)
	}
	head := readHeadConn(t, c)
	if !strings.HasPrefix(string(head), "HTTP/1.1 200") {
		t.Fatalf("CONNECT failed: %q", head)
	}
	select {
	case b := <-got:
		if b != "EARLY!" {
			t.Fatalf("handler read %q, want EARLY!", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler never saw the early bytes")
	}
}

// TestDefaultConfigUsesEnvironmentProxy demonstrates that the stock constructor
// let an environment variable redirect plain HTTP instead of using Dial.
func TestDefaultConfigUsesEnvironmentProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	sentinel, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen sentinel: %v", err)
	}
	defer sentinel.Close()

	hit := make(chan string, 1)
	go func() {
		c, err := sentinel.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufioReader(c)
		line, _ := br.ReadString('\n')
		hit <- line
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
	}()

	cmd := execCommandSubprocess(t, "TestEnvProxyChild", []string{
		"HTTP_PROXY=http://" + sentinel.Addr().String(),
		"http_proxy=http://" + sentinel.Addr().String(),
		"NO_PROXY=", "no_proxy=",
	})
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	select {
	case line := <-hit:
		if !strings.Contains(line, "env-proxy.invalid") {
			t.Fatalf("sentinel got %q", line)
		}
		t.Logf("OBSERVED: stock transport sent the request to HTTP_PROXY: %q", strings.TrimSpace(line))
	case <-time.After(5 * time.Second):
		t.Fatalf("environment proxy was not used; child output:\n%s", out)
	}
}
