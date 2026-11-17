package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Residue is the result of the legacy proposal-history scrub that Open runs.
// It carries a count for the operational log and fixed, destination-free
// warnings for doctor. A file the scrub cannot parse is reported and left
// untouched (D21).
type Residue struct {
	Rewritten int
	Warnings  []string
}

// Residue returns what the most recent Open scrubbed. The value is fixed before
// Open returns, so it is safe to read without a lock.
func (s *Store) Residue() Residue { return s.residue }

// Note returns the destination-free warnings joined for an error or doctor
// detail. An empty string means there is nothing to report.
func (r Residue) Note() string {
	return strings.Join(r.Warnings, "; ")
}

func (s *Store) addResidueWarning(format string, args ...any) {
	s.residue.Warnings = append(s.residue.Warnings, fmt.Sprintf(format, args...))
}

// scrubLegacyProposals removes destination-bearing proposal history that an
// earlier build wrote into state.json, state.json.bak, crashed-writer temp
// files and RouteLatch's own backup files. State documents keep every other
// byte; a backup state is re-encoded canonically so the existing checksum
// scheme still verifies, and every field known to contract.State survives. It
// is idempotent: a file without the key is not rewritten. A file it cannot
// parse or verify is left untouched and reported.
//
// The single-writer lock is taken for the pass. When another instance owns the
// data directory the pass is skipped: that instance still owns the files, and
// the next start after it exits will scrub them.
func (s *Store) scrubLegacyProposals() {
	unlock, err := s.lockForWrite()
	if err != nil {
		s.addResidueWarning("another RouteLatch instance owns the data directory; legacy proposal history was not inspected")
		return
	}
	defer unlock()

	s.scrubStateFile(s.paths.State, "state.json")
	s.scrubStateFile(s.paths.State+".bak", "state.json.bak")
	s.scrubStateTemps()
	s.scrubBackupDir()
}

// scrubStateTemps covers a crashed writer's leftover state.json.tmp-<pid> /
// state.json.bak.tmp-<pid>. writeFileAtomic treats these as garbage on the next
// write to the same target; until then they are still an app-owned file that
// may carry proposal history.
func (s *Store) scrubStateTemps() {
	entries, err := os.ReadDir(s.paths.StateDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasPrefix(name, "state.json") || !strings.Contains(name, ".tmp-") {
			continue
		}
		s.scrubStateFile(filepath.Join(s.paths.StateDir, name), "temp "+name)
	}
}

func (s *Store) scrubStateFile(path, label string) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		s.addResidueWarning("%s could not be read; left untouched", label)
		return
	}
	if !json.Valid(raw) {
		s.addResidueWarning("%s is not readable JSON; left untouched", label)
		return
	}
	out, found, err := removeAllTopLevelMembers(raw, "proposals")
	if err != nil {
		s.addResidueWarning("%s could not be inspected; left untouched", label)
		return
	}
	if !found {
		return
	}
	if err := writeFileAtomic(path, out); err != nil {
		s.addResidueWarning("%s could not be rewritten; left untouched", label)
		return
	}
	s.residue.Rewritten++
}

// scrubBackupDir rewrites only files this application names: state-*.json.
// Other files in the directory belong to the user and are not inspected.
func (s *Store) scrubBackupDir() {
	if s.paths.Backup == "" {
		return
	}
	entries, err := os.ReadDir(s.paths.Backup)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		s.addResidueWarning("the backup directory could not be read; legacy proposal history was not inspected")
		return
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		if !isBackupFileName(entry.Name()) && !isBackupTempName(entry.Name()) {
			continue
		}
		s.scrubBackupFile(filepath.Join(s.paths.Backup, entry.Name()))
	}
}

func isBackupFileName(name string) bool {
	return strings.HasPrefix(name, "state-") && strings.HasSuffix(name, ".json")
}

func isBackupTempName(name string) bool {
	return strings.HasPrefix(name, "state-") && strings.Contains(name, ".json.tmp-")
}

// scrubBackupFile rewrites a verified backup without proposal history. The
// state payload is re-encoded canonically, which is what the existing checksum
// scheme hashes; a backup this application wrote is already canonical, so only
// the removed key differs.
func (s *Store) scrubBackupFile(path string) {
	label := "backup " + filepath.Base(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		s.addResidueWarning("%s could not be read; left untouched", label)
		return
	}
	if !json.Valid(raw) {
		s.addResidueWarning("%s is not readable JSON; left untouched", label)
		return
	}
	var env backupEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || len(env.State) == 0 {
		// Not one of our envelopes. If it cannot be understood, do not guess at
		// its contents or rewrite it.
		s.addResidueWarning("%s is not a readable RouteLatch backup; left untouched", label)
		return
	}
	if _, _, _, found, err := findTopLevelMember(env.State, "proposals"); err != nil {
		s.addResidueWarning("%s could not be inspected; left untouched", label)
		return
	} else if !found {
		return
	}

	// Verify the file before touching it. A backup that does not verify is
	// residue we cannot safely rewrite.
	st, _, err := readBackup(path)
	if err != nil {
		s.addResidueWarning("%s does not verify; left untouched", label)
		return
	}
	canonical, err := canonicalState(st)
	if err != nil {
		s.addResidueWarning("%s could not be rewritten; left untouched", label)
		return
	}
	sum := sha256.Sum256(canonical)
	checksum := []byte(`"sha256:` + hex.EncodeToString(sum[:]) + `"`)
	out, found, err := replaceTopLevelMember(raw, "state", canonical)
	if err != nil || !found {
		s.addResidueWarning("%s could not be rewritten; left untouched", label)
		return
	}
	out, found, err = replaceTopLevelMember(out, "checksum", checksum)
	if err != nil || !found {
		s.addResidueWarning("%s could not be rewritten; left untouched", label)
		return
	}
	if err := writeFileAtomic(path, out); err != nil {
		s.addResidueWarning("%s could not be rewritten; left untouched", label)
		return
	}
	s.residue.Rewritten++
}

