package frontend

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"routelatch/internal/contract"
)

func readHeadDirect(t *testing.T, c net.Conn) string {
	t.Helper()
	var out []byte
	one := make([]byte, 1)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, err := io.ReadFull(c, one); err != nil {
			t.Fatalf("read head byte: %v", err)
		}
		out = append(out, one[0])
		if bytes.HasSuffix(out, []byte("\r\n\r\n")) {
			return string(out)
		}
		if len(out) > 64<<10 {
			t.Fatalf("head too long")
		}
	}
}

func TestForwardPreservesStatusHeadersAndBodyBytes(t *testing.T) {
	const originResponse = "HTTP/1.1 418 WAT\r\n" +
		"x-custom-lower: Value\r\n" +
		"Date: Mon, 01 Jan 2024 00:00:00 GMT\r\n" +
		"Content-Type: application/x-weird\r\n" +
		"Content-Length: 5\r\n" +
		"\r\n" +
		"hello"

	var gotHead atomic.Value
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		head, err := readHead(br)
		if err != nil {
			return
		}
		gotHead.Store(string(head))
		_ = writeAll(c, []byte(originResponse))
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())

	request := "GET http://" + origin + "/path%2Fa?q=1 HTTP/1.1\r\n" +
		"Host: " + origin + "\r\n" +
		"X-Lower-Case: KeepMe\r\n" +
		"proxy-connection: keep-alive\r\n" +
		"Proxy-Authorization: Basic c2VjcmV0\r\n" +
		"Connection: close\r\n\r\n"
	mustWrite(t, c, request)

	got := string(readAllWithDeadline(t, c, 3*time.Second))
	if got != originResponse {
		t.Fatalf("response was rewritten\n got: %q\nwant: %q", got, originResponse)
	}

	head, _ := gotHead.Load().(string)
	if !strings.HasPrefix(head, "GET /path%2Fa?q=1 HTTP/1.1\r\n") {
		t.Fatalf("request target not origin-form: %q", head)
	}
	if !strings.Contains(head, "X-Lower-Case: KeepMe\r\n") {
		t.Fatalf("header casing was not preserved: %q", head)
	}
	lower := strings.ToLower(head)
	if strings.Contains(lower, "proxy-connection") || strings.Contains(lower, "proxy-authorization") {
		t.Fatalf("proxy-only headers leaked to the origin: %q", head)
	}
	if !strings.Contains(head, "Host: "+origin+"\r\n") {
		t.Fatalf("Host header missing: %q", head)
	}

	specs := s.registry.allSpecs()
	if len(specs) != 1 || specs[0].Kind != contract.ConnHTTPForward {
		t.Fatalf("specs = %+v", specs)
	}
	if specs[0].Decision.PolicyVersion != 1 {
		t.Fatalf("registered version = %d", specs[0].Decision.PolicyVersion)
	}
}

func TestForwardRedirectIsNotFollowed(t *testing.T) {
	var requests atomic.Int32
	const redirect = "HTTP/1.1 302 Found\r\nLocation: /elsewhere\r\nContent-Length: 0\r\n\r\n"
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHead(br); err != nil {
			return
		}
		requests.Add(1)
		_ = writeAll(c, []byte(redirect))
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "GET http://"+origin+"/start HTTP/1.1\r\nHost: "+origin+"\r\nConnection: close\r\n\r\n")
	got := string(readAllWithDeadline(t, c, 3*time.Second))
	if got != redirect {
		t.Fatalf("redirect rewritten: %q", got)
	}
	if requests.Load() != 1 {
		t.Fatalf("origin requests = %d, want 1 (redirect must not be followed)", requests.Load())
	}
}

func TestForwardHEADHasNoBody(t *testing.T) {
	const response = "HTTP/1.1 200 OK\r\nContent-Length: 5\r\nContent-Type: text/plain\r\n\r\n"
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		head, err := readHead(br)
		if err != nil || !strings.HasPrefix(string(head), "HEAD / HTTP/1.1") {
			return
		}
		_ = writeAll(c, []byte(response))
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "HEAD http://"+origin+"/ HTTP/1.1\r\nHost: "+origin+"\r\nConnection: close\r\n\r\n")
	got := string(readAllWithDeadline(t, c, 3*time.Second))
	if got != response {
		t.Fatalf("HEAD response = %q", got)
	}
}

