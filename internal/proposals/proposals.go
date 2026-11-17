// Package proposals owns the diagnostic proposal registry. A proposal is a
// suggestion to approve one destination (Kind route) or remove one approved
// rule (Kind unroute); it never changes routing and never creates or deletes a
// rule (D09, docs/contracts.md §3.5, §4.3). The registry stores no request
// material: route evidence keeps only destination, port, probe kind, outcome
// and time.
//
// The registry is process memory only (D21). A destination that passed through
// RouteLatch is never written to state, config, a backup or a log; a proposal
// and its evidence live for the running service and disappear at restart.
package proposals

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"routelatch/internal/contract"
)

// Errors returned by the registry. They are not routing failures; the control
// layer maps them to the `proposals` command's own exit codes.
var (
	ErrNotFound        = errors.New("proposal not found")
	ErrNotPending      = errors.New("proposal was already decided")
	ErrExpired         = errors.New("proposal expired")
	ErrInvalidEvidence = errors.New("invalid proposal evidence")
	ErrInvalidProposal = errors.New("invalid proposal")
)

const maxEvidence = 8

// notFoundFailure keeps ErrNotFound as a wrapped cause while exposing the
// contract kind that maps a CLI lookup miss to exit code 5.
func notFoundFailure(op string) *contract.Failure {
	return &contract.Failure{
		Kind: contract.NotFound, Path: contract.PathLocal, Op: op,
		Message: ErrNotFound.Error(), Err: ErrNotFound,
	}
}

// Registry is a bounded in-memory proposal store. It never touches disk: the
// control layer lists, decides and clears proposals in this process only, so a
// restart starts empty (D21). Route and unroute suggestions share one map and
// are distinguished by contract.Proposal.Kind.
type Registry struct {
	mu     sync.Mutex
	byID   map[contract.ProposalID]contract.Proposal
	now    func() time.Time
	retain time.Duration
}

// New creates an empty registry. now is optional and exists for tests.
func New(now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{
		byID:   make(map[contract.ProposalID]contract.Proposal),
		now:    now,
		retain: contract.ProposalRetention,
	}
}

// StableID derives the proposal ID from the rule operand, not the port: rules
// are port-agnostic in v1, so failures on different ports of one host update a
// single proposal instead of piling up duplicates.
func StableID(dst contract.Destination) contract.ProposalID {
	var operand string
	switch dst.Kind {
	case contract.DestDomain:
		operand = "domain:" + dst.Host
	case contract.DestIP:
		operand = "ip:" + dst.Addr.Unmap().String()
	case contract.DestCIDR:
		operand = "cidr:" + dst.Prefix.Masked().String()
	default:
		operand = "invalid:" + string(dst.Kind)
	}
	sum := sha256.Sum256([]byte(operand))
	return contract.ProposalID("p-" + hex.EncodeToString(sum[:8]))
}

func validDestination(dst contract.Destination) bool {
	if dst.Port == 0 {
		return false
	}
	switch dst.Kind {
	case contract.DestDomain:
		return dst.Host != "" && !strings.ContainsAny(dst.Host, " /@?#\t\r\n")
	case contract.DestIP:
		return dst.Addr.IsValid() && !dst.Addr.IsUnspecified() && dst.Addr.Zone() == ""
	default:
		return false
	}
}

func validEvidence(ev contract.Evidence) bool {
	if ev.Address == "" || len(ev.Address) > 512 || strings.ContainsAny(ev.Address, "/@?#\t\r\n") ||
		strings.Contains(ev.Address, "://") {
		return false
	}
	host, portRaw, err := net.SplitHostPort(ev.Address)
	if err != nil || host == "" {
		return false
	}
	port, err := strconv.ParseUint(portRaw, 10, 16)
	if err != nil || port == 0 {
		return false
	}
	switch ev.Kind {
	case contract.EvidenceDirectRefused:
		if ev.Outcome != contract.OutcomeRefused {
			return false
		}
	case contract.EvidenceDirectTimeout:
		if ev.Outcome != contract.OutcomeTimeout {
			return false
		}
	case contract.EvidenceDirectDNS:
		if ev.Outcome != contract.OutcomeDNSFailure {
			return false
		}
	default:
		return false
	}
	return ev.ProbeKind == contract.ProbeDirectTCP || ev.ProbeKind == contract.ProbeDirectTLS
}

