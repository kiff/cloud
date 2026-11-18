package billing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/tenant"
)

// fakeSubs is Stripe's subscriptions as they are now.
type fakeSubs struct {
	subs map[string]billing.Subscription
	err  error
}

func (f *fakeSubs) Subscription(_ context.Context, id string) (billing.Subscription, error) {
	if f.err != nil {
		return billing.Subscription{}, f.err
	}
	return f.subs[id], nil
}

func sub(id, tenantID, status, priceKey string, quantity int, trialEnd, start, end time.Time) billing.Subscription {
	s := billing.Subscription{ID: id, Customer: "cus_1", Status: status, Metadata: map[string]string{}}
	if tenantID != "" {
		s.Metadata[billing.MetaTenant] = tenantID
	}
	if !trialEnd.IsZero() {
		s.TrialEnd = trialEnd.Unix()
	}
	item := billing.SubscriptionItem{Quantity: quantity}
	item.Price.LookupKey = priceKey
	if !start.IsZero() {
		// The newer API keeps the period on the item.
		item.CurrentPeriodStart, item.CurrentPeriodEnd = start.Unix(), end.Unix()
	}
	s.Items.Data = []billing.SubscriptionItem{item}
	return s
}

func subEvent(id, typ, subID string) billing.Event {
	var e billing.Event
	e.ID, e.Type = id, typ
	e.Data.Object.ID = subID
	return e
}

// The v1 lifecycle: the plan is set from the
// subscription as Stripe has it now, whatever the order of the events.
func TestSubscriptionsSetThePlan(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	store, tt := newTenantStoreWith(t, "acme")
	subs := &fakeSubs{subs: map[string]billing.Subscription{}}
	bs := billing.NewMemoryStore()
	p := billing.NewProcessor(bs, store, nil).WithSubscriptions(subs).WithClock(func() time.Time { return now })

	// The checkout KIFF opened names the account, and its subscription
	// starts on the 7-day trial.
	trialEnd := now.AddDate(0, 0, 7)
	trialing := sub("sub_1", tt.ID, "trialing", billing.PriceSoloMonthly, 1, trialEnd, time.Time{}, time.Time{})
	trialing.TrialStart = now.Unix()
	subs.subs["sub_1"] = trialing
	var done billing.Event
	done.ID, done.Type = "evt_checkout", "checkout.session.completed"
	done.Data.Object.Customer, done.Data.Object.Subscription = "cus_1", "sub_1"
	done.Data.Object.Metadata = map[string]string{billing.MetaTenant: tt.ID}
	if res, err := p.Process(ctx, done); err != nil || res.Action != "trialing" || res.TenantID != tt.ID {
		t.Fatalf("checkout: %+v %v", res, err)
	}
	if got := planOf(t, store, tt.ID); got.Canonical != tenant.PlanTrial {
		t.Fatalf("plan: %s", got.Canonical)
	}
	if got := metaOf(t, store, tt.ID, tenant.MetadataKeyTrialEndsAt); got != trialEnd.Format(time.RFC3339) {
		t.Fatalf("trial ends: %q", got)
	}
	if got := metaOf(t, store, tt.ID, tenant.MetadataKeyTrialStartedAt); got != now.Format(time.RFC3339) {
		t.Fatalf("trial started: %q", got)
	}
	// A later trialing event keeps the trial's start, and so its counts.
	trialing.TrialStart = now.Add(time.Hour).Unix()
	subs.subs["sub_1"] = trialing
	if _, err := p.Process(ctx, subEvent("evt_trialing_again", "customer.subscription.updated", "sub_1")); err != nil {
		t.Fatal(err)
	}
	if got := metaOf(t, store, tt.ID, tenant.MetadataKeyTrialStartedAt); got != now.Format(time.RFC3339) {
		t.Fatalf("trial start moved: %q", got)
	}
	if got, _ := bs.TenantForCustomer(ctx, "cus_1"); got != tt.ID {
		t.Fatalf("the customer isn't linked: %q", got)
	}

	// The trial becomes Solo, with Stripe's period.
	start, end := trialEnd, trialEnd.AddDate(0, 1, 0)
	subs.subs["sub_1"] = sub("sub_1", tt.ID, "active", billing.PriceSoloMonthly, 1, trialEnd, start, end)
	if res, err := p.Process(ctx, subEvent("evt_2", "customer.subscription.updated", "sub_1")); err != nil || res.Action != "solo" {
		t.Fatalf("active: %+v %v", res, err)
	}
	got, _ := store.Get(ctx, tt.ID)
	if from, to := tenant.Period(got, start.Add(time.Hour)); !from.Equal(start) || !to.Equal(end) || tenant.PlanFor(got).Canonical != tenant.PlanSolo {
		t.Fatalf("solo: %s [%v, %v)", tenant.PlanFor(got).Canonical, from, to)
	}

	// Team (Company), with its seats; never below 1.
	for quantity, want := range map[int]int{3: 3, 1: 1, 0: 1} {
		subs.subs["sub_1"] = sub("sub_1", tt.ID, "active", billing.PriceTeamMonthly, quantity, time.Time{}, start, end)
		if _, err := p.Process(ctx, subEvent("evt_team_"+string(rune('0'+quantity)), "customer.subscription.updated", "sub_1")); err != nil {
			t.Fatal(err)
		}
		if l := planOf(t, store, tt.ID); l.Canonical != tenant.PlanTeam || l.Seats != want || l.IncludedActions != 500*want {
			t.Fatalf("team of %d: %+v", quantity, l)
		}
	}

	// Cancelled: no plan follows, so the account is on an ended trial.
	subs.subs["sub_1"] = sub("sub_1", tt.ID, "canceled", billing.PriceTeamMonthly, 3, time.Time{}, start, end)
	if res, err := p.Process(ctx, subEvent("evt_del", "customer.subscription.deleted", "sub_1")); err != nil || res.Action != "ended" {
		t.Fatalf("deleted: %+v %v", res, err)
	}
	got, _ = store.Get(ctx, tt.ID)
	if ends, ok := tenant.TrialEndsAt(got); tenant.PlanFor(got).Canonical != tenant.PlanTrial || !ok || !ends.Equal(now) {
		t.Fatalf("ended: %s %v", tenant.PlanFor(got).Canonical, ends)
	}
	// A stale event delivered late reads the subscription as it is now,
	// so it can't bring the plan back.
	if _, err := p.Process(ctx, subEvent("evt_late", "customer.subscription.updated", "sub_1")); err != nil {
		t.Fatal(err)
	}
	if planOf(t, store, tt.ID).Canonical != tenant.PlanTrial {
		t.Fatal("a late event revived the plan")
	}

	// An unfinished checkout changes nothing.
	subs.subs["sub_2"] = sub("sub_2", tt.ID, "incomplete", billing.PriceSoloMonthly, 1, time.Time{}, time.Time{}, time.Time{})
	if res, err := p.Process(ctx, subEvent("evt_inc", "customer.subscription.created", "sub_2")); err != nil || !res.Ignored {
		t.Fatalf("incomplete: %+v %v", res, err)
	}

	// Stripe unreadable: an error, so Stripe retries, and the event isn't
	// marked seen.
	subs.err = errors.New("stripe down")
	if _, err := p.Process(ctx, subEvent("evt_down", "customer.subscription.updated", "sub_1")); err == nil {
		t.Fatal("a failed read was accepted")
	}
	if seen, _ := bs.Seen(ctx, "evt_down"); seen {
		t.Fatal("a failed event was marked seen")
	}
}

