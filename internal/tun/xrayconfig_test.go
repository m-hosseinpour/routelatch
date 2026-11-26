package tun

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
)

func decodeXray(t *testing.T, socks string) map[string]any {
	t.Helper()
	raw, err := buildXrayConfig(socks)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return doc
}

func TestXrayConfigShape(t *testing.T) {
	doc := decodeXray(t, "127.0.0.1:1080")
	if len(doc) != 4 {
		t.Fatalf("root keys=%v", keys(doc))
	}
	for _, key := range []string{"log", "inbounds", "outbounds", "routing"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("missing root key %q", key)
		}
	}

	logBlock := doc["log"].(map[string]any)
	if logBlock["logLevel"] != "none" || logBlock["access"] != "none" || logBlock["error"] != "none" {
		t.Fatalf("log block=%v", logBlock)
	}

	inbounds := doc["inbounds"].([]any)
	if len(inbounds) != 1 {
		t.Fatalf("inbounds=%d", len(inbounds))
	}
	in := inbounds[0].(map[string]any)
	if in["tag"] != inboundTag || in["protocol"] != "tun" {
		t.Fatalf("inbound=%v", in)
	}
	settings := in["settings"].(map[string]any)
	if settings["name"] != "rl-tun0" || settings["mtu"] != float64(1500) {
		t.Fatalf("tun settings=%v", settings)
	}
	if settings["autoOutboundsInterface"] != "auto" {
		t.Fatalf("autoOutboundsInterface=%v", settings["autoOutboundsInterface"])
	}
	if settings["autoSystemDnsToGateway"] != false {
		t.Fatalf("autoSystemDnsToGateway=%v", settings["autoSystemDnsToGateway"])
	}
	wantGateways := []string{"198.18.0.1/15", "fc00::1/64"}
	gotGateways := stringsOf(t, settings["gateway"])
	if !equalStrings(gotGateways, wantGateways) {
		t.Fatalf("gateways=%v", gotGateways)
	}
	wantRoutes := []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"}
	gotRoutes := stringsOf(t, settings["autoSystemRoutingTable"])
	if !equalStrings(gotRoutes, wantRoutes) {
		t.Fatalf("routes=%v", gotRoutes)
	}
	sniff := in["sniffing"].(map[string]any)
	if sniff["enabled"] != true || sniff["routeOnly"] != false {
		t.Fatalf("sniffing=%v", sniff)
	}
	if got := stringsOf(t, sniff["destOverride"]); !equalStrings(got, []string{"http", "tls"}) {
		t.Fatalf("destOverride=%v", got)
	}

	outbounds := doc["outbounds"].([]any)
	if len(outbounds) != 3 {
		t.Fatalf("outbounds=%d", len(outbounds))
	}
	byTag := map[string]map[string]any{}
	for _, item := range outbounds {
		o := item.(map[string]any)
		byTag[o["tag"].(string)] = o
	}
	if byTag[blockTag]["protocol"] != "blackhole" || byTag[dnsTag]["protocol"] != "freedom" {
		t.Fatalf("outbounds=%v", outbounds)
	}
	socks := byTag[socksTag]
	if socks["protocol"] != "socks" {
		t.Fatalf("socks outbound=%v", socks)
	}
	servers := socks["settings"].(map[string]any)["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("socks servers=%v", servers)
	}
	server := servers[0].(map[string]any)
	if server["address"] != "127.0.0.1" || server["port"] != float64(1080) {
		t.Fatalf("socks server=%v", server)
	}

	routing := doc["routing"].(map[string]any)
	if routing["domainStrategy"] != "AsIs" {
		t.Fatalf("domainStrategy=%v", routing["domainStrategy"])
	}
	rules := routing["rules"].([]any)
	if len(rules) != 4 {
		t.Fatalf("rules=%d", len(rules))
	}
	check := func(i int, network, port, outbound string) {
		r := rules[i].(map[string]any)
		if network != "" && r["network"] != network {
			t.Fatalf("rule %d network=%v", i, r["network"])
		}
		if port != "" && r["port"] != port {
			t.Fatalf("rule %d port=%v", i, r["port"])
		}
		if r["outboundTag"] != outbound {
			t.Fatalf("rule %d outbound=%v", i, r["outboundTag"])
		}
	}
	check(0, "tcp,udp", "53", dnsTag)
	check(1, "tcp", "", socksTag)
	check(2, "udp", "", blockTag)
	check(3, "tcp,udp", "", blockTag)

	for _, forbidden := range []string{"api", "dns", "observatory", "env", "stats", "policy", "reverse"} {
		if _, ok := doc[forbidden]; ok {
			t.Fatalf("generated a forbidden root section %q", forbidden)
		}
	}
}

func TestXrayConfigIsAcceptedAndDeterministic(t *testing.T) {
	a, err := buildXrayConfig("127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildXrayConfig("127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("the fixed config is not deterministic")
	}
	// The pinned parser and core builder are the authority for the shape; this
	// proves the generated document is a config this exact version accepts.
	parsed, err := serial.LoadJSONConfig(bytes.NewReader(a))
	if err != nil {
		t.Fatalf("the pinned parser rejected the config: %v", err)
	}
	inst, err := core.New(parsed)
	if err != nil {
		t.Fatalf("the pinned core rejected the config: %v", err)
	}
	if err := inst.Close(); err != nil {
		t.Fatalf("close the unstarted core: %v", err)
	}
}

func TestXrayConfigIPv6SocksTarget(t *testing.T) {
	doc := decodeXray(t, "[::1]:9050")
	for _, item := range doc["outbounds"].([]any) {
		o := item.(map[string]any)
		if o["tag"] != socksTag {
			continue
		}
		server := o["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)
		if server["address"] != "::1" || server["port"] != float64(9050) {
			t.Fatalf("IPv6 socks server=%v", server)
		}
		return
	}
	t.Fatal("socks outbound missing")
}

func TestXrayConfigHasNoDestinationCanary(t *testing.T) {
	raw, err := buildXrayConfig("127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(raw))
	for _, canary := range []string{"canary", "example.invalid", "example.com", "203.0.113.", "2001:db8"} {
		if strings.Contains(text, canary) {
			t.Fatalf("the generated config contains the canary %q", canary)
		}
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func stringsOf(t *testing.T, v any) []string {
	t.Helper()
	items, ok := v.([]any)
	if !ok {
		t.Fatalf("not a list: %v", v)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.(string))
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
