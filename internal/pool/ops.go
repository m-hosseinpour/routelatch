package pool

import (
	"errors"

	"routelatch/internal/contract"
)

// ErrExitNotFound and ErrExitUnusable let the CLI map command failures without
// inventing an ErrorKind. The frozen contract has no not-found kind.
var (
	ErrExitNotFound = errors.New("exit not found")
	ErrExitUnusable = errors.New("exit is not usable")
)

// RemoveRecord removes one record and its health entry. The caller closes live
// connections for the returned exit with CloseExitGone; this package only owns
// state. The policy version is bumped so a dial carrying the old version is
// fenced before it can use a removed tag.
func RemoveRecord(st *contract.State, id contract.RecordID) (contract.Record, bool) {
	for i := range st.Records {
		if st.Records[i].ID != id {
			continue
		}
		rec := st.Records[i]
		st.Records = append(st.Records[:i], st.Records[i+1:]...)
		removeHealth(st, contract.ExitID(id))
		if st.SelectedExit == contract.ExitID(id) {
			st.SelectedExit = ""
		}
		refreshSourceCounts(st)
		st.PolicyVersion++
		return rec, true
	}
	return contract.Record{}, false
}

// RemoveSource removes one source's references. Records that no other source
// owns are deleted, their health is pruned and the policy version is bumped if
// anything became unavailable.
func RemoveSource(st *contract.State, id contract.SourceID) (removed []contract.Record, found bool) {
	keptSources := st.Sources[:0]
	for _, src := range st.Sources {
		if src.ID == id {
			found = true
			continue
		}
		keptSources = append(keptSources, src)
	}
	st.Sources = keptSources
	if !found {
		return nil, false
	}
	keptRecords := make([]contract.Record, 0, len(st.Records))
	changed := false
	for _, rec := range st.Records {
		refs := rec.SourceIDs[:0]
		for _, src := range rec.SourceIDs {
			if src != id {
				refs = append(refs, src)
			}
		}
		rec.SourceIDs = refs
		if len(rec.SourceIDs) == 0 {
			removed = append(removed, rec)
			changed = true
			continue
		}
		keptRecords = append(keptRecords, rec)
	}
	st.Records = keptRecords
	pruneHealthState(st)
	refreshSourceCounts(st)
	if changed {
		st.PolicyVersion++
	}
	return removed, true
}

// SetSelected pins one exit for new pool dials (empty means automatic). The
// selection is state only: the engine is not restarted or retagged.
func SetSelected(st *contract.State, exit contract.ExitID) error {
	if exit == "" {
		if st.SelectedExit != "" {
			st.SelectedExit = ""
			st.PolicyVersion++
		}
		return nil
	}
	var found *contract.Record
	for i := range st.Records {
		if contract.ExitID(st.Records[i].ID) == exit {
			found = &st.Records[i]
			break
		}
	}
	if found == nil {
		return ErrExitNotFound
	}
	if found.Status == contract.RecordInvalid || found.Status == contract.RecordUnsupported || !Dialable(*found) {
		return ErrExitUnusable
	}
	if st.SelectedExit != exit {
		st.SelectedExit = exit
		st.PolicyVersion++
	}
	return nil
}

// ClearSelection is the explicit `pool select auto` operation.
func ClearSelection(st *contract.State) {
	if st.SelectedExit != "" {
		st.SelectedExit = ""
		st.PolicyVersion++
	}
}

func refreshSourceCounts(st *contract.State) {
	counts := make(map[contract.SourceID]int, len(st.Sources))
	for _, rec := range st.Records {
		for _, id := range rec.SourceIDs {
			counts[id]++
		}
	}
	for i := range st.Sources {
		st.Sources[i].RecordCount = counts[st.Sources[i].ID]
	}
}

func removeHealth(st *contract.State, exit contract.ExitID) {
	kept := st.Health[:0]
	for _, h := range st.Health {
		if h.Exit != exit {
			kept = append(kept, h)
		}
	}
	st.Health = kept
}

func pruneHealthState(st *contract.State) {
	live := make(map[contract.ExitID]bool, len(st.Records))
	for _, rec := range st.Records {
		live[contract.ExitID(rec.ID)] = true
	}
	kept := st.Health[:0]
	for _, h := range st.Health {
		if live[h.Exit] {
			kept = append(kept, h)
		}
	}
	st.Health = kept
	if st.SelectedExit != "" && !live[st.SelectedExit] {
		st.SelectedExit = ""
	}
}

// SetRecordStatus is used by revalidation paths outside a probe result. It
// refuses to overwrite an import-level invalid/unsupported classification.
func SetRecordStatus(st *contract.State, exit contract.ExitID, status contract.RecordStatus, reason string) {
	for i := range st.Records {
		if contract.ExitID(st.Records[i].ID) != exit {
			continue
		}
		if st.Records[i].Status == contract.RecordInvalid || st.Records[i].Status == contract.RecordUnsupported {
			return
		}
		st.Records[i].Status = status
		st.Records[i].StatusReason = reason
		return
	}
}
