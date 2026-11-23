package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"routelatch/internal/app"
	"routelatch/internal/autostart"
	"routelatch/internal/control"
	"routelatch/internal/platform"
	"routelatch/internal/systemd"
)

// The CLI owns the production wiring for the app-mode capability probes and
// the user-manager lookup; tests replace these seams instead of touching the
// session bus, pidfd or systemctl.
var (
	appTrayProbe                                = app.ProbeTray
	appPIDFDProbe                               = app.ProbePIDFD
	autostartExecutable                         = os.Executable
	autostartHomeDir                            = os.UserHomeDir
	autostartGetenv                             = os.Getenv
	autostartSystem      platform.CommandRunner = platform.ExecRunner{}
	autostartServiceUnit                        = detectManagedServiceUnit
)

// detectManagedServiceUnit resolves the user manager config base and inspects
// the unit RouteLatch would install. A failed base lookup still returns the
// shell fallback so a managed unit is not missed; the lookup error is only
// reported when no base is available at all.
func detectManagedServiceUnit(ctx context.Context) (systemd.Unit, error) {
	home, _ := autostartHomeDir()
	cfg := systemd.Config{Runner: autostartSystem, Getenv: autostartGetenv, Home: home}
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	base, _, err := cfg.ConfigBase(lookupCtx)
	if base == "" {
		return systemd.Unit{}, err
	}
	return systemd.ReadUnit(systemd.UnitPath(base))
}

func (c *cli) autostart(ctx context.Context, args []string) int {
	if len(args) != 1 {
		return c.localFailure("autostart", usageErr("autostart needs enable, disable or status"))
	}
	verb := args[0]
	command := "autostart " + verb
	home, _ := autostartHomeDir()
	base, err := autostart.ConfigBase(autostartGetenv, home)
	if err != nil {
		return c.autostartFailure(command, err)
	}
	manager := &autostart.Manager{
		ConfigBase:  base,
		ProbeTray:   appTrayProbe,
		ProbePIDFD:  appPIDFDProbe,
		ServiceUnit: autostartServiceUnit,
	}
	var status autostart.Status
	switch verb {
	case "status":
		status, err = manager.Status(ctx)
	case "enable":
		exe, exeErr := autostartExecutable()
		if exeErr != nil {
			return c.autostartFailure(command, exeErr)
		}
		manager.Executable = exe
		status, err = manager.Enable(ctx)
	case "disable":
		status, err = manager.Disable(ctx)
	default:
		return c.localFailure("autostart", usageErr("autostart needs enable, disable or status"))
	}
	if err != nil {
		return c.autostartFailure(command, err)
	}
	data := map[string]any{
		"path":            status.Path,
		"enabled":         status.Enabled,
		"managed":         status.Managed,
		"tray_available":  status.TrayAvailable,
		"pidfd_available": status.PIDFDAvailable,
	}
	if status.Conflict != "" {
		data["conflict"] = status.Conflict
	}
	return c.print(control.EnvelopeFor(command, data, nil))
}

// autostartFailure mirrors printAppFailure: the reason and suggestion ride in
// the error detail, and the documented exit code comes from the envelope.
func (c *cli) autostartFailure(command string, err error) int {
	env := control.EnvelopeFor(command, nil, err)
	if c.jsonOut {
		_ = c.print(env)
		return int(control.ExitCodeForEnvelope(env))
	}
	if env.Error != nil {
		fmt.Fprintln(c.stderr, "error:", env.Error.Message)
		if suggestion, _ := env.Error.Detail["suggestion"].(string); suggestion != "" {
			fmt.Fprintln(c.stderr, "suggestion:", suggestion)
		}
		if reason, _ := env.Error.Detail["reason"].(string); reason != "" {
			fmt.Fprintln(c.stderr, "reason:", reason)
		}
	}
	var opErr *control.OpError
	if errors.As(err, &opErr) && opErr.Exit != 0 {
		return int(opErr.Exit)
	}
	return int(control.ExitCodeForEnvelope(env))
}
