package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/store"
)

func startInProcess(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rlcli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	paths, err := store.ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := control.NewService(context.Background(), control.Options{Store: st, Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := control.NewServer(control.ServerOptions{Path: paths.Socket, Handler: svc, AllowStale: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return dir
}

type cliResult struct {
	code   int
	env    contract.Envelope
	stdout string
	stderr string
}

func runCLI(t *testing.T, dir string, args ...string) cliResult {
	t.Helper()
	var out, errOut bytes.Buffer
	full := append([]string{"--json", "--data-dir", dir}, args...)
	code := Execute(context.Background(), full, &out, &errOut)
	result := cliResult{code: code, stdout: out.String(), stderr: errOut.String()}
	if strings.HasPrefix(strings.TrimSpace(result.stdout), "{") {
		if err := json.Unmarshal(out.Bytes(), &result.env); err != nil {
			t.Fatalf("invalid JSON envelope %q: %v", result.stdout, err)
		}
	}
	return result
}

func TestCLIEnvelopesAndExitCodes(t *testing.T) {
	dir := startInProcess(t)

	status := runCLI(t, dir, "status")
	if status.code != 0 || !status.env.OK || status.env.Command != "status" || status.env.SchemaVersion != 1 {
		t.Fatalf("status = %+v (code %d)", status.env, status.code)
	}

	add := runCLI(t, dir, "rules", "add", "example.com")
	if add.code != 0 || !add.env.OK || add.env.Command != "rules add" {
		t.Fatalf("rules add = %+v (code %d, stderr %s)", add.env, add.code, add.stderr)
	}

	list := runCLI(t, dir, "rules", "list")
	if list.code != 0 || !list.env.OK || list.env.Command != "rules list" {
		t.Fatalf("rules list = %+v", list.env)
	}
	data, _ := list.env.Data.(map[string]any)
	rules, _ := data["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("rules = %v", data["rules"])
	}

	missing := runCLI(t, dir, "rules", "remove", "r-does-not-exist")
	if missing.code != 5 {
		t.Fatalf("missing rule exit = %d, want 5 (%+v)", missing.code, missing.env)
	}
	if missing.env.OK || missing.env.Error == nil || missing.env.Error.Kind != contract.NotFound {
		t.Fatalf("missing rule envelope = %+v", missing.env)
	}

	badMode := runCLI(t, dir, "mode", "sideways")
	if badMode.code != 2 || badMode.env.Error == nil || badMode.env.Error.Kind != contract.InvalidDestination {
		t.Fatalf("usage exit = %d, envelope %+v", badMode.code, badMode.env)
	}

	unknown := runCLI(t, dir, "frobnicate")
	if unknown.code != 2 {
		t.Fatalf("unknown command exit = %d, want 2", unknown.code)
	}

	emptyPool := runCLI(t, dir, "pool", "list")
	if emptyPool.code != 0 || !emptyPool.env.OK {
		t.Fatalf("pool list = %+v", emptyPool.env)
	}

	doctor := runCLI(t, dir, "doctor")
	if doctor.code != 0 || !doctor.env.OK || doctor.env.Command != "doctor" {
		t.Fatalf("doctor = %+v (code %d)", doctor.env, doctor.code)
	}
}

func TestCLIModeSwitchAndStoppedRestartRequirement(t *testing.T) {
	dir := startInProcess(t)
	if got := runCLI(t, dir, "mode", "all-direct"); got.code != 0 {
		t.Fatalf("mode all-direct exit = %d: %+v", got.code, got.env)
	}
	status := runCLI(t, dir, "status")
	data, _ := status.env.Data.(map[string]any)
	if data["mode"] != "all-direct" {
		t.Fatalf("mode = %v", data["mode"])
	}
	stopped := runCLI(t, dir, "mode", "stopped")
	if stopped.code != 0 {
		t.Fatalf("mode stopped exit = %d: %+v", stopped.code, stopped.env)
	}
	restart := runCLI(t, dir, "mode", "targeted")
	if restart.code != 6 {
		t.Fatalf("leave stopped exit = %d, want 6 (%+v)", restart.code, restart.env)
	}
}

func TestCLIWithoutServiceReportsStopped(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "--data-dir", dir, "status"}, &out, &errOut)
	if code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
	var env contract.Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.OK || env.Error == nil || env.Error.Kind != contract.Stopped {
		t.Fatalf("envelope = %+v", env)
	}
}

func TestMenuCommandMappingAndSanitizer(t *testing.T) {
	args, ok := menuCommand("rules.add", "example.com", "domain_suffix", "")
	if !ok || strings.Join(args, " ") != "rules add --kind domain_suffix example.com" {
		t.Fatalf("rules.add mapping = %v", args)
	}
	args, ok = menuCommand("mode", "all-direct", "", "")
	if !ok || strings.Join(args, " ") != "mode all-direct" {
		t.Fatalf("mode mapping = %v", args)
	}
	args, ok = menuCommand("config.set", "log_level", "debug", "")
	if !ok || strings.Join(args, " ") != "config set log_level debug" {
		t.Fatalf("config.set mapping = %v", args)
	}
	if _, ok := menuCommand("quit", "", "", ""); ok {
		t.Fatal("quit mapped to a command")
	}
	for _, choice := range menuChoices() {
		if choice.Key == "quit" {
			continue
		}
		if _, ok := menuCommand(choice.Key, "operand", "kind", ""); !ok {
			t.Errorf("menu choice %q has no command mapping", choice.Key)
		}
	}
	if got := menuLabel("node\x1b[2Jname"); strings.ContainsRune(got, 0x1b) {
		t.Fatalf("menu label kept an escape: %q", got)
	}
}

func TestNoSecretInCLIOutput(t *testing.T) {
	dir := startInProcess(t)
	secret := "FIXTURE-TOKEN-9f21c"
	link := "trojan://" + secret + "@example.com:443?security=tls&type=raw#node"
	add := runCLI(t, dir, "pool", "add", link)
	if add.code != 0 {
		t.Fatalf("pool add exit = %d: %+v", add.code, add.env)
	}
	for _, args := range [][]string{{"pool", "list"}, {"status"}, {"doctor"}} {
		got := runCLI(t, dir, args...)
		if strings.Contains(got.stdout+got.stderr, secret) {
			t.Fatalf("%v leaked the fixture credential", args)
		}
	}
	// The state file is private and contains the imported secret.
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), secret) {
		t.Fatal("fixture credential was not persisted in the private state file")
	}
}

