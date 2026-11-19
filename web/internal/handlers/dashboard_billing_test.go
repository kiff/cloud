package handlers_test

import (
	"strings"
	"testing"
)

// TestSettingsBilling_PastDueBanner: a past_due tenant sees the
// reassuring grace banner (service unaffected) and the Manage
// billing action (it has a Stripe customer).
func TestSettingsBilling_PastDueBanner(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Me.Plan = "growth"
	v.Me.CanonicalPlan = "growth"
	v.Me.BillingStatus = "past_due"
	v.Me.HasBilling = true

	body := renderSettings(t, v)

	for _, want := range []string{
		"couldn't process your last payment",
		"service is unaffected",
		"Manage billing",
		`action="/dashboard/billing/portal"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("past_due settings missing %q", want)
		}
	}
}

// TestSettingsBilling_CanceledNotice: a canceled tenant sees the
// downgrade notice.
func TestSettingsBilling_CanceledNotice(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Me.CanonicalPlan = "developer"
	v.Me.BillingStatus = "canceled"
	v.Me.HasBilling = true

	body := renderSettings(t, v)
	if !strings.Contains(body, "subscription was canceled") {
		t.Errorf("canceled settings missing the downgrade notice")
	}
}

// TestSettingsBilling_ActivePaidNoBanner: an active paid tenant sees
// Manage billing but no banner.
func TestSettingsBilling_ActivePaidNoBanner(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Me.CanonicalPlan = "growth"
	v.Me.BillingStatus = "active"
	v.Me.HasBilling = true

	body := renderSettings(t, v)
	if !strings.Contains(body, "Manage billing") {
		t.Error("active paid tenant should see Manage billing")
	}
	for _, banned := range []string{"couldn't process your last payment", "subscription was canceled"} {
		if strings.Contains(body, banned) {
			t.Errorf("active tenant should show no billing banner, found %q", banned)
		}
	}
}

// TestSettingsBilling_NoBillingHidesAction: a tenant with no Stripe
// customer sees neither a banner nor the Manage billing action.
func TestSettingsBilling_NoBillingHidesAction(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Me.CanonicalPlan = "developer"
	// BillingStatus empty, HasBilling false (never subscribed).

	body := renderSettings(t, v)
	if strings.Contains(body, "Manage billing") {
		t.Error("a tenant with no billing account must not see Manage billing")
	}
	if strings.Contains(body, `action="/dashboard/billing/portal"`) {
		t.Error("portal form must not render without a billing account")
	}
}
