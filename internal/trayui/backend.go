package trayui

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"routelatch/internal/contract"
)

// This file is the rendering seam between the pure menu model and a concrete
// StatusNotifierItem backend. It contains no D-Bus type and no product tray
// code: R15-D2 provides the deedles adapter, while tests use a recording sink.

// BackendItemKind is the backend's node vocabulary. Separator is reserved for
// a future menu divider; the controller tree currently emits only items and
// submenus, and a backend must still accept a separator because the interface
// is meant to describe the host tree completely.
type BackendItemKind int

const (
	BackendItem BackendItemKind = iota
	BackendSubmenu
	BackendSeparator
)

// MenuItem is one resolved tree node handed to a backend. Key is stable for
// the same logical item across View rebuilds. Label is already sanitized.
// Icon and Tooltip are optional presentation hints; the controller leaves them
// empty today, and a backend may map them only when non-empty.
type MenuItem struct {
	Key     string
	Label   string
	Kind    BackendItemKind
	Enabled bool
	Visible bool
	Checked bool
	Check   CheckKind
	Icon    string
	Tooltip string

	Children []MenuItem
}

// Tray is the status-notifier item itself. Icon is a semantic token
// (TrayIcon); Tun is a separate semantic token for the coverage layer so a
// backend can show both without parsing either. Tooltip is sanitized and
// bounded.
type Tray struct {
	Title   string
	Tooltip string
	Icon    string
	Engine  EngineState
	Tun     string
}

// TunIcon returns the semantic coverage token for the tray icon:
// "on", "error", "off", "pending" or "" when the engine did not report TUN.
// Desired is the user's preference, so coverage that is on without a request,
// or requested but not active, is an error state, not a quiet off
// (docs/contracts.md §10.1–10.2).
func TunIcon(t TunView) string {
	if !t.Known {
		return ""
	}
	if t.LastError == contract.TunReasonConflict {
		return "error"
	}
	switch t.State {
	case contract.TunOn:
		if t.Desired {
			return "on"
		}
		return "error"
	case contract.TunStarting, contract.TunStopping:
		return "pending"
	case contract.TunOff:
		if t.Desired {
			return "error"
		}
		return "off"
	default:
		return "error"
	}
}

// TrayIcon returns the semantic status icon token for an engine state. The
// names are part of the binder seam, not file paths or theme icon names.
func TrayIcon(state EngineState) string {
	switch state {
	case EngineReady:
		return "ready"
	case EngineStarting:
		return "starting"
	case EngineStopped:
		return "stopped"
	case EngineUnavailable:
		return "unavailable"
	case EngineRestarting:
		return "restarting"
	default:
		return "loading"
	}
}

// OpKind is the closed operation set one Apply batch can carry.
type OpKind int

const (
	// OpAddNode inserts Item (including its whole subtree) at Index under
	// Parent. Item.Key equals Key. Index is 0-based within Parent's children.
	OpAddNode OpKind = iota // OpRemoveNode removes Key and its subtree from Parent.
	OpRemoveNode
	// OpMoveNode moves an existing Key to Index within its current Parent.
	OpMoveNode
	// OpUpdateNode applies Item's scalar presentation fields to an existing
	// Key. Item.Children is always nil; Kind changes are add/remove, not update.
	OpUpdateNode
	// OpSetTray replaces the tray title/tooltip/icon state.
	OpSetTray
)

func (k OpKind) String() string {
	switch k {
	case OpAddNode:
		return "add"
	case OpRemoveNode:
		return "remove"
	case OpMoveNode:
		return "move"
	case OpUpdateNode:
		return "update"
	case OpSetTray:
		return "tray"
	default:
		return "unknown"
	}
}

// Op is one deterministic mutation. A batch is applied in order; a backend
// that supports page rebuild batching can treat the whole slice as one host
// update instead of publishing a layout event per node.
type Op struct {
	Kind   OpKind
	Parent string // stable key of the parent submenu; root.Key for top level
	Index  int
	Key    string
	Item   MenuItem // add subtree or update scalar fields
	Tray   Tray     // set-tray payload
}

