package proposals

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"routelatch/internal/contract"
)

// UnrouteID identifies one unroute suggestion.
//
// contract.Proposal describes only an addition to the approved list and has no
// rule-id or kind field, so the removal path needs its own small type. It
// follows the same pending/accepted/rejected/expired lifecycle and retention,
// and accepting it returns the rule ID for the caller to remove after explicit
// confirmation; the registry itself removes nothing. Persisting it needs a
// state-contract field and is therefore escalated rather than invented here.
type UnrouteID string

// UnrouteSuggestion is a suggestion to remove one approved rule.
type UnrouteSuggestion struct {
	ID          UnrouteID               `json:"id"`
	RuleID      contract.RuleID         `json:"rule_id"`
	Destination contract.Destination    `json:"destination"`
	Status      contract.ProposalStatus `json:"status"`
	CreatedAt   time.Time               `json:"created_at"`
	DecidedAt   time.Time               `json:"decided_at,omitzero"`
	ExpiresAt   time.Time               `json:"expires_at"`
	Note        string                  `json:"note,omitempty"`
}

// StableUnrouteID derives one stable suggestion per rule.
func StableUnrouteID(ruleID contract.RuleID) UnrouteID {
	sum := sha256.Sum256([]byte("unroute:" + string(ruleID)))
	return UnrouteID("u-" + hex.EncodeToString(sum[:8]))
}

// SuggestUnroute records a suggestion to remove an approved rule. It is only
// a proposal: AcceptUnroute still needs the user's confirmation and no rule is
// removed here.
func (r *Registry) SuggestUnroute(ruleID contract.RuleID, dst contract.Destination, note string) (UnrouteSuggestion, bool, error) {
	if strings.TrimSpace(string(ruleID)) == "" || !validDestination(dst) {
		return UnrouteSuggestion{}, false, ErrInvalidProposal
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	r.expireUnroutesLocked(now)
	id := StableUnrouteID(ruleID)
	existing, ok := r.unroutes[id]
	if ok && existing.Status == contract.ProposalExpired {
		ok = false
	}
	if ok {
		if note != "" {
			existing.Note = note
		}
		r.unroutes[id] = existing
		return existing, false, nil
	}
	s := UnrouteSuggestion{
		ID: id, RuleID: ruleID, Destination: dst,
		Status: contract.ProposalPending, CreatedAt: now,
		ExpiresAt: now.Add(r.retain), Note: note,
	}
	r.unroutes[id] = s
	return s, true, nil
}

// ListUnroutes returns every retained unroute suggestion, pending first.
func (r *Registry) ListUnroutes() []UnrouteSuggestion {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireUnroutesLocked(r.now().UTC())
	out := make([]UnrouteSuggestion, 0, len(r.unroutes))
	for _, s := range r.unroutes {
		out = append(out, s)
	}
	sortUnroutes(out)
	return out
}

// PendingUnroutes returns suggestions that still await confirmation.
func (r *Registry) PendingUnroutes() []UnrouteSuggestion {
	all := r.ListUnroutes()
	out := all[:0]
	for _, s := range all {
		if s.Status == contract.ProposalPending {
			out = append(out, s)
		}
	}
	return out
}

// AcceptUnroute marks the suggestion accepted and returns exactly the rule ID
// the caller may remove after its own confirmation step.
func (r *Registry) AcceptUnroute(id UnrouteID) (contract.RuleID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	r.expireUnroutesLocked(now)
	s, ok := r.unroutes[id]
	if !ok {
		return "", ErrNotFound
	}
	switch s.Status {
	case contract.ProposalPending:
		s.Status = contract.ProposalAccepted
		s.DecidedAt = now
		r.unroutes[id] = s
		return s.RuleID, nil
	case contract.ProposalExpired:
		return "", ErrExpired
	default:
		return "", ErrNotPending
	}
}

// RejectUnroute records a user rejection of a removal suggestion.
func (r *Registry) RejectUnroute(id UnrouteID, note string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	r.expireUnroutesLocked(now)
	s, ok := r.unroutes[id]
	if !ok {
		return ErrNotFound
	}
	switch s.Status {
	case contract.ProposalPending:
		s.Status = contract.ProposalRejected
		s.DecidedAt = now
		if note != "" {
			s.Note = note
		}
		r.unroutes[id] = s
		return nil
	case contract.ProposalExpired:
		return ErrExpired
	default:
		return ErrNotPending
	}
}

func (r *Registry) expireUnroutesLocked(now time.Time) int {
	n := 0
	for id, s := range r.unroutes {
		if s.Status != contract.ProposalPending || s.ExpiresAt.IsZero() || now.Before(s.ExpiresAt) {
			continue
		}
		s.Status = contract.ProposalExpired
		s.DecidedAt = now
		r.unroutes[id] = s
		n++
	}
	return n
}

func sortUnroutes(list []UnrouteSuggestion) {
	sort.SliceStable(list, func(i, j int) bool {
		ri, rj := statusRank(list[i].Status), statusRank(list[j].Status)
		if ri != rj {
			return ri < rj
		}
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.After(list[j].CreatedAt)
		}
		return list[i].ID < list[j].ID
	})
}
