package singbox

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"routelatch/internal/contract"
)

const (
	defaultStartTimeout = 10 * time.Second
	defaultStopTimeout  = 5 * time.Second
	dialProbeTimeout    = 150 * time.Millisecond
	readyPollInterval   = 25 * time.Millisecond
	configFileName      = "sing-box.json"
)

// Options names the one executable RouteLatch is allowed to run and a private
// directory for the generated config. Both are supplied by the caller: the
// package never searches PATH for a binary or invents a machine-specific path.
type Options struct {
	Executable string
	ConfigDir  string
}

// Runtime is one managed sing-box process. It owns no process-global state and
// publishes a handoff only after the process is listening on every generated
// loopback inbound.
type Runtime struct {
	mu       sync.Mutex
	opts     Options
	cmd      *exec.Cmd
	waitCh   <-chan error
	handoffs map[string]contract.SidecarHandoff
	healthy  bool
}

var _ contract.SidecarRuntime = (*Runtime)(nil)

// New validates the caller-supplied paths. A missing executable is not an
// error here: Sync then fails closed and Healthy stays false.
func New(opts Options) (*Runtime, error) {
	if strings.TrimSpace(opts.ConfigDir) == "" {
		return nil, &contract.Failure{
			Kind:    contract.ImportInvalid,
			Path:    contract.PathLocal,
			Op:      "singbox.new",
			Message: "a sidecar config directory is required",
		}
	}
	return &Runtime{opts: opts}, nil
}

// Sync regenerates the whole config and restarts the process. There is no
// add/remove-outbound RPC, so even one changed exit rebuilds everything. Any
// failure leaves no published handoff and Healthy false.
func (r *Runtime) Sync(ctx context.Context, exits []contract.SidecarExit) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopLocked(context.Background())

	if len(exits) == 0 {
		return nil
	}
	gen, err := Generate(exits)
	if err != nil {
		return err
	}
	path, err := writeConfig(r.opts.ConfigDir, gen.Raw)
	if err != nil {
		return failure("the sidecar config could not be written", err)
	}
	if strings.TrimSpace(r.opts.Executable) == "" {
		return failure("the sidecar executable is not configured", nil)
	}

	// exec.Command (not CommandContext): the process must outlive the Sync
	// call. ctx bounds only the readiness wait; the engine cancels it as soon
	// as Sync returns, and the process is stopped explicitly by Stop/Sync.
	cmd := exec.Command(r.opts.Executable, "run", "-c", path)
	// Discard both sinks: sing-box log lines can name destinations, which D21
	// forbids recording anywhere.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return failure("the sidecar could not be started", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	if err := waitReady(ctx, gen.Handoffs, waitCh); err != nil {
		kill(cmd)
		select {
		case <-waitCh:
		case <-time.After(defaultStopTimeout):
		}
		return err
	}
	r.cmd = cmd
	r.waitCh = waitCh
	r.handoffs = gen.Handoffs
	r.healthy = true
	return nil
}

// Handoff returns the endpoint of one explicit tag. An unknown tag is never a
// cue to pick another exit, and an unhealthy runtime publishes nothing.
func (r *Runtime) Handoff(tag string) (contract.SidecarHandoff, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.healthy {
		return contract.SidecarHandoff{}, false
	}
	h, ok := r.handoffs[tag]
	if !ok {
		return contract.SidecarHandoff{}, false
	}
	return h, true
}

func (r *Runtime) Healthy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.healthy
}

// Stop terminates the process and withdraws every handoff. It is idempotent.
func (r *Runtime) Stop(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	waitCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, defaultStopTimeout)
		defer cancel()
	}
	r.stopLocked(waitCtx)
	if err := waitCtx.Err(); err != nil {
		return &contract.Failure{
			Kind:    contract.Canceled,
			Path:    contract.PathLocal,
			Op:      "singbox.stop",
			Message: "the sidecar stop did not finish before the caller's deadline",
			Err:     err,
		}
	}
	return nil
}

func (r *Runtime) stopLocked(ctx context.Context) {
	if r.cmd != nil {
		kill(r.cmd)
		select {
		case <-r.waitCh:
		case <-ctx.Done():
		}
	}
	r.cmd = nil
	r.waitCh = nil
	r.handoffs = nil
	r.healthy = false
}

func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// waitReady proves only that the sidecar is listening on every generated
// inbound. It is not proof that any hysteria2 exit reached a destination: the
// engine's proof class comes from parsing the SOCKS5 reply on a real dial.
func waitReady(ctx context.Context, handoffs map[string]contract.SidecarHandoff, waitCh <-chan error) error {
	deadline := time.Now().Add(defaultStartTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	for {
		select {
		case err := <-waitCh:
			return failure("the sidecar exited before it was ready", err)
		case <-ctx.Done():
			return &contract.Failure{
				Kind:    contract.Canceled,
				Path:    contract.PathLocal,
				Op:      "singbox.sync",
				Message: "the sidecar start was canceled",
				Err:     ctx.Err(),
			}
		default:
		}
		all := true
		for _, h := range handoffs {
			conn, err := net.DialTimeout("tcp", h.Address, dialProbeTimeout)
			if err != nil {
				all = false
				break
			}
			_ = conn.Close()
		}
		if all {
			return nil
		}
		if !time.Now().Before(deadline) {
			return failure("the sidecar did not become ready in time", nil)
		}
		time.Sleep(readyPollInterval)
	}
}

// writeConfig replaces the config atomically and keeps the credentials in it
// readable only by the owner. A partial document is never handed to exec.
func writeConfig(dir string, raw []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".sing-box-*.json")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	final := filepath.Join(dir, configFileName)
	if err := os.Rename(name, final); err != nil {
		return "", err
	}
	ok = true
	return final, nil
}

func failure(message string, cause error) error {
	return &contract.Failure{
		Kind:    contract.Internal,
		Path:    contract.PathLocal,
		Op:      "singbox.runtime",
		Message: message,
		Err:     cause,
	}
}
