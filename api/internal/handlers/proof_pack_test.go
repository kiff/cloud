package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
)

const proofPackYAML = `
domain: refund-test
entity: Order
events:
  - ORDER_PLACED
  - ORDER_PAID
states:
  - CREATED
  - PAID
transitions:
  - on: ORDER_PLACED
    from: ""
    to: CREATED
  - on: ORDER_PAID
    from: CREATED
    to: PAID
actions:
  - name: REFUND_ORDER
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    required_permissions: [orders.refund]
    risk: high
    approval: required
    executor: refund.issue_refund
  - name: AUTO_REFUND
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    risk: medium
    approval: never
    executor: refund.auto_refund
permissions:
  roles:
    ops_agent:
      - orders.refund
`

// proofPackRig wires the proof-pack handler over in-memory stores
// behind a fake identity for tenant T.
type proofPackRig struct {
	mux      http.Handler
	tenantID string
	reader   *proposals.MemoryReader
	receipts *receipt.MemoryStore
}

func newProofPackRig(t *testing.T) *proofPackRig {
	t.Helper()
	ctx := context.Background()

	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "pp", DisplayName: "Proof Co", Status: tenant.StatusActive}
	if err := tenants.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(proofPackYAML)); err != nil {
		t.Fatalf("save yaml: %v", err)
	}

	controls := domain.NewMemoryControlStore()
	if err := controls.Create(ctx, domain.Control{
		ID:          "ctrl-1",
		TenantID:    tnt.ID,
		Domain:      "refund-test",
		Entity:      "Order",
		Actions:     []string{"REFUND_ORDER", "AUTO_REFUND"},
		ActivatedBy: "alice",
	}); err != nil {
		t.Fatalf("create control: %v", err)
	}

	reader := proposals.NewMemoryReader()
	receipts := receipt.NewMemoryStore()

	h := handlers.NewProofPackHandler(tenants, controls, src, executors.DefaultRegistry(), reader, receipts)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "alice"}))

	return &proofPackRig{mux: wrapped, tenantID: tnt.ID, reader: reader, receipts: receipts}
}

func (rig *proofPackRig) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)
	return rec
}