func TestForwardPOSTBodyIsExact(t *testing.T) {
	gotBody := make(chan string, 1)
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		body, _ := io.ReadAll(req.Body)
		gotBody <- string(body)
		_ = writeAll(c, []byte("HTTP/1.1 200 OK\r\nContent-Length: 11\r\n\r\nhello world"))
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	payload := "hello world"
	mustWrite(t, c, "POST http://"+origin+"/echo HTTP/1.1\r\nHost: "+origin+"\r\nContent-Length: 11\r\nConnection: close\r\n\r\n"+payload)
	br := bufio.NewReader(c)
	resp, body := mustReadResponse(t, br, &http.Request{Method: "POST"})
	if resp.StatusCode != 200 || string(body) != payload {
		t.Fatalf("response = %d %q", resp.StatusCode, body)
	}
	select {
	case got := <-gotBody:
		if got != payload {
			t.Fatalf("origin body = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("origin did not receive the body")
	}
}

func TestForwardChunkedResponseKeepsFraming(t *testing.T) {
	const response = "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n" +
		"6;ext=yes\r\nabcdef\r\n" +
		"0\r\n" +
		"X-Trailer: v\r\n\r\n"
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHead(br); err != nil {
			return
		}
		_ = writeAll(c, []byte(response))
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "GET http://"+origin+"/ HTTP/1.1\r\nHost: "+origin+"\r\nConnection: close\r\n\r\n")
	got := string(readAllWithDeadline(t, c, 3*time.Second))
	if got != response {
		t.Fatalf("chunked response was reframed\n got: %q\nwant: %q", got, response)
	}
}

func TestForwardChunkedRequestKeepsFraming(t *testing.T) {
	chunkBody := "5;x=1\r\nhello\r\n0\r\n\r\n"
	gotBody := make(chan string, 1)
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		head, err := readHead(br)
		if err != nil {
			return
		}
		if !strings.Contains(strings.ToLower(string(head)), "transfer-encoding: chunked") {
			gotBody <- "missing transfer-encoding"
			return
		}
		raw := make([]byte, len(chunkBody))
		if _, err := io.ReadFull(br, raw); err != nil {
			gotBody <- "short body"
			return
		}
		gotBody <- string(raw)
		_ = writeAll(c, []byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "POST http://"+origin+"/ HTTP/1.1\r\nHost: "+origin+"\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n"+chunkBody)
	br := bufio.NewReader(c)
	resp, body := mustReadResponse(t, br, &http.Request{Method: "POST"})
	if resp.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("response = %d %q", resp.StatusCode, body)
	}
	select {
	case got := <-gotBody:
		if got != chunkBody {
			t.Fatalf("origin raw body = %q, want %q", got, chunkBody)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("origin did not receive the request body")
	}
}

func TestForwardSmallDeltaIsFlushed(t *testing.T) {
	const head = "HTTP/1.1 200 OK\r\nContent-Length: 15\r\n\r\n"
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHead(br); err != nil {
			return
		}
		_ = writeAll(c, []byte(head+"first"))
		time.Sleep(500 * time.Millisecond)
		_ = writeAll(c, []byte("0123456789"))
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "GET http://"+origin+"/stream HTTP/1.1\r\nHost: "+origin+"\r\n\r\n")
	br := bufio.NewReader(c)
	start := time.Now()
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	first := make([]byte, 5)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatalf("read first delta: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("first body delta took %s; it was buffered until the rest arrived", elapsed)
	}
	if string(first) != "first" {
		t.Fatalf("first delta = %q", first)
	}
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	if string(rest) != "0123456789" {
		t.Fatalf("rest = %q", rest)
	}
}

func TestForwardTenMiBStreamsBeforeCompletion(t *testing.T) {
	const total = 10<<20 + 37
	release := make(chan struct{})
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHead(br); err != nil {
			return
		}
		head := "HTTP/1.1 200 OK\r\nContent-Length: " + itoa(total) + "\r\nConnection: close\r\n\r\n"
		if err := writeAll(c, []byte(head+"X")); err != nil {
			return
		}
		<-release
		chunk := bytes.Repeat([]byte("z"), 64<<10)
		remaining := total - 1
		for remaining > 0 {
			n := len(chunk)
			if n > remaining {
				n = remaining
			}
			if _, err := c.Write(chunk[:n]); err != nil {
				return
			}
			remaining -= n
		}
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "GET http://"+origin+"/big HTTP/1.1\r\nHost: "+origin+"\r\n\r\n")
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var first [1]byte
	if _, err := io.ReadFull(resp.Body, first[:]); err != nil {
		t.Fatalf("first body byte was not streamed: %v", err)
	}
	if first[0] != 'X' {
		t.Fatalf("first byte = %q", first[0])
	}
	close(release)
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	rest := make([]byte, total-1)
	if _, err := io.ReadFull(resp.Body, rest); err != nil {
		t.Fatalf("read remaining body: %v", err)
	}
	if len(rest) != total-1 {
		t.Fatalf("read %d bytes", len(rest))
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func TestConnectSeesOriginCertificate(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "origin-ok")
	}))
	defer origin.Close()

	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	authority := origin.Listener.Addr().String()
	mustWrite(t, c, "CONNECT "+authority+" HTTP/1.1\r\nHost: "+authority+"\r\n\r\n")
	head := readHeadDirect(t, c)
	if !strings.Contains(head, "200") {
		t.Fatalf("CONNECT reply = %q", head)
	}

	tc := tls.Client(c, &tls.Config{InsecureSkipVerify: true})
	if err := tc.Handshake(); err != nil {
		t.Fatalf("TLS through tunnel: %v", err)
	}
	peer := tc.ConnectionState().PeerCertificates
	if len(peer) == 0 || !bytes.Equal(peer[0].Raw, origin.Certificate().Raw) {
		t.Fatalf("client saw a certificate that is not the origin's; MITM or interception path exists")
	}
	if _, err := io.WriteString(tc, "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write over TLS: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(tc), &http.Request{Method: "GET"})
	if err != nil {
		t.Fatalf("read TLS response: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "origin-ok" {
		t.Fatalf("body = %q", body)
	}
}

func TestConnectReplaysPipelinedClientBytes(t *testing.T) {
	echo := startEchoServer(t)
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT "+echo+" HTTP/1.1\r\nHost: "+echo+"\r\n\r\nEARLY!")
	head := readHeadDirect(t, c)
	if !strings.Contains(head, "200") {
		t.Fatalf("CONNECT reply = %q", head)
	}
	got := make([]byte, len("EARLY!"))
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("read echoed early bytes: %v", err)
	}
	if string(got) != "EARLY!" {
		t.Fatalf("early bytes = %q", got)
	}
	specs := s.registry.allSpecs()
	if len(specs) != 1 || specs[0].Kind != contract.ConnHTTPConnect {
		t.Fatalf("specs = %+v", specs)
	}
}

