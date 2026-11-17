package imports

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"routelatch/internal/contract"
)

var _ contract.Importer = (*Importer)(nil)

// Importer implements contract.Importer. It is pure with respect to the engine:
// it validates and projects; it never builds or starts a core.
type Importer struct {
	now func() time.Time
}

// NewImporter returns an importer using the wall clock.
func NewImporter() *Importer { return &Importer{now: time.Now} }

// NewImporterWithClock injects a clock for deterministic tests.
func NewImporterWithClock(now func() time.Time) *Importer {
	if now == nil {
		now = time.Now
	}
	return &Importer{now: now}
}

// Audit counts projections that are not visible in the record shape. It never
// contains payload values.
type Audit struct {
	DroppedKeys int      `json:"dropped_keys"`
	Notices     []string `json:"notices,omitempty"`
}

// Import implements contract.Importer.
func (im *Importer) Import(ctx context.Context, src contract.Source, payload []byte) ([]contract.Record, error) {
	records, _, err := im.ImportWithAudit(ctx, src, payload)
	return records, err
}

// ImportWithAudit returns per-batch projection notes (mux removal, ignored
// allowInsecure) so the caller can report them without printing raw values.
func (im *Importer) ImportWithAudit(ctx context.Context, src contract.Source, payload []byte) ([]contract.Record, Audit, error) {
	var audit Audit
	if err := ctx.Err(); err != nil {
		return nil, audit, canceledFailure("imports.import")
	}
	if len(payload) > contract.MaxSourceBytes {
		return nil, audit, sourceFailure("source exceeds the 4 MiB limit", nil)
	}
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		if src.AllowEmpty {
			return nil, audit, nil
		}
		return nil, audit, sourceFailure("source is empty", nil)
	}

	var records []contract.Record
	now := im.now().UTC()

	isJSON := strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
	if src.Kind == contract.SourceV2rayN && isJSON {
		var err error
		records, err = parseV2rayNJSON(payload, src.ID, now)
		if err != nil {
			return nil, audit, sourceFailure("v2rayN payload is not a supported node export", err)
		}
		if len(records) > contract.MaxRecordsPerSource {
			return nil, audit, sourceFailure("source exceeds the record limit", nil)
		}
		audit.DroppedKeys += countJSONKeyFromRaw(payload, "mux")
		addRecordAudit(&audit, records)
		return records, audit, nil
	}
	if isJSON {
		// A file or subscription carrying node JSON is accepted only through
		// the same read-only projection; application configuration is not.
		var err error
		records, err = parseV2rayNJSON(payload, src.ID, now)
		if err != nil {
			return nil, audit, sourceFailure("payload is JSON but not a supported node export", err)
		}
		audit.DroppedKeys += countJSONKeyFromRaw(payload, "mux")
		addRecordAudit(&audit, records)
		return records, audit, nil
	}
	if strings.HasPrefix(trimmed, "<") {
		return nil, audit, sourceFailure("source returned HTML, not a subscription", nil)
	}

	text := string(payload)
	lower := strings.ToLower(text)
	if !strings.Contains(text, "://") {
		decoded, ok := decodeSubscription(text)
		if !ok {
			return nil, audit, sourceFailure("payload is not a share-link list", nil)
		}
		text = decoded
		if strings.HasPrefix(strings.TrimSpace(text), "<") {
			return nil, audit, sourceFailure("source returned HTML, not a subscription", nil)
		}
	} else if strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype html") {
		return nil, audit, sourceFailure("source returned HTML, not a subscription", nil)
	}

	lines := splitLines(text)
	if len(lines) > contract.MaxRecordsPerSource {
		return nil, audit, sourceFailure("source exceeds the record limit", nil)
	}
	records = make([]contract.Record, 0, len(lines))
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > maxRecordBytes {
			records = append(records, rawRecord(line, contract.RecordInvalid, ReasonRecordOversized, "", src.ID, i, now))
			continue
		}
		records = append(records, parseLink(line, src.ID, i, now))
		audit.DroppedKeys += countDroppedQueryKeys(line)
	}
	addRecordAudit(&audit, records)
	if len(records) == 0 {
		if src.AllowEmpty {
			return nil, audit, nil
		}
		return nil, audit, sourceFailure("source is empty", nil)
	}
	return records, audit, nil
}

