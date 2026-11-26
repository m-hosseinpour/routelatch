// Command tunservice is a disposable R16-A feasibility harness, not product
// code. It runs the pinned Xray core in-process with the TUN inbound, inside a
// hardened systemd system unit that gives only CAP_NET_ADMIN to a non-root
// user. It also serves the fixed control socket and keeps a restart lease.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all"
	_ "github.com/xtls/xray-core/proxy/tun"
	"golang.org/x/sys/unix"
)

type config struct {
	AllowedUID     int    `json:"allowed_uid"`
	SocketPath     string `json:"socket_path"`
	LeasePath      string `json:"lease_path"`
	XrayConfigPath string `json:"xray_config_path"`
	ProbeInterface string `json:"probe_interface"`
	PhysicalIface  string `json:"physical_interface"`
	Interface      string `json:"interface"`
}

type lease struct {
	Desired   string `json:"desired"`
	BootID    string `json:"boot_id"`
	PID       int    `json:"pid"`
	Interface string `json:"interface"`
	Since     string `json:"since"`
}

type response struct {
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
	State        string `json:"state"`
	Interface    string `json:"interface"`
	UID          int    `json:"uid"`
	EUID         int    `json:"euid"`
	CapNetAdmin  bool   `json:"cap_net_admin"`
	CapEff       string `json:"cap_eff"`
	CoreRunning  bool   `json:"core_running"`
	LinkPresent  bool   `json:"link_present"`
	Routes       int    `json:"routes"`
	LeaseDesired string `json:"lease_desired"`
	CleanupMS    int64  `json:"cleanup_ms"`
	LastError    string `json:"last_error,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
}

type service struct {
	cfg config
	log *log.Logger

	mu              sync.Mutex
	inst            *core.Instance
	state           string
	lastErr         string
	startedAt       time.Time
	cleanupMS       int64
	coreEverStarted bool
}

func main() {
	cfgPath := flag.String("config", "/etc/routelatch-tun-spike/service.json", "service config")
	flag.Parse()

	logger := log.New(os.Stderr, "routelatch-tun-spike: ", log.LstdFlags|log.LUTC)
	s := &service{cfg: loadConfig(*cfgPath, logger), log: logger, state: "off"}

	if err := s.verifyRootOwned(); err != nil {
		logger.Printf("refusing to run: %v", err)
		os.Exit(1)
	}
	uid := os.Getuid()
	euid := os.Geteuid()
	capEff, capBnd, err := readCaps()
	if err != nil {
		logger.Printf("read capabilities: %v", err)
		os.Exit(1)
	}
	capNetAdmin := capEff&(1<<12) != 0
	logger.Printf("identity uid=%d euid=%d capeff=%#x capbnd=%#x cap_net_admin=%v", uid, euid, capEff, capBnd, capNetAdmin)
	if euid == 0 {
		logger.Printf("refusing to run as root; the gate requires a non-root service")
		os.Exit(1)
	}
	if !capNetAdmin {
		logger.Printf("CAP_NET_ADMIN missing from the service; capability gate failed")
		os.Exit(1)
	}

	// Item 1: the service itself creates a TUN and adds a route before handing
	// the real interface to the pinned core.
	if err := s.probeTunAndRoute(); err != nil {
		logger.Printf("capability probe failed: %v", err)
		os.Exit(1)
	}

	ln, err := s.listen()
	if err != nil {
		logger.Printf("control socket: %v", err)
		os.Exit(1)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(s.cfg.SocketPath)
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		s.log.Printf("signal received; stopping core and keeping the lease for restart")
		s.mu.Lock()
		// A semantic stop is not an explicit user "off": the lease stays
		// desired=on so systemctl restart rebuilds in the same boot.
		s.stopCoreLocked(false)
		s.mu.Unlock()
		_ = ln.Close()
		_ = os.Remove(s.cfg.SocketPath)
		os.Exit(0)
	}()

	// Rebuild on service restart in the same boot if a lease says TUN was on.
	if l, ok := s.readLease(); ok && l.Desired == "on" && l.BootID == bootID() {
		s.log.Printf("lease requests rebuild (pid=%d since=%s)", l.PID, l.Since)
		s.mu.Lock()
		if err := s.startCoreLocked(); err != nil {
			s.log.Printf("lease rebuild failed: %v", err)
		}
		s.mu.Unlock()
	}

	s.log.Printf("control socket ready at %s (mode 0600, allowed uid %d)", s.cfg.SocketPath, s.cfg.AllowedUID)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Printf("accept: %v", err)
			continue
		}
		go s.handle(conn)
	}
}

func loadConfig(path string, logger *log.Logger) config {
	b, err := os.ReadFile(path)
	if err != nil {
		logger.Printf("read config: %v", err)
		os.Exit(1)
	}
	var c config
	if err := json.Unmarshal(b, &c); err != nil {
		logger.Printf("decode config: %v", err)
		os.Exit(1)
	}
	if c.SocketPath == "" || c.LeasePath == "" || c.XrayConfigPath == "" || c.Interface == "" || c.PhysicalIface == "" || c.ProbeInterface == "" {
		logger.Printf("config is missing a required field")
		os.Exit(1)
	}
	return c
}

func (s *service) verifyRootOwned() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	st, err := os.Stat(exe)
	if err != nil {
		return err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("no stat owner")
	}
	if sys.Uid != 0 {
		return fmt.Errorf("binary %s is not root-owned (uid %d)", exe, sys.Uid)
	}
	if st.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("binary %s is group/world writable (%o)", exe, st.Mode().Perm())
	}
	s.log.Printf("binary %s is root-owned mode %04o (uid=%d)", exe, st.Mode().Perm(), sys.Uid)
	return nil
}

func readCaps() (eff, bnd uint64, err error) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, 0, err
	}
	var haveEff, haveBnd bool
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		switch f[0] {
		case "CapEff:":
			eff, err = strconv.ParseUint(f[1], 16, 64)
			if err != nil {
				return 0, 0, err
			}
			haveEff = true
		case "CapBnd:":
			bnd, err = strconv.ParseUint(f[1], 16, 64)
			if err != nil {
				return 0, 0, err
			}
			haveBnd = true
		}
	}
	if !haveEff || !haveBnd {
		return 0, 0, errors.New("CapEff/CapBnd not found")
	}
	return eff, bnd, nil
}

func (s *service) probeTunAndRoute() error {
	run := func(args ...string) (string, error) {
		out, err := exec.Command("ip", args...).CombinedOutput()
		txt := strings.TrimSpace(string(out))
		if err != nil {
			return txt, fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, txt)
		}
		s.log.Printf("probe: ip %s -> %s", strings.Join(args, " "), txt)
		return txt, nil
	}
	// Remove a leftover from an earlier failed run; ignore "not found".
	_, _ = run("link", "del", s.cfg.ProbeInterface)
	if _, err := run("tuntap", "add", "dev", s.cfg.ProbeInterface, "mode", "tun"); err != nil {
		return err
	}
	defer func() { _, _ = run("link", "del", s.cfg.ProbeInterface) }()
	if _, err := run("link", "set", s.cfg.ProbeInterface, "up"); err != nil {
		return err
	}
	if _, err := run("addr", "add", "192.0.2.1/32", "dev", s.cfg.ProbeInterface); err != nil {
		return err
	}
	if _, err := run("route", "add", "192.0.2.2/32", "dev", s.cfg.ProbeInterface); err != nil {
		return err
	}
	out, err := run("route", "get", "192.0.2.2")
	if err != nil {
		return err
	}
	if !strings.Contains(out, "dev "+s.cfg.ProbeInterface) {
		return fmt.Errorf("route probe did not select %s: %s", s.cfg.ProbeInterface, out)
	}
	if _, err := run("link", "del", s.cfg.ProbeInterface); err != nil {
		return err
	}
	s.log.Printf("capability probe passed: non-root service created a TUN, addressed it and added a route")
	return nil
}

func (s *service) listen() (*net.UnixListener, error) {
	if err := os.MkdirAll(filepath.Dir(s.cfg.SocketPath), 0o750); err != nil {
		return nil, err
	}
	// A SIGKILL leaves the socket inode behind; remove only our own path.
	_ = os.Remove(s.cfg.SocketPath)
	addr, err := net.ResolveUnixAddr("unix", s.cfg.SocketPath)
	if err != nil {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(s.cfg.SocketPath, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	ln.SetUnlinkOnClose(false)
	return ln, nil
}

func peerUID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return -1, err
	}
	var uid = -1
	var credErr error
	err = raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			credErr = err
			return
		}
		uid = int(cred.Uid)
	})
	if err != nil {
		return -1, err
	}
	return uid, credErr
}

func (s *service) handle(conn net.Conn) {
	defer conn.Close()
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return
	}
	uid, err := peerUID(uc)
	if err != nil {
		s.log.Printf("control: peer credentials unavailable: %v", err)
		return
	}
	if uid != s.cfg.AllowedUID {
		s.log.Printf("control: rejected peer uid %d (allowed %d)", uid, s.cfg.AllowedUID)
		_ = json.NewEncoder(conn).Encode(response{OK: false, Error: "peer_uid_rejected", UID: uid})
		return
	}

	var req struct {
		Cmd string `json:"cmd"`
	}
	dec := json.NewDecoder(io.LimitReader(conn, 4096))
	if err := dec.Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(response{OK: false, Error: "bad_request"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	resp := s.statusLocked()
	switch req.Cmd {
	case "status":
		resp.OK = true
	case "enable":
		if s.inst != nil {
			resp.OK = true
			break
		}
		if s.coreEverStarted {
			// R-4: a core is not restarted inside the same process. The product
			// uses socket activation; this harness requires a service restart.
			resp.OK = false
			resp.Error = "state_locked: core was already started in this process; restart the service"
			break
		}
		err := s.startCoreLocked()
		resp = s.statusLocked()
		if err != nil {
			resp.OK = false
			resp.Error = "core_start_failed"
			s.log.Printf("enable failed: %v", err)
		} else {
			resp.OK = true
		}
	case "disable":
		s.stopCoreLocked(true)
		resp = s.statusLocked()
		resp.OK = true
	case "":
		resp.OK = false
		resp.Error = "bad_request"
	default:
		resp.OK = false
		resp.Error = "unknown_command"
	}
	_ = json.NewEncoder(conn).Encode(resp)
}

func (s *service) statusLocked() response {
	link := s.linkPresent()
	routes := 0
	if link {
		for _, args := range [][]string{
			{"route", "show", "dev", s.cfg.Interface},
			{"-6", "route", "show", "dev", s.cfg.Interface},
		} {
			if out, err := exec.Command("ip", args...).Output(); err == nil {
				for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
					if strings.TrimSpace(line) != "" {
						routes++
					}
				}
			}
		}
	}
	eff, _, _ := readCaps()
	l, _ := s.readLease()
	return response{
		State:        s.state,
		Interface:    s.cfg.Interface,
		UID:          os.Getuid(),
		EUID:         os.Geteuid(),
		CapNetAdmin:  eff&(1<<12) != 0,
		CapEff:       fmt.Sprintf("%#x", eff),
		CoreRunning:  s.inst != nil,
		LinkPresent:  link,
		Routes:       routes,
		LeaseDesired: l.Desired,
		CleanupMS:    s.cleanupMS,
		LastError:    s.lastErr,
		StartedAt:    formatTime(s.startedAt),
	}
}

func (s *service) linkPresent() bool {
	_, err := net.InterfaceByName(s.cfg.Interface)
	return err == nil
}

func (s *service) startCoreLocked() error {
	if s.inst != nil {
		return nil
	}
	s.state = "starting"
	s.startedAt = time.Now().UTC()
	b, err := os.ReadFile(s.cfg.XrayConfigPath)
	if err != nil {
		s.lastErr = "read_xray_config"
		s.state = "error"
		return err
	}
	cc, err := serial.LoadJSONConfig(bytes.NewReader(b))
	if err != nil {
		s.lastErr = "decode_xray_config"
		s.state = "error"
		return err
	}
	inst, err := core.New(cc)
	if err != nil {
		s.lastErr = "new_core"
		s.state = "error"
		return err
	}
	if err := inst.Start(); err != nil {
		_ = inst.Close()
		s.lastErr = "start_core"
		s.state = "error"
		return err
	}
	s.inst = inst
	s.coreEverStarted = true
	s.state = "on"
	s.lastErr = ""
	s.writeLease("on")
	s.log.Printf("core started version=%s; interface=%s present=%v", core.Version(), s.cfg.Interface, s.linkPresent())
	return nil
}

func (s *service) stopCoreLocked(explicitOff bool) {
	if s.inst == nil {
		s.state = "off"
		if explicitOff {
			s.writeLease("off")
		}
		return
	}
	s.state = "stopping"
	start := time.Now()
	err := s.inst.Close()
	s.inst = nil
	s.cleanupMS = time.Since(start).Milliseconds()
	// Xray removes routes/addresses in Close; poll for the link disappearing.
	deadline := time.Now().Add(2 * time.Second)
	for s.linkPresent() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	s.cleanupMS = time.Since(start).Milliseconds()
	if err != nil {
		s.lastErr = "close_core"
		s.log.Printf("core close returned an error; link present=%v cleanup_ms=%d", s.linkPresent(), s.cleanupMS)
	} else {
		s.lastErr = ""
		s.log.Printf("core stopped; link present=%v cleanup_ms=%d", s.linkPresent(), s.cleanupMS)
	}
	s.state = "off"
	if explicitOff {
		s.writeLease("off")
	}
}

func bootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (s *service) readLease() (lease, bool) {
	b, err := os.ReadFile(s.cfg.LeasePath)
	if err != nil {
		return lease{}, false
	}
	var l lease
	if err := json.Unmarshal(b, &l); err != nil {
		return lease{}, false
	}
	return l, true
}

func (s *service) writeLease(desired string) {
	l := lease{Desired: desired, BootID: bootID(), PID: os.Getpid(), Interface: s.cfg.Interface, Since: time.Now().UTC().Format(time.RFC3339Nano)}
	b, err := json.Marshal(l)
	if err != nil {
		return
	}
	tmp := s.cfg.LeasePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		s.log.Printf("lease write: %v", err)
		return
	}
	if err := os.Rename(tmp, s.cfg.LeasePath); err != nil {
		_ = os.Remove(tmp)
		s.log.Printf("lease rename: %v", err)
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
