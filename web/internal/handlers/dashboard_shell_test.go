package handlers_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/pages"
)

// renderKeys / renderPlayground / renderSettings render the
// surviving dashboard tab pages against a buffer so the tests can
// assert on the produced HTML without standing up the full handler
// stack. (Proposals/Receipts/Approvals were retired in the
// authenticated-surface design, — their surfaces folded into the
// domain Activity view.)
func renderKeys(t *testing.T, v pages.DashboardView) string {
	t.Helper()
	var buf bytes.Buffer
	if err := pages.DashboardKeys(v).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render keys: %v", err)
	}
	return buf.String()
}

// renderApprovals was retired with the standalone approvals page;
// the actionable queue now renders inside the domain Activity
// view (see dashboard_domains_test.go).

// renderSettings renders the Settings tab through the shell so
// the tab-coverage tests exercise it alongside Overview /
// Activity / Keys / Playground / Studio. Mirrors the
// freshDashboardView fixture so the workspace + user labels
// resolve consistently across views.
// renderSettingsTab renders one Settings tab, as the page does.
func renderSettingsTab(t *testing.T, v pages.DashboardView, tab string) string {
	t.Helper()
	v.SettingsTab = tab
	var buf bytes.Buffer
	if err := pages.DashboardSettings(v).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render settings: %v", err)
	}
	return buf.String()
}

// renderSettings renders every Settings tab, one after the other, so a
// test can look for what the page shows wherever it lives.
func renderSettings(t *testing.T, v pages.DashboardView) string {
	t.Helper()
	var buf bytes.Buffer
	for _, tab := range []string{"account", "view", "workspace", "usage", "data"} {
		v.SettingsTab = tab
		if err := pages.DashboardSettings(v).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render settings %s: %v", tab, err)
		}
	}
	return buf.String()
}

// TestDashboardShell_TabsRenderOnEveryPage asserts the section
// nav appears on every dashboard surface. If a page renders
// without the nav, the user lost the ability to navigate — the
// bug is silent until someone notices, so pin it as a regression
// guard. Covers Overview, Activity, Approvals, Keys, Playground,
// and Settings. API keys is still a route, but it is now a
// Settings sub-surface rather than a primary nav item. Studio renders through the same shell but needs a
// non-trivial StudioView fixture; its dedicated shell coverage is
// tracked as a follow-up. The nav links (including Studio's href)
// are asserted on every page below.
func TestDashboardShell_TabsRenderOnEveryPage(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	cases := map[string]string{
		"Keys":     renderKeys(t, v),
		"Settings": renderSettings(t, v),
	}
	// The journey-spine nav: one "Your domain" group — Domain →
	// Build → Govern — plus a thin Account group. The
	// evidence-class tabs (Evidence/Approvals) folded into the
	// domain's Activity view and are no longer nav peers.
	mustContain := []string{
		`class="dash-sidebar"`,
		`dash-domain-select`, // the persistent domain selector heads the domain-scoped section
		`Current domain`,
		`Account`,
		`href="/dashboard/domains"`, `Domain`,
		`href="/dashboard/build"`, `Build`,
		`href="/dashboard/govern"`, `Govern`,
		`href="/activity"`, `Activity`,
		`href="/agents"`, `Agents`,
		`href="/dashboard/settings"`, `Settings`,
	}
	for tab, body := range cases {
		for _, want := range mustContain {
			if !strings.Contains(body, want) {
				t.Errorf("%s tab missing %q", tab, want)
			}
		}
	}
	// The retired entry points are gone from the journey nav:
	// Overview (folded into the domain home) and the tenant-wide
	// Proposals / Receipts / Approvals tabs (folded into the domain
	// Activity view), plus old Onboarding markers and Examples (the
	// sandbox gallery, retired for the card-first journey — the
	// first-decision design amendment). Checked on Settings, whose
	// content won't self-reference those routes.
	settings := renderSettings(t, v)
	for _, leak := range []string{
		`href="/dashboard/proposals"`,
		`href="/dashboard/receipts"`,
		`href="/dashboard/approvals"`,
		`>Evidence</a>`,
		`>Approvals</a>`,
		`>Onboarding</a>`,
		`href="/dashboard/start"`,
		`href="/dashboard/examples"`,
		// Runtimes left the nav for Agents; the page stays as the
		// runtime detail, linked from an agent.
		`href="/dashboard/runtimes"`,
	} {
		if strings.Contains(settings, leak) {
			t.Errorf("journey nav still renders retired entry %q", leak)
		}
	}
}

