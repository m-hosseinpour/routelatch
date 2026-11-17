package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"routelatch/internal/contract"
)

func TestVersionDefaultsAreHonest(t *testing.T) {
	got := currentVersion()
	if got.Version != "dev" || got.Commit != "unknown" || got.BuildDate != "unknown" {
		t.Fatalf("defaults = %+v, want dev/unknown/unknown", got)
	}
}

func TestVersionCommandJSONShape(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "version"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	var env contract.Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v (%q)", err, out.String())
	}
	if !env.OK || env.Command != "version" || env.SchemaVersion != 1 {
		t.Fatalf("envelope = %+v", env)
	}
	data, _ := env.Data.(map[string]any)
	for _, key := range []string{"version", "commit", "build_date"} {
		if _, ok := data[key].(string); !ok {
			t.Fatalf("missing string %q in %+v", key, data)
		}
	}
	if data["version"] != "dev" {
		t.Fatalf("version = %v", data["version"])
	}
}

func TestVersionNeedsNoServiceOrDataDir(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "--data-dir", "/definitely/missing/routelatch", "version"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	var env contract.Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || !env.OK {
		t.Fatalf("envelope = %+v, err=%v", env, err)
	}
}

func TestVersionHumanOutputIsUnstamped(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Execute(context.Background(), []string{"version"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	text := out.String()
	for _, want := range []string{"RouteLatch dev (unstamped build)", "commit: unknown", "build date: unknown"} {
		if !strings.Contains(text, want) {
			t.Fatalf("human output %q missing %q", text, want)
		}
	}
}
