package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/tenant"
)

type controlDecisionsResp struct {
	Control struct {
		ID          string   `json:"id"`
		Action      string   `json:"action"`
		Actions     []string `json:"actions"`
		Environment string   `json:"environment"`
		Status      string   `json:"status"`
		Mode        string   `json:"mode"`
	} `json:"control"`
	Window struct {
		From time.Time `json:"from"`
		To   time.Time `json:"to"`
	} `json:"window"`
	Decisions struct {
		Total     int                     `json:"total"`
		Counts    map[string]int          `json:"counts"`
		Truncated bool                    `json:"truncated"`
		Rows      []proposals.DecisionRow `json:"rows"`
	} `json:"decisions"`
	ByAction []struct {
		Action string         `json:"action"`
		Total  int            `json:"total"`
		Counts map[string]int `json:"counts"`
	} `json:"by_action"`
}

func controlDecisionsRig(t *testing.T, opts ...handlers.ControlsOption) (http.Handler, string, *proposals.MemoryReader) {
	t.Helper()
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID: tenant.NewID(), Slug: "cd", Status: tenant.StatusActive,
		Metadata: map[string]any{tenant.MetadataKeyMode: "shadow"},
	}
	if err := tenants.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	controls := domain.NewMemoryControlStore()
	if err := controls.Create(ctx, domain.Control{
		ID: "ctrl-1", TenantID: tnt.ID, Domain: "refund", Entity: "Order",
		Actions: []string{"REFUND_ORDER"}, Status: "active",
	}); err != nil {
		t.Fatalf("create control: %v", err)
	}
	reader := proposals.NewMemoryReader()

	h := handlers.NewControlsHandler(controls, nil, append([]handlers.ControlsOption{
		handlers.WithControlsDecisionsReader(reader),
		handlers.WithControlsTenantStore(tenants),
	}, opts...)...)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "u"}))
	return wrapped, tnt.ID, reader
}

// multiActionControlRig is controlDecisionsRig with a control that
// governs several actions (a control mints from a whole domain), so the
// by_action breakdown has something to report.
func multiActionControlRig(t *testing.T, actions ...string) (http.Handler, string, *proposals.MemoryReader) {
	t.Helper()
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID: tenant.NewID(), Slug: "cd", Status: tenant.StatusActive,
		Metadata: map[string]any{tenant.MetadataKeyMode: "enforce"},
	}
	if err := tenants.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	controls := domain.NewMemoryControlStore()
	if err := controls.Create(ctx, domain.Control{
		ID: "ctrl-1", TenantID: tnt.ID, Domain: "prod-guardian", Entity: "Service",
		Actions: actions, Status: "active",
	}); err != nil {
		t.Fatalf("create control: %v", err)
	}
	reader := proposals.NewMemoryReader()
	h := handlers.NewControlsHandler(controls, nil,
		handlers.WithControlsDecisionsReader(reader),
		handlers.WithControlsTenantStore(tenants),
	)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "u"}))
	return wrapped, tnt.ID, reader
}

// A control that governs several actions returns a per-action, window-
// complete breakdown (by_action) so Govern can show every action's own
// allowed/blocked/held, not just the control-wide rollup.
func TestControlDecisions_ByActionBreakdown(t *testing.T) {
	t.Parallel()
	mux, tenantID, reader := multiActionControlRig(t, "RESTART_SERVICE", "DROP_DATABASE")
	now := time.Now().UTC()
	// RESTART_SERVICE: 2 allowed, 1 blocked. DROP_DATABASE: 2 blocked.
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "r1", ActionName: "RESTART_SERVICE", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-3 * time.Hour)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "r2", ActionName: "RESTART_SERVICE", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-2 * time.Hour)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "r3", ActionName: "RESTART_SERVICE", ControlID: "ctrl-1", Outcome: "blocked", CreatedAt: now.Add(-time.Hour)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "d1", ActionName: "DROP_DATABASE", ControlID: "ctrl-1", Outcome: "blocked", CreatedAt: now.Add(-50 * time.Minute)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "d2", ActionName: "DROP_DATABASE", ControlID: "ctrl-1", Outcome: "blocked", CreatedAt: now.Add(-40 * time.Minute)})

	rec := getJSON(t, mux, "/v1/me/controls/ctrl-1/decisions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp controlDecisionsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.ByAction) != 2 {
		t.Fatalf("by_action: got %d entries, want 2\n%+v", len(resp.ByAction), resp.ByAction)
	}
	got := map[string]struct {
		total   int
		allowed int
		blocked int
	}{}
	for _, a := range resp.ByAction {
		got[a.Action] = struct {
			total   int
			allowed int
			blocked int
		}{a.Total, a.Counts["allowed"], a.Counts["blocked"]}
	}
	if g := got["RESTART_SERVICE"]; g.total != 3 || g.allowed != 2 || g.blocked != 1 {
		t.Errorf("RESTART_SERVICE: %+v, want total=3 allowed=2 blocked=1", g)
	}
	if g := got["DROP_DATABASE"]; g.total != 2 || g.blocked != 2 || g.allowed != 0 {
		t.Errorf("DROP_DATABASE: %+v, want total=2 blocked=2 allowed=0", g)
	}
}

