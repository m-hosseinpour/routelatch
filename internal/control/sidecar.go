package control

import (
	"os"
	"path/filepath"
	"strings"

	"routelatch/internal/singbox"
	"routelatch/internal/store"
)

// The sidecar executable is caller-configured; RouteLatch never searches PATH
// and never downloads or installs a binary. The canonical variable is first,
// the others are accepted aliases so an operator's existing naming still works.
// A packaged `sing-box` next to the running executable is the safe default.
const (
	sidecarExecutableEnv  = "ROUTELATCH_SINGBOX_EXEC"
	sidecarExecutableAlt  = "ROUTELATCH_SINGBOX_EXECUTABLE"
	sidecarExecutableAlt2 = "ROUTELATCH_SINGBOX"
	sidecarBinaryName     = "sing-box"
	sidecarConfigDirName  = "sidecar"
)

// initSidecar selects the runtime once, at service construction. An injected
// runtime wins; otherwise an explicit path, an environment variable, or the
// packaged sibling binary is used. With none of those the runtime stays nil
// and engine.prepareSidecar fails hysteria2 exits closed with
// ReasonSidecarUnavailable (Xray exits are unaffected).
func (s *Service) initSidecar(opts Options, paths store.Paths) {
	if opts.Sidecar != nil {
		s.sidecar = opts.Sidecar
		return
	}
	executable := resolveSidecarExecutable(opts.SidecarExecutable)
	if executable == "" {
		return
	}
	dir := paths.StateDir
	if dir == "" {
		dir = opts.Store.Paths().StateDir
	}
	if strings.TrimSpace(dir) == "" {
		// No private place for the generated config: fail closed rather than
		// writing credentials somewhere implied.
		return
	}
	rt, err := singbox.New(singbox.Options{
		Executable: executable,
		ConfigDir:  filepath.Join(dir, sidecarConfigDirName),
	})
	if err != nil {
		// The message carries no path or credential.
		s.logger.Logf(levelWarning, "sidecar.init", "the hysteria2 sidecar could not be configured; those exits will be unavailable")
		return
	}
	s.sidecar = rt
	s.logger.Logf(levelInfo, "sidecar.init", "hysteria2 sidecar configured")
}

// resolveSidecarExecutable returns the configured executable without ever
// searching PATH. Relative values are made absolute, so a bare name resolves
// against the working directory rather than becoming an implicit PATH lookup.
func resolveSidecarExecutable(explicit string) string {
	for _, candidate := range []string{
		explicit,
		os.Getenv(sidecarExecutableEnv),
		os.Getenv(sidecarExecutableAlt),
		os.Getenv(sidecarExecutableAlt2),
	} {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if filepath.IsAbs(candidate) {
			return candidate
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return ""
		}
		return abs
	}
	// Packaged layout: bin/sing-box sits next to bin/routelatch. Only use it
	// when the file actually exists, so a fresh source checkout fails closed.
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	packaged := filepath.Join(filepath.Dir(exe), sidecarBinaryName)
	if fi, err := os.Stat(packaged); err == nil && !fi.IsDir() {
		return packaged
	}
	return ""
}
