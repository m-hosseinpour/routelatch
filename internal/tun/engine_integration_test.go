package tun

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/egress"
	"routelatch/internal/store"
)

// TestTunEngineIntegration wires the separate TUN service to the real control
// service in a disposable container: the desired preference is persisted, the
// engine starts TUN before its listeners, the loopback SOCKS listener is the
// service target, a user flow reaches it, and Quit disables coverage with no
// orphan link. It never runs on a host (`RL_TUN_TEST` + isolated netns gate).
func TestTunEngineIntegration(t *testing.T) {
	requireDisposableTunEnvironment(t)

	t.Cleanup(func() {
		_ = exec.Command("ip", "link", "del", InterfaceName).Run()
	})

	// A loopback destination keeps this test independent of any public exit;
	// the assertion is that the engine flow works while TUN is active and that
	// the engine's own dial is not captured by its own coverage.
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			conn, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	httpAddr := freeLoopback(t)
	socksAddr := freeLoopback(t)

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := store.ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	// A non-loopback fixture peer makes the engine's own pool/probe/refresh
	// dials actually traverse the egress controller. Loopback is skipped by
	// design, so a loopback-only test cannot prove the bind happens.
	fixtureHost := nonLoopbackIPv4(t)
	exitFixture, exitAddr := startSocksFixtureOn(t, net.JoinHostPort(fixtureHost, "0"))
	exitHost, exitPortText, err := net.SplitHostPort(exitAddr)
	if err != nil {
		t.Fatal(err)
	}
	exitPort, err := strconv.Atoi(exitPortText)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Mode = contract.ModeTargeted
		state.PolicyVersion++
		state.Records = []contract.Record{{
			ID: "x-tun-fixture", DedupeKey: "sha256:x-tun-fixture", Status: contract.RecordUnknown,
			Protocol: contract.ProtocolSOCKS5, Server: exitHost, Port: uint16(exitPort),
			Transport: contract.TransportRaw, Security: contract.SecurityNone,
		}}
		state.Rules = []contract.Rule{{
			ID: "r-tun-fixture", Kind: contract.RuleIP, Addr: netip.MustParseAddr("203.0.113.7"),
			Origin: contract.RuleOriginUser, AddedAt: time.Now().UTC(),
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.TunEnabled = true
		cfg.HTTPListen = httpAddr
		cfg.SOCKSListen = socksAddr
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "tun.json")
	body := fmt.Sprintf(`{"allowed_uid":%d,"socks_address":%q}`, os.Getuid(), socksAddr)
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(dir, "control.sock")
	leasePath := filepath.Join(dir, "lease.json")

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helperLog := filepath.Join(dir, "helper.log")
	logFile, err := os.OpenFile(helperLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(exe, "-test.run", "^TestTunServiceHelper$", "-test.v")
	helper.Env = append(os.Environ(),
		"RL_TUN_HELPER=1",
		"RL_TUN_CONFIG="+cfgPath,
		"RL_TUN_SOCKET="+socketPath,
		"RL_TUN_LEASE="+leasePath,
	)
	helper.Stdout = logFile
	helper.Stderr = logFile
	if err := helper.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- helper.Wait()
		_ = logFile.Close()
	}()
	t.Cleanup(func() {
		if helper.Process != nil && helper.ProcessState == nil {
			_ = helper.Process.Kill()
			select {
			case <-waitCh:
			case <-time.After(3 * time.Second):
			}
		}
		if t.Failed() {
			t.Logf("helper log:\n%s", readFile(helperLog))
		}
	})

	client := &Client{Path: socketPath, Timeout: 20 * time.Second}
	waitService(t, client, "off", 10*time.Second)

	ctrl := egress.NewController()
	svc, err := control.NewService(context.Background(), control.Options{
		Store: st, Paths: paths, Tun: client, LogOutput: io.Discard, Bypass: ctrl,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.StartNetwork(context.Background()); err != nil {
		t.Fatalf("start network: %v", err)
	}
	waitLink(t, true, 5*time.Second)
	if !ctrl.Active() || ctrl.Interface() == "" {
		t.Fatalf("egress bypass active=%v iface=%q, want an active physical binding", ctrl.Active(), ctrl.Interface())
	}
	st2, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st2.State != contract.TunOn || !st2.Desired {
		t.Fatalf("tun status=%+v, want on+desired", st2)
	}
	if st2.SocksAddress != socksAddr {
		t.Fatalf("service target over the private socket=%q, want the engine listener %q", st2.SocksAddress, socksAddr)
	}

	// A user flow through the engine's own SOCKS listener still works while the
	// separate TUN is active. The destination is loopback, which is excluded
	// from the bypass by design.
	echoAddr := echoLn.Addr().String()
	conn, err := dialSOCKS5(socksAddr, echoAddr)
	if err != nil {
		t.Fatalf("socks dial through the engine: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	payload := []byte("engine-tun-integration")
	if _, err := conn.Write(payload); err != nil {
		_ = conn.Close()
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		_ = conn.Close()
		t.Fatalf("read: %v", err)
	}
	_ = conn.Close()
	if string(got) != string(payload) {
		t.Fatalf("echo=%q, want %q", got, payload)
	}

	// An approved destination uses the pool. The exit is loopback, so the
	// engine's pool dial is excluded from the physical binding by design; the
	// fixture must see exactly one request (a TUN loop would repeat it).
	poolConn, err := dialSOCKS5(socksAddr, "203.0.113.7:80")
	if err != nil {
		t.Fatalf("pool socks dial: %v", err)
	}
	_ = poolConn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := poolConn.Write(payload); err != nil {
		_ = poolConn.Close()
		t.Fatalf("pool write: %v", err)
	}
	if _, err := io.ReadFull(poolConn, got); err != nil {
		_ = poolConn.Close()
		t.Fatalf("pool read: %v", err)
	}
	_ = poolConn.Close()
	exitFixture.waitFor(t, "203.0.113.7:80", 5*time.Second)
	exitFixture.mu.Lock()
	poolRequests := len(exitFixture.targets)
	exitFixture.mu.Unlock()
	if poolRequests != 1 {
		t.Fatalf("pool fixture requests=%d, want 1 (loop counter must stay 0)", poolRequests)
	}

	// A transport probe dials the exit through the same bypass. It must reach
	// the fixture directly while TUN is active.
	raw, err := svc.Handle(context.Background(), control.Request{Op: control.OpPoolTest, ID: "x-tun-fixture"})
	if err != nil {
		t.Fatalf("pool test while TUN is active: %v", err)
	}
	tested, ok := raw.(control.PoolTestData)
	if !ok || len(tested.Results) != 1 || tested.Results[0].Outcome != contract.OutcomeSuccess {
		t.Fatalf("pool test=%+v, want one successful transport probe", raw)
	}

	// A subscription refresh is engine egress too. The source is served from a
	// non-loopback peer, so reaching it proves the fetcher used the same
	// bypassed dialer while TUN was active. A fixture counter makes the hit
	// explicit rather than relying on import success.
	refreshHits := make(chan string, 1)
	refreshLn, err := net.Listen("tcp", net.JoinHostPort(fixtureHost, "0"))
	if err != nil {
		t.Fatal(err)
	}
	refreshSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case refreshHits <- r.URL.Path:
		default:
		}
		_, _ = fmt.Fprint(w, "vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls#Refresh")
	})}
	go func() { _ = refreshSrv.Serve(refreshLn) }()
	defer func() { _ = refreshSrv.Close() }()
	refreshOrigin := "http://" + refreshLn.Addr().String() + "/subscription"
	if _, err := st.Mutate(context.Background(), func(state *contract.State) error {
		state.Sources = append(state.Sources, contract.Source{
			ID: "src-refresh", Kind: contract.SourceSubscription, Label: "refresh",
			Origin: refreshOrigin, Order: 1, AddedAt: time.Now().UTC(),
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Handle(context.Background(), control.Request{Op: control.OpPoolRefresh}); err != nil {
		t.Fatalf("refresh while TUN is active: %v", err)
	}
	select {
	case <-refreshHits:
	case <-time.After(5 * time.Second):
		t.Fatal("the subscription fetch did not reach the non-loopback source; bypass might be broken")
	}

	// Quit order: coverage closes before listeners/engine, and the helper exits
	// without leaving a link or routes behind.
	if err := svc.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if ctrl.Active() {
		t.Fatal("egress bypass stayed active after Quit")
	}
	select {
	case err := <-waitCh:
		if err != nil {
			t.Fatalf("helper exited with error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not exit after Quit")
	}
	waitLink(t, false, 5*time.Second)

	// SIGTERM is the Quit path used by the supervisor and by `run --headless`.
	// A directly-run engine must close coverage before it exits; the separate
	// service then exits and leaves no link behind.
	helper2, wait2 := startTunHelperProcess(t, cfgPath, socketPath, leasePath, helperLog)
	t.Cleanup(func() {
		if helper2.Process != nil && helper2.ProcessState == nil {
			_ = helper2.Process.Kill()
			select {
			case <-wait2:
			case <-time.After(3 * time.Second):
			}
		}
	})
	waitService(t, client, "off", 10*time.Second)

	engineHelper := exec.Command(exe, "-test.run", "^TestTunEngineSignalHelper$", "-test.v")
	engineHelper.Env = append(os.Environ(),
		"RL_TUN_ENGINE_HELPER=1",
		"RL_TUN_ENGINE_DATA="+dir,
		"RL_TUN_SOCKET="+socketPath,
	)
	engineHelper.Stdout = logFile
	engineHelper.Stderr = logFile
	if err := engineHelper.Start(); err != nil {
		t.Fatal(err)
	}
	engineWait := make(chan error, 1)
	go func() { engineWait <- engineHelper.Wait() }()
	t.Cleanup(func() {
		if engineHelper.Process != nil && engineHelper.ProcessState == nil {
			_ = engineHelper.Process.Kill()
			select {
			case <-engineWait:
			case <-time.After(3 * time.Second):
			}
		}
	})
	waitService(t, client, "on", 15*time.Second)
	if err := engineHelper.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal engine helper: %v", err)
	}
	select {
	case err := <-engineWait:
		if err != nil {
			t.Fatalf("engine helper exited with error: %v\nlog:\n%s", err, readFile(helperLog))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("engine helper did not exit on SIGTERM")
	}
	select {
	case err := <-wait2:
		if err != nil {
			t.Fatalf("TUN helper exited with error after SIGTERM: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("TUN helper did not exit after SIGTERM")
	}
	waitLink(t, false, 5*time.Second)
}

// TestTunEngineSignalHelper is the engine child used by the SIGTERM scenario.
// It starts the real control service with the shipped TUN client, then closes
// coverage on SIGTERM exactly like `run --headless`.
func TestTunEngineSignalHelper(t *testing.T) {
	if os.Getenv("RL_TUN_ENGINE_HELPER") != "1" {
		t.Skip("engine signal helper only")
	}
	dir := os.Getenv("RL_TUN_ENGINE_DATA")
	socketPath := os.Getenv("RL_TUN_SOCKET")
	if dir == "" || socketPath == "" {
		t.Fatal("engine helper environment is incomplete")
	}
	paths, err := store.ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	svc, err := control.NewService(ctx, control.Options{
		Store: st, Paths: paths, Tun: &Client{Path: socketPath, Timeout: 20 * time.Second}, LogOutput: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.StartNetwork(ctx); err != nil {
		t.Fatalf("start network: %v", err)
	}
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := svc.StopCoverage(shutdownCtx); err != nil {
		t.Fatalf("stop coverage: %v", err)
	}
	if err := svc.Close(shutdownCtx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func startTunHelperProcess(t *testing.T, cfgPath, socketPath, leasePath, logPath string) (*exec.Cmd, <-chan error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
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
		waitCh <- cmd.Wait()
		_ = logFile.Close()
	}()
	return cmd, waitCh
}

func freeLoopback(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// nonLoopbackIPv4 returns an address on the physical default interface. Using
// it as a test peer makes the egress controller apply SO_BINDTODEVICE, which a
// loopback peer deliberately bypasses.
func nonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	ifaceName, err := egress.DiscoverInterface()
	if err != nil || ifaceName == "" {
		t.Skipf("no physical default interface for the egress check: %v", err)
	}
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		t.Skipf("physical interface %q is unavailable: %v", ifaceName, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		t.Skipf("cannot read addresses of %q: %v", ifaceName, err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 == nil || ip4.IsLoopback() || ip4.IsUnspecified() {
			continue
		}
		return ip4.String()
	}
	t.Skipf("physical interface %q has no non-loopback IPv4 address", ifaceName)
	return ""
}

func dialSOCKS5(socksAddr, target string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", socksAddr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if resp[0] != 5 || resp[1] != 0 {
		_ = conn.Close()
		return nil, fmt.Errorf("socks greeting rejected")
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	ip := net.ParseIP(host)
	port, err := strconv.Atoi(portText)
	if err != nil || ip == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("socks target is not a loopback IP literal")
	}
	req := []byte{5, 1, 0}
	if v4 := ip.To4(); v4 != nil {
		req = append(req, 1)
		req = append(req, v4...)
	} else {
		req = append(req, 4)
		req = append(req, ip.To16()...)
	}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	req = append(req, p[:]...)
	if _, err := conn.Write(req); err != nil {
		_ = conn.Close()
		return nil, err
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if head[1] != 0 {
		_ = conn.Close()
		return nil, fmt.Errorf("socks connect rejected: %d", head[1])
	}
	var skip int
	switch head[3] {
	case 1:
		skip = 4
	case 4:
		skip = 16
	case 3:
		one := make([]byte, 1)
		if _, err := io.ReadFull(conn, one); err != nil {
			_ = conn.Close()
			return nil, err
		}
		skip = int(one[0])
	default:
		_ = conn.Close()
		return nil, fmt.Errorf("socks reply has an unknown address type")
	}
	if _, err := io.ReadFull(conn, make([]byte, skip+2)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}
