package main

import (
	"io"
	"net"
	"sync"
	"time"
)

// peerMode describes how a loopback TCP peer behaves. The destination side of a
// pool-routed dial is one of these peers, or the peer acts as a fake exit that
// accepts the proxy transport and then does nothing.
type peerMode int

const (
	peerEcho peerMode = iota
	peerSilent
	peerCloseAfterAccept
	peerCloseAfterData
	peerResetOnData
	peerHoldUntilEOF
)

func (m peerMode) String() string {
	switch m {
	case peerEcho:
		return "echo"
	case peerSilent:
		return "silent"
	case peerCloseAfterAccept:
		return "close-after-accept"
	case peerCloseAfterData:
		return "close-after-data"
	case peerResetOnData:
		return "reset-after-data"
	case peerHoldUntilEOF:
		return "hold-until-eof"
	default:
		return "unknown"
	}
}

type peerEvent struct {
	at   time.Time
	kind string
}

type peer struct {
	name  string
	mode  peerMode
	delay time.Duration

	ln net.Listener

	mu        sync.Mutex
	accepts   []time.Time
	firstByte []time.Time
	eofs      []time.Time
	bytesIn   int
	events    []peerEvent

	closed chan struct{}
	once   sync.Once
}

func startPeer(name string, mode peerMode, delay time.Duration) (*peer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &peer{name: name, mode: mode, delay: delay, ln: ln, closed: make(chan struct{})}
	go p.serve()
	return p, nil
}

func (p *peer) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *peer) serve() {
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

func (p *peer) handle(c net.Conn) {
	defer c.Close()
	switch p.mode {
	case peerCloseAfterAccept:
		if p.delay > 0 {
			time.Sleep(p.delay)
		}
		p.note("close")
		return
	case peerCloseAfterData:
		buf := make([]byte, 256)
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _ := c.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.bytesIn += n
			p.firstByte = append(p.firstByte, time.Now())
			p.mu.Unlock()
		}
		if p.delay > 0 {
			time.Sleep(p.delay)
		}
		p.note("clean-close")
		return
	case peerResetOnData:
		buf := make([]byte, 64)
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _ := c.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.bytesIn += n
			p.firstByte = append(p.firstByte, time.Now())
			p.mu.Unlock()
		}
		if p.delay > 0 {
			time.Sleep(p.delay)
		}
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetLinger(0) // RST on close instead of FIN
		}
		p.note("reset-close")
		return
	}

	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.bytesIn += n
			if len(p.firstByte) == 0 {
				p.firstByte = append(p.firstByte, time.Now())
			}
			p.mu.Unlock()
			if p.mode == peerEcho {
				if _, werr := c.Write(buf[:n]); werr != nil {
					break
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				p.note("read-error:" + err.Error())
			}
			p.note("eof")
			return
		}
	}
}

func (p *peer) note(kind string) {
	p.mu.Lock()
	p.events = append(p.events, peerEvent{at: time.Now(), kind: kind})
	if kind == "eof" {
		p.eofs = append(p.eofs, time.Now())
	}
	p.mu.Unlock()
}

type peerState struct {
	accepts      int
	bytesIn      int
	firstAccept  time.Time
	firstByte    time.Time
	eofs         int
	lastEOF      time.Time
	eventSummary string
}

func (p *peer) state() peerState {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := peerState{accepts: len(p.accepts), bytesIn: p.bytesIn, eofs: len(p.eofs)}
	if len(p.accepts) > 0 {
		st.firstAccept = p.accepts[0]
	}
	if len(p.firstByte) > 0 {
		st.firstByte = p.firstByte[0]
	}
	if len(p.eofs) > 0 {
		st.lastEOF = p.eofs[len(p.eofs)-1]
	}
	for _, e := range p.events {
		st.eventSummary += e.kind + " "
	}
	return st
}

func (p *peer) reset() {
	p.mu.Lock()
	p.accepts = nil
	p.firstByte = nil
	p.eofs = nil
	p.bytesIn = 0
	p.events = nil
	p.mu.Unlock()
}

func (p *peer) waitAccept(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if p.state().accepts > 0 {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return p.state().accepts > 0
}

func (p *peer) waitEOF(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if p.state().eofs > 0 {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return p.state().eofs > 0
}

func (p *peer) waitEOFCount(n int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if p.state().eofs >= n {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return p.state().eofs >= n
}

func (p *peer) close() {
	p.once.Do(func() {
		close(p.closed)
		p.ln.Close()
	})
}
