package engine_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"

	_ "github.com/xtls/xray-core/main/distro/all"

	"routelatch/internal/contract"
	"routelatch/internal/engine"
)

// The helpers below build loopback exits and destinations. They are test-only:
// they reuse the technique of spike/xray but not its code, and nothing here is
// part of the product.

// TestHelperExit is the child-process exit server. It runs in its own process
// so the parent test process holds exactly one core (the engine under test),
// which is also what the product assumes.
func TestHelperExit(t *testing.T) {
	if os.Getenv("RL_HELPER_EXIT") != "1" {
		t.Skip("helper process")
	}
	var spec exitSpec
	if err := json.NewDecoder(os.Stdin).Decode(&spec); err != nil {
		fmt.Fprintf(os.Stderr, "exit helper: bad spec: %v\n", err)
		os.Exit(2)
	}
	cfg, err := serial.LoadJSONConfig(strings.NewReader(string(spec.config())))
	if err != nil {
		fmt.Fprintf(os.Stderr, "exit helper: config: %v\n", err)
		os.Exit(2)
	}
	inst, err := core.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exit helper: new: %v\n", err)
		os.Exit(2)
	}
	if err := inst.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "exit helper: start: %v\n", err)
		os.Exit(2)
	}
	fmt.Println("READY")
	_, _ = io.Copy(io.Discard, os.Stdin) // the parent closing stdin is the stop signal
	_ = inst.Close()
}

