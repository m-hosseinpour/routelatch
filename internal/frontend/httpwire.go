package frontend

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// maxHeadBytes bounds one request or response head. It matches the
// http.Server limit the listener applies to the first request.
const maxHeadBytes = 1 << 20

// connCtxKey carries the accepted connection into the HTTP handler so the
// handler can recover the raw request head that net/http parsed and discarded.
type connCtxKey struct{}

// recordingListener wraps accepted connections in recordingConn, preserving
// the exact first request head for byte-faithful forwarding. The standard
// server canonicalizes header names and status text if a handler re-serializes
// a request (spike/frontend FINDINGS C8), so the raw bytes are the only
// faithful source.
type recordingListener struct {
	net.Listener
}

func (l *recordingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &recordingConn{Conn: c, limit: maxHeadBytes + 8<<10}, nil
}

// recordingConn records bytes read before the handler switches the connection
// to its own raw loop. Recording stops there so tunnelled traffic is never
// copied into memory.
type recordingConn struct {
	net.Conn

	mu        sync.Mutex
	buf       []byte
	truncated bool
	stopped   bool
	limit     int
}

func (c *recordingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.mu.Lock()
		if !c.stopped {
			room := c.limit - len(c.buf)
			switch {
			case room <= 0:
				c.truncated = true
			case n > room:
				c.buf = append(c.buf, p[:room]...)
				c.truncated = true
			default:
				c.buf = append(c.buf, p[:n]...)
			}
		}
		c.mu.Unlock()
	}
	return n, err
}

// firstHead returns the raw bytes through the end of the first request head.
// Bytes net/http prefetched beyond the head remain in the hijacked
// bufio.Reader and are deliberately excluded here.
func (c *recordingConn) firstHead() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := c.buf
	best := -1
	for _, sep := range [][]byte{[]byte("\r\n\r\n"), []byte("\n\n")} {
		if i := bytes.Index(buf, sep); i >= 0 {
			if best < 0 || i+len(sep) < best {
				best = i + len(sep)
			}
		}
	}
	if best >= 0 {
		return append([]byte(nil), buf[:best]...), nil
	}
	if c.truncated {
		return nil, errors.New("frontend: request head exceeds the accepted limit")
	}
	return nil, errors.New("frontend: request head not captured")
}

func (c *recordingConn) stopRecording() {
	c.mu.Lock()
	c.stopped = true
	c.buf = nil
	c.mu.Unlock()
}

// CloseWrite keeps TCP half-close available through the wrapper. Embedding the
// net.Conn interface promotes only its methods, so without this the tunnel
// could not signal EOF to the client and would wait for the client to hang up
// first.
func (c *recordingConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c *recordingConn) CloseRead() error {
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return nil
}

// readHead reads one HTTP head byte-for-byte from br, stopping immediately
// after the empty line. The body stays in br for streaming.
func readHead(br *bufio.Reader) ([]byte, error) {
	var head []byte
	for {
		line, err := br.ReadSlice('\n')
		head = append(head, line...)
		if len(head) > maxHeadBytes {
			return nil, errors.New("frontend: head too large")
		}
		if err == nil {
			if isBlankLine(line) {
				return head, nil
			}
			continue
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}
}

func isBlankLine(line []byte) bool {
	return bytes.Equal(line, []byte("\r\n")) || bytes.Equal(line, []byte("\n"))
}

func isChunked(te []string) bool {
	for _, v := range te {
		if strings.EqualFold(strings.TrimSpace(v), "chunked") {
			return true
		}
	}
	return false
}

// copyChunked forwards a chunked body with its original framing: chunk-size
// lines (including extensions), chunk data, CRLF and trailers. Nothing is
// decoded or re-encoded.
func copyChunked(dst io.Writer, br *bufio.Reader) error {
	const maxChunkLine = 8 << 10
	for {
		line, err := readHeadLine(br, maxChunkLine)
		if err != nil {
			return err
		}
		if _, err := dst.Write(line); err != nil {
			return err
		}
		sizeText := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r"))
		if i := strings.IndexByte(sizeText, ';'); i >= 0 {
			sizeText = strings.TrimSpace(sizeText[:i])
		}
		size, err := strconv.ParseUint(sizeText, 16, 62)
		if err != nil {
			return errors.New("frontend: malformed chunk size")
		}
		if size == 0 {
			// Trailer section ends with an empty line.
			for {
				trailer, err := readHeadLine(br, maxChunkLine)
				if err != nil {
					return err
				}
				if _, err := dst.Write(trailer); err != nil {
					return err
				}
				if isBlankLine(trailer) {
					return nil
				}
			}
		}
		if _, err := io.CopyN(dst, br, int64(size)); err != nil {
			return err
		}
		crlf, err := readHeadLine(br, 2)
		if err != nil {
			return err
		}
		if _, err := dst.Write(crlf); err != nil {
			return err
		}
	}
}

func readHeadLine(br *bufio.Reader, max int) ([]byte, error) {
	line, err := br.ReadSlice('\n')
	if err == nil {
		if len(line) > max {
			return nil, errors.New("frontend: line too long")
		}
		return line, nil
	}
	if !errors.Is(err, bufio.ErrBufferFull) {
		return nil, err
	}
	buf := append([]byte(nil), line...)
	for {
		if len(buf) > max {
			return nil, errors.New("frontend: line too long")
		}
		line, err = br.ReadSlice('\n')
		buf = append(buf, line...)
		if err == nil {
			return buf, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

// stripProxyHeaders removes the two hop-by-hop headers a proxy must consume
// rather than forward: Proxy-Connection and Proxy-Authorization. Every other
// header line, including its original name casing and order, is preserved.
func stripProxyHeaders(headers []byte) []byte {
	out := make([]byte, 0, len(headers))
	skip := false
	for len(headers) > 0 {
		i := bytes.IndexByte(headers, '\n')
		var line []byte
		if i < 0 {
			line, headers = headers, nil
		} else {
			line, headers = headers[:i+1], headers[i+1:]
		}
		continuation := len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
		if !continuation {
			skip = false
			if c := bytes.IndexByte(line, ':'); c > 0 {
				name := strings.TrimSpace(string(line[:c]))
				if strings.EqualFold(name, "Proxy-Connection") ||
					strings.EqualFold(name, "Proxy-Authorization") {
					skip = true
				}
			}
		}
		if !skip {
			out = append(out, line...)
		}
	}
	return out
}

// originTarget rewrites an absolute-form request target into the origin-form
// one an origin server expects, preserving the escaped path and raw query.
func originTarget(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.ForceQuery || u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return path
}

// requestLineTarget bounds are the two spaces that delimit the target in the
// request line. The rest of the line is forwarded untouched.
func requestLineBounds(head []byte) (int, int, bool) {
	lineEnd := bytes.IndexByte(head, '\n')
	if lineEnd < 0 {
		return 0, 0, false
	}
	line := head[:lineEnd]
	first := bytes.IndexByte(line, ' ')
	if first < 0 {
		return 0, 0, false
	}
	rest := line[first+1:]
	secondRel := bytes.IndexByte(rest, ' ')
	if secondRel < 0 {
		return 0, 0, false
	}
	return first + 1, first + 1 + secondRel, true
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if n > 0 {
			b = b[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
