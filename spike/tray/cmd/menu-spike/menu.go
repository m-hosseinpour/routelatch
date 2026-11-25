package main

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"strconv"
	"strings"
	"sync"
	"time"

	"deedles.dev/tray"
)

const poolPageSize = 20

// menuIconImage is intentionally magenta: the installed product tray item on
// this host uses the blue R13-A icon, and the live evidence script needs an
// unambiguous colour to locate this temporary item in a screenshot.
func menuIconImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 22, 22))
	for y := 0; y < 22; y++ {
		for x := 0; x < 22; x++ {
			dx, dy := x-11, y-11
			if dx*dx+dy*dy <= 100 {
				img.Set(x, y, color.RGBA{R: 0xe0, G: 0x00, B: 0xe0, A: 0xff})
			}
		}
	}
	for y := 7; y < 15; y++ {
		for x := 10; x < 12; x++ {
			img.Set(x, y, color.RGBA{R: 0xff, G: 0xe0, B: 0x00, A: 0xff})
		}
	}
	return img
}

type poolGroup struct {
	name      string
	total     int
	parent    *tray.MenuItem
	pageLabel *tray.MenuItem
	prev      *tray.MenuItem
	next      *tray.MenuItem
	sep       *tray.MenuItem
	rows      []*tray.MenuItem
	page      int
}

type spikeMenu struct {
	mu sync.Mutex

	item *tray.Item
	root *tray.Menu

	status     *tray.MenuItem
	lastAction *tray.MenuItem

	mode        map[string]*tray.MenuItem
	stop        *tray.MenuItem
	autostart   *tray.MenuItem
	restartMore *tray.MenuItem

	job     *tray.MenuItem
	confirm []*tray.MenuItem

	expParent *tray.MenuItem
	expChild  *tray.MenuItem

	liveRelabel int

	pool []*poolGroup
}

func newSpikeMenu() *spikeMenu {
	return &spikeMenu{mode: make(map[string]*tray.MenuItem)}
}

func (s *spikeMenu) bind(item *tray.Item) {
	s.item = item
	s.root = item.Menu()
}

func (s *spikeMenu) stages() []string {
	return []string{
		"initial", "relabel", "add", "remove", "visibility", "visibility-restore",
		"radio", "radio-restore", "confirm", "unconfirm", "empty-submenu", "refill-submenu",
		"pool-page:2", "pool-page:99", "raw-label", "relabel-live",
	}
}

