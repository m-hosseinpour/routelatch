package httpfront

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPlainHTTPResponseIsRewritten sends a deliberately unusual origin response
// through the plain-HTTP path and shows which parts do not survive verbatim.
func TestPlainHTTPResponseIsRewritten(t *testing.T) {
	const canned = "HTTP/1.1 200 WAT\r\n" +
		"x-custom-lower: One\r\n" +
		"X-Order-B: two\r\n" +
		"X-Order-A: three\r\n" +
		"Content-Length: 5\r\n" +
		"\r\nhello"
	upstream := startTCPServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHeadErr(br); err != nil {
			return
		}
		_, _ = io.WriteString(c, canned)
	})

	var rec addrRecorder
	p := newProxy(t, Config{Dial: mapDial(&rec, upstream)})
	proxyAddr, _ := startProxy(t, p)

	c := dialTCP(t, proxyAddr)
	sendRaw(t, c, "GET http://raw-target.invalid:80/fidelity HTTP/1.1\r\nHost: raw-target.invalid:80\r\nConnection: close\r\n\r\n")
	got := readAllResponse(t, c)
	t.Logf("client received:\n%s", got)

	if line := statusLine(got); !strings.HasPrefix(line, "HTTP/1.1 200") {
		t.Fatalf("status = %q", line)
	}
	// Observed rewriting: the non-standard reason phrase and the missing Date
	// header are not preserved.
	if strings.Contains(string(got), "200 WAT") {
		t.Fatalf("reason phrase unexpectedly survived: %q", statusLine(got))
	}
	if !strings.Contains(string(got), "Date: ") {
		t.Fatalf("expected net/http to add a Date header, got:\n%s", got)
	}
	if !strings.Contains(string(got), "X-Custom-Lower: One") || strings.Contains(string(got), "x-custom-lower:") {
		t.Fatalf("header spelling was not canonicalised, got:\n%s", got)
	}
	if !strings.Contains(string(got), "Content-Type: text/plain; charset=utf-8") {
		t.Fatalf("expected net/http to add a sniffed Content-Type, got:\n%s", got)
	}
	if !bytes.HasSuffix(got, []byte("hello")) {
		t.Fatalf("body bytes were not preserved: %q", got)
	}
}

// TestChunkedFramingIsRewritten shows the chunk extension and the original
// chunk boundaries do not survive; the origin body bytes do.
func TestChunkedFramingIsRewritten(t *testing.T) {
	const canned = "HTTP/1.1 200 OK\r\n" +
		"Content-Type: text/plain\r\n" +
		"Transfer-Encoding: chunked\r\n" +
		"Trailer: X-Sum\r\n" +
		"\r\n" +
		"6;ext=yes\r\nhello \r\n" +
		"3\r\nabc\r\n" +
		"0\r\nX-Sum: 7\r\n\r\n"
	upstream := startTCPServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHeadErr(br); err != nil {
			return
		}
		_, _ = io.WriteString(c, canned)
	})

	var rec addrRecorder
	p := newProxy(t, Config{Dial: mapDial(&rec, upstream)})
	proxyAddr, _ := startProxy(t, p)

	c := dialTCP(t, proxyAddr)
	sendRaw(t, c, "GET http://raw-target.invalid:80/chunk HTTP/1.1\r\nHost: raw-target.invalid:80\r\nConnection: close\r\n\r\n")
	got := readAllResponse(t, c)
	t.Logf("client received:\n%q", got)

	if !bytes.Contains(got, []byte("hello abc")) {
		t.Fatalf("origin body bytes missing: %q", got)
	}
	if bytes.Contains(got, []byte("ext=yes")) {
		t.Fatalf("chunk extension unexpectedly survived: %q", got)
	}
	// Trailers are forwarded via a Trailer announcement or TrailerPrefix; the
	// framing around them is not byte-identical either way.
	t.Logf("trailer value forwarded: %v", bytes.Contains(got, []byte("X-Sum: 7")))
}

// TestRedirectIsNotFollowed checks the proxy does not turn a 302 into a fetch.
func TestRedirectIsNotFollowed(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer backend.Close()

	p := newProxy(t, Config{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return directDial(ctx, network, backend.Listener.Addr().String())
	}})
	proxyAddr, _ := startProxy(t, p)
	c := dialTCP(t, proxyAddr)
	sendRaw(t, c, "GET http://redirect.invalid:80/a HTTP/1.1\r\nHost: redirect.invalid:80\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufioReader(c), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/elsewhere" {
		t.Fatalf("Location = %q, want /elsewhere", loc)
	}
}

