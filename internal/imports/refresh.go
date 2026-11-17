package imports

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"routelatch/internal/contract"
)

// ErrSourceNotFound remains as a wrapped cause so existing callers can use
// errors.Is; Refresh returns a typed *contract.Failure with Kind NotFound for
// an ID the user named that does not exist (exit code 5).
var ErrSourceNotFound = errors.New("source not found")

func sourceNotFoundFailure() *contract.Failure {
	return &contract.Failure{
		Kind: contract.NotFound, Path: contract.PathLocal, Op: "imports.refresh",
		Message: ErrSourceNotFound.Error(), Err: ErrSourceNotFound,
	}
}

// SourceResult is the redacted outcome of refreshing one source.
type SourceResult struct {
	ID          contract.SourceID `json:"id"`
	OK          bool              `json:"ok"`
	Records     int               `json:"records"`
	Error       string            `json:"error,omitempty"`
	DroppedKeys int               `json:"dropped_keys,omitempty"`
	Notices     []string          `json:"notices,omitempty"`
}

// RefreshResult mirrors the `pool refresh` payload: one row per source plus
// whether any previous good record set had to be kept.
type RefreshResult struct {
	Sources      []SourceResult `json:"sources"`
	KeptLastGood bool           `json:"kept_last_good"`
}

// AddResult is the result of an explicit import command.
type AddResult struct {
	SourceID    contract.SourceID `json:"source_id"`
	Valid       int               `json:"valid"`
	Invalid     int               `json:"invalid"`
	Unsupported int               `json:"unsupported"`
	DroppedKeys int               `json:"dropped_keys,omitempty"`
	Notices     []string          `json:"notices,omitempty"`
}

// Refresher owns snapshot → fetch/read → import → atomic merge. A failed source
// never changes that source's records; the mutation either commits the new set
// or returns an error from Store with the old file intact.
type Refresher struct {
	Store    contract.Store
	Fetcher  contract.Fetcher
	Importer *Importer
	Now      func() time.Time
	// BuildCheck, when set, runs once per new record before the single commit.
	// A non-empty reason means the record cannot become a dialable exit; it is
	// stored as unsupported with that reason instead of failing the whole
	// source. A non-nil error aborts the import before anything is written.
	BuildCheck func(contract.Record) (reason string, err error)
}

// NewRefresher wires the production fetcher and importer.
func NewRefresher(store contract.Store, fetcher contract.Fetcher) *Refresher {
	if fetcher == nil {
		fetcher = NewFetcher()
	}
	return &Refresher{Store: store, Fetcher: fetcher, Importer: NewImporter(), Now: time.Now}
}

type preparedSource struct {
	source  contract.Source
	records []contract.Record
	audit   Audit
	err     string
}

