package platform

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"routelatch/internal/contract"
)

type fakeHealth struct {
	health Health
	err    error
	calls  int
}

func (f *fakeHealth) Check(context.Context) (Health, error) {
	f.calls++
	return f.health, f.err
}

var testTargets = Targets{HTTPListen: "127.0.0.1:8317", SOCKSListen: "127.0.0.1:8318"}

func TestBuildProxyEnvironmentReplacesInheritedProxySettings(t *testing.T) {
	inherited := []string{
		"HOME=/home/example",
		"PATH=/usr/bin:/bin",
		"KEEP=a=b=c",
		"EMPTY=",
		"HTTP_PROXY=http://corp.example:8080",
		"http_proxy=http://corp.example:8080",
		"HTTPS_PROXY=http://corp.example:8080",
		"ALL_PROXY=socks5://corp.example:1080",
		"NO_PROXY=api.example.com,.corp.example,localhost",
		"no_proxy=api.example.com",
	}

	env, err := BuildProxyEnvironment(inherited, testTargets)
	if err != nil {
		t.Fatalf("BuildProxyEnvironment: %v", err)
	}
	got := envMap(env)
	if got["HOME"] != "/home/example" || got["PATH"] != "/usr/bin:/bin" || got["KEEP"] != "a=b=c" || got["EMPTY"] != "" {
		t.Fatalf("unrelated variables were not preserved: %#v", got)
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		if got[name] != "http://127.0.0.1:8317" {
			t.Errorf("%s = %q, want http://127.0.0.1:8317", name, got[name])
		}
	}
	for _, name := range []string{"http_proxy", "https_proxy", "all_proxy"} {
		if got[name] != "http://127.0.0.1:8317" {
			t.Errorf("%s = %q, want http://127.0.0.1:8317", name, got[name])
		}
	}
	if got["NO_PROXY"] != NoProxy || got["no_proxy"] != NoProxy {
		t.Fatalf("NO_PROXY = %q / %q, want the fixed local/LAN list", got["NO_PROXY"], got["no_proxy"])
	}
	if !contains(strings.Split(NoProxy, ","), "127.0.0.1") {
		t.Fatal("NO_PROXY lacks the literal loopback address")
	}
	if strings.Contains(got["NO_PROXY"], "api.example.com") {
		t.Fatal("an inherited entry for an approved destination survived into NO_PROXY")
	}
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "no_proxy"} {
		if n := countEnvKey(env, name); n != 1 {
			t.Errorf("environment has %d entries for %s, want 1", n, name)
		}
	}
}

func TestBuildProxyEnvironmentRejectsNonLoopbackListener(t *testing.T) {
	for _, targets := range []Targets{
		{HTTPListen: "0.0.0.0:8317", SOCKSListen: "127.0.0.1:8318"},
		{HTTPListen: "127.0.0.1:8317", SOCKSListen: "192.168.1.5:8318"},
		{HTTPListen: "127.0.0.1:0", SOCKSListen: "127.0.0.1:8318"},
		{HTTPListen: "127.0.0.1:+8317", SOCKSListen: "127.0.0.1:8318"},
		{HTTPListen: "127.0.0.1:08317", SOCKSListen: "127.0.0.1:8318"},
	} {
		env, err := BuildProxyEnvironment(nil, targets)
		if err == nil {
			t.Fatalf("BuildProxyEnvironment(%v) succeeded, want refusal", targets)
		}
		if env != nil {
			t.Fatalf("BuildProxyEnvironment(%v) returned an environment on refusal", targets)
		}
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Kind != contract.Unsupported {
			t.Fatalf("error = %v, want contract.Unsupported", err)
		}
	}
}

func TestBuildProxyEnvironmentIPv6Loopback(t *testing.T) {
	targets := Targets{HTTPListen: "[::1]:8317", SOCKSListen: "[::1]:8318"}
	env, err := BuildProxyEnvironment([]string{"HTTP_PROXY=http://corp:8080"}, targets)
	if err != nil {
		t.Fatalf("BuildProxyEnvironment: %v", err)
	}
	got := envMap(env)
	if got["HTTP_PROXY"] != "http://[::1]:8317" || got["http_proxy"] != "http://[::1]:8317" {
		t.Fatalf("IPv6 proxy URL = %q / %q", got["HTTP_PROXY"], got["http_proxy"])
	}
	values, err := valuesFor(KindKDE, targets)
	if err != nil {
		t.Fatalf("valuesFor: %v", err)
	}
	if values["kioslaverc:Proxy Settings/httpProxy"] != "http://[::1] 8317" {
		t.Fatalf("KDE IPv6 proxy = %q", values["kioslaverc:Proxy Settings/httpProxy"])
	}
	gnome, err := valuesFor(KindGNOME, targets)
	if err != nil {
		t.Fatalf("valuesFor GNOME: %v", err)
	}
	if gnome["org.gnome.system.proxy.http host"] != "'::1'" {
		t.Fatalf("GNOME IPv6 host = %q", gnome["org.gnome.system.proxy.http host"])
	}
}

