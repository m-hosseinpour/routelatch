package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"routelatch/internal/contract"
	"routelatch/internal/tun"
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
