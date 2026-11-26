package trayui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeTerminal records Open calls and returns one canned outcome.
type fakeTerminal struct {
	mu      sync.Mutex
	opens   int
	err     error
	command string
}

func (t *fakeTerminal) Open(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.opens++
	return t.err
}

func (t *fakeTerminal) ManualCommand() string { return t.command }

func (t *fakeTerminal) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.opens
}

func TestTerminalMenuEntryPointsLaunchOnce(t *testing.T) {
	term := &fakeTerminal{}
	s := newSetup(t, newFixture(), func(o *Options) { o.Terminal = term })

	for _, key := range []string{"rules.add.terminal", "pool.import.terminal", "advanced.terminal"} {
		node, ok := findNode(s.ctl.View().Root, key)
		if !ok || node.Action != ActionTerminalMenu || !node.Enabled {
			t.Fatalf("%s = %+v, want an enabled Terminal menu action", key, node)
		}
	}
	before := len(s.caller.requests())
	for _, key := range []string{"rules.add.terminal", "pool.import.terminal", "advanced.terminal"} {
		node, _ := findNode(s.ctl.View().Root, key)
		if _, err := s.ctl.Trigger(context.Background(), node.Action, node.Target); err != nil {
			t.Fatalf("%s trigger: %v", key, err)
		}
	}
	if got := term.count(); got != 3 {
		t.Fatalf("terminal opens = %d, want 3", got)
	}
	if got := len(s.caller.requests()) - before; got != 0 {
		t.Fatalf("terminal menu issued %d management requests", got)
	}
	if got := s.ctl.View().LastAction; got != "Terminal menu opened" {
		t.Fatalf("last action = %q", got)
	}
	if notes := s.notifier.all(); len(notes) != 0 {
		t.Fatalf("successful terminal launch notified: %+v", notes)
	}
}

func TestTerminalMenuMissingShowsManualCommand(t *testing.T) {
	term := &fakeTerminal{err: ErrNoTerminal, command: "routelatch --data-dir /data/rl menu"}
	s := newSetup(t, newFixture(), func(o *Options) { o.Terminal = term })

	before := len(s.caller.requests())
	_, err := s.ctl.Trigger(context.Background(), ActionTerminalMenu, Target{})
	if !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("trigger error = %v, want ErrNoTerminal", err)
	}
	if got := len(s.caller.requests()) - before; got != 0 {
		t.Fatalf("missing terminal issued %d requests", got)
	}
	if got := s.ctl.View().LastAction; got != "No supported terminal found" {
		t.Fatalf("last action = %q", got)
	}
	notes := s.notifier.all()
	if len(notes) != 1 {
		t.Fatalf("notifications = %+v, want one", notes)
	}
	if !strings.Contains(notes[0].Body, "routelatch --data-dir /data/rl menu") {
		t.Fatalf("notification = %q, want the exact manual command", notes[0].Body)
	}
	if containsControl(notes[0].Body) || runeLen(notes[0].Body) > HeaderLimit {
		t.Fatalf("notification is unsafe: %q", notes[0].Body)
	}
}

func TestTerminalMenuFailureIsExplicit(t *testing.T) {
	for _, tc := range []struct {
		name string
		term Terminal
	}{
		{"launch failure", &fakeTerminal{err: ErrTerminalUnavailable}},
		{"no boundary", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSetup(t, newFixture(), func(o *Options) { o.Terminal = tc.term })
			_, err := s.ctl.Trigger(context.Background(), ActionTerminalMenu, Target{})
			if !errors.Is(err, ErrTerminalUnavailable) {
				t.Fatalf("trigger error = %v, want ErrTerminalUnavailable", err)
			}
			if got := s.ctl.View().LastAction; got != "Failed: terminal unavailable" {
				t.Fatalf("last action = %q", got)
			}
			notes := s.notifier.all()
			if len(notes) != 1 || notes[0].Body != "Failed: terminal unavailable" {
				t.Fatalf("notifications = %+v", notes)
			}
		})
	}
}

