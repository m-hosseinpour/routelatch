package acceptance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestA11DesktopProxyLifecycle is the gated companion to TestA11Launcher. It
// drives the shipped binary against the caller's real KDE or GNOME desktop
// setting: apply, read the OS configuration back, stop the service, restore
// offline and compare the OS setting with the pre-test snapshot.
//
// It is skipped unless ROUTELATCH_DESKTOP_TEST=1 because it changes a real
// desktop setting outside the isolated test HOME. A SIGKILL of the test process
// can therefore leave the desktop pointing at a closed listener; run it only on
// a machine whose desktop proxy you can repair, as the user authorized for R2.
// The deferred cleanup restores the captured snapshot even when an assertion
// fails.
func TestA11DesktopProxyLifecycle(t *testing.T) {
	if os.Getenv("ROUTELATCH_DESKTOP_TEST") != "1" {
		t.Skip("set ROUTELATCH_DESKTOP_TEST=1 to change and restore this machine's real desktop proxy setting")
	}
	kind := desktopKindForTest()
	if kind == "" {
		t.Skip("this process does not see XDG_CURRENT_DESKTOP, DESKTOP_SESSION, KDE_FULL_SESSION or GNOME_DESKTOP_SESSION_ID; run it from inside the desktop session")
	}
	snap := takeDesktopSnapshot(t, kind)

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod data dir: %v", err)
	}
	// The service and CLI must see the real HOME so the adapter edits the real
	// desktop configuration; only the product data dir and ports are isolated.
	env := &testEnv{home: os.Getenv("HOME"), tmp: t.TempDir(), vars: os.Environ()}
	cfg := serviceConfig{
		HTTPListen:  fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		SOCKSListen: fmt.Sprintf("127.0.0.1:%d", freePort(t)),
	}
	var svc *service
	t.Cleanup(func() {
		if svc != nil {
			// Ask the product to restore first: that exercises the ownership
			// check. The raw snapshot below is the safety net for everything
			// the product could not or should not change.
			_ = svc.cli("desktop", "proxy", "restore")
			_ = svc.stop()
		}
		if !desktopMatchesSnapshot(t, snap) {
			snap.restore(t)
			if !desktopMatchesSnapshot(t, snap) || !desktopBytesMatchSnapshot(t, snap) {
				t.Errorf("desktop proxy setting does not match the pre-test snapshot after cleanup")
			}
		}
	})

	svc = startService(t, env, dir, cfg)
	status := svc.mustOK("desktop", "proxy", "status").mapData(t)
	if status["supported"] != true || status["applied"] != false || str(status["environment"]) != kind {
		t.Fatalf("honest pre-apply status expected (supported=true applied=false environment=%s), got %+v", kind, status)
	}

	svc.mustOK("desktop", "proxy", "set")
	httpAddr := cfg.HTTPListen
	switch kind {
	case "kde":
		if got := kdeRead(t, "ProxyType"); got != "1" {
			t.Fatalf("kioslaverc ProxyType = %q, want manual (1)", got)
		}
		if got, want := kdeRead(t, "httpProxy"), kdeURL("http", httpAddr); got != want {
			t.Fatalf("kioslaverc httpProxy = %q, want %q", got, want)
		}
		if got, want := kdeRead(t, "socksProxy"), kdeURL("socks", cfg.SOCKSListen); got != want {
			t.Fatalf("kioslaverc socksProxy = %q, want %q", got, want)
		}
		if got := kdeRead(t, "NoProxyFor"); got == "" || got == kdeAbsent {
			t.Fatalf("kioslaverc NoProxyFor was not set: %q", got)
		}
	case "gnome":
		host, port := splitHostPort(t, httpAddr)
		if got := gsettingsGet(t, "org.gnome.system.proxy", "mode"); got != "'manual'" {
			t.Fatalf("GNOME proxy mode = %q, want 'manual'", got)
		}
		if got := gsettingsGet(t, "org.gnome.system.proxy.http", "host"); got != "'"+host+"'" {
			t.Fatalf("GNOME http host = %q, want %q", got, "'"+host+"'")
		}
		if got := gsettingsGet(t, "org.gnome.system.proxy.http", "port"); got != port {
			t.Fatalf("GNOME http port = %q, want %q", got, port)
		}
		socksHost, socksPort := splitHostPort(t, cfg.SOCKSListen)
		if got := gsettingsGet(t, "org.gnome.system.proxy.socks", "host"); got != "'"+socksHost+"'" {
			t.Fatalf("GNOME socks host = %q", got)
		}
		if got := gsettingsGet(t, "org.gnome.system.proxy.socks", "port"); got != socksPort {
			t.Fatalf("GNOME socks port = %q", got)
		}
	}

	// The marker must record what was replaced, not only that something was.
	marker := readDesktopMarker(t, dir)
	if marker["applied"] != true || str(marker["environment"]) != kind {
		t.Fatalf("state marker = %+v, want applied %s", marker, kind)
	}
	keys, _ := marker["keys"].([]any)
	if len(keys) == 0 {
		t.Fatal("state marker records no previous values")
	}
	recorded := ""
	for _, raw := range keys {
		key, _ := raw.(map[string]any)
		name := str(key["name"])
		if strings.HasSuffix(name, "/httpProxy") || name == "org.gnome.system.proxy.http port" {
			recorded = str(key["previous"])
		}
	}
	if recorded != snap.httpValue {
		t.Fatalf("recorded previous HTTP value = %q, want the pre-apply value %q", recorded, snap.httpValue)
	}

	// Stopping the listener must not silently rewrite the desktop to direct.
	if err := svc.stop(); err != nil {
		t.Fatalf("stop service: %v", err)
	}
	switch kind {
	case "kde":
		if got, want := kdeRead(t, "httpProxy"), kdeURL("http", httpAddr); got != want {
			t.Fatalf("after stop kioslaverc httpProxy = %q, want it still pointing at RouteLatch %q", got, want)
		}
	case "gnome":
		if got := gsettingsGet(t, "org.gnome.system.proxy", "mode"); got != "'manual'" {
			t.Fatalf("after stop GNOME mode = %q, want manual (no silent direct)", got)
		}
	}
	if conn, err := net.DialTimeout("tcp", httpAddr, 2*time.Second); err == nil {
		_ = conn.Close()
		t.Fatalf("RouteLatch listener at %s is still open after stop", httpAddr)
	}

	// Restore is documented to work with no running service.
	restore := svc.mustOK("desktop", "proxy", "restore").mapData(t)
	if restore["applied"] != false || restore["previous_set"] != false {
		t.Fatalf("restore still claims ownership: %+v", restore)
	}
	if !desktopMatchesSnapshot(t, snap) {
		t.Fatalf("restored desktop setting differs from the pre-test snapshot")
	}
	if marker := readDesktopMarker(t, dir); marker["applied"] == true {
		t.Fatalf("state marker still applied after restore: %+v", marker)
	}
}

