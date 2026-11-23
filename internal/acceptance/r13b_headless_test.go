package acceptance

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// R13-B phase 1: --headless is the explicit foreground mode and the canonical
// instance directory is derived from the selected data dir, not from
// XDG_RUNTIME_DIR (docs/contracts.md §7.1, §7.5).

func holdLegacyLock(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	return f
}

func cloneEnvWithRuntime(t *testing.T, env *testEnv) *testEnv {
	t.Helper()
	clone := *env
	clone.run = filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(clone.run, 0o700); err != nil {
		t.Fatal(err)
	}
	clone.vars = nil
	for _, entry := range env.vars {
		if strings.HasPrefix(entry, "XDG_RUNTIME_DIR=") {
			clone.vars = append(clone.vars, "XDG_RUNTIME_DIR="+clone.run)
			continue
		}
		clone.vars = append(clone.vars, entry)
	}
	return &clone
}

func TestR13BHeadlessInstanceDirAndReady(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "headless")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	svc := startService(t, env, dir, serviceConfig{})
	status := svc.mustOK("status").mapData(t)
	if status["ready"] == true && status["listeners_open"] == true {
		r.pass("R13-B", "`run --headless` answers status with ready=true and listeners_open=true")
	} else {
		r.fail("R13-B", "headless status: ready=%v listeners_open=%v", status["ready"], status["listeners_open"])
	}

	instanceDir := filepath.Join(dir, "run")
	checks := []struct {
		path string
		mode os.FileMode
		dir  bool
	}{
		{instanceDir, 0o700, true},
		{filepath.Join(instanceDir, "state.lock"), 0o600, false},
		{filepath.Join(instanceDir, "control.sock"), 0o600, false},
		{filepath.Join(dir, "state.lock"), 0o600, false},
	}
	ok := true
	for _, check := range checks {
		info, err := os.Stat(check.path)
		if err != nil {
			r.fail("R13-B", "canonical path %s: %v", check.path, err)
			ok = false
			continue
		}
		if info.Mode().Perm() != check.mode || info.IsDir() != check.dir {
			r.fail("R13-B", "%s mode=%04o dir=%v, want %04o/%v", check.path, info.Mode().Perm(), info.IsDir(), check.mode, check.dir)
			ok = false
		}
	}
	if info, err := os.Stat(filepath.Join(instanceDir, "control.sock")); err == nil && info.Mode()&os.ModeSocket == 0 {
		r.fail("R13-B", "management path in the instance dir is not a socket")
		ok = false
	}
	if ok {
		r.pass("R13-B", "InstanceDir is <data-dir>/run with a 0700 directory, 0600 state.lock/socket and the R12 legacy lock alongside")
	}
	if _, err := os.Stat(filepath.Join(env.run, "routelatch")); !os.IsNotExist(err) {
		r.fail("R13-B", "a --data-dir instance wrote into XDG_RUNTIME_DIR: %v", err)
	} else {
		r.pass("R13-B", "a --data-dir instance takes no global XDG_RUNTIME_DIR lock")
	}

	code, out := startExpectingFailure(t, env, dir)
	if code == 4 {
		r.pass("R13-B", "a second run on the same data dir exits 4")
	} else {
		r.fail("R13-B", "second run exit=%d output=%s", code, out)
	}

	svc.signal(syscall.SIGTERM)
	if err := svc.waitExit(20 * time.Second); err != nil {
		r.fail("R13-B", "SIGTERM exit: %v", err)
	} else {
		r.pass("R13-B", "`run --headless` exits 0 on SIGTERM")
	}
	restarted := startService(t, env, dir, svc.cfg)
	t.Cleanup(func() { _ = restarted.stop() })
	if res := restarted.mustOK("status").mapData(t); res["ready"] == true {
		r.pass("R13-B", "the same data dir restarts after a clean headless stop (lock reuse)")
	} else {
		r.fail("R13-B", "restarted status: ready=%v", res["ready"])
	}
	restarted.signal(os.Interrupt)
	if err := restarted.waitExit(20 * time.Second); err != nil {
		r.fail("R13-B", "SIGINT exit: %v", err)
	} else {
		r.pass("R13-B", "`run --headless` exits 0 on SIGINT")
	}
}