func copyProposal(p contract.Proposal) contract.Proposal {
	p.Evidence = append([]contract.Evidence(nil), p.Evidence...)
	return p
}

// Upsert records one piece of evidence for a destination and returns the
// proposal. A new proposal starts pending; an existing one keeps its status
// and ExpiresAt, so a rejected or expired proposal is never silently revived.
// The second return value reports whether the proposal was created.
func (r *Registry) Upsert(dst contract.Destination, ev contract.Evidence, note string) (contract.Proposal, bool, error) {
	if !validDestination(dst) {
		return contract.Proposal{}, false, ErrInvalidProposal
	}
	if ev.At.IsZero() {
		ev.At = r.now().UTC()
	} else {
		ev.At = ev.At.UTC()
	}
	if !validEvidence(ev) {
		return contract.Proposal{}, false, ErrInvalidEvidence
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	r.expireLocked(now)
	id := StableID(dst)
	existing, ok := r.byID[id]
	// An expired proposal is stale, not a user decision: a new eligible
	// failure starts a fresh retention window. A rejected one is never
	// revived this way, and an accepted one stays accepted.
	if !ok || existing.Status == contract.ProposalExpired {
		p := contract.Proposal{
			ID:          id,
			Kind:        contract.ProposalRoute,
			Destination: dst,
			Evidence:    []contract.Evidence{ev},
			Status:      contract.ProposalPending,
			CreatedAt:   now,
			ExpiresAt:   now.Add(r.retain),
			Note:        note,
		}
		r.byID[id] = p
		return copyProposal(p), true, nil
	}
	existing.Evidence = appendEvidence(existing.Evidence, ev)
	if note != "" {
		existing.Note = note
	}
	r.byID[id] = existing
	return copyProposal(existing), false, nil
}

func appendEvidence(list []contract.Evidence, ev contract.Evidence) []contract.Evidence {
	list = append(list, ev)
	if len(list) > maxEvidence {
		list = list[len(list)-maxEvidence:]
	}
	return list
}

// List returns every retained route proposal, pending first and newest first
// inside each status bucket. Unroute suggestions are listed separately.
func (r *Registry) List() []contract.Proposal {
	all := r.listAll()
	out := all[:0]
	for _, p := range all {
		if p.Kind == contract.ProposalUnroute {
			continue
		}
		out = append(out, p)
	}
	return out
}

// listAll returns both proposal kinds, sorted for display.
func (r *Registry) listAll() []contract.Proposal {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked(r.now().UTC())
	out := make([]contract.Proposal, 0, len(r.byID))
	for _, p := range r.byID {
		out = append(out, copyProposal(p))
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := statusRank(out[i].Status), statusRank(out[j].Status)
		if ri != rj {
			return ri < rj
		}
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func statusRank(s contract.ProposalStatus) int {
	switch s {
	case contract.ProposalPending:
		return 0
	case contract.ProposalRejected:
		return 1
	case contract.ProposalExpired:
		return 2
	case contract.ProposalAccepted:
		return 3
	default:
		return 4
	}
}

// Pending returns only proposals that still await a user decision.
func (r *Registry) Pending() []contract.Proposal {
	all := r.List()
	out := all[:0]
	for _, p := range all {
		if p.Status == contract.ProposalPending {
			out = append(out, p)
		}
	}
	return out
}

// Get returns one proposal by ID, of either kind. The caller decides what it
// can do with an unroute suggestion; the route-only operations (Accept,
// Reject, Recheck) reject an unroute ID as not found.
func (r *Registry) Get(id contract.ProposalID) (contract.Proposal, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked(r.now().UTC())
	p, ok := r.byID[id]
	if !ok {
		return contract.Proposal{}, false
	}
	return copyProposal(p), true
}

// Accept marks a pending proposal accepted and returns exactly the destination
// that the rule layer should apply. The registry writes no rule and changes no
// routing state; confirmation belongs to the caller.
func (r *Registry) Accept(id contract.ProposalID) (contract.Destination, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	r.expireLocked(now)
	p, ok := r.byID[id]
	if !ok || p.Kind == contract.ProposalUnroute {
		return contract.Destination{}, notFoundFailure("proposals.accept")
	}
	switch p.Status {
	case contract.ProposalPending:
		p.Status = contract.ProposalAccepted
		p.DecidedAt = now
		r.byID[id] = p
		return p.Destination, nil
	case contract.ProposalExpired:
		return contract.Destination{}, ErrExpired
	default:
		return contract.Destination{}, ErrNotPending
	}
}

// Reject marks a pending proposal rejected. The destination stays visible for
// the retention window so a later recheck can compare against the decision.
func (r *Registry) Reject(id contract.ProposalID, note string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	r.expireLocked(now)
	p, ok := r.byID[id]
	if !ok || p.Kind == contract.ProposalUnroute {
		return notFoundFailure("proposals.reject")
	}
	switch p.Status {
	case contract.ProposalPending:
		p.Status = contract.ProposalRejected
		p.DecidedAt = now
		if note != "" {
			p.Note = note
		}
		r.byID[id] = p
		return nil
	case contract.ProposalExpired:
		return ErrExpired
	default:
		return ErrNotPending
	}
}

// Recheck appends fresh evidence. It never changes status: a rejected
// proposal stays rejected and an expired one must be cleared, so nothing is
// revived without an explicit new user decision. The boolean reports whether
// evidence was added.
func (r *Registry) Recheck(id contract.ProposalID, ev contract.Evidence, note string) (contract.Proposal, bool, error) {
	if ev.At.IsZero() {
		ev.At = r.now().UTC()
	} else {
		ev.At = ev.At.UTC()
	}
	if !validEvidence(ev) {
		return contract.Proposal{}, false, ErrInvalidEvidence
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	r.expireLocked(now)
	p, ok := r.byID[id]
	if !ok || p.Kind == contract.ProposalUnroute {
		return contract.Proposal{}, false, notFoundFailure("proposals.recheck")
	}
	if p.Status == contract.ProposalExpired {
		return copyProposal(p), false, ErrExpired
	}
	if p.Status == contract.ProposalAccepted {
		return copyProposal(p), false, ErrNotPending
	}
	p.Evidence = appendEvidence(p.Evidence, ev)
	if note != "" {
		p.Note = note
	}
	r.byID[id] = p
	return copyProposal(p), true, nil
}

// Expire moves pending route and unroute proposals past their 30-day window to
// expired. It deletes nothing; Clear is the explicit removal path.
func (r *Registry) Expire() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.expireLocked(r.now().UTC())
}

func (r *Registry) expireLocked(now time.Time) int {
	n := 0
	for id, p := range r.byID {
		if p.Status != contract.ProposalPending || p.ExpiresAt.IsZero() || now.Before(p.ExpiresAt) {
			continue
		}
		p.Status = contract.ProposalExpired
		p.DecidedAt = now
		r.byID[id] = p
		n++
	}
	return n
}

// Clear explicitly removes decided and expired proposals and unroute
// suggestions whose retention window has elapsed. Pending entries are never
// deleted implicitly.
func (r *Registry) Clear() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	r.expireLocked(now)
	n := 0
	for id, p := range r.byID {
		if p.Status == contract.ProposalPending {
			continue
		}
		if !p.ExpiresAt.IsZero() && now.Before(p.ExpiresAt) {
			continue
		}
		delete(r.byID, id)
		n++
	}
	return n
}

// EntryAddress renders destination:port for evidence. It keeps a hostname
// unresolved and never includes userinfo, a path or a query.
func EntryAddress(dst contract.Destination) string {
	switch dst.Kind {
	case contract.DestDomain:
		return net.JoinHostPort(dst.Host, strconv.Itoa(int(dst.Port)))
	case contract.DestIP:
		return net.JoinHostPort(dst.Addr.Unmap().String(), strconv.Itoa(int(dst.Port)))
	default:
		return ""
	}
}
