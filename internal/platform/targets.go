package platform

import (
	"net"
	"strconv"
	"strings"

	"routelatch/internal/contract"
)

// NoProxy is the fixed NO_PROXY list used by the launcher and the desktop
// adapters. It contains only destination classes that RouteLatch's routing
// matrix always sends direct (Local and LAN, docs/contracts.md §1.4), so it can
// never bypass an approved public destination. An inherited NO_PROXY is
// discarded, not merged, for exactly that reason. 127.0.0.1 is listed literally
// as well as by CIDR because some clients only do exact/suffix matching.
const NoProxy = "localhost,127.0.0.1,.localhost,127.0.0.0/8,::1,169.254.0.0/16,fe80::/10,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,100.64.0.0/10,fc00::/7"

// Targets are the two loopback listeners an application or desktop setting is
// pointed at. Both must be literal loopback IP addresses with a real port.
type Targets struct {
	HTTPListen  string // host:port, e.g. 127.0.0.1:8317
	SOCKSListen string // host:port, e.g. 127.0.0.1:8318
}

type listener struct {
	host string // canonical literal, without brackets
	port string
}

// Validate rejects a non-loopback or malformed listener. A desktop setting or
// child environment must never advertise an address other machines can reach
// (D14, docs/contracts.md §4.2).
func (t Targets) Validate() error {
	if _, err := parseListener("platform.targets.http", t.HTTPListen); err != nil {
		return err
	}
	if _, err := parseListener("platform.targets.socks", t.SOCKSListen); err != nil {
		return err
	}
	return nil
}

// HTTP and SOCKS expose the canonical listener pieces.
func (t Targets) http() (listener, error) {
	return parseListener("platform.targets.http", t.HTTPListen)
}
func (t Targets) socks() (listener, error) {
	return parseListener("platform.targets.socks", t.SOCKSListen)
}

func parseListener(op, raw string) (listener, error) {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return listener{}, localFailure(contract.Unsupported, op,
			"listener must be a loopback host:port address (for example 127.0.0.1:8317)")
	}
	if strings.Contains(host, "%") {
		return listener{}, localFailure(contract.Unsupported, op, "listener must not use an IPv6 zone")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return listener{}, localFailure(contract.Unsupported, op,
			"listener must be loopback (127.0.0.1 or ::1); refusing to advertise a non-loopback proxy")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return listener{}, localFailure(contract.Unsupported, op,
			"listener port must be a canonical decimal between 1 and 65535")
	}
	return listener{host: ip.String(), port: strconv.Itoa(n)}, nil
}

func (l listener) hostPort() string { return net.JoinHostPort(l.host, l.port) }
