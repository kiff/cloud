package receipt

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStore_ListRecentAcrossTenants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := NewMemoryStore()
	now := time.Now().UTC()

	mk := func(tenantID, traceID string, issued time.Time) Receipt {
		return Receipt{
			SchemaVersion: SchemaVersion,
			TraceID:       traceID,
			TenantID:      tenantID,
			EntityID:      "ent",
			IssuedAt:      issued,
		}
	}
	if err := s.Save(ctx, mk("t1", "a", now.Add(-3*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, mk("t2", "b", now.Add(-1*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, mk("t1", "c", now.Add(-2*time.Hour))); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListRecentAcrossTenants(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecentAcrossTenants: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d receipts, want 3", len(got))
	}
	// Newest first across tenants: b (-1h), c (-2h), a (-3h).
	wantOrder := []string{"b", "c", "a"}
	for i, w := range wantOrder {
		if got[i].TraceID != w {
			t.Errorf("position %d: got %s, want %s", i, got[i].TraceID, w)
		}
	}

	// Limit is respected.
	limited, err := s.ListRecentAcrossTenants(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Fatalf("limit 2: got %d", len(limited))
	}
}