func TestTerminalManualCommandIsSanitized(t *testing.T) {
	term := &fakeTerminal{
		err:     ErrNoTerminal,
		command: "routelatch \x1b[2J\u202e\r\n" + strings.Repeat("x", 200),
	}
	s := newSetup(t, newFixture(), func(o *Options) { o.Terminal = term })
	if _, err := s.ctl.Trigger(context.Background(), ActionTerminalMenu, Target{}); !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("trigger error = %v", err)
	}
	notes := s.notifier.all()
	if len(notes) != 1 {
		t.Fatalf("notifications = %+v", notes)
	}
	if containsControl(notes[0].Body) || runeLen(notes[0].Body) > HeaderLimit {
		t.Fatalf("unsafe notification %q", notes[0].Body)
	}
	if containsControl(s.ctl.View().LastAction) {
		t.Fatalf("unsafe last action %q", s.ctl.View().LastAction)
	}
}

func TestTerminalMenuStaysAvailableWithoutAReadyEngine(t *testing.T) {
	t.Run("unavailable", func(t *testing.T) {
		term := &fakeTerminal{}
		s := newSetup(t, newFixture(), func(o *Options) { o.Terminal = term })
		s.caller.setTransport(true)
		if err := s.ctl.RefreshStatus(context.Background()); err == nil {
			t.Fatal("status unexpectedly succeeded")
		}
		node, ok := findNode(s.ctl.View().Root, "advanced.terminal")
		if !ok || !node.Enabled {
			t.Fatalf("advanced.terminal = %+v, want enabled", node)
		}
		if _, err := s.ctl.Trigger(context.Background(), ActionTerminalMenu, Target{}); err != nil {
			t.Fatalf("trigger: %v", err)
		}
		if term.count() != 1 {
			t.Fatalf("terminal opens = %d", term.count())
		}
	})

	t.Run("stopped", func(t *testing.T) {
		fx := newFixture()
		fx.status["mode"] = "stopped"
		fx.status["listeners_open"] = false
		term := &fakeTerminal{}
		s := newSetup(t, fx, func(o *Options) { o.Terminal = term })
		node, ok := findNode(s.ctl.View().Root, "advanced.terminal")
		if !ok || !node.Enabled {
			t.Fatalf("advanced.terminal = %+v, want enabled", node)
		}
		if _, err := s.ctl.Trigger(context.Background(), ActionTerminalMenu, Target{}); err != nil {
			t.Fatalf("trigger: %v", err)
		}
		if term.count() != 1 {
			t.Fatalf("terminal opens = %d", term.count())
		}
	})
}

// TestTerminalSurfaceOutcomeD21 keeps D21 at the terminal boundary: the
// notification and last action carry the manual argv, never a live proposal
// destination, rule operand or URL from the snapshot.
func TestTerminalSurfaceOutcomeD21(t *testing.T) {
	const canary = "http://d21-terminal-canary.example/share?token=secret"
	fx := newFixture()
	fx.proposals[0].Destination.Host = "d21-terminal-canary.example"
	fx.proposals[0].Note = canary
	fx.rules[0].Host = "d21-terminal-canary.example"
	fx.status["warnings"] = []string{canary}
	term := &fakeTerminal{err: ErrNoTerminal, command: "routelatch --data-dir /data/rl menu"}
	s := newSetup(t, fx, func(o *Options) { o.Terminal = term })

	if _, err := s.ctl.Trigger(context.Background(), ActionTerminalMenu, Target{}); !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("trigger error = %v", err)
	}
	notes := s.notifier.all()
	if len(notes) != 1 {
		t.Fatalf("notifications = %+v", notes)
	}
	body := notes[0].Body + " " + s.ctl.View().LastAction
	for _, forbidden := range []string{"d21-terminal-canary", "http://", "https://", "token=", "secret", "share?"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("terminal outcome leaked %q: %q", forbidden, body)
		}
	}
}