// handleMenuEvent receives events addressed to the menu root (id 0), notably
// "opened" and "closed" on hosts that send them.
func (s *spikeMenu) handleMenuEvent(eventID tray.MenuEventID, data any, timestamp uint32) error {
	logf("event=menu_event key=root event=%s data=%v timestamp=%d at=%s",
		eventID, data, timestamp, time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	return nil
}

func (s *spikeMenu) action(key string) tray.MenuItemProp {
	return tray.MenuItemHandler(tray.ClickedHandler(func(data any, timestamp uint32) error {
		start := time.Now()
		logf("event=menu_click key=%s timestamp=%d handler_start=%s", key, timestamp,
			start.UTC().Format("2006-01-02T15:04:05.000Z"))
		s.setLastAction("Last action: " + key + " clicked")
		logf("event=menu_click_done key=%s handler_us=%d", key, time.Since(start).Microseconds())
		return nil
	}))
}

func (s *spikeMenu) addRoot(label string, props ...tray.MenuItemProp) (*tray.MenuItem, error) {
	props = append(props, tray.MenuItemLabel(label))
	return s.root.AddChild(props...)
}

func (s *spikeMenu) addChild(parent *tray.MenuItem, label string, props ...tray.MenuItemProp) (*tray.MenuItem, error) {
	props = append(props, tray.MenuItemLabel(label))
	return parent.AddChild(props...)
}

func (s *spikeMenu) setLastAction(text string) {
	if s.lastAction == nil {
		return
	}
	if err := s.lastAction.SetProps(tray.MenuItemLabel(text)); err != nil {
		logf("event=error stage=set_last_action err=%q", err)
	}
}

func (s *spikeMenu) build() error {
	status, err := s.addRoot("Targeted · 156 usable · 3 open", tray.MenuItemEnabled(false))
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	s.status = status

	mode, err := s.addRoot("Mode")
	if err != nil {
		return fmt.Errorf("mode: %w", err)
	}
	for _, m := range []struct {
		key, label string
	}{
		{"targeted", "Targeted"},
		{"all-proxy", "All proxy"},
		{"all-direct", "All direct"},
	} {
		it, err := s.addChild(mode, m.label,
			tray.MenuItemToggleType(tray.Radio),
			tray.MenuItemToggleState(toggleState(m.key == "targeted")),
			tray.MenuItemHandler(tray.ClickedHandler(func(data any, timestamp uint32) error {
				return s.clickMode(m.key, timestamp)
			})),
		)
		if err != nil {
			return fmt.Errorf("mode %s: %w", m.key, err)
		}
		s.mode[m.key] = it
	}
	if _, err := s.addChild(mode, "", tray.MenuItemType(tray.Separator)); err != nil {
		return fmt.Errorf("mode separator: %w", err)
	}
	stop, err := s.addChild(mode, "Stop listening…", tray.MenuItemEnabled(true),
		tray.MenuItemHandler(tray.ClickedHandler(func(data any, timestamp uint32) error {
			logf("event=menu_click key=stop-listening timestamp=%d", timestamp)
			s.setLastAction("Last action: stop listening needs Confirm")
			return nil
		})))
	if err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	s.stop = stop

	if err := s.buildProposals(); err != nil {
		return err
	}
	if err := s.buildRules(); err != nil {
		return err
	}
	if err := s.buildPool(); err != nil {
		return err
	}
	if err := s.buildDesktop(); err != nil {
		return err
	}
	if err := s.buildSettings(); err != nil {
		return err
	}
	if err := s.buildAdvanced(); err != nil {
		return err
	}
	if err := s.buildExperiment(); err != nil {
		return err
	}

	last, err := s.addRoot("Last action: none", tray.MenuItemEnabled(false))
	if err != nil {
		return fmt.Errorf("last action: %w", err)
	}
	s.lastAction = last

	quit, err := s.addRoot("Quit")
	if err != nil {
		return fmt.Errorf("quit: %w", err)
	}
	if _, err := s.addChild(quit, "Quit RouteLatch", tray.MenuItemHandler(tray.ClickedHandler(
		func(data any, timestamp uint32) error {
			logf("event=menu_click key=quit timestamp=%d", timestamp)
			go waitClickDone()
			return nil
		}))); err != nil {
		return fmt.Errorf("quit item: %w", err)
	}
	if _, err := s.addChild(quit, "Quit and keep desktop proxy…", tray.MenuItemHandler(tray.ClickedHandler(
		func(data any, timestamp uint32) error {
			logf("event=menu_click key=quit-keep timestamp=%d", timestamp)
			go waitClickDone()
			return nil
		}))); err != nil {
		return fmt.Errorf("quit keep item: %w", err)
	}

	return nil
}

func (s *spikeMenu) buildProposals() error {
	proposals, err := s.addRoot("Proposals (3)")
	if err != nil {
		return fmt.Errorf("proposals: %w", err)
	}
	proposal, err := s.addChild(proposals, "example.com · suffix · proposed")
	if err != nil {
		return fmt.Errorf("proposal item: %w", err)
	}
	for _, label := range []string{"Accept", "Reject", "Recheck"} {
		if _, err := s.addChild(proposal, label, s.action("proposal-"+strings.ToLower(label))); err != nil {
			return fmt.Errorf("proposal %s: %w", label, err)
		}
	}
	decided, err := s.addChild(proposals, "Show decided (2)")
	if err != nil {
		return fmt.Errorf("decided: %w", err)
	}
	if _, err := s.addChild(decided, "example.org · accepted (decided)", tray.MenuItemEnabled(false)); err != nil {
		return fmt.Errorf("decided item: %w", err)
	}
	if _, err := s.addChild(proposals, "", tray.MenuItemType(tray.Separator)); err != nil {
		return fmt.Errorf("proposals separator: %w", err)
	}
	if _, err := s.addChild(proposals, "Clear decided/expired…", s.action("proposals-clear")); err != nil {
		return fmt.Errorf("clear proposals: %w", err)
	}
	return nil
}

func (s *spikeMenu) buildRules() error {
	rules, err := s.addRoot("Approved rules (117)")
	if err != nil {
		return fmt.Errorf("rules: %w", err)
	}
	if _, err := s.addChild(rules, "Page 1/6 · 117 rules", tray.MenuItemEnabled(false)); err != nil {
		return fmt.Errorf("rules page label: %w", err)
	}
	for i := 0; i < poolPageSize; i++ {
		label := fmt.Sprintf("rule-%03d · %s · suffix", i+1, []string{"example.com", "docs.example.org", "cdn.example.net"}[i%3])
		it, err := s.addChild(rules, label, s.action(fmt.Sprintf("rule-row-%03d", i+1)))
		if err != nil {
			return fmt.Errorf("rule row %d: %w", i, err)
		}
		if i == 0 {
			if _, err := s.addChild(it, "Remove…", s.action("rule-remove")); err != nil {
				return fmt.Errorf("rule remove: %w", err)
			}
			if _, err := s.addChild(it, "Details", tray.MenuItemEnabled(false)); err != nil {
				return fmt.Errorf("rule details: %w", err)
			}
		}
	}
	if _, err := s.addChild(rules, "", tray.MenuItemType(tray.Separator)); err != nil {
		return fmt.Errorf("rules separator: %w", err)
	}
	if _, err := s.addChild(rules, "Previous page", tray.MenuItemEnabled(false)); err != nil {
		return fmt.Errorf("rules prev: %w", err)
	}
	if _, err := s.addChild(rules, "Next page", s.action("rules-next")); err != nil {
		return fmt.Errorf("rules next: %w", err)
	}
	if _, err := s.addChild(rules, "Add rule… (Terminal menu)", s.action("rules-terminal")); err != nil {
		return fmt.Errorf("rules terminal: %w", err)
	}
	return nil
}

func (s *spikeMenu) buildPool() error {
	pool, err := s.addRoot("Pool")
	if err != nil {
		return fmt.Errorf("pool: %w", err)
	}
	if _, err := s.addChild(pool, "156 usable · 24 untested · 12 cooling · 20 unsupported", tray.MenuItemEnabled(false)); err != nil {
		return fmt.Errorf("pool summary: %w", err)
	}
	auto, err := s.addChild(pool, "Auto select",
		tray.MenuItemToggleType(tray.Radio), tray.MenuItemToggleState(tray.On),
		tray.MenuItemHandler(tray.ClickedHandler(func(data any, timestamp uint32) error {
			logf("event=menu_click key=pool-auto timestamp=%d", timestamp)
			return nil
		})))
	if err != nil {
		return fmt.Errorf("pool auto: %w", err)
	}
	var pinned *tray.MenuItem
	pinned, err = s.addChild(pool, "Pinned: tehran-001 · سرور",
		tray.MenuItemToggleType(tray.Radio), tray.MenuItemToggleState(tray.Off),
		tray.MenuItemHandler(tray.ClickedHandler(func(data any, timestamp uint32) error {
			logf("event=menu_click key=pool-pinned timestamp=%d", timestamp)
			_ = auto.SetProps(tray.MenuItemToggleState(tray.Off))
			_ = pinned.SetProps(tray.MenuItemToggleState(tray.On))
			return nil
		})))
	if err != nil {
		return fmt.Errorf("pool pinned: %w", err)
	}
	if _, err := s.addChild(pool, "", tray.MenuItemType(tray.Separator)); err != nil {
		return fmt.Errorf("pool separator: %w", err)
	}

	for _, g := range []struct {
		name  string
		total int
	}{
		{"Healthy", 120},
		{"Untested", 24},
		{"Cooling", 12},
		{"Unsupported/Invalid", 20},
	} {
		parent, err := s.addChild(pool, fmt.Sprintf("%s (%d)", g.name, g.total))
		if err != nil {
			return fmt.Errorf("pool group %s: %w", g.name, err)
		}
		pg := &poolGroup{name: g.name, total: g.total, parent: parent, page: 1}
		if pg.pageLabel, err = s.addChild(parent, "", tray.MenuItemEnabled(false)); err != nil {
			return fmt.Errorf("pool %s page label: %w", g.name, err)
		}
		if pg.sep, err = s.addChild(parent, "", tray.MenuItemType(tray.Separator)); err != nil {
			return fmt.Errorf("pool %s separator: %w", g.name, err)
		}
		if pg.prev, err = s.addChild(parent, "Previous page", tray.MenuItemEnabled(false),
			tray.MenuItemHandler(tray.ClickedHandler(func(data any, timestamp uint32) error {
				return s.clickPoolPage(pg, -1)
			}))); err != nil {
			return fmt.Errorf("pool %s prev: %w", g.name, err)
		}
		if pg.next, err = s.addChild(parent, "Next page", tray.MenuItemEnabled(true),
			tray.MenuItemHandler(tray.ClickedHandler(func(data any, timestamp uint32) error {
				return s.clickPoolPage(pg, 1)
			}))); err != nil {
			return fmt.Errorf("pool %s next: %w", g.name, err)
		}
		s.pool = append(s.pool, pg)
		if err := s.renderPool(pg, 1); err != nil {
			return err
		}
	}

	if _, err := s.addChild(pool, "Refresh all sources", s.action("pool-refresh")); err != nil {
		return fmt.Errorf("pool refresh: %w", err)
	}
	if _, err := s.addChild(pool, "Test first 20 usable", s.action("pool-test")); err != nil {
		return fmt.Errorf("pool test: %w", err)
	}
	if _, err := s.addChild(pool, "Add/import… (Terminal menu)", s.action("pool-terminal")); err != nil {
		return fmt.Errorf("pool terminal: %w", err)
	}
	return nil
}

func (s *spikeMenu) clickPoolPage(pg *poolGroup, delta int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := pg.page + delta
	if err := s.renderPool(pg, next); err != nil {
		logf("event=error stage=pool_page group=%s err=%q", pg.name, err)
		return nil
	}
	return nil
}

func poolLabel(group string, index int) string {
	if index == 2 {
		return "سرور تهران ۳ — deliberately long mixed label: αβγδεζηθικλμνξοπρστυφχψω Zürich São-Paulo 0123456789 abcdefghijklmnopqrstuvwxyz — end"
	}
	switch index % 5 {
	case 0:
		return fmt.Sprintf("%s-%03d · سرور", strings.ToLower(group), index+1)
	case 1:
		return fmt.Sprintf("%s-%03d · Zürich", strings.ToLower(group), index+1)
	case 2:
		return fmt.Sprintf("%s-%03d · αθήνα", strings.ToLower(group), index+1)
	case 3:
		return fmt.Sprintf("%s-%03d · Москва", strings.ToLower(group), index+1)
	default:
		return fmt.Sprintf("%s-%03d", strings.ToLower(group), index+1)
	}
}

func (s *spikeMenu) renderPool(pg *poolGroup, requested int) error {
	pages := (pg.total + poolPageSize - 1) / poolPageSize
	page := requested
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}

	for _, row := range pg.rows {
		if err := row.Remove(); err != nil {
			return fmt.Errorf("remove %s row: %w", pg.name, err)
		}
	}
	pg.rows = nil

	start := (page - 1) * poolPageSize
	end := start + poolPageSize
	if end > pg.total {
		end = pg.total
	}
	for i := start; i < end; i++ {
		it, err := pg.parent.AddChild(
			tray.MenuItemLabel(poolLabel(pg.name, i)),
			s.action(fmt.Sprintf("pool-%s-page%d-row%03d", strings.ToLower(pg.name), page, i+1)),
		)
		if err != nil {
			return fmt.Errorf("add %s row %d: %w", pg.name, i+1, err)
		}
		pg.rows = append(pg.rows, it)
		if err := it.MoveBefore(pg.sep); err != nil {
			return fmt.Errorf("place %s row %d: %w", pg.name, i+1, err)
		}
	}
	pg.page = page
	label := fmt.Sprintf("Page %d/%d · %d nodes", page, pages, pg.total)
	if err := pg.pageLabel.SetProps(tray.MenuItemLabel(label)); err != nil {
		return err
	}
	if err := pg.prev.SetProps(tray.MenuItemEnabled(page > 1)); err != nil {
		return err
	}
	if err := pg.next.SetProps(tray.MenuItemEnabled(page < pages)); err != nil {
		return err
	}
	logf("event=pool_page group=%s page=%d/%d rows=%d total=%d", pg.name, page, pages, len(pg.rows), pg.total)
	return nil
}

