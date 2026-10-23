package proposals_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/proposals"
)

// ListDecisionsInWindow filters by action + time window at the query
// layer: older decisions and other actions are excluded by the
// query, not by a post-filter on a tenant-wide page.
func TestListDecisionsInWindow_BoundsByActionAndWindow(t *testing.T) {
	t.Parallel()
	r := proposals.NewMemoryReader()
	now := time.Now().UTC()
	tenant := "t1"

	// In window, target action (x3).
	r.Append(tenant, proposals.DecisionRow{ProposalID: "p1", ActionName: "REFUND", Outcome: "allowed", CreatedAt: now.Add(-2 * time.Hour)})
	r.Append(tenant, proposals.DecisionRow{ProposalID: "p2", ActionName: "REFUND", Outcome: "blocked", CreatedAt: now.Add(-1 * time.Hour)})
	r.Append(tenant, proposals.DecisionRow{ProposalID: "p3", ActionName: "REFUND", Outcome: "approval_required", CreatedAt: now.Add(-30 * time.Minute)})
	// Different action — excluded.
	r.Append(tenant, proposals.DecisionRow{ProposalID: "p4", ActionName: "OTHER", Outcome: "allowed", CreatedAt: now.Add(-15 * time.Minute)})
	// Target action but OUTSIDE the window (90 days old) — excluded.
	r.Append(tenant, proposals.DecisionRow{ProposalID: "p5", ActionName: "REFUND", Outcome: "allowed", CreatedAt: now.AddDate(0, 0, -90)})

	got, err := r.ListDecisionsInWindow(context.Background(), tenant, proposals.DecisionQuery{
		Action: "REFUND",
		From:   now.AddDate(0, 0, -30),
		To:     now,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3 (REFUND in window); rows=%+v", len(got), got)
	}
	// DESC by created_at.
	if got[0].ProposalID != "p3" || got[2].ProposalID != "p1" {
		t.Errorf("order wrong: got %s..%s, want p3..p1", got[0].ProposalID, got[2].ProposalID)
	}
}

// control_id / environment narrow further when set, and tenant scoping
// holds (another tenant's rows never appear).
func TestListDecisionsInWindow_ScopesByControlAndTenant(t *testing.T) {
	t.Parallel()
	r := proposals.NewMemoryReader()
	now := time.Now().UTC()

	r.Append("t1", proposals.DecisionRow{ProposalID: "a", ActionName: "REFUND", ControlID: "ctrl-1", Environment: "prod", CreatedAt: now})
	r.Append("t1", proposals.DecisionRow{ProposalID: "b", ActionName: "REFUND", ControlID: "ctrl-2", Environment: "staging", CreatedAt: now})
	r.Append("t2", proposals.DecisionRow{ProposalID: "c", ActionName: "REFUND", ControlID: "ctrl-1", Environment: "prod", CreatedAt: now})

	got, err := r.ListDecisionsInWindow(context.Background(), "t1", proposals.DecisionQuery{
		Action: "REFUND", ControlID: "ctrl-1",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 || got[0].ProposalID != "a" {
		t.Fatalf("control scoping wrong: got %+v, want [a]", got)
	}

	// Environment narrowing.
	gotEnv, _ := r.ListDecisionsInWindow(context.Background(), "t1", proposals.DecisionQuery{
		Action: "REFUND", Environment: "staging",
	})
	if len(gotEnv) != 1 || gotEnv[0].ProposalID != "b" {
		t.Fatalf("env scoping wrong: got %+v, want [b]", gotEnv)
	}
}

// Limit clamps to MaxLimit and a zero limit uses DefaultLimit.
func TestListDecisionsInWindow_LimitClamping(t *testing.T) {
	t.Parallel()
	r := proposals.NewMemoryReader()
	now := time.Now().UTC()
	for i := 0; i < proposals.MaxLimit+10; i++ {
		r.Append("t1", proposals.DecisionRow{
			ProposalID: time.Duration(i).String(), ActionName: "REFUND",
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	got, err := r.ListDecisionsInWindow(context.Background(), "t1", proposals.DecisionQuery{
		Action: "REFUND", Limit: 10_000,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != proposals.MaxLimit {
		t.Errorf("got %d, want clamp to MaxLimit=%d", len(got), proposals.MaxLimit)
	}
}

// TestListDecisionsInWindow_OffsetPaginates pins the pagination
// primitive (evidence pagination slice): Offset skips the first N rows
// of the DESC-created_at window, Limit bounds the page, and an offset
// past the end is an empty page (not an error). Newest-first ordering
// is preserved across pages.
func TestListDecisionsInWindow_OffsetPaginates(t *testing.T) {
	t.Parallel()
	r := proposals.NewMemoryReader()
	now := time.Now().UTC()
	// 5 rows, p0 (newest) .. p4 (oldest).
	for i := 0; i < 5; i++ {
		r.Append("t1", proposals.DecisionRow{
			ProposalID: fmt.Sprintf("p%d", i), ActionName: "REFUND",
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	page := func(offset, limit int) []string {
		got, err := r.ListDecisionsInWindow(context.Background(), "t1", proposals.DecisionQuery{
			Action: "REFUND", Limit: limit, Offset: offset,
		})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		ids := make([]string, len(got))
		for i, row := range got {
			ids[i] = row.ProposalID
		}
		return ids
	}
	if ids := page(0, 2); !reflect.DeepEqual(ids, []string{"p0", "p1"}) {
		t.Errorf("page 1 = %v, want [p0 p1]", ids)
	}
	if ids := page(2, 2); !reflect.DeepEqual(ids, []string{"p2", "p3"}) {
		t.Errorf("page 2 = %v, want [p2 p3]", ids)
	}
	if ids := page(4, 2); !reflect.DeepEqual(ids, []string{"p4"}) {
		t.Errorf("page 3 = %v, want [p4]", ids)
	}
	if ids := page(5, 2); len(ids) != 0 {
		t.Errorf("offset at end = %v, want empty page", ids)
	}
	if ids := page(99, 2); len(ids) != 0 {
		t.Errorf("offset past end = %v, want empty page", ids)
	}
}

// CountDecisionsByOutcome counts over the FULL window, independent of
// the row limit — the correctness property the Control Room header and
// proof-pack counts depend on (a capped row sample must not shrink the
// counts).
func TestCountDecisionsByOutcome_WindowCompleteIgnoresLimit(t *testing.T) {
	t.Parallel()
	r := proposals.NewMemoryReader()
	now := time.Now().UTC()
	// 5 allowed + 3 blocked in window for REFUND.
	for i := 0; i < 5; i++ {
		r.Append("t1", proposals.DecisionRow{ProposalID: "a" + time.Duration(i).String(), ActionName: "REFUND", Outcome: "allowed", CreatedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	for i := 0; i < 3; i++ {
		r.Append("t1", proposals.DecisionRow{ProposalID: "b" + time.Duration(i).String(), ActionName: "REFUND", Outcome: "blocked", CreatedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	// Out-of-window and other-action rows must not count.
	r.Append("t1", proposals.DecisionRow{ProposalID: "old", ActionName: "REFUND", Outcome: "allowed", CreatedAt: now.AddDate(0, 0, -90)})
	r.Append("t1", proposals.DecisionRow{ProposalID: "other", ActionName: "OTHER", Outcome: "allowed", CreatedAt: now})

	q := proposals.DecisionQuery{
		Action: "REFUND",
		From:   now.AddDate(0, 0, -30),
		To:     now,
		Limit:  2, // deliberately tiny — must NOT affect counts
	}
	counts, total, err := r.CountDecisionsByOutcome(context.Background(), "t1", q)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 8 {
		t.Errorf("total = %d, want 8 (full window, limit ignored)", total)
	}
	if counts["allowed"] != 5 || counts["blocked"] != 3 {
		t.Errorf("counts = %+v, want allowed:5 blocked:3", counts)
	}

	// The capped row sample is small, but counts stayed complete.
	rows, _ := r.ListDecisionsInWindow(context.Background(), "t1", q)
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2 (the cap)", len(rows))
	}
}
