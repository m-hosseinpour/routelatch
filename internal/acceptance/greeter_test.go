package acceptance

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

// greeterPeer is a loopback destination that writes one byte as soon as it is
// contacted, so an egress probe can observe the first response byte.
type greeterPeer struct {
	ln       net.Listener
	contacts atomic.Int64
	mu       sync.Mutex
	conns    []net.Conn
}

func newGreeterPeer(t *testing.T, greeting string) *greeterPeer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("greeter listen: %v", err)
	}
	g := &greeterPeer{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			g.contacts.Add(1)
			g.mu.Lock()
			g.conns = append(g.conns, c)
			g.mu.Unlock()
			_, _ = c.Write([]byte(greeting))
			go func() { _, _ = c.Read(make([]byte, 1)) }()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		g.mu.Lock()
		conns := g.conns
		g.conns = nil
		g.mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return g
}

func (g *greeterPeer) port() uint16 { return uint16(g.ln.Addr().(*net.TCPAddr).Port) }
