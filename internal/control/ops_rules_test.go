package control

import (
	"context"
	"reflect"
	"testing"
)

// TestAddRulePrependsAndSurvivesReload pins the stored newest-first contract:
// each approval becomes index 0 and a fresh service reads the same order back.
func TestAddRulePrependsAndSurvivesReload(t *testing.T) {
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	ctx := context.Background()

	for _, host := range []string{"old.example", "mid.example", "new.example"} {
		if _, err := svc.addRule(ctx, Request{Input: host}); err != nil {
			t.Fatalf("add %s: %v", host, err)
		}
	}

	wantOrder := []string{"new.example", "mid.example", "old.example"}
	assertRuleOrder(t, svc, wantOrder)

	reopened, err := openTestStore(paths)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := reopened.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := make([]string, len(snap.State.Rules))
	for i, rule := range snap.State.Rules {
		reloaded[i] = rule.Host
	}
	if !reflect.DeepEqual(reloaded, wantOrder) {
		t.Fatalf("reloaded order = %v, want %v", reloaded, wantOrder)
	}
}

// TestRemoveRulePreservesRemainingOrder proves removal does not reshuffle the
// newest-first list.
func TestRemoveRulePreservesRemainingOrder(t *testing.T) {
	st, paths := newServiceStore(t)
	svc := newTestService(t, st, paths)
	ctx := context.Background()

	for _, host := range []string{"old.example", "mid.example", "new.example"} {
		if _, err := svc.addRule(ctx, Request{Input: host}); err != nil {
			t.Fatalf("add %s: %v", host, err)
		}
	}
	list, err := svc.listRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var mid string
	for _, rule := range list.Rules {
		if rule.Host == "mid.example" {
			mid = string(rule.ID)
		}
	}
	if mid == "" {
		t.Fatal("fixture rule mid.example is missing")
	}
	if _, err := svc.removeRule(ctx, mid); err != nil {
		t.Fatal(err)
	}
	assertRuleOrder(t, svc, []string{"new.example", "old.example"})
}

func assertRuleOrder(t *testing.T, svc *Service, want []string) {
	t.Helper()
	list, err := svc.listRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(list.Rules))
	for i, rule := range list.Rules {
		got[i] = rule.Host
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rule order = %v, want %v", got, want)
	}
}
