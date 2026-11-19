package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
)

// A decision receipt surfaces in the control-scoped receipts
// section labeled kind "decision", while an execution receipt is labeled
// "execution" — so the Control Room can distinguish pre-execution proof
// from execution proof.
func TestControlDecisions_LabelsReceiptKind(t *testing.T) {
	t.Parallel()
	mux, tenantID, reader, receipts, _ := controlSectionsRig(t)
	now := time.Now().UTC()
	ctx := context.Background()

	// Decision p-dec → a decision receipt (vote-shape enforce; no execution).
	reader.Append(tenantID, proposals.DecisionRow{
		ProposalID: "p-dec", EntityID: "order-dec", EntityType: "Order",
		ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-time.Hour),
	})
	if err := receipts.Save(ctx, receipt.Receipt{
		SchemaVersion: receipt.SchemaVersion, TenantID: tenantID,
		TraceID: receipt.DecisionReceiptTraceID("p-dec"), ProposalID: "p-dec",
		EntityID: "order-dec", ActionName: "REFUND_ORDER", IssuedAt: now.Add(-30 * time.Minute),
	}); err != nil {
		t.Fatalf("save decision receipt: %v", err)
	}
	// Decision p-exec → an execution receipt (raw trace).
	reader.Append(tenantID, proposals.DecisionRow{
		ProposalID: "p-exec", EntityID: "order-exec", EntityType: "Order",
		ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-2 * time.Hour),
	})
	if err := receipts.Save(ctx, receipt.Receipt{
		SchemaVersion: receipt.SchemaVersion, TenantID: tenantID,
		TraceID: "exec-trace-9", ProposalID: "p-exec",
		EntityID: "order-exec", ActionName: "REFUND_ORDER", IssuedAt: now.Add(-90 * time.Minute),
	}); err != nil {
		t.Fatalf("save execution receipt: %v", err)
	}

	rec := getJSON(t, mux, "/v1/me/controls/ctrl-1/decisions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Receipts struct {
			Samples []struct {
				TraceID    string `json:"trace_id"`
				ProposalID string `json:"proposal_id"`
				Kind       string `json:"kind"`
			} `json:"samples"`
		} `json:"receipts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	kinds := map[string]string{} // proposal_id -> kind
	for _, s := range resp.Receipts.Samples {
		kinds[s.ProposalID] = s.Kind
	}
	if kinds["p-dec"] != "decision" {
		t.Errorf("decision receipt kind = %q, want decision (samples=%+v)", kinds["p-dec"], resp.Receipts.Samples)
	}
	if kinds["p-exec"] != "execution" {
		t.Errorf("execution receipt kind = %q, want execution", kinds["p-exec"])
	}
}
