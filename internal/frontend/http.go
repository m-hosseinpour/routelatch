package frontend

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"routelatch/internal/contract"
)

// httpProxy serves both HTTP request kinds on one listener: an absolute-form
// request is forwarded byte-for-byte through a per-request upstream
// connection, and CONNECT becomes an opaque tunnel. The listener itself is a
// net/http.Server so accept, first-head parsing and shutdown stay standard;
// every request is hijacked immediately because the standard ResponseWriter
// cannot forward status text, header casing or chunk framing unchanged
// (spike/frontend FINDINGS C8).
type httpProxy struct {
	f *Frontend
}

func newHTTPProxy(f *Frontend) *httpProxy { return &httpProxy{f: f} }

func (p *httpProxy) server() *http.Server {
	return &http.Server{
		Handler: http.HandlerFunc(p.handle),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, connCtxKey{}, c)
		},
		BaseContext:    func(net.Listener) context.Context { return p.f.baseCtx },
		ErrorLog:       log.New(io.Discard, "", 0),
		MaxHeaderBytes: maxHeadBytes,
	}
}

// handle hijacks the connection, recovers the exact head net/http parsed, then
// runs the raw request loop. No TLS, certificate or interception path exists
// here: a CONNECT target is reached through a plain byte tunnel.
func (p *httpProxy) handle(w http.ResponseWriter, r *http.Request) {
	if !p.f.beginHandler() {
		return
	}
	defer p.f.endHandler()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "routelatch: proxy error", http.StatusInternalServerError)
		return
	}
	raw, bufrw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = raw.Close() }()
	rc, _ := r.Context().Value(connCtxKey{}).(*recordingConn)
	if rc == nil {
		return
	}
	head, err := rc.firstHead()
	if err != nil {
		return
	}
	rc.stopRecording()
	if !p.f.trackConn(raw) {
		_ = raw.Close()
		return
	}
	defer p.f.untrackConn(raw)

	p.serve(raw, bufrw.Reader, head)
}

// serve parses one head at a time. A CONNECT consumes the rest of the
// connection; a forwarded request may leave the client connection usable for
// the next request, in which case the next head is read from the same reader.
func (p *httpProxy) serve(c net.Conn, br *bufio.Reader, head []byte) {
	for {
		req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(head)))
		if err != nil {
			p.writeHTTPError(c, contract.InvalidDestination)
			return
		}
		if req.Method == "CONNECT" {
			p.serveConnect(c, br, req)
			return
		}
		keep, next := p.serveForward(c, br, req, head)
		if !keep {
			return
		}
		br = next
		nextHead, err := readHead(br)
		if err != nil {
			return
		}
		head = nextHead
	}
}

