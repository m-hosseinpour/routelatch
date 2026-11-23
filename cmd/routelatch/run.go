package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"routelatch/internal/app"
	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/instance"
	"routelatch/internal/store"
)

// run is the documented split from docs/contracts.md §7.5: plain run is the
// detached app/tray launcher, while --headless keeps the foreground engine
// behavior (A13/A15).
func (c *cli) run(ctx context.Context, args []string) int {
	fs := c.newFlags("run")
	headless := fs.Bool("headless", false, "run the engine in the foreground without the tray")
	if err := fs.Parse(args); err != nil {
		return c.localFailure("run", usageErr("invalid run flags"))
	}
	if fs.NArg() != 0 {
		return c.localFailure("run", usageErr("run takes no positional arguments"))
	}
	if *headless {
		return c.runHeadless(ctx)
	}
	return c.runApp(ctx)
}

// runApp starts the detached supervisor and returns only after the management
// socket reports status.ready=true and the tray registered.
func (c *cli) runApp(ctx context.Context) int {
	exe, err := os.Executable()
	if err != nil {
		return c.localFailure("run", err)
	}
	supervisorArgs := make([]string, 0, 3)
	if c.paths.DataDir != "" {
		supervisorArgs = append(supervisorArgs, "--data-dir", c.paths.DataDir)
	}
	supervisorArgs = append(supervisorArgs, "__app-supervisor")
	result, err := app.Launch(ctx, app.LaunchOptions{
		Command: app.Command{Path: exe, Args: supervisorArgs},
		LogDir:  c.paths.LogDir,
	})
	if err != nil {
		return c.printAppFailure(err)
	}
	data := map[string]any{
		"pid":       result.PID,
		"listeners": map[string]any{"http": result.Listeners.HTTP, "socks": result.Listeners.SOCKS},
		"log":       result.LogPath,
	}
	if result.AlreadyRunning {
		data["already_running"] = true
	}
	if c.jsonOut {
		return c.print(control.EnvelopeFor("run", data, nil))
	}
	if result.AlreadyRunning {
		fmt.Fprintf(c.stdout, "RouteLatch app is already running (pid %d)\n", result.PID)
	} else {
		fmt.Fprintf(c.stdout, "RouteLatch app started (pid %d)\n", result.PID)
	}
	fmt.Fprintf(c.stdout, "listeners: http %s, socks %s\nlog: %s\n",
		control.SanitizeTerminal(result.Listeners.HTTP), control.SanitizeTerminal(result.Listeners.SOCKS),
		control.SanitizeTerminal(result.LogPath))
	return int(contract.ExitOK)
}

func (c *cli) runHeadless(ctx context.Context) int {
	// An app supervisor owns this data dir even while its engine child is
	// starting. The engine lock alone is not enough to report that conflict.
	// The supervisor's own engine child is the one exception; it is still
	// serialized by state.lock, and it must be allowed to take that lock.
	if os.Getenv("ROUTELATCH_APP_ENGINE") != "1" {
		if err := app.RejectHeadlessConflict(c.paths.AppLock); err != nil {
			return c.printAppFailure(err)
		}
	}
	if parentCtx, cancel, err := app.ParentWatchFromEnv(); err != nil {
		return c.localFailure("run", err)
	} else if parentCtx != nil {
		ctx = parentCtx
		defer cancel()
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

// printAppFailure maps a documented app-mode failure to the same envelope and
// exit mapping every command uses.
func (c *cli) printAppFailure(err error) int {
	var af *app.Failure
	if errors.As(err, &af) {
		failure := &contract.Failure{Kind: af.Kind, Path: contract.PathLocal, Op: af.Op, Message: af.Message, Err: af.Err}
		env := control.EnvelopeFor("run", nil, failure)
		if env.Error != nil {
			env.Error.Detail = map[string]any{"exit_code": int(af.Exit)}
			if af.Reason != "" {
				env.Error.Detail["reason"] = af.Reason
			}
			if af.Fix != "" {
				env.Error.Detail["suggestion"] = af.Fix
			}
		}
		if c.jsonOut {
			_ = c.print(env)
		} else {
			fmt.Fprintln(c.stderr, "error:", env.Error.Message)
			if af.Fix != "" {
				fmt.Fprintln(c.stderr, "suggestion:", af.Fix)
			}
		}
		return int(af.Exit)
	}
	return c.localFailure("run", err)
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

// appSupervisor is the hidden child behind plain `run`. It is not a user
// command and never appears in help or the menu.
func (c *cli) appSupervisor(ctx context.Context) int {
	fd := 0
	if raw := os.Getenv("ROUTELATCH_READY_FD"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			fd = n
		}
	}
	err := app.RunSupervisor(ctx, app.SupervisorOptions{
		Paths: c.paths, LogDir: c.paths.LogDir, ReadyFD: fd,
	})
	if err != nil {
		var af *app.Failure
		if errors.As(err, &af) {
			return int(af.Exit)
		}
		return int(contract.ExitError)
	}
	return 0
}

// appTray is the hidden tray child. It exits when the supervisor pidfd becomes
// readable, on Quit, or when the StatusNotifier watcher/host disappears.
func (c *cli) appTray() int {
	opts, err := app.TrayOptionsFromEnv()
	if err != nil {
		return c.localFailure("run", err)
	}
	if err := app.RunTray(opts); err != nil {
		var af *app.Failure
		if errors.As(err, &af) {
			return int(af.Exit)
		}
		return int(contract.ExitError)
	}
	return 0
}
