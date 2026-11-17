package conns

import (
	"context"
	"sort"
	"sync"
	"time"

	"routelatch/internal/contract"
)

// Registry is the process-wide owner of user connections. Create one with New
// and share it; it is safe for concurrent use.
type Registry struct {
	// fence is held for writing by a sweep and for reading by Register, so a
	// connection can never enter between the version bump and the close pass.
	// The version itself is read under mu, which keeps the hot path cheap.
	fence sync.RWMutex

	mu      sync.Mutex
	version contract.PolicyVersion
	conns   map[contract.ConnID]*handle

	// now is overridable in tests; production uses time.Now.
	now func() time.Time
}

var _ contract.Registry = (*Registry)(nil)

// New returns an empty registry at policy version zero.
func New() *Registry {
	return &Registry{
		conns: make(map[contract.ConnID]*handle),
		now:   time.Now,
	}
}

// Register implements contract.Registry.
//
// A spec is registered with the version that admitted it. If a sweep has
// already committed a newer version, the spec is rejected with Kind Canceled:
// the caller must route again under the current policy instead of relabeling
// an old decision.
func (r *Registry) Register(spec contract.ConnSpec) (contract.ConnHandle, error) {
	if spec.ID == "" {
		return nil, &contract.Failure{
			Kind:    contract.Internal,
			Path:    contract.PathLocal,
			Op:      "conns.register",
			Message: "connection id is empty",
		}
	}
	if spec.StartedAt.IsZero() {
		spec.StartedAt = r.now()
	}

	r.fence.RLock()
	defer r.fence.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	if spec.Decision.PolicyVersion < r.version {
		return nil, &contract.Failure{
			Kind:        contract.Canceled,
			Path:        contract.PathLocal,
			Op:          "conns.register",
			Destination: spec.Destination,
			Retryable:   true,
			Message:     "policy version is stale; route the destination again",
		}
	}
	if _, exists := r.conns[spec.ID]; exists {
		return nil, &contract.Failure{
			Kind:        contract.Internal,
			Path:        contract.PathLocal,
			Op:          "conns.register",
			Destination: spec.Destination,
			Message:     "connection id is already registered",
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &handle{
		reg:    r,
		spec:   spec,
		ctx:    ctx,
		cancel: cancel,
		state:  contract.ConnDialing,
		done:   make(chan struct{}),
	}
	r.conns[spec.ID] = h
	return h, nil
}

// CloseConflicting implements contract.Registry.
//
// keep is called while the fence is held; it must be pure and must not call
// back into the registry. A nil keep closes everything.
//
// ctx does not shorten the sweep. The contract requires that the call return
// only after every closed socket is closed and every pending dial canceled, so
// a caller deadline cannot be allowed to leave a half-closed connection behind.
// The local work is non-blocking and runs in parallel, which is what keeps the
// sweep inside contract.LocalCloseDeadline.
func (r *Registry) CloseConflicting(ctx context.Context, cause contract.CloseCause, next contract.PolicyVersion, keep func(contract.ConnInfo) bool) (contract.CloseReport, error) {
	_ = ctx
	started := r.now()

	r.fence.Lock()
	defer r.fence.Unlock()

	r.mu.Lock()
	from := r.version
	if next > r.version {
		r.version = next
	}
	to := r.version
	list := make([]*handle, 0, len(r.conns))
	for _, h := range r.conns {
		list = append(list, h)
	}
	r.mu.Unlock()

	report := contract.CloseReport{Cause: cause, FromVersion: from, ToVersion: to}

	closeList := make([]*handle, 0, len(list))
	for _, h := range list {
		if keep != nil && keep(h.info()) {
			report.Kept++
			continue
		}
		closeList = append(closeList, h)
	}

	var wg sync.WaitGroup
	var reportMu sync.Mutex
	for _, h := range closeList {
		wg.Add(1)
		go func(h *handle) {
			defer wg.Done()
			state, alreadyClosed, err := h.closeFrom()
			reportMu.Lock()
			switch {
			case alreadyClosed:
				// Its client closed it first; it is not evidence for this sweep.
			case state == contract.ConnDialing:
				report.PendingCancel++
			default:
				report.Closed++
			}
			if err != nil {
				report.Errors = append(report.Errors, err)
			}
			reportMu.Unlock()
		}(h)
	}
	wg.Wait()
	report.Duration = r.now().Sub(started)
	return report, nil
}

// CloseAll implements contract.Registry. It closes every registered connection
// at the current policy version; it does not change the version. It does not
// close the registry: the service stops accepting new clients first, so a later
// Register is a programming error on its side, not a state this method invents.
func (r *Registry) CloseAll(ctx context.Context, cause contract.CloseCause) (contract.CloseReport, error) {
	r.mu.Lock()
	current := r.version
	r.mu.Unlock()
	return r.CloseConflicting(ctx, cause, current, nil)
}

// Snapshot implements contract.Registry. It never blocks a dial: it copies the
// live handle set and each handle's own fields under their own short lock.
func (r *Registry) Snapshot() []contract.ConnInfo {
	r.mu.Lock()
	list := make([]*handle, 0, len(r.conns))
	for _, h := range r.conns {
		list = append(list, h)
	}
	r.mu.Unlock()

	out := make([]contract.ConnInfo, 0, len(list))
	for _, h := range list {
		out = append(out, h.info())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

// SetVersion seeds or advances the fence outside a sweep. The frozen
// contract.Registry has no way to tell the registry the version that is already
// committed at startup, so a service calls this with the persisted
// PolicyVersion before it accepts traffic. The value never moves backwards.
func (r *Registry) SetVersion(v contract.PolicyVersion) {
	r.fence.Lock()
	defer r.fence.Unlock()
	r.mu.Lock()
	if v > r.version {
		r.version = v
	}
	r.mu.Unlock()
}

// Version reports the highest policy version the registry has fenced.
func (r *Registry) Version() contract.PolicyVersion {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.version
}

// remove drops h from the live set. It only removes the exact handle, so a
// later connection that reused the same id is not affected.
func (r *Registry) remove(id contract.ConnID, h *handle) {
	r.mu.Lock()
	if cur, ok := r.conns[id]; ok && cur == h {
		delete(r.conns, id)
	}
	r.mu.Unlock()
}