// TestProofPack_AssemblesControlEvidence drives a control with mixed
// decisions and signed/anchored receipts, then asserts the pack
// carries contract + state machine + policy + filtered decisions +
// receipts + an honest, computed verifiability claim.
func TestProofPack_AssemblesControlEvidence(t *testing.T) {
	t.Parallel()
	rig := newProofPackRig(t)
	now := time.Now().UTC()

	// Decisions for REFUND_ORDER within the window.
	rig.reader.Append(rig.tenantID, proposals.DecisionRow{
		ProposalID: "p1", EntityID: "o1", EntityType: "Order",
		ActionName: "REFUND_ORDER", Outcome: "allowed", CreatedAt: now.Add(-2 * time.Hour),
	})
	rig.reader.Append(rig.tenantID, proposals.DecisionRow{
		ProposalID: "p2", EntityID: "o1", EntityType: "Order",
		ActionName: "REFUND_ORDER", Outcome: "blocked", CreatedAt: now.Add(-1 * time.Hour),
	})
	rig.reader.Append(rig.tenantID, proposals.DecisionRow{
		ProposalID: "p3", EntityID: "o2", EntityType: "Order",
		ActionName: "REFUND_ORDER", Outcome: "approval_required", CreatedAt: now.Add(-30 * time.Minute),
	})
	// A decision for a DIFFERENT action — must be excluded.
	rig.reader.Append(rig.tenantID, proposals.DecisionRow{
		ProposalID: "p4", EntityID: "o3", EntityType: "Order",
		ActionName: "AUTO_REFUND", Outcome: "allowed", CreatedAt: now.Add(-15 * time.Minute),
	})
	// A REFUND_ORDER decision OUTSIDE the window — must be excluded.
	rig.reader.Append(rig.tenantID, proposals.DecisionRow{
		ProposalID: "p5", EntityID: "o4", EntityType: "Order",
		ActionName: "REFUND_ORDER", Outcome: "allowed", CreatedAt: now.AddDate(0, 0, -90),
	})

	// Receipts: one signed-only, one signed+anchored.
	mustSaveReceipt(t, rig.receipts, receipt.Receipt{
		TenantID: rig.tenantID, TraceID: "p1", SchemaVersion: "kiff.receipt.v1",
		EntityID: "o1", EntityType: "Order", ActionName: "REFUND_ORDER", IssuedAt: now.Add(-2 * time.Hour),
		Attestation: &receipt.Attestation{Algorithm: receipt.AttestationAlgorithm, Signature: "0xsig", WalletAddress: "0xabc", AnchoredAt: now},
	})
	mustSaveReceipt(t, rig.receipts, receipt.Receipt{
		TenantID: rig.tenantID, TraceID: "p1b", SchemaVersion: "kiff.receipt.v1",
		EntityID: "o1", EntityType: "Order", ActionName: "REFUND_ORDER", IssuedAt: now.Add(-90 * time.Minute),
		Attestation: &receipt.Attestation{Algorithm: receipt.AttestationAlgorithm, Signature: "0xsig2", WalletAddress: "0xabc", AnchorTxHash: "0xtx", AnchoredAt: now},
	})

	rec := rig.get(t, "/v1/me/proof-pack/REFUND_ORDER")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var pack struct {
		SchemaVersion string                                   `json:"schema_version"`
		Disclaimer    string                                   `json:"disclaimer"`
		Control       *struct{ Action, Domain, Status string } `json:"control"`
		Contract      *struct {
			Name                string   `json:"Name"`
			RequiredPermissions []string `json:"RequiredPermissions"`
		} `json:"contract"`
		StateMachine *struct{ States []string } `json:"state_machine"`
		Policy       map[string][]string        `json:"policy"`
		Decisions    struct {
			Total  int            `json:"total"`
			Counts map[string]int `json:"counts"`
		} `json:"decisions"`
		Receipts struct {
			Count         int `json:"count"`
			SignedCount   int `json:"signed_count"`
			AnchoredCount int `json:"anchored_count"`
		} `json:"receipts"`
		Verifiability struct{ Anchored string } `json:"verifiability"`
		Summary       string                    `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pack); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}

	if pack.SchemaVersion != "kiff.proofpack.v1" {
		t.Errorf("schema_version: %q", pack.SchemaVersion)
	}
	if pack.Disclaimer == "" {
		t.Error("disclaimer must be present (authorized-not-performed)")
	}
	if pack.Control == nil || pack.Control.Action != "REFUND_ORDER" || pack.Control.Status != "active" {
		t.Errorf("control summary wrong: %+v", pack.Control)
	}
	if pack.Contract == nil || pack.Contract.Name != "REFUND_ORDER" {
		t.Errorf("contract missing/wrong: %+v", pack.Contract)
	}
	if pack.StateMachine == nil || len(pack.StateMachine.States) != 2 {
		t.Errorf("state machine missing/wrong: %+v", pack.StateMachine)
	}
	if _, ok := pack.Policy["ops_agent"]; !ok {
		t.Errorf("policy should include ops_agent (grants orders.refund); got %+v", pack.Policy)
	}
	// 3 REFUND_ORDER decisions in window; AUTO_REFUND and the
	// 90-day-old one excluded.
	if pack.Decisions.Total != 3 {
		t.Errorf("decisions total: got %d want 3", pack.Decisions.Total)
	}
	if pack.Decisions.Counts["allowed"] != 1 || pack.Decisions.Counts["blocked"] != 1 || pack.Decisions.Counts["approval_required"] != 1 {
		t.Errorf("decision counts wrong: %+v", pack.Decisions.Counts)
	}
	if pack.Receipts.Count != 2 || pack.Receipts.SignedCount != 2 || pack.Receipts.AnchoredCount != 1 {
		t.Errorf("receipts wrong: %+v", pack.Receipts)
	}
	// 1 of 2 receipts anchored → "partial".
	if pack.Verifiability.Anchored != "partial" {
		t.Errorf("verifiability anchored: got %q want partial", pack.Verifiability.Anchored)
	}
	if pack.Summary == "" {
		t.Error("summary must be present")
	}
}

// TestProofPack_UnknownActionStillReturnsHonestPack: an action the
// tenant never used returns a 200 pack with zero decisions/receipts
// and an honest "none" verifiability note — not a 500.
func TestProofPack_UnknownActionEmptyButHonest(t *testing.T) {
	t.Parallel()
	rig := newProofPackRig(t)

	rec := rig.get(t, "/v1/me/proof-pack/REFUND_ORDER")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var pack struct {
		Decisions struct {
			Total int `json:"total"`
		} `json:"decisions"`
		Receipts struct {
			Count int `json:"count"`
		} `json:"receipts"`
		Verifiability struct{ Anchored, Note string } `json:"verifiability"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pack); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pack.Decisions.Total != 0 || pack.Receipts.Count != 0 {
		t.Errorf("expected empty evidence; got decisions=%d receipts=%d", pack.Decisions.Total, pack.Receipts.Count)
	}
	if pack.Verifiability.Anchored != "none" || pack.Verifiability.Note == "" {
		t.Errorf("expected honest none verifiability; got %+v", pack.Verifiability)
	}
}

