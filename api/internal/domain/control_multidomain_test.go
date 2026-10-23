package domain_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/tenant"
)

// TestControl_LineagePerDomain proves control lineage is
// scoped to (tenant, domain, environment), so two domains in the same
// environment each keep their own active control and version
// independently — activating one never supersedes the other.
func TestControl_LineagePerDomain(t *testing.T) {
	store := domain.NewMemoryControlStore()
	ctx := context.Background()

	// Same env (default) on purpose: the pre-#631 bug superseded
	// across domains because the lineage ignored the domain.
	mustCreate(t, store, domain.Control{ID: "a1", TenantID: "t1", Domain: "alpha", Entity: "E", Actions: []string{"A"}})
	mustCreate(t, store, domain.Control{ID: "b1", TenantID: "t1", Domain: "beta", Entity: "E", Actions: []string{"A"}})

	byID := func() map[string]domain.Control {
		list, err := store.List(ctx, "t1")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		m := map[string]domain.Control{}
		for _, c := range list {
			m[c.ID] = c
		}
		return m
	}

	m := byID()
	if m["a1"].Status != "active" {
		t.Errorf("alpha control should stay active after beta is created; got %s", m["a1"].Status)
	}
	if m["b1"].Status != "active" {
		t.Errorf("beta control should be active; got %s", m["b1"].Status)
	}
	if m["a1"].Version != 1 || m["b1"].Version != 1 {
		t.Errorf("each domain versions independently from 1; got alpha=%d beta=%d", m["a1"].Version, m["b1"].Version)
	}

	// Re-activate alpha: alpha v2 active, alpha v1 superseded, beta
	// untouched.
	mustCreate(t, store, domain.Control{ID: "a2", TenantID: "t1", Domain: "alpha", Entity: "E", Actions: []string{"A"}})
	m = byID()
	if m["a1"].Status != "superseded" || m["a2"].Status != "active" || m["a2"].Version != 2 {
		t.Errorf("alpha re-activation wrong: a1=%s a2=%s v=%d", m["a1"].Status, m["a2"].Status, m["a2"].Version)
	}
	if m["b1"].Status != "active" || m["b1"].Version != 1 {
		t.Errorf("beta must be untouched by alpha re-activation: b1=%s v=%d", m["b1"].Status, m["b1"].Version)
	}
}

// TestPostgresControl_LineagePerDomain is the same guarantee against
// Postgres, exercising the (tenant, domain, environment) supersede +
// version SQL and the domain-scoped one-active partial unique index.
func TestPostgresControl_LineagePerDomain(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	// Create the table this test writes to instead of relying on another
	// package having created it first in a shared database.
	if err := domain.ApplyTenantControlsSchema(ctx, pool); err != nil {
		t.Fatalf("ApplyTenantControlsSchema: %v", err)
	}
	truncate(t, pool)
	defer truncate(t, pool)

	store := tenant.NewPostgresStore(pool)
	tt := tenant.Tenant{ID: tenant.NewID(), Slug: "ctrl-md", DisplayName: "ctrl-md", Status: tenant.StatusProvisioning}
	if err := store.Create(ctx, tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	now := time.Now().UTC()
	if err := store.SetStatus(ctx, tt.ID, tenant.StatusActive, &now); err != nil {
		t.Fatalf("activate tenant: %v", err)
	}

	cs := domain.NewPostgresControlStore(pool)
	mk := func(id, dom string) domain.Control {
		return domain.Control{ID: id, TenantID: tt.ID, Domain: dom, Entity: "E", Actions: []string{"A"}}
	}
	if err := cs.Create(ctx, mk("a1", "alpha")); err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	// Both active in the same (default) environment — the pre-#631
	// one-active-per-env index would have rejected this insert.
	if err := cs.Create(ctx, mk("b1", "beta")); err != nil {
		t.Fatalf("create beta (two active domains, same env): %v", err)
	}

	list, err := cs.List(ctx, tt.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	active := map[string]int{}
	for _, c := range list {
		if c.Status == "active" {
			active[c.Domain] = c.Version
		}
	}
	if active["alpha"] != 1 || active["beta"] != 1 {
		t.Fatalf("want both domains active at v1; got %+v", active)
	}
}
