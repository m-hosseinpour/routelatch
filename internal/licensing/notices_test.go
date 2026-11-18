// Package licensing guards the distribution contract for the binary's
// third-party licenses without needing a toolchain, a module cache or a
// network. It compares the committed notices against go.mod rather than
// re-running `go list`, so it stays cheap and deterministic in every test run.
// The tradeoff is recorded in docs/licensing.md: a go.mod requirement that is
// never linked must be listed explicitly as excluded, and the full `go list
// -deps` audit has to be repeated when dependencies change.
package licensing

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	noticesFile = "THIRD-PARTY-NOTICES.md"
	linkedHead  = "## Modules linked into the binary"
	excluded    = "## Modules in `go.mod` but not linked into the Linux binary"
)

var (
	requireLine = regexp.MustCompile(`^\s*require\s+(\S+)\s+(\S+)`)
	requireItem = regexp.MustCompile(`^\s*(\S+)\s+(v\S+)\s*(?://.*)?$`)
	tableRow    = regexp.MustCompile("^\\|\\s*`([^`]+)`\\s*\\|\\s*([^|]+?)\\s*\\|\\s*([^|]+?)\\s*\\|")
	summaryRow  = regexp.MustCompile(`^\|\s*\*\*Total\*\*\s*\|\s*\*\*(\d+)\*\*\s*\|$`)
	knownBase   = map[string]bool{
		"MIT": true, "BSD-2-Clause": true, "BSD-3-Clause": true,
		"Apache-2.0": true, "MPL-2.0": true, "LGPL-3.0-only": true,
	}
)

func repoFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func goMod(t *testing.T) map[string]string {
	t.Helper()
	mods := make(map[string]string)
	inBlock := false
	sc := bufio.NewScanner(strings.NewReader(repoFile(t, "go.mod")))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(strings.TrimSpace(line), "require ("):
			inBlock = true
		case inBlock && strings.HasPrefix(strings.TrimSpace(line), ")"):
			inBlock = false
		case inBlock:
			if m := requireItem.FindStringSubmatch(line); m != nil {
				mods[m[1]] = m[2]
			}
		default:
			if m := requireLine.FindStringSubmatch(line); m != nil {
				mods[m[1]] = m[2]
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan go.mod: %v", err)
	}
	if len(mods) == 0 {
		t.Fatal("go.mod has no requirements")
	}
	return mods
}

// parseNotices returns the linked and excluded module tables plus the summary
// total. It fails on a malformed or duplicate row so a broken table cannot pass
// silently.
func parseNotices(t *testing.T, text string) (linked map[string]string, reasons map[string]string, total int) {
	t.Helper()
	linked = make(map[string]string)
	reasons = make(map[string]string)
	section := ""
	total = -1
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
			section = line
			continue
		}
		if m := summaryRow.FindStringSubmatch(line); m != nil {
			if total != -1 {
				t.Fatalf("notices have more than one summary total row")
			}
			total = atoi(t, m[1])
			continue
		}
		if section != linkedHead && section != excluded {
			continue
		}
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		m := tableRow.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("malformed notices row in %q: %q", section, line)
		}
		mod, version, extra := m[1], m[2], m[3]
		if _, dup := linked[mod]; dup {
			t.Fatalf("module %s appears twice in the notices tables", mod)
		}
		if _, dup := reasons[mod]; dup {
			t.Fatalf("module %s appears twice in the notices tables", mod)
		}
		if section == linkedHead {
			if !strings.HasPrefix(version, "v") {
				t.Fatalf("linked module %s has malformed version %q", mod, version)
			}
			linked[mod] = version
			if extra == "" || strings.Contains(strings.ToUpper(extra), "UNKNOWN") {
				t.Fatalf("linked module %s has no usable license", mod)
			}
			checkSPDX(t, mod, extra)
			continue
		}
		if !strings.HasPrefix(version, "v") {
			t.Fatalf("excluded module %s has malformed version %q", mod, version)
		}
		if strings.TrimSpace(extra) == "" {
			t.Fatalf("excluded module %s has no reason", mod)
		}
		reasons[mod] = version
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan notices: %v", err)
	}
	if total < 0 {
		t.Fatal("notices have no summary total row")
	}
	return linked, reasons, total
}

