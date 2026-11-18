package billing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/tenant"
)

func newTenantStoreWith(t *testing.T, slug string) (tenant.Store, tenant.Tenant) {
	t.Helper()
	store := tenant.NewMemoryStore()
	tt := tenant.Tenant{ID: tenant.NewID(), Slug: slug, Status: tenant.StatusActive}
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	return store, tt
}

func planOf(t *testing.T, store tenant.Store, id string) tenant.PlanLimits {
	t.Helper()
	tt, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get tenant: %v", err)
	}
	return tenant.PlanFor(tt)
}

func metaOf(t *testing.T, store tenant.Store, id, key string) string {
	t.Helper()
	tt, _ := store.Get(context.Background(), id)
	if v, ok := tt.Metadata[key].(string); ok {
		return v
	}
	return ""
}

// failingOnceTenantStore wraps a tenant.Store and fails the first N
// SetMetadata calls, to simulate a transient apply failure mid-event.
type failingOnceTenantStore struct {
	tenant.Store
	failsLeft int
}

func (f *failingOnceTenantStore) SetMetadata(ctx context.Context, id, key string, value any) error {
	if f.failsLeft > 0 {
		f.failsLeft--
		return errors.New("simulated apply failure")
	}
	return f.Store.SetMetadata(ctx, id, key, value)
}

// TestProcessor_ApplyFailureIsRetryableNotDeduped is a review
// regression: mark-after-apply must NOT silently dedupe a retry after
// a transient apply failure. First delivery fails the metadata write
// (→ error → handler 500 → Stripe retries); the second delivery must
// re-apply and succeed, not be swallowed as a duplicate.
func TestProcessor_ApplyFailureIsRetryableNotDeduped(t *testing.T) {
	t.Parallel()
	base, tt := newTenantStoreWith(t, "acme")
	failing := &failingOnceTenantStore{Store: base, failsLeft: 1}
	bs := billing.NewMemoryStore()
	p := billing.NewProcessor(bs, failing, nil)

	ev := checkoutEvent("evt_retry", "solo", "acme", "cus_r", "sub_r")

	// First delivery: apply fails → Process returns an error and must
	// NOT have marked the event processed.
	if _, err := p.Process(context.Background(), ev); err == nil {
		t.Fatal("first delivery: expected apply error, got nil")
	}
	if seen, _ := bs.Seen(context.Background(), "evt_retry"); seen {
		t.Fatal("event was marked processed despite apply failure — retry would be silently deduped")
	}
	if got := planOf(t, base, tt.ID); got.Canonical != tenant.PlanTrial {
		t.Errorf("plan = %s, want trial (apply did not complete)", got.Canonical)
	}

	// Second delivery (Stripe retry): apply now succeeds.
	res, err := p.Process(context.Background(), ev)
	if err != nil {
		t.Fatalf("retry delivery: %v", err)
	}
	if res.Duplicate {
		t.Fatal("retry was deduped — the lost-update bug")
	}
	if res.Action != "activated" {
		t.Errorf("res = %+v, want activated on retry", res)
	}
	if got := planOf(t, base, tt.ID); got.Canonical != tenant.PlanSolo {
		t.Errorf("plan = %s, want solo after successful retry", got.Canonical)
	}
}

func checkoutEvent(id, plan, slug, customer, sub string) billing.Event {
	var e billing.Event
	e.ID = id
	e.Type = "checkout.session.completed"
	e.Data.Object.Customer = customer
	e.Data.Object.Subscription = sub
	e.Data.Object.Metadata = map[string]string{"kiff_plan": plan}
	e.Data.Object.CustomFields = []struct {
		Key  string `json:"key"`
		Text struct {
			Value string `json:"value"`
		} `json:"text"`
	}{{Key: "tenant_slug"}}
	e.Data.Object.CustomFields[0].Text.Value = slug
	return e
}

func TestProcessor_ActivatesTenantFromCatalogMetadata(t *testing.T) {
	t.Parallel()
	store, tt := newTenantStoreWith(t, "acme")
	p := billing.NewProcessor(billing.NewMemoryStore(), store, nil)

	res, err := p.Process(context.Background(), checkoutEvent("evt_1", "solo", "acme", "cus_1", "sub_1"))
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if res.TenantID != tt.ID || res.Action != "activated" {
		t.Fatalf("res = %+v, want activated for %s", res, tt.ID)
	}
	if got := planOf(t, store, tt.ID); got.Canonical != tenant.PlanSolo {
		t.Errorf("plan = %s, want solo", got.Canonical)
	}
	if got := metaOf(t, store, tt.ID, billing.MetaStripeCustomerID); got != "cus_1" {
		t.Errorf("stripe_customer_id = %q, want cus_1", got)
	}
	if got := metaOf(t, store, tt.ID, billing.MetaBillingStatus); got != billing.StatusActive {
		t.Errorf("billing_status = %q, want active", got)
	}
}

// The plan must come from our catalog metadata, never the buyer. A
// session without kiff_plan cannot activate.
func TestProcessor_NoCatalogPlanIsUnmapped(t *testing.T) {
	t.Parallel()
	store, tt := newTenantStoreWith(t, "acme")
	p := billing.NewProcessor(billing.NewMemoryStore(), store, nil)

	res, err := p.Process(context.Background(), checkoutEvent("evt_x", "", "acme", "cus_1", "sub_1"))
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if !res.Unmapped {
		t.Errorf("res = %+v, want Unmapped", res)
	}
	if got := planOf(t, store, tt.ID); got.Canonical != tenant.PlanTrial {
		t.Errorf("plan = %s, want trial (no activation)", got.Canonical)
	}
}

