package handlers_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
)

// dashboard_settings_test.go covers the v3 Settings tab: a compact
// account/workspace surface with identity, API-key wayfinding, data
// export, and deletion controls.

// TestSettings_RendersTenantAndPlan asserts the Settings tab
// renders the tenant identity + plan information that moved
// off Overview.
func TestSettings_RendersTenantAndPlan(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	// Populate a usage counter so the plan card has data to
	// render (the partial branches on Usage being non-nil).
	v.Usage = &apiclient.Usage{
		TenantID:             "ten-test",
		Plan:                 "free_dev",
		MonthlyProposalLimit: 1000,
		From:                 time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		To:                   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		Counters: map[string]uint64{
			"kiff.cloud.proposals.submitted": 12,
		},
	}
	v.KeyCount = 2
	body := renderSettings(t, v)

	mustContain := []string{
		`Account &amp; workspace`,
		`<h2>Account</h2>`,
		`Alice`,
		`alice@example.com`,
		`action="/sign-out"`,
		// Data-rights export control on the Account card: the
		// self-serve Article 15/20 download.
		`href="/dashboard/account/export"`,
		`Export my data`,
		// Delete form — danger zone.
		`action="/dashboard/account/delete"`,
		`name="confirm_slug"`,
		`Delete account`,
		// Workspace card content. The tenant slug + id are tenant
		// metadata; the auto-generated tenant display name is no
		// longer shown as "Display name".
		`<h2>Workspace</h2>`,
		`>alice-team</code>`,
		`>ten-test</code>`,
		`Trial`,
		`href="/pricing"`,
		// Settings nav item is the active one.
		`class="dash-nav-item dash-nav-active"`,
		`href="/dashboard/settings"`,
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}

	// The tenant display name must NOT render as a human identity on
	// Settings (display-name fix). It is the account's name, shown only
	// in the Account name field.
	field := `id="account-name" name="name" class="dash-input" required maxlength="80" autocomplete="organization" value="Alice&#39;s Team"`
	if !strings.Contains(body, field) {
		t.Error("Settings doesn't offer the account's name to change")
	}
	if strings.Count(body, `Alice&#39;s Team`) != 1 {
		t.Error("Settings renders the tenant display name outside the Account name field")
	}
}

// TestSettings_DoesNotRenderRuntimeSignal asserts the Settings
// tab does NOT render Overview's runtime-signal partials.
// Catches a regression where a card lands on both pages.
func TestSettings_DoesNotRenderRuntimeSignal(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	body := renderSettings(t, v)

	mustNotContain := []string{
		`Latest decisions`,           // latestProposals partial
		`<h2>Governed activity</h2>`, // governedActivityCard partial
		`Connected.`,                 // connection state ("Connected tools" is a sidebar label)
		`Not connected yet`,          // connectionStatus empty state
		`Next action`,                // nextActionCard partial
	}
	for _, leak := range mustNotContain {
		if strings.Contains(body, leak) {
			t.Errorf("Settings rendered %q; should live on Overview", leak)
		}
	}
}

// Settings shows one topic per tab; an unknown tab is Account.
func TestSettings_OneTopicPerTab(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	account := renderSettingsTab(t, v, pages.SettingsTabFor("nope"))
	if !strings.Contains(account, "Sign out") || strings.Contains(account, "What this dashboard shows") || strings.Contains(account, "Delete account") {
		t.Error("the Account tab shows another tab's content")
	}
	data := renderSettingsTab(t, v, "data")
	if !strings.Contains(data, "Delete account") || strings.Contains(data, "What this dashboard shows") {
		t.Error("the Data & deletion tab shows another tab's content")
	}
	ws := renderSettingsTab(t, v, "workspace")
	if !strings.Contains(ws, `data-copy-target="st-tenant-id"`) || !strings.Contains(ws, `id="st-tenant-id"`) {
		t.Error("the Tenant ID has no Copy button")
	}
}

// Switching the view takes a second step: the choice links to a
// confirmation, and only that confirmation posts the change.
func TestSettings_ViewSwitchIsConfirmed(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	plain := renderSettingsTab(t, v, "view")
	if strings.Contains(plain, `action="/dashboard/account-kind"`) {
		t.Error("the view can be changed in one click")
	}
	if !strings.Contains(plain, `href="/dashboard/settings?tab=view&amp;switch=company"`) {
		t.Error("no link to confirm the company view")
	}
	v.SwitchTo = "company"
	confirm := renderSettingsTab(t, v, "view")
	for _, want := range []string{"Switch to the company view?", "Nothing is moved or deleted", `action="/dashboard/account-kind"`, `name="kind" value="company"`, "Cancel"} {
		if !strings.Contains(confirm, want) {
			t.Errorf("confirmation missing %q", want)
		}
	}
	v.SwitchTo = "family"
	if strings.Contains(renderSettingsTab(t, v, "view"), `action="/dashboard/account-kind"`) {
		t.Error("an unknown kind was offered for confirmation")
	}
}

// The Account tab says where held calls reach the owner and offers the
// opposite of the current chat setting.
func TestSettings_HeldCallsCard(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Me.ChatApprovals = true
	on := renderSettingsTab(t, v, "account")
	for _, want := range []string{"Held calls", "Also offered in the agent's chat: on.", `action="/dashboard/chat-approvals"`, `name="enabled" value="false"`} {
		if !strings.Contains(on, want) {
			t.Errorf("on: missing %q", want)
		}
	}
	v.Me.ChatApprovals = false
	if off := renderSettingsTab(t, v, "account"); !strings.Contains(off, `name="enabled" value="true"`) || !strings.Contains(off, "chat: off.") {
		t.Error("off: no way to turn it on")
	}
}

// While a switch is being confirmed, that option's own link is hidden.
func TestSettings_ConfirmHidesTheOptionLink(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.SwitchTo = "company"
	if body := renderSettingsTab(t, v, "view"); strings.Contains(body, "Use the company view") {
		t.Error("the company option still links to the confirmation it is showing")
	}
}
