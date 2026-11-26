// Command egressprobe is a disposable R16-A harness. It models the two engine
// egress paths that must stay outside the TUN while TUN is active:
//
//   - direct: a plain Go dial with SO_BINDTODEVICE (direct/probe/fetch style)
//   - xray-bound: an embedded pinned-Xray core with a dialer controller that
//     binds non-loopback dials to the physical interface (pool style)
//   - xray-loop: the same embedded core with no controller, a negative control
//     that lets the fixture observe a TUN loop
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	xcore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all"
	"github.com/xtls/xray-core/transport/internet"
	"golang.org/x/sys/unix"
)

type result struct {
	Mode      string `json:"mode"`
	Interface string `json:"interface"`
	Target    string `json:"target"`
	Echo      bool   `json:"echo"`
	Error     string `json:"error,omitempty"`
	Local     string `json:"local,omitempty"`
	DialMS    int64  `json:"dial_ms"`
	EchoMS    int64  `json:"echo_ms"`
	Nonce     string `json:"nonce"`
}

func main() {
	iface := flag.String("interface", "eth0", "physical interface to bind")
	target := flag.String("target", "172.31.77.20:7102", "TCP target peer:port")
	mode := flag.String("mode", "direct", "direct | xray-bound | xray-loop")
	timeout := flag.Duration("timeout", 6*time.Second, "overall timeout")
	flag.Parse()

	res := result{Mode: *mode, Interface: *iface, Target: *target, Nonce: randomNonce()}
	if err := run(&res, *iface, *target, *mode, *timeout); err != nil {
		res.Echo = false
		res.Error = err.Error()
	}
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(res)
	if !res.Echo {
		os.Exit(1)
	}
}

func run(res *result, iface, target, mode string, timeout time.Duration) error {
	switch mode {
	case "direct":
		return runDirect(res, iface, target, timeout)
	case "xray-bound":
		return runXray(res, iface, target, timeout, true)
	case "xray-loop":
		return runXray(res, iface, target, timeout, false)
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
}

func bindControl(iface string) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var bindErr error
		err := c.Control(func(fd uintptr) {
			bindErr = unix.BindToDevice(int(fd), iface)
		})
		if err != nil {
			return err
		}
		return bindErr
	}
}

func runDirect(res *result, iface, target string, timeout time.Duration) error {
	d := net.Dialer{Timeout: timeout, Control: bindControl(iface)}
	start := time.Now()
	conn, err := d.Dial("tcp", target)
	res.DialMS = time.Since(start).Milliseconds()
	if err != nil {
		return err
	}
	defer conn.Close()
	res.Local = conn.LocalAddr().String()
	return exchange(res, conn, timeout)
}

func runXray(res *result, iface, target string, timeout time.Duration, bind bool) error {
	if bind {
		// R16 contract: the pool dial bypass is registered once at core start.
		if err := internet.RegisterDialerController(bindControl(iface)); err != nil {
			return err
		}
	}
	cfg := `{"log":{"logLevel":"none","access":"none"},"outbounds":[{"tag":"freedom","protocol":"freedom","settings":{}}]}`
	cc, err := serial.LoadJSONConfig(strings.NewReader(cfg))
	if err != nil {
		return err
	}
	inst, err := xcore.New(cc)
	if err != nil {
		return err
	}
	if err := inst.Start(); err != nil {
		return err
	}
	defer inst.Close()

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return err
	}
	start := time.Now()
	conn, err := xcore.Dial(context.Background(), inst, xnet.Destination{
		Network: xnet.Network_TCP,
		Address: xnet.ParseAddress(host),
		Port:    xnet.Port(port),
	})
	res.DialMS = time.Since(start).Milliseconds()
	if err != nil {
		return err
	}
	defer conn.Close()
	return exchange(res, conn, timeout)
}

func exchange(res *result, conn net.Conn, timeout time.Duration) error {
	start := time.Now()
	if _, err := conn.Write([]byte("RLSPIKE:" + res.Nonce)); err != nil {
		return err
	}
	type readResult struct {
		b   []byte
		err error
	}
	ch := make(chan readResult, 1)
	go func() {
		b := make([]byte, 256)
		n, err := conn.Read(b)
		ch <- readResult{b: b[:n], err: err}
	}()
	select {
	case r := <-ch:
		res.EchoMS = time.Since(start).Milliseconds()
		if r.err != nil {
			return r.err
		}
		want := "ECHO:RLSPIKE:" + res.Nonce
		if string(r.b) != want {
			return fmt.Errorf("unexpected echo %q", string(r.b))
		}
		res.Echo = true
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("echo timeout after %s", timeout)
	}
}

func randomNonce() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b)
}

var _ = json.Marshal
