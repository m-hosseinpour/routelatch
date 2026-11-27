package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/instance"
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
	svc, err := control.NewService(context.Background(), control.Options{Store: st, Paths: paths, LogOutput: io.Discard})
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

func TestRunFailureLegacyExitOverride(t *testing.T) {
	var out, errOut bytes.Buffer
	c := &cli{stdout: &out, stderr: &errOut}
	for _, want := range []int{4, 6} {
		err := &instance.ExitError{
			Exit: contract.ExitCode(want),
			Failure: &contract.Failure{
				Kind: contract.StateLocked, Path: contract.PathLocal, Op: "instance.legacy", Message: "legacy",
			},
		}
		if got := c.runFailure(err, false); got != want {
			t.Errorf("runFailure(legacy exit %d) = %d", want, got)
		}
	}
}

func TestPrintStatusMarksClosedListeners(t *testing.T) {
	var out bytes.Buffer
	c := &cli{stdout: &out}
	c.printStatus(map[string]any{
		"mode": "stopped", "policy_version": 3, "ready": true, "listeners_open": false,
		"listeners": map[string]any{"http": "127.0.0.1:8317", "socks": "127.0.0.1:8318"},
	})
	text := out.String()
	if !strings.Contains(text, "ready: yes") || !strings.Contains(text, "(closed)") {
		t.Fatalf("stopped status = %q, want ready and a closed listener marker", text)
	}
}

// TestPrintStatusPrintsEgressWarning keeps the R-28 warning on the documented
// stderr channel instead of mixing it into the status body.
func TestPrintStatusPrintsEgressWarning(t *testing.T) {
	const warning = "recent approved egress dial failed; traffic remains fail-closed with no direct fallback"
	var out, errOut bytes.Buffer
	c := &cli{stdout: &out, stderr: &errOut}
	c.printStatus(map[string]any{
		"mode": "targeted", "policy_version": 1, "warnings": []any{warning},
	})
	if !strings.Contains(errOut.String(), "warning: "+warning) {
		t.Fatalf("stderr = %q, want the egress warning", errOut.String())
	}
	if strings.Contains(out.String(), warning) {
		t.Fatalf("stdout = %q, warnings must stay on stderr", out.String())
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

type envStubHandler struct{}

func (envStubHandler) Handle(ctx context.Context, req control.Request) (any, error) {
	if req.Op != control.OpEnv {
		return nil, &contract.Failure{Kind: contract.Unsupported, Path: contract.PathLocal, Message: "unexpected op"}
	}
	return map[string]any{
		"HTTP_PROXY": "http://127.0.0.1:8317", "HTTPS_PROXY": "http://127.0.0.1:8317",
		"ALL_PROXY": "http://127.0.0.1:8317", "NO_PROXY": "localhost", "notice": "",
	}, nil
}

func TestExecJSONKeepsEnvelopeAndChildOutputSeparate(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rlcli")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	paths, err := store.ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := control.NewServer(control.ServerOptions{Path: paths.Socket, Handler: envStubHandler{}, AllowStale: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	var out, errOut bytes.Buffer
	args := []string{"--json", "--data-dir", dir, "exec", "--", "sh", "-c", "echo child-out; echo child-err >&2; exit 7"}
	code := Execute(context.Background(), args, &out, &errOut)
	if code != 7 {
		t.Fatalf("exit = %d, want child 7 (stderr %s)", code, errOut.String())
	}
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	var env contract.Envelope
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("stdout is not one parseable envelope: %v (%q)", err, out.String())
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		t.Fatalf("stdout contains more than one JSON value: %q", out.String())
	}
	if !env.OK || env.Command != "exec" {
		t.Fatalf("envelope = %+v", env)
	}
	data, _ := env.Data.(map[string]any)
	if data["exit_code"] != float64(7) {
		t.Fatalf("exit_code = %v", data["exit_code"])
	}
	if strings.Contains(out.String(), "child-out") || strings.Contains(out.String(), "child-err") {
		t.Fatalf("child output leaked into JSON stdout: %q", out.String())
	}
	for _, want := range []string{"child-out", "child-err"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("child %q missing from stderr: %q", want, errOut.String())
		}
	}
}

func TestCLINonLoopbackListenerExitIsUnsupported(t *testing.T) {
	dir := startInProcess(t)
	got := runCLI(t, dir, "config", "set", "http_listen", "0.0.0.0:8317")
	if got.code != 7 {
		t.Fatalf("exit = %d, want 7 (%+v)", got.code, got.env)
	}
	if got.env.Error == nil || got.env.Error.Kind != contract.Unsupported {
		t.Fatalf("kind = %+v, want unsupported", got.env.Error)
	}
}

