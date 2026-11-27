package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/platform"
	"routelatch/internal/tun"
	tunsetup "routelatch/internal/tun/setup"
)

// tunService is the hidden privileged entrypoint run by the installed
// routelatch-tun system unit. It is not a user command: it takes no interface,
// route, domain or address, and its only public config comes from the
// root-owned file written by `routelatch tun setup` (R16-G).
func (c *cli) tunService(ctx context.Context, args []string) int {
	fs := c.newFlags("__tun-service")
	config := fs.String("config", tun.DefaultConfigPath, "root-owned TUN service config")
	if err := fs.Parse(args); err != nil {
		return c.localFailure("__tun-service", usageErr("invalid __tun-service arguments"))
	}
	if fs.NArg() != 0 {
		return c.localFailure("__tun-service", usageErr("__tun-service takes no positional arguments"))
	}
	runCtx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := tun.Run(runCtx, tun.RunOptions{ConfigPath: *config, Stderr: c.stderr}); err != nil {
		fmt.Fprintln(c.stderr, "tun service:", err)
		return int(contract.ExitError)
	}
	return int(contract.ExitOK)
}

// newTunSetupManager is the production factory for the privileged helper
// install. Tests replace it with the same Manager wired to a private tree and
// a fake systemctl, so no test writes /etc, /usr or /run.
var newTunSetupManager = func(force bool, socks string) *tunsetup.Manager {
	return &tunsetup.Manager{
		Runner:       platform.ExecRunner{},
		Getenv:       os.Getenv,
		Euid:         os.Geteuid,
		Getuid:       os.Getuid,
		Paths:        tunsetup.DefaultPaths(),
		Force:        force,
		SocksAddress: socks,
	}
}

// tunCmd is the public, non-privileged TUN command surface. status/on/off are
// thin aliases for the documented management operations; setup/uninstall are
// intercepted before instance paths are resolved and never use the socket.
func (c *cli) tunCmd(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return c.localFailure("tun", usageErr("tun needs status, on, off, setup or uninstall"))
	}
	verb := args[0]
	switch verb {
	case "status", "on", "off":
		if hasHelp(args[1:]) {
			c.tunUsage()
			return int(contract.ExitOK)
		}
		if len(args) != 1 {
			return c.localFailure("tun "+verb, usageErr("tun "+verb+" takes no arguments"))
		}
		op := control.OpTunStatus
		switch verb {
		case "on":
			op = control.OpTunEnable
		case "off":
			op = control.OpTunDisable
		}
		return c.tunCall(ctx, control.Request{Op: op})
	case "setup", "uninstall":
		// Execute intercepts these before store paths, but the menu and tests
		// may still reach them here. They never use the management socket.
		return c.tunPrivileged(ctx, args)
	case "help", "--help", "-h":
		c.tunUsage()
		return int(contract.ExitOK)
	default:
		return c.localFailure("tun", usageErr("unknown tun command: "+verb))
	}
}

// tunCall is c.call for status/on/off. It keeps the documented exit mapping and
// adds the stable reason to human stderr, while JSON stays in the one Envelope.
func (c *cli) tunCall(ctx context.Context, req control.Request) int {
	env, err := c.client().Call(ctx, req)
	if err != nil {
		if errors.Is(err, control.ErrNotRunning) {
			return c.localFailure(control.CommandName(req.Op), &contract.Failure{
				Kind: contract.Stopped, Path: contract.PathLocal, Op: "cli",
				Message: "RouteLatch is not running (management socket is absent); start it with 'routelatch run'",
			})
		}
		return c.localFailure(control.CommandName(req.Op), err)
	}
	return c.print(env)
}