func getJSON(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The endpoint returns the control header (id authoritative, mode from
// tenant), a window, and window-complete counts seeded with all five
// outcomes; rows is the sample.
func TestControlDecisions_ReturnsScopedTimeline(t *testing.T) {
	t.Parallel()
	mux, tenantID, reader := controlDecisionsRig(t)
	now := time.Now().UTC()

	// Decisions for ctrl-1 in window: 2 allowed, 1 blocked.
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p1", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-2 * time.Hour)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p2", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-1 * time.Hour)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p3", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "blocked", CreatedAt: now.Add(-30 * time.Minute)})
	// A decision for a DIFFERENT control — must not appear.
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p4", ActionName: "REFUND_ORDER", ControlID: "ctrl-other", Outcome: "allowed", CreatedAt: now})
	// An unattributed decision (no control_id) — falls out.
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p5", ActionName: "REFUND_ORDER", Outcome: "allowed", CreatedAt: now})

	rec := getJSON(t, mux, "/v1/me/controls/ctrl-1/decisions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp controlDecisionsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Control.ID != "ctrl-1" || resp.Control.Status != "active" {
		t.Errorf("control header wrong: %+v", resp.Control)
	}
	if resp.Control.Action != "REFUND_ORDER" {
		t.Errorf("action = %q, want REFUND_ORDER (primary action titles the timeline)", resp.Control.Action)
	}
	if resp.Control.Mode != "observe" {
		t.Errorf("mode = %q, want observe", resp.Control.Mode)
	}
	if resp.Control.Environment != domain.DefaultControlEnvironment {
		t.Errorf("environment = %q, want %q", resp.Control.Environment, domain.DefaultControlEnvironment)
	}
	// Scoped to ctrl-1: 3 decisions, not the other-control or unattributed ones.
	if resp.Decisions.Total != 3 {
		t.Errorf("total = %d, want 3 (ctrl-1 only)", resp.Decisions.Total)
	}
	if resp.Decisions.Counts["allowed"] != 2 || resp.Decisions.Counts["blocked"] != 1 {
		t.Errorf("counts wrong: %+v", resp.Decisions.Counts)
	}
	// All five outcomes present (seeded to 0).
	for _, o := range []string{"allowed", "blocked", "approval_required", "limit_exceeded", "invalid"} {
		if _, ok := resp.Decisions.Counts[o]; !ok {
			t.Errorf("counts missing seeded outcome %q: %+v", o, resp.Decisions.Counts)
		}
	}
}

// A control_id that doesn't belong to the tenant 404s (never leaks
// another tenant's control).
func TestControlDecisions_UnknownControl404(t *testing.T) {
	t.Parallel()
	mux, _, _ := controlDecisionsRig(t)
	rec := getJSON(t, mux, "/v1/me/controls/does-not-exist/decisions")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// ?outcome= filters the ROWS but the counts + total stay window-complete
// across ALL outcomes — so the dashboard's summary tiles keep showing every
// outcome's count (and let you switch filters) instead of collapsing to the
// one you clicked. Only the row sample narrows to the requested outcome.
func TestControlDecisions_OutcomeFilter(t *testing.T) {
	t.Parallel()
	mux, tenantID, reader := controlDecisionsRig(t)
	now := time.Now().UTC()
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p1", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p2", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "blocked", CreatedAt: now})

	rec := getJSON(t, mux, "/v1/me/controls/ctrl-1/decisions?outcome=blocked")
	var resp controlDecisionsResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	// Counts + total are window-complete across all outcomes, not the
	// filtered set — this is the fix for the collapsing summary tiles.
	if resp.Decisions.Total != 2 {
		t.Errorf("total should stay window-complete under a filter: got %d, want 2", resp.Decisions.Total)
	}
	if resp.Decisions.Counts["allowed"] != 1 || resp.Decisions.Counts["blocked"] != 1 {
		t.Errorf("counts should be window-complete across all outcomes: %+v", resp.Decisions.Counts)
	}
	// ...but the row sample narrows to the requested outcome.
	if len(resp.Decisions.Rows) != 1 || resp.Decisions.Rows[0].Outcome != "blocked" {
		t.Errorf("rows not filtered to blocked: %+v", resp.Decisions.Rows)
	}
	// One blocked row, one blocked in the window → the filtered view is
	// complete, not truncated.
	if resp.Decisions.Truncated {
		t.Errorf("filtered view of 1 blocked row (1 blocked in window) should not be truncated")
	}
}