func TestPoolFailureIsMappedAndNeverDialsDirectly(t *testing.T) {
	s := newStack(t, func(s *testStack) {
		s.router.fn = func(contract.Destination) (contract.Decision, error) {
			return contract.Decision{
				Mode: contract.ModeProxyAll, Class: contract.ClassOther,
				Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1,
			}, nil
		}
		s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			return contract.PoolDialResult{}, &contract.Failure{
				Kind: contract.PoolUnavailable, Path: contract.PathPool,
				Op: "pool.dial", Message: "no usable exit",
			}
		}
	})

	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	got := string(readAllWithDeadline(t, c, 3*time.Second))
	if !strings.HasPrefix(got, "HTTP/1.1 502 ") {
		t.Fatalf("CONNECT failure = %q", got)
	}
	if s.direct.count() != 0 {
		t.Fatalf("direct dials after pool failure = %d (must be 0)", s.direct.count())
	}
	if s.pool.count() != 1 {
		t.Fatalf("pool calls = %d", s.pool.count())
	}

	c2 := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c2, "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n")
	got2 := string(readAllWithDeadline(t, c2, 3*time.Second))
	if !strings.HasPrefix(got2, "HTTP/1.1 502 ") {
		t.Fatalf("forward failure = %q", got2)
	}
	if s.direct.count() != 0 {
		t.Fatalf("direct dials after forward pool failure = %d", s.direct.count())
	}
}

func TestPreReplyErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		request string
		mutate  func(*testStack)
		want    string
	}{
		{
			name:    "invalid CONNECT authority",
			request: "CONNECT example.com HTTP/1.1\r\nHost: example.com\r\n\r\n",
			want:    "HTTP/1.1 400 ",
		},
		{
			name:    "policy refused",
			request: "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n",
			mutate: func(s *testStack) {
				s.router.fn = func(contract.Destination) (contract.Decision, error) {
					return contract.Decision{
						Mode: contract.ModeTargeted, Class: contract.ClassSelf,
						Route: contract.RouteRefuse, Reason: contract.ReasonSelfLoop, PolicyVersion: 1,
					}, nil
				}
			},
			want: "HTTP/1.1 403 ",
		},
		{
			name:    "pool timeout",
			request: "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n",
			mutate: func(s *testStack) {
				s.router.fn = func(contract.Destination) (contract.Decision, error) {
					return contract.Decision{
						Mode: contract.ModeProxyAll, Class: contract.ClassOther,
						Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1,
					}, nil
				}
				s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
					return contract.PoolDialResult{}, &contract.Failure{
						Kind: contract.Timeout, Path: contract.PathPool,
						Op: "pool.dial", Message: "deadline exceeded",
					}
				}
			},
			want: "HTTP/1.1 504 ",
		},
		{
			name:    "direct refused",
			request: "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n",
			mutate: func(s *testStack) {
				s.direct.fn = func(context.Context, contract.Destination) (net.Conn, error) {
					return nil, &contract.Failure{
						Kind: contract.ConnectRefused, Path: contract.PathDirect,
						Op: "direct.dial", Message: "connection refused",
					}
				}
			},
			want: "HTTP/1.1 502 ",
		},
		{
			name:    "absolute https has no MITM path",
			request: "GET https://example.com/ HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n",
			want:    "HTTP/1.1 403 ",
		},
		{
			name:    "unknown scheme",
			request: "GET ftp://example.com/file HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n",
			want:    "HTTP/1.1 501 ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStack(t, tc.mutate)
			c := dialProxy(t, s.f.HTTPAddr().String())
			mustWrite(t, c, tc.request)
			got := string(readAllWithDeadline(t, c, 3*time.Second))
			if !strings.HasPrefix(got, tc.want) {
				t.Fatalf("response = %q, want status %q", got, tc.want)
			}
		})
	}
}