// serveConnect establishes one tunnel. The 200 is written only after the dial
// returned a usable connection; every failure before that point becomes a
// mapped HTTP error and no direct attempt is ever made in place of a failed
// pool dial (D11, docs/contracts.md §2.2, §3.3).
func (p *httpProxy) serveConnect(c net.Conn, br *bufio.Reader, req *http.Request) {
	dst, ferr := p.f.destination(req.RequestURI)
	if ferr != nil {
		p.writeHTTPError(c, ferr.Kind)
		return
	}
	dec, ferr := p.f.decide(p.f.baseCtx, dst)
	if ferr != nil {
		p.writeHTTPError(c, ferr.Kind)
		return
	}
	h, ferr := p.f.register(contract.ConnHTTPConnect, dst, dec, c)
	if ferr != nil {
		p.writeHTTPError(c, ferr.Kind)
		return
	}
	defer func() {
		p.f.untrackHandle(h)
		_ = h.Close()
	}()

	// A client that disappears while the dial is pending must cancel it
	// (canceled includes "client went away"; the pending-dial bound is 50ms).
	watch := startClientWatch(c, br, func() { _ = h.Close() })
	upstream, exit, proof, ferr := p.f.dial(h.Context(), dec, dst)
	captured, clientGone := watch.stop()
	if ferr != nil {
		p.writeHTTPError(c, ferr.Kind)
		return
	}
	if clientGone {
		_ = upstream.Close()
		_ = h.Close()
		return
	}
	if err := h.SetUpstream(upstream); err != nil {
		_ = upstream.Close()
		p.writeHTTPError(c, failureOf(err).Kind)
		return
	}
	if exit != "" {
		h.SetExit(exit)
	}
	if proof != contract.ProofUnknown {
		h.SetProof(proof)
	}
	if err := writeAll(c, []byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	clientReader := io.Reader(br)
	if len(captured) > 0 {
		clientReader = io.MultiReader(bytes.NewReader(captured), br)
	}
	tunnel(h.Context(), c, clientReader, upstream, bufio.NewReader(upstream))
}

// serveForward relays one absolute-form request. It returns true only when the
// client connection may carry another request, along with the reader that
// holds any bytes the pre-reply liveness watch consumed.
func (p *httpProxy) serveForward(c net.Conn, br *bufio.Reader, req *http.Request, head []byte) (bool, *bufio.Reader) {
	if req.URL == nil {
		p.writeHTTPError(c, contract.InvalidDestination)
		return false, nil
	}
	scheme := strings.ToLower(req.URL.Scheme)
	switch scheme {
	case "http":
	case "https":
		// Forwarding an absolute https URI would require terminating TLS at
		// the proxy. That path does not exist in this product (no MITM).
		p.writeHTTPError(c, contract.PolicyDenied)
		return false, nil
	default:
		p.writeHTTPError(c, contract.Unsupported)
		return false, nil
	}
	authority, ok := httpAuthority(req.URL)
	if !ok {
		p.writeHTTPError(c, contract.InvalidDestination)
		return false, nil
	}
	dst, ferr := p.f.destination(authority)
	if ferr != nil {
		p.writeHTTPError(c, ferr.Kind)
		return false, nil
	}
	dec, ferr := p.f.decide(p.f.baseCtx, dst)
	if ferr != nil {
		p.writeHTTPError(c, ferr.Kind)
		return false, nil
	}

	// The client socket outlives this request when keep-alive is used, so the
	// registry gets a per-request view whose Close can be detached after the
	// response. A policy close before detach closes the shared socket.
	shared := &detachableConn{Conn: c}
	h, ferr := p.f.register(contract.ConnHTTPForward, dst, dec, shared)
	if ferr != nil {
		p.writeHTTPError(c, ferr.Kind)
		return false, nil
	}
	defer p.f.untrackHandle(h)

	// The client may pipeline body bytes (or the next request) while the dial
	// is pending; the watch avoids losing them and cancels a vanished client.
	watch := startClientWatch(c, br, func() { _ = h.Close() })
	upstream, exit, proof, ferr := p.f.dial(h.Context(), dec, dst)
	captured, clientGone := watch.stop()
	if ferr != nil {
		p.writeHTTPError(c, ferr.Kind)
		_ = h.Close()
		return false, nil
	}
	if clientGone {
		_ = upstream.Close()
		_ = h.Close()
		return false, nil
	}
	if err := h.SetUpstream(upstream); err != nil {
		_ = upstream.Close()
		p.writeHTTPError(c, failureOf(err).Kind)
		_ = h.Close()
		return false, nil
	}
	if exit != "" {
		h.SetExit(exit)
	}
	if proof != contract.ProofUnknown {
		h.SetProof(proof)
	}

	reader := br
	if len(captured) > 0 {
		reader = bufio.NewReaderSize(io.MultiReader(bytes.NewReader(captured), br), 4096)
	}

	upstreamHead, ok := rewriteRequestHead(head, req.URL)
	if !ok {
		p.writeHTTPError(c, contract.InvalidDestination)
		_ = h.Close()
		return false, nil
	}
	if err := writeAll(upstream, upstreamHead); err != nil {
		p.writeHTTPSendFailure(c, dec.Route)
		_ = h.Close()
		return false, nil
	}

	bodyCh := startRequestBody(upstream, reader, req)
	upbr := bufio.NewReaderSize(upstream, 4096)

	closeAfter := req.Close
	wroteResponse := false
	for {
		respHead, err := readHead(upbr)
		if err != nil {
			if !wroteResponse {
				p.writeHTTPSendFailure(c, dec.Route)
			}
			_ = h.Close()
			<-bodyCh
			return false, nil
		}
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(respHead)), req)
		if err != nil {
			if !wroteResponse {
				p.writeHTTPSendFailure(c, dec.Route)
			}
			_ = h.Close()
			<-bodyCh
			return false, nil
		}
		if err := writeAll(c, respHead); err != nil {
			_ = h.Close()
			<-bodyCh
			return false, nil
		}
		wroteResponse = true

		if resp.StatusCode >= 100 && resp.StatusCode < 200 && resp.StatusCode != http.StatusSwitchingProtocols {
			continue
		}
		if resp.StatusCode == http.StatusSwitchingProtocols {
			// 101 hands the connection to another protocol. Only splice when
			// the request body finished; otherwise the client stream framing
			// is unknown and the connection must be torn down.
			select {
			case bodyErr := <-bodyCh:
				if bodyErr != nil {
					_ = h.Close()
					return false, nil
				}
				tunnel(h.Context(), c, reader, upstream, upbr)
			default:
				_ = h.Close()
				<-bodyCh
			}
			return false, nil
		}

		if resp.Close {
			closeAfter = true
		}
		closeDelimited := !responseHasNoBody(resp, req) &&
			!isChunked(resp.TransferEncoding) && resp.ContentLength < 0
		if err := copyResponseBody(c, upbr, resp, req); err != nil {
			_ = h.Close()
			<-bodyCh
			return false, nil
		}
		if closeDelimited {
			closeAfter = true
		}
		break
	}

	bodyDone := false
	select {
	case bodyErr := <-bodyCh:
		bodyDone = true
		if bodyErr != nil {
			closeAfter = true
		}
	default:
		// The origin answered before the client finished uploading. Reusing
		// this client connection would consume the remaining body bytes as a
		// new request, so it is closed instead.
		closeAfter = true
	}

	if closeAfter {
		_ = h.Close()
		if !bodyDone {
			<-bodyCh
		}
		return false, nil
	}
	shared.detach()
	_ = h.Close()
	return true, reader
}