// Backend renders the binder's tree and applies its operations. The Runner
// serializes Build, Apply and Close, so an implementation does not need its own
// lock for those calls. A backend may call Runner.Click from any goroutine.
//
// Keys are unique in the final tree. Resolve an operation by Parent and then
// Key: an Add may carry a subtree whose descendant key still exists elsewhere
// in the old tree until a later Remove in the same batch. An implementation
// must not assume global key uniqueness while a batch is in flight.
//
// Build replaces any previously rendered tree (including after an Apply
// failure) and must leave the tray registered. Close removes the item and
// releases its resources. Apply receives one batch per View change; an empty
// batch is never sent. If Apply returns an error the Binder discards its
// rendered state and calls Build before the next Apply, so an implementation
// may leave a partial tree after a failure.
type Backend interface {
	Build(ctx context.Context, tray Tray, root MenuItem) error
	Apply(ctx context.Context, ops []Op) error
	Close(ctx context.Context) error
}

// Binder errors. They are safe to compare; none carries a label or target.
var (
	ErrUnknownNode  = errors.New("unknown menu node")
	ErrDisabledNode = errors.New("the menu node is disabled")
	ErrBinderClosed = errors.New("the tray binder is closed")
)

// ClickTarget is the stored action/target for one stable key, as last rendered.
type ClickTarget struct {
	Action  Action
	Target  Target
	Enabled bool
}

// tree is the binder's immutable copy of one applied menu. menu holds scalar
// fields only; children carry the same structure recursively.
type tree struct {
	menu     MenuItem
	children []*tree
}

// applyResult is the outcome of one Binder.Apply.
type applyResult struct {
	Built bool
	Ops   int
}

// Binder turns a View into backend operations. It is the diff/apply owner:
// Apply is not safe for concurrent use (the Runner calls it from its main
// loop), while Lookup and LastError may be called from a click goroutine.
type Binder struct {
	backend Backend

	applied     *tree
	appliedTray Tray
	needsBuild  bool
	closed      bool

	mu         sync.RWMutex
	clickables map[string]ClickTarget
	lastErr    error
}

// NewBinder validates the backend and returns an empty binder.
func NewBinder(backend Backend) (*Binder, error) {
	if backend == nil {
		return nil, errors.New("trayui: a Backend is required")
	}
	return &Binder{backend: backend}, nil
}

// Apply renders view if it differs from the last applied tree. The first call
// (or the first call after a backend error) performs a full Build; later calls
// send exactly one Apply batch containing only the changed nodes. A View that
// is unchanged causes no backend call at all. Click targets are published
// before the backend call because hosts may expose a new label while the
// apply is still running; a click that sees that label must resolve against
// the same tree. A failed apply clears them until a rebuild succeeds, so no
// stale key can accept a click.
func (b *Binder) Apply(ctx context.Context, view View) (applyResult, error) {
	if b.closed {
		return applyResult{}, ErrBinderClosed
	}
	desired, err := buildTree(view.Root)
	if err != nil {
		b.recordFailure(err)
		return applyResult{}, err
	}
	tray := trayFor(view)
	b.publishClickables(view.Root)

	if b.applied == nil || b.needsBuild || b.applied.menu.Key != desired.menu.Key ||
		b.applied.menu.Kind != desired.menu.Kind {
		if err := b.backend.Build(ctx, tray, menuOf(desired)); err != nil {
			b.recordFailure(fmt.Errorf("tray backend build: %w", err))
			return applyResult{}, err
		}
		b.commit(desired, tray, view.Root)
		return applyResult{Built: true}, nil
	}

	ops := diffTrees(b.applied, desired)
	if tray != b.appliedTray {
		ops = append([]Op{{Kind: OpSetTray, Tray: tray}}, ops...)
	}
	if len(ops) == 0 {
		b.commit(desired, tray, view.Root)
		return applyResult{}, nil
	}
	if err := b.backend.Apply(ctx, ops); err != nil {
		// The backend may have applied a prefix of the batch. A full Build is
		// the only safe retry, and clicks stay rejected until it succeeds.
		b.recordFailure(fmt.Errorf("tray backend apply: %w", err))
		return applyResult{}, err
	}
	b.commit(desired, tray, view.Root)
	return applyResult{Ops: len(ops)}, nil
}

