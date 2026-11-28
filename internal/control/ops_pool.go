package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/engine"
	"routelatch/internal/imports"
	"routelatch/internal/pool"
	"routelatch/internal/probes"
)

// PoolListData is the `pool list` payload. Rows never carry credentials, a
// share link or a source origin.
type PoolListData struct {
	Records   []pool.Row  `json:"records"`
	Counts    pool.Counts `json:"counts"`
	Truncated bool        `json:"truncated"`
}

func (s *Service) listPool(ctx context.Context, limit, offset int) (PoolListData, error) {
	if err := ctx.Err(); err != nil {
		return PoolListData{}, err
	}
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	snap := s.Snapshot()
	p := pool.Build(snap.State, s.now().UTC())
	rows := p.List()
	total := len(rows)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := make([]pool.Row, 0, end-offset)
	for _, row := range rows[offset:end] {
		row.DisplayName = SanitizeTerminal(row.DisplayName)
		row.Reason = SanitizeTerminal(row.Reason)
		page = append(page, row)
	}
	return PoolListData{Records: page, Counts: p.Counts(), Truncated: end < total}, nil
}

// AddData is the shared shape of `pool add` and `import`.
type AddData struct {
	SourceID contract.SourceID `json:"source_id"`
	Records  RecordCounts      `json:"records"`
	Notices  []string          `json:"notices,omitempty"`
}

// RecordCounts is the documented valid/invalid/unsupported triple.
type RecordCounts struct {
	Valid       int `json:"valid"`
	Invalid     int `json:"invalid"`
	Unsupported int `json:"unsupported"`
}

func addData(result imports.AddResult) AddData {
	notices := make([]string, len(result.Notices))
	for i, n := range result.Notices {
		notices[i] = SanitizeTerminal(n)
	}
	return AddData{
		SourceID: result.SourceID,
		Records:  RecordCounts{Valid: result.Valid, Invalid: result.Invalid, Unsupported: result.Unsupported},
		Notices:  notices,
	}
}

