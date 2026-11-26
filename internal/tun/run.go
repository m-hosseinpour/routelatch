package tun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
)

// RunOptions are the installed entrypoint's inputs. The paths are fixed by the
// unit/setup; tests pass private paths. Nothing here is reachable from the
// management socket or from user commands.
type RunOptions struct {
	ConfigPath string
	SocketPath string
	LeasePath  string
	Stderr     io.Writer
}

// Run is the privileged `__tun-service` body. It refuses a writable or
// non-root-owned binary/config, refuses to run as root or as a uid other than
// the configured one, loads the one fixed config, serves the control socket and
// exits after an explicit disable (or a signal).
func Run(ctx context.Context, opts RunOptions) error {
	cfg, err := loadRuntimeConfig(&opts)
	if err != nil {
		return err
	}
	if err := checkServiceIdentity(os.Geteuid(), cfg); err != nil {
		return err
	}
	return serveService(ctx, opts, cfg)
}

// loadRuntimeConfig applies the fixed defaults, verifies the installed files
// are root-owned and loads the root-owned service config. It fills opts so the
// caller serves the same paths it verified.
func loadRuntimeConfig(opts *RunOptions) (ServiceConfig, error) {
	if opts.ConfigPath == "" {
		opts.ConfigPath = DefaultConfigPath
	}
	if opts.SocketPath == "" {
		opts.SocketPath = ControlSocketPath
	}
	if opts.LeasePath == "" {
		opts.LeasePath = LeasePath
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if err := verifyRootOwnedFile(opts.ConfigPath); err != nil {
		return ServiceConfig{}, fmt.Errorf("the TUN service config is not trusted: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return ServiceConfig{}, fmt.Errorf("locate the TUN service binary: %w", err)
	}
	if err := verifyRootOwnedFile(exe); err != nil {
		return ServiceConfig{}, fmt.Errorf("the TUN service binary is not trusted: %w", err)
	}
	return LoadServiceConfig(opts.ConfigPath)
}

// checkServiceIdentity enforces the unit's non-root boundary. The TUN unit
// runs with User=<allowed uid> and only CAP_NET_ADMIN; root would widen the
// privilege surface and make the socket's SO_PEERCRED check meaningless. It is
// separate from serveService so tests can exercise the production entrypoint
// (Run) without weakening the guard or running the disposable harness as a
// second user.
func checkServiceIdentity(euid int, cfg ServiceConfig) error {
	if euid == 0 {
		return fmt.Errorf("the TUN service must not run as root")
	}
	if euid != cfg.AllowedUID {
		return fmt.Errorf("the TUN service must run as the configured user")
	}
	return nil
}

// serveService is the guarded body used by Run and by the disposable
// integration harness. It assumes the caller already verified RunOptions.
func serveService(ctx context.Context, opts RunOptions, cfg ServiceConfig) error {
	logger := log.New(opts.Stderr, "routelatch-tun: ", log.LstdFlags|log.LUTC)
	ln, _, err := listenControl(opts.SocketPath, cfg.AllowedUID)
	if err != nil {
		return err
	}

	svc, err := NewService(Options{
		Config:           cfg,
		CoreFactory:      NewXrayCoreFactory(),
		Network:          NewSystemNetwork(),
		LeasePath:        opts.LeasePath,
		Logger:           logger,
		ExitAfterDisable: true,
	})
	if err != nil {
		_ = ln.Close()
		return err
	}
	if err := svc.Serve(ctx, ln); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}
