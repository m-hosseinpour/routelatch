package tun

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"
)

// systemNetwork inspects and removes the link the pinned core created. It uses
// only fixed arguments and never a shell; the interface name is the package
// constant, so no request or config value can reach a command line.
type systemNetwork struct{}

// NewSystemNetwork returns the production network helper.
func NewSystemNetwork() Network { return systemNetwork{} }

func (systemNetwork) LinkPresent(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

func (systemNetwork) SetDown(ctx context.Context, name string) error {
	if !(systemNetwork{}).LinkPresent(name) {
		return nil
	}
	if _, err := runIP(ctx, "", "link", "set", name, "down"); err != nil {
		// A concurrent teardown is not an error: the link is already gone.
		if !(systemNetwork{}).LinkPresent(name) {
			return nil
		}
		return err
	}
	return nil
}

func (systemNetwork) CheckUp(name string, gateways, routes []string) error {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return fmt.Errorf("the TUN interface is missing")
	}
	if iface.Flags&net.FlagUp == 0 {
		return fmt.Errorf("the TUN interface is down")
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return fmt.Errorf("the TUN interface addresses are unreadable")
	}
	have := make(map[netip.Prefix]bool, len(addrs))
	for _, addr := range addrs {
		prefix, err := netip.ParsePrefix(addr.String())
		if err != nil {
			continue
		}
		have[prefix] = true
	}
	for _, want := range gateways {
		prefix, err := netip.ParsePrefix(want)
		if err != nil {
			return fmt.Errorf("a fixed gateway is invalid")
		}
		if !have[prefix] {
			return fmt.Errorf("a fixed TUN gateway is missing")
		}
	}

	listed, err := linkRoutes(context.Background(), name)
	if err != nil {
		return err
	}
	for _, want := range routes {
		prefix, err := netip.ParsePrefix(want)
		if err != nil {
			return fmt.Errorf("a fixed half route is invalid")
		}
		if !listed[prefix] {
			return fmt.Errorf("a fixed TUN half route is missing")
		}
	}
	return nil
}

func (systemNetwork) Remove(ctx context.Context, name string) error {
	deadline := time.Now().Add(2 * time.Second)
	deleted := false
	for {
		link := (systemNetwork{}).LinkPresent(name)
		routes, routeErr := linkRoutes(ctx, name)
		if routeErr != nil {
			return routeErr
		}
		if !link && len(routes) == 0 {
			return nil
		}
		if link && !deleted {
			// Routes and addresses on this exact link are removed by the
			// kernel with the link. The core normally does this itself; this
			// is the verified fallback, not a second routing policy.
			_, _ = runIP(ctx, "", "link", "del", name)
			deleted = true
		} else if !link && len(routes) > 0 {
			removeStaleRoutes(ctx, name, routes)
		}
		if !time.Now().Before(deadline) {
			if (systemNetwork{}).LinkPresent(name) || len(routes) > 0 {
				return fmt.Errorf("the TUN link or its routes could not be removed")
			}
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// linkRoutes returns every prefix the kernel still lists on the interface.
func linkRoutes(ctx context.Context, name string) (map[netip.Prefix]bool, error) {
	out := make(map[netip.Prefix]bool)
	for _, family := range []string{"-4", "-6"} {
		text, err := runIP(ctx, family, "route", "show", "dev", name)
		if err != nil {
			// `ip` fails when the device is already gone; the caller checks
			// the link separately. A failure while the link exists is real.
			if !(systemNetwork{}).LinkPresent(name) {
				continue
			}
			return nil, err
		}
		for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if prefix, err := netip.ParsePrefix(fields[0]); err == nil {
				out[prefix] = true
			}
		}
	}
	return out, nil
}

// removeStaleRoutes deletes only the fixed prefixes, and only when they are
// still attached to our interface. No route from a request is ever accepted.
func removeStaleRoutes(ctx context.Context, name string, present map[netip.Prefix]bool) {
	candidates := append([]string(nil), HalfRoutes...)
	for _, gateway := range Gateways {
		if prefix, err := netip.ParsePrefix(gateway); err == nil {
			candidates = append(candidates, prefix.Masked().String())
		}
	}
	for _, cidr := range candidates {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || !present[prefix] {
			continue
		}
		family := "-6"
		if prefix.Addr().Is4() {
			family = "-4"
		}
		_, _ = runIP(ctx, family, "route", "del", prefix.String(), "dev", name)
	}
}

func runIP(ctx context.Context, family string, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	full := make([]string, 0, len(args)+1)
	if family != "" {
		full = append(full, family)
	}
	full = append(full, args...)
	ip, err := ipBinary()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(runCtx, ip, full...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("ip %s failed", strings.Join(full, " "))
	}
	return string(out), nil
}

// ipBinary resolves iproute2 without relying on a unit's PATH being complete.
func ipBinary() (string, error) {
	if path, err := exec.LookPath("ip"); err == nil {
		return path, nil
	}
	for _, path := range []string{"/usr/sbin/ip", "/sbin/ip", "/usr/bin/ip", "/bin/ip"} {
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("iproute2 is not available")
}