func checkSPDX(t *testing.T, mod, expr string) {
	t.Helper()
	if strings.Contains(strings.ToUpper(expr), "UNKNOWN") {
		t.Fatalf("module %s has unknown license %q", mod, expr)
	}
	for _, part := range regexp.MustCompile(`\s+(AND|OR|WITH)\s+`).Split(expr, -1) {
		part = strings.TrimSpace(part)
		if part == "static-linking-exception" {
			continue
		}
		if !knownBase[part] {
			t.Fatalf("module %s has unrecognized license term %q (add the audit decision before shipping)", mod, part)
		}
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("not a number: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func TestLicenseFilesExist(t *testing.T) {
	license := repoFile(t, "LICENSE")
	for _, want := range []string{
		"Apache License",
		"Version 2.0, January 2004",
		"END OF TERMS AND CONDITIONS",
		"Copyright [yyyy] [name of copyright owner]",
	} {
		if !strings.Contains(license, want) {
			t.Errorf("LICENSE is not the canonical Apache-2.0 text: missing %q", want)
		}
	}
	notice := repoFile(t, "NOTICE")
	// Rewrap-independent: the file breaks lines for readability.
	flatNotice := strings.Join(strings.Fields(notice), " ")
	for _, want := range []string{
		"Copyright 2026 Mahdi Hosseinpour",
		"Apache License, Version 2.0",
		"THIRD-PARTY-NOTICES.md",
		"Mozilla Public License 2.0",
		"go mod download",
	} {
		if !strings.Contains(flatNotice, want) {
			t.Errorf("NOTICE is missing %q", want)
		}
	}
}

// TestNoticesCoverGoMod is the drift gate: adding, removing or upgrading a
// module in go.mod without updating the shipped inventory fails here, offline.
func TestNoticesCoverGoMod(t *testing.T) {
	mods := goMod(t)
	notice := repoFile(t, noticesFile)
	linked, excludedMods, total := parseNotices(t, notice)

	if total != len(linked) {
		t.Errorf("notices summary says %d modules, the linked table has %d", total, len(linked))
	}
	for mod, version := range linked {
		want, ok := mods[mod]
		if !ok {
			t.Errorf("notices list %s %s, which is not a go.mod requirement", mod, version)
			continue
		}
		if want != version {
			t.Errorf("%s: notices say %s, go.mod says %s", mod, version, want)
		}
	}
	for mod, version := range excludedMods {
		if _, ok := linked[mod]; ok {
			t.Errorf("%s is listed as both linked and excluded", mod)
			continue
		}
		want, ok := mods[mod]
		if !ok {
			t.Errorf("excluded table lists %s, which is not a go.mod requirement", mod)
			continue
		}
		if want != version {
			t.Errorf("excluded %s: notices say %s, go.mod says %s", mod, version, want)
		}
	}
	var missing []string
	for mod := range mods {
		if _, ok := linked[mod]; ok {
			continue
		}
		if _, ok := excludedMods[mod]; ok {
			continue
		}
		missing = append(missing, mod)
	}
	sort.Strings(missing)
	for _, mod := range missing {
		t.Errorf("%s is required by go.mod but missing from %s (add it with its license, or to the not-linked table)", mod, noticesFile)
	}
}

// TestLicenseTextsPresent checks that every license family named in the table
// has a section with its text, and that the MPL/LGPL obligations stay visible.
func TestLicenseTextsPresent(t *testing.T) {
	notice := repoFile(t, noticesFile)
	linked, _, _ := parseNotices(t, notice)
	wantSections := map[string]bool{}
	for _, expr := range linked {
		for _, part := range regexp.MustCompile(`\s+(AND|OR|WITH)\s+`).Split(expr, -1) {
			part = strings.TrimSpace(part)
			switch part {
			case "MIT", "BSD-2-Clause", "BSD-3-Clause", "Apache-2.0", "MPL-2.0":
				wantSections["### "+part] = true
			case "LGPL-3.0-only":
				wantSections["### LGPL-3.0"] = true
			}
		}
	}
	for heading := range wantSections {
		if !strings.Contains(notice, heading) {
			t.Errorf("%s names license %q but has no %q section", noticesFile, strings.TrimPrefix(heading, "### "), heading)
		}
	}
	if !strings.Contains(notice, "Section 3.2") {
		t.Errorf("%s does not state the MPL-2.0 source-availability obligation", noticesFile)
	}
}
