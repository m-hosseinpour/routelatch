package control

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"routelatch/internal/contract"
)

func TestLogFileDocumentedPathAndPermissions(t *testing.T) {
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	if svc.logErr != nil {
		t.Fatalf("logger failed: %v", svc.logErr)
	}
	logPath := filepath.Join(paths.LogDir, "routelatch.log")
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("documented log file missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode = %04o, want 0600", info.Mode().Perm())
	}
	dir, err := os.Stat(paths.LogDir)
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Fatalf("log dir mode = %04o, want 0700", dir.Mode().Perm())
	}
	if info.Size() == 0 {
		t.Fatal("log file is empty after service initialization")
	}
	_ = svc.Close(context.Background())
}

func TestLogRotationKeepsThreeRotatedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routelatch.log")
	logger, err := newServiceLoggerWithLimit(path, "debug", io.Discard, 200, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	for i := 0; i < 40; i++ {
		logger.Logf(levelInfo, "test.rotation", "message %d with enough bytes to force rotation", i)
	}
	for i := 1; i <= 3; i++ {
		if _, err := os.Stat(fmt.Sprintf("%s.%d", path, i)); err != nil {
			t.Fatalf("rotated file %d missing: %v", i, err)
		}
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Fatalf("retained more than three rotated files: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 200 {
		t.Fatalf("active log exceeds the rotation limit: %d", info.Size())
	}
}

func TestLogLevelHonoursConfiguredLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routelatch.log")
	logger, err := newServiceLoggerWithLimit(path, "warning", io.Discard, 4096, 3)
	if err != nil {
		t.Fatal(err)
	}
	logger.Logf(levelInfo, "test", "info should be suppressed")
	logger.Logf(levelWarning, "test", "warning should be written")
	logger.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "info should be suppressed") {
		t.Fatal("info line was written at warning level")
	}
	if !strings.Contains(string(raw), "warning should be written") {
		t.Fatal("warning line was not written")
	}
}

func TestSecretFixtureNeverReachesLog(t *testing.T) {
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	secret := "LOG-SECRET-FIXTURE-41c9"
	link := "trojan://" + secret + "@example.com:443?security=tls&type=raw&sni=example.com#node"
	if _, err := svc.Handle(context.Background(), Request{Op: OpPoolAdd, Input: link}); err != nil {
		t.Fatalf("pool add: %v", err)
	}
	if _, err := svc.setMode(context.Background(), string(contract.ModeDirectAll)); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(paths.LogDir, "routelatch.log")
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "trojan://") {
		t.Fatalf("log leaked a credential or share link: %q", raw)
	}
}

func TestDoctorReportsLogPathAndUnwritableLogIsNonFatal(t *testing.T) {
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	data, err := svc.doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range data.Checks {
		if check.Name == "log_file" {
			found = true
			if check.Status != "ok" || !strings.Contains(check.Detail, filepath.Join(paths.LogDir, "routelatch.log")) {
				t.Fatalf("log check = %+v", check)
			}
		}
	}
	if !found {
		t.Fatal("doctor did not report log_file")
	}
	_ = svc.Close(context.Background())

	// A log path that cannot be opened must leave the service usable.
	dir, err := os.MkdirTemp("/tmp", "rllogbad")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	badPaths, err := resolveTestPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badPaths.LogDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	badStore, err := openTestStore(badPaths)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := NewService(context.Background(), Options{Store: badStore, Paths: badPaths, LogOutput: io.Discard})
	if err != nil {
		t.Fatalf("unwritable log made the service fail: %v", err)
	}
	defer bad.Close(context.Background())
	if bad.logErr == nil {
		t.Fatal("expected a log error")
	}
	if _, err := bad.status(context.Background()); err != nil {
		t.Fatalf("service unusable with a broken log: %v", err)
	}
	checks, err := bad.doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	warned := false
	for _, check := range checks.Checks {
		if check.Name == "log_file" && check.Status == "warn" {
			warned = true
		}
	}
	if !warned {
		t.Fatal("doctor did not warn about the unwritable log")
	}
}

func TestNonLoopbackListenerIsUnsupported(t *testing.T) {
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	_, err := svc.Handle(context.Background(), Request{Op: OpConfigSet, Key: "http_listen", Value: "0.0.0.0:8317"})
	var failure *contract.Failure
	if err == nil || !errorsAs(err, &failure) || failure.Kind != contract.Unsupported {
		t.Fatalf("config set err = %v, want unsupported", err)
	}
	if got := contract.ExitCodeFor[failure.Kind]; got != contract.ExitUnsupported {
		t.Fatalf("exit code = %d, want %d", got, contract.ExitUnsupported)
	}
	if !strings.Contains(failure.Message, "unsupported") {
		t.Fatalf("message is not actionable: %q", failure.Message)
	}
	if runes := len([]rune(failure.Message)); runes > contract.MaxNodeNameRunes {
		t.Fatalf("the message is %d runes; EnvelopeFor would truncate it at %d: %q", runes, contract.MaxNodeNameRunes, failure.Message)
	}
	// Runtime validation reaches the same kind even if the file was edited by
	// something other than the CLI.
	if _, err := st.MutateConfig(context.Background(), func(cfg *contract.Config) error {
		cfg.HTTPListen = "0.0.0.0:8317"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc2, err := NewService(context.Background(), Options{Store: st, Paths: paths, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	err = svc2.StartNetwork(context.Background())
	if err == nil || !errorsAs(err, &failure) || failure.Kind != contract.Unsupported {
		t.Fatalf("StartNetwork err = %v, want unsupported", err)
	}
}