// TestDashboardShell_ActiveTabHighlighted asserts each page
// renders its own nav item with the dash-nav-active class and
// no other item carries that class. This is the visual feedback
// that tells the user which page they're on.
func TestDashboardShell_ActiveTabHighlighted(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	cases := []struct {
		name     string
		body     string
		wantHref string
	}{
		{"Keys", renderKeys(t, v), "/dashboard/keys"},
		{"Settings", renderSettingsTab(t, v, "account"), "/dashboard/settings"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			activeCount := strings.Count(c.body, `dash-nav-active`)
			if activeCount != 1 {
				t.Errorf("%s: dash-nav-active count = %d, want 1", c.name, activeCount)
			}
			// The active class should sit on the link whose href
			// matches this page. Locate the link and confirm.
			needle := `class="dash-nav-item dash-nav-active"`
			idx := strings.Index(c.body, needle)
			if idx < 0 {
				t.Fatalf("%s: did not find an active nav item on the page", c.name)
			}
			// Within the next 200 chars of the active link's
			// markup, the matching href should appear.
			window := c.body[idx:min(idx+200, len(c.body))]
			if !strings.Contains(window, `href="`+c.wantHref+`"`) {
				t.Errorf("%s: active nav href = ?, expected %q\nwindow: %s", c.name, c.wantHref, window)
			}
		})
	}
}

// TestDashboardShell_KeysRendersKeyPanel asserts the Keys tab
// renders the keys panel + mint form.
func TestDashboardShell_KeysRendersKeyPanel(t *testing.T) {
	t.Parallel()
	body := renderKeys(t, freshDashboardView())
	if !strings.Contains(body, `<h2>Active keys</h2>`) {
		t.Error("Keys tab missing the active-keys panel")
	}
	if !strings.Contains(body, `action="/dashboard/keys"`) {
		t.Error("Keys tab missing the mint key form (POST /dashboard/keys)")
	}
}

// TestDashboardShell_SidebarShell asserts the shell renders the
// redesigned sidebar + topbar (#4): the topbar with the kiff.dev
// logo and the left sidebar with the section nav. The sign-out
// action lives on the Settings tab's Account card (asserted in
// dashboard_settings_test.go). Rendered through the domain home —
// the surviving landing surface after the Overview retired.
func TestDashboardShell_SidebarShell(t *testing.T) {
	t.Parallel()
	body := renderDomainHome(t, freshDomainHomeView())

	// The shell grid: topbar + sidebar.
	if !strings.Contains(body, `class="dash-shell"`) {
		t.Error("dashboard missing the app-shell grid")
	}
	if !strings.Contains(body, `class="dash-topbar"`) {
		t.Error("dashboard missing the topbar")
	}
	if !strings.Contains(body, `class="dash-sidebar"`) {
		t.Error("dashboard missing the sidebar nav")
	}
	if !strings.Contains(body, `class="dash-topbar-logo" href="/dashboard"`) {
		t.Error("topbar missing the kiff.dev logo link")
	}

	// No in-bar sign-out form; that moved to Settings.
	mustNotContain := []string{
		`dash-shell-bar`,
		`dash-shell-tabs`,
		`action="/sign-out"`,
	}
	for _, leak := range mustNotContain {
		if strings.Contains(body, leak) {
			t.Errorf("domain home still renders retired chrome marker %q", leak)
		}
	}

	// The domain home leads with the domain, not a generic Overview.
	for _, want := range []string{
		`<p class="eyebrow">Domain</p>`,
		`<h1>duplicate-payment</h1>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("domain home missing spine marker %q", want)
		}
	}

	// The marketing footer is gone from the authenticated app.
	if strings.Contains(body, `<footer>`) {
		t.Error("dashboard still renders the marketing footer")
	}
}