// TestProofPack_RejectsBadWindow: a malformed ?from= is a 400, and
// to<from is a 400.
func TestProofPack_RejectsBadWindow(t *testing.T) {
	t.Parallel()
	rig := newProofPackRig(t)

	if rec := rig.get(t, "/v1/me/proof-pack/REFUND_ORDER?from=not-a-date"); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed from: status %d want 400", rec.Code)
	}
	if rec := rig.get(t, "/v1/me/proof-pack/REFUND_ORDER?from=2026-06-07T00:00:00Z&to=2026-06-01T00:00:00Z"); rec.Code != http.StatusBadRequest {
		t.Errorf("to<from: status %d want 400", rec.Code)
	}
}

func mustSaveReceipt(t *testing.T, store *receipt.MemoryStore, r receipt.Receipt) {
	t.Helper()
	if err := store.Save(context.Background(), r); err != nil {
		t.Fatalf("save receipt %s: %v", r.TraceID, err)
	}
}

// When every receipt in the window is anchored, verifiability reads
// "all" and the disclaimer is present.
func TestProofPack_AllAnchoredVerifiability(t *testing.T) {
	t.Parallel()
	rig := newProofPackRig(t)
	now := time.Now().UTC()

	for _, tr := range []string{"a1", "a2"} {
		mustSaveReceipt(t, rig.receipts, receipt.Receipt{
			TenantID: rig.tenantID, TraceID: tr, SchemaVersion: "kiff.receipt.v1",
			EntityID: "o1", EntityType: "Order", ActionName: "REFUND_ORDER", IssuedAt: now.Add(-time.Hour),
			Attestation: &receipt.Attestation{Algorithm: receipt.AttestationAlgorithm, Signature: "0xsig", WalletAddress: "0xabc", AnchorTxHash: "0xtx", AnchoredAt: now},
		})
	}

	rec := rig.get(t, "/v1/me/proof-pack/REFUND_ORDER")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var pack struct {
		Disclaimer    string                    `json:"disclaimer"`
		Verifiability struct{ Anchored string } `json:"verifiability"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pack); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pack.Verifiability.Anchored != "all" {
		t.Errorf("verifiability anchored: got %q want all", pack.Verifiability.Anchored)
	}
	if pack.Disclaimer == "" {
		t.Error("disclaimer must be present")
	}
}

// A proof pack holds every receipt of its action and window, whatever the
// entity is named: a real "proof-order-…" entity is in it, and counted.
func TestProofPack_KeepsAReceiptWhateverItsEntityIsNamed(t *testing.T) {
	t.Parallel()
	rig := newProofPackRig(t)
	now := time.Now().UTC()
	for i, entity := range []string{"o1", "proof-order-xyz"} {
		mustSaveReceipt(t, rig.receipts, receipt.Receipt{
			TenantID: rig.tenantID, TraceID: "trace-" + entity, SchemaVersion: "kiff.receipt.v1",
			EntityID: entity, EntityType: "Order", ActionName: "REFUND_ORDER", IssuedAt: now.Add(-time.Duration(i+1) * time.Hour),
			Attestation: &receipt.Attestation{Algorithm: receipt.AttestationAlgorithm, Signature: "0xsig", WalletAddress: "0xabc"},
		})
	}
	rec := rig.get(t, "/v1/me/proof-pack/REFUND_ORDER")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var pack struct {
		Receipts struct {
			Count       int `json:"count"`
			SignedCount int `json:"signed_count"`
		} `json:"receipts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pack); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pack.Receipts.Count != 2 || pack.Receipts.SignedCount != 2 || !strings.Contains(rec.Body.String(), "proof-order-xyz") {
		t.Fatalf("receipts %d/%d, want both: %s", pack.Receipts.Count, pack.Receipts.SignedCount, rec.Body.String())
	}
}