// tunPrivileged runs setup or uninstall. It requires the caller to be root and
// (for setup) to name a non-root invoking user; it deliberately bypasses
// store.ResolvePaths so sudo's root home never chooses an instance.
func (c *cli) tunPrivileged(ctx context.Context, args []string) int {
	verb := args[0]
	command := "tun " + verb
	switch verb {
	case "setup":
		if hasHelp(args[1:]) {
			c.tunSetupUsage()
			return int(contract.ExitOK)
		}
		fs := c.newFlags(command)
		force := fs.Bool("force", false, "replace a unit/config that RouteLatch does not own (after stopping it)")
		socks := fs.String("socks", tunsetup.DefaultSocksAddress, "engine loopback SOCKS listener")
		if err := fs.Parse(args[1:]); err != nil {
			return c.localFailure(command, usageErr("invalid tun setup arguments"))
		}
		if fs.NArg() != 0 {
			return c.localFailure(command, usageErr("tun setup takes only --force and --socks"))
		}
		status, err := newTunSetupManager(*force, *socks).Install(ctx)
		if err != nil {
			return c.tunFailure(command, err)
		}
		return c.print(control.EnvelopeFor(command, tunSetupData(status), nil))
	case "uninstall":
		if hasHelp(args[1:]) {
			c.tunSetupUsage()
			return int(contract.ExitOK)
		}
		if len(args) != 1 {
			return c.localFailure(command, usageErr("tun uninstall takes no arguments"))
		}
		status, err := newTunSetupManager(false, "").Uninstall(ctx)
		if err != nil {
			return c.tunFailure(command, err)
		}
		return c.print(control.EnvelopeFor(command, tunSetupData(status), nil))
	default:
		return c.localFailure("tun", usageErr("unknown tun command: "+verb))
	}
}

func hasHelp(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "help", "--help", "-h":
			return true
		}
	}
	return false
}

func tunSetupData(status tunsetup.Status) map[string]any {
	data := map[string]any{
		"unit":        status.Unit,
		"socket_unit": status.SocketUnit,
		"config":      status.Config,
		"binary":      status.Binary,
		"tmpfiles":    status.Tmpfiles,
		"changed":     status.Changed,
		"managed":     status.Managed,
	}
	// uninstall has no target uid when no owned unit remains; reporting 0
	// would falsely name root as the allowed engine user.
	if status.AllowedUID > 0 {
		data["allowed_uid"] = status.AllowedUID
	}
	if len(status.Removed) > 0 {
		data["removed"] = status.Removed
	}
	return data
}

// tunFailure prints the one Envelope in JSON mode; human mode adds the fixed
// reason. It never prints runtime text beyond the documented safe message.
func (c *cli) tunFailure(command string, err error) int {
	env := control.EnvelopeFor(command, nil, err)
	if c.jsonOut {
		return c.print(env)
	}
	if env.Error != nil {
		fmt.Fprintln(c.stderr, "error:", env.Error.Message)
		if reason, _ := env.Error.Detail["reason"].(string); reason != "" {
			fmt.Fprintln(c.stderr, "reason:", reason)
		}
	}
	return int(control.ExitCodeForEnvelope(env))
}

func (c *cli) tunUsage() {
	fmt.Fprintln(c.stderr, `Usage: routelatch [--json] tun <command>

  status      show system-wide TUN coverage and its safe reason
  on          enable coverage (only while mode is not stopped)
  off         disable coverage and clear the saved preference
  setup       install the privileged helper (sudo; CLI only)
  uninstall   remove only the helper installed by setup (sudo; CLI only)

TUN is an overlay on targeted|all-proxy|all-direct. Plain DNS stays direct and
non-DNS UDP stays blocked. The helper is installed once with:
  sudo routelatch tun setup
Run 'routelatch tun help' for this text.`)
}

func (c *cli) tunSetupUsage() {
	fmt.Fprintln(c.stderr, `Usage: sudo routelatch tun setup [--socks HOST:PORT] [--force]
       sudo routelatch tun uninstall

setup writes root-owned /etc/systemd/system/routelatch-tun.{service,socket},
/etc/routelatch/tun.json, /etc/tmpfiles.d/routelatch-tun.conf and a root-owned
helper binary under /usr/libexec/routelatch/. The tmpfiles entry recreates
/run/routelatch-tun after boot so the non-root engine can reach the 0600
control socket. It records the invoking user's uid from SUDO_UID and refuses to
install for root. --socks must be a loopback IP literal (default 127.0.0.1:8318);
pass it when the engine listens elsewhere. A unit/config/tmpfiles entry that
RouteLatch does not own is only replaced with --force, and a foreign unit only
while it is inactive. A failed install restores the previous files.

uninstall stops and disables the socket before the service, then removes only
files carrying the RouteLatch marker, the proven root-owned binary/config, and
the known runtime lease/socket. It never removes user state or unrelated
settings. If the engine is gone, this command and
'systemctl stop routelatch-tun.socket routelatch-tun.service' are the
independent recovery path.`)
}