// ?limit=N caps the rows sample but counts/total stay window-complete
// (the load-bearing property: a small limit must not shrink the counts).
func TestControlDecisions_LimitCapsRowsNotCounts(t *testing.T) {
	t.Parallel()
	mux, tenantID, reader := controlDecisionsRig(t)
	now := time.Now().UTC()
	// 5 allowed decisions for ctrl-1.
	for i := 0; i < 5; i++ {
		reader.Append(tenantID, proposals.DecisionRow{
			ProposalID: "p" + time.Duration(i).String(), ActionName: "REFUND_ORDER",
			ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}

	rec := getJSON(t, mux, "/v1/me/controls/ctrl-1/decisions?limit=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp controlDecisionsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// rows capped at the limit...
	if len(resp.Decisions.Rows) != 2 {
		t.Errorf("rows = %d, want 2 (the limit)", len(resp.Decisions.Rows))
	}
	// ...but total + counts stay window-complete.
	if resp.Decisions.Total != 5 || resp.Decisions.Counts["allowed"] != 5 {
		t.Errorf("counts must ignore limit: total=%d counts=%+v, want total=5 allowed=5", resp.Decisions.Total, resp.Decisions.Counts)
	}
	// total > sample → truncated.
	if !resp.Decisions.Truncated {
		t.Error("truncated should be true when total exceeds the row sample")
	}
}

// A card leads to its holder's proposals, so this route has to be able
// to answer for one actor. The store has filtered on actor since the
// per-runtime slice; this route simply never read the parameter, so a
// card could name its holder and had nowhere to send anyone.
//
// Counts are scoped with the rows. A timeline whose rows are one
// agent's and whose tiles are the whole domain's is two answers on one
// screen, and the reader has no way to tell which is which.
func TestControlDecisions_FilterByActor(t *testing.T) {
	t.Parallel()
	mux, tenantID, reader := controlDecisionsRig(t)
	now := time.Now().UTC()
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p1", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", ActorID: "refund-agent-eu", Outcome: "allowed", CreatedAt: now.Add(-2 * time.Hour)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p2", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", ActorID: "refund-agent-eu", Outcome: "blocked", CreatedAt: now.Add(-time.Hour)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p3", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", ActorID: "some-other-agent", Outcome: "allowed", CreatedAt: now.Add(-30 * time.Minute)})

	rec := getJSON(t, mux, "/v1/me/controls/ctrl-1/decisions?actor=refund-agent-eu")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp controlDecisionsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Decisions.Rows) != 2 {
		t.Fatalf("rows = %d, want the holder's two: %s", len(resp.Decisions.Rows), rec.Body.String())
	}
	for _, row := range resp.Decisions.Rows {
		if row.ActorID != "refund-agent-eu" {
			t.Errorf("leaked another actor: %s", row.ActorID)
		}
	}
	if resp.Decisions.Total != 2 {
		t.Errorf("total = %d, want the counts scoped with the rows", resp.Decisions.Total)
	}
}
