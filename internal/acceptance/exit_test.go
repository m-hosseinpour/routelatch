package acceptance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"

	_ "github.com/xtls/xray-core/main/distro/all"
)

// TestHelperExit is the child-process VLESS/Trojan exit. Running it in its own
// process is the T1 technique: the product process then holds exactly the one
// embedded core it assumes, and the test asserts against a real pinned core
// rather than a stub.
func TestHelperExit(t *testing.T) {
	if os.Getenv("RL_ACCEPT_EXIT_HELPER") != "1" {
		t.Skip("exit helper process only")
	}
	var spec exitSpec
	if err := json.NewDecoder(os.Stdin).Decode(&spec); err != nil {
		fmt.Fprintf(os.Stderr, "exit helper: bad spec: %v\n", err)
		os.Exit(2)
	}
	cfg, err := serial.LoadJSONConfig(strings.NewReader(string(spec.config())))
	if err != nil {
		fmt.Fprintf(os.Stderr, "exit helper: config: %v\n", err)
		os.Exit(2)
	}
	inst, err := core.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exit helper: new: %v\n", err)
		os.Exit(2)
	}
	if err := inst.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "exit helper: start: %v\n", err)
		os.Exit(2)
	}
	fmt.Println("READY")
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = inst.Close()
}

type exitSpec struct {
	Protocol string            `json:"protocol"` // "vless" | "trojan"
	Port     int               `json:"port"`
	UUID     string            `json:"uuid,omitempty"`
	Password string            `json:"password,omitempty"`
	Hosts    map[string]string `json:"hosts,omitempty"`
}

func (s exitSpec) config() []byte {
	var inbound map[string]any
	stream := map[string]any{"network": "tcp"}
	switch s.Protocol {
	case "vless":
		inbound = map[string]any{
			"tag":      "in",
			"listen":   "127.0.0.1",
			"port":     s.Port,
			"protocol": "vless",
			"settings": map[string]any{
				"clients":    []any{map[string]any{"id": s.UUID}},
				"decryption": "none",
			},
			"streamSettings": stream,
		}
	case "trojan":
		inbound = map[string]any{
			"tag":            "in",
			"listen":         "127.0.0.1",
			"port":           s.Port,
			"protocol":       "trojan",
			"settings":       map[string]any{"clients": []any{map[string]any{"password": s.Password}}},
			"streamSettings": stream,
		}
	default:
		panic("exitSpec: unknown protocol " + s.Protocol)
	}

	hosts := map[string]any{}
	for host, ip := range s.Hosts {
		hosts[host] = ip
	}

	cfg := map[string]any{
		"log": map[string]any{"loglevel": "info", "access": "none"},
		// The exit — not RouteLatch — resolves the requested hostname. The
		// static mapping is what makes a loopback destination reachable for a
		// public-looking hostname. The product process cannot resolve it, so a
		// successful tunnel proves the hostname travelled unmodified.
		"dns":      map[string]any{"hosts": hosts},
		"inbounds": []any{inbound},
		"outbounds": []any{map[string]any{
			"protocol": "freedom",
			"tag":      "direct",
			// Resolve the target at the exit through the configured DNS hosts.
			"targetStrategy": "useip",
			// The pinned core blocks private destinations in freedom by
			// default; this test exit explicitly allows 127/8 (a fixture
			// exception, not RouteLatch behaviour; T1 §0).
			"settings": map[string]any{
				"finalRules": []any{map[string]any{
					"action": "allow", "network": "tcp", "ip": []string{"127.0.0.0/8"},
				}},
			},
		}},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		panic(err)
	}
	return raw
}

type exitProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	port  int
	logs  *safeBuffer

	once sync.Once
}

// startExit launches a pinned-core exit child process and waits for READY.
func startExit(t *testing.T, spec exitSpec) *exitProc {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestHelperExit$", "-test.v=false")
	cmd.Env = append(os.Environ(), "RL_ACCEPT_EXIT_HELPER=1")
	logs := &safeBuffer{}
	cmd.Stderr = logs
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start exit: %v", err)
	}
	if err := json.NewEncoder(stdin).Encode(spec); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("send exit spec: %v", err)
	}
	ready := make(chan error, 1)
	go func() {
		br := bufio.NewReader(stdout)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				ready <- fmt.Errorf("exit stdout ended before READY: %w (%s)", err, logs.String())
				return
			}
			if strings.TrimSpace(line) == "READY" {
				ready <- nil
				break
			}
		}
		_, _ = io.Copy(logs, br)
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("%v", err)
		}
	case <-time.After(25 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("exit did not become ready: %s", logs.String())
	}
	proc := &exitProc{cmd: cmd, stdin: stdin, port: spec.Port, logs: logs}
	t.Cleanup(proc.stop)
	return proc
}

func (p *exitProc) stop() {
	if p == nil || p.cmd == nil {
		return
	}
	p.once.Do(func() {
		_ = p.stdin.Close()
		done := make(chan struct{})
		go func() {
			_ = p.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = p.cmd.Process.Kill()
			<-done
		}
	})
}

func (p *exitProc) Logs() string { return p.logs.String() }