func TestR13BPlainRunMatchesHeadless(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "plain")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	// Plain run is still the foreground engine in this phase; --headless is the
	// explicit spelling. Both must produce the same ready state and stop clean.
	plain := startServiceWithArgs(t, env, dir, serviceConfig{})
	if res := plain.mustOK("status").mapData(t); res["ready"] == true && res["listeners_open"] == true {
		r.pass("R13-B", "plain `run` still preserves the foreground behavior")
	} else {
		r.fail("R13-B", "plain run status: ready=%v listeners_open=%v", res["ready"], res["listeners_open"])
	}
	if err := plain.stop(); err != nil {
		r.fail("R13-B", "plain run stop: %v", err)
	}
	headless := startServiceWithArgs(t, env, dir, plain.cfg, "--headless")
	t.Cleanup(func() { _ = headless.stop() })
	if res := headless.mustOK("status").mapData(t); res["ready"] == true && res["listeners_open"] == true {
		r.pass("R13-B", "`run --headless` matches plain run for the same data dir")
	} else {
		r.fail("R13-B", "headless status: ready=%v listeners_open=%v", res["ready"], res["listeners_open"])
	}
}

func TestR13BTwoDataDirsAndLegacyScope(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dirA := filepath.Join(env.home, "a")
	dirB := filepath.Join(env.home, "b")
	for _, dir := range []string{dirA, dirB} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("data dir: %v", err)
		}
	}
	a := startService(t, env, dirA, serviceConfig{})
	t.Cleanup(func() { _ = a.stop() })
	b := startService(t, env, dirB, serviceConfig{})
	t.Cleanup(func() { _ = b.stop() })
	readyA := a.mustOK("status").mapData(t)["ready"] == true
	readyB := b.mustOK("status").mapData(t)["ready"] == true
	if readyA && readyB && a.env.run == b.env.run {
		r.pass("R13-B", "two --data-dir instances run at the same time with independent canonical locks")
	} else {
		r.fail("R13-B", "two data dirs: readyA=%v readyB=%v", readyA, readyB)
	}

	// A legacy lock in one data dir must not block a different data dir: R-19
	// says the migration set is scoped to the selected root, not the user.
	heldDir := filepath.Join(env.home, "held")
	otherDir := filepath.Join(env.home, "other")
	for _, dir := range []string{heldDir, otherDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("data dir: %v", err)
		}
	}
	held := holdLegacyLock(t, filepath.Join(heldDir, "state.lock"))
	defer held.Close()
	if code, out := startExpectingFailure(t, env, heldDir); code != 4 {
		r.fail("R13-B", "held legacy lock: exit=%d output=%s", code, out)
	} else {
		r.pass("R13-B", "a held legacy lock in the same data dir exits 4 (stopped mode holds it too)")
	}
	other := startService(t, env, otherDir, serviceConfig{})
	t.Cleanup(func() { _ = other.stop() })
	if other.mustOK("status").mapData(t)["ready"] == true {
		r.pass("R13-B", "a legacy lock in another data dir does not block this instance")
	} else {
		r.fail("R13-B", "instance beside a foreign legacy lock is not ready")
	}
}

func TestR13BStoppedModeHasNoListener(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	dir := filepath.Join(env.home, "stopped")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	svc := startService(t, env, dir, serviceConfig{})
	t.Cleanup(func() { _ = svc.stop() })
	svc.mustOK("mode", "stopped")
	status := svc.mustOK("status").mapData(t)
	if status["ready"] == true && status["listeners_open"] == false {
		r.pass("R13-B", "stopped mode reports ready=true with listeners_open=false")
	} else {
		r.fail("R13-B", "stopped status: ready=%v listeners_open=%v", status["ready"], status["listeners_open"])
	}
	if listenerRefuses(t, svc.httpAddr) && listenerRefuses(t, svc.socksAddr) {
		r.pass("R13-B", "stopped mode leaves both traffic listeners closed")
	} else {
		r.fail("R13-B", "stopped mode still answers on a traffic listener")
	}
	if err := svc.stop(); err != nil {
		r.fail("R13-B", "stopped shutdown: %v", err)
	}
	restarted := startService(t, env, dir, svc.cfg)
	t.Cleanup(func() { _ = restarted.stop() })
	status = restarted.mustOK("status").mapData(t)
	if status["mode"] == "stopped" && status["ready"] == true && status["listeners_open"] == false {
		r.pass("R13-B", "a persisted stopped mode starts ready without listeners")
	} else {
		r.fail("R13-B", "restarted stopped status: %+v", status)
	}
}

