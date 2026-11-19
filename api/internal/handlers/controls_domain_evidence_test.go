package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

// The domain-keyed evidence route resolves the domain's
// active control and returns the same control-scoped evidence payload
// as the control_id route. controlDecisionsRig seeds an active control
// "ctrl-1" governing domain "refund".
func TestDomainEvidence_ResolvesActiveControl(t *testing.T) {
	t.Parallel()
	mux, tenantID, reader := controlDecisionsRig(t)
	now := time.Now().UTC()

	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p1", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-2 * time.Hour)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p2", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "blocked", CreatedAt: now.Add(-time.Hour)})
	// A decision for a different control must not appear via the domain.
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p3", ActionName: "REFUND_ORDER", ControlID: "ctrl-other", Outcome: "allowed", CreatedAt: now})

	rec := getJSON(t, mux, "/v1/me/domains/refund/evidence")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp controlDecisionsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Same payload as the control_id route, resolved from the domain.
	if resp.Control.ID != "ctrl-1" {
		t.Errorf("control.id = %q, want ctrl-1 (resolved from domain)", resp.Control.ID)
	}
	if resp.Decisions.Total != 2 {
		t.Errorf("total = %d, want 2 (ctrl-1 only, not ctrl-other)", resp.Decisions.Total)
	}
	if resp.Decisions.Counts["allowed"] != 1 || resp.Decisions.Counts["blocked"] != 1 {
		t.Errorf("counts wrong: %+v", resp.Decisions.Counts)
	}
}

// The domain route honors the same window/limit contract (shared
// composition): a small limit caps rows but not counts.
func TestDomainEvidence_LimitCapsRowsNotCounts(t *testing.T) {
	t.Parallel()
	mux, tenantID, reader := controlDecisionsRig(t)
	now := time.Now().UTC()
	for i := 0; i < 4; i++ {
		reader.Append(tenantID, proposals.DecisionRow{
			ProposalID: "p" + time.Duration(i).String(), ActionName: "REFUND_ORDER",
			ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	rec := getJSON(t, mux, "/v1/me/domains/refund/evidence?limit=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp controlDecisionsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Decisions.Rows) != 2 {
		t.Errorf("rows = %d, want 2 (limit)", len(resp.Decisions.Rows))
	}
	if resp.Decisions.Total != 4 || resp.Decisions.Counts["allowed"] != 4 {
		t.Errorf("counts must ignore limit: total=%d counts=%+v", resp.Decisions.Total, resp.Decisions.Counts)
	}
	if !resp.Decisions.Truncated {
		t.Error("truncated should be true when total exceeds the row sample")
	}
}

// A domain the tenant doesn't own 404s — never leaks another tenant's
// control, mirroring the control_id route's unknown-control behavior.
func TestDomainEvidence_UnknownDomain404(t *testing.T) {
	t.Parallel()
	mux, _, _ := controlDecisionsRig(t)
	rec := getJSON(t, mux, "/v1/me/domains/does-not-exist/evidence")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// Without an identity the route is 401 (no control store access).
func TestDomainEvidence_Unauthorized(t *testing.T) {
	t.Parallel()
	controls := domain.NewMemoryControlStore()
	h := handlers.NewControlsHandler(controls, nil)
	mux := http.NewServeMux()
	h.Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains/refund/evidence", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// fakeOutcomes answers tool outcomes from a map, and records what it was asked.
type fakeOutcomes struct {
	m     map[string]tooloutcome.Outcome
	asked []string
}

func (f *fakeOutcomes) Outcomes(_ context.Context, _ string, ids []string) (map[string]tooloutcome.Outcome, error) {
	f.asked = append(f.asked, ids...)
	out := map[string]tooloutcome.Outcome{}
	for _, id := range ids {
		if o, ok := f.m[id]; ok {
			out[id] = o
		}
	}
	return out, nil
}

// An allowed decision shows how the call ended at the tool, next to
// the decision; a decision that authorized nothing has no tool outcome.
func TestDomainEvidence_AllowedRowsCarryTheToolOutcome(t *testing.T) {
	t.Parallel()
	outcomes := &fakeOutcomes{m: map[string]tooloutcome.Outcome{
		"p-refused": {Status: tooloutcome.RefusedByTool, Message: "Refund amount (€25.00) is greater than unrefunded amount on charge (€20.00)"},
		"p-done":    {Status: tooloutcome.Done},
		"p-held":    {Status: tooloutcome.Done}, // never shown: the row is not an allowed decision
	}}
	mux, tenantID, reader := controlDecisionsRig(t, handlers.WithControlsToolOutcomes(outcomes))
	now := time.Now().UTC()
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p-refused", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-3 * time.Minute)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p-done", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-2 * time.Minute)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p-held", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "owner_required", CreatedAt: now.Add(-time.Minute)})

	rec := getJSON(t, mux, "/v1/me/domains/refund/evidence")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Decisions struct {
			Rows []struct {
				ProposalID  string               `json:"proposal_id"`
				Outcome     string               `json:"outcome"`
				ToolOutcome *tooloutcome.Outcome `json:"tool_outcome"`
			} `json:"rows"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]*tooloutcome.Outcome{}
	for _, r := range resp.Decisions.Rows {
		got[r.ProposalID] = r.ToolOutcome
	}
	if o := got["p-refused"]; o == nil || o.Status != tooloutcome.RefusedByTool || o.Message == "" {
		t.Errorf("refused: %+v", o)
	}
	if o := got["p-done"]; o == nil || o.Status != tooloutcome.Done {
		t.Errorf("done: %+v", o)
	}
	if o := got["p-held"]; o != nil {
		t.Errorf("a held decision carries a tool outcome: %+v", o)
	}
	for _, id := range outcomes.asked {
		if id == "p-held" {
			t.Errorf("asked about a decision that authorized nothing")
		}
	}
}
