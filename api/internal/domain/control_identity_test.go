package domain_test

import (
	"context"
	"testing"

	"github.com/kiff/cloud/api/internal/domain"
)

// A control created without an explicit environment/workflow gets the
// package defaults, so every persisted control is addressable.
func TestControl_IdentityDefaultsApplied(t *testing.T) {
	store := domain.NewMemoryControlStore()
	ctx := context.Background()
	if err := store.Create(ctx, domain.Control{
		ID: "c1", TenantID: "t1", Domain: "refund", Entity: "Order",
		Actions: []string{"REFUND_ORDER"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := store.List(ctx, "t1")
	if err != nil || len(got) != 1 {
		t.Fatalf("list: %v (n=%d)", err, len(got))
	}
	if got[0].Environment != domain.DefaultControlEnvironment {
		t.Errorf("environment: got %q want %q", got[0].Environment, domain.DefaultControlEnvironment)
	}
	if got[0].Workflow != domain.DefaultControlWorkflow {
		t.Errorf("workflow: got %q want %q", got[0].Workflow, domain.DefaultControlWorkflow)
	}
}

// FindActiveControl addresses by (action, environment): the same action
// under two environments resolves to distinct controls, and an empty
// environment matches the single active control (back-compat).
func TestFindActiveControl_AddressesByEnvironment(t *testing.T) {
	store := domain.NewMemoryControlStore()
	ctx := context.Background()
	// Two active controls for the same action in different environments.
	// (A real tenant reaches this once multi-environment activation lands;
	// the store allows it and selection must disambiguate.)
	mustCreate(t, store, domain.Control{
		ID: "prod", TenantID: "t1", Domain: "refund", Entity: "Order",
		Actions: []string{"REFUND_ORDER"}, Environment: "prod", Status: "active",
	})
	mustCreate(t, store, domain.Control{
		ID: "staging", TenantID: "t1", Domain: "refund", Entity: "Order",
		Actions: []string{"REFUND_ORDER"}, Environment: "staging", Status: "active",
	})

	prod, err := domain.FindActiveControl(ctx, store, "t1", "REFUND_ORDER", "prod")
	if err != nil || prod.Environment != "prod" {
		t.Fatalf("prod lookup: %v env=%q", err, prod.Environment)
	}
	staging, err := domain.FindActiveControl(ctx, store, "t1", "REFUND_ORDER", "staging")
	if err != nil || staging.Environment != "staging" {
		t.Fatalf("staging lookup: %v env=%q", err, staging.Environment)
	}
	// Unknown environment for a declared action is not found.
	if _, err := domain.FindActiveControl(ctx, store, "t1", "REFUND_ORDER", "qa"); err != domain.ErrControlNotFound {
		t.Errorf("qa lookup: got %v want ErrControlNotFound", err)
	}
	// Unknown action is not found.
	if _, err := domain.FindActiveControl(ctx, store, "t1", "NOPE", ""); err != domain.ErrControlNotFound {
		t.Errorf("unknown action: got %v want ErrControlNotFound", err)
	}
}

// Empty environment matches the single active control — the
// single-control tenant path is unchanged by G0.
func TestFindActiveControl_EmptyEnvironmentBackCompat(t *testing.T) {
	store := domain.NewMemoryControlStore()
	ctx := context.Background()
	mustCreate(t, store, domain.Control{
		ID: "only", TenantID: "t1", Domain: "refund", Entity: "Order",
		Actions: []string{"REFUND_ORDER"}, Status: "active",
	})
	c, err := domain.FindActiveControl(ctx, store, "t1", "REFUND_ORDER", "")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if c.ID != "only" || c.Environment != domain.DefaultControlEnvironment {
		t.Errorf("got id=%q env=%q", c.ID, c.Environment)
	}
}

func mustCreate(t *testing.T, store *domain.MemoryControlStore, c domain.Control) {
	t.Helper()
	if err := store.Create(context.Background(), c); err != nil {
		t.Fatalf("create %s: %v", c.ID, err)
	}
}

// Version is monotonic per (tenant, environment) lineage: first
// activation is v1, each re-activation increments, and environments
// version independently.
func TestControl_VersionMonotonicPerEnvironment(t *testing.T) {
	store := domain.NewMemoryControlStore()
	ctx := context.Background()

	mustCreate(t, store, domain.Control{ID: "p1", TenantID: "t1", Domain: "d", Entity: "E", Actions: []string{"A"}, Environment: "prod"})
	mustCreate(t, store, domain.Control{ID: "p2", TenantID: "t1", Domain: "d", Entity: "E", Actions: []string{"A"}, Environment: "prod"})
	mustCreate(t, store, domain.Control{ID: "s1", TenantID: "t1", Domain: "d", Entity: "E", Actions: []string{"A"}, Environment: "staging"})

	list, err := store.List(ctx, "t1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[string]domain.Control{}
	for _, c := range list {
		byID[c.ID] = c
	}
	if byID["p1"].Version != 1 {
		t.Errorf("prod first activation: version %d, want 1", byID["p1"].Version)
	}
	if byID["p2"].Version != 2 {
		t.Errorf("prod re-activation: version %d, want 2", byID["p2"].Version)
	}
	if byID["p1"].Status != "superseded" || byID["p2"].Status != "active" {
		t.Errorf("supersession wrong: p1=%s p2=%s", byID["p1"].Status, byID["p2"].Status)
	}
	// staging versions independently of prod.
	if byID["s1"].Version != 1 {
		t.Errorf("staging first activation: version %d, want 1 (independent of prod)", byID["s1"].Version)
	}
	// Historical p1 keeps its original version after p2 supersedes it.
	if byID["p1"].Version != 1 {
		t.Errorf("historical version mutated: p1 version %d, want 1", byID["p1"].Version)
	}
}
