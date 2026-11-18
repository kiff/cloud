package billing_test

import (
	"context"
	"testing"

	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/tenant"
)

type seatSetter struct{ set map[string]int }

func (s *seatSetter) SetSeats(_ context.Context, sub string, seats int) error {
	s.set[sub] = seats
	return nil
}

type peopleByTenant map[string][]membership.Member

func (p peopleByTenant) List(_ context.Context, id string) ([]membership.Member, error) {
	return p[id], nil
}

// Team's seats follow its admins and editors, at least 1; approvers and
// viewers are free, and other plans are left alone.
func TestSeatsFollowThePeopleWhoRunThings(t *testing.T) {
	ctx := context.Background()
	store := tenant.NewMemoryStore()
	mk := func(slug string, meta map[string]any) string {
		id := tenant.NewID()
		if err := store.Create(ctx, tenant.Tenant{ID: id, Slug: slug, Status: tenant.StatusActive, Metadata: meta}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	grew := mk("grew", map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeySeats: float64(2), billing.MetaStripeSubscriptionID: "sub_grew"})
	same := mk("same", map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeySeats: float64(1), billing.MetaStripeSubscriptionID: "sub_same"})
	solo := mk("solo", map[string]any{tenant.MetadataKeyPlan: "solo", billing.MetaStripeSubscriptionID: "sub_solo"})
	people := peopleByTenant{
		grew: {{Role: membership.RoleAdmin}, {Role: membership.RoleEditor}, {Role: membership.RoleEditor}, {Role: membership.RoleApprover}, {Role: membership.RoleViewer}},
		same: {{Role: membership.RoleAdmin}, {Role: membership.RoleApprover}},
		solo: {{Role: membership.RoleAdmin}},
	}
	set := &seatSetter{set: map[string]int{}}
	if err := billing.NewSeatsReconciler(store, people, set, nil).Once(ctx); err != nil {
		t.Fatal(err)
	}
	if len(set.set) != 1 || set.set["sub_grew"] != 3 {
		t.Fatalf("seats set: %v; want only sub_grew at 3", set.set)
	}
}
