package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// tmp environment and product process lifecycle
// ---------------------------------------------------------------------------

// testEnv is the isolated environment every child process receives. The
// product is reached by absolute path, so PATH is only needed for the binary's
// own `exec --` children (not used here).
type testEnv struct {
	home   string
	config string
	state  string
	run    string
	tmp    string
	vars   []string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	e := &testEnv{
		home:   filepath.Join(root, "home"),
		config: filepath.Join(root, "xdg-config"),
		state:  filepath.Join(root, "xdg-state"),
		run:    filepath.Join(root, "xdg-run"),
		tmp:    filepath.Join(root, "tmp"),
	}
	for _, dir := range []string{e.home, e.config, e.state, e.run, e.tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create env dir: %v", err)
		}
	}
	e.vars = []string{
		"HOME=" + e.home,
		"XDG_CONFIG_HOME=" + e.config,
		"XDG_STATE_HOME=" + e.state,
		"XDG_RUNTIME_DIR=" + e.run,
		"TMPDIR=" + e.tmp,
		"PATH=" + os.Getenv("PATH"),
		"LANG=C",
	}
	return e
}

type serviceConfig struct {
	HTTPListen  string
	SOCKSListen string
	Diagnostics map[string]any
	Probes      map[string]any
}

type service struct {
	t    *testing.T
	dir  string
	env  *testEnv
	cfg  serviceConfig
	proc *exec.Cmd

	mu      sync.Mutex
	stderr  strings.Builder
	stdout  strings.Builder
	outputs []string

	done   chan error
	exit   error
	exitAt time.Time

	stopOnce sync.Once
	stopErr  error

	httpAddr  string
	socksAddr string
}

func writeConfig(t *testing.T, dir string, cfg serviceConfig) string {
	t.Helper()
	doc := map[string]any{
		"schema_version": 1,
		"http_listen":    cfg.HTTPListen,
		"socks_listen":   cfg.SOCKSListen,
		"log_level":      "info",
		"diagnostics": map[string]any{
			"enabled": true, "queue_limit": 32, "max_concurrent": 1, "retention_days": 30,
		},
		"probes": map[string]any{
			"targets": []string{}, "timeout_ms": 20000, "cooldown_base": 60, "cooldown_max": 900,
		},
	}
	if cfg.Diagnostics != nil {
		doc["diagnostics"] = cfg.Diagnostics
	}
	if cfg.Probes != nil {
		doc["probes"] = cfg.Probes
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// startService starts `routelatch run` in its own process and waits until the
// management socket answers `status`. It never talks to the service in-process.
func startService(t *testing.T, env *testEnv, dir string, cfg serviceConfig) *service {
	t.Helper()
	if cfg.HTTPListen == "" {
		cfg.HTTPListen = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	}
	if cfg.SOCKSListen == "" {
		cfg.SOCKSListen = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	}
	writeConfig(t, dir, cfg)

	s := &service{t: t, dir: dir, env: env, cfg: cfg, done: make(chan error, 1),
		httpAddr: cfg.HTTPListen, socksAddr: cfg.SOCKSListen}

	cmd := exec.Command(productBin, "--data-dir", dir, "run")
	cmd.Env = env.vars
	cmd.Dir = env.tmp
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("run stderr pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("run stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start run: %v", err)
	}
	s.proc = cmd
	t.Cleanup(func() { _ = s.stop() })
	go func() {
		_, _ = io.Copy(&lockedWriter{s: s, target: &s.stderr}, stderr)
	}()
	go func() {
		_, _ = io.Copy(&lockedWriter{s: s, target: &s.stdout}, stdout)
	}()
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		s.exit = err
		s.exitAt = time.Now()
		s.mu.Unlock()
		s.done <- err
	}()

	deadline := time.Now().Add(25 * time.Second)
	for {
		res := s.cli("status")
		if res.code == 0 && res.env.OK {
			return s
		}
		if s.Exited() {
			t.Fatalf("run exited during startup (%d): stderr=%s\nstdout=%s\nerr=%v",
				res.code, s.Stderr(), s.Stdout(), s.ExitErr())
		}
		if time.Now().After(deadline) {
			t.Fatalf("service did not answer status within 25s (%d): %s", res.code, s.Stderr())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// startExpectingFailure starts `routelatch run` and returns its exit code and
// captured stderr once it terminates. Used for corrupt state, a second
// instance and port collisions, where run must never become ready.
func startExpectingFailure(t *testing.T, env *testEnv, dir string) (code int, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, productBin, "--json", "--data-dir", dir, "run")
	cmd.Env = env.vars
	cmd.Dir = env.tmp
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code = 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("run: %v", err)
		}
	}
	return code, out.String() + errb.String()
}

type lockedWriter struct {
	s      *service
	target *strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	return w.target.Write(p)
}

func (s *service) Stderr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stderr.String()
}

func (s *service) Stdout() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stdout.String()
}

