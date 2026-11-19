package handlers_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kiffapproval "github.com/kiff/kiff/pkg/kiff/approval"
)

// approvalResp is the decode shape for the approval round-trip
// tests: it adds the approval handle the gate returns on an
// approval_required outcome.
type approvalResp struct {
	ProposalID string `json:"proposal_id"`
	Outcome    string `json:"outcome"`
	Approval   *struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		ReviewURL string `json:"review_url"`
	} `json:"approval"`
}

func decodeApproval(t *testing.T, rec *httptest.ResponseRecorder) approvalResp {
	t.Helper()
	var out approvalResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

// grant resolves the approval id to granted, as the dashboard's
// grant button does through the framework endpoint.
func (rig *idemRig) grant(t *testing.T, approvalID string) {
	t.Helper()
	rt, err := rig.rt.For(context.Background(), rig.tenantID)
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	if _, err := rt.ReviewApproval(context.Background(), approvalID, "human-reviewer", kiffapproval.StatusGranted, "ok"); err != nil {
		t.Fatalf("grant approval: %v", err)
	}
}

func (rig *idemRig) deny(t *testing.T, approvalID string) {
	t.Helper()
	rt, err := rig.rt.For(context.Background(), rig.tenantID)
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	if _, err := rt.ReviewApproval(context.Background(), approvalID, "human-reviewer", kiffapproval.StatusDenied, "no"); err != nil {
		t.Fatalf("deny approval: %v", err)
	}
}

func refundBody(id string) map[string]any {
	return map[string]any{
		"id":          id,
		"entity_id":   "order-idem",
		"entity_type": "Order",
		"action_name": "REFUND_ORDER", // high risk, approval: required
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 9900, "reason": "policy override"},
	}
}

// The full round-trip: propose → approval_required + handle →
// human grants → re-decide with the same proposal → allowed.
func TestProposals_ApprovalRoundTrip(t *testing.T) {
	t.Parallel()
	rig := newIdemRig(t, 24*time.Hour)

	first := decodeApproval(t, rig.driveJSON(t, refundBody("rfnd-rt-1"), ""))
	if first.Outcome != "approval_required" {
		t.Fatalf("first outcome: got %q want approval_required", first.Outcome)
	}
	if first.Approval == nil {
		t.Fatalf("expected an approval handle on approval_required")
	}
	if first.Approval.Status != "pending" {
		t.Fatalf("handle status: got %q want pending", first.Approval.Status)
	}
	if first.Approval.ID == "" {
		t.Fatalf("expected a non-empty approval id")
	}
	if first.Approval.ReviewURL == "" {
		t.Fatalf("expected a review_url pointer")
	}

	// Human grants in the dashboard.
	rig.grant(t, first.Approval.ID)

	// Re-decide with the same proposal id → now allowed.
	second := decodeApproval(t, rig.driveJSON(t, refundBody("rfnd-rt-1"), ""))
	if second.Outcome != "allowed" {
		t.Fatalf("after grant, outcome: got %q want allowed", second.Outcome)
	}
	if second.Approval == nil || second.Approval.Status != "granted" {
		t.Fatalf("after grant, handle should report granted; got %+v", second.Approval)
	}
}

// A denied approval resolves to blocked, not approval_required.
func TestProposals_ApprovalDeniedBlocks(t *testing.T) {
	t.Parallel()
	rig := newIdemRig(t, 24*time.Hour)

	first := decodeApproval(t, rig.driveJSON(t, refundBody("rfnd-deny-1"), ""))
	if first.Approval == nil {
		t.Fatalf("expected an approval handle")
	}
	rig.deny(t, first.Approval.ID)

	second := decodeApproval(t, rig.driveJSON(t, refundBody("rfnd-deny-1"), ""))
	if second.Outcome != "blocked" {
		t.Fatalf("after deny, outcome: got %q want blocked", second.Outcome)
	}
}

// Repeated approval_required calls for the same proposal must not
// fan out duplicate approvals — auto-create is get-before-create.
func TestProposals_ApprovalAutoCreateIsIdempotent(t *testing.T) {
	t.Parallel()
	rig := newIdemRig(t, 24*time.Hour)

	_ = decodeApproval(t, rig.driveJSON(t, refundBody("rfnd-dup-1"), ""))
	_ = decodeApproval(t, rig.driveJSON(t, refundBody("rfnd-dup-1"), ""))
	_ = decodeApproval(t, rig.driveJSON(t, refundBody("rfnd-dup-1"), ""))

	rt, err := rig.rt.For(context.Background(), rig.tenantID)
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	approvals, err := rt.Approvals.List(context.Background(), "order-idem")
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("expected exactly 1 approval after 3 identical proposals; got %d", len(approvals))
	}
}

// A caller-supplied approval_id drives the round-trip explicitly,
// independent of the derived id.
func TestProposals_ApprovalRoundTripWithCallerID(t *testing.T) {
	t.Parallel()
	rig := newIdemRig(t, 24*time.Hour)

	body := refundBody("rfnd-cid-1")
	body["approval_id"] = "my-approval-42"

	first := decodeApproval(t, rig.driveJSON(t, body, ""))
	if first.Approval == nil || !strings.HasPrefix(first.Approval.ID, "my-approval-42_") {
		t.Fatalf("expected handle id derived from my-approval-42 (with a param fingerprint); got %+v", first.Approval)
	}
	rig.grant(t, first.Approval.ID)

	second := decodeApproval(t, rig.driveJSON(t, body, ""))
	if second.Outcome != "allowed" {
		t.Fatalf("after grant with caller id, outcome: got %q want allowed", second.Outcome)
	}
}

