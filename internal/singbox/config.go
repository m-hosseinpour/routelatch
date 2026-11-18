package singbox

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"routelatch/internal/contract"
)

const (
	// BlockTag is the fail-closed final outbound: anything that does not match
	// an explicit inbound-to-exit rule is dropped, never sent direct.
	BlockTag         = "rl-block"
	inboundTagPrefix = "rl-in-"

	// DefaultUpMbps and DefaultDownMbps are applied when a record carries no
	// bandwidth. They are RouteLatch's own policy, never read from a URI.
	DefaultUpMbps   uint32 = 100
	DefaultDownMbps uint32 = 200

	maxExits       = 256
	maxMbps        = 100000
	maxTokenLength = 128
)

// Config is the whole generated sing-box document. The absence of a dns or
// route.resolve section is intentional; the route block only pins one inbound
// to one hysteria2 outbound and falls through to a block outbound.
type Config struct {
	Log       LogConfig   `json:"log"`
	Inbounds  []Inbound   `json:"inbounds"`
	Outbounds []Outbound  `json:"outbounds"`
	Route     RouteConfig `json:"route"`
}

type LogConfig struct {
	Disabled bool `json:"disabled"`
}

type Inbound struct {
	Type           string        `json:"type"`
	Tag            string        `json:"tag"`
	Listen         string        `json:"listen"`
	ListenPort     uint16        `json:"listen_port"`
	Users          []InboundUser `json:"users"`
	SetSystemProxy bool          `json:"set_system_proxy"`
}

type InboundUser struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Outbound carries only the fields RouteLatch owns. There is no transport,
// network, realm, port-hopping, detour or TLS-file field: an untrusted value
// for any of those was refused at import.
type Outbound struct {
	Type       string      `json:"type"`
	Tag        string      `json:"tag"`
	Server     string      `json:"server,omitempty"`
	ServerPort uint16      `json:"server_port,omitempty"`
	Password   string      `json:"password,omitempty"`
	TLS        *TLSConfig  `json:"tls,omitempty"`
	UpMbps     uint32      `json:"up_mbps,omitempty"`
	DownMbps   uint32      `json:"down_mbps,omitempty"`
	Obfs       *ObfsConfig `json:"obfs,omitempty"`
}

type TLSConfig struct {
	Enabled    bool   `json:"enabled"`
	ServerName string `json:"server_name"`
	Insecure   bool   `json:"insecure"`
}

type ObfsConfig struct {
	Type     string `json:"type"`
	Password string `json:"password"`
}

type RouteConfig struct {
	Rules []RouteRule `json:"rules"`
	Final string      `json:"final"`
}

type RouteRule struct {
	Inbound  []string `json:"inbound"`
	Action   string   `json:"action"`
	Outbound string   `json:"outbound"`
}

// Generated bundles the document with the loopback endpoints it created, so
// the runtime can publish exactly one handoff per explicit exit tag.
type Generated struct {
	Raw      []byte
	Config   Config
	Handoffs map[string]contract.SidecarHandoff
}

