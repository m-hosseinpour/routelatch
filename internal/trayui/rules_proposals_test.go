package trayui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/control"
)

// pageRuleKeys returns the rule submenu keys rendered on the current page.
func pageRuleKeys(root Node) []string {
	rules, ok := findNode(root, "rules")
	if !ok {
		return nil
	}
	out := make([]string, 0, PageSize)
	for _, child := range rules.Children {
		if strings.HasPrefix(child.Key, "rules.r-") {
			out = append(out, child.Key)
		}
	}
	return out
}

func TestRulePagesCoverAll117RulesOnce(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()

	seen := make(map[string]bool, 117)
	for page := 1; page <= 6; page++ {
		view := s.ctl.View()
		label, ok := findNode(view.Root, "rules.page")
		if !ok {
			t.Fatalf("page %d: no page label", page)
		}
		wantLabel := "Page " + strconv.Itoa(page) + "/6"
		if label.Label != wantLabel {
			t.Fatalf("page %d label = %q, want %q", page, label.Label, wantLabel)
		}
		keys := pageRuleKeys(view.Root)
		wantCount := PageSize
		if page == 6 {
			wantCount = 117 - 5*PageSize
		}
		if len(keys) != wantCount {
			t.Fatalf("page %d has %d rule nodes, want %d", page, len(keys), wantCount)
		}
		for _, key := range keys {
			if seen[key] {
				t.Fatalf("rule %q appears on more than one page", key)
			}
			seen[key] = true
		}
		prev, _ := findNode(view.Root, "rules.prev")
		next, _ := findNode(view.Root, "rules.next")
		if page == 1 && prev.Enabled {
			t.Fatal("Previous page enabled on page 1")
		}
		if page == 6 && next.Enabled {
			t.Fatal("Next page enabled on the last page")
		}
		if page < 6 {
			if _, err := s.ctl.Trigger(ctx, ActionPageNext, Target{Section: SectionRules}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(seen) != 117 {
		t.Fatalf("reachable rules = %d, want 117", len(seen))
	}
	// The count label is the full list, not the current page.
	if n, _ := findNode(s.ctl.View().Root, "rules"); n.Label != "Approved rules (117)" {
		t.Fatalf("rules label = %q", n.Label)
	}
}

func TestRuleRemoveConfirmationUsesTheSelectedRule(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	view := s.ctl.View()
	node, ok := findNode(view.Root, "rules.r-005.remove")
	if !ok || !node.Enabled || node.Action != ActionRuleRemove || node.Target.RuleID != "r-005" {
		t.Fatalf("remove node = %+v", node)
	}
	res, err := s.ctl.Trigger(ctx, node.Action, node.Target)
	if err != nil || res.Status != ResultConfirmationRequired {
		t.Fatalf("remove trigger = %+v, %v", res, err)
	}
	if s.caller.countOp(control.OpRulesRemove) != 0 {
		t.Fatal("Remove executed before confirmation")
	}
	pending := s.ctl.View().Pending
	if pending == nil || pending.Label != "Remove rule r-005" {
		t.Fatalf("pending = %+v", pending)
	}
	if _, err := s.ctl.Confirm(ctx); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	reqs := s.caller.requests()
	removes := 0
	for _, req := range reqs {
		if req.Op == control.OpRulesRemove {
			removes++
			if req.ID != "r-005" {
				t.Fatalf("rules.remove id = %q, want the selected r-005", req.ID)
			}
		}
	}
	if removes != 1 {
		t.Fatalf("rules.remove requests = %d, want 1", removes)
	}
	if got := s.ctl.View().LastAction; got != "Rule removed (1 connections closed)" {
		t.Fatalf("last action = %q", got)
	}
	for _, note := range s.notifier.all() {
		if strings.Contains(note.Body, "rule-005.example") {
			t.Fatalf("notification leaked the rule destination: %q", note.Body)
		}
	}
	if _, err := s.ctl.Confirm(ctx); !errors.Is(err, ErrNoPending) {
		t.Fatalf("second confirm = %v", err)
	}
	if s.caller.countOp(control.OpRulesRemove) != 1 {
		t.Fatal("second confirm executed another removal")
	}
}

func TestProposalAcceptRouteRunsOnTheClick(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	view := s.ctl.View()
	node, ok := findNode(view.Root, "proposals.pending.p-route-2.accept")
	if !ok || !node.Enabled || node.Target.ProposalID != "p-route-2" {
		t.Fatalf("accept node = %+v", node)
	}
	res, err := s.ctl.Trigger(ctx, node.Action, node.Target)
	if err != nil || res.Status != ResultCompleted {
		t.Fatalf("accept route = %+v, %v", res, err)
	}
	if pending := s.ctl.View().Pending; pending != nil {
		t.Fatalf("route accept created a pending confirmation: %+v", pending)
	}
	reqs := s.caller.requests()
	accepts := 0
	for _, req := range reqs {
		if req.Op == control.OpProposalsAccept {
			accepts++
			if req.ID != "p-route-2" {
				t.Fatalf("accept id = %q, want p-route-2", req.ID)
			}
		}
	}
	if accepts != 1 {
		t.Fatalf("accept requests = %d, want 1", accepts)
	}
	if got := s.ctl.View().LastAction; got != "Proposal accepted" {
		t.Fatalf("last action = %q", got)
	}
	for _, note := range s.notifier.all() {
		if strings.Contains(note.Body, "other.example") {
			t.Fatalf("notification leaked the proposal destination: %q", note.Body)
		}
	}
}

func TestProposalAcceptUnrouteNeedsConfirmationAndUsesTheSelectedProposal(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	view := s.ctl.View()
	node, ok := findNode(view.Root, "proposals.pending.p-unroute-1.accept")
	if !ok || !node.Enabled || node.Target.ProposalID != "p-unroute-1" {
		t.Fatalf("accept node = %+v", node)
	}
	res, err := s.ctl.Trigger(ctx, node.Action, node.Target)
	if err != nil || res.Status != ResultConfirmationRequired {
		t.Fatalf("accept unroute = %+v, %v", res, err)
	}
	if s.caller.countOp(control.OpProposalsAccept) != 0 {
		t.Fatal("unroute accept executed before confirmation")
	}
	pending := s.ctl.View().Pending
	if pending == nil || pending.Target.ProposalID != "p-unroute-1" {
		t.Fatalf("pending = %+v", pending)
	}
	if pending.Label != "Accept unroute (removes rule r-000)" {
		t.Fatalf("pending label = %q", pending.Label)
	}
	if _, err := s.ctl.Confirm(ctx); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	reqs := s.caller.requests()
	accepts := 0
	for _, req := range reqs {
		if req.Op == control.OpProposalsAccept {
			accepts++
			if req.ID != "p-unroute-1" {
				t.Fatalf("accept id = %q, want p-unroute-1", req.ID)
			}
		}
	}
	if accepts != 1 {
		t.Fatalf("accept requests = %d, want 1", accepts)
	}
	if got := s.ctl.View().LastAction; got != "Unroute accepted; rule removed" {
		t.Fatalf("last action = %q", got)
	}
	for _, note := range s.notifier.all() {
		if strings.Contains(note.Body, "rule-000.example") {
			t.Fatalf("notification leaked the unroute destination: %q", note.Body)
		}
	}
	if _, err := s.ctl.Confirm(ctx); !errors.Is(err, ErrNoPending) {
		t.Fatalf("second confirm = %v", err)
	}
	if s.caller.countOp(control.OpProposalsAccept) != 1 {
		t.Fatal("second confirm executed another accept")
	}
}

func TestUnrouteRecheckIsRejectedByTheAvailabilityRule(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	view := s.ctl.View()
	node, ok := findNode(view.Root, "proposals.pending.p-unroute-1.recheck")
	if !ok || node.Enabled {
		t.Fatalf("unroute recheck menu node = %+v", node)
	}
	if _, err := s.ctl.Trigger(ctx, ActionProposalRecheck, Target{ProposalID: "p-unroute-1"}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("unroute recheck trigger = %v, want ErrBlocked", err)
	}
	if s.caller.countOp(control.OpProposalsRecheck) != 0 {
		t.Fatal("unroute recheck reached the socket")
	}

	// A route proposal still rechecks, exactly once through the job slot.
	if res, err := s.ctl.Trigger(ctx, ActionProposalRecheck, Target{ProposalID: "p-route-2"}); err != nil || res.Status != ResultJobStarted {
		t.Fatalf("route recheck = %+v, %v", res, err)
	}
	waitFor(t, "recheck job", func() bool { return s.ctl.View().Job == nil })
	if s.caller.countOp(control.OpProposalsRecheck) != 1 {
		t.Fatalf("recheck requests = %d, want 1", s.caller.countOp(control.OpProposalsRecheck))
	}
}

func TestProposalRejectRecheckAndClearReachTheSocketOnce(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()

	res, err := s.ctl.Trigger(ctx, ActionProposalReject, Target{ProposalID: "p-route-1"})
	if err != nil || res.Status != ResultCompleted {
		t.Fatalf("reject = %+v, %v", res, err)
	}
	if s.caller.countOp(control.OpProposalsReject) != 1 {
		t.Fatal("reject did not reach the socket once")
	}
	for _, req := range s.caller.requests() {
		if req.Op == control.OpProposalsReject && req.ID != "p-route-1" {
			t.Fatalf("reject id = %q, want p-route-1", req.ID)
		}
	}
	if got := s.ctl.View().LastAction; got != "Proposal rejected" {
		t.Fatalf("last action after reject = %q", got)
	}

	if res, err := s.ctl.Trigger(ctx, ActionProposalRecheck, Target{ProposalID: "p-route-2"}); err != nil || res.Status != ResultJobStarted {
		t.Fatalf("recheck = %+v, %v", res, err)
	}
	waitFor(t, "recheck job", func() bool { return s.ctl.View().Job == nil })
	if s.caller.countOp(control.OpProposalsRecheck) != 1 {
		t.Fatalf("recheck requests = %d, want 1", s.caller.countOp(control.OpProposalsRecheck))
	}
	if got := s.ctl.View().LastAction; got != "Proposal rechecked" {
		t.Fatalf("last action after recheck = %q", got)
	}

	res, err = s.ctl.Trigger(ctx, ActionProposalsClear, Target{})
	if err != nil || res.Status != ResultConfirmationRequired {
		t.Fatalf("clear = %+v, %v", res, err)
	}
	if s.caller.countOp(control.OpProposalsClear) != 0 {
		t.Fatal("clear executed before confirmation")
	}
	if p := s.ctl.View().Pending; p == nil || p.Label != "Clear decided/expired proposals" {
		t.Fatalf("clear pending = %+v", p)
	}
	if _, err := s.ctl.Confirm(ctx); err != nil {
		t.Fatalf("confirm clear: %v", err)
	}
	if s.caller.countOp(control.OpProposalsClear) != 1 {
		t.Fatal("clear did not reach the socket once")
	}
	if got := s.ctl.View().LastAction; got != "Cleared 2 proposals" {
		t.Fatalf("last action after clear = %q", got)
	}
	if _, err := s.ctl.Confirm(ctx); !errors.Is(err, ErrNoPending) {
		t.Fatalf("second confirm = %v", err)
	}
	if s.caller.countOp(control.OpProposalsClear) != 1 {
		t.Fatal("second confirm repeated the clear")
	}
}

func TestMaliciousProposalAndRuleIDsStayOutOfKeysAndConfirmLabels(t *testing.T) {
	bad := "id-\x1b[2J\u202e\r\n" + strings.Repeat("z", 100)
	fx := newFixture()
	fx.rules[0].ID = contract.RuleID(bad)
	fx.proposals[0].ID = contract.ProposalID("p-" + bad)
	fx.proposals[0].Kind = contract.ProposalUnroute
	fx.proposals[0].RuleID = contract.RuleID(bad)
	s := newSetup(t, fx, nil)
	ctx := context.Background()

	walk(s.ctl.View().Root, func(n Node) {
		if containsControl(n.Key) {
			t.Fatalf("unsafe menu key: %q", n.Key)
		}
		if containsControl(n.Label) || runeLen(n.Label) > LabelLimit {
			t.Fatalf("unsafe label at %q: %q", n.Key, n.Label)
		}
	})

	if _, err := s.ctl.Trigger(ctx, ActionRuleRemove, Target{RuleID: contract.RuleID(bad)}); err != nil {
		t.Fatal(err)
	}
	pending := s.ctl.View().Pending
	if pending == nil {
		t.Fatal("no pending confirmation")
	}
	if containsControl(pending.Label) || runeLen(pending.Label) > LabelLimit {
		t.Fatalf("unsafe confirmation label: %q", pending.Label)
	}
	if _, err := s.ctl.Confirm(ctx); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, req := range s.caller.requests() {
		if req.Op == control.OpRulesRemove {
			found = true
			if req.ID != bad {
				t.Fatalf("confirm sent %q, want the stored id", req.ID)
			}
		}
	}
	if !found {
		t.Fatal("confirm did not send the stored rule id")
	}
}

func TestStaleProposalConfirmationKeepsTheSelectedTarget(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	if _, err := s.ctl.Trigger(ctx, ActionProposalAccept, Target{ProposalID: "p-unroute-1"}); err != nil {
		t.Fatal(err)
	}

	// The proposal list refreshes with the selected proposal gone and another
	// pending entry first. Confirm must keep the stored target, not the new row.
	s.caller.mu.Lock()
	s.caller.fx.proposals = []control.ProposalView{
		{ID: "p-unroute-9", Kind: contract.ProposalUnroute, RuleID: "r-009", Status: contract.ProposalPending},
	}
	s.caller.mu.Unlock()
	if err := s.ctl.RefreshLists(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ctl.Confirm(ctx); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	reqs := s.caller.requests()
	accepts := 0
	for _, req := range reqs {
		if req.Op == control.OpProposalsAccept {
			accepts++
			if req.ID != "p-unroute-1" {
				t.Fatalf("stale confirm accepted %q, want the selected p-unroute-1", req.ID)
			}
		}
	}
	if accepts != 1 {
		t.Fatalf("accept requests = %d, want 1", accepts)
	}
}

func TestExpiredConfirmationNeverReachesTheSocket(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	if _, err := s.ctl.Trigger(ctx, ActionProposalAccept, Target{ProposalID: "p-unroute-1"}); err != nil {
		t.Fatal(err)
	}
	s.clock.Advance(61 * time.Second)
	if _, err := s.ctl.Confirm(ctx); !errors.Is(err, ErrNoPending) {
		t.Fatalf("confirm after expiry = %v", err)
	}
	if s.caller.countOp(control.OpProposalsAccept) != 0 {
		t.Fatal("an expired confirmation reached the socket")
	}
}

func TestProposalActionsNeverPutDestinationsInOutcomeText(t *testing.T) {
	const canary = "d21-canary.example"
	fx := newFixture()
	fx.proposals = []control.ProposalView{
		{ID: "p-d21-accept", Kind: contract.ProposalRoute, Destination: contract.Destination{Kind: contract.DestDomain, Host: canary, Port: 443}, Status: contract.ProposalPending},
		{ID: "p-d21-reject", Kind: contract.ProposalRoute, Destination: contract.Destination{Kind: contract.DestDomain, Host: canary, Port: 443}, Status: contract.ProposalPending},
		{ID: "p-d21-recheck", Kind: contract.ProposalRoute, Destination: contract.Destination{Kind: contract.DestDomain, Host: canary, Port: 443}, Status: contract.ProposalPending},
		{ID: "p-d21-unroute", Kind: contract.ProposalUnroute, RuleID: "r-000", Destination: contract.Destination{Kind: contract.DestDomain, Host: canary, Port: 443}, Status: contract.ProposalPending},
	}
	s := newSetup(t, fx, nil)
	ctx := context.Background()

	var texts []string
	record := func() {
		view := s.ctl.View()
		texts = append(texts, view.LastAction)
	}
	step := func(action Action, target Target, confirm bool) {
		t.Helper()
		res, err := s.ctl.Trigger(ctx, action, target)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		texts = append(texts, res.Message)
		if confirm {
			res, err := s.ctl.Confirm(ctx)
			if err != nil {
				t.Fatalf("%s confirm: %v", action, err)
			}
			texts = append(texts, res.Message)
		}
	}
	step(ActionProposalAccept, Target{ProposalID: "p-d21-accept"}, false)
	record()
	step(ActionProposalReject, Target{ProposalID: "p-d21-reject"}, false)
	record()
	step(ActionProposalRecheck, Target{ProposalID: "p-d21-recheck"}, false)
	waitFor(t, "recheck job", func() bool { return s.ctl.View().Job == nil })
	record()
	step(ActionProposalAccept, Target{ProposalID: "p-d21-unroute"}, true)
	record()
	step(ActionProposalsClear, Target{}, true)
	record()

	for _, note := range s.notifier.all() {
		texts = append(texts, note.Title, note.Body)
	}
	for _, text := range texts {
		lower := strings.ToLower(text)
		for _, forbidden := range []string{"d21-canary", "example", "http", "://", "secret"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("outcome text leaked %q: %q", forbidden, text)
			}
		}
	}
	// D21 allows the destination only as the live in-memory menu view.
	if snap := s.ctl.Snapshot(); len(snap.Proposals) != 4 || snap.Proposals[0].Destination == "" {
		t.Fatalf("live proposal view lost its destination: %+v", snap.Proposals)
	}
}

func TestProposalGroupingSeparatesPendingFromDecided(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	view := s.ctl.View()
	if n, _ := findNode(view.Root, "proposals"); n.Label != "Proposals (3)" {
		t.Fatalf("pending count label = %q", n.Label)
	}
	if n, _ := findNode(view.Root, "proposals.decided"); n.Label != "Show decided (2)" {
		t.Fatalf("decided count label = %q", n.Label)
	}
	pending, ok := findNode(view.Root, "proposals")
	if !ok {
		t.Fatal("no proposals submenu")
	}
	pendingKeys := make(map[string]bool)
	for _, child := range pending.Children {
		pendingKeys[child.Key] = true
	}
	for _, key := range []string{"proposals.pending.p-route-1", "proposals.pending.p-route-2", "proposals.pending.p-unroute-1"} {
		if !pendingKeys[key] {
			t.Fatalf("pending group is missing %q", key)
		}
	}
	if pendingKeys["proposals.decided.p-accepted"] {
		t.Fatal("accepted proposal is in the pending group")
	}
	decided, _ := findNode(view.Root, "proposals.decided")
	var decidedLabels []string
	for _, child := range decided.Children {
		decidedLabels = append(decidedLabels, child.Key)
	}
	joined := strings.Join(decidedLabels, ",")
	if !strings.Contains(joined, "p-accepted") || !strings.Contains(joined, "p-expired") {
		t.Fatalf("decided group keys = %v", decidedLabels)
	}
	if strings.Contains(joined, "p-route-1") {
		t.Fatalf("pending proposal is in the decided group: %v", decidedLabels)
	}
}

func TestConcurrentProposalConfirmAndRefresh(t *testing.T) {
	s := newSetup(t, newFixture(), nil)
	ctx := context.Background()
	if _, err := s.ctl.Trigger(ctx, ActionProposalAccept, Target{ProposalID: "p-unroute-1"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_ = s.ctl.RefreshLists(ctx)
				return
			}
			_ = s.ctl.View()
		}(i)
	}
	var confirmed error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, confirmed = s.ctl.Confirm(ctx)
	}()
	wg.Wait()
	if confirmed != nil && !errors.Is(confirmed, ErrNoPending) {
		t.Fatalf("confirm = %v", confirmed)
	}
	if got := s.caller.countOp(control.OpProposalsAccept); got != 1 {
		t.Fatalf("accept requests = %d, want exactly 1", got)
	}
	for _, req := range s.caller.requests() {
		if req.Op == control.OpProposalsAccept && req.ID != "p-unroute-1" {
			t.Fatalf("concurrent confirm accepted %q", req.ID)
		}
	}
}