func TestHeadAndPostPassThrough(t *testing.T) {
	body := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			body <- string(b)
			fmt.Fprint(w, "posted")
			return
		}
		w.Header().Set("X-Method", r.Method)
		fmt.Fprint(w, "hello")
	}))
	defer backend.Close()

	p := newProxy(t, Config{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return directDial(ctx, network, backend.Listener.Addr().String())
	}})
	proxyAddr, _ := startProxy(t, p)

	t.Run("HEAD", func(t *testing.T) {
		c := dialTCP(t, proxyAddr)
		sendRaw(t, c, "HEAD http://backend.invalid:80/h HTTP/1.1\r\nHost: backend.invalid:80\r\nConnection: close\r\n\r\n")
		resp, err := http.ReadResponse(bufioReader(c), &http.Request{Method: http.MethodHead})
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 || resp.ContentLength != 5 {
			t.Fatalf("status=%d content-length=%d, want 200/5", resp.StatusCode, resp.ContentLength)
		}
		b, _ := io.ReadAll(resp.Body)
		if len(b) != 0 {
			t.Fatalf("HEAD response carried a body: %q", b)
		}
	})

	t.Run("POST", func(t *testing.T) {
		c := dialTCP(t, proxyAddr)
		sendRaw(t, c, "POST http://backend.invalid:80/p HTTP/1.1\r\nHost: backend.invalid:80\r\nContent-Length: 11\r\nConnection: close\r\n\r\nhello world")
		resp, err := http.ReadResponse(bufioReader(c), nil)
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		defer resp.Body.Close()
		got, _ := io.ReadAll(resp.Body)
		if string(got) != "posted" {
			t.Fatalf("response body = %q, want posted", got)
		}
		select {
		case b := <-body:
			if b != "hello world" {
				t.Fatalf("origin received body %q, want hello world", b)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("origin never received the POST body")
		}
	})
}

// TestNonSSEStreamStallsMidBody shows a large chunked body is forwarded in
// pieces but the proxy does not flush after each write: with the origin still
// holding the connection open, the client stalls before the body is complete.
func TestNonSSEStreamStallsMidBody(t *testing.T) {
	firstSent := make(chan struct{})
	release := make(chan struct{})
	payload := bytes.Repeat([]byte("a"), 4096)
	upstream := startTCPServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHeadErr(br); err != nil {
			return
		}
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		_, _ = fmt.Fprintf(c, "%x\r\n%s\r\n", len(payload), payload)
		close(firstSent)
		<-release
		_, _ = io.WriteString(c, "5\r\nsecnd\r\n0\r\n\r\n")
	})

	var rec addrRecorder
	p := newProxy(t, Config{Dial: mapDial(&rec, upstream)})
	proxyAddr, _ := startProxy(t, p)
	c := dialTCP(t, proxyAddr)
	sendRaw(t, c, "GET http://raw-target.invalid:80/stream HTTP/1.1\r\nHost: raw-target.invalid:80\r\n\r\n")
	<-firstSent

	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	_ = c.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	var got []byte
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			break
		}
	}
	if len(got) == 0 || len(got) >= len(payload) {
		t.Fatalf("expected a partial body while the origin held the connection, got %d bytes", len(got))
	}
	t.Logf("OBSERVED: %d of %d origin bytes reached the client before it stalled; no per-write flush off SSE", len(got), len(payload))
	// The client-side body reader is poisoned by the read timeout, so the rest
	// is checked by TestChunkedBodyCompletes with a fresh request.
	close(release)
}

// TestChunkedBodyCompletes verifies that buffering affects timing, not content:
// an ending chunked body arrives intact.
func TestChunkedBodyCompletes(t *testing.T) {
	payload := bytes.Repeat([]byte("b"), 4096)
	upstream := startTCPServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHeadErr(br); err != nil {
			return
		}
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		_, _ = fmt.Fprintf(c, "%x\r\n%s\r\n", len(payload), payload)
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(c, "5\r\nsecnd\r\n0\r\n\r\n")
	})

	var rec addrRecorder
	p := newProxy(t, Config{Dial: mapDial(&rec, upstream)})
	proxyAddr, _ := startProxy(t, p)
	c := dialTCP(t, proxyAddr)
	sendRaw(t, c, "GET http://raw-target.invalid:80/stream HTTP/1.1\r\nHost: raw-target.invalid:80\r\nConnection: close\r\n\r\n")
	_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
	resp, err := http.ReadResponse(bufioReader(c), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read full body: %v", err)
	}
	if !bytes.Equal(body, append(payload, []byte("secnd")...)) {
		t.Fatalf("body bytes were not preserved: got %d, want %d", len(body), len(payload)+5)
	}
}

// TestSmallBinaryStreamDeltaIsBuffered records that a small body write on a
// non-SSE response is held in the ResponseWriter buffer instead of being
// flushed to the client immediately.
func TestSmallBinaryStreamDeltaIsBuffered(t *testing.T) {
	firstSent := make(chan struct{})
	release := make(chan struct{})
	upstream := startTCPServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHeadErr(br); err != nil {
			return
		}
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nfirst\r\n")
		close(firstSent)
		<-release
		_, _ = io.WriteString(c, "5\r\nsecnd\r\n0\r\n\r\n")
	})

	var rec addrRecorder
	p := newProxy(t, Config{Dial: mapDial(&rec, upstream)})
	proxyAddr, _ := startProxy(t, p)
	c := dialTCP(t, proxyAddr)
	sendRaw(t, c, "GET http://raw-target.invalid:80/stream HTTP/1.1\r\nHost: raw-target.invalid:80\r\n\r\n")
	<-firstSent

	br := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(900 * time.Millisecond))
	if _, err := http.ReadResponse(br, nil); err == nil {
		t.Fatalf("small delta was flushed immediately; revisit this finding")
	} else {
		t.Log("OBSERVED: even the response head plus a 5-byte delta was still buffered after 900ms")
	}
	close(release)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response after release: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "firstsecnd" {
		t.Fatalf("body = %q, want firstsecnd", body)
	}
}