func TestProcessor_UnknownSlugIsUnmappedNotError(t *testing.T) {
	t.Parallel()
	store, _ := newTenantStoreWith(t, "acme")
	p := billing.NewProcessor(billing.NewMemoryStore(), store, nil)

	res, err := p.Process(context.Background(), checkoutEvent("evt_2", "solo", "does-not-exist", "cus_9", "sub_9"))
	if err != nil {
		t.Fatalf("unmapped slug must not error: %v", err)
	}
	if !res.Unmapped {
		t.Errorf("res = %+v, want Unmapped", res)
	}
}

func TestProcessor_DuplicateEventIsNoOp(t *testing.T) {
	t.Parallel()
	store, tt := newTenantStoreWith(t, "acme")
	bs := billing.NewMemoryStore()
	p := billing.NewProcessor(bs, store, nil)

	if _, err := p.Process(context.Background(), checkoutEvent("evt_dup", "solo", "acme", "cus_1", "sub_1")); err != nil {
		t.Fatalf("first process: %v", err)
	}
	// Downgrade the tenant out-of-band, then redeliver the SAME event.
	_ = store.SetMetadata(context.Background(), tt.ID, tenant.MetadataKeyPlan, string(tenant.PlanTrial))

	res, err := p.Process(context.Background(), checkoutEvent("evt_dup", "solo", "acme", "cus_1", "sub_1"))
	if err != nil {
		t.Fatalf("second process: %v", err)
	}
	if !res.Duplicate {
		t.Errorf("res = %+v, want Duplicate", res)
	}
	// The duplicate must NOT re-activate (plan stays as we left it).
	if got := planOf(t, store, tt.ID); got.Canonical != tenant.PlanTrial {
		t.Errorf("duplicate re-applied plan = %s, want trial (no-op)", got.Canonical)
	}
}

// Payment failure enters grace: billing_status flips, the PLAN and
// thus the runtime limit are untouched (never a limit_exceeded
// outage from a billing problem).
func TestProcessor_PaymentFailedIsGraceNotDowngrade(t *testing.T) {
	t.Parallel()
	store, tt := newTenantStoreWith(t, "acme")
	bs := billing.NewMemoryStore()
	p := billing.NewProcessor(bs, store, nil)

	// Activate first so the customer is linked.
	if _, err := p.Process(context.Background(), checkoutEvent("evt_a", "solo", "acme", "cus_7", "sub_7")); err != nil {
		t.Fatalf("activate: %v", err)
	}

	var fail billing.Event
	fail.ID = "evt_fail"
	fail.Type = "invoice.payment_failed"
	fail.Data.Object.Customer = "cus_7"
	res, err := p.Process(context.Background(), fail)
	if err != nil {
		t.Fatalf("payment_failed: %v", err)
	}
	if res.Action != "grace" || res.TenantID != tt.ID {
		t.Fatalf("res = %+v, want grace for %s", res, tt.ID)
	}
	if got := metaOf(t, store, tt.ID, billing.MetaBillingStatus); got != billing.StatusPastDue {
		t.Errorf("billing_status = %q, want past_due", got)
	}
	// Plan unchanged — still Solo, with its included tasks.
	if got := planOf(t, store, tt.ID); got.Canonical != tenant.PlanSolo || got.IncludedTasks != 20 {
		t.Errorf("grace must not touch plan: got %s/%d", got.Canonical, got.IncludedTasks)
	}
}

func TestProcessor_SubscriptionDeletedDowngrades(t *testing.T) {
	t.Parallel()
	store, tt := newTenantStoreWith(t, "acme")
	bs := billing.NewMemoryStore()
	p := billing.NewProcessor(bs, store, nil)

	if _, err := p.Process(context.Background(), checkoutEvent("evt_a", "solo", "acme", "cus_3", "sub_3")); err != nil {
		t.Fatalf("activate: %v", err)
	}

	var del billing.Event
	del.ID = "evt_del"
	del.Type = "customer.subscription.deleted"
	del.Data.Object.Customer = "cus_3"
	res, err := p.Process(context.Background(), del)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.Action != "canceled" {
		t.Fatalf("res = %+v, want canceled", res)
	}
	if got := planOf(t, store, tt.ID); got.Canonical != tenant.PlanTrial {
		t.Errorf("plan = %s, want trial after cancel", got.Canonical)
	}
	if got := metaOf(t, store, tt.ID, billing.MetaBillingStatus); got != billing.StatusCanceled {
		t.Errorf("billing_status = %q, want canceled", got)
	}
}

// A lifecycle event for a customer we never linked is unmapped, not an
// error (no Stripe retry storm on a genuinely unknown customer).
func TestProcessor_LifecycleUnmappedCustomer(t *testing.T) {
	t.Parallel()
	store, _ := newTenantStoreWith(t, "acme")
	p := billing.NewProcessor(billing.NewMemoryStore(), store, nil)

	var del billing.Event
	del.ID = "evt_orphan"
	del.Type = "customer.subscription.deleted"
	del.Data.Object.Customer = "cus_unknown"
	res, err := p.Process(context.Background(), del)
	if err != nil {
		t.Fatalf("unmapped lifecycle must not error: %v", err)
	}
	if !res.Unmapped {
		t.Errorf("res = %+v, want Unmapped", res)
	}
}
