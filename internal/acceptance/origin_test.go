package acceptance

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// recording origin: counts requests and keeps method, path, body and framing
// ---------------------------------------------------------------------------

type recordedRequest struct {
	Method           string
	Path             string
	Body             string
	TransferEncoding []string
	ContentLength    int64
	At               time.Time
}

type recordingOrigin struct {
	ln  net.Listener
	srv *http.Server

	mu    sync.Mutex
	reqs  []recordedRequest
	start time.Time // streaming handler started
	end   time.Time // streaming handler finished
	once  sync.Once
}

func newRecordingOrigin(t *testing.T) *recordingOrigin {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("origin listen: %v", err)
	}
	o := &recordingOrigin{ln: ln}
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		w.Header().Set("X-RL-Method", r.Method)
		w.Header().Set("X-RL-Body-Length", fmt.Sprint(len(body)))
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "12")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "echo:"+r.Method+":"+string(body))
	})
	mux.HandleFunc("/chunked", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		w.Header().Set("X-RL-Transfer-Encoding", strings.Join(r.TransferEncoding, ","))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "chunked:"+string(body))
	})
	mux.HandleFunc("/infinite", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		chunk := make([]byte, 1024)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		o.mu.Lock()
		o.start = time.Now()
		o.mu.Unlock()
		_, _ = io.WriteString(w, "start")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(500 * time.Millisecond)
		_, _ = io.WriteString(w, "end")
		if flusher != nil {
			flusher.Flush()
		}
		o.mu.Lock()
		o.end = time.Now()
		o.mu.Unlock()
	})
	// Record every request, including paths no handler serves, so a replayed
	// request at this listener can never go uncounted.
	recordAll := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		copied := r.Clone(r.Context())
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		copied.Body = io.NopCloser(strings.NewReader(string(body)))
		rec := recordedRequest{
			Method: copied.Method, Path: copied.URL.Path, Body: string(body),
			TransferEncoding: append([]string(nil), r.TransferEncoding...),
			ContentLength:    r.ContentLength, At: time.Now(),
		}
		o.mu.Lock()
		o.reqs = append(o.reqs, rec)
		o.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		mux.ServeHTTP(w, r)
	})
	o.srv = &http.Server{Handler: recordAll}
	go func() { _ = o.srv.Serve(ln) }()
	t.Cleanup(o.close)
	return o
}

func (o *recordingOrigin) close() {
	o.once.Do(func() { _ = o.srv.Close() })
}

func (o *recordingOrigin) port() uint16 { return uint16(o.ln.Addr().(*net.TCPAddr).Port) }

func (o *recordingOrigin) requests() []recordedRequest {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]recordedRequest(nil), o.reqs...)
}

func (o *recordingOrigin) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.reqs)
}

func (o *recordingOrigin) streamTimes() (time.Time, time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.start, o.end
}

// ---------------------------------------------------------------------------
// low-level proxy clients
// ---------------------------------------------------------------------------

// httpForwardMethod sends an absolute-form request with the given method used
// only for response framing (HEAD has no body), and returns the response and
// body bytes.
func httpForwardMethod(t *testing.T, proxyAddr, raw, method string, timeout time.Duration) (*http.Response, []byte, error) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := io.WriteString(conn, raw); err != nil {
		return nil, nil, err
	}
	req, _ := http.NewRequest(method, "http://fixture.invalid/", nil)
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return nil, nil, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, body, err
}

// socksCommand sends one SOCKS5 request with an arbitrary command and returns
// the wire reply code. It does not require success.
func socksCommand(t *testing.T, proxyAddr string, cmd byte, host string, port int) (byte, error) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		return 0, err
	}
	br := bufio.NewReader(conn)
	var greet [2]byte
	if _, err := io.ReadFull(br, greet[:]); err != nil {
		return 0, err
	}
	req := []byte{5, cmd, 0}
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
		return 0, err
	}
	var head [4]byte
	if _, err := io.ReadFull(br, head[:]); err != nil {
		return 0, err
	}
	_, _, _ = readSocksAddr(br, head[3])
	return head[1], nil
}

// deadVLESSPort returns a loopback port with no listener, for a pool record
// whose transport cannot connect.
func deadVLESSPort(t *testing.T) uint16 {
	t.Helper()
	port := freePort(t)
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err == nil {
		_ = conn.Close()
		port = freePort(t)
	}
	return port
}

// markerExists reports whether a launcher child created its marker file.
func markerExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
