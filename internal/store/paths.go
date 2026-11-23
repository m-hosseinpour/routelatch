package store

import (
	"fmt"
	"os"
	"path/filepath"

	"routelatch/internal/instance"
)

// Paths are the absolute locations of every persisted or runtime file the
// application owns (docs/contracts.md §4.1, §7.5). A --data-dir root places
// them all under one directory, which is what isolated tests use.
//
// InstanceDir is the canonical coordination directory: state.lock, app.lock,
// app.owner and control.sock live there and are derived from StateDir (or the
// data-dir root), never from XDG_RUNTIME_DIR. RuntimeDir is kept as the
// directory the store creates and doctor inspects; it points at InstanceDir.
type Paths struct {
	DataDir     string
	ConfigDir   string
	StateDir    string
	RuntimeDir  string
	InstanceDir string

	Config   string
	State    string
	Backup   string
	Lock     string
	AppLock  string
	AppOwner string
	Socket   string
	LogDir   string

	// LegacyLocks is the R12 migration-window lock set for this instance. The
	// engine probes and holds it after taking Lock (docs/contracts.md §7.5).
	LegacyLocks []instance.Legacy
}

// ResolvePaths returns the XDG paths (or one data-dir root). It does not create
// anything. Environment lookups go through os.Getenv so tests can set HOME and
// the XDG variables in a child process; tests in this package use
// ResolvePathsFor with explicit values.
func ResolvePaths(dataDir string) (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}
	return ResolvePathsFor(dataDir, os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_STATE_HOME"), os.Getenv("XDG_RUNTIME_DIR"), home)
}

// ResolvePathsFor is ResolvePaths with explicit environment inputs.
func ResolvePathsFor(dataDir, configHome, stateHome, runtimeDir, home string) (Paths, error) {
	if dataDir != "" {
		abs, err := filepath.Abs(dataDir)
		if err != nil {
			return Paths{}, fmt.Errorf("resolve data dir: %w", err)
		}
		instanceDir := instance.Dir(abs)
		return Paths{
			DataDir:     abs,
			ConfigDir:   abs,
			StateDir:    abs,
			RuntimeDir:  instanceDir,
			InstanceDir: instanceDir,
			Config:      filepath.Join(abs, "config.json"),
			State:       filepath.Join(abs, "state.json"),
			Backup:      filepath.Join(abs, "backup"),
			Lock:        filepath.Join(instanceDir, "state.lock"),
			AppLock:     filepath.Join(instanceDir, "app.lock"),
			AppOwner:    filepath.Join(instanceDir, "app.owner"),
			Socket:      filepath.Join(instanceDir, "control.sock"),
			LogDir:      filepath.Join(abs, "log"),
			LegacyLocks: instance.LegacyPaths(abs, abs, "", os.Geteuid()),
		}, nil
	}
	if home == "" {
		return Paths{}, fmt.Errorf("home directory is empty; set --data-dir")
	}
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	if stateHome == "" {
		stateHome = filepath.Join(home, ".local", "state")
	}
	configDir := filepath.Join(configHome, "routelatch")
	stateDir := filepath.Join(stateHome, "routelatch")
	instanceDir := instance.Dir(stateDir)
	return Paths{
		ConfigDir:   configDir,
		StateDir:    stateDir,
		RuntimeDir:  instanceDir,
		InstanceDir: instanceDir,
		Config:      filepath.Join(configDir, "config.json"),
		State:       filepath.Join(stateDir, "state.json"),
		Backup:      filepath.Join(stateDir, "backup"),
		Lock:        filepath.Join(instanceDir, "state.lock"),
		AppLock:     filepath.Join(instanceDir, "app.lock"),
		AppOwner:    filepath.Join(instanceDir, "app.owner"),
		Socket:      filepath.Join(instanceDir, "control.sock"),
		LogDir:      filepath.Join(stateDir, "log"),
		LegacyLocks: instance.LegacyPaths("", stateDir, runtimeDir, os.Geteuid()),
	}, nil
}
