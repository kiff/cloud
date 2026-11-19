package handlers_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
)

// renderDomainHome renders the domain-home surface (DashboardDomains
// with a selected domain) against a buffer. After the Overview retired,
// the domain home is where the next-action card and the
// governed-activity glance live, so the shell + folded-card tests
// render through it.
func renderDomainHome(t *testing.T, v pages.DashboardDomainsView) string {
	t.Helper()
	var buf bytes.Buffer
	if err := pages.DashboardDomains(v).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render domain home: %v", err)
	}
	return buf.String()
}

// freshDomainHomeView is the domain-home counterpart to
// freshDashboardView: one selected domain so the home renders its spine
// (domain header, activity glance, folded next-action) rather than the
// list or empty states.
func freshDomainHomeView() pages.DashboardDomainsView {
	return pages.DashboardDomainsView{
		Email:       "alice@example.com",
		Subject:     "alice",
		DisplayName: "Alice",
		Me: &apiclient.Me{
			Subject:    "alice",
			Email:      "alice@example.com",
			TenantID:   "ten-test",
			TenantSlug: "alice-team",
			Plan:       "free_dev",
		},
		Selected: &apiclient.DomainDetail{
			Name:   "duplicate-payment",
			Entity: "Invoice",
			Status: "active",
		},
		Usage: &apiclient.Usage{
			TenantID: "ten-test",
			Counters: map[string]uint64{},
		},
	}
}

func freshDashboardView() pages.DashboardView {
	return pages.DashboardView{
		Email:       "alice@example.com",
		Subject:     "alice",
		DisplayName: "Alice",
		Me: &apiclient.Me{
			Subject:              "alice",
			Email:                "alice@example.com",
			TenantID:             "ten-test",
			TenantSlug:           "alice-team",
			DisplayName:          "Alice's Team",
			Plan:                 "free_dev",
			MonthlyProposalLimit: 1000,
		},
		Usage: &apiclient.Usage{
			TenantID: "ten-test",
			From:     time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			To:       time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC),
			Counters: map[string]uint64{},
		},
	}
}

// TestSettingsWorkspace_TrialSummary covers the default plan summary:
// the trial, with the way to start a plan on the web. The detailed usage meter moved out of
// the launch surface; Settings keeps plan and key facts compact.
func TestSettingsWorkspace_TrialSummary(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.KeyCount = 1

	body := renderSettings(t, v)

	mustContain := []string{
		`<h2>Workspace</h2>`,
		`Trial`,
		`<span class="dash-pill dash-pill-muted">trial</span>`,
		`href="/dashboard/checkout?plan=team"`,
		`href="/pricing"`,
		`See pricing`,
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}

	// The legacy plan-meter copy stays retired, but the account-level
	// Consumption card deliberately reintroduces period-scoped
	// consumption to Settings — so "this period" is now expected and
	// no longer a retired-copy signal.
	for _, retired := range []string{
		`Request paid activation`,
		`remaining`,
	} {
		if strings.Contains(body, retired) {
			t.Errorf("v3 Settings rendered retired plan-meter copy %q", retired)
		}
	}
}

// TestSettingsWorkspace_SoloPlan shows a paid account as paid, with
// Manage billing and no checkout button.
func TestSettingsWorkspace_SoloPlan(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Me.Plan = "solo"
	v.Me.HasBilling = true
	v.Me.BillingStatus = "active"
	v.KeyCount = 3

	body := renderSettings(t, v)

	mustContain := []string{
		`Solo`,
		`<span class="dash-pill dash-pill-ok">paid</span>`,
		`Manage billing`,
		`href="/pricing"`,
	}
	if strings.Contains(body, `/dashboard/checkout`) {
		t.Errorf("a paid account was offered checkout")
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}

	if strings.Contains(body, `Request paid activation`) {
		t.Errorf("a Solo account rendered the old upgrade CTA")
	}
}

// TestSettingsWorkspace_MissingUsageStillRenders covers the degraded
// path: the Usage call to apps/api failed at index time. The v3
// workspace summary does not depend on Usage.
func TestSettingsWorkspace_MissingUsageStillRenders(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Usage = nil

	body := renderSettings(t, v)

	for _, want := range []string{`<h2>Workspace</h2>`, `Trial`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q with nil usage", want)
		}
	}
}

// TestSettingsWorkspace_CanonicalPlans pins the v1 plan ids: each
// renders its name, never its raw id, and a legacy id is the trial.
func TestSettingsWorkspace_CanonicalPlans(t *testing.T) {
	t.Parallel()

	cases := []struct {
		plan     string
		wantName string
		wantPill string
	}{
		{"trial", "Trial", `<span class="dash-pill dash-pill-muted">trial</span>`},
		{"solo", "Solo", `<span class="dash-pill dash-pill-ok">paid</span>`},
		{"team", "Team", `<span class="dash-pill dash-pill-ok">paid</span>`},
		{"company", "Company", `<span class="dash-pill dash-pill-ok">paid</span>`},
		{"internal", "Internal", `<span class="dash-pill dash-pill-muted">internal</span>`},
		// A legacy plan is the trial: there is no free tier.
		{"growth", "Trial", `<span class="dash-pill dash-pill-muted">trial</span>`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.plan, func(t *testing.T) {
			t.Parallel()
			v := freshDashboardView()
			v.Me.Plan = tc.plan
			v.KeyCount = 1

			body := renderSettings(t, v)

			if !strings.Contains(body, tc.wantName) {
				t.Errorf("plan %q: body missing display name %q", tc.plan, tc.wantName)
			}
			if !strings.Contains(body, tc.wantPill) {
				t.Errorf("plan %q: body missing pill %q", tc.plan, tc.wantPill)
			}
			// The raw id must never be the plan's name (the status pill
			// is a word of its own, such as "trial" or "paid").
			if strings.Contains(body, "<dd>"+tc.plan+"</dd>") {
				t.Errorf("plan %q: raw lowercase id leaked into rendered output", tc.plan)
			}
		})
	}
}
