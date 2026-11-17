package proposals

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"routelatch/internal/contract"
)

// UnrouteID identifies one unroute suggestion. It is the ProposalID of the
// contract.Proposal stored in State.Proposals with Kind ProposalUnroute, so an
// unroute suggestion persists and expires exactly like a route proposal. It
// follows the same pending/accepted/rejected/expired lifecycle; accepting it
// returns the rule ID for the caller to remove after explicit confirmation, and
// the registry itself removes nothing.
type UnrouteID string

// UnrouteSuggestion is the command-facing view of one unroute proposal. The
// persisted form is contract.Proposal{Kind: ProposalUnroute, RuleID: ...}.
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

func unrouteView(p contract.Proposal) UnrouteSuggestion {
	return UnrouteSuggestion{
		ID:          UnrouteID(p.ID),
		RuleID:      p.RuleID,
		Destination: p.Destination,
		Status:      p.Status,
		CreatedAt:   p.CreatedAt,
		DecidedAt:   p.DecidedAt,
		ExpiresAt:   p.ExpiresAt,
		Note:        p.Note,
	}
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
	r.expireLocked(now)
	id := contract.ProposalID(StableUnrouteID(ruleID))
	existing, ok := r.byID[id]
	if ok && (existing.Kind != contract.ProposalUnroute || existing.Status == contract.ProposalExpired) {
		ok = false
	}
	if ok {
		if note != "" {
			existing.Note = note
		}
		r.byID[id] = existing
		return unrouteView(existing), false, nil
	}
	s := contract.Proposal{
		ID: id, Kind: contract.ProposalUnroute, RuleID: ruleID, Destination: dst,
		Status: contract.ProposalPending, CreatedAt: now,
		ExpiresAt: now.Add(r.retain), Note: note,
	}
	r.byID[id] = s
	return unrouteView(s), true, nil
}

// ListUnroutes returns every retained unroute suggestion, pending first.
func (r *Registry) ListUnroutes() []UnrouteSuggestion {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked(r.now().UTC())
	out := make([]UnrouteSuggestion, 0, len(r.byID))
	for _, p := range r.byID {
		if p.Kind == contract.ProposalUnroute {
			out = append(out, unrouteView(p))
		}
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
	r.expireLocked(now)
	pid := contract.ProposalID(id)
	s, ok := r.byID[pid]
	if !ok || s.Kind != contract.ProposalUnroute {
		return "", notFoundFailure("proposals.accept_unroute")
	}
	switch s.Status {
	case contract.ProposalPending:
		s.Status = contract.ProposalAccepted
		s.DecidedAt = now
		r.byID[pid] = s
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
	r.expireLocked(now)
	pid := contract.ProposalID(id)
	s, ok := r.byID[pid]
	if !ok || s.Kind != contract.ProposalUnroute {
		return notFoundFailure("proposals.reject_unroute")
	}
	switch s.Status {
	case contract.ProposalPending:
		s.Status = contract.ProposalRejected
		s.DecidedAt = now
		if note != "" {
			s.Note = note
		}
		r.byID[pid] = s
		return nil
	case contract.ProposalExpired:
		return ErrExpired
	default:
		return ErrNotPending
	}
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
