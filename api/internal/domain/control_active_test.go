package domain_test

import (
	"context"
	"testing"

	"github.com/kiff/cloud/api/internal/domain"
)

// listCounting is a ControlStore that counts full List reads.
type listCounting struct {
	domain.ControlStore
	lists int
}

func (s *listCounting) List(ctx context.Context, tenantID string) ([]domain.Control, error) {
	s.lists++
	return s.ControlStore.List(ctx, tenantID)
}

// activeOnly is listCounting with the store's own active and by-id reads.
type activeOnly struct{ *listCounting }

func (s activeOnly) ListActive(ctx context.Context, tenantID string) ([]domain.Control, error) {
	all, err := s.ControlStore.List(ctx, tenantID)
	var out []domain.Control
	for _, c := range all {
		if c.Status == "active" {
			out = append(out, c)
		}
	}
	return out, err
}

func (s activeOnly) GetControl(ctx context.Context, tenantID, id string) (domain.Control, error) {
	all, _ := s.ControlStore.List(ctx, tenantID)
	for _, c := range all {
		if c.ID == id {
			return c, nil
		}
	}
	return domain.Control{}, domain.ErrControlNotFound
}

func seeded(t *testing.T) domain.ControlStore {
	t.Helper()
	m := domain.NewMemoryControlStore()
	for _, id := range []string{"v1", "v2"} { // v1 superseded by v2
		if err := m.Create(context.Background(), domain.Control{ID: id, TenantID: "t", Domain: "d", Entity: "E", Actions: []string{"A"}, Environment: "prod"}); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

// A store with its own active read is never listed in full on the decision
// path; one without falls back to List, filtered to the active controls.
func TestActiveControls_UseTheStoresActiveRead(t *testing.T) {
	ctx := context.Background()
	with := activeOnly{&listCounting{ControlStore: seeded(t)}}
	if c, err := domain.FindActiveControl(ctx, with, "t", "A", "prod"); err != nil || c.ID != "v2" {
		t.Fatalf("FindActiveControl = %v, %v", c.ID, err)
	}
	if c, err := domain.FindControlByID(ctx, with, "t", "v1"); err != nil || c.Status != "superseded" {
		t.Fatalf("FindControlByID(v1) = %+v, %v", c, err)
	}
	if with.lists != 0 {
		t.Fatalf("the decision path listed every version %d time(s)", with.lists)
	}

	without := &listCounting{ControlStore: seeded(t)}
	active, err := domain.ActiveControls(ctx, without, "t")
	if err != nil || len(active) != 1 || active[0].ID != "v2" {
		t.Fatalf("fallback ActiveControls = %+v, %v", active, err)
	}
	if without.lists != 1 {
		t.Fatalf("fallback lists = %d, want 1", without.lists)
	}
}
