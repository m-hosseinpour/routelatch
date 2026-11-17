package main

import (
	"io"
	"net"
	"sync"
	"time"
)

// forwarder is a transparent loopback TCP hop in front of the exit. It counts
// how many transport connections the client core opens and how many stay open.
// Xray does not expose its warm transport pools, so this peer-side counter is
// the only way to see connection reuse and residue.
type forwarder struct {
	name       string
	targetPort int
	ln         net.Listener

	mu      sync.Mutex
	accepts []time.Time
	closes  []time.Time
	active  int
	bytes   int64
}

func startForwarder(name string, targetPort int) (*forwarder, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f := &forwarder{name: name, targetPort: targetPort, ln: ln}
	go f.serve()
	return f, nil
}

func (f *forwarder) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *forwarder) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.accepts = append(f.accepts, time.Now())
		f.active++
		f.mu.Unlock()
		go f.handle(c)
	}
}

func (f *forwarder) handle(c net.Conn) {
	defer func() {
		c.Close()
		f.mu.Lock()
		f.active--
		f.closes = append(f.closes, time.Now())
		f.mu.Unlock()
	}()
	up, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(f.targetPort)))
	if err != nil {
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	go func() {
		n, _ := io.Copy(up, c)
		f.addBytes(n)
		if tc, ok := up.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		n, _ := io.Copy(c, up)
		f.addBytes(n)
		if tc, ok := c.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}

func (f *forwarder) addBytes(n int64) {
	f.mu.Lock()
	f.bytes += n
	f.mu.Unlock()
}

type forwarderState struct {
	accepts int
	closes  int
	active  int
	bytes   int64
}

func (f *forwarder) state() forwarderState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return forwarderState{accepts: len(f.accepts), closes: len(f.closes), active: f.active, bytes: f.bytes}
}

func (f *forwarder) reset() {
	f.mu.Lock()
	f.accepts = nil
	f.closes = nil
	f.bytes = 0
	f.mu.Unlock()
}

func (f *forwarder) close() {
	if f != nil {
		f.ln.Close()
	}
}

func mustForwarder(rep *reporter, name string, targetPort int) *forwarder {
	f, err := startForwarder(name, targetPort)
	if err != nil {
		panic(err)
	}
	rep.kv("forwarder "+name, "127.0.0.1:"+itoa(f.port())+" -> exit 127.0.0.1:"+itoa(targetPort))
	return f
}
