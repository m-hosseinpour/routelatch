package acceptance

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// checkPermissions verifies the documented private layout (§4.1).
func checkPermissions(t *testing.T, r report, dir string) {
	t.Helper()
	wantDir := map[string]os.FileMode{
		dir:                          0o700,
		filepath.Join(dir, "run"):    0o700,
		filepath.Join(dir, "backup"): 0o700,
	}
	okDirs := true
	for path, want := range wantDir {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			r.fail("A09", "cannot stat %s: %v", filepath.Base(path), err)
			okDirs = false
			continue
		}
		if info.Mode().Perm() != want {
			r.fail("A09", "%s mode is %04o, want %04o", filepath.Base(path), info.Mode().Perm(), want)
			okDirs = false
		}
	}
	if okDirs {
		r.pass("A09", "every created data/runtime directory is 0700")
	}

	wantFiles := map[string]os.FileMode{
		filepath.Join(dir, "state.json"):     0o600,
		filepath.Join(dir, "config.json"):    0o600,
		filepath.Join(dir, "state.lock"):     0o600,
		filepath.Join(dir, "state.json.bak"): 0o600,
	}
	okFiles := true
	for path, want := range wantFiles {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			r.fail("A09", "cannot stat %s: %v", filepath.Base(path), err)
			okFiles = false
			continue
		}
		if info.Mode().Perm() != want {
			r.fail("A09", "%s mode is %04o, want %04o", filepath.Base(path), info.Mode().Perm(), want)
			okFiles = false
		}
	}
	if okFiles {
		r.pass("A09", "state.json, state.json.bak, config.json and state.lock are 0600")
	}

	sock := filepath.Join(dir, "run", "control.sock")
	if info, err := os.Stat(sock); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			r.fail("A09", "management path is not a Unix socket")
		} else if info.Mode().Perm() != 0o600 {
			r.fail("A09", "management socket mode is %04o, want 0600", info.Mode().Perm())
		} else {
			r.pass("A09", "management socket is a Unix socket with mode 0600 (no TCP management surface)")
		}
	} else {
		r.fail("A09", "management socket missing while the service runs: %v", err)
	}
}

// scanSecrets checks every CLI output, the service log stream and every file
// under the data dir for the fixture's secret tokens. The documented state file
// may hold credentials and the subscription origin in its own fields; those
// fields are masked before the scan, which is the only place they are allowed.
func scanSecrets(t *testing.T, r report, svc *service, dir string, fx *fixtureSet, sub *subscriptionServer, extra ...string) {
	t.Helper()
	secrets := []struct {
		name  string
		value string
	}{
		{"node UUID", fx.uuid},
		{"node password", fx.password},
		{"subscription URL", sub.url},
		{"subscription token", sub.token},
		{"VLESS share link", fx.links[0]},
		{"Trojan share link", fx.links[1]},
	}
	for i, value := range extra {
		secrets = append(secrets, struct {
			name  string
			value string
		}{fmt.Sprintf("extra secret %d", i+1), value})
	}

	svc.mu.Lock()
	surfaces := []string{svc.stderr.String(), svc.stdout.String()}
	surfaces = append(surfaces, svc.outputs...)
	svc.mu.Unlock()

	clean := true
	for _, secret := range secrets {
		for _, surface := range surfaces {
			if strings.Contains(surface, secret.value) {
				r.fail("A09", "%s appears in CLI/log output", secret.name)
				clean = false
				break
			}
		}
	}
	if clean {
		r.pass("A09", "all %d CLI/log surfaces contain no UUID, password, share link or subscription URL", len(surfaces))
	}

	// State files: mask the two documented secret-bearing fields, then scan.
	for _, name := range []string{"state.json", "state.json.bak"} {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			r.fail("A09", "cannot read %s: %v", name, err)
			continue
		}
		masked := maskStateSecrets(t, raw)
		for _, secret := range secrets {
			if strings.Contains(masked, secret.value) {
				r.fail("A09", "%s outside credentials/origin contains the %s", name, secret.name)
				return
			}
		}
	}
	r.pass("A09", "state files carry secrets only in records[].credentials / sources[].origin (all other bytes scanned)")

	// Anything else under the data dir is a log or an auxiliary file: no secret
	// may be there at all.
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if base == "state.json" || base == "state.json.bak" || base == "state.lock" || base == "config.json" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, secret := range secrets {
			if strings.Contains(string(raw), secret.value) {
				r.fail("A09", "auxiliary file %s contains the %s", base, secret.name)
				return nil
			}
		}
		return nil
	})
}

