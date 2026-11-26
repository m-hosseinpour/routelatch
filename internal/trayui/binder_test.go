package trayui

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeBackend is the recording sink: it captures every Build/Apply/Close and
// maintains the tree a real host would show, so tests can assert both the
// operation sequence and the resulting menu.
type fakeBackend struct {
	mu       sync.Mutex
	builds   int
	closes   int
	batches  [][]Op
	tray     Tray
	root     MenuItem
	buildErr error
	applyErr error
	// applyHook runs inside Apply, after the batch is recorded. It lets a test
	// observe what Lookup returns while the backend render is still in flight.
	applyHook func()
}

func newFakeBackend() *fakeBackend { return &fakeBackend{} }

func (f *fakeBackend) Build(_ context.Context, tray Tray, root MenuItem) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds++
	if f.buildErr != nil {
		return f.buildErr
	}
	f.tray = tray
	f.root = cloneMenuItem(root)
	return nil
}

func (f *fakeBackend) Apply(_ context.Context, ops []Op) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, append([]Op(nil), ops...))
	if f.applyHook != nil {
		f.applyHook()
	}
	if f.applyErr != nil {
		return f.applyErr
	}
	for _, op := range ops {
		if err := applyFakeOp(&f.root, &f.tray, op); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeBackend) Close(context.Context) error {
	f.mu.Lock()
	f.closes++
	f.mu.Unlock()
	return nil
}

func (f *fakeBackend) setBuildErr(err error) { f.mu.Lock(); f.buildErr = err; f.mu.Unlock() }
func (f *fakeBackend) setApplyErr(err error) { f.mu.Lock(); f.applyErr = err; f.mu.Unlock() }

func (f *fakeBackend) counts() (builds, applies, closes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.builds, len(f.batches), f.closes
}

func (f *fakeBackend) batch(n int) []Op {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n >= len(f.batches) {
		return nil
	}
	return append([]Op(nil), f.batches[n]...)
}

func (f *fakeBackend) batchesCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batches)
}

func (f *fakeBackend) current() MenuItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneMenuItem(f.root)
}

func (f *fakeBackend) trayState() Tray {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tray
}

func (f *fakeBackend) find(key string) (MenuItem, bool) {
	root := f.current()
	n := findMenuItem(&root, key)
	if n == nil {
		return MenuItem{}, false
	}
	return *n, true
}

func (f *fakeBackend) label(key string) (string, bool) {
	n, ok := f.find(key)
	return n.Label, ok
}

// keysOf returns the direct children keys of a submenu, in rendered order.
func (f *fakeBackend) keysOf(key string) []string {
	root := f.current()
	n := findMenuItem(&root, key)
	if n == nil {
		return nil
	}
	out := make([]string, 0, len(n.Children))
	for _, child := range n.Children {
		out = append(out, child.Key)
	}
	return out
}

func cloneMenuItem(n MenuItem) MenuItem {
	out := n
	if n.Children != nil {
		out.Children = make([]MenuItem, 0, len(n.Children))
		for _, child := range n.Children {
			out.Children = append(out.Children, cloneMenuItem(child))
		}
	}
	return out
}

func findMenuItem(n *MenuItem, key string) *MenuItem {
	if n.Key == key {
		return n
	}
	for i := range n.Children {
		if found := findMenuItem(&n.Children[i], key); found != nil {
			return found
		}
	}
	return nil
}

func applyFakeOp(root *MenuItem, tray *Tray, op Op) error {
	if op.Kind == OpSetTray {
		*tray = op.Tray
		return nil
	}
	if op.Kind == OpUpdateNode {
		node := findMenuItem(root, op.Key)
		if node == nil {
			return errors.New("fake backend: update of missing node")
		}
		children := node.Children
		*node = op.Item
		node.Children = children
		return nil
	}
	parent := findMenuItem(root, op.Parent)
	if parent == nil {
		return errors.New("fake backend: missing parent")
	}
	switch op.Kind {
	case OpAddNode:
		if op.Index < 0 || op.Index > len(parent.Children) {
			return errors.New("fake backend: add index out of range")
		}
		parent.Children = append(parent.Children, MenuItem{})
		copy(parent.Children[op.Index+1:], parent.Children[op.Index:])
		parent.Children[op.Index] = cloneMenuItem(op.Item)
	case OpRemoveNode:
		index := childIndex(parent, op.Key)
		if index < 0 {
			return errors.New("fake backend: remove of missing node")
		}
		parent.Children = append(parent.Children[:index], parent.Children[index+1:]...)
	case OpMoveNode:
		index := childIndex(parent, op.Key)
		if index < 0 {
			return errors.New("fake backend: move of missing node")
		}
		item := parent.Children[index]
		parent.Children = append(parent.Children[:index], parent.Children[index+1:]...)
		if op.Index < 0 || op.Index > len(parent.Children) {
			return errors.New("fake backend: move index out of range")
		}
		parent.Children = append(parent.Children, MenuItem{})
		copy(parent.Children[op.Index+1:], parent.Children[op.Index:])
		parent.Children[op.Index] = item
	default:
		return errors.New("fake backend: unknown op")
	}
	return nil
}

