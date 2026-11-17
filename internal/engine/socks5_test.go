package engine_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/engine"
)

// socksPeer is a minimal RFC 1928 server used to observe exactly what our
// adapter puts on the wire and to answer with a chosen reply code.
type socksPeerConfig struct {
	requireAuth bool
	user, pass  string
	greetMethod byte // default 0x00; 0xff means "no acceptable method"
	replyCode   byte // 0 is success
	replyATYP   byte // default 0x01; 0x03/0x04 exercise the reply parser
	replyAddr   []byte
	silent      bool // accept the greeting and never answer
}

type socksPeer struct {
	ln      net.Listener
	cfg     socksPeerConfig
	conns   atomic.Int64
	mu      sync.Mutex
	got     socksRequest
	methods []byte
	seen    chan struct{}
}

type socksRequest struct {
	atyp     byte
	host     string
	port     uint16
	method   byte
	user     string
	password string
}

func newSocksPeer(t *testing.T, cfg socksPeerConfig) *socksPeer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("socks peer listen: %v", err)
	}
	p := &socksPeer{ln: ln, cfg: cfg, seen: make(chan struct{}, 1)}
	go p.acceptLoop()
	t.Cleanup(func() { _ = ln.Close() })
	return p
}

func (p *socksPeer) port() uint16 { return uint16(p.ln.Addr().(*net.TCPAddr).Port) }

func (p *socksPeer) request() (socksRequest, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.got, p.got.atyp != 0
}

func (p *socksPeer) acceptLoop() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.conns.Add(1)
		go p.handle(c)
	}
}

