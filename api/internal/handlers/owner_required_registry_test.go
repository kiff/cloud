package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/proposals"
)

// a review: owner_required is a wire outcome, so an owner can
// subscribe an alert target to it.
func TestAlertTargetCanSubscribeToOwnerRequired(t *testing.T) {
	t.Parallel()
	mux := newAlertTargetsMux(t, alert.NewMemoryStore())
	rec := drivePost(t, mux, "/v1/me/alert-targets",
		json.RawMessage(`{"url":"https://hooks.example.com/kiff","outcomes":["owner_required"]}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
}

// a review: the decision counts carry owner_required, seeded to
// zero, and count it in the totals and per action.
func TestControlDecisionsCountOwnerRequired(t *testing.T) {
	t.Parallel()
	mux, tenantID, reader := multiActionControlRig(t, "RESTART_SERVICE", "DROP_DATABASE")
	rec := getJSON(t, mux, "/v1/me/controls/ctrl-1/decisions")
	var empty controlDecisionsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if n, ok := empty.Decisions.Counts["owner_required"]; !ok || n != 0 {
		t.Fatalf("zero counts must carry owner_required: %+v", empty.Decisions.Counts)
	}

	now := time.Now().UTC()
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "o1", ActionName: "RESTART_SERVICE", ControlID: "ctrl-1", Outcome: "owner_required", CreatedAt: now.Add(-2 * time.Hour)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "o2", ActionName: "RESTART_SERVICE", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-time.Hour)})
	rec = getJSON(t, mux, "/v1/me/controls/ctrl-1/decisions")
	var resp controlDecisionsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Decisions.Counts["owner_required"] != 1 || resp.Decisions.Total != 2 {
		t.Fatalf("totals: %+v", resp.Decisions)
	}
	for _, a := range resp.ByAction {
		if a.Action == "RESTART_SERVICE" && (a.Counts["owner_required"] != 1 || a.Total != 2) {
			t.Fatalf("per action: %+v", a)
		}
		if a.Action == "DROP_DATABASE" {
			if n, ok := a.Counts["owner_required"]; ok && n != 0 {
				t.Fatalf("per action zero: %+v", a)
			}
		}
	}
}