// maskStateSecrets blanks the two documented secret-bearing fields so the rest
// of the document can be scanned without false positives.
func maskStateSecrets(t *testing.T, raw []byte) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return string(raw)
	}
	if records, ok := doc["records"].([]any); ok {
		for _, item := range records {
			if rec, ok := item.(map[string]any); ok {
				delete(rec, "credentials")
			}
		}
	}
	if sources, ok := doc["sources"].([]any); ok {
		for _, item := range sources {
			if src, ok := item.(map[string]any); ok {
				src["origin"] = ""
			}
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// assertLoopbackListeners checks the bound addresses from status.
func assertLoopbackListeners(t *testing.T, r report, svc *service) {
	t.Helper()
	data := svc.mustOK("status").mapData(t)
	listeners, _ := data["listeners"].(map[string]any)
	httpAddr, socksAddr := str(listeners["http"]), str(listeners["socks"])
	if strings.HasPrefix(httpAddr, "127.0.0.1:") && strings.HasPrefix(socksAddr, "127.0.0.1:") {
		r.pass("A09", "both traffic listeners are bound to literal loopback addresses (%s, %s)", httpAddr, socksAddr)
	} else {
		r.fail("A09", "listeners are not loopback: %v", listeners)
	}
}

// reviewStateArea prints a compact inventory so the report can cite what was
// inspected for the secret and permission checks.
func reviewStateArea(t *testing.T, dir string) string {
	t.Helper()
	var names []string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		info, _ := entry.Info()
		mode := os.FileMode(0)
		if info != nil {
			mode = info.Mode().Perm()
		}
		names = append(names, fmt.Sprintf("%s(%04o)", rel, mode))
		return nil
	})
	return strings.Join(names, " ")
}

// assertExternalLoopback inspects the product process's own socket table. It
// does not trust status: a product that bound 0.0.0.0 but reported 127.0.0.1
// would fail here, and a TCP management listener would show up as an extra
// listening port owned by the process.
func assertExternalLoopback(t *testing.T, r report, svc *service) {
	t.Helper()
	pid := svc.proc.Process.Pid
	inodes := socketInodes(t, pid)

	type listener struct {
		port   int
		addr   string
		family string
	}
	var listeners []listener
	for _, family := range []string{"tcp", "tcp6"} {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/%s", pid, family))
		if err != nil {
			r.fail("A09", "cannot read the process %s table: %v", family, err)
			return
		}
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != "0A" || !inodes[fields[9]] {
				continue
			}
			hostPort := fields[1]
			i := strings.LastIndexByte(hostPort, ':')
			if i < 0 {
				continue
			}
			port, err := strconv.ParseInt(hostPort[i+1:], 16, 32)
			if err != nil {
				continue
			}
			listeners = append(listeners, listener{port: int(port), addr: hostPort[:i], family: family})
		}
	}

	want := map[int]string{portOf(t, svc.httpAddr): "http", portOf(t, svc.socksAddr): "socks"}
	if len(listeners) != len(want) {
		r.fail("A09", "the process owns %d listening TCP sockets, want exactly 2 (no TCP management surface): %+v", len(listeners), listeners)
		return
	}
	loopback := true
	for _, l := range listeners {
		name, ok := want[l.port]
		if !ok {
			r.fail("A09", "unexpected listening port %d owned by the product", l.port)
			loopback = false
			continue
		}
		switch l.addr {
		case "0100007F": // 127.0.0.1
		case "00000000000000000000000001000000": // ::1
		default:
			r.fail("A09", "the %s listener binds %s (family %s), not loopback", name, l.addr, l.family)
			loopback = false
		}
	}
	if loopback {
		r.pass("A09", "the process's own socket table shows exactly two listeners, both bound to loopback")
	}

	unix, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/unix", pid))
	if err != nil {
		r.fail("A09", "cannot read the process unix table: %v", err)
		return
	}
	if strings.Contains(string(unix), filepath.Join("run", "control.sock")) {
		r.pass("A09", "management is a Unix socket in the process's table, not a TCP port")
	} else {
		r.fail("A09", "management socket path is not listed in the process unix table")
	}
}

func socketInodes(t *testing.T, pid int) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		t.Fatalf("read process fds: %v", err)
	}
	out := make(map[string]bool, len(entries))
	for _, entry := range entries {
		target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, entry.Name()))
		if err != nil || !strings.HasPrefix(target, "socket:[") {
			continue
		}
		out[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
	}
	return out
}

func portOf(t *testing.T, addr string) int {
	t.Helper()
	_, portRaw, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("listener address %q: %v", addr, err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		t.Fatalf("listener port %q: %v", portRaw, err)
	}
	return port
}
