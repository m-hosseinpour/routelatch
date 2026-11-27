package trayui

import (
	"context"
	"testing"

	"routelatch/internal/control"
)

// TestRenderedActionSurfaceIsComplete walks every rendered action in the
// representative engine states. It is the G2 cross-check that the
// visible surface and the click path cannot drift: each action has a kind, a
// management request exactly when it is a management action, and an enabled
// item is accepted by the same availability rule a click uses.
func TestRenderedActionSurfaceIsComplete(t *testing.T) {
	states := map[string]func(t *testing.T) (*Controller, func()){
		"loading": func(t *testing.T) (*Controller, func()) {
			caller := newFixtureCaller(newFixture())
			ctl, err := New(Options{Caller: caller, Now: newFakeClock().Now})
			if err != nil {
				t.Fatal(err)
			}
			return ctl, func() {}
		},
		"ready": func(t *testing.T) (*Controller, func()) {
			s := newSetup(t, newFixture(), nil)
			return s.ctl, func() {}
		},
		"ready with applied desktop": func(t *testing.T) (*Controller, func()) {
			fx := newFixture()
			fx.desktop.Applied = true
			s := newSetup(t, fx, nil)
			return s.ctl, func() {}
		},
		"stopped": func(t *testing.T) (*Controller, func()) {
			fx := newFixture()
			fx.status["mode"] = "stopped"
			fx.status["listeners_open"] = false
			s := newSetup(t, fx, nil)
			return s.ctl, func() {}
		},
		"unavailable": func(t *testing.T) (*Controller, func()) {
			s := newSetup(t, newFixture(), nil)
			s.caller.setTransport(true)
			if err := s.ctl.RefreshStatus(context.Background()); err == nil {
				t.Fatal("status unexpectedly succeeded")
			}
			return s.ctl, func() {}
		},
		"job running": func(t *testing.T) (*Controller, func()) {
			s := newSetup(t, newFixture(), nil)
			entered, release := s.caller.gateOn(control.OpBackup)
			if _, err := s.ctl.Trigger(context.Background(), ActionBackup, Target{}); err != nil {
				t.Fatal(err)
			}
			<-entered
			return s.ctl, func() {
				release()
				waitFor(t, "job after surface scan", func() bool { return s.ctl.View().Job == nil })
			}
		},
	}

	known := make(map[Action]bool, len(allActions))
	for _, a := range allActions {
		known[a] = true
	}

	for name, build := range states {
		t.Run(name, func(t *testing.T) {
			ctl, cleanup := build(t)
			defer cleanup()
			view := ctl.View()

			ctl.mu.Lock()
			defer ctl.mu.Unlock()

			seen := map[string]bool{}
			terminals := 0
			walk(view.Root, func(n Node) {
				if n.Key == "" {
					t.Fatalf("node %q has an empty key", n.Label)
				}
				if seen[n.Key] {
					t.Fatalf("duplicate menu key %q", n.Key)
				}
				seen[n.Key] = true

				if n.Kind == NodeItem && n.Label == "" {
					t.Fatalf("%q has an empty label", n.Key)
				}
				if containsControl(n.Label) || runeLen(n.Label) > LabelLimit {
					t.Fatalf("%q has an unsafe label %q", n.Key, n.Label)
				}
				if n.Action == ActionNone {
					return
				}
				if !known[n.Action] {
					t.Fatalf("%q uses an action outside the closed set: %d", n.Key, n.Action)
				}
				kind := kindOf(n.Action)
				if kind == kindNone {
					t.Fatalf("%q (%s) has no action kind", n.Key, n.Action)
				}
				_, hasRequest := requestFor(n.Action, n.Target)
				if (kind == kindRequest) != hasRequest {
					t.Fatalf("%q (%s) request=%t for kind %v", n.Key, n.Action, hasRequest, kind)
				}
				if n.Action == ActionTerminalMenu {
					terminals++
				}
				if n.Enabled {
					if err := ctl.allowedLocked(n.Action, n.Target); err != nil {
						t.Fatalf("%q (%s) is enabled but a click is rejected: %v", n.Key, n.Action, err)
					}
				}
			})

			if view.Engine == EngineLoading {
				if terminals != 0 {
					t.Fatalf("loading menu offers %d terminal entries", terminals)
				}
			} else if terminals != 3 {
				t.Fatalf("terminal entries = %d, want Advanced/Rules/Pool", terminals)
			}
			for _, key := range []string{"advanced.terminal", "rules.add.terminal", "pool.import.terminal"} {
				if view.Engine == EngineLoading {
					break
				}
				node, ok := findNode(view.Root, key)
				if !ok || node.Action != ActionTerminalMenu || !node.Enabled {
					t.Fatalf("%s = %+v, want an enabled terminal entry", key, node)
				}
			}
		})
	}
}