func TestLauncherRefusesWhenHealthCheckFails(t *testing.T) {
	health := &fakeHealth{err: errors.New("connection refused")}
	launcher := &Launcher{Health: health}

	launch, err := launcher.Prepare(context.Background(), testTargets, []string{"NO_PROXY=api.example.com"})
	if err == nil {
		t.Fatal("Prepare succeeded with an unhealthy service")
	}
	if launch.Environment != nil {
		t.Fatalf("Prepare returned an environment on refusal: %v", launch.Environment)
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error type = %T, want *contract.Failure", err)
	}
	if failure.Kind != contract.Stopped {
		t.Fatalf("kind = %s, want stopped", failure.Kind)
	}
	if contract.ExitCodeFor[failure.Kind] != contract.ExitNotRunning {
		t.Fatalf("exit code = %d, want %d", contract.ExitCodeFor[failure.Kind], contract.ExitNotRunning)
	}
	if health.calls != 1 {
		t.Fatalf("health check calls = %d, want 1", health.calls)
	}
}

func TestLauncherRefusesListenerIdentityMismatch(t *testing.T) {
	health := &fakeHealth{health: Health{HTTPListen: "127.0.0.1:9000", SOCKSListen: "127.0.0.1:8318"}}
	launch, err := (&Launcher{Health: health}).Prepare(context.Background(), testTargets, nil)
	if err == nil || launch.Environment != nil {
		t.Fatalf("Prepare accepted a foreign listener: launch=%v err=%v", launch, err)
	}
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.Stopped {
		t.Fatalf("error = %v, want stopped", err)
	}
}

func TestLauncherRejectsNonLoopbackBeforeHealthCheck(t *testing.T) {
	health := &fakeHealth{health: Health{HTTPListen: "0.0.0.0:8317", SOCKSListen: "127.0.0.1:8318"}}
	bad := Targets{HTTPListen: "0.0.0.0:8317", SOCKSListen: "127.0.0.1:8318"}
	launch, err := (&Launcher{Health: health}).Prepare(context.Background(), bad, nil)
	if err == nil || launch.Environment != nil {
		t.Fatal("Prepare accepted a non-loopback listener")
	}
	if health.calls != 0 {
		t.Fatalf("health was checked before listener validation: %d calls", health.calls)
	}
}

func TestLauncherRefusesWithoutHealthChecker(t *testing.T) {
	launch, err := (&Launcher{}).Prepare(context.Background(), testTargets, nil)
	if err == nil || launch.Environment != nil {
		t.Fatal("Prepare ran without a health checker")
	}
}

func TestLauncherSuccessCarriesHonestNotice(t *testing.T) {
	health := &fakeHealth{health: Health{HTTPListen: testTargets.HTTPListen, SOCKSListen: testTargets.SOCKSListen}}
	launch, err := (&Launcher{Health: health}).Prepare(context.Background(), testTargets, []string{"NO_PROXY=api.example.com"})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	got := envMap(launch.Environment)
	if got["NO_PROXY"] != NoProxy {
		t.Fatalf("NO_PROXY = %q, want %q", got["NO_PROXY"], NoProxy)
	}
	if !strings.Contains(launch.Notice, "ignore") || !strings.Contains(launch.Notice, "outside") {
		t.Fatalf("notice does not state the capture limit: %q", launch.Notice)
	}
}

func TestTestsAreIsolatedFromRealHomeAndTools(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" || home == originalHome || !strings.HasPrefix(home, isolatedRoot) {
		t.Fatalf("tests are using a non-isolated HOME: %q", home)
	}
	if config := os.Getenv("XDG_CONFIG_HOME"); !strings.HasPrefix(config, isolatedRoot) {
		t.Fatalf("XDG_CONFIG_HOME is not isolated: %q", config)
	}
	if _, err := exec.LookPath("gsettings"); err == nil {
		t.Fatal("real gsettings is reachable from the test PATH")
	}
	if _, err := exec.LookPath("kwriteconfig6"); err == nil {
		t.Fatal("real kwriteconfig6 is reachable from the test PATH")
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		t.Fatal("real systemctl is reachable from the test PATH")
	}
}

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, entry := range env {
		if i := strings.IndexByte(entry, '='); i >= 0 {
			m[entry[:i]] = entry[i+1:]
		}
	}
	return m
}

func countEnvKey(env []string, key string) int {
	n := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, key+"=") {
			n++
		}
	}
	return n
}
