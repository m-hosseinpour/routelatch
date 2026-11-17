package main

import (
	"io"
	"net"
	"sync"
	"time"
)

// socksPeer is a minimal RFC 1928 SOCKS5 CONNECT server used as a control: it
// connects to the destination *before* sending the success reply. Xray's own
// SOCKS inbound does the opposite (reply first, dispatch later), so this peer
// shows whether the client-side SOCKS handshake can carry destination status
// when the server is conforming.
type socksPeer struct {
	ln net.Listener

	mu       sync.Mutex
	accepts  []time.Time
	connects []time.Time
	failures []string
	bytes    int64
}

func startSocksPeer() (*socksPeer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &socksPeer{ln: ln}
	go p.serve()
	return p, nil
}

func (p *socksPeer) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *socksPeer) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		p.accepts = append(p.accepts, time.Now())
		p.mu.Unlock()
		go p.handle(c)
	}
}

func (p *socksPeer) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	head := make([]byte, 2)
	if _, err := io.ReadFull(c, head); err != nil || head[0] != 5 {
		return
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil { // no auth
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil || req[0] != 5 || req[1] != 1 {
		_, _ = c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = string(b)
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		_, _ = c.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return
	}
	port := int(pb[0])<<8 | int(pb[1])
	up, err := net.DialTimeout("tcp", net.JoinHostPort(host, itoa(port)), 2*time.Second)
	if err != nil {
		p.mu.Lock()
		p.failures = append(p.failures, err.Error())
		p.mu.Unlock()
		// 0x05 = connection refused
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	p.mu.Lock()
	p.connects = append(p.connects, time.Now())
	p.mu.Unlock()
	_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	_ = c.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	go func() {
		n, _ := io.Copy(up, c)
		p.addBytes(n)
		if tc, ok := up.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		n, _ := io.Copy(c, up)
		p.addBytes(n)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func (p *socksPeer) addBytes(n int64) {
	p.mu.Lock()
	p.bytes += n
	p.mu.Unlock()
}

type socksPeerState struct {
	accepts  int
	connects int
	failures []string
	bytes    int64
}

func (p *socksPeer) state() socksPeerState {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := socksPeerState{accepts: len(p.accepts), connects: len(p.connects), bytes: p.bytes}
	out.failures = append(out.failures, p.failures...)
	return out
}

func (p *socksPeer) close() { p.ln.Close() }
