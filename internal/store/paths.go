package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// Paths are the absolute locations of every persisted or runtime file the
// application owns (docs/contracts.md §4.1). A --data-dir root places them all
// under one directory, which is what isolated tests use.
type Paths struct {
	DataDir    string
	ConfigDir  string
	StateDir   string
	RuntimeDir string

	Config string
	State  string
	Backup string
	Lock   string
	Socket string
	LogDir string
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
		return Paths{
			DataDir:    abs,
			ConfigDir:  abs,
			StateDir:   abs,
			RuntimeDir: abs,
			Config:     filepath.Join(abs, "config.json"),
			State:      filepath.Join(abs, "state.json"),
			Backup:     filepath.Join(abs, "backup"),
			Lock:       filepath.Join(abs, "state.lock"),
			Socket:     filepath.Join(abs, "run", "control.sock"),
			LogDir:     filepath.Join(abs, "log"),
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
	explicitRuntime := runtimeDir != ""
	if runtimeDir == "" {
		runtimeDir = stateHome
	}
	configDir := filepath.Join(configHome, "routelatch")
	stateDir := filepath.Join(stateHome, "routelatch")
	runtime := filepath.Join(runtimeDir, "routelatch")
	socket := filepath.Join(runtime, "control.sock")
	if !explicitRuntime {
		socket = filepath.Join(stateDir, "run", "control.sock")
	}
	return Paths{
		ConfigDir:  configDir,
		StateDir:   stateDir,
		RuntimeDir: runtime,
		Config:     filepath.Join(configDir, "config.json"),
		State:      filepath.Join(stateDir, "state.json"),
		Backup:     filepath.Join(stateDir, "backup"),
		Lock:       filepath.Join(runtime, "state.lock"),
		Socket:     socket,
		LogDir:     filepath.Join(stateDir, "log"),
	}, nil
}