type exitSpec struct {
	Protocol string `json:"protocol"` // "vless" | "trojan"
	Port     int    `json:"port"`
	UUID     string `json:"uuid,omitempty"`
	Password string `json:"password,omitempty"`
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`
	SNI      string `json:"sni,omitempty"`
}

func (s exitSpec) config() []byte {
	network := "tcp"
	var stream map[string]any
	if s.CertFile != "" {
		stream = map[string]any{
			"network":  network,
			"security": "tls",
			"tlsSettings": map[string]any{
				"serverName": s.SNI,
				"certificates": []any{
					map[string]any{"certificateFile": s.CertFile, "keyFile": s.KeyFile},
				},
			},
		}
	} else {
		stream = map[string]any{"network": network}
	}

	var inbound map[string]any
	switch s.Protocol {
	case "vless":
		inbound = map[string]any{
			"tag":      "in",
			"listen":   "127.0.0.1",
			"port":     s.Port,
			"protocol": "vless",
			"settings": map[string]any{
				"clients":    []any{map[string]any{"id": s.UUID}},
				"decryption": "none",
			},
			"streamSettings": stream,
		}
	case "trojan":
		inbound = map[string]any{
			"tag":            "in",
			"listen":         "127.0.0.1",
			"port":           s.Port,
			"protocol":       "trojan",
			"settings":       map[string]any{"clients": []any{map[string]any{"password": s.Password}}},
			"streamSettings": stream,
		}
	default:
		panic("exitSpec: unknown protocol " + s.Protocol)
	}

	cfg := map[string]any{
		"log":      map[string]any{"loglevel": "warning", "access": "none"},
		"inbounds": []any{inbound},
		"outbounds": []any{map[string]any{
			"protocol": "freedom",
			"tag":      "direct",
			// The pinned core blocks private destinations in freedom by
			// default; this loopback fixture explicitly allows 127/8. It is a
			// test-exit exception, not RouteLatch behaviour (T1 §0).
			"settings": map[string]any{
				"finalRules": []any{map[string]any{
					"action": "allow", "network": "tcp", "ip": []string{"127.0.0.0/8"},
				}},
			},
		}},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		panic(err)
	}
	return raw
}

type exitProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	port  int
	logs  *safeBuffer
	once  sync.Once
}

func startExit(t *testing.T, spec exitSpec) *exitProc {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestHelperExit$")
	cmd.Env = append(os.Environ(), "RL_HELPER_EXIT=1")
	logs := &safeBuffer{}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start exit: %v", err)
	}
	if err := json.NewEncoder(stdin).Encode(spec); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("send exit spec: %v", err)
	}
	ready := make(chan error, 1)
	go func() {
		br := bufio.NewReader(stdout)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				ready <- fmt.Errorf("exit stdout ended before READY: %w (%s)", err, logs.String())
				return
			}
			if strings.TrimSpace(line) == "READY" {
				ready <- nil
				break
			}
		}
		_, _ = io.Copy(io.Discard, br)
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("%v", err)
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("exit did not become ready: %s", logs.String())
	}
	proc := &exitProc{cmd: cmd, stdin: stdin, port: spec.Port, logs: logs}
	t.Cleanup(func() { proc.stop() })
	return proc
}

func (p *exitProc) stop() {
	if p == nil || p.cmd == nil {
		return
	}
	p.once.Do(func() {
		_ = p.stdin.Close()
		done := make(chan struct{})
		go func() {
			_ = p.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = p.cmd.Process.Kill()
			<-done
		}
	})
}

type safeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// peer is a loopback destination that records what happened to it.
type peer struct {
	ln    net.Listener
	mode  string       // "echo", "accept-silent", "close-on-accept", "reset-on-data"
	conns atomic.Int64 // accepted connections
	first atomic.Int64 // unix nanoseconds of the first accept
	mu    sync.Mutex
	open  []net.Conn
}

func newPeer(t *testing.T, mode string) *peer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("peer listen: %v", err)
	}
	p := &peer{ln: ln, mode: mode}
	go p.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		p.closeAll()
	})
	return p
}

func (p *peer) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		n := p.conns.Add(1)
		if n == 1 {
			p.first.Store(time.Now().UnixNano())
		}
		p.mu.Lock()
		p.open = append(p.open, c)
		p.mu.Unlock()

		switch p.mode {
		case "echo":
			go func() {
				_, _ = io.Copy(c, c)
				_ = c.Close()
			}()
		case "close-on-accept":
			_ = c.Close()
		case "reset-on-data":
			go func() {
				_ = c.(*net.TCPConn).SetLinger(0)
				buf := make([]byte, 1)
				_, _ = c.Read(buf)
				_ = c.Close()
			}()
		case "accept-silent":
			// keep the connection open and never speak
		}
	}
}

func (p *peer) closeAll() {
	p.mu.Lock()
	open := p.open
	p.open = nil
	p.mu.Unlock()
	for _, c := range open {
		_ = c.Close()
	}
}

func (p *peer) port() uint16 { return uint16(p.ln.Addr().(*net.TCPAddr).Port) }

func (p *peer) accepts() int64 { return p.conns.Load() }

func (p *peer) waitAccept(d time.Duration) bool {
	return p.waitAccepts(1, d)
}

func (p *peer) waitAccepts(n int64, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if p.conns.Load() >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return p.conns.Load() >= n
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}

func randomUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// tlsFixture writes a self-signed loopback certificate and returns the files
// and the leaf pin. TLS verification stays on; the client pins the leaf.
func tlsFixture(t *testing.T) (certFile, keyFile, pin string) {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "rl.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(2 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"rl.test"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	sum := sha256.Sum256(der)
	return certFile, keyFile, hex.EncodeToString(sum[:])
}

func vlessRecord(id, server string, port uint16, uuid string, mutate func(*contract.Record)) contract.Record {
	rec := contract.Record{
		ID:          contract.RecordID(id),
		DedupeKey:   "sha256:" + id,
		Protocol:    contract.ProtocolVLESS,
		Server:      server,
		Port:        port,
		Credentials: contract.Credentials{UserID: uuid},
		Transport:   contract.TransportRaw,
		Security:    contract.SecurityNone,
		Status:      contract.RecordUnknown,
	}
	if mutate != nil {
		mutate(&rec)
	}
	return rec
}

func trojanRecord(id, server string, port uint16, password string, mutate func(*contract.Record)) contract.Record {
	rec := contract.Record{
		ID:          contract.RecordID(id),
		DedupeKey:   "sha256:" + id,
		Protocol:    contract.ProtocolTrojan,
		Server:      server,
		Port:        port,
		Credentials: contract.Credentials{Password: password},
		Transport:   contract.TransportRaw,
		Security:    contract.SecurityNone,
		Status:      contract.RecordUnknown,
	}
	if mutate != nil {
		mutate(&rec)
	}
	return rec
}

func socksRecord(id, server string, port uint16, user, pass string) contract.Record {
	return contract.Record{
		ID:          contract.RecordID(id),
		DedupeKey:   "sha256:" + id,
		Protocol:    contract.ProtocolSOCKS5,
		Server:      server,
		Port:        port,
		Credentials: contract.Credentials{UserID: user, Password: pass},
		Transport:   contract.TransportRaw,
		Security:    contract.SecurityNone,
		Status:      contract.RecordUnknown,
	}
}

func startEngine(t *testing.T, version contract.PolicyVersion, records ...contract.Record) *engine.Engine {
	t.Helper()
	e, err := engine.Start(engine.Options{Exits: records, PolicyVersion: version, LogLevel: "none"})
	if err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.Stop(ctx); err != nil {
			t.Errorf("stop engine: %v", err)
		}
	})
	return e
}

func ipDestination(port uint16) contract.Destination {
	return contract.Destination{Kind: contract.DestIP, Addr: netip.MustParseAddr("127.0.0.1"), Port: port}
}

func domainDestination(host string, port uint16) contract.Destination {
	return contract.Destination{Kind: contract.DestDomain, Host: host, Port: port}
}

func dialRequest(exit contract.ExitID, dst contract.Destination, version contract.PolicyVersion) engine.DialRequest {
	return engine.DialRequest{Exit: exit, Destination: dst, PolicyVersion: version}
}

// failureKind extracts the contract kind from an error.
func failureKind(t *testing.T, err error) contract.ErrorKind {
	t.Helper()
	var f *contract.Failure
	if !errors.As(err, &f) {
		t.Fatalf("error %v is not a *contract.Failure", err)
	}
	return f.Kind
}

// expectExcluded starts an engine with one record that cannot be built and
// asserts the record was isolated, reported, and not registered as an exit.
func expectExcluded(t *testing.T, rec contract.Record, kind contract.ErrorKind) {
	t.Helper()
	e, err := engine.Start(engine.Options{Exits: []contract.Record{rec}, LogLevel: "none"})
	if err != nil {
		t.Fatalf("start with unbuildable record %q: %v", rec.ID, err)
	}
	defer func() { _ = e.Stop(context.Background()) }()
	excluded := e.Excluded()
	if len(excluded) != 1 {
		t.Fatalf("excluded = %+v, want one record", excluded)
	}
	if excluded[0].ID != contract.ExitID(rec.ID) || excluded[0].Kind != kind || excluded[0].Reason == "" {
		t.Fatalf("excluded record = %+v, want id=%q kind=%q with a reason", excluded[0], rec.ID, kind)
	}
	if e.HasExit(contract.ExitID(rec.ID)) {
		t.Fatalf("excluded record %q is still registered", rec.ID)
	}
}

func waitGoroutines(t *testing.T, want int, d time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		got := runtime.NumGoroutine()
		if got <= want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func socketFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read fds: %v", err)
	}
	n := 0
	for _, e := range ents {
		target, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err == nil && strings.HasPrefix(target, "socket:") {
			n++
		}
	}
	return n
}
