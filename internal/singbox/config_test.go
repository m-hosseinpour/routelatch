package singbox

import (
	"encoding/json"
	"strings"
	"testing"

	"routelatch/internal/contract"
)

func testExit(tag string) contract.SidecarExit {
	return contract.SidecarExit{
		Tag:          tag,
		Server:       "hy.example.com",
		Port:         443,
		Password:     "auth-secret",
		ServerName:   "hy.example.com",
		Obfs:         "salamander",
		ObfsPassword: "river",
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestGenerateConfigShape(t *testing.T) {
	gen, err := Generate([]contract.SidecarExit{testExit("rl-x1"), testExit("rl-x2")})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(gen.Raw, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The document owns exactly these four keys. dns/resolve are absent so the
	// destination hostname is not resolved locally (D15).
	for _, key := range []string{"log", "inbounds", "outbounds", "route"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("missing root key %s", key)
		}
	}
	if len(doc) != 4 {
		t.Fatalf("unexpected root keys: %v", keysOf(doc))
	}
	if _, ok := doc["dns"]; ok {
		t.Fatal("a dns section was generated")
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if strings.EqualFold(k, "resolve") {
					t.Fatalf("a resolve rule was generated under %q", k)
				}
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(doc)

	inbounds, _ := doc["inbounds"].([]any)
	outbounds, _ := doc["outbounds"].([]any)
	if len(inbounds) != 2 || len(outbounds) != 3 {
		t.Fatalf("inbounds=%d outbounds=%d", len(inbounds), len(outbounds))
	}
	for i, raw := range inbounds {
		in, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("inbound %d is not an object", i)
		}
		if in["type"] != "mixed" || in["listen"] != "127.0.0.1" {
			t.Fatalf("inbound %d is not a loopback mixed inbound: %v", i, in)
		}
		if in["set_system_proxy"] != false {
			t.Fatalf("inbound %d may change system proxy settings", i)
		}
		users, _ := in["users"].([]any)
		if len(users) != 1 {
			t.Fatalf("inbound %d has no single authenticated user", i)
		}
		u, _ := users[0].(map[string]any)
		if u["username"] == "" || u["password"] == "" {
			t.Fatalf("inbound %d credentials are empty", i)
		}
		want := gen.Handoffs[[]string{"rl-x1", "rl-x2"}[i]]
		if in["listen_port"] == nil || want.Address == "" {
			t.Fatalf("inbound %d has no published handoff", i)
		}
		if !strings.HasPrefix(want.Address, "127.0.0.1:") {
			t.Fatalf("handoff is not loopback: %q", want.Address)
		}
	}
	for i, raw := range outbounds {
		out, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("outbound %d is not an object", i)
		}
		if i == len(outbounds)-1 {
			if out["type"] != "block" || out["tag"] != BlockTag {
				t.Fatalf("final outbound is not fail-closed: %v", out)
			}
			continue
		}
		if out["type"] != "hysteria2" || out["tag"] == nil || out["server"] != "hy.example.com" ||
			out["server_port"] == nil || out["password"] == nil {
			t.Fatalf("outbound %d is not a projected hysteria2 exit: %v", i, out)
		}
		if _, ok := out["transport"]; ok {
			t.Fatalf("outbound %d carries a transport field", i)
		}
		if out["up_mbps"] != float64(DefaultUpMbps) || out["down_mbps"] != float64(DefaultDownMbps) {
			t.Fatalf("outbound %d bandwidth defaults: %v", i, out)
		}
		tls, ok := out["tls"].(map[string]any)
		if !ok || tls["enabled"] != true || tls["insecure"] != false || tls["server_name"] != "hy.example.com" {
			t.Fatalf("outbound %d TLS is not verification-only: %v", i, out["tls"])
		}
		obfs, ok := out["obfs"].(map[string]any)
		if !ok || obfs["type"] != "salamander" || obfs["password"] != "river" {
			t.Fatalf("outbound %d obfs shape: %v", i, out["obfs"])
		}
	}

	route, _ := doc["route"].(map[string]any)
	if route["final"] != BlockTag {
		t.Fatalf("route final=%v", route["final"])
	}
	rules, _ := route["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("route rules=%d", len(rules))
	}
	for i, raw := range rules {
		rule, _ := raw.(map[string]any)
		if rule["action"] != "route" || rule["outbound"] != []string{"rl-x1", "rl-x2"}[i] {
			t.Fatalf("rule %d=%v", i, rule)
		}
	}
	if len(gen.Handoffs) != 2 {
		t.Fatalf("handoffs=%d", len(gen.Handoffs))
	}
}

func TestGenerateValidation(t *testing.T) {
	cases := []struct {
		name string
		exit contract.SidecarExit
	}{
		{"empty tag", func() contract.SidecarExit { e := testExit(""); return e }()},
		{"control char in tag", contract.SidecarExit{Tag: "x\n", Server: "h", Port: 443, Password: "p", ServerName: "h"}},
		{"missing password", contract.SidecarExit{Tag: "t", Server: "h", Port: 443, ServerName: "h"}},
		{"missing server", contract.SidecarExit{Tag: "t", Port: 443, Password: "p", ServerName: "h"}},
		{"missing port", contract.SidecarExit{Tag: "t", Server: "h", Password: "p", ServerName: "h"}},
		{"missing server name", contract.SidecarExit{Tag: "t", Server: "h", Port: 443, Password: "p"}},
		{"control char in server", contract.SidecarExit{Tag: "t", Server: "h\r\n", Port: 443, Password: "p", ServerName: "h"}},
		{"bad obfs", func() contract.SidecarExit { e := testExit("t"); e.Obfs = "tls"; return e }()},
		{"obfs without password", func() contract.SidecarExit { e := testExit("t"); e.ObfsPassword = ""; return e }()},
		{"oversized bandwidth", func() contract.SidecarExit { e := testExit("t"); e.UpMbps = maxMbps + 1; return e }()},
	}
	for _, tc := range cases {
		if _, err := Generate([]contract.SidecarExit{tc.exit}); err == nil {
			t.Fatalf("%s was accepted", tc.name)
		}
	}
	if _, err := Generate([]contract.SidecarExit{testExit("same"), testExit("same")}); err == nil {
		t.Fatal("duplicate tags were accepted")
	}
	if _, err := Generate(nil); err == nil {
		t.Fatal("empty exit list was accepted")
	}
}

func TestGenerateIsFresh(t *testing.T) {
	a, err := Generate([]contract.SidecarExit{testExit("rl-x1")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate([]contract.SidecarExit{testExit("rl-x1")})
	if err != nil {
		t.Fatal(err)
	}
	if a.Handoffs["rl-x1"] == b.Handoffs["rl-x1"] {
		t.Fatal("a regeneration reused credentials or a port")
	}
}