// findTopLevelMember locates one member of a top-level JSON object and returns
// the byte offsets of its key and value. It works on the original bytes so a
// rewrite can preserve every other byte exactly.
func findTopLevelMember(raw []byte, want string) (keyStart, valueStart, valueEnd int, found bool, err error) {
	i := skipJSONSpace(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return 0, 0, 0, false, fmt.Errorf("not a JSON object")
	}
	i++
	for {
		i = skipJSONSpace(raw, i)
		if i >= len(raw) {
			return 0, 0, 0, false, fmt.Errorf("unterminated object")
		}
		if raw[i] == '}' {
			return 0, 0, 0, false, nil
		}
		ks := i
		ke, ok := scanJSONString(raw, i)
		if !ok {
			return 0, 0, 0, false, fmt.Errorf("bad key")
		}
		var key string
		if err := json.Unmarshal(raw[ks:ke], &key); err != nil {
			return 0, 0, 0, false, err
		}
		i = skipJSONSpace(raw, ke)
		if i >= len(raw) || raw[i] != ':' {
			return 0, 0, 0, false, fmt.Errorf("missing colon")
		}
		i = skipJSONSpace(raw, i+1)
		vs := i
		ve, ok := scanJSONValue(raw, i)
		if !ok {
			return 0, 0, 0, false, fmt.Errorf("bad value")
		}
		if key == want {
			return ks, vs, ve, true, nil
		}
		i = skipJSONSpace(raw, ve)
		if i < len(raw) && raw[i] == ',' {
			i++
			continue
		}
		return 0, 0, 0, false, nil
	}
}

// removeTopLevelMember deletes one member from the original object bytes and
// keeps every other byte in place. It handles the separating comma on either
// side so the result stays valid JSON.
func removeTopLevelMember(raw []byte, key string) ([]byte, bool, error) {
	ks, _, ve, found, err := findTopLevelMember(raw, key)
	if err != nil || !found {
		return raw, found, err
	}
	if j := skipJSONSpace(raw, ve); j < len(raw) && raw[j] == ',' {
		// A following member exists: drop the key through its trailing comma.
		// Trim the indentation that only belonged to the removed key.
		start := ks
		for start > 0 && (raw[start-1] == ' ' || raw[start-1] == '\t') {
			start--
		}
		return joinJSON(raw[:start], raw[j+1:]), true, nil
	}
	// Last member: drop the comma before it when there is one.
	k := ks
	for k > 0 && isJSONSpace(raw[k-1]) {
		k--
	}
	if k > 0 && raw[k-1] == ',' {
		return joinJSON(raw[:k-1], raw[ve:]), true, nil
	}
	start := ks
	for start > 0 && (raw[start-1] == ' ' || raw[start-1] == '\t') {
		start--
	}
	return joinJSON(raw[:start], raw[ve:]), true, nil
}

// removeAllTopLevelMembers removes every occurrence of key. A hand-crafted
// document can repeat a key; one pass must not leave a destination behind.
func removeAllTopLevelMembers(raw []byte, key string) ([]byte, bool, error) {
	removed := false
	for {
		out, found, err := removeTopLevelMember(raw, key)
		if err != nil {
			return raw, removed, err
		}
		if !found {
			return raw, removed, nil
		}
		raw = out
		removed = true
	}
}

// replaceTopLevelMember replaces one member's value with value, preserving the
// key, the colon, the surrounding whitespace and every other byte.
func replaceTopLevelMember(raw []byte, key string, value []byte) ([]byte, bool, error) {
	_, vs, ve, found, err := findTopLevelMember(raw, key)
	if err != nil || !found {
		return raw, found, err
	}
	return joinJSON(raw[:vs], value, raw[ve:]), true, nil
}

func joinJSON(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func skipJSONSpace(raw []byte, i int) int {
	for i < len(raw) && isJSONSpace(raw[i]) {
		i++
	}
	return i
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// scanJSONString returns the offset just after the closing quote. i must point
// at the opening quote.
func scanJSONString(raw []byte, i int) (int, bool) {
	if i >= len(raw) || raw[i] != '"' {
		return i, false
	}
	i++
	for i < len(raw) {
		switch raw[i] {
		case '\\':
			i += 2
		case '"':
			return i + 1, true
		default:
			i++
		}
	}
	return i, false
}

// scanJSONValue returns the offset just after a complete JSON value.
func scanJSONValue(raw []byte, i int) (int, bool) {
	if i >= len(raw) {
		return i, false
	}
	switch raw[i] {
	case '"':
		return scanJSONString(raw, i)
	case '{', '[':
		depth := 0
		for i < len(raw) {
			switch raw[i] {
			case '"':
				next, ok := scanJSONString(raw, i)
				if !ok {
					return i, false
				}
				i = next
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
			i++
		}
		return i, false
	default:
		// number, true, false or null: ends at a structural byte or space.
		start := i
		for i < len(raw) && !isJSONSpace(raw[i]) && raw[i] != ',' && raw[i] != '}' && raw[i] != ']' {
			i++
		}
		return i, i > start
	}
}
