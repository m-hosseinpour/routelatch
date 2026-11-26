package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"deedles.dev/tray"

	"routelatch/internal/autostart"
	"routelatch/internal/systemd"
	"routelatch/internal/trayui"
)

// This file is the R15-D2 deedles adapter for trayui.Backend. The pure menu
// model stays in internal/trayui; this layer owns the StatusNotifierItem, the
// stable-key <-> dbusmenu-id mapping and the click callback into Runner.Click.
//
// menuOps is the primitive set the adapter needs. Production maps it to
// deedles; tests record it, so the stable-key/order/update rules can be tested
// without a session bus.

type menuNode any

type menuItemProps struct {
	Label   string
	Kind    trayui.BackendItemKind
	Enabled bool
	Visible bool
	Checked bool
	Check   trayui.CheckKind
	Icon    string
	OnClick func() error
	// ClearCheck asks the backend to reset a toggle that became a plain item.
	// Without it a de-selected radio keeps its old toggle-type/state, because a
	// zero-valued CheckKind means "no property change" to deedles.
	ClearCheck bool
}

type menuOps interface {
	Add(parent menuNode, props menuItemProps) (menuNode, error)
	Remove(node menuNode) error
	// Move places node inside parent. A nil before appends; otherwise node is
	// inserted immediately before that sibling.
	Move(parent, node, before menuNode) error
	Set(node menuNode, props menuItemProps) error
	SetTray(t trayui.Tray) error
	Close() error
}

// deedlesOps implements menuOps over one deedles StatusNotifierItem.
type deedlesOps struct {
	item *tray.Item
	menu *tray.Menu
}

func newDeedlesOps(t trayui.Tray) (menuOps, error) {
	item, err := tray.New(
		tray.ItemID("routelatch"),
		tray.ItemTitle(t.Title),
		tray.ItemIconPixmap(trayStatusIcon(t.Icon)),
		tray.ItemToolTip("", nil, t.Title, t.Tooltip),
		tray.ItemIsMenu(true),
	)
	if err != nil {
		return nil, err
	}
	return &deedlesOps{item: item, menu: item.Menu()}, nil
}

func (o *deedlesOps) Add(parent menuNode, p menuItemProps) (menuNode, error) {
	props := p.deedlesProps()
	switch parent := parent.(type) {
	case nil:
		return o.menu.AddChild(props...)
	case *tray.MenuItem:
		return parent.AddChild(props...)
	default:
		return nil, fmt.Errorf("app.tray: unsupported menu parent %T", parent)
	}
}

func (o *deedlesOps) Remove(node menuNode) error {
	item, ok := node.(*tray.MenuItem)
	if !ok {
		return errors.New("app.tray: not a menu item")
	}
	return item.Remove()
}

func (o *deedlesOps) Move(parent, node, before menuNode) error {
	item, ok := node.(*tray.MenuItem)
	if !ok {
		return errors.New("app.tray: not a menu item")
	}
	if before != nil {
		sibling, ok := before.(*tray.MenuItem)
		if !ok {
			return errors.New("app.tray: not a menu sibling")
		}
		return item.MoveBefore(sibling)
	}
	switch parent := parent.(type) {
	case nil:
		return o.menu.AppendChild(item)
	case *tray.MenuItem:
		return parent.AppendChild(item)
	default:
		return fmt.Errorf("app.tray: unsupported menu parent %T", parent)
	}
}

func (o *deedlesOps) Set(node menuNode, p menuItemProps) error {
	item, ok := node.(*tray.MenuItem)
	if !ok {
		return errors.New("app.tray: not a menu item")
	}
	return item.SetProps(p.deedlesProps()...)
}

func (o *deedlesOps) SetTray(t trayui.Tray) error {
	return o.item.SetProps(
		tray.ItemTitle(t.Title),
		tray.ItemToolTip("", nil, t.Title, t.Tooltip),
		tray.ItemIconPixmap(trayStatusIcon(t.Icon)),
	)
}

func (o *deedlesOps) Close() error {
	if o.item == nil {
		return nil
	}
	err := o.item.Close()
	o.item = nil
	o.menu = nil
	return err
}