func (p *socksPeer) handle(c net.Conn) {
	defer c.Close()
	if p.cfg.silent {
		// Read until the client gives up; nothing is ever sent, so the
		// adapter's cancellation path is exercised.
		_, _ = io.Copy(io.Discard, c)
		return
	}
	br := bufio.NewReader(c)

	var head [2]byte
	if _, err := io.ReadFull(br, head[:]); err != nil {
		return
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	p.mu.Lock()
	p.methods = methods
	p.mu.Unlock()
	if p.cfg.silent {
		// Read until the client gives up; nothing is ever sent, so the
		// adapter's cancellation path is exercised.
		_, _ = io.Copy(io.Discard, c)
		return
	}
	selected := byte(0x00)
	if p.cfg.greetMethod != 0 {
		selected = p.cfg.greetMethod
	} else if p.cfg.requireAuth {
		selected = 0x02
	}
	method := selected
	if _, err := c.Write([]byte{0x05, selected}); err != nil {
		return
	}
	if selected == 0xff {
		return
	}
	req := socksRequest{method: method}

	if selected == 0x02 {
		var authHead [2]byte
		if _, err := io.ReadFull(br, authHead[:]); err != nil {
			return
		}
		user := make([]byte, authHead[1])
		if _, err := io.ReadFull(br, user); err != nil {
			return
		}
		var plen [1]byte
		if _, err := io.ReadFull(br, plen[:]); err != nil {
			return
		}
		pass := make([]byte, plen[0])
		if _, err := io.ReadFull(br, pass); err != nil {
			return
		}
		req.user, req.password = string(user), string(pass)
		status := byte(0x00)
		if p.cfg.requireAuth && (req.user != p.cfg.user || req.password != p.cfg.pass) {
			status = 0x01
		}
		if _, err := c.Write([]byte{0x01, status}); err != nil {
			return
		}
		if status != 0x00 {
			return
		}
	}

	var reqHead [4]byte
	if _, err := io.ReadFull(br, reqHead[:]); err != nil {
		return
	}
	req.atyp = reqHead[3]
	switch req.atyp {
	case 0x01:
		var addr [4]byte
		if _, err := io.ReadFull(br, addr[:]); err != nil {
			return
		}
		req.host = net.IP(addr[:]).String()
	case 0x04:
		var addr [16]byte
		if _, err := io.ReadFull(br, addr[:]); err != nil {
			return
		}
		req.host = net.IP(addr[:]).String()
	case 0x03:
		var length [1]byte
		if _, err := io.ReadFull(br, length[:]); err != nil {
			return
		}
		name := make([]byte, length[0])
		if _, err := io.ReadFull(br, name); err != nil {
			return
		}
		req.host = string(name)
	default:
		return
	}
	var port [2]byte
	if _, err := io.ReadFull(br, port[:]); err != nil {
		return
	}
	req.port = binary.BigEndian.Uint16(port[:])

	p.mu.Lock()
	p.got = req
	p.mu.Unlock()
	select {
	case p.seen <- struct{}{}:
	default:
	}

	reply := p.cfg.replyCode
	atyp := p.cfg.replyATYP
	if atyp == 0 {
		atyp = 0x01
	}
	addr := p.cfg.replyAddr
	if atyp == 0x01 && len(addr) == 0 {
		addr = []byte{0, 0, 0, 0}
	}
	msg := append([]byte{0x05, reply, 0x00, atyp}, addr...)
	msg = append(msg, 0, 0)
	if _, err := c.Write(msg); err != nil {
		return
	}
	if reply == 0x00 {
		_, _ = io.Copy(c, br)
	}
}

func TestSocks5AdapterSendsDomainUnresolvedAndConfirms(t *testing.T) {
	p := newSocksPeer(t, socksPeerConfig{})
	e := startEngine(t, 1, socksRecord("x-socks", "127.0.0.1", p.port(), "", ""))

	res, err := e.Dial(context.Background(), dialRequest("x-socks", domainDestination("api.example.test", 8443), 1))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer res.Conn.Close()

	if res.Proof != contract.ProofConfirmed {
		t.Fatalf("proof = %q, want confirmed (our adapter parsed the exit's reply)", res.Proof)
	}
	if res.Tag != "" {
		t.Fatalf("tag = %q, want empty for an adapter-dialed exit", res.Tag)
	}
	req, ok := p.request()
	if !ok {
		t.Fatalf("peer saw no request")
	}
	if req.atyp != 0x03 {
		t.Fatalf("address type = %#x, want 0x03 (domain); a hostname must not be resolved locally", req.atyp)
	}
	if req.host != "api.example.test" || req.port != 8443 {
		t.Fatalf("request = %s:%d, want api.example.test:8443", req.host, req.port)
	}

	if _, err := res.Conn.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readWithin(res.Conn, 2*time.Second)
	if got.err != nil || string(got.data) != "hello" {
		t.Fatalf("echo = %q, %v", got.data, got.err)
	}
}

func TestSocks5AdapterRejectionsAreTyped(t *testing.T) {
	cases := []struct {
		name string
		peer socksPeerConfig
		kind contract.ErrorKind
	}{
		{"destination refused", socksPeerConfig{replyCode: 0x05}, contract.DestinationRefused},
		{"request rejected", socksPeerConfig{replyCode: 0x04}, contract.HandshakeRejected},
		{"no acceptable auth", socksPeerConfig{greetMethod: 0xff}, contract.HandshakeRejected},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newSocksPeer(t, tc.peer)
			id := contract.RecordID("x-socks-" + string(rune('a'+i)))
			e := startEngine(t, 1, socksRecord(string(id), "127.0.0.1", p.port(), "", ""))
			res, err := e.Dial(context.Background(), dialRequest(contract.ExitID(id), domainDestination("dest.example.test", 443), 1))
			if err == nil {
				_ = res.Conn.Close()
				t.Fatalf("dial succeeded, want %s", tc.kind)
			}
			var f *contract.Failure
			if !errors.As(err, &f) {
				t.Fatalf("error %v is not a *contract.Failure", err)
			}
			if f.Kind != tc.kind {
				t.Fatalf("kind = %q, want %q (%v)", f.Kind, tc.kind, err)
			}
			if tc.kind == contract.DestinationRefused && f.Proof != contract.ProofConfirmed {
				t.Fatalf("refused destination proof = %q, want confirmed", f.Proof)
			}
		})
	}
}

func TestSocks5AdapterUsesUsernamePasswordOnlyWhenPresent(t *testing.T) {
	good := newSocksPeer(t, socksPeerConfig{requireAuth: true, user: "alice", pass: "s3cret"})
	e := startEngine(t, 1, socksRecord("x-auth", "127.0.0.1", good.port(), "alice", "s3cret"))
	res, err := e.Dial(context.Background(), dialRequest("x-auth", domainDestination("dest.example.test", 443), 1))
	if err != nil {
		t.Fatalf("authenticated dial: %v", err)
	}
	_ = res.Conn.Close()

	if got, ok := good.request(); !ok || got.method != 0x02 || got.user != "alice" || got.password != "s3cret" {
		t.Fatalf("peer saw %+v (ok=%v)", got, ok)
	}

	bad := newSocksPeer(t, socksPeerConfig{requireAuth: true, user: "alice", pass: "s3cret"})
	e2 := startEngine(t, 1, socksRecord("x-bad-auth", "127.0.0.1", bad.port(), "alice", "wrong"))
	_, err = e2.Dial(context.Background(), dialRequest("x-bad-auth", domainDestination("dest.example.test", 443), 1))
	var f *contract.Failure
	if !errors.As(err, &f) || f.Kind != contract.HandshakeRejected {
		t.Fatalf("wrong password error = %v, want handshake_rejected", err)
	}
}