// Lookup returns the click target rendered for key. The boolean reports
// whether the key exists at all; disabled/hidden items exist but are rejected
// by the caller.
func (b *Binder) Lookup(key string) (ClickTarget, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	t, ok := b.clickables[key]
	return t, ok
}

// LastError is the last rendering failure, or nil after a successful render.
func (b *Binder) LastError() error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.lastErr
}

// Close releases the backend item. Apply must not be called after Close.
func (b *Binder) Close(ctx context.Context) error {
	if b.closed {
		return nil
	}
	b.closed = true
	b.mu.Lock()
	b.clickables = nil
	b.mu.Unlock()
	if err := b.backend.Close(ctx); err != nil {
		b.mu.Lock()
		b.lastErr = fmt.Errorf("tray backend close: %w", err)
		b.mu.Unlock()
		return b.lastErr
	}
	return nil
}

// publishClickables makes the view's click targets visible to Lookup before
// the backend render starts. commit publishes them again on success; keeping
// the two writes identical means a click resolved during the render matches
// the label it was rendered next to.
func (b *Binder) publishClickables(root Node) {
	clickables := make(map[string]ClickTarget)
	collectClickables(root, clickables)
	b.mu.Lock()
	b.clickables = clickables
	b.mu.Unlock()
}

func (b *Binder) commit(t *tree, tray Tray, root Node) {
	b.applied = t
	b.appliedTray = tray
	b.needsBuild = false
	b.publishClickables(root)
	b.mu.Lock()
	b.lastErr = nil
	b.mu.Unlock()
}

func (b *Binder) recordFailure(err error) {
	b.needsBuild = true
	b.mu.Lock()
	b.clickables = nil
	b.lastErr = err
	b.mu.Unlock()
}

func collectClickables(n Node, out map[string]ClickTarget) {
	if n.Action != ActionNone {
		out[n.Key] = ClickTarget{Action: n.Action, Target: n.Target, Enabled: n.Enabled && n.Visible}
	}
	for _, child := range n.Children {
		collectClickables(child, out)
	}
}

func trayFor(view View) Tray {
	title := "RouteLatch"
	tooltip := title
	for _, child := range view.Root.Children {
		if child.Key == "status" {
			tooltip = SanitizeHeader(child.Label)
			break
		}
	}
	if suffix := tunTooltipSuffix(view.Tun); suffix != "" {
		tooltip = SanitizeHeader(tooltip + " · " + suffix)
	}
	return Tray{
		Title: title, Tooltip: tooltip, Icon: TrayIcon(view.Engine),
		Engine: view.Engine, Tun: TunIcon(view.Tun),
	}
}

// tunTooltipSuffix adds the coverage state to the safe status tooltip. It uses
// fixed state words only, never last_error/detail or any dynamic text.
func tunTooltipSuffix(t TunView) string {
	switch TunIcon(t) {
	case "on":
		return "TUN on"
	case "off":
		return "TUN off"
	case "error":
		return "TUN error"
	case "pending":
		return "TUN " + tunStateWord(t.State)
	default:
		return ""
	}
}

// buildTree copies the controller tree into the binder's immutable type and
// rejects a tree a backend could not render safely: an empty or duplicate key,
// a non-submenu root, or an unreasonable depth.
func buildTree(root Node) (*tree, error) {
	if root.Key == "" {
		return nil, errors.New("trayui: menu root has an empty key")
	}
	if root.Kind != NodeSubmenu {
		return nil, errors.New("trayui: menu root must be a submenu")
	}
	seen := make(map[string]bool)
	var convert func(Node, int) (*tree, error)
	convert = func(n Node, depth int) (*tree, error) {
		if depth > 64 {
			return nil, errors.New("trayui: menu tree is too deep")
		}
		if n.Key == "" {
			return nil, errors.New("trayui: menu node has an empty key")
		}
		if seen[n.Key] {
			return nil, fmt.Errorf("trayui: duplicate menu key %q", n.Key)
		}
		seen[n.Key] = true
		t := &tree{menu: menuScalars(n)}
		for _, child := range n.Children {
			converted, err := convert(child, depth+1)
			if err != nil {
				return nil, err
			}
			t.children = append(t.children, converted)
		}
		return t, nil
	}
	return convert(root, 0)
}

