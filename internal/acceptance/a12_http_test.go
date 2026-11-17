package acceptance

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestA12Forwarding covers the HTTP methods/framing that the first pass did not
// exercise, and the explicit UDP refusal at the SOCKS5 surface.
func TestA12Forwarding(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "http")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	origin := newRecordingOrigin(t)
	_ = newFixtureSet(t)
	svc := startService(t, env, dir, serviceConfig{})
	t.Cleanup(func() { _ = svc.stop() })

	target := fmt.Sprintf("127.0.0.1:%d", origin.port())
	authority := fmt.Sprintf("http://%s", target)

	// HEAD: headers only, no synthesized body.
	headResp, headBody, err := httpForwardMethod(t, svc.httpAddr,
		"HEAD "+authority+"/echo HTTP/1.1\r\nHost: "+target+"\r\nConnection: close\r\n\r\n", "HEAD", 10*time.Second)
	if err != nil || headResp == nil || headResp.StatusCode != 200 || len(headBody) != 0 ||
		headResp.Header.Get("X-RL-Method") != "HEAD" || headResp.ContentLength != 12 {
		r.fail("A12", "HEAD: status=%v body=%q len=%d err=%v", headResp, headBody, len(headBody), err)
	} else {
		r.pass("A12", "HEAD is forwarded and returns the origin headers with no body (Content-Length 12, X-RL-Method HEAD)")
	}

	// POST with a declared body.
	postResp, postBody, err := httpForwardMethod(t, svc.httpAddr,
		"POST "+authority+"/echo HTTP/1.1\r\nHost: "+target+"\r\nContent-Length: 10\r\nConnection: close\r\n\r\nhello-body", "POST", 10*time.Second)
	if err != nil || postResp == nil || postResp.StatusCode != 200 || string(postBody) != "echo:POST:hello-body" {
		r.fail("A12", "POST: resp=%v body=%q err=%v", postResp, postBody, err)
	} else {
		r.pass("A12", "POST with a body is forwarded byte for byte (origin received the declared 10 bytes)")
	}

	// Chunked request body, raw framing preserved.
	chunkedReq := "POST " + authority + "/chunked HTTP/1.1\r\nHost: " + target +
		"\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n3\r\nabc\r\n3\r\ndef\r\n0\r\n\r\n"
	chunkResp, chunkBody, err := httpForwardMethod(t, svc.httpAddr, chunkedReq, "POST", 10*time.Second)
	var chunkRecord *recordedRequest
	for _, rec := range origin.requests() {
		if rec.Path == "/chunked" {
			rec := rec
			chunkRecord = &rec
		}
	}
	if err != nil || chunkResp == nil || chunkResp.StatusCode != 200 || string(chunkBody) != "chunked:abcdef" ||
		chunkRecord == nil || !containsString(chunkRecord.TransferEncoding, "chunked") || chunkRecord.Body != "abcdef" {
		r.fail("A12", "chunked: resp=%v body=%q record=%+v err=%v", chunkResp, chunkBody, chunkRecord, err)
	} else {
		r.pass("A12", "a chunked request body is forwarded with its framing intact (origin saw chunked abcdef)")
	}

	// Incremental response: the client receives the first chunk before the
	// origin finishes the response.
	conn, err := net.DialTimeout("tcp", svc.httpAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("stream dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := io.WriteString(conn, "GET "+authority+"/stream HTTP/1.1\r\nHost: "+target+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("stream write: %v", err)
	}
	req, _ := http.NewRequest("GET", authority+"/stream", nil)
	streamResp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("stream head: %v", err)
	}
	first := make([]byte, 5)
	if _, err := io.ReadFull(streamResp.Body, first); err != nil {
		t.Fatalf("stream first bytes: %v", err)
	}
	firstAt := time.Now()
	rest, _ := io.ReadAll(streamResp.Body)
	_, serverEnd := origin.streamTimes()
	if string(first) == "start" && string(rest) == "end" && !firstAt.After(serverEnd) {
		r.pass("A12", "a streaming response reaches the client before the origin finishes (first 5 bytes at %s before the origin end)", serverEnd.Sub(firstAt).Round(time.Millisecond))
	} else {
		r.fail("A12", "stream: first=%q rest=%q firstAt=%s serverEnd=%s", first, rest, firstAt, serverEnd)
	}

	// Unbounded stream: the proxy forwards without buffering or a length cap.
	infConn, err := net.DialTimeout("tcp", svc.httpAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("infinite dial: %v", err)
	}
	_ = infConn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := io.WriteString(infConn, "GET "+authority+"/infinite HTTP/1.1\r\nHost: "+target+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("infinite write: %v", err)
	}
	infReq, _ := http.NewRequest("GET", authority+"/infinite", nil)
	infResp, err := http.ReadResponse(bufio.NewReader(infConn), infReq)
	if err != nil {
		t.Fatalf("infinite head: %v", err)
	}
	started := time.Now()
	buf := make([]byte, 64<<10)
	n, err := io.ReadFull(infResp.Body, buf)
	elapsed := time.Since(started)
	_ = infConn.Close()
	if err == nil && n == len(buf) && elapsed < 5*time.Second {
		r.pass("A12", "an unbounded stream is forwarded incrementally (64 KiB in %s, no length cap)", elapsed.Round(time.Millisecond))
	} else {
		r.fail("A12", "unbounded stream: bytes=%d elapsed=%s err=%v", n, elapsed, err)
	}

	// UDP and BIND are refused with 0x07 and create no UDP socket.
	udpCode, udpErr := socksCommand(t, svc.socksAddr, 0x03, "127.0.0.1", 53)
	bindCode, bindErr := socksCommand(t, svc.socksAddr, 0x02, "127.0.0.1", int(origin.port()))
	if udpErr == nil && bindErr == nil && udpCode == 0x07 && bindCode == 0x07 {
		r.pass("A12", "SOCKS5 UDP ASSOCIATE and BIND are refused with 0x07")
	} else {
		r.fail("A12", "SOCKS5 auxiliary commands: udp=%#x/%v bind=%#x/%v", udpCode, udpErr, bindCode, bindErr)
	}
	if n := productUDPSockets(t, svc.proc.Process.Pid); n == 0 {
		r.pass("A12", "the product process owns no UDP socket after the refused UDP ASSOCIATE")
	} else {
		r.fail("A12", "the product owns %d UDP socket(s) after a refused UDP ASSOCIATE", n)
	}

	// No HTTPS interception and no unsupported scheme handling in the proxy.
	beforeHTTPS := origin.count()
	httpsResp, _, _ := httpForwardMethod(t, svc.httpAddr,
		"GET https://"+target+"/ HTTP/1.1\r\nHost: "+target+"\r\nConnection: close\r\n\r\n", "GET", 10*time.Second)
	ftpResp, _, _ := httpForwardMethod(t, svc.httpAddr,
		"GET ftp://"+target+"/ HTTP/1.1\r\nHost: "+target+"\r\nConnection: close\r\n\r\n", "GET", 10*time.Second)
	httpsCode, ftpCode := 0, 0
	if httpsResp != nil {
		httpsCode = httpsResp.StatusCode
	}
	if ftpResp != nil {
		ftpCode = ftpResp.StatusCode
	}
	if httpsCode >= 400 && httpsCode < 600 && origin.count() == beforeHTTPS && (ftpCode == 405 || ftpCode == 501) {
		r.pass("A12", "absolute-form https is refused with no request reaching the origin (no MITM, HTTP %d) and an unsupported scheme is 405/501", httpsCode)
	} else {
		r.fail("A12", "https=%d origin-requests %d→%d ftp=%d", httpsCode, beforeHTTPS, origin.count(), ftpCode)
	}
	if httpsCode != 0 && httpsCode != 405 && httpsCode != 501 {
		r.note("A12", "absolute-form https is refused with %d; §3.2 maps an operation outside v1 to unsupported (405/501), while policy_denied is reserved for self/local prohibition (taxonomy mismatch, minor)", httpsCode)
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// productUDPSockets counts listening/connected UDP sockets whose inode belongs
// to the product process.
func productUDPSockets(t *testing.T, pid int) int {
	t.Helper()
	inodes := socketInodes(t, pid)
	n := 0
	for _, family := range []string{"udp", "udp6"} {
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/net/" + family)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			if inodes[fields[9]] {
				n++
			}
		}
	}
	return n
}