const kdeAbsent = "__routelatch_desktop_test_absent__"

func desktopKindForTest() string {
	has := func(value string, needles ...string) bool {
		lower := strings.ToLower(value)
		for _, needle := range needles {
			if strings.Contains(lower, needle) {
				return true
			}
		}
		return false
	}
	if has(os.Getenv("XDG_CURRENT_DESKTOP"), "kde", "plasma") {
		return "kde"
	}
	if has(os.Getenv("XDG_CURRENT_DESKTOP"), "gnome") {
		return "gnome"
	}
	if has(os.Getenv("DESKTOP_SESSION"), "plasma", "kde") {
		return "kde"
	}
	if has(os.Getenv("DESKTOP_SESSION"), "gnome") {
		return "gnome"
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("KDE_FULL_SESSION")), "true") {
		return "kde"
	}
	if strings.TrimSpace(os.Getenv("GNOME_DESKTOP_SESSION_ID")) != "" {
		return "gnome"
	}
	return ""
}

type desktopSnapshot struct {
	kind string
	// KDE: the complete file, so keys RouteLatch does not manage (ftpProxy,
	// Authmode) are restored too, plus the managed values for a semantic
	// comparison after the product's own restore.
	kdePath    string
	kdeExisted bool
	kdeRaw     []byte
	kdeManaged map[string]string
	// GNOME: the managed keys only, captured as GVariant text.
	gnome map[string]string
	// The single HTTP value whose previous value the marker must record.
	httpValue string
}

var kdeManagedKeys = []string{
	"ReversedException", "httpProxy", "httpsProxy", "socksProxy", "NoProxyFor", "ProxyType",
}