// A human granting a refund for one amount must NOT authorize a
// re-decide that changes the amount (a review point 1). The
// derived approval id binds the parameter fingerprint, so the
// changed-amount re-decide derives a different id, the grant isn't
// found, and a fresh pending approval is created instead of
// returning allowed.
func TestProposals_GrantDoesNotAuthorizeChangedParams(t *testing.T) {
	t.Parallel()
	rig := newIdemRig(t, 24*time.Hour)

	at50 := func() map[string]any {
		return map[string]any{
			"id":          "rfnd-param-1",
			"entity_id":   "order-idem",
			"entity_type": "Order",
			"action_name": "REFUND_ORDER",
			"actor_id":    "agent-refunds",
			"parameters":  map[string]any{"amount": 50, "reason": "approved small refund"},
		}
	}
	at5000 := map[string]any{
		"id":          "rfnd-param-1",
		"entity_id":   "order-idem",
		"entity_type": "Order",
		"action_name": "REFUND_ORDER",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 5000, "reason": "approved small refund"},
	}

	first := decodeApproval(t, rig.driveJSON(t, at50(), ""))
	if first.Approval == nil {
		t.Fatalf("expected an approval handle for the $50 request")
	}
	rig.grant(t, first.Approval.ID)

	// Same key, changed amount, while the only settled decision is
	// still approval_required: the derived id binds the parameter
	// fingerprint, so this derives a different approval id, the $50
	// grant isn't found, and a fresh pending approval is created —
	// never allowed.
	changed := decodeApproval(t, rig.driveJSON(t, at5000, ""))
	if changed.Outcome == "allowed" {
		t.Fatalf("changed-amount re-decide was allowed off a grant for a different amount — authority hole")
	}
	if changed.Outcome != "approval_required" {
		t.Fatalf("changed-amount re-decide: got %q want approval_required (a fresh approval)", changed.Outcome)
	}
	if changed.Approval == nil || changed.Approval.ID == first.Approval.ID {
		t.Fatalf("changed params should derive a different approval id; got %+v (orig %s)", changed.Approval, first.Approval.ID)
	}

	// The original $50 request still resolves to allowed off its
	// own grant.
	same := decodeApproval(t, rig.driveJSON(t, at50(), ""))
	if same.Outcome != "allowed" {
		t.Fatalf("re-decide with the granted params: got %q want allowed", same.Outcome)
	}
}

// Without a stable proposal id, the round-trip still completes if
// the caller passes back the approval_id from the first response
// (a review point 2): the derived id isn't reproducible, but the
// explicit approval_id is.
func TestProposals_NonStableKeyRoundTripViaApprovalID(t *testing.T) {
	t.Parallel()
	rig := newIdemRig(t, 24*time.Hour)

	noID := func() map[string]any {
		return map[string]any{
			"entity_id":   "order-idem",
			"entity_type": "Order",
			"action_name": "REFUND_ORDER",
			"actor_id":    "agent-refunds",
			"parameters":  map[string]any{"amount": 9900, "reason": "policy override"},
		}
	}

	first := decodeApproval(t, rig.driveJSON(t, noID(), ""))
	if first.Outcome != "approval_required" || first.Approval == nil {
		t.Fatalf("first call: got %q (handle=%v)", first.Outcome, first.Approval)
	}
	rig.grant(t, first.Approval.ID)

	// Re-decide with no id but the returned approval_id.
	body := noID()
	body["approval_id"] = first.Approval.ID
	second := decodeApproval(t, rig.driveJSON(t, body, ""))
	if second.Outcome != "allowed" {
		t.Fatalf("round-trip via approval_id without a stable proposal id: got %q want allowed", second.Outcome)
	}
}

// A caller-supplied approval_id binds to parameters too: granting it for
// one amount must not authorize a re-decide with a changed amount under
// the same approval_id.
func TestProposals_CallerApprovalIDBindsParams(t *testing.T) {
	t.Parallel()
	rig := newIdemRig(t, 24*time.Hour)

	at := func(amount int) map[string]any {
		return map[string]any{
			"id":          "rfnd-cidbind-1",
			"approval_id": "human-approval-7",
			"entity_id":   "order-idem",
			"entity_type": "Order",
			"action_name": "REFUND_ORDER",
			"actor_id":    "agent-refunds",
			"parameters":  map[string]any{"amount": amount, "reason": "x"},
		}
	}

	first := decodeApproval(t, rig.driveJSON(t, at(50), ""))
	if first.Approval == nil {
		t.Fatalf("expected an approval handle")
	}
	rig.grant(t, first.Approval.ID)

	// Same caller approval_id, changed amount → must not be allowed.
	changed := decodeApproval(t, rig.driveJSON(t, at(5000), ""))
	if changed.Outcome == "allowed" {
		t.Fatalf("changed amount under the same caller approval_id was allowed — param binding hole")
	}
	if changed.Approval == nil || changed.Approval.ID == first.Approval.ID {
		t.Fatalf("changed params should derive a different bound approval id; got %+v (orig %s)", changed.Approval, first.Approval.ID)
	}

	// Original amount still resolves off its grant.
	same := decodeApproval(t, rig.driveJSON(t, at(50), ""))
	if same.Outcome != "allowed" {
		t.Fatalf("re-decide with granted amount + caller id: got %q want allowed", same.Outcome)
	}
}
