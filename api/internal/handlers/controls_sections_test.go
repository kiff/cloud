package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
)

type controlSectionsResp struct {
	Receipts struct {
		Status        string `json:"status"`
		Count         int    `json:"count"`
		SignedCount   int    `json:"signed_count"`
		AnchoredCount int    `json:"anchored_count"`
		Truncated     bool   `json:"truncated"`
		Verifiability struct {
			Anchored string `json:"anchored"`
			Scope    string `json:"scope"`
		} `json:"verifiability"`
		Samples []struct {
			TraceID    string `json:"trace_id"`
			ProposalID string `json:"proposal_id"`
			EntityID   string `json:"entity_id"`
			ActionName string `json:"action_name"`
			Signed     bool   `json:"signed"`
			Anchored   bool   `json:"anchored"`
		} `json:"samples"`
	} `json:"receipts"`
	Alerts struct {
		Status     string `json:"status"`
		Deliveries []struct {
			ProposalID string `json:"proposal_id"`
			TargetID   string `json:"target_id"`
			Status     string `json:"status"`
		} `json:"deliveries"`
	} `json:"alerts"`
	Approvals struct {
		Status string `json:"status"`
	} `json:"approvals"`
}

// controlSectionsRig wires the decisions endpoint with the decisions
// reader + receipt store + alert delivery store so the receipts/alerts
// sections populate from the composer.
func controlSectionsRig(t *testing.T) (http.Handler, string, *proposals.MemoryReader, *receipt.MemoryStore, *alert.MemoryDeliveryStore) {
	t.Helper()
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "cs", Status: tenant.StatusActive,
		Metadata: map[string]any{tenant.MetadataKeyMode: "enforce"}}
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
	receipts := receipt.NewMemoryStore()
	deliveries := alert.NewMemoryDeliveryStore()

	h := handlers.NewControlsHandler(controls, nil,
		handlers.WithControlsDecisionsReader(reader),
		handlers.WithControlsTenantStore(tenants),
		handlers.WithControlsReceipts(receipts),
		handlers.WithControlsAlertDeliveries(deliveries),
	)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "u"}))
	return wrapped, tnt.ID, reader, receipts, deliveries
}

// With receipts + deliveries wired, the decisions response carries the
// control-scoped receipts section (trimmed samples incl entity_id,
// scope=sample), the alert-deliveries section, and an honest
// approvals=unavailable (shape).
func TestControlDecisions_SurfacesReceiptsAlertsApprovals(t *testing.T) {
	t.Parallel()
	mux, tenantID, reader, receipts, deliveries := controlSectionsRig(t)
	now := time.Now().UTC()
	ctx := context.Background()

	reader.Append(tenantID, proposals.DecisionRow{
		ProposalID: "p1", EntityID: "order-1", EntityType: "Order",
		ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-time.Hour),
	})
	if err := receipts.Save(ctx, receipt.Receipt{
		SchemaVersion: receipt.SchemaVersion, TenantID: tenantID, TraceID: "exec-1", ProposalID: "p1",
		EntityID: "order-1", ActionName: "REFUND_ORDER", IssuedAt: now.Add(-50 * time.Minute),
		Attestation: &receipt.Attestation{Algorithm: receipt.AttestationAlgorithm, Signature: "0xsig"},
	}); err != nil {
		t.Fatalf("save receipt: %v", err)
	}
	_ = deliveries.RecordDelivery(ctx, alert.Delivery{
		ID: "d1", TenantID: tenantID, ProposalID: "p1", TargetID: "t-1", TargetType: "webhook",
		Status: alert.DeliveryBlocked, AttemptedAt: now.Add(-49 * time.Minute),
	})

	rec := getJSON(t, mux, "/v1/me/controls/ctrl-1/decisions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp controlSectionsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Receipts: tied to decision p1, scope=sample, trimmed sample with entity_id.
	if resp.Receipts.Status != "complete" || resp.Receipts.Count != 1 || resp.Receipts.SignedCount != 1 {
		t.Fatalf("receipts section wrong: %+v", resp.Receipts)
	}
	if resp.Receipts.Verifiability.Scope != "sample" {
		t.Errorf("verifiability.scope = %q, want sample", resp.Receipts.Verifiability.Scope)
	}
	if len(resp.Receipts.Samples) != 1 {
		t.Fatalf("samples = %d, want 1", len(resp.Receipts.Samples))
	}
	s := resp.Receipts.Samples[0]
	if s.TraceID != "exec-1" || s.EntityID != "order-1" || s.ProposalID != "p1" || !s.Signed || s.Anchored {
		t.Errorf("receipt sample wrong: %+v", s)
	}

	// Alerts: the blocked delivery linked to p1.
	if resp.Alerts.Status != "complete" || len(resp.Alerts.Deliveries) != 1 {
		t.Fatalf("alerts section wrong: %+v", resp.Alerts)
	}
	if d := resp.Alerts.Deliveries[0]; d.ProposalID != "p1" || d.TargetID != "t-1" || d.Status != "blocked" {
		t.Errorf("delivery wrong: %+v", d)
	}

	// Approvals: honest unavailable.
	if resp.Approvals.Status != "unavailable" {
		t.Errorf("approvals status = %q, want unavailable", resp.Approvals.Status)
	}
}

// Without the receipt / delivery stores wired, the sections report
// unavailable (the web renders "not yet recorded"), not a hollow empty.
func TestControlDecisions_SectionsUnavailableWithoutStores(t *testing.T) {
	t.Parallel()
	mux, tenantID, reader := controlDecisionsRig(t)
	reader.Append(tenantID, proposals.DecisionRow{
		ProposalID: "p1", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: time.Now().UTC(),
	})
	rec := getJSON(t, mux, "/v1/me/controls/ctrl-1/decisions")
	var resp controlSectionsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Receipts.Status != "unavailable" {
		t.Errorf("receipts status = %q, want unavailable (no store wired)", resp.Receipts.Status)
	}
	if resp.Alerts.Status != "unavailable" {
		t.Errorf("alerts status = %q, want unavailable (no store wired)", resp.Alerts.Status)
	}
	if resp.Approvals.Status != "unavailable" {
		t.Errorf("approvals status = %q, want unavailable", resp.Approvals.Status)
	}
}
