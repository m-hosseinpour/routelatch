package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fixtureSet owns the loopback exits and the import payload. The secrets are
// generated per test and never leave the temp data dir or the 0600 state file.
type fixtureSet struct {
	uuid     string
	password string

	vless  *exitProc
	trojan *exitProc
	socks  *socksExit

	filePath string
	links    []string
}

func newFixtureSet(t *testing.T) *fixtureSet {
	t.Helper()
	uuid := randomUUID(t)
	password := "pw-" + randomHex(t, 12)

	vlessPort := freePort(t)
	trojanPort := freePort(t)
	socks := newSOCKSExit(t)

	hosts := map[string]string{
		"exact.rl.test":       "127.0.0.1",
		"deep.suffix.rl.test": "127.0.0.1",
		"vless.rl.test":       "127.0.0.1",
		"trojan.rl.test":      "127.0.0.1",
		"stall.rl.test":       "127.0.0.1",
	}
	vless := startExit(t, exitSpec{Protocol: "vless", Port: int(vlessPort), UUID: uuid, Hosts: hosts})
	trojan := startExit(t, exitSpec{Protocol: "trojan", Port: int(trojanPort), Password: password, Hosts: hosts})

	links := []string{
		fmt.Sprintf("vless://%s@127.0.0.1:%d?encryption=none&security=none&type=tcp#vless-exit", uuid, vlessPort),
		fmt.Sprintf("trojan://%s@127.0.0.1:%d?security=none&type=tcp#trojan-exit", password, trojanPort),
		fmt.Sprintf("socks5://127.0.0.1:%d#socks-exit", socks.port()),
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.txt")
	payload := ""
	for _, l := range links {
		payload += l + "\n"
	}
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return &fixtureSet{
		uuid: uuid, password: password,
		vless: vless, trojan: trojan, socks: socks,
		filePath: path, links: links,
	}
}

// close stops every fixture the set owns. It is idempotent and can be called
// explicitly before a resource measurement.
func (f *fixtureSet) close() {
	f.vless.stop()
	f.trojan.stop()
	f.socks.close()
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	f, err := os.Open("/dev/urandom")
	if err != nil {
		t.Fatalf("urandom: %v", err)
	}
	defer f.Close()
	b := make([]byte, n)
	if _, err := ioReadFull(f, b); err != nil {
		t.Fatalf("urandom read: %v", err)
	}
	return b
}

func ioReadFull(r interface{ Read([]byte) (int, error) }, b []byte) (int, error) {
	total := 0
	for total < len(b) {
		n, err := r.Read(b[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := randomBytes(t, n)
	const digits = "0123456789abcdef"
	out := make([]byte, n*2)
	for i, v := range b {
		out[i*2] = digits[v>>4]
		out[i*2+1] = digits[v&0x0f]
	}
	return string(out)
}

func randomUUID(t *testing.T) string {
	t.Helper()
	b := randomBytes(t, 16)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// findPoolRow returns the first pool row matching pred, or nil.
func findPoolRow(t *testing.T, svc *service, pred func(map[string]any) bool) map[string]any {
	t.Helper()
	for _, row := range svc.mustOK("pool", "list").list(t, "records") {
		if pred(row) {
			return row
		}
	}
	return nil
}

func findRecord(t *testing.T, res cliResult, protocol string) map[string]any {
	t.Helper()
	for _, row := range res.list(t, "records") {
		if str(row["protocol"]) == protocol {
			return row
		}
	}
	t.Fatalf("no %s record in pool list: %+v", protocol, res.list(t, "records"))
	return nil
}
