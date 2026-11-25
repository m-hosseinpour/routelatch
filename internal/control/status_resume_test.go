package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"routelatch/internal/contract"
)

func hasWarningContaining(warnings []string, part string) bool {
	for _, w := range warnings {
		if strings.Contains(w, part) {
			return true
		}
	}
	return false
}

// legacyStatusClient mirrors the pre-R15 status payload a shipped consumer
// decodes. The additive field must not disturb it.
type legacyStatusClient struct {
	Mode          contract.Mode `json:"mode"`
	Ready         bool          `json:"ready"`
	ListenersOpen bool          `json:"listeners_open"`
}

// TestStatusResumeRequiresRestartAfterStopInThisProcess covers the only case
// where resuming needs a restart: this process closed the engine itself.
func TestStatusResumeRequiresRestartAfterStopInThisProcess(t *testing.T) {
	svc, ctx := configuredService(t)
	if err := svc.StartNetwork(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.setMode(ctx, string(contract.ModeStopped)); err != nil {
		t.Fatal(err)
	}
	status, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.ResumeRequiresRestart == nil || !*status.ResumeRequiresRestart {
		t.Fatalf("resume_requires_restart=%v, want true after stopping in this process", status.ResumeRequiresRestart)
	}
	if !hasWarningContaining(status.Warnings, "needs a process restart") {
		t.Fatalf("warnings=%v, want the restart warning", status.Warnings)
	}
	if hasWarningContaining(status.Warnings, "no process restart is needed") {
		t.Fatalf("warnings=%v, want only the restart warning", status.Warnings)
	}
}

// TestStatusResumeRequiresRestartFreshProcessInStopped covers a process that
// started in the persisted stopped state: a mode change is the resume.
func TestStatusResumeRequiresRestartFreshProcessInStopped(t *testing.T) {
	ctx := context.Background()
	st, paths := newServiceStore(t)
	if _, err := st.Mutate(ctx, func(state *contract.State) error {
		state.Mode = contract.ModeStopped
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, st, paths)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	status, err := svc.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.ResumeRequiresRestart == nil || *status.ResumeRequiresRestart {
		t.Fatalf("resume_requires_restart=%v, want false for a fresh process in stored stopped mode", status.ResumeRequiresRestart)
	}
	if !hasWarningContaining(status.Warnings, "choose a mode to resume") {
		t.Fatalf("warnings=%v, want the resume-without-restart warning", status.Warnings)
	}
	if hasWarningContaining(status.Warnings, "needs a process restart") {
		t.Fatalf("warnings=%v, the restart warning must not appear when a mode change can resume", status.Warnings)
	}
}

// TestStatusResumeRequiresRestartFalseOutsideStopped pins that the field is
// false in every non-stopped mode and is still emitted explicitly.
func TestStatusResumeRequiresRestartFalseOutsideStopped(t *testing.T) {
	for _, mode := range []contract.Mode{contract.ModeTargeted, contract.ModeProxyAll} {
		t.Run(string(mode), func(t *testing.T) {
			svc, ctx := configuredService(t)
			if err := svc.StartNetwork(ctx); err != nil {
				t.Fatal(err)
			}
			if mode != contract.ModeTargeted {
				if _, err := svc.setMode(ctx, string(mode)); err != nil {
					t.Fatal(err)
				}
			}
			status, err := svc.status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if status.ResumeRequiresRestart == nil || *status.ResumeRequiresRestart {
				t.Fatalf("resume_requires_restart=%v, want false in mode %s", status.ResumeRequiresRestart, mode)
			}
			if hasWarningContaining(status.Warnings, "service is stopped") {
				t.Fatalf("warnings=%v, want no stopped warning in mode %s", status.Warnings, mode)
			}
			raw, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if value, present := decoded["resume_requires_restart"]; !present || value != false {
				t.Fatalf("status JSON = %s, want an explicit false resume_requires_restart", raw)
			}
		})
	}
}

// TestStatusResumeRequiresRestartJSONCompatibility pins the encoded shape and
// the decode rule: the server always emits a boolean, the payload stays
// additive, schema_version does not move, and an older payload without the key
// decodes as unknown (nil) rather than false.
func TestStatusResumeRequiresRestartJSONCompatibility(t *testing.T) {
	truth := true
	status := StatusData{Mode: contract.ModeStopped, Ready: true, ResumeRequiresRestart: &truth, Warnings: []string{}}
	// schema_version here is the management protocol version, not the state
	// schema; this additive field must not move it.
	env := EnvelopeFor("status", status, nil)
	if env.SchemaVersion != 1 {
		t.Fatalf("management schema_version moved to %d; this field is additive and must not change it", env.SchemaVersion)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if value, present := decoded["resume_requires_restart"]; !present || value != true {
		t.Fatalf("status JSON = %s, want an explicit true resume_requires_restart", raw)
	}
	if _, present := decoded["schema_version"]; present {
		t.Fatalf("status payload = %s, it must not carry schema_version", raw)
	}

	// An old client decodes the new payload without changes.
	var oldClient legacyStatusClient
	if err := json.Unmarshal(raw, &oldClient); err != nil {
		t.Fatalf("old client cannot decode the additive payload: %v", err)
	}
	if oldClient.Mode != contract.ModeStopped || !oldClient.Ready || oldClient.ListenersOpen {
		t.Fatalf("old client decoded %+v", oldClient)
	}

	// A new consumer decoding an old payload must see unknown, not false.
	var fromOld StatusData
	if err := json.Unmarshal([]byte(`{"mode":"stopped","ready":true,"listeners_open":false}`), &fromOld); err != nil {
		t.Fatal(err)
	}
	if fromOld.ResumeRequiresRestart != nil {
		t.Fatalf("missing field decoded as %v, want unknown (nil)", *fromOld.ResumeRequiresRestart)
	}

	var explicitFalse StatusData
	if err := json.Unmarshal([]byte(`{"mode":"stopped","resume_requires_restart":false}`), &explicitFalse); err != nil {
		t.Fatal(err)
	}
	if explicitFalse.ResumeRequiresRestart == nil || *explicitFalse.ResumeRequiresRestart {
		t.Fatalf("explicit false decoded as %v, want false", explicitFalse.ResumeRequiresRestart)
	}
}
