package tun

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests create real TUN interfaces, routes and a peer network namespace,
// so they run only when the explicit gate, an explicit isolated-netns marker
// and the container capabilities are all present. A host run of `go test ./...`
// skips them and touches no network state. Run them only from a dedicated,
// non-host-network container with iproute2 and the repository toolchain env
// (GOROOT/PATH/GOMODCACHE/GOCACHE), for example:
//
//	docker run --rm --privileged -v "$PWD:/src" -w /src \
//	  -e GOROOT=/src/.toolchain/go -e PATH=/src/.toolchain/go/bin:/usr/sbin:/usr/bin:/sbin:/bin \
//	  -e GOTOOLCHAIN=local -e GOMODCACHE=/src/.toolchain/modcache -e GOCACHE=/tmp/gocache \
//	  -e RL_TUN_TEST=1 -e RL_TUN_ISOLATED_NETNS=1 <disposable-image> \
//	  go test -run '^TestTunIntegration$' -v ./internal/tun
//
// RL_TUN_ISOLATED_NETNS asserts that the container has its own network
// namespace (not `--network host`); never set it on a host or a shared network.

func requireDisposableTunEnvironment(t *testing.T) {
	t.Helper()
	if os.Getenv("RL_TUN_TEST") != "1" {
		t.Skip("set RL_TUN_TEST=1 inside a disposable container to run the TUN integration test")
	}
	if os.Getenv("RL_TUN_ISOLATED_NETNS") != "1" {
		t.Skip("set RL_TUN_ISOLATED_NETNS=1 from the documented dedicated-container command")
	}
	if os.Geteuid() != 0 {
		t.Skip("the TUN integration harness must run as root inside a disposable container")
	}
	if !hasCapNetAdmin() {
		t.Skip("CAP_NET_ADMIN is absent")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Skip("/dev/net/tun is absent")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("iproute2 is missing")
	}
	if _, err := net.InterfaceByName(InterfaceName); err == nil {
		t.Skip("rl-tun0 already exists; refusing to disturb it")
	}
}

// TestTunServiceHelper is the service side of the integration test. The parent
// re-executes this test binary with RL_TUN_HELPER=1; each helper is a fresh
// process, so a core is never restarted in one process (R-4).
//
// The harness itself runs as root (it needs to create the TUN, routes and the
// peer namespace), so it calls the guarded body directly instead of Run. The
// production Run entrypoint keeps its non-root identity check, which is tested
// separately in run_test.go; it is not weakened for this harness.
func TestTunServiceHelper(t *testing.T) {
	if os.Getenv("RL_TUN_HELPER") != "1" {
		t.Skip("helper process only")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := RunOptions{
		ConfigPath: os.Getenv("RL_TUN_CONFIG"),
		SocketPath: os.Getenv("RL_TUN_SOCKET"),
		LeasePath:  os.Getenv("RL_TUN_LEASE"),
		Stderr:     os.Stderr,
	}
	cfg, err := loadRuntimeConfig(&opts)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := serveService(ctx, opts, cfg); err != nil {
		t.Fatalf("serve: %v", err)
	}
}

type socksFixture struct {
	mu      sync.Mutex
	targets []string
	ln      net.Listener
}

func startSocksFixture(t *testing.T) (*socksFixture, string) {
	t.Helper()
	return startSocksFixtureOn(t, "127.0.0.1:0")
}

// startSocksFixtureOn binds the fixture to an explicit address so a gated test
// can use a non-loopback peer and exercise the egress bypass for real.
func startSocksFixtureOn(t *testing.T, address string) (*socksFixture, string) {
	t.Helper()
	ln, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	f := &socksFixture{ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return f, ln.Addr().String()
}

func (f *socksFixture) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil || head[0] != 5 {
		return
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil || req[0] != 5 || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		raw := make([]byte, 4)
		if _, err := io.ReadFull(br, raw); err != nil {
			return
		}
		host = net.IP(raw).String()
	case 4:
		raw := make([]byte, 16)
		if _, err := io.ReadFull(br, raw); err != nil {
			return
		}
		host = net.IP(raw).String()
	case 3:
		n, err := br.ReadByte()
		if err != nil {
			return
		}
		raw := make([]byte, int(n))
		if _, err := io.ReadFull(br, raw); err != nil {
			return
		}
		host = string(raw)
	default:
		return
	}
	portRaw := make([]byte, 2)
	if _, err := io.ReadFull(br, portRaw); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portRaw))))
	f.mu.Lock()
	f.targets = append(f.targets, target)
	f.mu.Unlock()
	if _, err := conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	// The fixture is not a real HTTP/TLS peer; echo so the app sees a live
	// connection. The observation is the target Xray asked for.
	_, _ = io.Copy(conn, br)
}

