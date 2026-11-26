// Package tun implements RouteLatch's separate TUN coverage service. It owns
// only the network plumbing: it creates the rl-tun0 interface, hands TCP to the
// engine's loopback SOCKS listener, sends plain DNS directly and blocks non-DNS
// UDP. It has no rule, pool, exit-selection or destination policy; that stays in
// the engine (docs/architecture.md §9, docs/contracts.md §10).
//
// The service is started by a root-owned system unit with only CAP_NET_ADMIN,
// reads one root-owned config file, and exposes a 0600 Unix socket that accepts
// exactly enable|disable|status. A fresh core is started for each enable and the
// process exits after an explicit disable, so no proxy core is restarted inside
// one process (R-4); a failed enable is torn down and may be retried with a new
// core. The restart lease in /run is used only to rebuild coverage after a
// service restart in the same boot.
package tun

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

// Fixed coverage constants. Both values and the interface name were verified by
// R16-A and are pinned by docs/contracts.md §10.5; no user input can change
// them.
const (
	InterfaceName = "rl-tun0"
	MTU           = 1500

	IPv4Gateway = "198.18.0.1/15"
	IPv6Gateway = "fc00::1/64"

	// ControlSocketPath and LeasePath live on /run, which is cleared on boot.
	// The systemd socket unit uses the same path; setup creates the directory
	// with the allowed uid (docs/contracts.md §10.4).
	ControlSocketPath = "/run/routelatch-tun/control.sock"
	LeasePath         = "/run/routelatch-tun/lease.json"

	// DefaultConfigPath is the root-owned service config written by
	// `routelatch tun setup` (R16-G). The unit may pass an explicit --config;
	// this is only the fixed default.
	DefaultConfigPath = "/etc/routelatch/tun.json"

	inboundTag = "rl-tun-in"
	socksTag   = "rl-tun-socks"
	dnsTag     = "rl-tun-dns"
	blockTag   = "rl-block"
)

// Gateways and HalfRoutes are the fixed dual-stack addresses and the two halves
// per family. The system default route is left alone so LAN/DNS routes keep
// working (contracts §10.5).
var (
	Gateways   = []string{IPv4Gateway, IPv6Gateway}
	HalfRoutes = []string{
		"0.0.0.0/1",
		"128.0.0.0/1",
		"::/1",
		"8000::/1",
	}
)

// ServiceConfig is the whole root-owned service configuration. Every other
// value is compiled in: there is no interface, route, domain, log-level or
// policy input (contracts §10.4–§10.5).
type ServiceConfig struct {
	// AllowedUID is the engine user that may call the control socket. The
	// service compares it with SO_PEERCRED; root and every other uid are
	// rejected.
	AllowedUID int `json:"allowed_uid"`
	// SocksAddress is the loopback SOCKS5 listener of the engine instance this
	// service hands TCP to. It must be an IP literal on 127.0.0.0/8 or ::1.
	SocksAddress string `json:"socks_address"`
}

// LoadServiceConfig reads the root-owned JSON config. Unknown fields are
// refused: a config file may not smuggle in an interface, route or policy
// setting that the service does not own.
func LoadServiceConfig(path string) (ServiceConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return ServiceConfig{}, fmt.Errorf("read the TUN service config: %w", err)
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 64*1024))
	dec.DisallowUnknownFields()
	var cfg ServiceConfig
	if err := dec.Decode(&cfg); err != nil {
		return ServiceConfig{}, fmt.Errorf("decode the TUN service config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return ServiceConfig{}, err
	}
	return cfg, nil
}

func (c ServiceConfig) Validate() error {
	if c.AllowedUID < 0 {
		return fmt.Errorf("allowed_uid must not be negative")
	}
	if _, _, err := ParseSocksAddress(c.SocksAddress); err != nil {
		return err
	}
	return nil
}

// ParseSocksAddress accepts only a loopback IP literal and a real port. A
// hostname would introduce a resolver step and a non-loopback host would let
// the fixed routing point somewhere the contract does not allow.
func ParseSocksAddress(address string) (string, uint16, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, fmt.Errorf("socks_address must be a loopback host:port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", 0, fmt.Errorf("socks_address must be a loopback IP literal")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return "", 0, fmt.Errorf("socks_address must have a valid port")
	}
	return ip.String(), uint16(port), nil
}

// safeText keeps fixed status detail free of control characters before it is
// copied into a contract.TunStatus.
func safeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
