package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"routelatch/internal/control"
	"routelatch/internal/platform"
	"routelatch/internal/service"
	"routelatch/internal/store"
)

// Production wiring for the opt-in systemd user unit. Tests replace these seams
// instead of touching the real user manager, loginctl or instance locks.
var (
	serviceSystem           platform.CommandRunner = platform.ExecRunner{}
	serviceExecutable                              = os.Executable
	serviceGetenv                                  = os.Getenv
	serviceHomeDir                                 = os.UserHomeDir
	serviceSyncDir          func(string) error
	serviceReadinessTimeout = 10 * time.Second
	servicePollInterval     = 200 * time.Millisecond
	serviceReadinessFactory = func(paths store.Paths) service.Readiness {
		return service.SocketReadiness(paths.Socket)
	}
	// nil selects service.ProbeOtherEngines. Tests that run against a custom
	// XDG tree set this to skip host runtime locks.
	serviceProbeLocks func(context.Context, store.Paths) error
)

func (c *cli) serviceCmd(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return c.localFailure("service", usageErr("service needs install, uninstall or status"))
	}
	verb := args[0]
	command := "service " + verb
	force := false
	switch verb {
	case "install":
		fs := c.newFlags("service install")
		fs.BoolVar(&force, "force", false, "replace an existing unit after the previous one is inactive")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return c.localFailure(command, usageErr("service install takes only --force"))
		}
	case "uninstall", "status":
		if len(args) != 1 {
			return c.localFailure(command, usageErr("service "+verb+" takes no arguments"))
		}
	default:
		return c.localFailure("service", usageErr("service needs install, uninstall or status"))
	}
	home, _ := serviceHomeDir()
	manager := &service.Manager{
		Runner:           serviceSystem,
		Getenv:           serviceGetenv,
		Home:             home,
		Paths:            c.paths,
		DataDirSet:       c.dataDirSet,
		Force:            force,
		Ready:            serviceReadinessFactory(c.paths),
		ReadinessTimeout: serviceReadinessTimeout,
		PollInterval:     servicePollInterval,
		ProbeLocks:       serviceProbeLocks,
		SyncDir:          serviceSyncDir,
	}
	if verb == "install" {
		exe, err := serviceExecutable()
		if err != nil {
			return c.serviceFailure(command, err)
		}
		manager.Executable = exe
	}
	var status service.Status
	var err error
	switch verb {
	case "install":
		status, err = manager.Install(ctx)
	case "uninstall":
		status, err = manager.Uninstall(ctx)
	case "status":
		status, err = manager.Status(ctx)
	}
	if err != nil {
		return c.serviceFailure(command, err)
	}
	data := map[string]any{
		"unit":    status.Unit,
		"enabled": status.Enabled,
		"active":  status.Active,
		"linger":  status.Linger,
		"managed": status.Managed,
	}
	if status.Conflict != "" {
		data["conflict"] = status.Conflict
	}
	code := c.print(control.EnvelopeFor(command, data, nil))
	if verb == "install" && status.Linger == "no" {
		if name, nameErr := manager.UserName(); nameErr == nil && name != "" {
			fmt.Fprintln(c.stderr, service.LingerHint(name))
		}
	}
	return code
}

// serviceFailure mirrors autostartFailure: the documented exit code comes from
// the OpError/envelope, and reason/suggestion detail reaches stderr.
func (c *cli) serviceFailure(command string, err error) int {
	env := control.EnvelopeFor(command, nil, err)
	if c.jsonOut {
		_ = c.print(env)
		return int(control.ExitCodeForEnvelope(env))
	}
	if env.Error != nil {
		fmt.Fprintln(c.stderr, "error:", env.Error.Message)
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