func (p menuItemProps) deedlesProps() []tray.MenuItemProp {
	props := []tray.MenuItemProp{
		tray.MenuItemLabel(p.Label),
		tray.MenuItemEnabled(p.Enabled),
		tray.MenuItemVisible(p.Visible),
	}
	if p.Kind == trayui.BackendSeparator {
		props = append(props, tray.MenuItemType(tray.Separator))
	}
	switch p.Check {
	case trayui.CheckRadio:
		props = append(props, tray.MenuItemToggleType(tray.Radio))
	case trayui.CheckCheckbox:
		props = append(props, tray.MenuItemToggleType(tray.Checkmark))
	}
	if p.Check != trayui.CheckNone {
		state := tray.Off
		if p.Checked {
			state = tray.On
		}
		props = append(props, tray.MenuItemToggleState(state))
	} else if p.ClearCheck {
		props = append(props, tray.MenuItemToggleType(tray.NonToggleable), tray.MenuItemToggleState(tray.Off))
	}
	if p.Icon != "" {
		props = append(props, tray.MenuItemIconName(p.Icon))
	}
	if p.OnClick != nil {
		onClick := p.OnClick
		props = append(props, tray.MenuItemHandler(tray.ClickedHandler(func(any, uint32) error {
			return onClick()
		})))
	}
	return props
}

// trayBackend renders trayui.MenuItem trees through menuOps. It keeps its own
// stable-key view so a batch can resolve every operation by parent and then
// key, including a duplicate key that exists under two parents until a later
// remove in the same batch (R15-D1 note; docs/contracts.md §7.3).
//
// Identity is the node's full path: parent path + key. A child-bearing submenu
// may exist twice during a batch (added under one parent before the old copy is
// removed elsewhere), so neither the bare key nor the bare parent key is
// unique; the full path keeps their descendants apart.
type trayBackend struct {
	newOps func(trayui.Tray) (menuOps, error)
	click  func(string) error

	ops      menuOps
	rootKey  string
	handles  map[string]menuNode      // node path -> backend node
	paths    map[string]string        // key -> node path (last add; parents are unique)
	children map[string][]string      // node path -> ordered child keys
	rendered map[string]menuItemProps // node path -> last rendered properties
	closed   bool
}

func newTrayBackend(click func(string) error) *trayBackend {
	return &trayBackend{
		newOps: newDeedlesOps,
		click:  click,
	}
}

func childPath(parentPath, key string) string { return parentPath + "\x00" + key }

