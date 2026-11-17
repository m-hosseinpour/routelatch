package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"

	// Register the same apps, proxies, and transports as the official Xray
	// distribution. Without this the config builder cannot construct handlers.
	_ "github.com/xtls/xray-core/main/distro/all"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <gate|transports|teardown|all|exit>\n", os.Args[0])
		os.Exit(2)
	}
	if os.Args[1] == "exit" {
		runExit()
		return
	}
	if os.Args[1] == "all" {
		// Run each section in a fresh process so resource counts are not
		// polluted by listeners and cores left from the previous section.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		for _, sub := range []string{"gate", "transports", "teardown"} {
			cmd := exec.Command(exe, sub)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Stdin = os.Stdin
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "section %s failed: %v\n", sub, err)
				os.Exit(1)
			}
		}
		return
	}

	rep := newReporter(os.Stdout)
	rep.line("RouteLatch T1 spike — embedded Xray core boundary and handshake gate")
	for _, v := range moduleVersions() {
		rep.line("build: %s", v)
	}
	e := newEnv(rep)
	rep.kv("transport matrix", fmt.Sprint(allTransports()))

	switch os.Args[1] {
	case "gate":
		e.runGate()
	case "transports":
		e.runTransports()
	case "teardown":
		e.runTeardown()
	case "all":
		e.runGate()
		e.runTransports()
		e.runTeardown()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

type env struct {
	rep      *reporter
	cert     *certBundle
	uuid     string
	uuid2    string
	password string
}

func newEnv(rep *reporter) *env {
	e := &env{rep: rep, uuid: randomHex(16), uuid2: randomHex(16), password: randomHex(16)}
	cert, err := newCertBundle()
	if err != nil {
		panic(err)
	}
	e.cert = cert
	rep.kv("ephemeral identity", "fresh per run, not written to repo (uuid, password, TLS key)")
	rep.kv("tls material", cert.dir+" (self-signed, leaf-pinned; no verification disabled)")
	return e
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// rig is one isolated client core plus the loopback peers and exits it talks to.
// The exit cores run in child processes; only one core exists in this process.
type rig struct {
	rep         *reporter
	tr          transportKind
	exitPort    int
	badPort     int
	deadPort    int
	refusedPort int
	exit        *exitProcess
	badExit     *exitProcess
	dead        *peer
	client      *client
	front       *forwarder
	socksPeer   *socksPeer
}

func (e *env) newRig(tr transportKind) (*rig, error) {
	r := &rig{rep: e.rep, tr: tr}
	if tr == trSocksPeer {
		sp, err := startSocksPeer()
		if err != nil {
			return nil, err
		}
		r.socksPeer = sp
		e.rep.kv("conforming socks exit", "127.0.0.1:"+itoa(sp.port()))
		r.dead = mustPeer(e.rep, "fake-exit-"+string(tr), peerSilent, 0)
		r.deadPort = r.dead.port()
		r.refusedPort = freePort()
		r.badPort = freePort()
		c, err := startClient(tr, clientOutbounds{
			poolPort: sp.port(), deadPort: r.deadPort, refusedPort: r.refusedPort,
			badPort: r.badPort, uuid: e.uuid, password: e.password, cert: e.cert,
		})
		if err != nil {
			r.stop()
			return nil, fmt.Errorf("start client: %w", err)
		}
		r.client = c
		return r, nil
	}
	r.exitPort = freePort()
	exit, err := startExit(exitSpec{
		Port: r.exitPort, Transport: string(tr), UUID: e.uuid, Password: e.password,
		CertFile: e.cert.certFile, KeyFile: e.cert.keyFile, PinHex: e.cert.pinHex,
		FastPolicy: exitFast(),
	})
	if err != nil {
		return nil, fmt.Errorf("start exit: %w", err)
	}
	r.exit = exit

	r.badPort = freePort()
	badExit, err := startExit(exitSpec{
		Port: r.badPort, Transport: string(tr), UUID: e.uuid2, Password: e.password,
		CertFile: e.cert.certFile, KeyFile: e.cert.keyFile, PinHex: e.cert.pinHex,
		FastPolicy: exitFast(),
	})
	if err != nil {
		r.stop()
		return nil, fmt.Errorf("start bad-auth exit: %w", err)
	}
	r.badExit = badExit

	r.dead = mustPeer(e.rep, "fake-exit-"+string(tr), peerSilent, 0)
	r.refusedPort = freePort()
	r.deadPort = r.dead.port()
	if tr != trSocks {
		r.front = mustForwarder(e.rep, "transport-front", r.exitPort)
	}
	pool := r.exitPort
	if r.front != nil {
		pool = r.front.port()
	}
	c, err := startClient(tr, clientOutbounds{
		poolPort: pool, deadPort: r.deadPort, refusedPort: r.refusedPort,
		badPort: r.badPort, uuid: e.uuid, password: e.password, cert: e.cert,
	})
	if err != nil {
		r.stop()
		return nil, fmt.Errorf("start client: %w", err)
	}
	r.client = c
	return r, nil
}

func (r *rig) stop() {
	if r.client != nil {
		if err := r.client.stop(); err != nil {
			r.rep.kv("client stop error", oneLine(err))
		}
		r.client = nil
	}
	r.stopExits()
}

// stopExits closes the harness peers and exits while leaving the client core
// running, so core-only goroutine residue can be separated from child plumbing.
func (r *rig) stopExits() {
	if r.front != nil {
		r.front.close()
		r.front = nil
	}
	if r.dead != nil {
		r.dead.close()
		r.dead = nil
	}
	if r.socksPeer != nil {
		r.socksPeer.close()
		r.socksPeer = nil
	}
	if r.badExit != nil {
		if err := r.badExit.stop(); err != nil {
			r.rep.kv("bad exit stop error", oneLine(err))
		}
		r.badExit = nil
	}
	if r.exit != nil {
		if err := r.exit.stop(); err != nil {
			r.rep.kv("exit stop error", oneLine(err))
		}
		r.exit = nil
	}
}

func mustPeer(rep *reporter, name string, mode peerMode, delayMS int) *peer {
	p, err := startPeer(name, mode, ms(delayMS))
	if err != nil {
		panic(err)
	}
	rep.kv("peer "+name, fmt.Sprintf("%s on 127.0.0.1:%d", mode, p.port()))
	return p
}
