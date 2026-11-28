package control

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"routelatch/internal/contract"
)

// TestAllProxyAndAllDirectNeverLogOrPersistDestinations is the D21 regression
// test for the explicit non-targeted modes. A destination handed to the
// management surface is refused by the mode gate; neither the reply, the
// foreground log, the operational log file nor persisted state may retain the
// host or address. The test is read-only: it asserts the existing gating and
// does not change routing or probe behaviour.
func TestAllProxyAndAllDirectNeverLogOrPersistDestinations(t *testing.T) {
	var logBuf bytes.Buffer
	h := newCanaryHarness(t, &logBuf)

	address := "192.0.2.77"
	destinations := []string{address}
	for _, mode := range []contract.Mode{contract.ModeProxyAll, contract.ModeDirectAll} {
		if env := h.call(t, Request{Op: OpMode, Mode: string(mode)}); !env.OK {
			t.Fatalf("mode %s failed: %+v", mode, env.Error)
		}
		domain := canaryName(t)
		destinations = append(destinations, domain)

		for _, input := range []string{domain + ":443", address + ":443"} {
			env := h.call(t, Request{Op: OpDiagnose, Input: input})
			if env.OK {
				t.Fatalf("%s diagnose accepted %q; the mode gate must refuse it", mode, input)
			}
			if env.Error == nil || env.Error.Kind != contract.PolicyDenied {
				t.Fatalf("%s diagnose error = %+v, want policy_denied", mode, env.Error)
			}
			raw, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{domain, address} {
				if strings.Contains(string(raw), want) {
					t.Fatalf("%s diagnose reply leaked %q: %s", mode, want, raw)
				}
			}
		}
	}

	if got := h.svc.Snapshot().State.Mode; got != contract.ModeDirectAll {
		t.Fatalf("persisted mode = %s, want the last requested all-direct mode", got)
	}
	for _, want := range destinations {
		if strings.Contains(logBuf.String(), want) {
			t.Fatalf("foreground service log contains %q", want)
		}
		assertAbsentFromTree(t, h.dir, want)
	}
}