// TestSSEStreamFlushes is the control: the frontend special-cases
// text/event-stream and flushes each write.
func TestSSEStreamFlushes(t *testing.T) {
	firstSent := make(chan struct{})
	release := make(chan struct{})
	upstream := startTCPServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		if _, err := readHeadErr(br); err != nil {
			return
		}
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n8\r\ndata: 1\n\r\n")
		close(firstSent)
		<-release
		_, _ = io.WriteString(c, "8\r\ndata: 2\n\r\n0\r\n\r\n")
	})

	var rec addrRecorder
	p := newProxy(t, Config{Dial: mapDial(&rec, upstream)})
	proxyAddr, _ := startProxy(t, p)
	c := dialTCP(t, proxyAddr)
	sendRaw(t, c, "GET http://raw-target.invalid:80/sse HTTP/1.1\r\nHost: raw-target.invalid:80\r\n\r\n")
	<-firstSent

	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, len("data: 1\n"))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("SSE event was not flushed: %v", err)
	}
	close(release)
	_, _ = io.ReadAll(resp.Body)
}

// TestPlainHTTPRetriesIdempotentGET makes the upstream read a GET on a pooled
// connection and close without answering. Go's http.Transport retries the
// idempotent request on a new connection; the client sees one response while
// the origin saw the request twice.
func TestPlainHTTPRetriesIdempotentGET(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	upstream := startTCPServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		for {
			head, err := readHeadErr(br)
			if err != nil {
				return
			}
			line := strings.SplitN(string(head), "\r\n", 2)[0]
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return
			}
			path := fields[1]
			mu.Lock()
			counts[path]++
			n := counts[path]
			mu.Unlock()
			if path == "/two" && n == 1 {
				// Close a reused connection before writing any response.
				return
			}
			_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		}
	})

	var rec addrRecorder
	p := newProxy(t, Config{Dial: mapDial(&rec, upstream)})
	proxyAddr, _ := startProxy(t, p)
	c := dialTCP(t, proxyAddr)
	br := bufio.NewReader(c)

	for _, path := range []string{"/one", "/two"} {
		sendRaw(t, c, "GET http://raw-target.invalid:80"+path+" HTTP/1.1\r\nHost: raw-target.invalid:80\r\n\r\n")
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("%s: read response: %v", path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if string(b) != "ok" {
			t.Fatalf("%s: body = %q", path, b)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	t.Logf("origin request counts: %v", counts)
	if counts["/two"] != 2 {
		t.Fatalf("expected the transport to replay /two on a new connection, counts=%v", counts)
	}
}

// TestAbsoluteHTTPSIsFetcherPath: with the stock configuration an absolute-form
// https request makes the proxy open its own TLS connection to the origin (it
// fails here only because the test certificate is untrusted). The hardened
// configuration refuses the scheme instead.
func TestAbsoluteHTTPSIsFetcherPath(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "fetched-by-proxy")
	}))
	defer backend.Close()
	target := backend.Listener.Addr().String()

	t.Run("stock-config-attempts-fetch", func(t *testing.T) {
		p := newStockProxy(t)
		proxyAddr, _ := startProxy(t, p)
		c := dialTCP(t, proxyAddr)
		sendRaw(t, c, "GET https://"+target+"/ HTTP/1.1\r\nHost: "+target+"\r\nConnection: close\r\n\r\n")
		got := readAllResponse(t, c)
		t.Logf("stock client received:\n%s", got)
		if strings.HasPrefix(string(got), "HTTP/1.1 200") {
			t.Fatal("stock proxy fetched an https origin without CONNECT")
		}
		if !strings.Contains(string(got), "certificate") && !strings.Contains(string(got), "x509") {
			t.Fatalf("expected a TLS verification failure from a proxy-side fetch, got:\n%s", got)
		}
	})

	t.Run("hardened-config-refuses", func(t *testing.T) {
		p := newProxy(t, Config{Dial: directDial})
		proxyAddr, _ := startProxy(t, p)
		c := dialTCP(t, proxyAddr)
		sendRaw(t, c, "GET https://"+target+"/ HTTP/1.1\r\nHost: "+target+"\r\nConnection: close\r\n\r\n")
		got := readAllResponse(t, c)
		if !strings.HasPrefix(string(got), "HTTP/1.1 403") {
			t.Fatalf("want 403 for absolute https, got:\n%s", got)
		}
	})
}
