package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
)

// transportKind is one cell of the transport matrix this spike exercises.
type transportKind string

const (
	trVLESSRaw       transportKind = "vless-raw"
	trVLESSTLS       transportKind = "vless-tls"
	trVLESSWebSocket transportKind = "vless-ws-tls"
	trVLESSGRPC      transportKind = "vless-grpc-tls"
	trVLESSXHTTP     transportKind = "vless-xhttp-tls"
	trTrojanRaw      transportKind = "trojan-raw"
	trSocks          transportKind = "socks5-xray-exit"
	trSocksPeer      transportKind = "socks5-conforming-exit"
)

func allTransports() []transportKind {
	return []transportKind{trVLESSRaw, trVLESSTLS, trVLESSWebSocket, trVLESSGRPC, trVLESSXHTTP, trTrojanRaw, trSocks, trSocksPeer}
}

func (t transportKind) protocol() string {
	switch t {
	case trVLESSRaw, trVLESSTLS, trVLESSWebSocket, trVLESSGRPC, trVLESSXHTTP:
		return "vless"
	case trTrojanRaw:
		return "trojan"
	case trSocks, trSocksPeer:
		return "socks"
	default:
		panic("unknown transport " + string(t))
	}
}

func (t transportKind) usesTLS() bool {
	switch t {
	case trVLESSTLS, trVLESSWebSocket, trVLESSGRPC, trVLESSXHTTP:
		return true
	}
	return false
}

func (t transportKind) alpn() []string {
	switch t {
	case trVLESSWebSocket:
		return []string{"http/1.1"}
	case trVLESSGRPC, trVLESSXHTTP:
		return []string{"h2"}
	default:
		return []string{"http/1.1"}
	}
}

// certBundle is a throwaway self-signed loopback certificate. The client pins
// the leaf hash; no TLS verification is disabled.
type certBundle struct {
	certFile string
	keyFile  string
	pinHex   string
	dir      string
}