func takeDesktopSnapshot(t *testing.T, kind string) *desktopSnapshot {
	t.Helper()
	s := &desktopSnapshot{kind: kind}
	if kind == "kde" {
		kdeRead(t, "ProxyType") // fail early with a clear message when the tool is missing
		configHome := os.Getenv("XDG_CONFIG_HOME")
		if configHome == "" {
			configHome = filepath.Join(os.Getenv("HOME"), ".config")
		}
		s.kdePath = filepath.Join(configHome, "kioslaverc")
		raw, err := os.ReadFile(s.kdePath)
		if err == nil {
			s.kdeExisted, s.kdeRaw = true, raw
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("snapshot kioslaverc: %v", err)
		}
		s.kdeManaged = make(map[string]string, len(kdeManagedKeys))
		for _, key := range kdeManagedKeys {
			value := kdeRead(t, key)
			if value == kdeAbsent {
				value = ""
			}
			s.kdeManaged[key] = value
		}
		s.httpValue = s.kdeManaged["httpProxy"]
		return s
	}
	s.gnome = make(map[string]string)
	for _, item := range gnomeManagedKeys {
		s.gnome[item.schema+" "+item.key] = gsettingsGet(t, item.schema, item.key)
	}
	s.httpValue = gsettingsGet(t, "org.gnome.system.proxy.http", "port")
	return s
}

var gnomeManagedKeys = []struct{ schema, key string }{
	{"org.gnome.system.proxy.http", "host"},
	{"org.gnome.system.proxy.http", "port"},
	{"org.gnome.system.proxy.https", "host"},
	{"org.gnome.system.proxy.https", "port"},
	{"org.gnome.system.proxy.socks", "host"},
	{"org.gnome.system.proxy.socks", "port"},
	{"org.gnome.system.proxy", "ignore-hosts"},
	{"org.gnome.system.proxy", "mode"},
}

func (s *desktopSnapshot) restore(t *testing.T) {
	t.Helper()
	if s.kind == "kde" {
		if !s.kdeExisted {
			if err := os.Remove(s.kdePath); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Errorf("remove kioslaverc created by the test: %v", err)
			}
			return
		}
		if err := os.WriteFile(s.kdePath, s.kdeRaw, 0o600); err != nil {
			t.Errorf("restore kioslaverc snapshot: %v", err)
		}
		return
	}
	for _, item := range gnomeManagedKeys {
		value := s.gnome[item.schema+" "+item.key]
		if out, err := exec.Command("gsettings", "set", item.schema, item.key, value).CombinedOutput(); err != nil {
			t.Errorf("restore %s %s: %v (%s)", item.schema, item.key, err, out)
		}
	}
}

func desktopMatchesSnapshot(t *testing.T, s *desktopSnapshot) bool {
	t.Helper()
	if s.kind == "kde" {
		for _, key := range kdeManagedKeys {
			got := kdeRead(t, key)
			if got == kdeAbsent {
				got = ""
			}
			if got != s.kdeManaged[key] {
				return false
			}
		}
		return true
	}
	for _, item := range gnomeManagedKeys {
		if got := gsettingsGet(t, item.schema, item.key); got != s.gnome[item.schema+" "+item.key] {
			return false
		}
	}
	return true
}

// desktopBytesMatchSnapshot is the stricter check used after the raw fallback:
// the fallback writes the captured file back, so byte identity is expected even
// though the product's key-by-key restore only promises values.
func desktopBytesMatchSnapshot(t *testing.T, s *desktopSnapshot) bool {
	t.Helper()
	if s.kind != "kde" {
		return true
	}
	raw, err := os.ReadFile(s.kdePath)
	if !s.kdeExisted {
		return errors.Is(err, os.ErrNotExist)
	}
	return err == nil && bytes.Equal(raw, s.kdeRaw)
}

func kdeRead(t *testing.T, key string) string {
	t.Helper()
	tool := "kreadconfig6"
	if _, err := exec.LookPath(tool); err != nil {
		tool = "kreadconfig5"
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("kreadconfig6/5 not found; KDE desktop settings cannot be read")
		}
	}
	out, err := exec.Command(tool, "--file", "kioslaverc", "--group", "Proxy Settings", "--key", key, "--default", kdeAbsent).Output()
	if err != nil {
		t.Fatalf("%s %s: %v", tool, key, err)
	}
	return strings.TrimSpace(string(out))
}

func gsettingsGet(t *testing.T, schema, key string) string {
	t.Helper()
	if _, err := exec.LookPath("gsettings"); err != nil {
		t.Fatalf("gsettings not found; GNOME desktop settings cannot be read")
	}
	out, err := exec.Command("gsettings", "get", schema, key).Output()
	if err != nil {
		t.Fatalf("gsettings get %s %s: %v", schema, key, err)
	}
	return strings.TrimSpace(string(out))
}

func kdeURL(scheme, addr string) string {
	host, port := splitHostPortValue(addr)
	return scheme + "://" + host + " " + port
}

func splitHostPort(t *testing.T, addr string) (string, string) {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	return host, port
}

func splitHostPortValue(addr string) (string, string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, ""
	}
	return host, port
}

func readDesktopMarker(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode state.json: %v", err)
	}
	marker, _ := doc["desktop"].(map[string]any)
	return marker
}
