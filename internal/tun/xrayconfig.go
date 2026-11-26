package tun

import (
	"encoding/json"
)

// The generated Xray document is fixed. It contains one TUN inbound, three
// outbounds and four routing rules; there is no API, DNS section, user routing
// or second inbound. logLevel/access/error stay "none" because stock Xray logs
// flow destinations at info/debug (D21, contracts §10.5). The structs below are
// the complete writable surface: anything not named here cannot be generated.

type xrayDocument struct {
	Log       xrayLog        `json:"log"`
	Inbounds  []xrayInbound  `json:"inbounds"`
	Outbounds []xrayOutbound `json:"outbounds"`
	Routing   xrayRouting    `json:"routing"`
}

type xrayLog struct {
	LogLevel string `json:"logLevel"`
	Access   string `json:"access"`
	Error    string `json:"error"`
}

type xrayInbound struct {
	Tag      string       `json:"tag"`
	Protocol string       `json:"protocol"`
	Settings xrayTun      `json:"settings"`
	Sniffing xraySniffing `json:"sniffing"`
}

type xrayTun struct {
	Name                   string   `json:"name"`
	MTU                    uint32   `json:"mtu"`
	Gateway                []string `json:"gateway"`
	AutoSystemRoutingTable []string `json:"autoSystemRoutingTable"`
	AutoOutboundsInterface string   `json:"autoOutboundsInterface"`
	AutoSystemDnsToGateway bool     `json:"autoSystemDnsToGateway"`
}

type xraySniffing struct {
	Enabled      bool     `json:"enabled"`
	DestOverride []string `json:"destOverride"`
	RouteOnly    bool     `json:"routeOnly"`
}

type xrayOutbound struct {
	Tag      string                `json:"tag"`
	Protocol string                `json:"protocol"`
	Settings *xrayOutboundSettings `json:"settings,omitempty"`
}

type xrayOutboundSettings struct {
	Servers []xraySocksServer `json:"servers"`
}

type xraySocksServer struct {
	Address string `json:"address"`
	Port    uint16 `json:"port"`
}

type xrayRouting struct {
	DomainStrategy string     `json:"domainStrategy"`
	Rules          []xrayRule `json:"rules"`
}

type xrayRule struct {
	Type        string   `json:"type"`
	InboundTag  []string `json:"inboundTag,omitempty"`
	Network     string   `json:"network,omitempty"`
	Port        string   `json:"port,omitempty"`
	OutboundTag string   `json:"outboundTag"`
}

// buildXrayConfig renders the service's fixed document for one loopback SOCKS
// target. It is called once per enable; there is no persistent Xray config file.
func buildXrayConfig(socksAddress string) ([]byte, error) {
	host, port, err := ParseSocksAddress(socksAddress)
	if err != nil {
		return nil, err
	}
	doc := xrayDocument{
		Log: xrayLog{LogLevel: "none", Access: "none", Error: "none"},
		Inbounds: []xrayInbound{{
			Tag:      inboundTag,
			Protocol: "tun",
			Settings: xrayTun{
				Name:                   InterfaceName,
				MTU:                    MTU,
				Gateway:                append([]string(nil), Gateways...),
				AutoSystemRoutingTable: append([]string(nil), HalfRoutes...),
				AutoOutboundsInterface: "auto",
				AutoSystemDnsToGateway: false,
			},
			Sniffing: xraySniffing{
				Enabled:      true,
				DestOverride: []string{"http", "tls"},
				RouteOnly:    false,
			},
		}},
		Outbounds: []xrayOutbound{
			{Tag: blockTag, Protocol: "blackhole"},
			{Tag: dnsTag, Protocol: "freedom"},
			{
				Tag:      socksTag,
				Protocol: "socks",
				Settings: &xrayOutboundSettings{Servers: []xraySocksServer{{Address: host, Port: port}}},
			},
		},
		Routing: xrayRouting{
			DomainStrategy: "AsIs",
			Rules: []xrayRule{
				// Plain DNS on TCP or UDP is sent directly by the freedom
				// outbound; autoOutboundsInterface keeps it off the TUN.
				{Type: "field", Network: "tcp,udp", Port: "53", OutboundTag: dnsTag},
				// Everything else TCP goes to the engine's loopback SOCKS
				// listener, which owns policy, pool and registry.
				{Type: "field", InboundTag: []string{inboundTag}, Network: "tcp", OutboundTag: socksTag},
				// Non-DNS UDP (QUIC included) is dropped in the first phase.
				{Type: "field", Network: "udp", OutboundTag: blockTag},
				// Final catch-all: never a direct fallback. The network field
				// is required; the pinned router refuses a rule with no
				// effective condition.
				{Type: "field", Network: "tcp,udp", OutboundTag: blockTag},
			},
		},
	}
	return json.MarshalIndent(doc, "", "  ")
}