func TestRunJSONRoutesServiceLogToStderr(t *testing.T) {
	var out, errOut bytes.Buffer
	c := &cli{jsonOut: true, stdout: &out, stderr: &errOut}
	if c.serviceLogOutput() != &errOut {
		t.Fatal("--json run log output is not stderr")
	}
	c.jsonOut = false
	if c.serviceLogOutput() != &out {
		t.Fatal("human run log output is not stdout")
	}
}

func TestPoolRemoveDeletesUnbuildableRecord(t *testing.T) {
	dir := startInProcess(t)
	// allowInsecure without a pin is stored as unsupported and cannot be built
	// by the engine; it must still be removable through the documented CLI.
	insecure := "vless://11111111-1111-4111-8111-111111111111@127.0.0.1:443?encryption=none&security=tls&allowInsecure=1&type=tcp#insecure"
	good := "trojan://pass-one@example.com:443?security=tls&type=raw#good"
	path := filepath.Join(t.TempDir(), "pool.txt")
	if err := os.WriteFile(path, []byte(insecure+"\n"+good+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runCLI(t, dir, "import", "file", path); got.code != 0 {
		t.Fatalf("import exit = %d: %+v", got.code, got.env)
	}
	list := runCLI(t, dir, "pool", "list")
	data, _ := list.env.Data.(map[string]any)
	records, _ := data["records"].([]any)
	if len(records) != 2 {
		t.Fatalf("pool records = %+v", data["records"])
	}
	var id string
	for _, item := range records {
		row, _ := item.(map[string]any)
		if row["status"] == "unsupported" {
			id, _ = row["id"].(string)
		}
	}
	if id == "" {
		t.Fatalf("no unsupported record in pool: %+v", records)
	}
	removed := runCLI(t, dir, "pool", "remove", id)
	if removed.code != 0 || !removed.env.OK {
		t.Fatalf("pool remove exit = %d: %+v", removed.code, removed.env)
	}
	list = runCLI(t, dir, "pool", "list")
	data, _ = list.env.Data.(map[string]any)
	counts, _ := data["counts"].(map[string]any)
	if num, _ := counts["total"].(float64); num != 1 {
		t.Fatalf("pool total after remove = %v", counts["total"])
	}
}

func TestPoolRerankJSONShape(t *testing.T) {
	dir := startInProcess(t)
	for _, link := range []string{
		"trojan://pass-one@example.com:443?security=tls&type=raw#one",
		"trojan://pass-two@example.net:443?security=tls&type=raw#two",
	} {
		if got := runCLI(t, dir, "pool", "add", link); got.code != 0 {
			t.Fatalf("pool add exit = %d: %+v", got.code, got.env)
		}
	}
	got := runCLI(t, dir, "pool", "rerank")
	if got.code != 0 || !got.env.OK || got.env.Command != "pool rerank" {
		t.Fatalf("rerank = %+v (code %d, stderr %s)", got.env, got.code, got.stderr)
	}
	data, _ := got.env.Data.(map[string]any)
	if _, ok := data["selected_before"].(string); !ok {
		t.Fatalf("selected_before missing/not a string: %+v", data)
	}
	after, ok := data["selected_after"].(string)
	if !ok || after == "" {
		t.Fatalf("selected_after = %v", data["selected_after"])
	}
	if _, ok := data["skipped"].([]any); !ok {
		t.Fatalf("skipped is not an array: %+v", data["skipped"])
	}
	if _, ok := data["policy_version"].(float64); !ok {
		t.Fatalf("policy_version = %v", data["policy_version"])
	}
}

func TestCLIProposalsListWithoutServiceExplainsMemoryOnlyHistory(t *testing.T) {
	dir := t.TempDir()
	got := runCLI(t, dir, "proposals", "list")
	if got.code != 3 {
		t.Fatalf("exit = %d, want 3 (%+v)", got.code, got.env)
	}
	if got.env.Error == nil || got.env.Error.Kind != contract.Stopped {
		t.Fatalf("envelope = %+v", got.env)
	}
	msg := got.env.Error.Message
	if !strings.Contains(msg, "memory") || !strings.Contains(msg, "lost on restart") {
		t.Fatalf("proposals list does not explain that history is memory-only: %q", msg)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("offline proposals list created files: %v", entries)
	}
}

// startNetworkService builds a service whose real listeners and probes run, so
// a diagnose command produces genuine evidence. Ports are free loopback ports;
// the engine starts with an empty pool, which is enough for direct probes.
func startNetworkService(t *testing.T, dir string) (*control.Service, *control.Server) {
	t.Helper()
	paths, err := store.ResolvePathsFor(dir, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.HTTPListen, cfg.SOCKSListen = freeLoopbackPair(t)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := control.NewService(context.Background(), control.Options{Store: st, Paths: paths, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.StartNetwork(context.Background()); err != nil {
		t.Fatalf("start network: %v", err)
	}
	srv, err := control.NewServer(control.ServerOptions{Path: paths.Socket, Handler: svc, AllowStale: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return svc, srv
}

func freeLoopback(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// freeLoopbackPair holds both listeners at once so the two chosen addresses
// cannot be the same port.
func freeLoopbackPair(t *testing.T) (string, string) {
	t.Helper()
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	return first.Addr().String(), second.Addr().String()
}

func closedLoopbackPort(t *testing.T) uint16 {
	t.Helper()
	addr := freeLoopback(t)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(n)
}

func TestCLIProposalIsMemoryOnlyAcrossCallsAndDiesAtRestart(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rlcli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	svc, srv := startNetworkService(t, dir)
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(closedLoopbackPort(t))))

	diag := runCLI(t, dir, "diagnose", target)
	if diag.code != 0 || !diag.env.OK {
		t.Fatalf("diagnose exit = %d: %+v", diag.code, diag.env)
	}
	data, _ := diag.env.Data.(map[string]any)
	proposalID, _ := data["proposal_id"].(string)
	if proposalID == "" {
		t.Fatalf("diagnose did not create a proposal: %+v", data)
	}

	for i := 0; i < 2; i++ {
		list := runCLI(t, dir, "proposals", "list")
		if list.code != 0 || !list.env.OK {
			t.Fatalf("proposals list #%d exit = %d: %+v", i+1, list.code, list.env)
		}
		raw, _ := json.Marshal(list.env.Data)
		if !strings.Contains(string(raw), proposalID) {
			t.Fatalf("proposal missing from call #%d: %s", i+1, raw)
		}
	}

	// The destination was in the reply above, but its port must not be in any
	// stored file (the listener and socket addresses use unrelated ports).
	_, port, _ := net.SplitHostPort(target)
	needles := []string{":" + port, `"port":` + port}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, needle := range needles {
			if strings.Contains(string(raw), needle) {
				t.Errorf("%s contains the diagnosed destination port", path)
			}
		}
		return nil
	})

	_ = srv.Close()
	_ = svc.Close(context.Background())

	// A restart must not resurrect the proposal from any file.
	svc2, srv2 := startNetworkService(t, dir)
	defer func() {
		_ = srv2.Close()
		_ = svc2.Close(context.Background())
	}()
	list := runCLI(t, dir, "proposals", "list")
	if list.code != 0 || !list.env.OK {
		t.Fatalf("post-restart proposals list = %+v", list.env)
	}
	data, _ = list.env.Data.(map[string]any)
	if proposals, _ := data["proposals"].([]any); len(proposals) != 0 {
		t.Fatalf("restart resurrected proposals: %+v", proposals)
	}

}

func TestPrintStatusReportsSelectionHonestly(t *testing.T) {
	cases := []struct {
		name      string
		data      map[string]any
		want      string
		forbidden string
	}{
		{
			name:      "a service that omits the key is not reported as auto",
			data:      map[string]any{"mode": "targeted", "policy_version": 1},
			want:      "exit: unknown (the running service does not report selected_exit",
			forbidden: "exit: auto",
		},
		{
			name: "empty value is automatic ranking",
			data: map[string]any{"mode": "targeted", "policy_version": 1, "selected_exit": ""},
			want: "exit: auto",
		},
		{
			name: "manual pin is printed verbatim",
			data: map[string]any{"mode": "targeted", "policy_version": 1, "selected_exit": "x-0123456789abcdef"},
			want: "exit: x-0123456789abcdef",
		},
		{
			name:      "control bytes in a restored state cannot reach the terminal",
			data:      map[string]any{"mode": "targeted", "policy_version": 1, "selected_exit": "x-\x1b[31mred"},
			want:      "exit: x-red",
			forbidden: "\x1b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			c := &cli{stdout: &out}
			c.printStatus(tc.data)
			lines := strings.Split(out.String(), "\n")
			if len(lines) < 2 || !strings.Contains(lines[1], tc.want) {
				t.Fatalf("status output = %q, want line 2 to contain %q", out.String(), tc.want)
			}
			if tc.forbidden != "" && strings.Contains(out.String(), tc.forbidden) {
				t.Fatalf("status output = %q must not contain %q", out.String(), tc.forbidden)
			}
		})
	}
}