func newCertBundle() (*certBundle, error) {
	dir, err := os.MkdirTemp("", "rl-spike-tls-")
	if err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "spike.local"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(2 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"spike.local"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(der)
	return &certBundle{certFile: certPath, keyFile: keyPath, pinHex: hex.EncodeToString(sum[:]), dir: dir}, nil
}

// streamSettings builds the client-side transport block for one kind.
func clientStreamSettings(t transportKind, b *certBundle) map[string]any {
	ss := map[string]any{}
	switch t {
	case trVLESSRaw, trTrojanRaw, trSocks, trSocksPeer:
		ss["network"] = "tcp"
	case trVLESSTLS:
		ss["network"] = "tcp"
	case trVLESSWebSocket:
		ss["network"] = "ws"
		ss["wsSettings"] = map[string]any{"path": "/spike"}
	case trVLESSGRPC:
		ss["network"] = "grpc"
		ss["grpcSettings"] = map[string]any{"serviceName": "spike"}
	case trVLESSXHTTP:
		ss["network"] = "xhttp"
		ss["xhttpSettings"] = map[string]any{"path": "/spike", "mode": "auto"}
	}
	if t.usesTLS() {
		ss["security"] = "tls"
		// Leaf pinning is used instead of disabling verification: the chain is
		// not trusted, but the exact loopback leaf is pinned.
		ss["tlsSettings"] = map[string]any{
			"serverName":           "spike.local",
			"pinnedPeerCertSha256": b.pinHex,
			"alpn":                 t.alpn(),
		}
	}
	return ss
}

// serverStreamSettings builds the exit-side transport block for one kind.
func serverStreamSettings(t transportKind, b *certBundle) map[string]any {
	ss := clientStreamSettings(t, b)
	if !t.usesTLS() {
		return ss
	}
	ss["tlsSettings"] = map[string]any{
		"serverName": "spike.local",
		"certificates": []any{
			map[string]any{"certificateFile": b.certFile, "keyFile": b.keyFile},
		},
		"alpn": t.alpn(),
	}
	return ss
}

func vlessInbound(port int, uuid string, ss map[string]any) map[string]any {
	return map[string]any{
		"tag":      "in",
		"listen":   "127.0.0.1",
		"port":     port,
		"protocol": "vless",
		"settings": map[string]any{
			"clients":    []any{map[string]any{"id": uuid}},
			"decryption": "none",
		},
		"streamSettings": ss,
	}
}

func trojanInbound(port int, password string, ss map[string]any) map[string]any {
	return map[string]any{
		"tag":            "in",
		"listen":         "127.0.0.1",
		"port":           port,
		"protocol":       "trojan",
		"settings":       map[string]any{"clients": []any{map[string]any{"password": password}}},
		"streamSettings": ss,
	}
}

func socksInbound(port int) map[string]any {
	return map[string]any{
		"tag":      "in",
		"listen":   "127.0.0.1",
		"port":     port,
		"protocol": "socks",
		"settings": map[string]any{"auth": "noauth", "udp": false},
	}
}

func vlessOutbound(tag string, port int, uuid string, ss map[string]any) map[string]any {
	return map[string]any{
		"protocol": "vless",
		"tag":      tag,
		"settings": map[string]any{
			"vnext": []any{map[string]any{
				"address": "127.0.0.1",
				"port":    port,
				"users":   []any{map[string]any{"id": uuid, "encryption": "none"}},
			}},
		},
		"streamSettings": ss,
	}
}

func trojanOutbound(tag string, port int, password string, ss map[string]any) map[string]any {
	return map[string]any{
		"protocol": "trojan",
		"tag":      tag,
		"settings": map[string]any{
			"servers": []any{map[string]any{"address": "127.0.0.1", "port": port, "password": password}},
		},
		"streamSettings": ss,
	}
}

func socksOutbound(tag string, port int) map[string]any {
	return map[string]any{
		"protocol": "socks",
		"tag":      tag,
		"settings": map[string]any{
			"servers": []any{map[string]any{"address": "127.0.0.1", "port": port}},
		},
	}
}

type exitSpec struct {
	Port       int    `json:"port"`
	Transport  string `json:"transport"`
	UUID       string `json:"uuid"`
	Password   string `json:"password"`
	CertFile   string `json:"certFile,omitempty"`
	KeyFile    string `json:"keyFile,omitempty"`
	PinHex     string `json:"pinHex,omitempty"`
	FastPolicy bool   `json:"fastPolicy,omitempty"`
}

func logLevel() string {
	if v := os.Getenv("RL_SPIKE_LOG"); v != "" {
		return v
	}
	return "warning"
}

// fastTeardown sets both ends' one-way timeouts to zero; the split envs below
// let the client and exit policies be isolated.
func fastTeardown() bool { return os.Getenv("RL_SPIKE_FAST_TEARDOWN") != "" }

func exitFast() bool {
	return fastTeardown() || os.Getenv("RL_SPIKE_FAST_EXIT") != ""
}

func clientFast() bool {
	return fastTeardown() || os.Getenv("RL_SPIKE_FAST_CLIENT") != ""
}

func policyBlock(fast bool) map[string]any {
	if !fast {
		return nil
	}
	return map[string]any{
		"levels": map[string]any{
			"0": map[string]any{"uplinkOnly": 0, "downlinkOnly": 0},
		},
	}
}

func buildExitConfig(spec exitSpec) ([]byte, error) {
	t := transportKind(spec.Transport)
	b := &certBundle{certFile: spec.CertFile, keyFile: spec.KeyFile, pinHex: spec.PinHex}
	ss := serverStreamSettings(t, b)
	var inbound map[string]any
	switch t.protocol() {
	case "vless":
		inbound = vlessInbound(spec.Port, spec.UUID, ss)
	case "trojan":
		inbound = trojanInbound(spec.Port, spec.Password, ss)
	case "socks":
		inbound = socksInbound(spec.Port)
	}
	cfg := map[string]any{
		"log":      map[string]any{"loglevel": logLevel(), "access": "none"},
		"inbounds": []any{inbound},
		"outbounds": []any{map[string]any{
			"protocol": "freedom",
			"tag":      "direct",
			// Xray 26.11.9 applies a default "block private IP" final rule to
			// freedom when the inbound is VLESS/VMess/Trojan. This spike only
			// uses loopback peers, so the test exit explicitly allows 127/8.
			// It is a fixture exception, not RouteLatch behaviour.
			"settings": map[string]any{
				"finalRules": []any{map[string]any{
					"action": "allow", "network": "tcp", "ip": []string{"127.0.0.0/8"},
				}},
			},
		}},
	}
	if p := policyBlock(spec.FastPolicy); p != nil {
		cfg["policy"] = p
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// buildClientConfig creates one core with several outbounds so a single process
// can compare a healthy pool tag, a dead pool tag, a refused exit, direct, and
// blackhole without restarting the engine.
type clientOutbounds struct {
	poolPort    int
	deadPort    int
	refusedPort int
	badPort     int
	uuid        string
	password    string
	cert        *certBundle
}

func buildClientConfig(t transportKind, o clientOutbounds) ([]byte, error) {
	ss := clientStreamSettings(t, o.cert)
	mk := func(tag string, port int) map[string]any {
		switch t.protocol() {
		case "vless":
			return vlessOutbound(tag, port, o.uuid, ss)
		case "trojan":
			return trojanOutbound(tag, port, o.password, ss)
		case "socks":
			return socksOutbound(tag, port)
		}
		return nil
	}
	outbounds := []any{
		mk("pool", o.poolPort),
		mk("badpool", o.badPort),
		mk("deadpool", o.deadPort),
		mk("refusedpool", o.refusedPort),
		map[string]any{"protocol": "freedom", "tag": "direct"},
		map[string]any{"protocol": "blackhole", "tag": "blocked"},
	}
	cfg := map[string]any{
		"log":       map[string]any{"loglevel": logLevel(), "access": "none"},
		"outbounds": outbounds,
	}
	if p := policyBlock(clientFast()); p != nil {
		cfg["policy"] = p
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// buildRouteConfig is for the fallback-boundary check, not for the product:
// it makes the core's default handler dangerous (direct is first) and routes
// either all TCP or only one non-matching domain to a non-existent tag.
func buildRouteConfig(allTCP bool) ([]byte, error) {
	var rules []any
	if allTCP {
		rules = []any{map[string]any{
			"type": "field", "network": "tcp", "outboundTag": "ghost", "ruleTag": "ghost-all-tcp",
		}}
	} else {
		rules = []any{map[string]any{
			"type": "field", "domain": []string{"only.invalid"}, "outboundTag": "ghost",
		}}
	}
	cfg := map[string]any{
		"log": map[string]any{"loglevel": logLevel(), "access": "none"},
		"outbounds": []any{
			map[string]any{"protocol": "freedom", "tag": "direct"},
			map[string]any{"protocol": "blackhole", "tag": "blocked"},
		},
		"routing": map[string]any{"rules": rules},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func buildBalancerConfig() ([]byte, error) {
	cfg := map[string]any{
		"log": map[string]any{"loglevel": logLevel(), "access": "none"},
		"outbounds": []any{
			map[string]any{"protocol": "freedom", "tag": "direct"},
			map[string]any{"protocol": "blackhole", "tag": "blocked"},
		},
		"routing": map[string]any{
			"balancers": []any{map[string]any{"tag": "b", "selector": []string{"pool"}}},
			"rules": []any{map[string]any{
				"type": "field", "network": "tcp", "balancerTag": "b",
			}},
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func startCore(configBytes []byte) (*core.Instance, error) {
	cfg, err := serial.LoadJSONConfig(bytes.NewReader(configBytes))
	if err != nil {
		return nil, err
	}
	inst, err := core.New(cfg)
	if err != nil {
		return nil, err
	}
	if err := inst.Start(); err != nil {
		return nil, err
	}
	return inst, nil
}