func TestClientAbortTearsDownUpstream(t *testing.T) {
	originFailed := make(chan struct{}, 1)
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHead(br); err != nil {
			return
		}
		if err := writeAll(c, []byte("HTTP/1.1 200 OK\r\nContent-Length: 134217728\r\n\r\n")); err != nil {
			originFailed <- struct{}{}
			return
		}
		chunk := bytes.Repeat([]byte("a"), 64<<10)
		for {
			if _, err := c.Write(chunk); err != nil {
				originFailed <- struct{}{}
				return
			}
		}
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "GET http://"+origin+"/abort HTTP/1.1\r\nHost: "+origin+"\r\n\r\n")
	br := bufio.NewReader(c)
	if _, err := http.ReadResponse(br, &http.Request{Method: "GET"}); err != nil {
		t.Fatalf("read response head: %v", err)
	}
	some := make([]byte, 1024)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(br, some); err != nil {
		t.Fatalf("read initial body: %v", err)
	}
	_ = c.Close()

	select {
	case <-originFailed:
	case <-time.After(5 * time.Second):
		t.Fatal("origin kept writing after the client aborted; upstream was not torn down")
	}
	h := s.registry.firstHandle()
	waitFor(t, 2*time.Second, func() bool { return h != nil && h.isClosed() }, "handle close")
}