func TestSocks5AdapterCancelAndTimeout(t *testing.T) {
	t.Run("cancel during handshake", func(t *testing.T) {
		p := newSocksPeer(t, socksPeerConfig{silent: true})
		e := startEngine(t, 1, socksRecord("x-silent", "127.0.0.1", p.port(), "", ""))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			_, err := e.Dial(ctx, dialRequest("x-silent", domainDestination("dest.example.test", 443), 1))
			done <- err
		}()
		time.Sleep(50 * time.Millisecond) // let the adapter block in the greeting read
		start := time.Now()
		cancel()
		select {
		case err := <-done:
			elapsed := time.Since(start)
			if elapsed > contract.PendingDialCancelDeadline {
				t.Fatalf("cancel surfaced after %v, bound %v", elapsed, contract.PendingDialCancelDeadline)
			}
			if kind := failureKind(t, err); kind != contract.Canceled {
				t.Fatalf("kind = %q, want canceled", kind)
			}
			t.Logf("pending SOCKS5 handshake canceled in %v (bound %v)", elapsed, contract.PendingDialCancelDeadline)
		case <-time.After(2 * time.Second):
			t.Fatalf("dial did not return after cancel")
		}
	})

	t.Run("caller deadline", func(t *testing.T) {
		p := newSocksPeer(t, socksPeerConfig{silent: true})
		e := startEngine(t, 1, socksRecord("x-slow", "127.0.0.1", p.port(), "", ""))
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := e.Dial(ctx, dialRequest("x-slow", domainDestination("dest.example.test", 443), 1))
		if kind := failureKind(t, err); kind != contract.Timeout {
			t.Fatalf("kind = %q, want timeout", kind)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("deadline surfaced after %v", elapsed)
		}
	})
}

func TestSocks5AdapterRequiresAPort(t *testing.T) {
	p := newSocksPeer(t, socksPeerConfig{})
	e := startEngine(t, 1, socksRecord("x-socks", "127.0.0.1", p.port(), "", ""))
	_, err := e.Dial(context.Background(), engine.DialRequest{
		Exit:          "x-socks",
		Destination:   contract.Destination{Kind: contract.DestDomain, Host: "example.test"},
		PolicyVersion: 1,
	})
	if kind := failureKind(t, err); kind != contract.InvalidDestination {
		t.Fatalf("kind = %q, want invalid_destination", kind)
	}
}

func (p *socksPeer) offeredMethods() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.methods...)
}

func TestSocks5AdapterOffersOnlyTheMethodsItCanUse(t *testing.T) {
	plain := newSocksPeer(t, socksPeerConfig{})
	e := startEngine(t, 1, socksRecord("x-plain", "127.0.0.1", plain.port(), "", ""))
	res, err := e.Dial(context.Background(), dialRequest("x-plain", domainDestination("dest.example.test", 443), 1))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = res.Conn.Close()
	if got := plain.offeredMethods(); len(got) != 1 || got[0] != 0x00 {
		t.Fatalf("offered methods without credentials = %v, want [0x00]", got)
	}

	auth := newSocksPeer(t, socksPeerConfig{requireAuth: true, user: "u", pass: "p"})
	e2 := startEngine(t, 1, socksRecord("x-auth", "127.0.0.1", auth.port(), "u", "p"))
	res, err = e2.Dial(context.Background(), dialRequest("x-auth", domainDestination("dest.example.test", 443), 1))
	if err != nil {
		t.Fatalf("auth dial: %v", err)
	}
	_ = res.Conn.Close()
	if got := auth.offeredMethods(); len(got) != 2 || got[0] != 0x00 || got[1] != 0x02 {
		t.Fatalf("offered methods with credentials = %v, want [0x00 0x02]", got)
	}
}