// Solo → Team on an account Apple bills: Stripe
// calls the credited Solo days a trial, and the account runs as Team on
// them, billed by Stripe from then on, with no warning of paying twice.
func TestTeamOnCreditedAppleDaysRunsAsTeam(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	store, tt := newTenantStoreWith(t, "acme")
	for k, v := range map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "apple", billing.MetaBillingStatus: "active"} {
		if err := store.SetMetadata(ctx, tt.ID, k, v); err != nil {
			t.Fatal(err)
		}
	}
	paidUntil := now.AddDate(0, 0, 12)
	credited := sub("sub_team", tt.ID, "trialing", billing.PriceTeamMonthly, 3, paidUntil, time.Time{}, time.Time{})
	credited.Metadata[billing.MetaAppleCredit] = "1"
	subs := &fakeSubs{subs: map[string]billing.Subscription{"sub_team": credited}}
	p := billing.NewProcessor(billing.NewMemoryStore(), store, nil).WithSubscriptions(subs).WithClock(func() time.Time { return now })
	if res, err := p.Process(ctx, subEvent("evt_team", "customer.subscription.created", "sub_team")); err != nil || res.Action != "team_credited" {
		t.Fatalf("credited: %+v %v", res, err)
	}
	if got := planOf(t, store, tt.ID); got.Canonical != tenant.PlanTeam {
		t.Fatalf("plan: %s", got.Canonical)
	}
	for key, want := range map[string]string{tenant.MetadataKeyBillingSource: "stripe", billing.MetaBillingStatus: "trialing",
		tenant.MetadataKeyPeriodEnd: paidUntil.Format(time.RFC3339)} {
		if got := metaOf(t, store, tt.ID, key); got != want {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
	// A plain trial on Team, with no credit, is still the trial.
	plain := sub("sub_plain", tt.ID, "trialing", billing.PriceTeamMonthly, 2, paidUntil, time.Time{}, time.Time{})
	subs.subs["sub_plain"] = plain
	if res, err := p.Process(ctx, subEvent("evt_plain", "customer.subscription.updated", "sub_plain")); err != nil || res.Action != "trialing" {
		t.Fatalf("plain trial: %+v %v", res, err)
	}
}
