package httpfront

import (
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/elazarl/goproxy"
)

// execCommandSubprocess re-runs this test binary for one helper test with a
// clean proxy-related environment plus extraEnv.
func execCommandSubprocess(t *testing.T, testName string, extraEnv []string) *exec.Cmd {
	t.Helper()
	drop := map[string]bool{
		"HTTP_PROXY": true, "http_proxy": true,
		"HTTPS_PROXY": true, "https_proxy": true,
		"NO_PROXY": true, "no_proxy": true,
		"ALL_PROXY": true, "all_proxy": true,
		"ROUTELATCH_CHILD": true,
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			env = append(env, kv)
		}
	}
	env = append(env, extraEnv...)
	env = append(env, "ROUTELATCH_CHILD=1")

	cmd := exec.Command(os.Args[0], "-test.run=^"+testName+"$", "-test.v")
	cmd.Env = env
	return cmd
}

// TestEnvProxyChild is only executed as a child of TestDefaultConfigUsesEnvironmentProxy.
func TestEnvProxyChild(t *testing.T) {
	if os.Getenv("ROUTELATCH_CHILD") != "1" {
		t.Skip("helper process only")
	}
	p := goproxy.NewProxyHttpServer()
	proxyAddr, _ := startProxy(t, p)

	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(c, "GET http://env-proxy.invalid/ HTTP/1.1\r\nHost: env-proxy.invalid\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _ = io.ReadAll(c)
}