func (b *trayBackend) Build(ctx context.Context, t trayui.Tray, root trayui.MenuItem) error {
	if b.closed {
		return errors.New("app.tray: the menu backend is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ops, err := b.newOps(t)
	if err != nil {
		return fmt.Errorf("%w: %v", errTrayUnavailable, err)
	}
	if b.ops != nil {
		// A Build replaces the previous tree. A new item is the only way to
		// drop both the old handlers and deedles' internal id map without
		// leaving unreachable nodes behind; it registers under the same PID.
		_ = b.ops.Close()
	}
	if err := ops.SetTray(t); err != nil {
		_ = ops.Close()
		return fmt.Errorf("app.tray: set the tray item: %w", err)
	}
	b.ops = ops
	b.rootKey = root.Key
	b.handles = make(map[string]menuNode)
	b.paths = make(map[string]string)
	b.children = map[string][]string{root.Key: nil}
	b.rendered = make(map[string]menuItemProps)
	for i, child := range root.Children {
		if err := b.addSubtree(ctx, root.Key, nil, child, i); err != nil {
			return err
		}
	}
	return nil
}

func (b *trayBackend) Apply(ctx context.Context, ops []trayui.Op) error {
	if b.closed {
		return errors.New("app.tray: the menu backend is closed")
	}
	if b.ops == nil {
		return errors.New("app.tray: the menu backend is not built")
	}
	for _, op := range ops {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch op.Kind {
		case trayui.OpSetTray:
			if err := b.ops.SetTray(op.Tray); err != nil {
				return fmt.Errorf("app.tray: update the tray item: %w", err)
			}
		case trayui.OpAddNode:
			if op.Key != op.Item.Key {
				return errors.New("app.tray: add operation key mismatch")
			}
			parentPath, err := b.pathFor(op.Parent)
			if err != nil {
				return err
			}
			parent, err := b.nodeAt(op.Parent)
			if err != nil {
				return err
			}
			if err := b.addSubtree(ctx, parentPath, parent, op.Item, op.Index); err != nil {
				return err
			}
		case trayui.OpRemoveNode:
			parentPath, err := b.pathFor(op.Parent)
			if err != nil {
				return fmt.Errorf("remove: %w", err)
			}
			if err := b.removeSubtree(parentPath, op.Key); err != nil {
				return fmt.Errorf("remove: %w", err)
			}
		case trayui.OpMoveNode:
			if err := b.moveNode(op.Parent, op.Key, op.Index); err != nil {
				return fmt.Errorf("move: %w", err)
			}
		case trayui.OpUpdateNode:
			parentPath, err := b.pathFor(op.Parent)
			if err != nil {
				return fmt.Errorf("update: %w", err)
			}
			ref := childPath(parentPath, op.Key)
			node, ok := b.handles[ref]
			if !ok {
				return fmt.Errorf("update: app.tray: unknown menu node %q under %q", op.Key, op.Parent)
			}
			prev := b.rendered[ref]
			props := propsFor(op.Item)
			props.ClearCheck = prev.Check != trayui.CheckNone && props.Check == trayui.CheckNone
			if err := b.ops.Set(node, props); err != nil {
				return fmt.Errorf("app.tray: update menu node: %w", err)
			}
			b.rendered[ref] = props
		default:
			return fmt.Errorf("app.tray: unknown menu operation %d", op.Kind)
		}
	}
	return nil
}

func (b *trayBackend) Close(context.Context) error {
	if b.closed {
		return nil
	}
	b.closed = true
	if b.ops == nil {
		return nil
	}
	err := b.ops.Close()
	b.ops = nil
	return err
}

// addSubtree inserts one node and its descendants. parentPath identifies the
// parent occurrence, so a duplicate key under another parent keeps its own
// identity and its descendants stay attached to the right copy.
func (b *trayBackend) addSubtree(ctx context.Context, parentPath string, parent menuNode, item trayui.MenuItem, index int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	props := propsFor(item)
	if item.Kind != trayui.BackendSubmenu {
		onClick := b.click
		key := item.Key
		props.OnClick = func() error { return onClick(key) }
	}
	list := b.children[parentPath]
	index = clampIndex(index, len(list))
	// deedles only appends, so an insert before an existing sibling needs a
	// follow-up move. Children map order stays authoritative either way.
	var before menuNode
	if index < len(list) {
		sibling, ok := b.handles[childPath(parentPath, list[index])]
		if !ok {
			return fmt.Errorf("app.tray: unknown menu node %q under path", list[index])
		}
		before = sibling
	}
	node, err := b.ops.Add(parent, props)
	if err != nil {
		return fmt.Errorf("app.tray: add menu node: %w", err)
	}
	ref := childPath(parentPath, item.Key)
	b.handles[ref] = node
	b.paths[item.Key] = ref
	b.rendered[ref] = props
	b.children[parentPath] = insertChildKey(list, index, item.Key)
	b.children[ref] = nil
	if before != nil {
		if err := b.ops.Move(parent, node, before); err != nil {
			return fmt.Errorf("app.tray: position menu node: %w", err)
		}
	}
	for i, child := range item.Children {
		if err := b.addSubtree(ctx, ref, node, child, i); err != nil {
			return err
		}
	}
	return nil
}

func (b *trayBackend) removeSubtree(parentPath, key string) error {
	ref := childPath(parentPath, key)
	node, ok := b.handles[ref]
	if !ok {
		return fmt.Errorf("app.tray: unknown menu node %q under path", key)
	}
	// Remove descendants first so neither the backend nor deedles keeps an
	// unreachable node for a child whose parent was removed.
	for _, childKey := range append([]string(nil), b.children[ref]...) {
		if err := b.removeSubtree(ref, childKey); err != nil {
			return err
		}
	}
	if err := b.ops.Remove(node); err != nil {
		return fmt.Errorf("app.tray: remove menu node: %w", err)
	}
	delete(b.children, ref)
	delete(b.handles, ref)
	delete(b.rendered, ref)
	if current, ok := b.paths[key]; ok && current == ref {
		delete(b.paths, key)
	}
	b.children[parentPath] = removeChildKey(b.children[parentPath], key)
	return nil
}

func (b *trayBackend) moveNode(parentKey, key string, index int) error {
	parentPath, err := b.pathFor(parentKey)
	if err != nil {
		return err
	}
	ref := childPath(parentPath, key)
	node, ok := b.handles[ref]
	if !ok {
		return fmt.Errorf("app.tray: unknown menu node %q under path", key)
	}
	list := removeChildKey(b.children[parentPath], key)
	index = clampIndex(index, len(list))
	list = insertChildKey(list, index, key)
	var before menuNode
	if index+1 < len(list) {
		sibling, ok := b.handles[childPath(parentPath, list[index+1])]
		if !ok {
			return fmt.Errorf("app.tray: unknown menu sibling")
		}
		before = sibling
	}
	parent, err := b.nodeAt(parentKey)
	if err != nil {
		return err
	}
	if err := b.ops.Move(parent, node, before); err != nil {
		return fmt.Errorf("app.tray: move menu node: %w", err)
	}
	b.children[parentPath] = list
	return nil
}

// pathFor resolves a parent key to the node occurrence currently addressed by
// the binder. Submenus are unique in the desired tree; a duplicated leaf never
// appears as a batch parent.
func (b *trayBackend) pathFor(parentKey string) (string, error) {
	if parentKey == b.rootKey {
		return b.rootKey, nil
	}
	path, ok := b.paths[parentKey]
	if !ok {
		return "", fmt.Errorf("app.tray: unknown menu parent")
	}
	return path, nil
}

// nodeAt resolves the menu container for a parent key. The root key is the
// Menu itself; every other parent is a submenu handle.
func (b *trayBackend) nodeAt(parentKey string) (menuNode, error) {
	if parentKey == b.rootKey {
		return nil, nil
	}
	path, err := b.pathFor(parentKey)
	if err != nil {
		return nil, err
	}
	node, ok := b.handles[path]
	if !ok {
		return nil, fmt.Errorf("app.tray: unknown menu parent")
	}
	return node, nil
}

func propsFor(item trayui.MenuItem) menuItemProps {
	label := item.Label
	if item.Kind == trayui.BackendSeparator {
		label = ""
	}
	return menuItemProps{
		Label: label, Kind: item.Kind, Enabled: item.Enabled, Visible: item.Visible,
		Checked: item.Checked, Check: item.Check, Icon: item.Icon,
	}
}

func insertChildKey(keys []string, index int, key string) []string {
	index = clampIndex(index, len(keys))
	keys = append(keys, "")
	copy(keys[index+1:], keys[index:])
	keys[index] = key
	return keys
}

func removeChildKey(keys []string, key string) []string {
	for i, k := range keys {
		if k == key {
			return append(keys[:i], keys[i+1:]...)
		}
	}
	return keys
}

func clampIndex(index, length int) int {
	if index < 0 {
		return 0
	}
	if index > length {
		return length
	}
	return index
}

// errTrayUnavailable marks a failure to create the StatusNotifierItem, which
// the supervisor maps to the documented unsupported/tray_unavailable path.
var errTrayUnavailable = errors.New("the StatusNotifierItem is unavailable")

// trayAutostartManager wires the local XDG autostart manager the controller
// expects. It owns no service/systemd state: the service unit is only inspected
// so the menu can report the documented autostart-vs-service conflict.
func trayAutostartManager() trayui.AutostartManager {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	base, err := autostart.ConfigBase(os.Getenv, home)
	if err != nil {
		return nil
	}
	executable, _ := os.Executable()
	return &autostart.Manager{
		ConfigBase: base,
		Executable: executable,
		ProbeTray: func(ctx context.Context) error {
			return ProbeTrayContext(ctx)
		},
		ProbePIDFD: ProbePIDFD,
		ServiceUnit: func(ctx context.Context) (systemd.Unit, error) {
			cfg := systemd.Config{Getenv: os.Getenv, Home: home}
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			unitBase, _, err := cfg.ConfigBase(ctx)
			if unitBase == "" {
				return systemd.Unit{}, err
			}
			return systemd.ReadUnit(systemd.UnitPath(unitBase))
		},
	}
}