func TestConnectClientAbortTearsDownUpstream(t *testing.T) {
	originFailed := make(chan struct{}, 1)
	origin := startRawServer(t, func(c net.Conn) {
		chunk := bytes.Repeat([]byte("b"), 64<<10)
		for {
			if _, err := c.Write(chunk); err != nil {
				originFailed <- struct{}{}
				return
			}
		}
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT "+origin+" HTTP/1.1\r\nHost: "+origin+"\r\n\r\n")
	if head := readHeadDirect(t, c); !strings.Contains(head, "200") {
		t.Fatalf("CONNECT reply = %q", head)
	}
	buf := make([]byte, 1024)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read tunnel bytes: %v", err)
	}
	_ = c.Close()
	select {
	case <-originFailed:
	case <-time.After(5 * time.Second):
		t.Fatal("origin kept writing after the client aborted the tunnel")
	}
}

func TestRegistryCloseTearsDownBothPeers(t *testing.T) {
	originClosed := make(chan struct{}, 1)
	echo := startRawServer(t, func(c net.Conn) {
		_, _ = io.Copy(c, c)
		originClosed <- struct{}{}
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT "+echo+" HTTP/1.1\r\nHost: "+echo+"\r\n\r\nping")
	if head := readHeadDirect(t, c); !strings.Contains(head, "200") {
		t.Fatalf("CONNECT reply = %q", head)
	}
	echoed := make([]byte, 4)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, echoed); err != nil || string(echoed) != "ping" {
		t.Fatalf("echo = %q, err=%v", echoed, err)
	}
	h := s.registry.firstHandle()
	if h == nil {
		t.Fatal("no registered handle")
	}
	go func() { _ = h.Close() }()
	if !isClosed(c, 2*time.Second) {
		t.Fatal("client did not observe teardown")
	}
	select {
	case <-originClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("origin did not observe teardown")
	}
}

func TestKeepAliveReuseGetsFreshDialAndNoReplay(t *testing.T) {
	var mu atomic.Int32
	var targets [2]atomic.Value
	upstreams := atomic.Int32{}
	origin := startRawServer(t, func(c net.Conn) {
		idx := mu.Add(1) - 1
		upstreams.Add(1)
		br := bufio.NewReader(c)
		head, err := readHead(br)
		if err != nil {
			return
		}
		line := strings.SplitN(string(head), "\r\n", 2)[0]
		if idx < int32(len(targets)) {
			targets[idx].Store(line)
		}
		_ = writeAll(c, []byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	br := bufio.NewReader(c)

	mustWrite(t, c, "GET http://"+origin+"/one HTTP/1.1\r\nHost: "+origin+"\r\n\r\n")
	resp, body := mustReadResponse(t, br, &http.Request{Method: "GET"})
	if resp.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("first response = %d %q", resp.StatusCode, body)
	}
	mustWrite(t, c, "GET http://"+origin+"/two HTTP/1.1\r\nHost: "+origin+"\r\n\r\n")
	resp, body = mustReadResponse(t, br, &http.Request{Method: "GET"})
	if resp.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("second response = %d %q", resp.StatusCode, body)
	}

	if got := s.direct.count(); got != 2 {
		t.Fatalf("direct dials = %d, want 2 (one fresh upstream per request)", got)
	}
	if got := upstreams.Load(); got != 2 {
		t.Fatalf("origin connections = %d, want 2", got)
	}
	first, _ := targets[0].Load().(string)
	second, _ := targets[1].Load().(string)
	if first != "GET /one HTTP/1.1" || second != "GET /two HTTP/1.1" {
		t.Fatalf("origin saw %q and %q; a request was replayed or reordered", first, second)
	}
	if got := s.registry.handleCount(); got != 2 {
		t.Fatalf("registered requests = %d, want 2", got)
	}
}

func TestOriginCloseBeforeResponseIsNotRetried(t *testing.T) {
	var requests atomic.Int32
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHead(br); err != nil {
			return
		}
		requests.Add(1)
		// Close without any response.
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "GET http://"+origin+"/once HTTP/1.1\r\nHost: "+origin+"\r\nConnection: close\r\n\r\n")
	got := string(readAllWithDeadline(t, c, 3*time.Second))
	if !strings.HasPrefix(got, "HTTP/1.1 502 ") {
		t.Fatalf("response = %q", got)
	}
	if requests.Load() != 1 {
		t.Fatalf("origin saw the request %d times; no replay is allowed", requests.Load())
	}
	if s.direct.count() != 1 {
		t.Fatalf("direct dials = %d, want exactly 1", s.direct.count())
	}
}

func TestConnectHalfClosePropagates(t *testing.T) {
	received := make(chan string, 1)
	origin := startRawServer(t, func(c net.Conn) {
		body, _ := io.ReadAll(c)
		received <- string(body)
		_ = writeAll(c, []byte("BYE"))
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT "+origin+" HTTP/1.1\r\nHost: "+origin+"\r\n\r\n")
	if head := readHeadDirect(t, c); !strings.Contains(head, "200") {
		t.Fatalf("CONNECT reply = %q", head)
	}
	mustWrite(t, c, "ping")
	tcp, ok := c.(*net.TCPConn)
	if !ok {
		t.Fatalf("client conn is %T", c)
	}
	if err := tcp.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	rest := readAllWithDeadline(t, c, 3*time.Second)
	if string(rest) != "BYE" {
		t.Fatalf("after half-close the client got %q, want %q", rest, "BYE")
	}
	select {
	case got := <-received:
		if got != "ping" {
			t.Fatalf("origin received %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("origin never saw the half-close")
	}
}

func TestAfter200TeardownWritesNoErrorPage(t *testing.T) {
	origin := startRawServer(t, func(net.Conn) {
		// Accept and close: the tunnel is established, then the peer is gone.
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT "+origin+" HTTP/1.1\r\nHost: "+origin+"\r\n\r\n")
	head := readHeadDirect(t, c)
	if !strings.Contains(head, "200") {
		t.Fatalf("CONNECT reply = %q", head)
	}
	rest := readAllWithDeadline(t, c, 3*time.Second)
	if len(rest) != 0 {
		t.Fatalf("bytes after a 2xx: %q; only teardown is allowed", rest)
	}
}

func TestConnectPoolAcceptedSends200(t *testing.T) {
	echo := startEchoServer(t)
	s := newStack(t, func(s *testStack) {
		s.router.fn = func(contract.Destination) (contract.Decision, error) {
			return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonApprovedRule, RuleID: "r-1", PolicyVersion: 1}, nil
		}
		s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			conn, err := net.Dial("tcp", echo)
			if err != nil {
				return contract.PoolDialResult{}, err
			}
			return contract.PoolDialResult{Conn: conn, Exit: "x-1", Tag: "rl-x", Proof: contract.ProofAccepted}, nil
		}
	})
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT 127.0.0.1:80 HTTP/1.1\r\nHost: 127.0.0.1:80\r\n\r\nhi")
	head := readHeadDirect(t, c)
	if !strings.Contains(head, "200") {
		t.Fatalf("CONNECT reply = %q", head)
	}
	echoed := make([]byte, 2)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, echoed); err != nil || string(echoed) != "hi" {
		t.Fatalf("echo = %q err=%v", echoed, err)
	}
	h := s.registry.firstHandle()
	if h == nil || h.proofClass() != contract.ProofAccepted || h.info().Exit != "x-1" {
		t.Fatalf("handle = %+v", h.info())
	}
	if s.direct.count() != 0 {
		t.Fatalf("direct dials = %d", s.direct.count())
	}
}

func TestConnectPoolWithoutProofIsRefused(t *testing.T) {
	echo := startEchoServer(t)
	s := newStack(t, func(s *testStack) {
		s.router.fn = func(contract.Destination) (contract.Decision, error) {
			return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1}, nil
		}
		s.pool.fn = func(context.Context, contract.PoolDialRequest) (contract.PoolDialResult, error) {
			conn, err := net.Dial("tcp", echo)
			if err != nil {
				return contract.PoolDialResult{}, err
			}
			return contract.PoolDialResult{Conn: conn, Exit: "x-1", Proof: contract.ProofUnknown}, nil
		}
	})
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT 127.0.0.1:80 HTTP/1.1\r\nHost: 127.0.0.1:80\r\n\r\n")
	got := string(readAllWithDeadline(t, c, 3*time.Second))
	// A pool conn without a proof class is a wiring defect, not a routing
	// outcome: §3.2 maps internal to 500, and the important guarantee is that
	// no 2xx is ever sent on a guess.
	if !strings.HasPrefix(got, "HTTP/1.1 500 ") {
		t.Fatalf("reply = %q; a proof-less pool result must not be a 2xx", got)
	}
	if s.direct.count() != 0 {
		t.Fatalf("direct dials = %d", s.direct.count())
	}
}

func TestForwardExpectContinueIsRelayed(t *testing.T) {
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		head, err := readHead(br)
		if err != nil {
			return
		}
		if !strings.Contains(strings.ToLower(string(head)), "expect: 100-continue") {
			return
		}
		if err := writeAll(c, []byte("HTTP/1.1 100 Continue\r\n\r\n")); err != nil {
			return
		}
		body := make([]byte, 4)
		if _, err := io.ReadFull(br, body); err != nil {
			return
		}
		_ = writeAll(c, []byte("HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\n"+string(body)))
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	br := bufio.NewReader(c)
	mustWrite(t, c, "POST http://"+origin+"/upload HTTP/1.1\r\nHost: "+origin+"\r\nContent-Length: 4\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n")
	interim, err := http.ReadResponse(br, &http.Request{Method: "POST"})
	if err != nil {
		t.Fatalf("read interim response: %v", err)
	}
	if interim.StatusCode != 100 {
		t.Fatalf("interim status = %d", interim.StatusCode)
	}
	mustWrite(t, c, "data")
	final, body := mustReadResponse(t, br, &http.Request{Method: "POST"})
	if final.StatusCode != 200 || string(body) != "data" {
		t.Fatalf("final = %d %q", final.StatusCode, body)
	}
}

func TestForwardCloseDelimitedBodyStreamsToEOF(t *testing.T) {
	const response = "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\nabcdef"
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHead(br); err != nil {
			return
		}
		if err := writeAll(c, []byte(response)); err != nil {
			return
		}
		// Returning closes the origin connection: the response has no length
		// and is delimited by EOF.
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "GET http://"+origin+"/open HTTP/1.1\r\nHost: "+origin+"\r\n\r\n")
	got := string(readAllWithDeadline(t, c, 3*time.Second))
	if got != response {
		t.Fatalf("close-delimited response = %q", got)
	}
}

func TestConnectKeepsHostnameUnresolved(t *testing.T) {
	echo := startEchoServer(t)
	var seen atomic.Value
	s := newStack(t, func(s *testStack) {
		s.direct.fn = func(_ context.Context, dst contract.Destination) (net.Conn, error) {
			seen.Store(dst)
			return net.Dial("tcp", echo)
		}
	})
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT no-such-name.invalid:443 HTTP/1.1\r\nHost: no-such-name.invalid:443\r\n\r\n")
	if head := readHeadDirect(t, c); !strings.Contains(head, "200") {
		t.Fatalf("CONNECT reply = %q", head)
	}
	dst, ok := seen.Load().(contract.Destination)
	if !ok {
		t.Fatal("dialer never ran")
	}
	if dst.Kind != contract.DestDomain || dst.Host != "no-such-name.invalid" || dst.Port != 443 {
		t.Fatalf("destination = %+v; hostname was resolved or rewritten", dst)
	}
}

func TestForwardKeepsHostnameUnresolved(t *testing.T) {
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHead(br); err != nil {
			return
		}
		_ = writeAll(c, []byte("HTTP/1.1 204 No Content\r\n\r\n"))
	})
	var seen atomic.Value
	s := newStack(t, func(s *testStack) {
		s.direct.fn = func(_ context.Context, dst contract.Destination) (net.Conn, error) {
			seen.Store(dst)
			return net.Dial("tcp", origin)
		}
	})
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "GET http://plain-name.invalid:80/p HTTP/1.1\r\nHost: plain-name.invalid\r\nConnection: close\r\n\r\n")
	br := bufio.NewReader(c)
	resp, _ := mustReadResponse(t, br, &http.Request{Method: "GET"})
	if resp.StatusCode != 204 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	dst, ok := seen.Load().(contract.Destination)
	if !ok {
		t.Fatal("dialer never ran")
	}
	if dst.Kind != contract.DestDomain || dst.Host != "plain-name.invalid" || dst.Port != 80 {
		t.Fatalf("destination = %+v; hostname was resolved or rewritten", dst)
	}
}

