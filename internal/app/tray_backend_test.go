package app

import (
	"context"
	"errors"
	"testing"

	"routelatch/internal/trayui"
)

// fakeMenuTree records menuOps without a session bus. It is enough to verify
// the stable-key/order/property rules the deedles adapter must implement.
type fakeMenuNode struct {
	props    menuItemProps
	children []*fakeMenuNode
	parent   *fakeMenuNode
}

type fakeMenuOps struct {
	root    *fakeMenuNode
	tray    trayui.Tray
	closed  bool
	adds    int
	removes int
	moves   int
	sets    int
}

func newFakeMenuOps(trayui.Tray) (menuOps, error) {
	return &fakeMenuOps{root: &fakeMenuNode{}}, nil
}

func (f *fakeMenuOps) Add(parent menuNode, p menuItemProps) (menuNode, error) {
	n := &fakeMenuNode{props: p}
	if parent != nil {
		parentNode, ok := parent.(*fakeMenuNode)
		if !ok {
			return nil, errors.New("bad parent")
		}
		n.parent = parentNode
		parentNode.children = append(parentNode.children, n)
	} else {
		f.root.children = append(f.root.children, n)
	}
	f.adds++
	return n, nil
}

func (f *fakeMenuOps) Remove(node menuNode) error {
	n, ok := node.(*fakeMenuNode)
	if !ok {
		return errors.New("bad node")
	}
	if n.parent == nil {
		f.root.children = removeFakeChild(f.root.children, n)
	} else {
		n.parent.children = removeFakeChild(n.parent.children, n)
	}
	f.removes++
	return nil
}

func (f *fakeMenuOps) Move(parent, node, before menuNode) error {
	n, ok := node.(*fakeMenuNode)
	if !ok {
		return errors.New("bad node")
	}
	if n.parent == nil {
		f.root.children = removeFakeChild(f.root.children, n)
	} else {
		n.parent.children = removeFakeChild(n.parent.children, n)
	}
	var siblings *[]*fakeMenuNode
	if parent == nil {
		siblings = &f.root.children
		n.parent = nil
	} else {
		p, ok := parent.(*fakeMenuNode)
		if !ok {
			return errors.New("bad parent")
		}
		siblings = &p.children
		n.parent = p
	}
	*siblings = insertFakeChild(*siblings, before, n)
	f.moves++
	return nil
}

func (f *fakeMenuOps) Set(node menuNode, p menuItemProps) error {
	n, ok := node.(*fakeMenuNode)
	if !ok {
		return errors.New("bad node")
	}
	onClick := n.props.OnClick
	n.props = p
	if n.props.OnClick == nil {
		n.props.OnClick = onClick
	}
	f.sets++
	return nil
}

func (f *fakeMenuOps) SetTray(t trayui.Tray) error { f.tray = t; return nil }
func (f *fakeMenuOps) Close() error {
	f.closed = true
	return nil
}

