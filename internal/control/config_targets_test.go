package control

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestConfigSetEmptyProbesTargetsPersistsAnEmptyList(t *testing.T) {
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	if _, err := svc.Handle(context.Background(), Request{Op: OpConfigSet, Key: "probes.targets", Value: ""}); err != nil {
		t.Fatalf("config set probes.targets=: %v", err)
	}
	snap, err := st.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Config.Probes.Targets == nil || len(snap.Config.Probes.Targets) != 0 {
		t.Fatalf("targets = %#v, want a non-nil empty list", snap.Config.Probes.Targets)
	}
	raw, err := os.ReadFile(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"targets": []`) {
		t.Fatalf("config does not persist an empty targets list:\n%s", raw)
	}
}