func (s *Service) poolAdd(ctx context.Context, req Request) (AddData, error) {
	if err := s.stoppedFailure("pool.add"); err != nil {
		return AddData{}, err
	}
	input := strings.TrimSpace(req.Input)
	if input == "" {
		return AddData{}, usage("pool add needs a share link or a file path")
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	data, err := s.addAndReload(ctx, func() (imports.AddResult, error) {
		if kind == "file" || kind == "" && isRegularFile(input) {
			return s.refresher.AddFile(ctx, contract.SourceFile, filepath.Base(input), input)
		}
		return s.refresher.AddLink(ctx, input)
	})
	if err != nil {
		return data, err
	}
	s.logger.Logf(levelInfo, "pool.add", "source=%s valid=%d invalid=%d unsupported=%d", data.SourceID, data.Records.Valid, data.Records.Invalid, data.Records.Unsupported)
	return data, nil
}

func (s *Service) importSource(ctx context.Context, req Request) (AddData, error) {
	if err := s.stoppedFailure("import"); err != nil {
		return AddData{}, err
	}
	input := strings.TrimSpace(req.Input)
	if input == "" {
		return AddData{}, usage("import needs a link, file path or subscription URL")
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	data, err := s.addAndReload(ctx, func() (imports.AddResult, error) {
		switch kind {
		case "link":
			return s.refresher.AddLink(ctx, input)
		case "file":
			return s.refresher.AddFile(ctx, contract.SourceFile, filepath.Base(input), input)
		case "v2rayn", "v2rayN":
			return s.refresher.AddFile(ctx, contract.SourceV2rayN, filepath.Base(input), input)
		case "subscription":
			return s.refresher.AddSubscription(ctx, "subscription", input)
		default:
			return imports.AddResult{}, usage("import kind must be link, file, subscription or v2rayn")
		}
	})
	if err != nil {
		return data, err
	}
	s.logger.Logf(levelInfo, "pool.import", "source=%s valid=%d invalid=%d unsupported=%d", data.SourceID, data.Records.Valid, data.Records.Invalid, data.Records.Unsupported)
	return data, nil
}

// buildCheck runs the engine's record validation before the import commit. A
// record-specific rejection is downgraded to unsupported; an unexpected error
// aborts the import before anything is written.
func buildCheck(rec contract.Record) (string, error) {
	err := engine.CheckRecord(rec)
	if err == nil {
		return "", nil
	}
	var failure *contract.Failure
	if errors.As(err, &failure) {
		switch failure.Kind {
		case contract.Unsupported, contract.ImportInvalid:
			return engine.ReasonRecordRejected, nil
		}
	}
	return "", err
}

// markEngineExcludedLocked persists the engine's per-record exclusions so pool
// list/status never claim a record is usable when the engine refused it. The
// caller holds pubMu (startNetwork's exclusion path or restore).
func (s *Service) markEngineExcludedLocked(ctx context.Context, excluded []engine.ExcludedExit) error {
	after, err := s.store.Mutate(ctx, func(st *contract.State) error {
		for _, ex := range excluded {
			for i := range st.Records {
				if contract.ExitID(st.Records[i].ID) != ex.ID {
					continue
				}
				if st.Records[i].Status == contract.RecordInvalid {
					continue
				}
				st.Records[i].Status = contract.RecordUnsupported
				st.Records[i].StatusReason = ex.Reason
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	nextPolicy, err := s.rebuildPolicy(after)
	if err != nil {
		return err
	}
	s.commitSnapshotLocked(after, nextPolicy)
	return nil
}

// rollbackImport restores the pre-import records, sources and selection after a
// later reload step failed. The policy version is never allowed to move
// backwards; a selection restore that changes the policy advances it.
func (s *Service) rollbackImport(ctx context.Context, before contract.Snapshot) error {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	current := s.Snapshot()
	after, err := s.store.Mutate(ctx, func(st *contract.State) error {
		policyChanged := st.Mode != before.State.Mode ||
			st.SelectedExit != before.State.SelectedExit ||
			!reflect.DeepEqual(st.Rules, before.State.Rules)
		currentVersion := st.PolicyVersion
		st.Records = append([]contract.Record(nil), before.State.Records...)
		st.Sources = append([]contract.Source(nil), before.State.Sources...)
		st.Health = append([]contract.ExitHealth(nil), before.State.Health...)
		st.SelectedExit = before.State.SelectedExit
		if policyChanged && st.PolicyVersion <= currentVersion {
			st.PolicyVersion = currentVersion + 1
		}
		return nil
	})
	if err != nil {
		return err
	}
	nextPolicy, err := s.rebuildPolicy(after)
	if err != nil {
		return err
	}
	s.commitSnapshotLocked(after, nextPolicy)
	// Undo exits that were already added to the live engine before the
	// failure, so engine and persisted state agree.
	return s.reconcileExits(ctx, current, after)
}

// addAndReload runs one import with a pre-import snapshot. If the post-commit
// engine/policy reload fails, the imported state is rolled back so the command's
// non-zero exit never leaves the record stored.
func (s *Service) addAndReload(ctx context.Context, do func() (imports.AddResult, error)) (AddData, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	before, err := s.store.Load(ctx)
	if err != nil {
		return AddData{}, mapImportError(err)
	}
	result, err := do()
	if err != nil {
		return AddData{}, mapImportError(err)
	}
	if err := s.reloadAfterImport(ctx); err != nil {
		// The rollback must still run when the caller's request context is
		// already canceled or expired; otherwise the failed import would be
		// reported with a non-zero exit while its record stayed persisted.
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if rbErr := s.rollbackImport(rollbackCtx, before); rbErr != nil {
			s.logger.Logf(levelError, "import.rollback", "the imported state could not be rolled back: %s", rbErr.Error())
			return AddData{}, &contract.Failure{
				Kind: contract.Internal, Path: contract.PathLocal, Op: "control.import",
				Message: "the import failed and the pre-import state could not be restored",
				Err:     errors.Join(err, rbErr),
			}
		}
		return AddData{}, err
	}
	return addData(result), nil
}

// reloadAfterImport loads the committed state, adds new exits to the live
// engine and installs the matching policy. Imports never bump a policy version
// by themselves; additions do not change an existing destination's route.
func (s *Service) reloadAfterImport(ctx context.Context) error {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	before := s.Snapshot()
	after, err := s.store.Load(ctx)
	if err != nil {
		return err
	}
	lost := lostExits(before, after)
	if len(lost) > 0 && after.State.PolicyVersion <= before.State.PolicyVersion {
		after, err = s.store.Mutate(ctx, func(st *contract.State) error {
			st.PolicyVersion = before.State.PolicyVersion + 1
			return nil
		})
		if err != nil {
			return err
		}
	}
	if err := s.reconcileExits(ctx, before, after); err != nil {
		return err
	}
	nextPolicy, err := s.rebuildPolicy(after)
	if err != nil {
		return err
	}
	s.commitSnapshotLocked(after, nextPolicy)
	if len(lost) > 0 {
		keep := func(ci contract.ConnInfo) bool {
			if _, gone := lost[ci.Exit]; gone {
				return false
			}
			return routeKeep(nextPolicy)(ci)
		}
		if _, err := s.sweep(ctx, contract.CloseExitGone, nextPolicy.PolicyVersion(), keep); err != nil {
			return err
		}
	}
	return nil
}

// reconcileExits adds and removes engine exits to match the committed state. A
// removed exit is closed by the caller's sweep; this function never dials.
func (s *Service) reconcileExits(ctx context.Context, before, after contract.Snapshot) error {
	if s.engine == nil {
		return nil
	}
	want := make(map[contract.ExitID]bool)
	for _, rec := range dialableRecords(after.State) {
		want[contract.ExitID(rec.ID)] = true
		if !s.engine.HasExit(contract.ExitID(rec.ID)) {
			if _, err := s.engine.AddExit(rec); err != nil {
				return err
			}
		}
	}
	for _, rec := range dialableRecords(before.State) {
		id := contract.ExitID(rec.ID)
		if !want[id] {
			s.engine.RemoveExit(id)
		}
	}
	return nil
}

// ErrRecordNotFound distinguishes a missing pool record.
var ErrRecordNotFound = errors.New("pool record not found")

// PoolRemoveData is the `pool remove` payload.
type PoolRemoveData struct {
	Removed contract.RecordID `json:"removed"`
	Closed  int               `json:"closed"`
}

func (s *Service) poolRemove(ctx context.Context, rawID string) (PoolRemoveData, error) {
	id := contract.RecordID(strings.TrimSpace(rawID))
	if id == "" {
		return PoolRemoveData{}, usage("pool remove needs a record id")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	before := s.Snapshot()
	found := false
	for _, rec := range before.State.Records {
		if rec.ID == id {
			found = true
			break
		}
	}
	if !found {
		return PoolRemoveData{}, NotFound("record " + string(id))
	}
	_, nextPolicy, err := s.mutateAndCommit(ctx, func(st *contract.State) error {
		_, ok := pool.RemoveRecord(st, id)
		if !ok {
			return ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrRecordNotFound) {
			return PoolRemoveData{}, NotFound("record " + string(id))
		}
		return PoolRemoveData{}, err
	}
	s.engineRemove(contract.ExitID(id))
	keep := func(ci contract.ConnInfo) bool {
		if ci.Exit == contract.ExitID(id) {
			return false
		}
		return routeKeep(nextPolicy)(ci)
	}
	report, err := s.sweep(ctx, contract.CloseExitGone, nextPolicy.PolicyVersion(), keep)
	if err != nil {
		return PoolRemoveData{Removed: id, Closed: report.Closed}, err
	}
	s.logger.Logf(levelInfo, "pool.remove", "record=%s closed=%d", id, report.Closed)
	return PoolRemoveData{Removed: id, Closed: report.Closed}, nil
}

func (s *Service) engineRemove(id contract.ExitID) {
	if s.engine != nil {
		s.engine.RemoveExit(id)
	}
}

func (s *Service) poolRefresh(ctx context.Context, req Request) (imports.RefreshResult, error) {
	if err := s.stoppedFailure("pool.refresh"); err != nil {
		return imports.RefreshResult{}, err
	}
	var ids []contract.SourceID
	if req.SourceID != "" {
		ids = []contract.SourceID{contract.SourceID(req.SourceID)}
	}
	result, err := s.refresher.Refresh(ctx, ids, req.AllowEmpty)
	if err != nil {
		return imports.RefreshResult{}, err
	}
	anyOK := false
	for _, row := range result.Sources {
		if row.OK {
			anyOK = true
			break
		}
	}
	if !anyOK && len(result.Sources) > 0 {
		return result, &contract.Failure{
			Kind: contract.SourceFetch, Path: contract.PathLocal, Op: "pool.refresh",
			Message: "every selected source failed; the previous pool was kept",
		}
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	before := s.Snapshot()
	after, err := s.store.Load(ctx)
	if err != nil {
		return result, err
	}
	removed := lostExits(before, after)
	version := after.State.PolicyVersion
	if len(removed) > 0 && version <= before.State.PolicyVersion {
		// Removing records changes the dialable set; fence dials that still
		// carry the old version before closing their connections.
		version = before.State.PolicyVersion + 1
		after, err = s.store.Mutate(ctx, func(st *contract.State) error {
			st.PolicyVersion = version
			return nil
		})
		if err != nil {
			return result, err
		}
	}
	if err := s.reconcileExits(ctx, before, after); err != nil {
		return result, err
	}
	nextPolicy, err := s.rebuildPolicy(after)
	if err != nil {
		return result, err
	}
	s.commitSnapshotLocked(after, nextPolicy)
	keep := func(ci contract.ConnInfo) bool {
		if _, gone := removed[ci.Exit]; gone {
			return false
		}
		return routeKeep(nextPolicy)(ci)
	}
	if _, err := s.sweep(ctx, contract.CloseExitGone, nextPolicy.PolicyVersion(), keep); err != nil {
		return result, err
	}
	s.logger.Logf(levelInfo, "pool.refresh", "sources=%d kept_last_good=%t", len(result.Sources), result.KeptLastGood)
	return result, nil
}

// lostExits returns exits that were dialable before and are not dialable now,
// whether because the record disappeared or because its status became
// invalid/unsupported. Those connections must be closed before success.
func lostExits(before, after contract.Snapshot) map[contract.ExitID]struct{} {
	now := make(map[contract.ExitID]bool)
	for _, rec := range dialableRecords(after.State) {
		now[contract.ExitID(rec.ID)] = true
	}
	out := make(map[contract.ExitID]struct{})
	for _, rec := range dialableRecords(before.State) {
		id := contract.ExitID(rec.ID)
		if !now[id] {
			out[id] = struct{}{}
		}
	}
	return out
}

// PoolSelectData is the `pool select` payload.
type PoolSelectData struct {
	Selected      contract.ExitID        `json:"selected"`
	PolicyVersion contract.PolicyVersion `json:"policy_version"`
}

func (s *Service) poolSelect(ctx context.Context, rawID string) (PoolSelectData, error) {
	rawID = strings.TrimSpace(rawID)
	if rawID == "" {
		return PoolSelectData{}, usage("pool select needs an exit id or 'auto'")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	before := s.Snapshot()
	after, nextPolicy, err := s.mutateAndCommit(ctx, func(st *contract.State) error {
		if rawID == "auto" {
			pool.ClearSelection(st)
			return nil
		}
		return pool.SetSelected(st, contract.ExitID(rawID))
	})
	if err != nil {
		if errors.Is(err, pool.ErrExitUnusable) {
			return PoolSelectData{}, &OpError{Kind: contract.StateLocked, Exit: contract.ExitConflict, Message: "the selected exit is not usable (invalid, unsupported or cooling)"}
		}
		return PoolSelectData{}, err
	}
	if nextPolicy.PolicyVersion() > before.State.PolicyVersion {
		if _, err := s.sweep(ctx, contract.CloseRuleChange, nextPolicy.PolicyVersion(), routeKeep(nextPolicy)); err != nil {
			return PoolSelectData{Selected: after.State.SelectedExit, PolicyVersion: nextPolicy.PolicyVersion()}, err
		}
	}
	s.logger.Logf(levelInfo, "pool.select", "selected=%s policy=%d", after.State.SelectedExit, nextPolicy.PolicyVersion())
	return PoolSelectData{Selected: after.State.SelectedExit, PolicyVersion: nextPolicy.PolicyVersion()}, nil
}

// ProbeTestResult is one `pool test --json` row.
type ProbeTestResult struct {
	Exit       contract.ExitID       `json:"exit"`
	Kind       contract.ProbeKind    `json:"kind"`
	Outcome    contract.ProbeOutcome `json:"outcome"`
	DurationMS int64                 `json:"duration_ms"`
	BytesDown  int64                 `json:"bytes_down"`
	Failure    contract.ErrorKind    `json:"failure,omitempty"`
}

// PoolTestData is the `pool test` payload. Truncated is set when a probe was
// canceled or hit its deadline before the batch finished; the completed rows
// are still returned (§4.6).
type PoolTestData struct {
	Results   []ProbeTestResult `json:"results"`
	Truncated bool              `json:"truncated,omitempty"`
}

func (s *Service) poolTest(ctx context.Context, req Request) (PoolTestData, error) {
	s.opMu.Lock()
	stopped := s.stopped
	s.opMu.Unlock()
	if stopped {
		return PoolTestData{}, &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "pool.test", Message: "service is stopped; no probe can run"}
	}
	if s.prober == nil {
		return PoolTestData{}, &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "pool.test", Message: "the probe engine is not running"}
	}
	var target contract.Destination
	kind := contract.ProbeTransport
	if strings.TrimSpace(req.Input) != "" {
		dst, ok := probes.ParseTarget(req.Input)
		if !ok {
			return PoolTestData{}, usage("pool test target must be host:port, tcp://host:port or tls://host:port")
		}
		target = dst
		kind = contract.ProbeEgress
	}
	snap := s.Snapshot()
	var exits []contract.ExitID
	if req.ID != "" {
		exits = []contract.ExitID{contract.ExitID(req.ID)}
	} else {
		p := pool.Build(snap.State, s.now().UTC())
		for _, rec := range p.Records() {
			if p.Usable(contract.ExitID(rec.ID)) {
				exits = append(exits, contract.ExitID(rec.ID))
			}
		}
	}
	if len(exits) == 0 {
		return PoolTestData{}, &contract.Failure{Kind: contract.PoolUnavailable, Path: contract.PathPool, Op: "pool.test", Message: "no usable exit to test"}
	}
	if len(exits) > contract.MaxPoolTestExits {
		exits = exits[:contract.MaxPoolTestExits]
	}
	results := make([]ProbeTestResult, 0, len(exits))
	var measurements []probes.Measurement
	truncated := false
	for _, exit := range exits {
		probeCtx, cancel := context.WithTimeout(ctx, contract.ProbeTimeout+2*time.Second)
		m, err := s.prober.RunMeasurement(probeCtx, contract.ProbeRequest{
			Kind: kind, Exit: exit, Destination: target, Trigger: contract.TriggerManual,
			PolicyVersion: snap.State.PolicyVersion,
		})
		cancel()
		if err != nil {
			truncated = true
			outcome := contract.OutcomeCanceled
			failure := contract.Canceled
			if errors.Is(err, context.DeadlineExceeded) {
				outcome = contract.OutcomeTimeout
				failure = contract.Timeout
			}
			results = append(results, ProbeTestResult{Exit: exit, Kind: kind, Outcome: outcome, Failure: failure})
			if ctx.Err() != nil {
				break
			}
			continue
		}
		measurements = append(measurements, m)
		results = append(results, ProbeTestResult{
			Exit: exit, Kind: m.Result.Kind, Outcome: m.Result.Outcome,
			DurationMS: m.Result.Duration.Milliseconds(), BytesDown: m.Result.BytesDown,
			Failure: m.Result.Failure,
		})
	}
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	now := s.now().UTC()
	after, err := s.store.Mutate(ctx, func(st *contract.State) error {
		for _, m := range measurements {
			pool.ApplyProbe(st, m.Result, now)
		}
		pool.SyncStatuses(st, now)
		return nil
	})
	if err != nil {
		return PoolTestData{Results: results, Truncated: truncated}, err
	}
	s.snap.Store(&after)
	s.logger.Logf(levelInfo, "pool.test", "results=%d truncated=%t", len(results), truncated)
	return PoolTestData{Results: results, Truncated: truncated}, nil
}

// PoolScanData is one `pool.scan` slice. Next is the cursor for the following
// call, or -1 when every exit was checked. Tested counts exits attempted, not
// probes (an exit with a configured target gets a transport and an egress
// probe). Results carries the same documented rows as `pool test`.
type PoolScanData struct {
	Tested    int               `json:"tested"`
	Total     int               `json:"total"`
	Next      int               `json:"next"`
	Results   []ProbeTestResult `json:"results,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
}

// poolScan checks every dialable exit in stable pool order, starting at the
// caller's cursor. It never changes the mode, connected users or the automatic
// selection; only health evidence and its cached summary are written. One call
// is bounded by PoolScanBudget so the tray can show progress through repeated
// calls instead of holding one management request for the whole pool.
func (s *Service) poolScan(ctx context.Context, req Request) (PoolScanData, error) {
	return s.poolScanWithBudget(ctx, req, contract.PoolScanBudget)
}

// poolScanWithBudget is poolScan with an injectable probe budget so tests can
// exercise the cutoff deterministically without waiting for the real bound.
func (s *Service) poolScanWithBudget(ctx context.Context, req Request, budget time.Duration) (PoolScanData, error) {
	s.opMu.Lock()
	stopped := s.stopped
	s.opMu.Unlock()
	if stopped {
		return PoolScanData{}, &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "pool.scan", Message: "service is stopped; no probe can run"}
	}
	if s.prober == nil {
		return PoolScanData{}, &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "pool.scan", Message: "the probe engine is not running"}
	}
	snap := s.Snapshot()
	p := pool.Build(snap.State, s.now().UTC())
	exits := make([]contract.ExitID, 0, len(snap.State.Records))
	for _, rec := range p.Records() {
		if pool.Dialable(rec) {
			exits = append(exits, contract.ExitID(rec.ID))
		}
	}
	total := len(exits)
	if total == 0 {
		return PoolScanData{Tested: 0, Total: 0, Next: -1}, nil
	}
	cursor := req.Offset
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= total {
		cursor = total
	}
	limit := req.Limit
	if limit <= 0 || limit > contract.PoolScanBatch {
		limit = contract.PoolScanBatch
	}
	var targets []contract.Destination
	for _, raw := range snap.Config.Probes.Targets {
		if dst, ok := probes.ParseTarget(raw); ok {
			targets = append(targets, dst)
		}
	}

	scanCtx, cancelScan := context.WithTimeout(ctx, budget)
	defer cancelScan()
	results := make([]ProbeTestResult, 0, limit)
	measurements := make([]probes.Measurement, 0, limit)
	tested := 0
	end := cursor
	for end < total && tested < limit && scanCtx.Err() == nil {
		exit := exits[end]
		probeCtx, cancel := context.WithTimeout(scanCtx, contract.ProbeTimeout+2*time.Second)
		m, err := s.prober.RunMeasurement(probeCtx, contract.ProbeRequest{
			Kind: contract.ProbeTransport, Exit: exit, Trigger: contract.TriggerRerank,
			PolicyVersion: snap.State.PolicyVersion,
		})
		cancel()
		if err != nil {
			results = append(results, probeErrorResult(exit, contract.ProbeTransport, err))
		} else {
			measurements = append(measurements, m)
			results = append(results, probeResultRow(exit, m))
			for _, dst := range targets {
				if scanCtx.Err() != nil {
					break
				}
				probeCtx, cancel := context.WithTimeout(scanCtx, contract.ProbeTimeout+2*time.Second)
				em, eerr := s.prober.RunMeasurement(probeCtx, contract.ProbeRequest{
					Kind: contract.ProbeEgress, Exit: exit, Destination: dst,
					Trigger: contract.TriggerRerank, PolicyVersion: snap.State.PolicyVersion,
				})
				cancel()
				if eerr != nil {
					results = append(results, probeErrorResult(exit, contract.ProbeEgress, eerr))
					continue
				}
				measurements = append(measurements, em)
				results = append(results, probeResultRow(exit, em))
			}
		}
		tested++
		end++
	}

	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	now := s.now().UTC()
	after, err := s.store.Mutate(ctx, func(st *contract.State) error {
		for _, m := range measurements {
			pool.ApplyProbe(st, m.Result, now)
		}
		pool.SyncStatuses(st, now)
		return nil
	})
	if err != nil {
		return PoolScanData{Tested: tested, Total: total, Next: -1, Results: results}, err
	}
	s.snap.Store(&after)
	next := end
	if next >= total {
		next = -1
	}
	truncated := next != -1
	s.logger.Logf(levelInfo, "pool.scan", "tested=%d total=%d next=%d truncated=%t", tested, total, next, truncated)
	return PoolScanData{Tested: tested, Total: total, Next: next, Results: results, Truncated: truncated}, nil
}

func probeResultRow(exit contract.ExitID, m probes.Measurement) ProbeTestResult {
	return ProbeTestResult{
		Exit: exit, Kind: m.Result.Kind, Outcome: m.Result.Outcome,
		DurationMS: m.Result.Duration.Milliseconds(), BytesDown: m.Result.BytesDown,
		Failure: m.Result.Failure,
	}
}

func probeErrorResult(exit contract.ExitID, kind contract.ProbeKind, err error) ProbeTestResult {
	outcome := contract.OutcomeCanceled
	failure := contract.Canceled
	if errors.Is(err, context.DeadlineExceeded) {
		outcome = contract.OutcomeTimeout
		failure = contract.Timeout
	}
	return ProbeTestResult{Exit: exit, Kind: kind, Outcome: outcome, Failure: failure}
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
func mapImportError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, imports.ErrSourceNotFound):
		return NotFound("source")
	}
	var failure *contract.Failure
	if errors.As(err, &failure) {
		return failure
	}
	return err
}

// RerankData is the `pool rerank` payload. It reports the automatic selection
// before and after a re-rank, why every other exit was skipped, and the policy
// version (which rerank never advances).
type RerankData struct {
	SelectedBefore contract.ExitID        `json:"selected_before"`
	SelectedAfter  contract.ExitID        `json:"selected_after"`
	Skipped        []pool.RerankSkip      `json:"skipped"`
	PolicyVersion  contract.PolicyVersion `json:"policy_version"`
}

func (s *Service) rerank(ctx context.Context) (RerankData, error) {
	if err := ctx.Err(); err != nil {
		return RerankData{}, &contract.Failure{Kind: contract.Canceled, Path: contract.PathLocal, Op: "pool.rerank", Message: "rerank canceled"}
	}
	s.opMu.Lock()
	stopped := s.stopped
	reranker := s.reranker
	snap := s.Snapshot()
	s.opMu.Unlock()
	if stopped {
		return RerankData{}, &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "pool.rerank", Message: "service is stopped; no exit can be selected"}
	}
	var (
		result pool.RerankResult
		err    error
	)
	if reranker != nil {
		// Production: re-rank the selector that future pool dials actually use.
		result, err = reranker.Rerank(s.now().UTC())
	} else {
		// A management-only service has no dialer to affect; use a throwaway
		// selector so the command can still report the same ranking decision.
		result, err = pool.NewSelector().Rerank(snap.State, s.now().UTC())
	}
	if err != nil {
		return RerankData{}, err
	}
	data := RerankData{
		SelectedBefore: result.Before,
		SelectedAfter:  result.After,
		Skipped:        result.Skipped,
		PolicyVersion:  result.Version,
	}
	if data.Skipped == nil {
		data.Skipped = []pool.RerankSkip{}
	}
	if data.PolicyVersion == 0 {
		data.PolicyVersion = snap.State.PolicyVersion
	}
	if data.SelectedAfter == "" {
		return RerankData{}, &OpError{
			Kind: contract.PoolUnavailable, Exit: contract.ExitPoolUnavailable,
			Message: "no usable exit: the pool is empty, cooling, unsupported or has no target",
			Detail: map[string]any{
				"selected_before": data.SelectedBefore,
				"skipped":         data.Skipped,
				"policy_version":  data.PolicyVersion,
			},
		}
	}
	s.logger.Logf(levelInfo, "pool.rerank", "before=%s after=%s skipped=%d policy=%d", data.SelectedBefore, data.SelectedAfter, len(data.Skipped), data.PolicyVersion)
	return data, nil
}
