package handlers

import (
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

func ctrl(id, status string) apiclient.Control {
	return apiclient.Control{
		ID: id, Domain: "duplicate-payment", Actions: []string{"PAY_INVOICE"},
		Environment: "dev", Workflow: "default", Status: status,
	}
}

// Eight versions of one logical control (1 active + 7 superseded) collapse
// to a single nav row — the active one. This is the live-tenant case that
// made the Control Room nav unreadable.
func TestCollapseControls_CollapsesVersionsToActive(t *testing.T) {
	in := []apiclient.Control{
		ctrl("v8-active", "active"),
		ctrl("v7", "superseded"),
		ctrl("v6", "superseded"),
		ctrl("v5", "superseded"),
	}
	out := collapseControls(in)
	if len(out) != 1 {
		t.Fatalf("got %d controls, want 1 (collapsed)", len(out))
	}
	if out[0].ID != "v8-active" {
		t.Errorf("collapsed to %q, want the active version v8-active", out[0].ID)
	}
}

// Active version wins regardless of position in the list.
func TestCollapseControls_PrefersActiveRegardlessOfOrder(t *testing.T) {
	in := []apiclient.Control{
		ctrl("old", "superseded"),
		ctrl("new-active", "active"),
	}
	out := collapseControls(in)
	if len(out) != 1 || out[0].ID != "new-active" {
		t.Fatalf("got %+v, want single active new-active", out)
	}
}

// Distinct logical controls (different action) each keep one row, in
// first-seen order.
func TestCollapseControls_KeepsDistinctControls(t *testing.T) {
	pay := ctrl("pay", "active")
	refund := apiclient.Control{
		ID: "refund", Domain: "duplicate-payment", Actions: []string{"REFUND_ORDER"},
		Environment: "dev", Workflow: "default", Status: "active",
	}
	out := collapseControls([]apiclient.Control{pay, refund, ctrl("pay-old", "superseded")})
	if len(out) != 2 {
		t.Fatalf("got %d, want 2 distinct logical controls", len(out))
	}
	if out[0].ID != "pay" || out[1].ID != "refund" {
		t.Errorf("first-seen order not preserved: %q, %q", out[0].ID, out[1].ID)
	}
}

// No active version anywhere: keep the first-seen so the control never
// vanishes from the nav.
func TestCollapseControls_FallsBackWhenNoneActive(t *testing.T) {
	out := collapseControls([]apiclient.Control{ctrl("a", "superseded"), ctrl("b", "superseded")})
	if len(out) != 1 || out[0].ID != "a" {
		t.Fatalf("got %+v, want single first-seen a", out)
	}
}

func TestCollapseControls_Empty(t *testing.T) {
	if out := collapseControls(nil); len(out) != 0 {
		t.Fatalf("got %d, want 0", len(out))
	}
}
