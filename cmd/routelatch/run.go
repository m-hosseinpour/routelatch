package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/store"
)

func (c *cli) run(ctx context.Context) int {
	st, err := store.Open(store.Options{Paths: c.paths})
	if err != nil {
		return c.runFailure(err, false)
	}
	lock, err := st.Acquire(ctx)
	if err != nil {
		return c.runFailure(err, false)
	}
	defer lock.Release()

	svc, err := control.NewService(ctx, control.Options{Store: st, Paths: c.paths})
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

func (c *cli) runFailure(err error, afterNetworkStart bool) int {
	code := contract.ExitError
	var failure *contract.Failure
	if errors.As(err, &failure) {
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
	cmd.Stdout = c.stdout
	cmd.Stderr = c.stderr
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