func countJSONKeyFromRaw(payload []byte, want string) int {
	dec := json.NewDecoder(strings.NewReader(string(payload)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0
	}
	return countJSONKey(v, want)
}

func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimPrefix(text, "\ufeff")
	return strings.Split(text, "\n")
}

// decodeSubscription accepts standard and URL-safe base64, padded or not.
func decodeSubscription(text string) (string, bool) {
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, text)
	if compact == "" {
		return "", false
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		decoded, err := enc.DecodeString(compact)
		if err != nil || len(decoded) == 0 {
			continue
		}
		if !utf8.Valid(decoded) {
			continue
		}
		s := string(decoded)
		if strings.Contains(s, "://") {
			return s, true
		}
	}
	return "", false
}

// ReadFileSource reads one user-selected file with the subscription size
// bound. It is the only local read in the import path and it never writes,
// locks or changes anything.
func ReadFileSource(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, sourceFailure("cannot read the selected file", err)
	}
	if !info.Mode().IsRegular() {
		return nil, sourceFailure("the selected path is not a regular file", nil)
	}
	if info.Size() > contract.MaxSourceBytes {
		return nil, sourceFailure("source exceeds the 4 MiB limit", nil)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, sourceFailure("cannot read the selected file", err)
	}
	defer f.Close()
	buf := make([]byte, 0, info.Size())
	limited := make([]byte, 32<<10)
	for {
		n, err := f.Read(limited)
		if n > 0 {
			if len(buf)+n > contract.MaxSourceBytes {
				return nil, sourceFailure("source exceeds the 4 MiB limit", nil)
			}
			buf = append(buf, limited[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, sourceFailure("cannot read the selected file", err)
		}
	}
	return buf, nil
}

func sourceFailure(message string, cause error) *contract.Failure {
	return &contract.Failure{
		Kind:    contract.SourceFetch,
		Path:    contract.PathLocal,
		Op:      "imports.import",
		Message: message,
		Err:     cause,
	}
}

func canceledFailure(op string) *contract.Failure {
	return &contract.Failure{Kind: contract.Canceled, Path: contract.PathLocal, Op: op, Message: "operation canceled"}
}

// DescribeCounts is a small convenience for import/refresh output.
func DescribeCounts(records []contract.Record) (valid, invalid, unsupported int) {
	for _, rec := range records {
		switch rec.Status {
		case contract.RecordInvalid:
			invalid++
		case contract.RecordUnsupported:
			unsupported++
		default:
			valid++
		}
	}
	return valid, invalid, unsupported
}

var linkQueryKeys = map[string]bool{
	"type": true, "network": true, "security": true, "sni": true, "servername": true,
	"fp": true, "fingerprint": true, "flow": true, "path": true, "host": true,
	"servicename": true, "alpn": true, "pbk": true, "publickey": true, "sid": true,
	"shortid": true, "spx": true, "spiderx": true, "pinsha256": true,
	"pinnedpeercertsha256": true, "verifypeercertbyname": true, "allowinsecure": true,
	"insecure": true, "skipcertverify": true, "mode": true, "extra": true,
	"headertype": true, "encryption": true,
}

// countDroppedQueryKeys counts link parameters outside the allow-list. They are
// data, not instructions, and must not be silently ignored.
func countDroppedQueryKeys(line string) int {
	i := strings.IndexByte(line, '?')
	if i < 0 {
		return 0
	}
	query := line[i+1:]
	if j := strings.IndexByte(query, '#'); j >= 0 {
		query = query[:j]
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return 0
	}
	n := 0
	for key := range values {
		if !linkQueryKeys[strings.ToLower(key)] {
			n++
		}
	}
	return n
}

// countJSONKey counts a benign projection field that the record shape cannot
// represent (mux today). The value is dropped; the count makes it visible.
func countJSONKey(v any, want string) int {
	switch t := v.(type) {
	case map[string]any:
		n := 0
		for k, child := range t {
			if strings.EqualFold(k, want) {
				n++
				continue
			}
			n += countJSONKey(child, want)
		}
		return n
	case []any:
		n := 0
		for _, child := range t {
			n += countJSONKey(child, want)
		}
		return n
	default:
		return 0
	}
}

func addRecordAudit(audit *Audit, records []contract.Record) {
	for _, rec := range records {
		if rec.InsecureTLS && rec.PinnedPeerCertSHA256 != "" {
			audit.Notices = append(audit.Notices, "allow_insecure_ignored: verification stays on and the certificate pin is applied")
		}
	}
}
