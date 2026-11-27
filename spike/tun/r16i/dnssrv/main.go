// R16-I disposable DNS fixture. It is not product code.
//
// Modes:
//   - doh: RFC 8484 DoH server over TLS (HTTP/2), answering A/AAAA with fixed
//     synthetic addresses and logging the queried name (test-only).
//   - tcp: plain DNS-over-TCP server on the same wire format, for the
//     trust-boundary comparison.
package main

import (
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

type logger struct {
	mu   sync.Mutex
	path string
}

func (l *logger) event(obj map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	obj["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, _ := json.Marshal(obj)
	f.Write(append(b, '\n'))
}

type question struct {
	name  string
	qtype uint16
	end   int
}

func parseQuestion(msg []byte) (question, bool) {
	var q question
	if len(msg) < 12 {
		return q, false
	}
	off := 12
	var name []byte
	for {
		if off >= len(msg) {
			return q, false
		}
		n := int(msg[off])
		off++
		if n == 0 {
			break
		}
		if n&0xc0 != 0 || off+n > len(msg) {
			return q, false
		}
		if len(name) > 0 {
			name = append(name, '.')
		}
		name = append(name, msg[off:off+n]...)
		off += n
	}
	if off+4 > len(msg) {
		return q, false
	}
	q.name = string(name)
	q.qtype = binary.BigEndian.Uint16(msg[off:])
	q.end = off + 4
	return q, true
}

func response(query []byte, answer4, answer6 net.IP) []byte {
	q, ok := parseQuestion(query)
	if !ok {
		return nil
	}
	var rdata []byte
	qtype := q.qtype
	switch qtype {
	case 1:
		rdata = answer4.To4()
	case 28:
		rdata = answer6.To16()
	default:
		rdata = nil
	}
	if qtype == 1 && rdata == nil {
		rdata = []byte{0, 0, 0, 0}
	}
	if qtype == 28 && rdata == nil {
		rdata = make([]byte, 16)
	}
	an := uint16(0)
	if rdata != nil {
		an = 1
	}
	out := make([]byte, 0, 12+(q.end-12)+16+len(rdata))
	out = append(out, query[0], query[1], 0x81, 0x80, 0, 1, 0, byte(an), 0, 0, 0, 0)
	out = append(out, query[12:q.end]...)
	if rdata != nil {
		out = append(out, 0xc0, 0x0c)
		out = append(out, byte(qtype>>8), byte(qtype), 0, 1, 0, 0, 0, 60)
		out = append(out, byte(len(rdata)>>8), byte(len(rdata)))
		out = append(out, rdata...)
	}
	return out
}

func main() {
	mode := flag.String("mode", "doh", "doh|tcp")
	addr := flag.String("addr", ":8443", "listen address")
	cert := flag.String("cert", "", "TLS certificate (doh)")
	key := flag.String("key", "", "TLS key (doh)")
	logPath := flag.String("log", "/work/logs/dnssrv.log", "JSONL log")
	answer4 := flag.String("answer4", "203.0.113.201", "A answer")
	answer6 := flag.String("answer6", "2001:db8::201", "AAAA answer")
	flag.Parse()

	l := &logger{path: *logPath}
	ip4 := net.ParseIP(*answer4)
	ip6 := net.ParseIP(*answer6)
	if ip4 == nil || ip6 == nil {
		log.Fatal("bad answer address")
	}

	switch *mode {
	case "doh":
		http.HandleFunc("/dns-query", func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
			if err != nil {
				http.Error(w, "read", http.StatusBadRequest)
				return
			}
			q, ok := parseQuestion(body)
			if !ok {
				http.Error(w, "dns", http.StatusBadRequest)
				return
			}
			l.event(map[string]any{"event": "doh_query", "name": q.name, "qtype": q.qtype,
				"remote": r.RemoteAddr, "proto": r.Proto})
			resp := response(body, ip4, ip6)
			if resp == nil {
				http.Error(w, "dns", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/dns-message")
			w.Write(resp)
		})
		srv := &http.Server{
			Addr:      *addr,
			TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			ErrorLog:  log.New(io.Discard, "", 0),
		}
		l.event(map[string]any{"event": "doh_listen", "addr": *addr})
		log.Fatal(srv.ListenAndServeTLS(*cert, *key))
	case "tcp":
		ln, err := net.Listen("tcp", *addr)
		if err != nil {
			log.Fatal(err)
		}
		l.event(map[string]any{"event": "tcp_listen", "addr": *addr})
		for {
			c, err := ln.Accept()
			if err != nil {
				log.Fatal(err)
			}
			go func(c net.Conn) {
				defer c.Close()
				c.SetDeadline(time.Now().Add(10 * time.Second))
				var hdr [2]byte
				if _, err := io.ReadFull(c, hdr[:]); err != nil {
					return
				}
				n := int(binary.BigEndian.Uint16(hdr[:]))
				if n < 12 || n > 4096 {
					return
				}
				body := make([]byte, n)
				if _, err := io.ReadFull(c, body); err != nil {
					return
				}
				q, ok := parseQuestion(body)
				if !ok {
					return
				}
				l.event(map[string]any{"event": "tcp_query", "name": q.name, "qtype": q.qtype,
					"remote": c.RemoteAddr().String()})
				resp := response(body, ip4, ip6)
				if resp == nil {
					return
				}
				out := make([]byte, 2+len(resp))
				binary.BigEndian.PutUint16(out, uint16(len(resp)))
				copy(out[2:], resp)
				if _, err := c.Write(out); err != nil {
					fmt.Fprintln(os.Stderr, err)
				}
			}(c)
		}
	default:
		log.Fatalf("unknown mode %q", *mode)
	}
}