func responseHasNoBody(resp *http.Response, req *http.Request) bool {
	return req.Method == "HEAD" ||
		resp.StatusCode == http.StatusNoContent ||
		resp.StatusCode == http.StatusNotModified ||
		(resp.StatusCode >= 100 && resp.StatusCode < 200)
}

// copyResponseBody forwards exactly the response body the head declared,
// preserving chunk framing. A close-delimited body is streamed until EOF.
func copyResponseBody(dst io.Writer, br *bufio.Reader, resp *http.Response, req *http.Request) error {
	if responseHasNoBody(resp, req) {
		return nil
	}
	if isChunked(resp.TransferEncoding) {
		return copyChunked(dst, br)
	}
	if resp.ContentLength >= 0 {
		_, err := io.CopyN(dst, br, resp.ContentLength)
		return err
	}
	_, err := io.Copy(dst, br)
	return err
}

// startRequestBody streams the request body with its original framing to the
// upstream. A body failure closes the upstream so a blocked response read
// cannot outlive an aborted upload.
func startRequestBody(upstream net.Conn, br *bufio.Reader, req *http.Request) <-chan error {
	ch := make(chan error, 1)
	switch {
	case isChunked(req.TransferEncoding):
		go func() {
			err := copyChunked(upstream, br)
			if err != nil {
				_ = upstream.Close()
			}
			ch <- err
		}()
	case req.ContentLength > 0:
		go func() {
			_, err := io.CopyN(upstream, br, req.ContentLength)
			if err != nil {
				_ = upstream.Close()
			}
			ch <- err
		}()
	default:
		ch <- nil
	}
	return ch
}