func TestRunFailureExitCodes(t *testing.T) {
	var out, errOut bytes.Buffer
	c := &cli{stdout: &out, stderr: &errOut}
	cases := []struct {
		failure *contract.Failure
		after   bool
		want    int
	}{
		{&contract.Failure{Kind: contract.StateLocked, Message: "lock"}, false, 4},
		{&contract.Failure{Kind: contract.StateLocked, Message: "bind"}, true, 6},
		{&contract.Failure{Kind: contract.StateCorrupt, Message: "corrupt"}, false, 10},
		{&contract.Failure{Kind: contract.Unsupported, Message: "platform"}, false, 7},
	}
	for _, tc := range cases {
		if got := c.runFailure(tc.failure, tc.after); got != tc.want {
			t.Errorf("runFailure(%s, after=%v) = %d, want %d", tc.failure.Kind, tc.after, got, tc.want)
		}
	}
}

func TestOfflineDoctorAndCorruptStateExit(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rlcli")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	got := runCLI(t, dir, "doctor")
	if got.code != 0 || !got.env.OK || got.env.Command != "doctor" {
		t.Fatalf("offline doctor = %+v (code %d)", got.env, got.code)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	perms := runCLI(t, dir, "doctor")
	if perms.code != 0 || !perms.env.OK {
		t.Fatalf("doctor with loose permissions = %+v (code %d)", perms.env, perms.code)
	}
	checks, _ := perms.env.Data.(map[string]any)["checks"].([]any)
	foundWarn := false
	for _, item := range checks {
		if ch, ok := item.(map[string]any); ok && ch["name"] == "state_dir" && ch["status"] == "warn" {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Fatal("doctor did not report the loose state directory")
	}
	paths, err := store.ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.State, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := runCLI(t, dir, "doctor")
	if corrupt.code != 10 {
		t.Fatalf("corrupt offline doctor exit = %d, want 10 (%+v)", corrupt.code, corrupt.env)
	}
	if corrupt.env.Error == nil || corrupt.env.Error.Kind != contract.StateCorrupt {
		t.Fatalf("corrupt doctor envelope = %+v", corrupt.env)
	}
}

func TestOfflineBackupAndRestore(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rlcli")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	paths, err := store.ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.Mutate(ctx, func(state *contract.State) error {
		state.Mode = contract.ModeDirectAll
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	backup := runCLI(t, dir, "backup")
	if backup.code != 0 || !backup.env.OK {
		t.Fatalf("offline backup = %+v (code %d)", backup.env, backup.code)
	}
	data, _ := backup.env.Data.(map[string]any)
	ref, _ := data["ref"].(map[string]any)
	path, _ := ref["path"].(string)
	if path == "" {
		t.Fatalf("backup path missing: %+v", data)
	}
	if _, err := st.Mutate(ctx, func(state *contract.State) error {
		state.Mode = contract.ModeTargeted
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	restored := runCLI(t, dir, "restore", path)
	if restored.code != 0 || !restored.env.OK {
		t.Fatalf("offline restore = %+v (code %d)", restored.env, restored.code)
	}
	snap, err := st.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.State.Mode != contract.ModeDirectAll {
		t.Fatalf("restored mode = %s", snap.State.Mode)
	}
}

func TestOfflineRestoreFromCorruptState(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rlcli")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	paths, err := store.ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.Mutate(ctx, func(state *contract.State) error {
		state.Mode = contract.ModeDirectAll
		state.PolicyVersion++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ref, err := st.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.State, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := runCLI(t, dir, "restore", ref.Path)
	if got.code != 0 || !got.env.OK {
		t.Fatalf("offline restore over corrupt state = %+v (code %d)", got.env, got.code)
	}
	snap, err := st.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.State.Mode != contract.ModeDirectAll {
		t.Fatalf("mode = %s", snap.State.Mode)
	}
}