func (s *service) Exited() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitAt != time.Time{}
}

func (s *service) ExitErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exit
}

// waitExit waits for the process to terminate and returns its error.
func (s *service) waitExit(d time.Duration) error {
	select {
	case err := <-s.done:
		return err
	case <-time.After(d):
		s.t.Fatalf("run did not exit within %s", d)
		return nil
	}
}

func (s *service) signal(sig os.Signal) {
	if s.proc == nil || s.proc.Process == nil {
		return
	}
	_ = s.proc.Process.Signal(sig)
}

// stop shuts the service down and records its exit for later assertions. It is
// idempotent so a test that stops the service explicitly can still register a
// cleanup.
func (s *service) stop() error {
	if s == nil || s.proc == nil {
		return nil
	}
	s.stopOnce.Do(func() {
		s.signal(os.Interrupt)
		s.stopErr = s.waitExit(20 * time.Second)
	})
	return s.stopErr
}

// fdCount is the number of open descriptors in the product process.
func (s *service) fdCount() (int, error) {
	if s.proc == nil || s.proc.Process == nil {
		return 0, fmt.Errorf("no process")
	}
	ents, err := os.ReadDir("/proc/" + strconv.Itoa(s.proc.Process.Pid) + "/fd")
	if err != nil {
		return 0, err
	}
	return len(ents), nil
}

// ---------------------------------------------------------------------------
// CLI surface
// ---------------------------------------------------------------------------

type envelope struct {
	SchemaVersion int             `json:"schema_version"`
	OK            bool            `json:"ok"`
	Command       string          `json:"command"`
	Data          json.RawMessage `json:"data"`
	Error         *envelopeError  `json:"error"`
}

type envelopeError struct {
	Kind    string         `json:"kind"`
	Path    string         `json:"path"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail"`
}

type cliResult struct {
	code   int
	stdout string
	stderr string
	env    envelope
}

func (s *service) cli(args ...string) cliResult {
	s.t.Helper()
	full := append([]string{"--json", "--data-dir", s.dir}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, productBin, full...)
	cmd.Env = s.env.vars
	cmd.Dir = s.env.tmp
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	res := cliResult{stdout: out.String(), stderr: errb.String()}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.code = exitErr.ExitCode()
		} else {
			s.t.Fatalf("cli %v: %v", args, err)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(res.stdout), "\n") {
		if line == "" {
			continue
		}
		var env envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue
		}
		res.env = env
	}
	s.mu.Lock()
	s.outputs = append(s.outputs, res.stdout, res.stderr)
	s.mu.Unlock()
	return res
}

// mustOK fails the test when the command did not succeed with a JSON envelope.
func (s *service) mustOK(args ...string) cliResult {
	s.t.Helper()
	res := s.cli(args...)
	if res.code != 0 || !res.env.OK {
		errText := ""
		if res.env.Error != nil {
			errText = fmt.Sprintf("kind=%s path=%s message=%q", res.env.Error.Kind, res.env.Error.Path, res.env.Error.Message)
		}
		s.t.Fatalf("command %q failed: exit=%d %s stdout=%s stderr=%s", strings.Join(args, " "), res.code, errText, res.stdout, res.stderr)
	}
	return res
}

// mustFail asserts the documented exit code and returns the typed error.
func (s *service) mustFail(code int, args ...string) cliResult {
	s.t.Helper()
	res := s.cli(args...)
	if res.code != code {
		s.t.Fatalf("command %q: exit=%d, want %d; envelope=%+v stderr=%s", strings.Join(args, " "), res.code, code, res.env, res.stderr)
	}
	return res
}

func (r cliResult) mapData(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if len(r.env.Data) > 0 {
		if err := json.Unmarshal(r.env.Data, &m); err != nil {
			t.Fatalf("decode data for %q: %v (%s)", r.env.Command, err, string(r.env.Data))
		}
	}
	return m
}

func (r cliResult) list(t *testing.T, key string) []map[string]any {
	t.Helper()
	m := r.mapData(t)
	raw, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if obj, ok := item.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// ---------------------------------------------------------------------------
// small utilities
// ---------------------------------------------------------------------------

type safeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}

// waitFor polls cond until it is true or the deadline expires.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// refusableAddr is a loopback port with no listener. A direct dial there
// returns ECONNREFUSED without leaving the machine.
func refusableAddr(t *testing.T) uint16 {
	t.Helper()
	port := freePort(t)
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("refusable: %v", err)
	}
	_ = l.Close()
	return port
}

// readLineWithin reads one newline-terminated line with a deadline.
func readLineWithin(t *testing.T, br *bufio.Reader, d time.Duration) (string, error) {
	t.Helper()
	type res struct {
		line string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := br.ReadString('\n')
		ch <- res{line, err}
	}()
	select {
	case v := <-ch:
		return v.line, v.err
	case <-time.After(d):
		return "", fmt.Errorf("read deadline exceeded")
	}
}