func TestClientGoneCancelsPendingConnectDial(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	var startOnce, cancelOnce sync.Once
	s := newStack(t, func(s *testStack) {
		s.router.fn = func(contract.Destination) (contract.Decision, error) {
			return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1}, nil
		}
		s.pool.fn = func(ctx context.Context, _ contract.PoolDialRequest) (contract.PoolDialResult, error) {
			startOnce.Do(func() { close(started) })
			<-ctx.Done()
			cancelOnce.Do(func() { close(canceled) })
			return contract.PoolDialResult{}, ctx.Err()
		}
	})
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("pool dial never started")
	}
	resetConn(t, c)
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("resetting the client did not cancel the pending CONNECT dial")
	}
}

func TestClientGoneCancelsPendingForwardDial(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	var startOnce, cancelOnce sync.Once
	s := newStack(t, func(s *testStack) {
		s.router.fn = func(contract.Destination) (contract.Decision, error) {
			return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1}, nil
		}
		s.pool.fn = func(ctx context.Context, _ contract.PoolDialRequest) (contract.PoolDialResult, error) {
			startOnce.Do(func() { close(started) })
			<-ctx.Done()
			cancelOnce.Do(func() { close(canceled) })
			return contract.PoolDialResult{}, ctx.Err()
		}
	})
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("pool dial never started")
	}
	resetConn(t, c)
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("resetting the client did not cancel the pending forward dial")
	}
}