// Generate builds a fresh config and a fresh set of credentials for every
// exit. It validates every field at the trust boundary before construction;
// a rejected exit fails the whole generation, never a silent downgrade.
func Generate(exits []contract.SidecarExit) (Generated, error) {
	fail := func(message string, cause error) error {
		return &contract.Failure{
			Kind:    contract.ImportInvalid,
			Path:    contract.PathLocal,
			Op:      "singbox.config",
			Message: message,
			Err:     cause,
		}
	}
	if len(exits) == 0 {
		return Generated{}, fail("no hysteria2 exits to configure", nil)
	}
	if len(exits) > maxExits {
		return Generated{}, fail("too many hysteria2 exits", nil)
	}

	cfg := Config{
		Log:       LogConfig{Disabled: true},
		Inbounds:  make([]Inbound, 0, len(exits)),
		Outbounds: make([]Outbound, 0, len(exits)+1),
		Route:     RouteConfig{Rules: make([]RouteRule, 0, len(exits)), Final: BlockTag},
	}
	handoffs := make(map[string]contract.SidecarHandoff, len(exits))
	seen := make(map[string]bool, len(exits))

	for _, exit := range exits {
		if err := validateExit(exit); err != nil {
			return Generated{}, fail("a hysteria2 exit is not usable", err)
		}
		if seen[exit.Tag] {
			return Generated{}, fail("duplicate hysteria2 exit tag", nil)
		}
		seen[exit.Tag] = true

		port, err := loopbackPort()
		if err != nil {
			return Generated{}, fail("a loopback endpoint could not be reserved", err)
		}
		user, err := randomToken()
		if err != nil {
			return Generated{}, fail("handoff credentials could not be generated", err)
		}
		password, err := randomToken()
		if err != nil {
			return Generated{}, fail("handoff credentials could not be generated", err)
		}

		inboundTag := inboundTagPrefix + exit.Tag
		cfg.Inbounds = append(cfg.Inbounds, Inbound{
			Type:       "mixed",
			Tag:        inboundTag,
			Listen:     "127.0.0.1",
			ListenPort: port,
			Users:      []InboundUser{{Username: user, Password: password}},
			// Never let the sidecar touch the machine's proxy settings.
			SetSystemProxy: false,
		})
		up, down := exit.UpMbps, exit.DownMbps
		if up == 0 {
			up = DefaultUpMbps
		}
		if down == 0 {
			down = DefaultDownMbps
		}
		out := Outbound{
			Type:       "hysteria2",
			Tag:        exit.Tag,
			Server:     exit.Server,
			ServerPort: exit.Port,
			Password:   exit.Password,
			TLS: &TLSConfig{
				Enabled:    true,
				ServerName: exit.ServerName,
				Insecure:   false, // verification is never disabled
			},
			UpMbps:   up,
			DownMbps: down,
		}
		if exit.Obfs != "" {
			out.Obfs = &ObfsConfig{Type: exit.Obfs, Password: exit.ObfsPassword}
		}
		cfg.Outbounds = append(cfg.Outbounds, out)
		cfg.Route.Rules = append(cfg.Route.Rules, RouteRule{
			Inbound:  []string{inboundTag},
			Action:   "route",
			Outbound: exit.Tag,
		})
		handoffs[exit.Tag] = contract.SidecarHandoff{
			Address:  fmt.Sprintf("127.0.0.1:%d", port),
			Username: user,
			Password: password,
		}
	}
	cfg.Outbounds = append(cfg.Outbounds, Outbound{Type: "block", Tag: BlockTag})

	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return Generated{}, fail("the generated configuration could not be encoded", err)
	}
	return Generated{Raw: raw, Config: cfg, Handoffs: handoffs}, nil
}

func validateExit(exit contract.SidecarExit) error {
	bad := func() error { return fmt.Errorf("exit %q failed validation", exit.Tag) }
	if exit.Tag == "" || len(exit.Tag) > maxTokenLength || !plainToken(exit.Tag) {
		return bad()
	}
	if exit.Tag == BlockTag || strings.HasPrefix(exit.Tag, inboundTagPrefix) {
		return bad()
	}
	if exit.Server == "" || len(exit.Server) > 253 || strings.ContainsAny(exit.Server, " \t\r\n\x00/@?#") {
		return bad()
	}
	if exit.Port == 0 {
		return bad()
	}
	if exit.Password == "" || len(exit.Password) > maxTokenLength || !plainToken(exit.Password) {
		return bad()
	}
	if exit.ServerName == "" || len(exit.ServerName) > 253 || strings.ContainsAny(exit.ServerName, " \t\r\n\x00/@?#") {
		return bad()
	}
	if exit.UpMbps > maxMbps || exit.DownMbps > maxMbps {
		return bad()
	}
	switch exit.Obfs {
	case "", "salamander", "gecko":
	default:
		return bad()
	}
	if strings.ContainsAny(exit.ObfsPassword, "\r\n\x00") || len(exit.ObfsPassword) > maxTokenLength {
		return bad()
	}
	if exit.Obfs == "salamander" && exit.ObfsPassword == "" {
		return bad()
	}
	return nil
}

func plainToken(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func randomToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// loopbackPort reserves a free loopback port and releases it. The small race
// before the sidecar binds it is bounded by the readiness check in Runtime.Sync.
func loopbackPort() (uint16, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return uint16(ln.Addr().(*net.TCPAddr).Port), nil
}