func childIndex(parent *MenuItem, key string) int {
	for i, child := range parent.Children {
		if child.Key == key {
			return i
		}
	}
	return -1
}

func menuView(engine EngineState, children ...Node) View {
	return View{Root: submenu("root", "RouteLatch", children...), Engine: engine}
}

func TestBinderInitialApplyAndIdempotentReapply(t *testing.T) {
	backend := newFakeBackend()
	binder, err := NewBinder(backend)
	if err != nil {
		t.Fatal(err)
	}
	view := menuView(EngineReady, item("a", "A"), item("b", "B"))
	if _, err := binder.Apply(context.Background(), view); err != nil {
		t.Fatal(err)
	}
	if builds, applies, _ := backend.counts(); builds != 1 || applies != 0 {
		t.Fatalf("initial apply = %d builds, %d applies", builds, applies)
	}
	if got := backend.trayState(); got.Icon != "ready" || got.Title != "RouteLatch" {
		t.Fatalf("tray = %+v", got)
	}

	// The same view must not touch the backend again.
	for i := 0; i < 3; i++ {
		if _, err := binder.Apply(context.Background(), view); err != nil {
			t.Fatal(err)
		}
	}
	if builds, applies, _ := backend.counts(); builds != 1 || applies != 0 {
		t.Fatalf("re-apply changed the backend: %d builds, %d applies", builds, applies)
	}
}

func TestBinderAddRemoveRelabelReorder(t *testing.T) {
	backend := newFakeBackend()
	binder, err := NewBinder(backend)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	view := menuView(EngineReady, item("a", "A"), item("b", "B"))
	if _, err := binder.Apply(ctx, view); err != nil {
		t.Fatal(err)
	}

	// Add in the middle: one Add at the target index.
	view = menuView(EngineReady, item("a", "A"), item("c", "C"), item("b", "B"))
	if _, err := binder.Apply(ctx, view); err != nil {
		t.Fatal(err)
	}
	if n := backend.batchesCount(); n != 1 {
		t.Fatalf("applies = %d", n)
	}
	if ops := backend.batch(0); len(ops) != 1 || ops[0].Kind != OpAddNode || ops[0].Key != "c" || ops[0].Index != 1 {
		t.Fatalf("add ops = %+v", ops)
	}
	if got := backend.keysOf("root"); !equalKeys(got, []string{"a", "c", "b"}) {
		t.Fatalf("root keys = %v", got)
	}

	// Relabel: one property update only.
	view = menuView(EngineReady, item("a", "A"), item("c", "C renumbered"), item("b", "B"))
	if _, err := binder.Apply(ctx, view); err != nil {
		t.Fatal(err)
	}
	if ops := backend.batch(1); len(ops) != 1 || ops[0].Kind != OpUpdateNode || ops[0].Key != "c" || ops[0].Item.Label != "C renumbered" {
		t.Fatalf("relabel ops = %+v", ops)
	}

	// Reorder: one move brings c to the front.
	view = menuView(EngineReady, item("c", "C renumbered"), item("a", "A"), item("b", "B"))
	if _, err := binder.Apply(ctx, view); err != nil {
		t.Fatal(err)
	}
	if ops := backend.batch(2); len(ops) != 1 || ops[0].Kind != OpMoveNode || ops[0].Key != "c" || ops[0].Index != 0 {
		t.Fatalf("move ops = %+v", ops)
	}
	if got := backend.keysOf("root"); !equalKeys(got, []string{"c", "a", "b"}) {
		t.Fatalf("root keys after move = %v", got)
	}

	// Remove: the subtree disappears and the rest keeps its order.
	view = menuView(EngineReady, item("c", "C renumbered"), item("b", "B"))
	if _, err := binder.Apply(ctx, view); err != nil {
		t.Fatal(err)
	}
	if ops := backend.batch(3); len(ops) != 1 || ops[0].Kind != OpRemoveNode || ops[0].Key != "a" {
		t.Fatalf("remove ops = %+v", ops)
	}
	if got := backend.keysOf("root"); !equalKeys(got, []string{"c", "b"}) {
		t.Fatalf("root keys after remove = %v", got)
	}

	// Every successful apply is idempotent.
	before := backend.batchesCount()
	if _, err := binder.Apply(ctx, view); err != nil {
		t.Fatal(err)
	}
	if got := backend.batchesCount(); got != before {
		t.Fatalf("idempotent re-apply added %d batches", got-before)
	}
}

