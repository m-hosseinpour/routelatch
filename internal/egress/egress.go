// Package egress keeps RouteLatch's own outbound connections outside the
// system-wide TUN. When coverage is active every non-loopback dial made by the
// engine (direct, probe, subscription fetch, resolver and the embedded Xray
// pool dials) is bound to the physical default interface with
// SO_BINDTODEVICE. Loopback is deliberately excluded so the engine can still
// reach its own listeners and the TUN service (docs/contracts.md §10.6).
//
// The controller is registered with the pinned core once per process and only
// acts while coverage is active, so disabling TUN restores ordinary dials
// without restarting a core (R-4).
package egress

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/xtls/xray-core/transport/internet"
	"golang.org/x/sys/unix"
)

// minBindDeviceMajor/minor are the first Linux release where an unprivileged
// process may use SO_BINDTODEVICE (docs/contracts.md §10.6).
const (
	minBindDeviceMajor = 5
	minBindDeviceMinor = 7
)

// Controller owns the TUN-active bypass state. The zero value is not usable;
// use NewController.
type Controller struct {
	active atomic.Bool
	mu     sync.RWMutex
	iface  string
}

// NewController returns an inactive controller.
func NewController() *Controller { return &Controller{} }

// Activate starts binding non-loopback dials to iface. It does not validate the
// interface or the kernel; callers run the TUN feasibility checks first.
func (c *Controller) Activate(iface string) error {
	if strings.TrimSpace(iface) == "" {
		return errors.New("egress: physical interface is empty")
	}
	c.mu.Lock()
	c.iface = iface
	c.mu.Unlock()
	c.active.Store(true)
	return nil
}

// Deactivate stops affecting dials. A dial that has already received a
// descriptor is unaffected; new dials do not bind. This is the "no effect
// after disable" property: the controller is never unregistered because the
// pinned core exposes no unregister API.
func (c *Controller) Deactivate() {
	c.active.Store(false)
}

// Active reports whether coverage bypass is in effect.
func (c *Controller) Active() bool { return c.active.Load() }

// Interface returns the bound interface name, if any.
func (c *Controller) Interface() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.iface
}

// Control is a net.Dialer.Control callback. It binds only when coverage is
// active and the resolved destination is not loopback. A Unix socket or an
// unresolvable address is left alone in the inactive state; while active an
// unparsable network destination fails closed rather than leaking unbound.
func (c *Controller) Control(network, address string, raw syscall.RawConn) error {
	if !c.active.Load() {
		return nil
	}
	if network == "unix" || network == "unixgram" || network == "unixpacket" {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("egress: destination address is not host:port: %w", err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		// The dialer hands Control an already-resolved address. An unparsable
		// value means a custom resolver; refuse instead of dialing unbound.
		return fmt.Errorf("egress: destination is not a numeric address: %w", err)
	}
	if ip.IsLoopback() {
		return nil
	}
	c.mu.RLock()
	iface := c.iface
	c.mu.RUnlock()
	if iface == "" {
		return errors.New("egress: coverage is active without an interface")
	}
	var bindErr error
	if err := raw.Control(func(fd uintptr) {
		bindErr = unix.BindToDevice(int(fd), iface)
	}); err != nil {
		return err
	}
	if bindErr != nil {
		return fmt.Errorf("egress: bind to %s failed: %w", iface, bindErr)
	}
	return nil
}

// Dialer returns a net.Dialer with the bypass control. A zero timeout keeps
// Go's default, matching an ordinary dial.
func (c *Controller) Dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, Control: c.Control}
}

// Default is the process-wide controller. The pinned core can register only
// one controller and cannot unregister it, so one shared, reversible controller
// is the only shape that keeps "register once" and "no effect after disable"
// true at the same time.
var Default = NewController()

var (
	registerOnce sync.Once
	registerErr  error
)

// RegisterXray registers one controller with the pinned core's system dialer
// exactly once per process. A core that uses a custom system dialer refuses
// registration; the caller must treat that as unsupported rather than letting
// pool dials loop through TUN.
func RegisterXray(c *Controller) error {
	if c == nil {
		c = Default
	}
	registerOnce.Do(func() {
		registerErr = internet.RegisterDialerController(c.Control)
	})
	return registerErr
}

// KernelSupported reports whether this kernel permits an unprivileged
// SO_BINDTODEVICE. Older kernels are an unsupported platform, never a silent
// unbound dial.
func KernelSupported() error {
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return fmt.Errorf("egress: read kernel version: %w", err)
	}
	release := strings.TrimRight(string(uts.Release[:]), "\x00")
	major, minor, err := parseKernelRelease(release)
	if err != nil {
		return err
	}
	if major < minBindDeviceMajor || (major == minBindDeviceMajor && minor < minBindDeviceMinor) {
		return fmt.Errorf("egress: kernel %s is older than %d.%d", release, minBindDeviceMajor, minBindDeviceMinor)
	}
	return nil
}

func parseKernelRelease(release string) (int, int, error) {
	base := release
	if i := strings.IndexAny(base, "-+"); i >= 0 {
		base = base[:i]
	}
	parts := strings.Split(base, ".")
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("egress: kernel release %q is not major.minor", release)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("egress: kernel release %q has no major", release)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("egress: kernel release %q has no minor", release)
	}
	return major, minor, nil
}

// DiscoverInterface finds the physical default-route interface. It ignores the
// TUN itself and loopback. The TUN uses half routes rather than replacing the
// system default, so the physical default route is still present while
// coverage is active.
func DiscoverInterface() (string, error) {
	return discoverInterface("/proc/net/route", "/proc/net/ipv6_route")
}

func discoverInterface(v4Path, v6Path string) (string, error) {
	if iface, err := defaultInterfaceV4(v4Path); err == nil && iface != "" {
		return iface, nil
	}
	if iface, err := defaultInterfaceV6(v6Path); err == nil && iface != "" {
		return iface, nil
	}
	return "", errors.New("egress: no physical default-route interface was found")
}

const tunInterfaceName = "rl-tun0"

func defaultInterfaceV4(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	type candidate struct {
		name   string
		metric int
	}
	var candidates []candidate
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 {
			continue
		}
		name := fields[0]
		if name == "lo" || name == tunInterfaceName {
			continue
		}
		if fields[1] != "00000000" || fields[7] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil || flags&0x1 == 0 {
			continue
		}
		metric := 0
		if m, err := strconv.Atoi(fields[6]); err == nil {
			metric = m
		}
		candidates = append(candidates, candidate{name: name, metric: metric})
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].metric < candidates[j].metric })
	return candidates[0].name, nil
}

func defaultInterfaceV6(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	type candidate struct {
		name   string
		metric int
	}
	var candidates []candidate
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// dest(32) prefix(2) src(32) srcPrefix(2) nextHop(32) metric refcnt
		// use flags iface
		if len(fields) < 10 {
			continue
		}
		name := fields[len(fields)-1]
		if name == "lo" || name == tunInterfaceName {
			continue
		}
		if strings.Trim(fields[0], "0") != "" || strings.Trim(fields[1], "0") != "" {
			continue
		}
		flags, err := strconv.ParseUint(fields[8], 16, 32)
		if err != nil || flags&0x1 == 0 {
			continue
		}
		metric := 0
		if m, err := strconv.ParseUint(fields[5], 16, 32); err == nil {
			metric = int(m)
		}
		candidates = append(candidates, candidate{name: name, metric: metric})
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].metric < candidates[j].metric })
	return candidates[0].name, nil
}