func (s *spikeMenu) buildDesktop() error {
	desktop, err := s.addRoot("Desktop proxy")
	if err != nil {
		return fmt.Errorf("desktop: %w", err)
	}
	if _, err := s.addChild(desktop, "applied · http://127.0.0.1:7890", tray.MenuItemEnabled(false)); err != nil {
		return fmt.Errorf("desktop status: %w", err)
	}
	if _, err := s.addChild(desktop, "Refresh status", s.action("desktop-refresh")); err != nil {
		return fmt.Errorf("desktop refresh: %w", err)
	}
	if _, err := s.addChild(desktop, "Set/Re-apply desktop proxy…", s.action("desktop-set")); err != nil {
		return fmt.Errorf("desktop set: %w", err)
	}
	if _, err := s.addChild(desktop, "Restore previous desktop proxy…", s.action("desktop-restore")); err != nil {
		return fmt.Errorf("desktop restore: %w", err)
	}
	return nil
}

func (s *spikeMenu) buildSettings() error {
	settings, err := s.addRoot("Settings")
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	autostart, err := s.addChild(settings, "Start at login: Off",
		tray.MenuItemToggleType(tray.Checkmark), tray.MenuItemToggleState(tray.Off),
		tray.MenuItemHandler(tray.ClickedHandler(func(data any, timestamp uint32) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			logf("event=menu_click key=autostart timestamp=%d", timestamp)
			next := s.autostart.ToggleState() != tray.On
			label := "Start at login: Off"
			state := tray.Off
			if next {
				label = "Start at login: On"
				state = tray.On
			}
			_ = s.autostart.SetProps(tray.MenuItemLabel(label), tray.MenuItemToggleState(state))
			s.setLastAction("Last action: autostart toggled")
			return nil
		})))
	if err != nil {
		return fmt.Errorf("autostart: %w", err)
	}
	s.autostart = autostart
	if _, err := s.addChild(settings, "http 127.0.0.1:7890 · socks 127.0.0.1:7891 · info", tray.MenuItemEnabled(false)); err != nil {
		return fmt.Errorf("settings summary: %w", err)
	}
	return nil
}