func TestBinderPageRebuildIsOneBatch(t *testing.T) {
	backend := newFakeBackend()
	binder, err := NewBinder(backend)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	oldPage := make([]Node, 0, 20)
	newPage := make([]Node, 0, 20)
	for i := 0; i < 20; i++ {
		oldPage = append(oldPage, item(fakeKey("old", i), fakeKey("Old", i)))
		newPage = append(newPage, item(fakeKey("new", i), fakeKey("New", i)))
	}
	view := menuView(EngineReady, submenu("group", "Group", oldPage...))
	if _, err := binder.Apply(ctx, view); err != nil {
		t.Fatal(err)
	}

	view = menuView(EngineReady, submenu("group", "Group", newPage...))
	if _, err := binder.Apply(ctx, view); err != nil {
		t.Fatal(err)
	}
	if n := backend.batchesCount(); n != 1 {
		t.Fatalf("page rebuild used %d Apply calls", n)
	}
	ops := backend.batch(0)
	if len(ops) != 40 {
		t.Fatalf("page rebuild ops = %d", len(ops))
	}
	for i := 0; i < 20; i++ {
		if ops[i].Kind != OpRemoveNode {
			t.Fatalf("op %d = %+v, want remove", i, ops[i])
		}
	}
	for i := 20; i < 40; i++ {
		if ops[i].Kind != OpAddNode {
			t.Fatalf("op %d = %+v, want add", i, ops[i])
		}
	}
	if got := backend.keysOf("group"); len(got) != 20 || got[0] != "new-aa" || got[19] != "new-bj" {
		t.Fatalf("group keys = %v", got)
	}
}

func TestBinderKindChangeAndTrayUpdate(t *testing.T) {
	backend := newFakeBackend()
	binder, err := NewBinder(backend)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	view := menuView(EngineReady, item("x", "X"))
	if _, err := binder.Apply(ctx, view); err != nil {
		t.Fatal(err)
	}
	// Item -> submenu is a replacement, not an in-place update.
	view = menuView(EngineReady, submenu("x", "X", item("x.child", "Child")))
	if _, err := binder.Apply(ctx, view); err != nil {
		t.Fatal(err)
	}
	ops := backend.batch(0)
	if len(ops) != 2 || ops[0].Kind != OpRemoveNode || ops[1].Kind != OpAddNode || ops[1].Item.Kind != BackendSubmenu {
		t.Fatalf("kind change ops = %+v", ops)
	}

	// A pure engine change produces only the tray update.
	view = menuView(EngineStopped, submenu("x", "X", item("x.child", "Child")))
	if _, err := binder.Apply(ctx, view); err != nil {
		t.Fatal(err)
	}
	ops = backend.batch(1)
	if len(ops) != 1 || ops[0].Kind != OpSetTray || ops[0].Tray.Icon != "stopped" {
		t.Fatalf("tray ops = %+v", ops)
	}
}