func TestForwardPipelinedRequestsAreNotLost(t *testing.T) {
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		head, err := readHead(br)
		if err != nil {
			return
		}
		line := strings.SplitN(string(head), "\r\n", 2)[0]
		body := strings.TrimPrefix(strings.Fields(line)[1], "/")
		_ = writeAll(c, []byte("HTTP/1.1 200 OK\r\nContent-Length: "+itoa(len(body))+"\r\n\r\n"+body))
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	// Both requests are sent before either response is read.
	mustWrite(t, c, "GET http://"+origin+"/one HTTP/1.1\r\nHost: "+origin+"\r\n\r\n"+
		"GET http://"+origin+"/two HTTP/1.1\r\nHost: "+origin+"\r\n\r\n")
	br := bufio.NewReader(c)
	for _, want := range []string{"one", "two"} {
		resp, body := mustReadResponse(t, br, &http.Request{Method: "GET"})
		if resp.StatusCode != 200 || string(body) != want {
			t.Fatalf("response = %d %q, want %q", resp.StatusCode, body, want)
		}
	}
	if got := s.direct.count(); got != 2 {
		t.Fatalf("direct dials = %d, want 2", got)
	}
}

func TestConnectHalfCloseBeforeReplyDoesNotCancelDial(t *testing.T) {
	release := make(chan struct{})
	dialDone := make(chan struct{})
	canceled := make(chan struct{}, 1)
	origin := startRawServer(t, func(c net.Conn) {
		_ = writeAll(c, []byte("HELLO"))
		_, _ = io.Copy(io.Discard, c)
	})
	s := newStack(t, func(s *testStack) {
		s.router.fn = func(contract.Destination) (contract.Decision, error) {
			return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1}, nil
		}
		s.pool.fn = func(ctx context.Context, _ contract.PoolDialRequest) (contract.PoolDialResult, error) {
			conn, err := net.Dial("tcp", origin)
			if err != nil {
				return contract.PoolDialResult{}, err
			}
			select {
			case <-release:
				close(dialDone)
				return contract.PoolDialResult{Conn: conn, Exit: "x-1", Proof: contract.ProofConfirmed}, nil
			case <-ctx.Done():
				_ = conn.Close()
				canceled <- struct{}{}
				return contract.PoolDialResult{}, ctx.Err()
			}
		}
	})
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "CONNECT 127.0.0.1:80 HTTP/1.1\r\nHost: 127.0.0.1:80\r\n\r\n")
	tcp, ok := c.(*net.TCPConn)
	if !ok {
		t.Fatalf("client conn is %T", c)
	}
	if err := tcp.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	select {
	case <-canceled:
		t.Fatal("a clean FIN (half-close) canceled the pending dial")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case <-dialDone:
	case <-time.After(2 * time.Second):
		t.Fatal("dial did not complete after release")
	}
	if head := readHeadDirect(t, c); !strings.Contains(head, "200") {
		t.Fatalf("CONNECT reply = %q", head)
	}
	got := make([]byte, 5)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "HELLO" {
		t.Fatalf("tunnel data = %q err=%v", got, err)
	}
}