func (s *spikeMenu) buildAdvanced() error {
	advanced, err := s.addRoot("Advanced")
	if err != nil {
		return fmt.Errorf("advanced: %w", err)
	}
	if _, err := s.addChild(advanced, "Restart engine…", s.action("restart-engine")); err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	if _, err := s.addChild(advanced, "Terminal menu…", s.action("terminal-menu")); err != nil {
		return fmt.Errorf("terminal: %w", err)
	}
	restartMore, err := s.addChild(advanced, "Restart engine to resume…",
		tray.MenuItemVisible(false), s.action("restart-resume"))
	if err != nil {
		return fmt.Errorf("restart resume: %w", err)
	}
	s.restartMore = restartMore
	return nil
}

func (s *spikeMenu) buildExperiment() error {
	exp, err := s.addRoot("Experiment")
	if err != nil {
		return fmt.Errorf("experiment: %w", err)
	}
	child, err := s.addChild(exp, "Experiment child", s.action("experiment-child"))
	if err != nil {
		return fmt.Errorf("experiment child: %w", err)
	}
	s.expParent = exp
	s.expChild = child
	return nil
}

func toggleState(on bool) tray.MenuToggleState {
	if on {
		return tray.On
	}
	return tray.Off
}

func (s *spikeMenu) clickMode(key string, timestamp uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	logf("event=menu_click key=mode-%s timestamp=%d", key, timestamp)
	return s.setModeLocked(key)
}