func TestBinderClickLookup(t *testing.T) {
	backend := newFakeBackend()
	binder, err := NewBinder(backend)
	if err != nil {
		t.Fatal(err)
	}
	enabled := item("enabled", "Enabled")
	enabled.Action = ActionModeTargeted
	disabled := disabledItem("disabled", "Disabled")
	disabled.Action = ActionModeAllProxy
	hidden := item("hidden", "Hidden")
	hidden.Action = ActionModeAllDirect
	hidden.Visible = false
	view := menuView(EngineReady, enabled, disabled, hidden)
	if _, err := binder.Apply(context.Background(), view); err != nil {
		t.Fatal(err)
	}

	target, ok := binder.Lookup("enabled")
	if !ok || !target.Enabled || target.Action != ActionModeTargeted {
		t.Fatalf("enabled lookup = %+v, %v", target, ok)
	}
	if target, ok := binder.Lookup("disabled"); !ok || target.Enabled {
		t.Fatalf("disabled lookup = %+v, %v", target, ok)
	}
	if target, ok := binder.Lookup("hidden"); !ok || target.Enabled {
		t.Fatalf("hidden lookup = %+v, %v", target, ok)
	}
	if _, ok := binder.Lookup("missing"); ok {
		t.Fatal("unknown key resolved")
	}
}

// A host can expose a changed label while the backend apply is still running.
// Click resolution must already see the tree that produced that label, not the
// previous one.
func TestBinderPublishesClickTargetsBeforeBackendApply(t *testing.T) {
	backend := newFakeBackend()
	binder, err := NewBinder(backend)
	if err != nil {
		t.Fatal(err)
	}
	enabled := item("mode", "Targeted")
	enabled.Action = ActionModeTargeted
	if _, err := binder.Apply(context.Background(), menuView(EngineReady, enabled)); err != nil {
		t.Fatal(err)
	}

	disabled := disabledItem("mode", "Targeted")
	disabled.Action = ActionModeTargeted
	checked := 0
	backend.applyHook = func() {
		checked++
		target, ok := binder.Lookup("mode")
		if !ok {
			t.Error("the updated key is not published during apply")
			return
		}
		if target.Enabled {
			t.Error("Lookup still returns the previous enabled target during apply")
		}
	}
	if _, err := binder.Apply(context.Background(), menuView(EngineReady, disabled)); err != nil {
		t.Fatal(err)
	}
	if checked != 1 {
		t.Fatalf("apply hook ran %d times, want 1", checked)
	}
}

func TestBinderValidationAndBackendErrors(t *testing.T) {
	backend := newFakeBackend()
	binder, err := NewBinder(backend)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	duplicate := menuView(EngineReady, item("same", "One"), item("same", "Two"))
	if _, err := binder.Apply(ctx, duplicate); err == nil {
		t.Fatal("duplicate key was accepted")
	}
	if builds, _, _ := backend.counts(); builds != 0 {
		t.Fatal("invalid tree reached the backend")
	}
	if binder.LastError() == nil {
		t.Fatal("validation error was not recorded")
	}

	// A Build failure is reported and retried on the next Apply.
	backend.setBuildErr(errors.New("no watcher"))
	action := item("a", "A")
	action.Action = ActionModeTargeted
	view := menuView(EngineReady, action)
	if _, err := binder.Apply(ctx, view); err == nil {
		t.Fatal("build failure was silent")
	}
	if binder.LastError() == nil {
		t.Fatal("build error was not recorded")
	}
	backend.setBuildErr(nil)
	if _, err := binder.Apply(ctx, view); err != nil {
		t.Fatal(err)
	}
	if binder.LastError() != nil {
		t.Fatalf("error survived a successful build: %v", binder.LastError())
	}

	// An Apply failure clears clickables and forces a rebuild next time.
	relabeled := item("a", "A relabeled")
	relabeled.Action = ActionModeTargeted
	view = menuView(EngineReady, relabeled)
	backend.setApplyErr(errors.New("host gone"))
	if _, err := binder.Apply(ctx, view); err == nil {
		t.Fatal("apply failure was silent")
	}
	if _, ok := binder.Lookup("a"); ok {
		t.Fatal("clickable survived a failed apply")
	}
	backend.setApplyErr(nil)
	buildsBefore, _, _ := backend.counts()
	if _, err := binder.Apply(ctx, view); err != nil {
		t.Fatal(err)
	}
	if builds, _, _ := backend.counts(); builds != buildsBefore+1 {
		t.Fatal("apply failure did not force a rebuild")
	}
	if n, ok := backend.find("a"); !ok || n.Label != "A relabeled" {
		t.Fatalf("rebuilt tree = %+v, %v", n, ok)
	}
	if target, ok := binder.Lookup("a"); !ok || !target.Enabled {
		t.Fatal("clickable missing after recovery")
	}
}

func fakeKey(prefix string, i int) string {
	return prefix + "-" + string(rune('a'+i/10)) + string(rune('a'+i%10))
}

func equalKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