// rewriteRequestHead swaps the absolute-form target for its origin-form
// equivalent and removes the two proxy-only headers. Method, protocol version,
// header names/casing/order and all other bytes are preserved.
func rewriteRequestHead(head []byte, u *url.URL) ([]byte, bool) {
	targetStart, targetEnd, ok := requestLineBounds(head)
	if !ok {
		return nil, false
	}
	target := originTarget(u)
	tail := stripProxyHeaders(head[targetEnd:])
	out := make([]byte, 0, targetStart+len(target)+len(tail))
	out = append(out, head[:targetStart]...)
	out = append(out, target...)
	out = append(out, tail...)
	return out, true
}

// httpAuthority turns an absolute http URL's host into host:port for the
// router. A syntactically present but non-numeric port is rejected rather than
// defaulted to 80.
func httpAuthority(u *url.URL) (string, bool) {
	if u.Opaque != "" || u.Host == "" {
		return "", false
	}
	host := u.Hostname()
	if host == "" {
		return "", false
	}
	if port := u.Port(); port != "" {
		return net.JoinHostPort(host, port), true
	}
	if hostHasPort(u.Host) {
		return "", false
	}
	return net.JoinHostPort(host, "80"), true
}

func hostHasPort(host string) bool {
	if strings.HasPrefix(host, "[") {
		if i := strings.LastIndexByte(host, ']'); i >= 0 {
			return len(host) > i+1
		}
		return false
	}
	return strings.Count(host, ":") == 1
}

// writeHTTPSendFailure maps a failure that happened after a successful dial but
// before any origin byte reached the client. Direct and pool paths share the
// 502 status; the kind keeps the path distinguishable in a report.
func (p *httpProxy) writeHTTPSendFailure(c net.Conn, route contract.Route) {
	kind := contract.ExitUnreachable
	if route == contract.RouteDirect {
		kind = contract.ConnectRefused
	}
	p.writeHTTPError(c, kind)
}

func (p *httpProxy) writeHTTPError(c net.Conn, kind contract.ErrorKind) {
	status := httpStatusFor(kind)
	body := "routelatch: " + httpReasonText(kind) + "\n"
	head := fmt.Sprintf(
		"HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(body), body)
	_ = writeAll(c, []byte(head))
}

// detachableConn is the per-request client view handed to the registry. A
// policy close before the request finishes closes the shared socket; after the
// request finishes the frontend detaches it and retires the handle without
// destroying the client's keep-alive connection.
type detachableConn struct {
	net.Conn
	mu       sync.Mutex
	detached bool
}

func (d *detachableConn) Close() error {
	d.mu.Lock()
	if d.detached {
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()
	return d.Conn.Close()
}

func (d *detachableConn) detach() {
	d.mu.Lock()
	d.detached = true
	d.mu.Unlock()
}

// tunnel forwards bytes in both directions, propagates half-close where the
// connection supports it, and closes both sides on a real error or a
// registry-initiated close (docs/contracts.md §2.2, §3.3).
func tunnel(ctx context.Context, client net.Conn, clientR io.Reader, upstream net.Conn, upstreamR io.Reader) {
	closeBoth := func() {
		_ = client.Close()
		_ = upstream.Close()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := io.Copy(upstream, clientR)
		closeWrite(upstream)
		if err != nil && !errors.Is(err, net.ErrClosed) {
			closeBoth()
		}
	}()
	go func() {
		defer wg.Done()
		_, err := io.Copy(client, upstreamR)
		closeWrite(client)
		if err != nil && !errors.Is(err, net.ErrClosed) {
			closeBoth()
		}
	}()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		closeBoth()
		<-done
	}
}

type closeWriter interface {
	CloseWrite() error
}

// closeWrite is best-effort: contract.Dialer and PoolDialer return net.Conn,
// which does not promise CloseWrite. A conn without it cannot signal EOF to its
// peer; the tunnel still forwards the other direction until something closes.
// Adapters should return *net.TCPConn or otherwise expose CloseWrite for
// protocols that half-close.
func closeWrite(c net.Conn) {
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
}