// Refresh reloads the selected sources (all when ids is empty). Network and
// file I/O happen before the single Store.Mutate, so a slow fetch does not hold
// the state lock.
func (r *Refresher) Refresh(ctx context.Context, ids []contract.SourceID, allowEmpty bool) (RefreshResult, error) {
	snap, err := r.Store.Load(ctx)
	if err != nil {
		return RefreshResult{}, err
	}
	selected, missing := selectSources(snap.State.Sources, ids)
	result := RefreshResult{}
	if len(missing) > 0 {
		for _, id := range missing {
			result.Sources = append(result.Sources, SourceResult{ID: id, Error: ErrSourceNotFound.Error()})
			result.KeptLastGood = true
		}
	}
	if len(selected) == 0 {
		// Every requested source is missing (or the pool has no sources at
		// all). This is a lookup failure, not "every source failed".
		return result, sourceNotFoundFailure()
	}

	prepared := make([]preparedSource, 0, len(selected))
	for _, src := range selected {
		prepared = append(prepared, r.prepare(ctx, src, allowEmpty || src.AllowEmpty))
	}

	now := r.Now().UTC()
	_, mutateErr := r.Store.Mutate(ctx, func(st *contract.State) error {
		selectedBefore := st.SelectedExit
		for _, p := range prepared {
			idx := sourceIndex(st.Sources, p.source.ID)
			if idx < 0 {
				continue
			}
			if p.err != "" {
				st.Sources[idx].LastError = p.err
				st.Sources[idx].LastErrorAt = now
				continue
			}
			replaceSourceRecords(st, p.source.ID, p.records, now)
			st.Sources[idx].RefreshedAt = now
			st.Sources[idx].LastError = ""
			st.Sources[idx].LastErrorAt = time.Time{}
			st.Sources[idx].RecordCount = countSourceRecords(st.Records, p.source.ID)
			if p.source.AllowEmpty {
				st.Sources[idx].AllowEmpty = true
			}
		}
		if selectedBefore != "" && st.SelectedExit == "" {
			// Clearing a manual pin is a policy change; Store.Mutate requires a
			// strictly greater version.
			st.PolicyVersion++
		}
		return nil
	})
	if mutateErr != nil {
		return result, mutateErr
	}

	byID := make(map[contract.SourceID]int, len(result.Sources))
	for i, row := range result.Sources {
		byID[row.ID] = i
	}
	for _, p := range prepared {
		row := SourceResult{ID: p.source.ID, OK: p.err == "", Error: p.err, DroppedKeys: p.audit.DroppedKeys, Notices: p.audit.Notices}
		for _, rec := range p.records {
			if rec.Status != contract.RecordInvalid && rec.Status != contract.RecordUnsupported {
				row.Records++
			}
		}
		if i, ok := byID[p.source.ID]; ok {
			result.Sources[i] = row
		} else {
			result.Sources = append(result.Sources, row)
		}
		if !row.OK {
			result.KeptLastGood = true
		}
	}
	sort.Slice(result.Sources, func(i, j int) bool { return result.Sources[i].ID < result.Sources[j].ID })
	return result, nil
}

func (r *Refresher) prepare(ctx context.Context, src contract.Source, allowEmpty bool) preparedSource {
	p := preparedSource{source: src}
	var payload []byte
	var err error
	switch src.Kind {
	case contract.SourceSubscription:
		payload, err = r.Fetcher.FetchSource(ctx, src)
	case contract.SourceFile, contract.SourceV2rayN:
		payload, err = ReadFileSource(src.Origin)
	default:
		// A manual source stores its link as the origin; re-importing it is
		// local and never touches the network.
		payload = []byte(src.Origin)
	}
	if err != nil {
		p.err = redactError(err)
		return p
	}
	importSrc := src
	importSrc.AllowEmpty = allowEmpty
	records, audit, importErr := r.Importer.ImportWithAudit(ctx, importSrc, payload)
	if importErr != nil {
		p.err = redactError(importErr)
		return p
	}
	if err := r.applyBuildCheck(records); err != nil {
		p.err = redactError(err)
		return p
	}
	p.audit = audit
	valid, _, _ := DescribeCounts(records)
	if valid == 0 && !allowEmpty {
		p.err = "source has no valid records; previous pool preserved"
		return p
	}
	p.records = records
	return p
}

// applyBuildCheck downgrades records the engine cannot build to unsupported
// before they are committed. A check failure that is not record-specific
// aborts the caller; it is never reported as a stored success.
func (r *Refresher) applyBuildCheck(records []contract.Record) error {
	if r.BuildCheck == nil {
		return nil
	}
	for i := range records {
		switch records[i].Status {
		case contract.RecordInvalid, contract.RecordUnsupported:
			continue
		}
		reason, err := r.BuildCheck(records[i])
		if err != nil {
			return err
		}
		if reason != "" {
			records[i].Status = contract.RecordUnsupported
			records[i].StatusReason = reason
		}
	}
	return nil
}

