package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
)

// These tests confirm the operator console pages render on the shared
// dashboard shell + content primitives (the hardening design reuse),
// not a bespoke theme: the output must carry the dashboard's
// structural classes.

func opsTestChrome() components.DashboardChrome {
	return components.NewDashboardChrome("Ada Ops", "ada@kiff.test", "operator")
}

func TestOpsOverview_RendersDashboardShell(t *testing.T) {
	t.Parallel()
	v := apiclient.OpsOverview{
		Tenants: apiclient.OpsTenantStats{
			Total:    3,
			ByStatus: map[string]int{"active": 2, "suspended": 1},
			ByPlan:   map[string]int{"free_dev": 2, "launch_pilot": 1},
			New7d:    1,
			New30d:   3,
		},
		Activity: apiclient.OpsActivityStats{
			WindowDays:     7,
			Counters:       map[string]uint64{"kiff.cloud.proposals.blocked": 4},
			ReceiptsRecent: 5,
		},
		Backlog: apiclient.OpsBacklog{Pending: 2, OldestAgeSeconds: 3600},
		Attention: []apiclient.OpsTenantRisk{
			{ID: "t-1", Slug: "acme", Status: "active", Plan: "launch_pilot", Score: 2, Level: "high",
				Signals: []apiclient.OpsRiskSignal{{Key: "blocked_spike", Detail: "40% of proposals blocked", Level: "high"}}},
		},
	}
	var sb strings.Builder
	if err := OpsOverview(v, opsTestChrome()).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	for _, want := range []string{"dash-shell", "dash-card", "dash-metrics", "What needs attention", "Attention list", "acme", "40% of proposals blocked", "Internal"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview output missing %q", want)
		}
	}
}

func TestOpsTenants_RendersRows(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	rows := []apiclient.OpsTenantRow{
		{ID: "t-abc", Slug: "alpha", DisplayName: "Alpha", Status: "active", Plan: "launch_pilot", CreatedAt: now, LastActivityAt: &now},
	}
	var sb strings.Builder
	if err := OpsTenants(rows, "", opsTestChrome()).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	for _, want := range []string{"dash-table", "/ops/tenants/t-abc", "alpha", "dash-pill"} {
		if !strings.Contains(out, want) {
			t.Errorf("tenants output missing %q", want)
		}
	}
}

func TestOpsActivity_RendersFeed(t *testing.T) {
	t.Parallel()
	rows := []apiclient.OpsActivityRow{
		{TenantID: "t-1", TraceID: "trace-x", Action: "refund", Outcome: "allowed", IssuedAt: time.Now().UTC(), Anchored: true},
	}
	var sb strings.Builder
	if err := OpsActivity(rows, opsTestChrome()).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	for _, want := range []string{"dash-table", "trace-x", "anchored", "/ops/tenants/t-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("activity output missing %q", want)
		}
	}
}

func TestOpsUnavailable_Renders(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	if err := OpsUnavailable("Overview", opsTestChrome()).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	if out := sb.String(); !strings.Contains(out, "unavailable") || !strings.Contains(out, "dash-shell") {
		t.Errorf("unavailable output unexpected: %s", out[:min(200, len(out))])
	}
}

func TestOpsSupport_RendersQueue(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	rows := []apiclient.OpsSupportTicket{
		{ID: "tkt-1", TenantID: "ten-abc", Title: "Cannot deploy", Category: "bug", Status: "open", UpdatedAt: now},
	}
	var sb strings.Builder
	if err := OpsSupport(rows, "", opsTestChrome()).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	for _, want := range []string{"dash-table", "/ops/support/tkt-1", "Cannot deploy", "ten-abc", "Open", "dash-pill"} {
		if !strings.Contains(out, want) {
			t.Errorf("ops support queue missing %q", want)
		}
	}
}

func TestOpsSupportDetail_RendersThreadAndForms(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	v := apiclient.OpsSupportDetail{
		Ticket: apiclient.OpsSupportTicket{
			ID: "tkt-1", TenantID: "ten-abc", AuthorSubject: "user-a",
			Title: "Cannot deploy", Body: "the deploy spins forever", Category: "bug",
			Status: "open", CreatedAt: now,
		},
		Messages: []apiclient.OpsSupportMessage{
			{ID: "m1", TicketID: "tkt-1", AuthorKind: "user", Body: "still stuck", CreatedAt: now},
		},
	}
	var sb strings.Builder
	if err := OpsSupportDetail(v, opsTestChrome()).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		"Cannot deploy",             // title
		"the deploy spins forever",  // opening body
		"still stuck",               // thread message
		"Tenant",                    // author label (user side)
		"/ops/support/tkt-1/reply",  // reply form
		"/ops/support/tkt-1/status", // status form
		"Send reply",
		"Update status",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ops support detail missing %q", want)
		}
	}
}