func TestSocks5AdapterParsesEveryReplyShape(t *testing.T) {
	cases := []struct {
		name string
		cfg  socksPeerConfig
		kind contract.ErrorKind
		ok   bool
	}{
		{"ipv4 bound address", socksPeerConfig{}, "", true},
		{"domain bound address", socksPeerConfig{replyATYP: 0x03, replyAddr: []byte{4, 'o', 'k', '.', 'x'}}, "", true},
		{"ipv6 bound address", socksPeerConfig{replyATYP: 0x04, replyAddr: make([]byte, 16)}, "", true},
		{"unknown address type", socksPeerConfig{replyATYP: 0x09}, contract.HandshakeRejected, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newSocksPeer(t, tc.cfg)
			id := contract.RecordID("x-reply-" + string(rune('a'+i)))
			e := startEngine(t, 1, socksRecord(string(id), "127.0.0.1", p.port(), "", ""))
			res, err := e.Dial(context.Background(), dialRequest(contract.ExitID(id), domainDestination("dest.example.test", 443), 1))
			if tc.ok {
				if err != nil {
					t.Fatalf("dial: %v", err)
				}
				_ = res.Conn.Close()
				return
			}
			if kind := failureKind(t, err); kind != tc.kind {
				t.Fatalf("kind = %q, want %q", kind, tc.kind)
			}
		})
	}
}

// TestSocks5TunnelReturnsTypedErrors: the adapter's conn must classify like the
// core-path conn, otherwise a cancel looks like an I/O error and the exit
// endpoint can leak through %v.
func TestSocks5TunnelReturnsTypedErrors(t *testing.T) {
	p := newSocksPeer(t, socksPeerConfig{})
	e := startEngine(t, 1, socksRecord("x-socks", "127.0.0.1", p.port(), "", ""))

	t.Run("session cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		res, err := e.Dial(ctx, dialRequest("x-socks", domainDestination("dest.example.test", 443), 1))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer res.Conn.Close()
		if _, err := res.Conn.Write([]byte("hi")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got := readWithin(res.Conn, 2*time.Second); got.err != nil || string(got.data) != "hi" {
			t.Fatalf("echo = %q, %v", got.data, got.err)
		}
		cancel()
		got := readWithin(res.Conn, time.Second)
		if got.err == nil || got.err == errReadTimedOut {
			t.Fatalf("read after cancel = %v", got.err)
		}
		if kind := failureKind(t, got.err); kind != contract.Canceled {
			t.Fatalf("kind = %q, want canceled", kind)
		}
	})

	t.Run("engine stop", func(t *testing.T) {
		res, err := e.Dial(context.Background(), dialRequest("x-socks", domainDestination("dest.example.test", 443), 1))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		if err := e.Stop(context.Background()); err != nil {
			t.Fatalf("stop: %v", err)
		}
		got := readWithin(res.Conn, time.Second)
		if got.err == nil || got.err == errReadTimedOut {
			t.Fatalf("read after stop = %v", got.err)
		}
		if kind := failureKind(t, got.err); kind != contract.Canceled {
			t.Fatalf("kind = %q, want canceled", kind)
		}
	})
}

// TestStopClosesPendingSocksHandshake: an exit socket that is still inside the
// SOCKS greeting must be owned by the engine, so Stop closes it instead of
// leaving it to the caller's deadline.
func TestStopClosesPendingSocksHandshake(t *testing.T) {
	p := newSocksPeer(t, socksPeerConfig{silent: true})
	baseline := socketFDs(t)
	e, err := engine.Start(engine.Options{
		Exits:         []contract.Record{socksRecord("x-silent", "127.0.0.1", p.port(), "", "")},
		PolicyVersion: 1,
		LogLevel:      "none",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.Dial(context.Background(), dialRequest("x-silent", domainDestination("dest.example.test", 443), 1))
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for p.conns.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if p.conns.Load() == 0 {
		t.Fatalf("peer never accepted the handshake")
	}
	time.Sleep(50 * time.Millisecond)
	if got := socketFDs(t); got < baseline+1 {
		t.Fatalf("expected the pending exit socket to be open: fds %d, baseline %d", got, baseline)
	}

	start := time.Now()
	if err := e.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case err := <-done:
		if kind := failureKind(t, err); kind != contract.Canceled {
			t.Fatalf("pending dial kind = %q, want canceled", kind)
		}
		if elapsed := time.Since(start); elapsed > contract.LocalCloseDeadline {
			t.Fatalf("pending handshake closed after %v, bound %v", elapsed, contract.LocalCloseDeadline)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("pending SOCKS5 dial was not released by Stop")
	}
	time.Sleep(50 * time.Millisecond)
	if got := socketFDs(t); got > baseline+1 {
		t.Fatalf("socket fds after Stop = %d, baseline %d", got, baseline)
	}
}
