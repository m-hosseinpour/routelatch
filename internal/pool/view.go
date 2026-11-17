// Package pool owns the import/trust status view, health bookkeeping, ranking
// and exit selection. It never starts, restarts or reconfigures the engine: a
// selection is a value returned to the dial path (docs/contracts.md §6).
package pool

import (
	"sort"
	"time"

	"routelatch/internal/contract"
)

// Pool is an immutable view built from one consistent state snapshot.
type Pool struct {
	records []contract.Record
	byID    map[contract.ExitID]int
	health  map[contract.ExitID]contract.ExitHealth
	now     time.Time
}

// Build snapshots the records with their effective status. Expired cooldowns
// read as unknown; the persisted state is normalized by SyncStatuses during a
// mutation, not on the dial path.
func Build(state contract.State, now time.Time) *Pool {
	p := &Pool{
		records: make([]contract.Record, len(state.Records)),
		byID:    make(map[contract.ExitID]int, len(state.Records)),
		health:  make(map[contract.ExitID]contract.ExitHealth, len(state.Health)),
		now:     now,
	}
	for _, h := range state.Health {
		p.health[h.Exit] = h
	}
	copy(p.records, state.Records)
	sort.SliceStable(p.records, func(i, j int) bool {
		if p.records[i].Order != p.records[j].Order {
			return p.records[i].Order < p.records[j].Order
		}
		return p.records[i].ID < p.records[j].ID
	})
	for i := range p.records {
		rec := &p.records[i]
		rec.Status, rec.StatusReason = EffectiveStatus(*rec, p.health[contract.ExitID(rec.ID)], now)
		p.byID[contract.ExitID(rec.ID)] = i
	}
	return p
}

// Records returns the stable display order (user order, then id).
func (p *Pool) Records() []contract.Record { return p.records }

// Record returns one record by exit id.
func (p *Pool) Record(exit contract.ExitID) (contract.Record, bool) {
	i, ok := p.byID[exit]
	if !ok {
		return contract.Record{}, false
	}
	return p.records[i], true
}

// Health returns the persisted health entry for an exit.
func (p *Pool) Health(exit contract.ExitID) (contract.ExitHealth, bool) {
	h, ok := p.health[exit]
	return h, ok
}

// Usable reports whether selection may dial the exit. Invalid, unsupported and
// currently-cooling exits are not usable; unknown is usable because a positive
// sample may not exist yet.
func (p *Pool) Usable(exit contract.ExitID) bool {
	rec, ok := p.Record(exit)
	if !ok {
		return false
	}
	return (rec.Status == contract.RecordHealthy || rec.Status == contract.RecordUnknown) && Dialable(rec)
}

// Dialable is defense in depth for records that predate a validation change:
// a skip-verify record without a pin and a REALITY record are never dialable in
// v1 even if their cached status says unknown.
func Dialable(rec contract.Record) bool {
	if rec.InsecureTLS && rec.PinnedPeerCertSHA256 == "" {
		return false
	}
	if rec.Security == contract.SecurityReality {
		return false
	}
	if rec.Protocol == contract.ProtocolHTTP {
		return false
	}
	return rec.Status != contract.RecordInvalid && rec.Status != contract.RecordUnsupported
}

// Counts summarizes the pool for status/CLI output.
type Counts struct {
	Total       int `json:"total"`
	Usable      int `json:"usable"`
	Healthy     int `json:"healthy"`
	Cooling     int `json:"cooling"`
	Unknown     int `json:"unknown"`
	Unsupported int `json:"unsupported"`
	Invalid     int `json:"invalid"`
}

// Counts computes the status counters.
func (p *Pool) Counts() Counts {
	var c Counts
	for _, rec := range p.records {
		c.Total++
		switch rec.Status {
		case contract.RecordHealthy:
			c.Healthy++
			if Dialable(rec) {
				c.Usable++
			}
		case contract.RecordUnknown:
			c.Unknown++
			if Dialable(rec) {
				c.Usable++
			}
		case contract.RecordCooling:
			c.Cooling++
		case contract.RecordUnsupported:
			c.Unsupported++
		case contract.RecordInvalid:
			c.Invalid++
		}
	}
	return c
}

// EffectiveStatus combines import validity with the cached health bucket. A
// destination-level failure never changes the record; only a transport-level
// failure that reaches the exit sets cooling (docs/contracts.md §5.3, §6.2).
func EffectiveStatus(rec contract.Record, h contract.ExitHealth, now time.Time) (contract.RecordStatus, string) {
	if rec.Status == contract.RecordInvalid {
		return contract.RecordInvalid, nonEmpty(rec.StatusReason, "invalid")
	}
	if rec.Status == contract.RecordUnsupported {
		return contract.RecordUnsupported, nonEmpty(rec.StatusReason, "unsupported")
	}
	if h.Exit == contract.ExitID(rec.ID) {
		switch h.Status {
		case contract.HealthCooling:
			if h.CooldownUntil.After(now) {
				return contract.RecordCooling, cooldownReason(h)
			}
			return contract.RecordUnknown, ""
		case contract.HealthHealthy:
			return contract.RecordHealthy, ""
		}
	}
	switch rec.Status {
	case contract.RecordHealthy:
		return contract.RecordHealthy, ""
	case contract.RecordCooling:
		return contract.RecordCooling, nonEmpty(rec.StatusReason, "cooling")
	default:
		return contract.RecordUnknown, ""
	}
}

func cooldownReason(h contract.ExitHealth) string {
	if h.LastFailureKind != "" {
		return "cooling:" + string(h.LastFailureKind)
	}
	return "cooling"
}

func nonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// Rows is the list view used by `pool list`. It contains no credential, source
// origin or share link.
type Row struct {
	ID          contract.RecordID     `json:"id"`
	Status      contract.RecordStatus `json:"status"`
	Reason      string                `json:"status_reason,omitempty"`
	DisplayName string                `json:"display_name,omitempty"`
	Protocol    contract.Protocol     `json:"protocol"`
	Server      string                `json:"server"`
	Port        uint16                `json:"port"`
	Transport   contract.Transport    `json:"transport"`
	Security    contract.Security     `json:"security"`
	Sources     []contract.SourceID   `json:"sources"`
	Health      *contract.ExitHealth  `json:"health,omitempty"`
	InsecureTLS bool                  `json:"insecure_tls,omitempty"`
	Order       int                   `json:"order"`
}

// List returns stable rows with effective status and cached health.
func (p *Pool) List() []Row {
	rows := make([]Row, 0, len(p.records))
	for _, rec := range p.records {
		row := Row{
			ID: rec.ID, Status: rec.Status, Reason: rec.StatusReason,
			DisplayName: rec.DisplayName, Protocol: rec.Protocol,
			Server: rec.Server, Port: rec.Port, Transport: rec.Transport,
			Security: rec.Security, Sources: append([]contract.SourceID(nil), rec.SourceIDs...),
			InsecureTLS: rec.InsecureTLS, Order: rec.Order,
		}
		if h, ok := p.health[contract.ExitID(rec.ID)]; ok {
			copy := h
			row.Health = &copy
		}
		rows = append(rows, row)
	}
	return rows
}

// RecordsForSource returns the records one source currently owns.
func (p *Pool) RecordsForSource(id contract.SourceID) []contract.Record {
	var out []contract.Record
	for _, rec := range p.records {
		for _, src := range rec.SourceIDs {
			if src == id {
				out = append(out, rec)
				break
			}
		}
	}
	return out
}