func TestR13BLegacyLockMigrationWindow(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	mkdir := func(name string) string {
		dir := filepath.Join(env.home, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("data dir: %v", err)
		}
		return dir
	}

	symlinkDir := mkdir("symlink")
	target := filepath.Join(symlinkDir, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(symlinkDir, "state.lock")); err != nil {
		t.Fatal(err)
	}
	code, out := startExpectingFailure(t, env, symlinkDir)
	raw, _ := os.ReadFile(target)
	if code == 6 && string(raw) == "untouched" {
		r.pass("R13-B", "a symlinked legacy lock is rejected with exit 6 and never followed")
	} else {
		r.fail("R13-B", "symlink legacy lock: exit=%d target=%q output=%s", code, raw, out)
	}

	nonRegular := mkdir("nonregular")
	if err := os.Mkdir(filepath.Join(nonRegular, "state.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code, out := startExpectingFailure(t, env, nonRegular); code == 6 {
		r.pass("R13-B", "a non-regular legacy lock is rejected with exit 6")
	} else {
		r.fail("R13-B", "non-regular legacy lock: exit=%d output=%s", code, out)
	}

	if os.Geteuid() != 0 {
		unverifiable := mkdir("unverifiable")
		lock := filepath.Join(unverifiable, "state.lock")
		if err := os.WriteFile(lock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(lock, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(lock, 0o600) })
		if code, out := startExpectingFailure(t, env, unverifiable); code == 6 {
			r.pass("R13-B", "an unopenable legacy lock with no legacy socket exits 6 instead of starting")
		} else {
			r.fail("R13-B", "unverifiable legacy lock: exit=%d output=%s", code, out)
		}

		answered := mkdir("answered")
		answeredLock := filepath.Join(answered, "state.lock")
		if err := os.WriteFile(answeredLock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(answeredLock, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(answeredLock, 0o600) })
		if err := os.MkdirAll(filepath.Join(answered, "run"), 0o700); err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("unix", filepath.Join(answered, "run", "control.sock"))
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		if code, out := startExpectingFailure(t, env, answered); code == 4 {
			r.pass("R13-B", "an unopenable legacy lock whose legacy socket answers exits 4")
		} else {
			r.fail("R13-B", "answered legacy socket: exit=%d output=%s", code, out)
		}
	} else {
		r.note("R13-B", "unverifiable legacy-lock cases are skipped as uid 0, which can open mode-000 files")
	}

	r.note("R13-B", "a legacy lock owned by another uid is not executable here without root; the lstat owner branch has a root-gated unit test and the non-regular/symlink branches cover rejection without following")
}

func TestR13BDifferingRuntimeOneLock(t *testing.T) {
	r := report{t: t}
	envA := newTestEnv(t)
	dir := filepath.Join(envA.home, "runtime-independent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	a := startService(t, envA, dir, serviceConfig{})
	socket := filepath.Join(dir, "run", "control.sock")
	if info, err := os.Stat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		r.fail("R13-B", "canonical socket missing: %v", err)
	}
	envB := cloneEnvWithRuntime(t, envA)
	if code, out := startExpectingFailure(t, envB, dir); code == 4 {
		r.pass("R13-B", "an SSH session with a different XDG_RUNTIME_DIR is refused by the same live lock (exit 4)")
	} else {
		r.fail("R13-B", "second runtime while the first is live: exit=%d output=%s", code, out)
	}
	if err := a.stop(); err != nil {
		r.fail("R13-B", "first runtime stop: %v", err)
	}
	b := startService(t, envB, dir, a.cfg)
	t.Cleanup(func() { _ = b.stop() })
	if res := b.mustOK("status").mapData(t); res["ready"] == true {
		r.pass("R13-B", "a desktop and an SSH XDG_RUNTIME_DIR reach the same canonical lock and socket")
	} else {
		r.fail("R13-B", "second runtime status: ready=%v", res["ready"])
	}
	for _, runtime := range []string{envA.run, envB.run} {
		if _, err := os.Stat(filepath.Join(runtime, "routelatch")); !os.IsNotExist(err) {
			r.fail("R13-B", "runtime %s was used for coordination: %v", runtime, err)
		}
	}
}

func legacyLockHeld(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// TestR13BDefaultAndDataDirCoexist proves the default instance and a --data-dir
// instance are independent. It is skipped when this host already runs a
// default R12 engine, because the migration window must (correctly) refuse a
// second default instance and the live service must not be touched.
func TestR13BDefaultAndDataDirCoexist(t *testing.T) {
	r := report{t: t}
	env := newTestEnv(t)
	runUserLock := filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "routelatch", "state.lock")
	if legacyLockHeld(runUserLock) {
		r.note("R13-B", "default+data-dir coexistence not executed: a live R12 default engine holds %s and must not be stopped; the same invocation is expected to exit 4 by design", runUserLock)
		return
	}
	// The product creates this runtime lock by design. Remove the directory
	// afterwards only when it did not exist before the test, so a real
	// installation's runtime state is never deleted.
	runUserDir := filepath.Dir(runUserLock)
	if _, err := os.Stat(runUserDir); errors.Is(err, os.ErrNotExist) {
		t.Cleanup(func() { _ = os.RemoveAll(runUserDir) })
	}

	cfgDir := filepath.Join(env.config, "routelatch")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	defaultCfg := serviceConfig{
		HTTPListen:  fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		SOCKSListen: fmt.Sprintf("127.0.0.1:%d", freePort(t)),
	}
	writeConfig(t, cfgDir, defaultCfg)
	cmd := exec.Command(productBin, "run", "--headless")
	cmd.Env = env.vars
	cmd.Dir = env.tmp
	var log strings.Builder
	cmd.Stdout = &log
	cmd.Stderr = &log
	if err := cmd.Start(); err != nil {
		t.Fatalf("start default run: %v", err)
	}
	defaultDone := make(chan struct{})
	var defaultErr error
	go func() {
		defaultErr = cmd.Wait()
		close(defaultDone)
	}()
	t.Cleanup(func() {
		select {
		case <-defaultDone:
			return
		default:
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-defaultDone:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			<-defaultDone
		}
	})

	socket := filepath.Join(env.state, "routelatch", "run", "control.sock")
	if !waitFor(25*time.Second, func() bool {
		info, err := os.Stat(socket)
		return err == nil && info.Mode()&os.ModeSocket != 0
	}) {
		select {
		case <-defaultDone:
			if legacyLockHeld(runUserLock) {
				r.note("R13-B", "default run exited under the live R12 legacy lock; coexistence not executed")
				return
			}
			r.fail("R13-B", "default run exited before its socket appeared: %v (%s)", defaultErr, log.String())
			return
		default:
		}
		r.fail("R13-B", "default run never bound %s: %s", socket, log.String())
		return
	}
	status := exec.Command(productBin, "--json", "status")
	status.Env = env.vars
	status.Dir = env.tmp
	out, err := status.Output()
	if err != nil || !strings.Contains(string(out), `"ready":true`) {
		r.fail("R13-B", "default status: err=%v output=%s", err, out)
	} else {
		r.pass("R13-B", "the default instance is ready on <StateDir>/run with a temp XDG_RUNTIME_DIR")
	}

	dataDir := filepath.Join(env.home, "beside-default")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	beside := startService(t, env, dataDir, serviceConfig{})
	t.Cleanup(func() { _ = beside.stop() })
	if beside.mustOK("status").mapData(t)["ready"] == true {
		r.pass("R13-B", "a --data-dir instance runs beside the default instance")
	} else {
		r.fail("R13-B", "the --data-dir instance beside the default is not ready")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	<-defaultDone
	if defaultErr != nil {
		r.fail("R13-B", "default run SIGTERM exit: %v", defaultErr)
	} else {
		r.pass("R13-B", "the default headless instance exits 0 on SIGTERM")
	}
}

func TestR13BHeadlessFlagParsing(t *testing.T) {
	r := report{t: t}
	dir := t.TempDir()
	run := func(args ...string) (int, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, productBin, args...)
		cmd.Env = append(os.Environ(), "LANG=C")
		var out strings.Builder
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		code := 0
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("run %v: %v", args, err)
			}
			code = exitErr.ExitCode()
		}
		return code, out.String()
	}
	for _, args := range [][]string{
		{"--data-dir", dir, "run", "--unknown"},
		{"--data-dir", dir, "run", "extra"},
	} {
		if code, _ := run(args...); code == 2 {
			r.pass("R13-B", "%v is a usage error (exit 2)", args[2:])
		} else {
			r.fail("R13-B", "%v exit=%d, want 2", args[2:], code)
		}
	}
}