func TestForwardHalfCloseBeforeReplyDoesNotCancelDial(t *testing.T) {
	release := make(chan struct{})
	dialDone := make(chan struct{})
	canceled := make(chan struct{}, 1)
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHead(br); err != nil {
			return
		}
		_ = writeAll(c, []byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK"))
	})
	s := newStack(t, func(s *testStack) {
		s.router.fn = func(contract.Destination) (contract.Decision, error) {
			return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1}, nil
		}
		s.pool.fn = func(ctx context.Context, _ contract.PoolDialRequest) (contract.PoolDialResult, error) {
			conn, err := net.Dial("tcp", origin)
			if err != nil {
				return contract.PoolDialResult{}, err
			}
			select {
			case <-release:
				close(dialDone)
				return contract.PoolDialResult{Conn: conn, Exit: "x-1", Proof: contract.ProofConfirmed}, nil
			case <-ctx.Done():
				_ = conn.Close()
				canceled <- struct{}{}
				return contract.PoolDialResult{}, ctx.Err()
			}
		}
	})
	c := dialProxy(t, s.f.HTTPAddr().String())
	mustWrite(t, c, "GET http://127.0.0.1:80/ HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n")
	tcp, ok := c.(*net.TCPConn)
	if !ok {
		t.Fatalf("client conn is %T", c)
	}
	if err := tcp.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	select {
	case <-canceled:
		t.Fatal("a clean FIN (half-close) canceled the pending forward dial")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case <-dialDone:
	case <-time.After(2 * time.Second):
		t.Fatal("dial did not complete after release")
	}
	br := bufio.NewReader(c)
	resp, body := mustReadResponse(t, br, &http.Request{Method: "GET"})
	if resp.StatusCode != 200 || string(body) != "OK" {
		t.Fatalf("response = %d %q", resp.StatusCode, body)
	}
}

func TestConnectEarlyStreamAcrossCaptureCapIsNotTruncated(t *testing.T) {
	const total = 70_000
	payload := make([]byte, total)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}
	release := make(chan struct{})
	echo := startEchoServer(t)
	s := newStack(t, func(s *testStack) {
		s.router.fn = func(contract.Destination) (contract.Decision, error) {
			return contract.Decision{Route: contract.RoutePool, Reason: contract.ReasonProxyAll, PolicyVersion: 1}, nil
		}
		s.pool.fn = func(ctx context.Context, _ contract.PoolDialRequest) (contract.PoolDialResult, error) {
			conn, err := net.Dial("tcp", echo)
			if err != nil {
				return contract.PoolDialResult{}, err
			}
			select {
			case <-release:
				return contract.PoolDialResult{Conn: conn, Exit: "x-1", Proof: contract.ProofConfirmed}, nil
			case <-ctx.Done():
				_ = conn.Close()
				return contract.PoolDialResult{}, ctx.Err()
			}
		}
	})
	c := dialProxy(t, s.f.HTTPAddr().String())
	head := []byte("CONNECT 127.0.0.1:80 HTTP/1.1\r\nHost: 127.0.0.1:80\r\n\r\n")
	if err := writeAll(c, append(head, payload...)); err != nil {
		t.Fatalf("write early stream: %v", err)
	}
	// Let the pre-reply watch reach its capture cap, then let the dial return.
	time.Sleep(150 * time.Millisecond)
	close(release)
	if reply := readHeadDirect(t, c); !strings.Contains(reply, "200") {
		t.Fatalf("CONNECT reply = %q", reply)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, total)
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("read %d of %d early bytes: %v", len(got), total, err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("early stream was truncated or reordered at the capture cap")
	}
}

func TestForward101SwitchesToBidirectionalTunnel(t *testing.T) {
	const serverFirst = "SERVER-FIRST"
	originClosed := make(chan struct{}, 1)
	origin := startRawServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		head, err := readHead(br)
		if err != nil {
			return
		}
		lower := strings.ToLower(string(head))
		if !strings.Contains(lower, "upgrade: test") {
			return
		}
		if err := writeAll(c, []byte(
			"HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n"+serverFirst)); err != nil {
			return
		}
		// origin -> client first (proves the origin can initiate), then echo
		// the client's early bytes (proves client -> origin after the 101).
		echo := make([]byte, 4)
		if _, err := io.ReadFull(br, echo); err != nil {
			return
		}
		if err := writeAll(c, echo); err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, br)
		originClosed <- struct{}{}
	})
	s := newStack(t, nil)
	c := dialProxy(t, s.f.HTTPAddr().String())
	// "ping" is pipelined with the upgrade request.
	mustWrite(t, c, "GET http://"+origin+"/upgrade HTTP/1.1\r\nHost: "+origin+
		"\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\nping")
	if head := readHeadDirect(t, c); !strings.Contains(head, "101") {
		t.Fatalf("upgrade reply = %q", head)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, len(serverFirst))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != serverFirst {
		t.Fatalf("origin-first bytes = %q err=%v", got, err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(c, echo); err != nil || string(echo) != "ping" {
		t.Fatalf("echoed early bytes = %q err=%v", echo, err)
	}
	_ = c.Close()
	select {
	case <-originClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("origin did not observe tunnel teardown")
	}
}