func redactError(err error) string {
	if err == nil {
		return ""
	}
	var f *contract.Failure
	if errors.As(err, &f) {
		return f.Error()
	}
	// A plain error may contain a path or URL; keep only a stable phrase.
	return "source could not be read or parsed"
}

func selectSources(all []contract.Source, ids []contract.SourceID) ([]contract.Source, []contract.SourceID) {
	if len(ids) == 0 {
		return append([]contract.Source(nil), all...), nil
	}
	want := make(map[contract.SourceID]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var selected []contract.Source
	for _, src := range all {
		if want[src.ID] {
			selected = append(selected, src)
			delete(want, src.ID)
		}
	}
	var missing []contract.SourceID
	for _, id := range ids {
		if want[id] {
			missing = append(missing, id)
		}
	}
	return selected, missing
}

func sourceIndex(sources []contract.Source, id contract.SourceID) int {
	for i := range sources {
		if sources[i].ID == id {
			return i
		}
	}
	return -1
}

func countSourceRecords(records []contract.Record, id contract.SourceID) int {
	n := 0
	for _, rec := range records {
		for _, src := range rec.SourceIDs {
			if src == id {
				n++
				break
			}
		}
	}
	return n
}

// replaceSourceRecords removes exactly one source's references, merges canonical
// records by dedupe key with other sources, preserves user order and prunes
// health for exits that no longer exist. It is pure state manipulation.
func replaceSourceRecords(st *contract.State, srcID contract.SourceID, incoming []contract.Record, now time.Time) {
	kept := make([]contract.Record, 0, len(st.Records))
	for _, rec := range st.Records {
		refs := rec.SourceIDs[:0]
		for _, id := range rec.SourceIDs {
			if id != srcID {
				refs = append(refs, id)
			}
		}
		rec.SourceIDs = refs
		if len(rec.SourceIDs) > 0 {
			kept = append(kept, rec)
		}
	}
	st.Records = kept

	nextOrder := 0
	for _, rec := range st.Records {
		if rec.Order >= nextOrder {
			nextOrder = rec.Order + 1
		}
	}
	index := make(map[string]int, len(st.Records))
	for i, rec := range st.Records {
		index[rec.DedupeKey] = i
	}
	for _, rec := range incoming {
		rec.SourceIDs = []contract.SourceID{srcID}
		rec.AddedAt = now
		if i, ok := index[rec.DedupeKey]; ok {
			existing := st.Records[i]
			if !hasSource(existing.SourceIDs, srcID) {
				existing.SourceIDs = append(existing.SourceIDs, srcID)
			}
			sortSourceIDs(existing.SourceIDs)
			// An invalid/unsupported classification can be repaired by a new
			// valid record with identical settings; health and order survive.
			if (existing.Status == contract.RecordInvalid || existing.Status == contract.RecordUnsupported) &&
				rec.Status == contract.RecordUnknown {
				existing.Status = rec.Status
				existing.StatusReason = rec.StatusReason
				existing.LastError = ""
				existing.ValidatedAt = now
			}
			st.Records[i] = existing
			continue
		}
		rec.Order = nextOrder
		nextOrder++
		st.Records = append(st.Records, rec)
		index[rec.DedupeKey] = len(st.Records) - 1
	}
	sort.SliceStable(st.Records, func(i, j int) bool {
		if st.Records[i].Order != st.Records[j].Order {
			return st.Records[i].Order < st.Records[j].Order
		}
		return st.Records[i].ID < st.Records[j].ID
	})
	pruneHealth(st)
}

func pruneHealth(st *contract.State) {
	live := make(map[contract.ExitID]bool, len(st.Records))
	for _, rec := range st.Records {
		live[contract.ExitID(rec.ID)] = true
	}
	health := st.Health[:0]
	for _, h := range st.Health {
		if live[h.Exit] {
			health = append(health, h)
		}
	}
	st.Health = health
	if st.SelectedExit != "" && !live[st.SelectedExit] {
		st.SelectedExit = ""
	}
}

func hasSource(ids []contract.SourceID, want contract.SourceID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func sortSourceIDs(ids []contract.SourceID) {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
}

// AddSource imports one explicitly supplied source. The network/file read
// happens before the mutation, and the source and its records commit together.
func (r *Refresher) AddSource(ctx context.Context, kind contract.SourceKind, label, origin string, payload []byte) (AddResult, error) {
	snap, err := r.Store.Load(ctx)
	if err != nil {
		return AddResult{}, err
	}
	now := r.Now().UTC()
	source := contract.Source{
		ID:      SourceIDFor(kind, origin),
		Kind:    kind,
		Label:   SanitizeDisplay(label),
		Origin:  origin,
		Order:   nextSourceOrder(snap.State.Sources),
		AddedAt: now,
	}
	if existing := sourceIndex(snap.State.Sources, source.ID); existing >= 0 {
		source.Order = snap.State.Sources[existing].Order
		source.AddedAt = snap.State.Sources[existing].AddedAt
	}
	records, audit, importErr := r.Importer.ImportWithAudit(ctx, source, payload)
	if importErr != nil {
		return AddResult{}, importErr
	}
	if err := r.applyBuildCheck(records); err != nil {
		return AddResult{}, err
	}
	valid, invalid, unsupported := DescribeCounts(records)
	if valid == 0 {
		return AddResult{SourceID: source.ID, Valid: valid, Invalid: invalid, Unsupported: unsupported,
				DroppedKeys: audit.DroppedKeys, Notices: audit.Notices},
			sourceFailure("import contains no usable records", nil)
	}

	_, err = r.Store.Mutate(ctx, func(st *contract.State) error {
		if sourceIndex(st.Sources, source.ID) < 0 {
			st.Sources = append(st.Sources, source)
		}
		replaceSourceRecords(st, source.ID, records, now)
		idx := sourceIndex(st.Sources, source.ID)
		st.Sources[idx].RefreshedAt = now
		st.Sources[idx].LastError = ""
		st.Sources[idx].RecordCount = countSourceRecords(st.Records, source.ID)
		return nil
	})
	if err != nil {
		return AddResult{}, err
	}
	return AddResult{SourceID: source.ID, Valid: valid, Invalid: invalid, Unsupported: unsupported,
		DroppedKeys: audit.DroppedKeys, Notices: audit.Notices}, nil
}

// AddFile imports a user-selected local file (plain, base64 or node JSON).
func (r *Refresher) AddFile(ctx context.Context, kind contract.SourceKind, label, path string) (AddResult, error) {
	payload, err := ReadFileSource(path)
	if err != nil {
		return AddResult{}, err
	}
	return r.AddSource(ctx, kind, label, path, payload)
}

// AddSubscription fetches one user-supplied URL and imports it. This is the
// only automatic-looking path, and it runs only because the user asked.
func (r *Refresher) AddSubscription(ctx context.Context, label, rawURL string) (AddResult, error) {
	src := contract.Source{Kind: contract.SourceSubscription, Origin: rawURL}
	payload, err := r.Fetcher.FetchSource(ctx, src)
	if err != nil {
		return AddResult{}, err
	}
	return r.AddSource(ctx, contract.SourceSubscription, label, rawURL, payload)
}

// AddLink imports one pasted share link as a manual source.
func (r *Refresher) AddLink(ctx context.Context, rawLink string) (AddResult, error) {
	return r.AddSource(ctx, contract.SourceManual, "manual link", rawLink, []byte(rawLink))
}

func nextSourceOrder(sources []contract.Source) int {
	next := 0
	for _, src := range sources {
		if src.Order >= next {
			next = src.Order + 1
		}
	}
	return next
}

func describeSource(id contract.SourceID, ok bool, n int, err string) string {
	return fmt.Sprintf("%s ok=%t records=%d %s", id, ok, n, err)
}