func menuScalars(n Node) MenuItem {
	kind := BackendItem
	if n.Kind == NodeSubmenu {
		kind = BackendSubmenu
	}
	return MenuItem{
		Key: n.Key, Label: n.Label, Kind: kind,
		Enabled: n.Enabled, Visible: n.Visible, Checked: n.Checked, Check: n.Check,
		Icon: "", Tooltip: "",
	}
}

func menuOf(t *tree) MenuItem {
	item := t.menu
	if len(t.children) > 0 {
		item.Children = make([]MenuItem, 0, len(t.children))
		for _, child := range t.children {
			item.Children = append(item.Children, menuOf(child))
		}
	}
	return item
}

// diffTrees returns the ordered operations that turn old into desired. It is
// deterministic: removals in old order, additions in new order, moves in new
// order, then property updates and descendant diffs in new order.
func diffTrees(old, desired *tree) []Op {
	// The root is the menu container, not a visible item: its key must match
	// (otherwise Apply rebuilds) and its label is represented by Tray.Title.
	return diffChildren(old, desired)
}

func diffChildren(old, desired *tree) []Op {
	var ops []Op
	oldByKey := make(map[string]*tree, len(old.children))
	for _, child := range old.children {
		oldByKey[child.menu.Key] = child
	}
	newByKey := make(map[string]*tree, len(desired.children))
	for _, child := range desired.children {
		newByKey[child.menu.Key] = child
	}

	// A kind change is a replacement, not an in-place update.
	replaced := func(key string) bool {
		o := oldByKey[key]
		n := newByKey[key]
		return o != nil && n != nil && o.menu.Kind != n.menu.Kind
	}

	current := make([]string, 0, len(old.children))
	for _, child := range old.children {
		key := child.menu.Key
		if _, keep := newByKey[key]; !keep || replaced(key) {
			ops = append(ops, Op{Kind: OpRemoveNode, Parent: desired.menu.Key, Key: key})
			continue
		}
		current = append(current, key)
	}

	for i, child := range desired.children {
		key := child.menu.Key
		if oldByKey[key] == nil || replaced(key) {
			ops = append(ops, Op{Kind: OpAddNode, Parent: desired.menu.Key, Index: i, Key: key, Item: menuOf(child)})
			current = insertKey(current, i, key)
		}
	}

	for i, child := range desired.children {
		key := child.menu.Key
		if current[i] != key {
			ops = append(ops, Op{Kind: OpMoveNode, Parent: desired.menu.Key, Index: i, Key: key})
			current = moveKey(current, key, i)
		}
	}

	for _, child := range desired.children {
		key := child.menu.Key
		o := oldByKey[key]
		if o == nil || replaced(key) {
			continue // the Add carried the whole subtree
		}
		if !sameScalars(o.menu, child.menu) {
			ops = append(ops, Op{Kind: OpUpdateNode, Parent: desired.menu.Key, Key: key, Item: child.menu})
		}
		if child.menu.Kind == BackendSubmenu {
			ops = append(ops, diffChildren(o, child)...)
		}
	}
	return ops
}

func sameScalars(a, b MenuItem) bool {
	return a.Key == b.Key && a.Label == b.Label && a.Kind == b.Kind &&
		a.Enabled == b.Enabled && a.Visible == b.Visible &&
		a.Checked == b.Checked && a.Check == b.Check &&
		a.Icon == b.Icon && a.Tooltip == b.Tooltip
}

func insertKey(keys []string, index int, key string) []string {
	if index < 0 {
		index = 0
	}
	if index > len(keys) {
		index = len(keys)
	}
	keys = append(keys, "")
	copy(keys[index+1:], keys[index:])
	keys[index] = key
	return keys
}

func moveKey(keys []string, key string, index int) []string {
	from := -1
	for i, k := range keys {
		if k == key {
			from = i
			break
		}
	}
	if from < 0 {
		return keys
	}
	keys = append(keys[:from], keys[from+1:]...)
	return insertKey(keys, index, key)
}
