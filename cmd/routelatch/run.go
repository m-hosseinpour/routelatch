package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/instance"
	"routelatch/internal/store"
)

// run is the foreground engine in this phase. --headless is the explicit name
// for that behavior (contracts §7.5, A13); the detached app/tray launcher that
// becomes the plain-run default is a later unit, so both spellings currently
// take the same path.
func (c *cli) run(ctx context.Context, args []string) int {
	fs := c.newFlags("run")
	_ = fs.Bool("headless", false, "run the engine in the foreground without the tray")
	if err := fs.Parse(args); err != nil {
		return c.localFailure("run", usageErr("invalid run flags"))
	}
	if fs.NArg() != 0 {
		return c.localFailure("run", usageErr("run takes no positional arguments"))
	}

	st, err := store.Open(store.Options{Paths: c.paths})
	if err != nil {
		return c.runFailure(err, false)
	}
	lock, err := st.Acquire(ctx)
	if err != nil {
		return c.runFailure(err, false)
	}
	defer lock.Release()

	// The R13 migration window: hold the R12 locks for this data dir so a
	// forgotten old unit is stopped instead of serving a second engine. The
	// canonical lock above is already held, so this cannot race another new
	// process.
	legacy, err := instance.AcquireLegacy(ctx, c.paths.LegacyLocks)
	if err != nil {
		return c.runFailure(err, false)
	}
	defer func() { _ = legacy.Close() }()

	svc, err := control.NewService(ctx, control.Options{Store: st, Paths: c.paths, LogOutput: c.serviceLogOutput()})
	if err != nil {
		return c.runFailure(err, false)
	}
	server, err := control.NewServer(control.ServerOptions{
		Path: c.paths.Socket, Handler: svc, AllowStale: true,
	})
	if err != nil {
		return c.runFailure(err, false)
	}
	if err := server.Start(ctx); err != nil {
		return c.runFailure(err, false)
	}
	if err := svc.StartNetwork(ctx); err != nil {
		_ = server.Close()
		_ = svc.Close(context.Background())
		return c.runFailure(err, true)
	}

	snap := svc.Snapshot()
	if snap.State.Mode == contract.ModeStopped {
		fmt.Fprintln(c.stderr, "RouteLatch is stopped; management is available at", control.SanitizeTerminal(c.paths.Socket))
	} else {
		fmt.Fprintf(c.stderr, "RouteLatch listening: http %s, socks %s (mode %s, policy %d)\n",
			control.SanitizeTerminal(snap.Config.HTTPListen), control.SanitizeTerminal(snap.Config.SOCKSListen),
			snap.State.Mode, snap.State.PolicyVersion)
	}

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-sigCtx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = server.Close()
	closeErr := svc.Close(shutdownCtx)
	if closeErr != nil {
		fmt.Fprintln(c.stderr, "shutdown:", closeErr.Error())
	}
	if c.jsonOut {
		var env contract.Envelope
		if closeErr != nil {
			env = control.EnvelopeFor("run", nil, closeErr)
		} else {
			env = control.EnvelopeFor("run", map[string]any{"http": snap.Config.HTTPListen, "socks": snap.Config.SOCKSListen}, nil)
		}
		_ = c.print(env)
	}
	if closeErr != nil {
		return int(contract.ExitError)
	}
	return int(contract.ExitOK)
}

// serviceLogOutput keeps --json stdout reserved for the single envelope; in
// human mode the operational log mirrors to stdout for foreground use.
func (c *cli) serviceLogOutput() io.Writer {
	if c.jsonOut {
		return c.stderr
	}
	return c.stdout
}

func (c *cli) runFailure(err error, afterNetworkStart bool) int {
	code := contract.ExitError
	var legacy *instance.ExitError
	var failure *contract.Failure
	switch {
	case errors.As(err, &legacy):
		code = legacy.Exit
	case errors.As(err, &failure):
		switch failure.Kind {
		case contract.StateCorrupt:
			code = contract.ExitStateCorrupt
		case contract.StateLocked:
			if afterNetworkStart {
				code = contract.ExitConflict // listener/port collision
			} else {
				code = contract.ExitAlreadyRunning // documented singleton exit
			}
		case contract.PolicyDenied, contract.PeerRejected:
			code = contract.ExitDenied
		case contract.Unsupported:
			code = contract.ExitUnsupported
		}
	}
	env := control.EnvelopeFor("run", nil, err)
	if c.jsonOut {
		_ = c.print(env)
	} else if env.Error != nil {
		fmt.Fprintln(c.stderr, "error:", env.Error.Message)
	}
	return int(code)
}

func (c *cli) exec(ctx context.Context, args []string) int {
	for len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return c.localFailure("exec", usageErr("exec needs a command after --"))
	}
	env, callErr := c.client().Call(ctx, control.Request{Op: control.OpEnv})
	if callErr != nil {
		if errors.Is(callErr, control.ErrNotRunning) {
			return c.localFailure("exec", &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "cli.exec", Message: "RouteLatch is not running; refusing to start the child without a proxy"})
		}
		return c.localFailure("exec", callErr)
	}
	if !env.OK {
		return c.print(env)
	}
	data, ok := env.Data.(map[string]any)
	if !ok {
		return c.localFailure("exec", &contract.Failure{Kind: contract.Internal, Path: contract.PathLocal, Op: "cli.exec", Message: "env response is incomplete"})
	}
	childEnv := replaceProxyEnv(os.Environ(), data)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = childEnv
	cmd.Stdin = os.Stdin
	childOut := io.Writer(c.stdout)
	childErr := io.Writer(c.stderr)
	if c.jsonOut {
		// stdout is reserved for the single machine-readable envelope. The
		// child's own stdout and stderr both stay on stderr so they are never
		// interleaved into (or glued to) that JSON.
		childOut = c.stderr
		childErr = c.stderr
	}
	cmd.Stdout = childOut
	cmd.Stderr = childErr
	runErr := cmd.Run()
	if notice, _ := data["notice"].(string); notice != "" {
		fmt.Fprintln(c.stderr, notice)
	}
	code := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = int(contract.ExitError)
			fmt.Fprintln(c.stderr, "exec:", runErr.Error())
		}
	}
	if c.jsonOut {
		result := control.EnvelopeFor("exec", map[string]any{"exit_code": code}, nil)
		_ = c.print(result)
	}
	return code
}

func replaceProxyEnv(inherited []string, values map[string]any) []string {
	keys := map[string]bool{
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
		"http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true,
	}
	out := make([]string, 0, len(inherited)+8)
	for _, entry := range inherited {
		key := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			key = entry[:i]
		}
		if keys[key] {
			continue
		}
		out = append(out, entry)
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"} {
		if v, ok := values[key].(string); ok && v != "" {
			out = append(out, key+"="+v)
		}
	}
	for _, key := range []string{"http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		upper := strings.ToUpper(key)
		if v, ok := values[upper].(string); ok && v != "" {
			out = append(out, key+"="+v)
		}
	}
	return out
}