func removeFakeChild(list []*fakeMenuNode, node *fakeMenuNode) []*fakeMenuNode {
	for i, n := range list {
		if n == node {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

func insertFakeChild(list []*fakeMenuNode, before menuNode, node *fakeMenuNode) []*fakeMenuNode {
	if before != nil {
		if target, ok := before.(*fakeMenuNode); ok {
			for i, n := range list {
				if n == target {
					list = append(list, nil)
					copy(list[i+1:], list[i:])
					list[i] = node
					return list
				}
			}
		}
	}
	return append(list, node)
}

func nodeByLabel(t *testing.T, root *fakeMenuNode, label string) *fakeMenuNode {
	t.Helper()
	var walk func(*fakeMenuNode) *fakeMenuNode
	walk = func(n *fakeMenuNode) *fakeMenuNode {
		if n.props.Label == label {
			return n
		}
		for _, child := range n.children {
			if found := walk(child); found != nil {
				return found
			}
		}
		return nil
	}
	for _, child := range root.children {
		if found := walk(child); found != nil {
			return found
		}
	}
	t.Fatalf("menu label %q not found", label)
	return nil
}

func buildTestRoot(items ...trayui.MenuItem) trayui.MenuItem {
	return trayui.MenuItem{Key: "root", Kind: trayui.BackendSubmenu, Enabled: true, Visible: true, Children: items}
}

func TestTrayBackendBuildRendersDeedlesTree(t *testing.T) {
	var clicked []string
	backend := newTrayBackend(func(key string) error {
		clicked = append(clicked, key)
		return nil
	})
	fake := &fakeMenuOps{root: &fakeMenuNode{}}
	backend.newOps = func(trayui.Tray) (menuOps, error) { return fake, nil }

	root := buildTestRoot(
		trayui.MenuItem{Key: "status", Kind: trayui.BackendItem, Label: "Targeted · 3 usable", Enabled: false, Visible: true},
		trayui.MenuItem{Key: "mode", Kind: trayui.BackendSubmenu, Label: "Mode", Enabled: true, Visible: true, Children: []trayui.MenuItem{
			{Key: "mode.targeted", Kind: trayui.BackendItem, Label: "Targeted", Enabled: true, Visible: true, Checked: true, Check: trayui.CheckRadio},
			{Key: "mode.all-proxy", Kind: trayui.BackendItem, Label: "All proxy", Enabled: true, Visible: true, Check: trayui.CheckRadio},
			{Key: "mode.stop", Kind: trayui.BackendItem, Label: "Stop listening…", Enabled: true, Visible: true},
		}},
		trayui.MenuItem{Key: "advanced", Kind: trayui.BackendSubmenu, Label: "Advanced", Enabled: true, Visible: true, Children: []trayui.MenuItem{
			{Key: "advanced.restart", Kind: trayui.BackendItem, Label: "Restart engine…", Enabled: true, Visible: true},
		}},
		trayui.MenuItem{Key: "sep", Kind: trayui.BackendSeparator, Visible: true},
	)
	state := trayui.Tray{Title: "RouteLatch", Tooltip: "Targeted · 3 usable", Icon: "ready", Engine: trayui.EngineReady}
	if err := backend.Build(context.Background(), state, root); err != nil {
		t.Fatal(err)
	}

	if fake.tray != state {
		t.Fatalf("tray = %+v, want %+v", fake.tray, state)
	}
	if len(fake.root.children) != 4 {
		t.Fatalf("root children = %d, want 4", len(fake.root.children))
	}
	if got := fake.root.children[0].props.Label; got != "Targeted · 3 usable" {
		t.Fatalf("status label = %q", got)
	}
	if fake.root.children[0].props.Enabled {
		t.Fatal("disabled status item was enabled")
	}
	if fake.root.children[0].props.OnClick == nil {
		t.Fatal("status item has a handler; Binder.Lookup will reject it")
	}
	mode := fake.root.children[1]
	if mode.props.Kind != trayui.BackendSubmenu || mode.props.OnClick != nil {
		t.Fatalf("mode submenu props = %+v", mode.props)
	}
	if len(mode.children) != 3 {
		t.Fatalf("mode children = %d", len(mode.children))
	}
	if !mode.children[0].props.Checked || mode.children[0].props.Check != trayui.CheckRadio ||
		mode.children[1].props.Checked || mode.children[1].props.Check != trayui.CheckRadio {
		t.Fatalf("radio sync = %+v / %+v", mode.children[0].props, mode.children[1].props)
	}
	if fake.root.children[3].props.Kind != trayui.BackendSeparator || fake.root.children[3].props.Label != "" {
		t.Fatalf("separator props = %+v", fake.root.children[3].props)
	}
	if err := mode.children[0].props.OnClick(); err != nil {
		t.Fatal(err)
	}
	if len(clicked) != 1 || clicked[0] != "mode.targeted" {
		t.Fatalf("clicks = %v, want [mode.targeted]", clicked)
	}
}

func TestTrayBackendApplyAddsMovesUpdatesRemoves(t *testing.T) {
	backend := newTrayBackend(func(string) error { return nil })
	fake := &fakeMenuOps{root: &fakeMenuNode{}}
	backend.newOps = func(trayui.Tray) (menuOps, error) { return fake, nil }
	root := buildTestRoot(
		trayui.MenuItem{Key: "a", Kind: trayui.BackendItem, Label: "A", Enabled: true, Visible: true},
		trayui.MenuItem{Key: "b", Kind: trayui.BackendItem, Label: "B", Enabled: true, Visible: true},
		trayui.MenuItem{Key: "c", Kind: trayui.BackendItem, Label: "C", Enabled: true, Visible: true},
	)
	if err := backend.Build(context.Background(), trayui.Tray{Title: "RouteLatch", Icon: "ready"}, root); err != nil {
		t.Fatal(err)
	}
	ops := []trayui.Op{
		{Kind: trayui.OpSetTray, Tray: trayui.Tray{Title: "RouteLatch", Tooltip: "Stopped", Icon: "stopped"}},
		{Kind: trayui.OpUpdateNode, Parent: "root", Key: "b", Item: trayui.MenuItem{Key: "b", Kind: trayui.BackendItem, Label: "B changed", Enabled: false, Visible: true, Checked: true, Check: trayui.CheckCheckbox}},
		{Kind: trayui.OpMoveNode, Parent: "root", Key: "c", Index: 0},
		{Kind: trayui.OpAddNode, Parent: "root", Index: 1, Key: "d", Item: trayui.MenuItem{Key: "d", Kind: trayui.BackendItem, Label: "D", Enabled: true, Visible: true}},
		{Kind: trayui.OpRemoveNode, Parent: "root", Key: "a"},
	}
	if err := backend.Apply(context.Background(), ops); err != nil {
		t.Fatal(err)
	}
	labels := make([]string, 0, len(fake.root.children))
	for _, child := range fake.root.children {
		labels = append(labels, child.props.Label)
	}
	want := []string{"C", "D", "B changed"}
	if len(labels) != len(want) {
		t.Fatalf("order = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("order = %v, want %v", labels, want)
		}
	}
	if fake.root.children[2].props.Enabled || !fake.root.children[2].props.Checked || fake.root.children[2].props.Check != trayui.CheckCheckbox {
		t.Fatalf("updated props = %+v", fake.root.children[2].props)
	}
	if fake.tray.Icon != "stopped" || fake.tray.Tooltip != "Stopped" {
		t.Fatalf("tray = %+v", fake.tray)
	}
	if err := backend.Apply(context.Background(), []trayui.Op{{Kind: trayui.OpRemoveNode, Parent: "root", Key: "a"}}); err == nil {
		t.Fatal("removing an already removed node succeeded")
	}
	if err := backend.Apply(context.Background(), []trayui.Op{{Kind: trayui.OpUpdateNode, Parent: "root", Key: "missing"}}); err == nil {
		t.Fatal("updating an unknown node succeeded")
	}
}

// A batch may add a subtree whose key still exists under another parent and
// remove the old one later in the same batch (R15-D1 note). Resolution is by
// parent then key, so the temporary duplicate must not misroute the removal.
func TestTrayBackendBatchResolvesParentBeforeKey(t *testing.T) {
	backend := newTrayBackend(func(string) error { return nil })
	fake := &fakeMenuOps{root: &fakeMenuNode{}}
	backend.newOps = func(trayui.Tray) (menuOps, error) { return fake, nil }
	root := buildTestRoot(
		trayui.MenuItem{Key: "p1", Kind: trayui.BackendSubmenu, Label: "P1", Enabled: true, Visible: true, Children: []trayui.MenuItem{
			{Key: "dup", Kind: trayui.BackendItem, Label: "old", Enabled: true, Visible: true},
		}},
		trayui.MenuItem{Key: "p2", Kind: trayui.BackendSubmenu, Label: "P2", Enabled: true, Visible: true},
	)
	if err := backend.Build(context.Background(), trayui.Tray{Title: "RouteLatch"}, root); err != nil {
		t.Fatal(err)
	}
	ops := []trayui.Op{
		{Kind: trayui.OpAddNode, Parent: "p2", Index: 0, Key: "dup", Item: trayui.MenuItem{
			Key: "dup", Kind: trayui.BackendSubmenu, Label: "new", Enabled: true, Visible: true,
			Children: []trayui.MenuItem{{Key: "dup.child", Kind: trayui.BackendItem, Label: "child", Enabled: true, Visible: true}},
		}},
		{Kind: trayui.OpRemoveNode, Parent: "p1", Key: "dup"},
	}
	if err := backend.Apply(context.Background(), ops); err != nil {
		t.Fatalf("batch apply: %v", err)
	}
	if len(fake.root.children[0].children) != 0 {
		t.Fatalf("p1 children = %d, want 0", len(fake.root.children[0].children))
	}
	p2 := fake.root.children[1]
	if len(p2.children) != 1 || p2.children[0].props.Label != "new" || len(p2.children[0].children) != 1 {
		t.Fatalf("p2 subtree = %+v", p2.children)
	}
}

func TestTrayBackendBuildReplacesAndCloseIsIdempotent(t *testing.T) {
	backend := newTrayBackend(func(string) error { return nil })
	first := &fakeMenuOps{root: &fakeMenuNode{}}
	second := &fakeMenuOps{root: &fakeMenuNode{}}
	ops := []menuOps{first, second}
	backend.newOps = func(trayui.Tray) (menuOps, error) {
		next := ops[0]
		ops = ops[1:]
		return next, nil
	}
	root := buildTestRoot(trayui.MenuItem{Key: "a", Kind: trayui.BackendItem, Label: "A", Enabled: true, Visible: true})
	if err := backend.Build(context.Background(), trayui.Tray{Title: "RouteLatch"}, root); err != nil {
		t.Fatal(err)
	}
	if err := backend.Build(context.Background(), trayui.Tray{Title: "RouteLatch", Icon: "restarting"}, root); err != nil {
		t.Fatal(err)
	}
	if !first.closed {
		t.Fatal("rebuild did not close the previous item")
	}
	if second.closed {
		t.Fatal("rebuild closed the new item")
	}
	if err := backend.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !second.closed {
		t.Fatal("close did not release the item")
	}
	if err := backend.Apply(context.Background(), nil); err == nil {
		t.Fatal("apply after close succeeded")
	}
}

// A radio that becomes unselected loses its toggle-type in the controller
// model; the backend must explicitly clear the old deedles property instead of
// leaving it checked.
func TestTrayBackendClearsToggleWhenCheckDisappears(t *testing.T) {
	backend := newTrayBackend(func(string) error { return nil })
	fake := &fakeMenuOps{root: &fakeMenuNode{}}
	backend.newOps = func(trayui.Tray) (menuOps, error) { return fake, nil }
	root := buildTestRoot(trayui.MenuItem{
		Key: "mode.targeted", Kind: trayui.BackendItem, Label: "Targeted",
		Enabled: true, Visible: true, Checked: true, Check: trayui.CheckRadio,
	})
	if err := backend.Build(context.Background(), trayui.Tray{Title: "RouteLatch"}, root); err != nil {
		t.Fatal(err)
	}
	plain := trayui.MenuItem{Key: "mode.targeted", Kind: trayui.BackendItem, Label: "Targeted", Enabled: true, Visible: true}
	if err := backend.Apply(context.Background(), []trayui.Op{{Kind: trayui.OpUpdateNode, Parent: "root", Key: "mode.targeted", Item: plain}}); err != nil {
		t.Fatal(err)
	}
	node := fake.root.children[0]
	if !node.props.ClearCheck || node.props.Check != trayui.CheckNone {
		t.Fatalf("clear props = %+v", node.props)
	}
	if err := backend.Apply(context.Background(), []trayui.Op{{Kind: trayui.OpUpdateNode, Parent: "root", Key: "mode.targeted", Item: plain}}); err != nil {
		t.Fatal(err)
	}
	if fake.root.children[0].props.ClearCheck {
		t.Fatal("a plain update cleared the toggle twice")
	}
}

// A nested page replacement must update the same child list on add and remove.
// This regressed when an add stored its parent list under the bare parent key
// while a remove looked it up through the parent's own identity.
func TestTrayBackendNestedPageReplacement(t *testing.T) {
	backend := newTrayBackend(func(string) error { return nil })
	fake := &fakeMenuOps{root: &fakeMenuNode{}}
	backend.newOps = func(trayui.Tray) (menuOps, error) { return fake, nil }
	root := buildTestRoot(trayui.MenuItem{
		Key: "proposals", Kind: trayui.BackendSubmenu, Label: "Proposals", Enabled: true, Visible: true,
		Children: []trayui.MenuItem{{Key: "proposals.empty", Kind: trayui.BackendItem, Label: "No pending proposals", Enabled: false, Visible: true}},
	})
	if err := backend.Build(context.Background(), trayui.Tray{Title: "RouteLatch"}, root); err != nil {
		t.Fatal(err)
	}
	loading := trayui.MenuItem{Key: "proposals.loading", Kind: trayui.BackendItem, Label: "Proposal list refreshing…", Enabled: false, Visible: true}
	ops := []trayui.Op{
		{Kind: trayui.OpRemoveNode, Parent: "proposals", Key: "proposals.empty"},
		{Kind: trayui.OpAddNode, Parent: "proposals", Index: 0, Key: "proposals.loading", Item: loading},
	}
	if err := backend.Apply(context.Background(), ops); err != nil {
		t.Fatalf("replace with loading: %v", err)
	}
	empty := trayui.MenuItem{Key: "proposals.empty", Kind: trayui.BackendItem, Label: "No pending proposals", Enabled: false, Visible: true}
	ops = []trayui.Op{
		{Kind: trayui.OpRemoveNode, Parent: "proposals", Key: "proposals.loading"},
		{Kind: trayui.OpAddNode, Parent: "proposals", Index: 0, Key: "proposals.empty", Item: empty},
	}
	if err := backend.Apply(context.Background(), ops); err != nil {
		t.Fatalf("replace with empty: %v", err)
	}
	proposals := fake.root.children[0]
	if len(proposals.children) != 1 || proposals.children[0].props.Label != "No pending proposals" {
		t.Fatalf("nested children = %+v", proposals.children)
	}
}

// A child-bearing submenu can exist under two parents for one batch (added
// under an earlier parent before the old copy is removed from a later one).
// Its descendants must stay attached to the new copy; a bare-key identity used
// to delete the new children while removing the old row.
func TestTrayBackendMovedSubtreeKeepsChildren(t *testing.T) {
	backend := newTrayBackend(func(string) error { return nil })
	fake := &fakeMenuOps{root: &fakeMenuNode{}}
	backend.newOps = func(trayui.Tray) (menuOps, error) { return fake, nil }
	row := func() trayui.MenuItem {
		return trayui.MenuItem{Key: "row", Kind: trayui.BackendSubmenu, Label: "row", Enabled: true, Visible: true, Children: []trayui.MenuItem{
			{Key: "row.select", Kind: trayui.BackendItem, Label: "Select exit", Enabled: true, Visible: true},
			{Key: "row.remove", Kind: trayui.BackendItem, Label: "Remove…", Enabled: true, Visible: true},
		}}
	}
	root := buildTestRoot(
		trayui.MenuItem{Key: "g1", Kind: trayui.BackendSubmenu, Label: "G1", Enabled: true, Visible: true, Children: []trayui.MenuItem{
			{Key: "g1.empty", Kind: trayui.BackendItem, Label: "No rows", Enabled: false, Visible: true},
		}},
		trayui.MenuItem{Key: "g2", Kind: trayui.BackendSubmenu, Label: "G2", Enabled: true, Visible: true, Children: []trayui.MenuItem{row()}},
	)
	if err := backend.Build(context.Background(), trayui.Tray{Title: "RouteLatch"}, root); err != nil {
		t.Fatal(err)
	}
	ops := []trayui.Op{
		{Kind: trayui.OpRemoveNode, Parent: "g1", Key: "g1.empty"},
		{Kind: trayui.OpAddNode, Parent: "g1", Index: 0, Key: "row", Item: row()},
		{Kind: trayui.OpRemoveNode, Parent: "g2", Key: "row"},
	}
	if err := backend.Apply(context.Background(), ops); err != nil {
		t.Fatalf("batch apply: %v", err)
	}
	g1, g2 := fake.root.children[0], fake.root.children[1]
	if len(g1.children) != 1 || g1.children[0].props.Label != "row" {
		t.Fatalf("g1 = %+v", g1.children)
	}
	if len(g1.children[0].children) != 2 {
		t.Fatalf("new row lost its children: %d", len(g1.children[0].children))
	}
	if len(g2.children) != 0 {
		t.Fatalf("g2 still has %d children", len(g2.children))
	}
}

// The real Binder emits the add under the first parent before the remove from
// the later one; drive the same sequence through it to lock the op order.
func TestTrayBackendBinderMovedSubtree(t *testing.T) {
	backend := newTrayBackend(func(string) error { return nil })
	fake := &fakeMenuOps{root: &fakeMenuNode{}}
	backend.newOps = func(trayui.Tray) (menuOps, error) { return fake, nil }
	binder, err := trayui.NewBinder(backend)
	if err != nil {
		t.Fatal(err)
	}
	item := func(key, label string) trayui.Node {
		return trayui.Node{Key: key, Label: label, Kind: trayui.NodeItem, Enabled: true, Visible: true}
	}
	sub := func(key, label string, children ...trayui.Node) trayui.Node {
		return trayui.Node{Key: key, Label: label, Kind: trayui.NodeSubmenu, Enabled: true, Visible: true, Children: children}
	}
	root := func(children ...trayui.Node) trayui.Node {
		return trayui.Node{Key: "root", Label: "RouteLatch", Kind: trayui.NodeSubmenu, Enabled: true, Visible: true, Children: children}
	}
	row := sub("row", "row", item("row.select", "Select exit"), item("row.remove", "Remove…"))
	first := trayui.View{Root: root(sub("g1", "G1", item("g1.empty", "No rows")), sub("g2", "G2", row)), Engine: trayui.EngineReady}
	second := trayui.View{Root: root(sub("g1", "G1", row), sub("g2", "G2", item("g2.empty", "No rows"))), Engine: trayui.EngineReady}
	if _, err := binder.Apply(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := binder.Apply(context.Background(), second); err != nil {
		t.Fatalf("binder apply: %v", err)
	}
	g1 := fake.root.children[0]
	if len(g1.children) != 1 || len(g1.children[0].children) != 2 {
		t.Fatalf("moved row = %+v", g1.children)
	}
	if len(fake.root.children[1].children) != 1 {
		t.Fatalf("g2 = %+v", fake.root.children[1].children)
	}
}

func TestTrayBackendCreateFailureIsUnavailable(t *testing.T) {
	backend := newTrayBackend(func(string) error { return nil })
	backend.newOps = func(trayui.Tray) (menuOps, error) { return nil, errors.New("no bus") }
	err := backend.Build(context.Background(), trayui.Tray{Title: "RouteLatch"}, buildTestRoot())
	if !errors.Is(err, errTrayUnavailable) {
		t.Fatalf("Build error = %v, want errTrayUnavailable", err)
	}
}
