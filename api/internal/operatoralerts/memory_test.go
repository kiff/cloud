package operatoralerts

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStore_UpsertDedupAndStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := NewMemoryStore()
	now := time.Now().UTC()

	a1, err := s.Upsert(ctx, Upsert{TenantID: "t1", TenantSlug: "acme", Kind: "blocked_spike", Severity: "high", Detail: "40% blocked"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if a1.Count != 1 || a1.Status != StatusOpen {
		t.Fatalf("first upsert: %+v", a1)
	}
	// Same (tenant, kind) → dedup, bump count.
	a2, _ := s.Upsert(ctx, Upsert{TenantID: "t1", Kind: "blocked_spike", Severity: "high", Detail: "45% blocked"}, now.Add(time.Hour))
	if a2.ID != a1.ID || a2.Count != 2 || a2.Detail != "45% blocked" {
		t.Fatalf("dedup upsert: %+v", a2)
	}
	// Different kind → new alert.
	if _, err := s.Upsert(ctx, Upsert{TenantID: "t1", Kind: "quota_pressure", Severity: "warn", Detail: "85% used"}, now); err != nil {
		t.Fatal(err)
	}

	open, _ := s.List(ctx, true, 10)
	if len(open) != 2 {
		t.Fatalf("open alerts = %d, want 2", len(open))
	}

	// Resolve one; it drops from the open list.
	if _, err := s.SetStatus(ctx, a1.ID, StatusResolved, "ops@kiff.dev", "fixed config"); err != nil {
		t.Fatal(err)
	}
	open, _ = s.List(ctx, true, 10)
	if len(open) != 1 {
		t.Fatalf("after resolve open = %d, want 1", len(open))
	}
	// Re-upsert the resolved condition → re-opens.
	a3, _ := s.Upsert(ctx, Upsert{TenantID: "t1", Kind: "blocked_spike", Severity: "high", Detail: "again"}, now.Add(2*time.Hour))
	if a3.Status != StatusOpen || a3.AckedBy != "" {
		t.Fatalf("re-open: %+v", a3)
	}
}

func TestMemoryStore_SetStatusNotFound(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	if _, err := s.SetStatus(context.Background(), "nope", StatusAcked, "x", ""); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