func (s *spikeMenu) setModeLocked(key string) error {
	for k, item := range s.mode {
		state := toggleState(k == key)
		if err := item.SetProps(tray.MenuItemToggleState(state)); err != nil {
			return err
		}
	}
	var status string
	switch key {
	case "all-proxy":
		status = "All proxy · 156 usable · 2 open"
	case "all-direct":
		status = "All direct · 3 open"
	default:
		status = "Targeted · 156 usable · 3 open"
	}
	if err := s.status.SetProps(tray.MenuItemLabel(status)); err != nil {
		return err
	}
	s.setLastAction("Last action: mode " + key)
	return nil
}

func (s *spikeMenu) apply(stage string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch stage {
	case "initial":
		return "no-op; menu is in its initial state", nil
	case "relabel":
		if err := s.status.SetProps(tray.MenuItemLabel("Targeted · 155 usable · 4 open · relabelled")); err != nil {
			return "", err
		}
		s.setLastAction("Last action: relabel applied")
		return "relabelled status and Last action", nil
	case "add":
		if s.job != nil {
			return "", errors.New("job item already present")
		}
		job, err := s.root.AddChild(
			tray.MenuItemLabel("Job: pool refresh running"),
			tray.MenuItemEnabled(false),
		)
		if err != nil {
			return "", err
		}
		if err := job.MoveBefore(s.status); err != nil {
			return "", err
		}
		s.job = job
		return "added Job item before status", nil
	case "remove":
		if s.job == nil {
			return "", errors.New("job item not present")
		}
		if err := s.job.Remove(); err != nil {
			return "", err
		}
		s.job = nil
		return "removed Job item", nil
	case "visibility":
		if err := s.restartMore.SetProps(tray.MenuItemVisible(true)); err != nil {
			return "", err
		}
		if err := s.stop.SetProps(tray.MenuItemEnabled(false)); err != nil {
			return "", err
		}
		return "restart-more visible=true; stop enabled=false", nil
	case "visibility-restore":
		if err := s.restartMore.SetProps(tray.MenuItemVisible(false)); err != nil {
			return "", err
		}
		if err := s.stop.SetProps(tray.MenuItemEnabled(true)); err != nil {
			return "", err
		}
		return "restart-more visible=false; stop enabled=true", nil
	case "radio":
		if err := s.setModeLocked("all-proxy"); err != nil {
			return "", err
		}
		if err := s.autostart.SetProps(
			tray.MenuItemLabel("Start at login: On"),
			tray.MenuItemToggleState(tray.On),
		); err != nil {
			return "", err
		}
		return "mode radio -> all-proxy; autostart checkmark -> On", nil
	case "radio-restore":
		if err := s.setModeLocked("targeted"); err != nil {
			return "", err
		}
		if err := s.autostart.SetProps(
			tray.MenuItemLabel("Start at login: Off"),
			tray.MenuItemToggleState(tray.Off),
		); err != nil {
			return "", err
		}
		return "mode radio -> targeted; autostart checkmark -> Off", nil
	case "confirm":
		if len(s.confirm) > 0 {
			return "", errors.New("confirmation already pending")
		}
		confirm, err := s.root.AddChild(tray.MenuItemLabel("Confirm: Stop listening…"),
			tray.MenuItemHandler(tray.ClickedHandler(func(data any, timestamp uint32) error {
				logf("event=menu_click key=confirm-stop timestamp=%d", timestamp)
				return nil
			})))
		if err != nil {
			return "", err
		}
		cancel, err := s.root.AddChild(tray.MenuItemLabel("Cancel"),
			tray.MenuItemHandler(tray.ClickedHandler(func(data any, timestamp uint32) error {
				logf("event=menu_click key=cancel-confirm timestamp=%d", timestamp)
				return nil
			})))
		if err != nil {
			return "", err
		}
		if err := confirm.MoveBefore(s.status); err != nil {
			return "", err
		}
		if err := cancel.MoveBefore(s.status); err != nil {
			return "", err
		}
		s.confirm = []*tray.MenuItem{confirm, cancel}
		return "added Confirm and Cancel before status", nil
	case "unconfirm":
		if len(s.confirm) == 0 {
			return "", errors.New("no confirmation pending")
		}
		for _, item := range s.confirm {
			if err := item.Remove(); err != nil {
				return "", err
			}
		}
		s.confirm = nil
		return "removed confirmation items", nil
	case "empty-submenu":
		if err := s.expChild.Remove(); err != nil {
			return "", err
		}
		return "removed the only Experiment child (parent should stop being a submenu)", nil
	case "refill-submenu":
		child, err := s.expParent.AddChild(tray.MenuItemLabel("Experiment child again"), s.action("experiment-child-again"))
		if err != nil {
			return "", err
		}
		s.expChild = child
		return "re-added an Experiment child", nil
	case "raw-label":
		if err := s.status.SetProps(tray.MenuItemLabel(
			"Unicode δοκιμή · آزمون · テスト — ESC-free bidi-free label")); err != nil {
			return "", err
		}
		return "status label replaced with control-free multilingual text", nil
	case "relabel-live":
		s.liveRelabel++
		label := fmt.Sprintf("Live relabel #%d · 155 usable · 4 open", s.liveRelabel)
		if err := s.status.SetProps(tray.MenuItemLabel(label)); err != nil {
			return "", err
		}
		return "status label -> " + label, nil
	}

	if after, ok := strings.CutPrefix(stage, "pool-page:"); ok {
		page, err := strconv.Atoi(after)
		if err != nil {
			return "", fmt.Errorf("bad page %q: %w", stage, err)
		}
		pg := s.pool[0] // Healthy group: 120 nodes, 6 pages
		if err := s.renderPool(pg, page); err != nil {
			return "", err
		}
		return fmt.Sprintf("Healthy page requested=%d effective=%d", page, pg.page), nil
	}

	return "", fmt.Errorf("unknown stage %q", stage)
}