func (f *socksFixture) waitFor(t *testing.T, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, got := range f.targets {
			if got == want {
				f.mu.Unlock()
				return
			}
		}
		f.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	f.mu.Lock()
	got := append([]string(nil), f.targets...)
	f.mu.Unlock()
	t.Fatalf("the SOCKS fixture did not see %q; saw %v", want, got)
}

func (f *socksFixture) saw(want string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, got := range f.targets {
		if got == want {
			return true
		}
	}
	return false
}

func waitLink(t *testing.T, present bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := net.InterfaceByName(InterfaceName)
		if (err == nil) == present {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, err := net.InterfaceByName(InterfaceName)
	t.Fatalf("rl-tun0 present=%v after %s (err=%v)", err == nil, timeout, err)
}

func waitService(t *testing.T, client *Client, state string, timeout time.Duration) contractStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last error
	var st contractStatus
	for time.Now().Before(deadline) {
		got, err := client.Status(context.Background())
		if err == nil {
			st = contractStatus{State: string(got.State), Desired: got.Desired}
			if string(got.State) == state {
				return st
			}
		} else {
			last = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service did not reach %q (last status=%+v err=%v)", state, st, last)
	return st
}

type contractStatus struct {
	State   string
	Desired bool
}

const udpPeerIP = "198.51.100.2"

// udpPeer is a UDP sink in a dedicated peer network namespace. The container's
// own addresses are local, so a same-namespace listener would bypass the TUN
// through the local routing table; a veth peer is the smallest faithful stand-in
// for R16-A's second container.
type udpPeer struct {
	ns       string
	hostLink string
	peerLink string
	logPath  string
	sink     *exec.Cmd
}

func setupUDPPeer(t *testing.T) *udpPeer {
	t.Helper()
	suffix := fmt.Sprintf("%d", os.Getpid()%100000)
	p := &udpPeer{
		ns:       "rltun" + suffix,
		hostLink: "rltunh" + suffix,
		peerLink: "rltunp" + suffix,
		logPath:  filepath.Join(t.TempDir(), "udp-sink.log"),
	}
	mustIP(t, "netns", "add", p.ns)
	t.Cleanup(func() {
		_ = exec.Command("ip", "netns", "del", p.ns).Run()
		_ = exec.Command("ip", "link", "del", p.hostLink).Run()
	})
	mustIP(t, "link", "add", p.hostLink, "type", "veth", "peer", "name", p.peerLink)
	mustIP(t, "link", "set", p.peerLink, "netns", p.ns)
	mustIP(t, "addr", "add", "198.51.100.1/30", "dev", p.hostLink)
	mustIP(t, "link", "set", p.hostLink, "up")
	mustIP(t, "netns", "exec", p.ns, "ip", "addr", "add", udpPeerIP+"/30", "dev", p.peerLink)
	mustIP(t, "netns", "exec", p.ns, "ip", "link", "set", "lo", "up")
	mustIP(t, "netns", "exec", p.ns, "ip", "link", "set", p.peerLink, "up")

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(p.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	p.sink = exec.Command("ip", "netns", "exec", p.ns, exe, "-test.run", "^TestTunUDPPeerSink$", "-test.v")
	p.sink.Env = append(os.Environ(),
		"RL_TUN_UDP_SINK="+net.JoinHostPort(udpPeerIP, "9999"),
		"RL_TUN_UDP_LOG="+p.logPath,
	)
	p.sink.Stdout = logFile
	p.sink.Stderr = logFile
	if err := p.sink.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if p.sink.Process != nil {
			_ = p.sink.Process.Kill()
		}
		_ = p.sink.Wait()
		_ = logFile.Close()
	})
	p.waitFor(t, "listening", 5*time.Second)
	return p
}

func (p *udpPeer) send(t *testing.T, payload string) {
	t.Helper()
	conn, err := net.Dial("udp", net.JoinHostPort(udpPeerIP, "9999"))
	if err != nil {
		t.Fatalf("udp dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("udp send: %v", err)
	}
}

func (p *udpPeer) lines(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(p.logPath)
	if err != nil {
		t.Fatalf("read udp sink log: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func (p *udpPeer) waitFor(t *testing.T, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, line := range p.lines(t) {
			if line == want || line == "datagram "+want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the UDP peer did not see %q; log=%v", want, p.lines(t))
}

func (p *udpPeer) assertNoDatagrams(t *testing.T, prefix string) {
	t.Helper()
	for _, line := range p.lines(t) {
		if strings.HasPrefix(line, "datagram "+prefix) {
			t.Fatalf("the UDP peer received %q while the TUN was active", line)
		}
	}
}

// TestTunUDPPeerSink is re-executed inside the peer namespace by setupUDPPeer.
func TestTunUDPPeerSink(t *testing.T) {
	addr := os.Getenv("RL_TUN_UDP_SINK")
	if addr == "" {
		t.Skip("UDP peer sink helper only")
	}
	logPath := os.Getenv("RL_TUN_UDP_LOG")
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	defer conn.Close()
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open sink log: %v", err)
	}
	defer f.Close()
	fmt.Fprintln(f, "listening")
	_ = f.Sync()
	buf := make([]byte, 2048)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		fmt.Fprintf(f, "datagram %s\n", strings.TrimSpace(string(buf[:n])))
		_ = f.Sync()
	}
}

func mustIP(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ip %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// The integration test itself. It runs inside the disposable container as root.

func TestTunIntegration(t *testing.T) {
	requireDisposableTunEnvironment(t)

	// Every failure path must leave no link, test route or peer namespace.
	t.Cleanup(func() {
		_ = exec.Command("ip", "route", "del", udpPeerIP+"/32", "dev", InterfaceName).Run()
		_ = exec.Command("ip", "link", "del", InterfaceName).Run()
	})
	peer := setupUDPPeer(t)

	fixture, socksAddr := startSocksFixture(t)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "tun.json")
	body := fmt.Sprintf(`{"allowed_uid":%d,"socks_address":%q}`, os.Getuid(), socksAddr)
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(dir, "control.sock")
	leasePath := filepath.Join(dir, "lease.json")

	resolvBefore, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := sha256.Sum256(resolvBefore)

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "helper.log")
	start := func() (*exec.Cmd, <-chan error) {
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run", "^TestTunServiceHelper$", "-test.v")
		cmd.Env = append(os.Environ(),
			"RL_TUN_HELPER=1",
			"RL_TUN_CONFIG="+cfgPath,
			"RL_TUN_SOCKET="+socketPath,
			"RL_TUN_LEASE="+leasePath,
		)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if err := cmd.Start(); err != nil {
			_ = logFile.Close()
			t.Fatal(err)
		}
		waitCh := make(chan error, 1)
		go func() {
			err := cmd.Wait()
			_ = logFile.Close()
			waitCh <- err
		}()
		return cmd, waitCh
	}
	stopHelper := func(cmd *exec.Cmd, waitCh <-chan error) {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-waitCh:
		case <-time.After(5 * time.Second):
			t.Fatalf("the helper did not exit; log:\n%s", readFile(logPath))
		}
	}
	var cmd *exec.Cmd
	var waitCh <-chan error
	defer func() {
		if cmd != nil && cmd.Process != nil && cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			if waitCh != nil {
				select {
				case <-waitCh:
				case <-time.After(3 * time.Second):
				}
			}
		}
		if t.Failed() {
			t.Logf("helper log:\n%s", readFile(logPath))
		}
	}()

	// The control datagram proves the peer listener is reachable while the
	// TUN path is not in use; the same listener must see nothing once the peer
	// is forced through the TUN below.
	peer.send(t, "control-before")
	peer.waitFor(t, "control-before", 3*time.Second)

	cmd, waitCh = start()
	client := &Client{Path: socketPath, Timeout: 10 * time.Second}
	waitService(t, client, "off", 10*time.Second)

	if err := client.Enable(context.Background()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	st := waitService(t, client, "on", 10*time.Second)
	if !st.Desired {
		t.Fatal("TUN is on but not desired")
	}
	waitLink(t, true, 2*time.Second)

	// The fixed dual-stack address and half routes must be present.
	assertLink(t, "198.18.0.1/15", "fc00::1/64")
	assertRoutes(t, "0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1")

	// HTTP Host sniffing reaches the loopback SOCKS fixture.
	httpConn, err := net.DialTimeout("tcp", "203.0.113.10:80", 5*time.Second)
	if err != nil {
		t.Fatalf("tcp through TUN: %v", err)
	}
	_, _ = httpConn.Write([]byte("GET / HTTP/1.1\r\nHost: canary-c.invalid\r\nConnection: close\r\n\r\n"))
	_ = httpConn.Close()
	fixture.waitFor(t, "canary-c.invalid:80", 5*time.Second)

	// TLS SNI over IPv4 and IPv6 is handed to SOCKS as the domain.
	for _, tc := range []struct {
		dest string
		sni  string
	}{
		{"203.0.113.11:443", "canary-tls-c.invalid"},
		{"[2001:db8::11]:443", "canary6-tls-c.invalid"},
	} {
		raw, err := net.DialTimeout("tcp", tc.dest, 5*time.Second)
		if err != nil {
			t.Fatalf("tls dial %s: %v", tc.dest, err)
		}
		_ = raw.SetDeadline(time.Now().Add(3 * time.Second))
		tlsConn := tls.Client(raw, &tls.Config{ServerName: tc.sni, InsecureSkipVerify: true}) // test fixture only
		_ = tlsConn.Handshake()
		_ = tlsConn.Close()
		fixture.waitFor(t, net.JoinHostPort(tc.sni, "443"), 5*time.Second)
	}

	// Non-DNS UDP is blackholed. The peer address is on-link, so a test-only
	// /32 (as in R16-A) forces it into the TUN; the peer sink must receive
	// zero datagrams. Its pre-TUN control datagram already proved the listener
	// works, so silence here means the packet really entered the blackhole.
	mustIP(t, "route", "add", udpPeerIP+"/32", "dev", InterfaceName)
	if got := mustIP(t, "route", "get", udpPeerIP); !strings.Contains(got, "dev "+InterfaceName) {
		t.Fatalf("the test peer route did not enter the TUN: %s", got)
	}
	for i := 0; i < 3; i++ {
		peer.send(t, fmt.Sprintf("blocked-%d", i))
	}
	time.Sleep(1200 * time.Millisecond)
	peer.assertNoDatagrams(t, "blocked-")

	// DNS system setting is untouched.
	resolvAfter, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(resolvAfter) != beforeHash {
		t.Fatal("/etc/resolv.conf changed while the TUN was up")
	}

	// SIGKILL: the link/routes disappear with the process, the lease stays on,
	// and a fresh service process rebuilds coverage.
	stopHelper(cmd, waitCh)
	waitLink(t, false, 3*time.Second)
	rec := readLeaseFile(t, leasePath)
	if rec.Desired != "on" {
		t.Fatalf("lease after SIGKILL=%+v", rec)
	}
	cmd, waitCh = start()
	waitService(t, client, "on", 10*time.Second)
	waitLink(t, true, 2*time.Second)

	// Explicit off removes coverage, records the wish, and exits 0.
	if err := client.Disable(context.Background()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	select {
	case err := <-waitCh:
		if err != nil {
			t.Fatalf("the helper exited with an error: %v\nlog:\n%s", err, readFile(logPath))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the helper did not exit after disable")
	}
	waitLink(t, false, 2*time.Second)
	if got := readLeaseFile(t, leasePath); got.Desired != "off" {
		t.Fatalf("lease after disable=%+v", got)
	}
	_ = exec.Command("ip", "route", "del", udpPeerIP+"/32", "dev", InterfaceName).Run()
	peer.send(t, "control-after")
	peer.waitFor(t, "control-after", 3*time.Second)
}

func assertLink(t *testing.T, want ...string) {
	t.Helper()
	iface, err := net.InterfaceByName(InterfaceName)
	if err != nil {
		t.Fatalf("interface: %v", err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, a := range addrs {
		have[a.String()] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Fatalf("missing address %s; have %v", w, addrs)
		}
	}
}

func assertRoutes(t *testing.T, want ...string) {
	t.Helper()
	for _, w := range want {
		family := "-6"
		if strings.Contains(w, ".") {
			family = "-4"
		}
		out, err := exec.Command("ip", family, "route", "show", "dev", InterfaceName).Output()
		if err != nil {
			t.Fatalf("ip %s route: %v", family, err)
		}
		found := false
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), w+" ") {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing route %s; output:\n%s", w, out)
		}
	}
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(unreadable: %v)", err)
	}
	return string(b)
}
