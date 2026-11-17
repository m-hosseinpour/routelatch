package main

import (
	"fmt"

	"routelatch/internal/control"
)

// Build metadata is injected by scripts/build.sh:
//
//	-X main.version=... -X main.commit=... -X main.buildDate=...
//
// The defaults keep `go run` and `go test` honest instead of pretending to be a
// release; `routelatch version` reports the unstamped state explicitly.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// VersionData is the `version` payload.
type VersionData struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
}

func currentVersion() VersionData {
	return VersionData{
		Version:   control.SanitizeTerminal(version),
		Commit:    control.SanitizeTerminal(commit),
		BuildDate: control.SanitizeTerminal(buildDate),
	}
}

// versionCommand needs no service, socket or state directory: it only reports
// the values the linker put into this binary.
func (c *cli) versionCommand() int {
	data := currentVersion()
	if c.jsonOut {
		return c.print(control.EnvelopeFor("version", data, nil))
	}
	if data.Version == "dev" {
		fmt.Fprintln(c.stdout, "RouteLatch dev (unstamped build)")
	} else {
		fmt.Fprintf(c.stdout, "RouteLatch %s\n", data.Version)
	}
	fmt.Fprintf(c.stdout, "commit: %s\n", data.Commit)
	fmt.Fprintf(c.stdout, "build date: %s\n", data.BuildDate)
	return 0
}
