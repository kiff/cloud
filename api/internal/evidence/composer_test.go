package evidence_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/evidence"
	"github.com/kiff/cloud/api/internal/posture"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
)

const tenantID = "ten-1"

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func seedControl(t *testing.T, store domain.ControlStore, id string, actions []string) {
	t.Helper()
	if err := store.Create(context.Background(), domain.Control{
		ID: id, TenantID: tenantID, Domain: "refund", Entity: "Order",
		Actions: actions, Environment: "dev", Workflow: "default", Version: 1,
		Status: "active",
	}); err != nil {
		t.Fatalf("seed control: %v", err)
	}
}

// Compose for a control_id scope assembles decisions (window-complete
// counts + capped rows + precise truncation), posture, and identity.
func TestCompose_ControlScope_DecisionsAndPosture(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)

	controls := domain.NewMemoryControlStore()
	seedControl(t, controls, "ctrl-1", []string{"REFUND_ORDER"})

	reader := proposals.NewMemoryReader()
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p1", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-2 * time.Hour)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p2", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "blocked", CreatedAt: now.Add(-time.Hour)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p3", ActionName: "REFUND_ORDER", ControlID: "ctrl-other", Outcome: "allowed", CreatedAt: now})

	pr := posture.NewMemoryReader()
	pr.Append(tenantID, posture.Transition{From: "enforce", To: "shadow", ChangedBy: "alice", ChangedAt: now.Add(-48 * time.Hour)})
	pr.Append(tenantID, posture.Transition{From: "shadow", To: "enforce", ChangedBy: "bob", ChangedAt: now.Add(-24 * time.Hour)})

	c := evidence.NewComposer(
		evidence.WithControls(controls),
		evidence.WithDecisions(reader),
		evidence.WithPosture(pr),
		evidence.WithClock(fixedClock(now)),
	)

	ev, err := c.Compose(context.Background(), evidence.Input{
		TenantID:  tenantID,
		ControlID: "ctrl-1",
		Window:    evidence.Window{From: now.Add(-72 * time.Hour), To: now},
		Decisions: proposals.DecisionQuery{ControlID: "ctrl-1", From: now.Add(-72 * time.Hour), To: now},
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	if ev.Control == nil || ev.Control.ID != "ctrl-1" {
		t.Fatalf("control: %+v", ev.Control)
	}
	if ev.GeneratedAt != now {
		t.Errorf("GeneratedAt = %v, want %v", ev.GeneratedAt, now)
	}
	// Only ctrl-1's two decisions; ctrl-other excluded by the query.
	if ev.Decisions.Total != 2 || ev.Decisions.Counts["allowed"] != 1 || ev.Decisions.Counts["blocked"] != 1 {
		t.Errorf("decisions: total=%d counts=%+v", ev.Decisions.Total, ev.Decisions.Counts)
	}
	if ev.Decisions.Status != evidence.StatusComplete {
		t.Errorf("decisions status = %q", ev.Decisions.Status)
	}
	// Posture: became enforce by bob @ -24h; last change is the same.
	if ev.Posture.Status != evidence.StatusComplete {
		t.Errorf("posture status = %q", ev.Posture.Status)
	}
	if ev.Posture.BecameEnforceBy != "bob" || !ev.Posture.BecameEnforceAt.Equal(now.Add(-24*time.Hour)) {
		t.Errorf("posture became-enforce: %+v", ev.Posture)
	}
	if ev.Posture.Current != "enforce" {
		t.Errorf("posture current = %q, want enforce", ev.Posture.Current)
	}
	// Unwired sections are honestly unavailable.
	if ev.Receipts.Status != evidence.StatusUnavailable {
		t.Errorf("receipts status = %q, want unavailable (no store)", ev.Receipts.Status)
	}
	if ev.Approvals.Status != evidence.StatusUnavailable {
		t.Errorf("approvals status = %q, want unavailable", ev.Approvals.Status)
	}
}

// An outcome filter on the row query must NOT shrink the window-complete
// counts: the summary tiles need every outcome's count even while the rows
// are filtered to one. Rows narrow; Counts/Total stay window-complete.
func TestCompose_OutcomeFilter_CountsStayWindowComplete(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)

	controls := domain.NewMemoryControlStore()
	seedControl(t, controls, "ctrl-1", []string{"REFUND_ORDER"})

	reader := proposals.NewMemoryReader()
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "a1", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-3 * time.Hour)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "a2", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-2 * time.Hour)})
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "b1", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "blocked", CreatedAt: now.Add(-time.Hour)})

	c := evidence.NewComposer(
		evidence.WithControls(controls),
		evidence.WithDecisions(reader),
		evidence.WithClock(fixedClock(now)),
	)

	win := evidence.Window{From: now.Add(-72 * time.Hour), To: now}
	ev, err := c.Compose(context.Background(), evidence.Input{
		TenantID:  tenantID,
		ControlID: "ctrl-1",
		Window:    win,
		Decisions: proposals.DecisionQuery{ControlID: "ctrl-1", From: win.From, To: win.To, Outcomes: []string{"blocked"}},
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	// Counts + total window-complete across all outcomes (2 allowed, 1 blocked).
	if ev.Decisions.Total != 3 || ev.Decisions.Counts["allowed"] != 2 || ev.Decisions.Counts["blocked"] != 1 {
		t.Errorf("counts should stay window-complete under filter: total=%d counts=%+v", ev.Decisions.Total, ev.Decisions.Counts)
	}
	// Rows narrow to the requested outcome.
	if len(ev.Decisions.Rows) != 1 || ev.Decisions.Rows[0].Outcome != "blocked" {
		t.Errorf("rows should be filtered to blocked: %+v", ev.Decisions.Rows)
	}
	// The one blocked row is the whole blocked set → not truncated.
	if ev.Decisions.Truncated {
		t.Errorf("filtered blocked view (1 of 1) should not be truncated")
	}
}

// Compose for a control_id that doesn't exist returns ErrControlNotFound.
func TestCompose_ControlScope_NotFound(t *testing.T) {
	t.Parallel()
	controls := domain.NewMemoryControlStore()
	c := evidence.NewComposer(evidence.WithControls(controls))
	_, err := c.Compose(context.Background(), evidence.Input{TenantID: tenantID, ControlID: "nope"})
	if err != evidence.ErrControlNotFound {
		t.Fatalf("err = %v, want ErrControlNotFound", err)
	}
}

// Receipts collector filters by action + window and tallies
// signed/anchored; verifiability is computed, not asserted.
func TestCompose_Receipts_FilterAndVerifiability(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	win := evidence.Window{From: now.Add(-24 * time.Hour), To: now}

	recs := receipt.NewMemoryStore()
	mk := func(trace, action string, issued time.Time, signed, anchored bool) {
		r := receipt.Receipt{TenantID: tenantID, TraceID: trace, EntityID: "o1", ActionName: action, IssuedAt: issued, SchemaVersion: receipt.SchemaVersion}
		if signed || anchored {
			att := &receipt.Attestation{Algorithm: receipt.AttestationAlgorithm}
			if signed {
				att.Signature = "0xsig"
			}
			if anchored {
				att.AnchorTxHash = "0xtx"
			}
			r.Attestation = att
		}
		if err := recs.Save(context.Background(), r); err != nil {
			t.Fatalf("save receipt: %v", err)
		}
	}
	mk("t1", "REFUND_ORDER", now.Add(-2*time.Hour), true, true)
	mk("t2", "REFUND_ORDER", now.Add(-3*time.Hour), true, false)
	mk("t3", "OTHER_ACTION", now.Add(-time.Hour), true, true)    // wrong action
	mk("t4", "REFUND_ORDER", now.Add(-48*time.Hour), true, true) // out of window

	c := evidence.NewComposer(evidence.WithReceipts(recs), evidence.WithClock(fixedClock(now)))
	ev, err := c.Compose(context.Background(), evidence.Input{
		TenantID: tenantID, Action: "REFUND_ORDER", Window: win,
		Decisions: proposals.DecisionQuery{Action: "REFUND_ORDER", From: win.From, To: win.To},
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if ev.Receipts.Count != 2 || ev.Receipts.SignedCount != 2 || ev.Receipts.AnchoredCount != 1 {
		t.Fatalf("receipts tally: %+v", ev.Receipts)
	}
	v := ev.Verifiability()
	if v.Anchored != "partial" {
		t.Errorf("anchored = %q, want partial", v.Anchored)
	}
	if v.SignedAlgorithm != receipt.AttestationAlgorithm {
		t.Errorf("algo = %q", v.SignedAlgorithm)
	}
}

// Alerts collector enriches decision rows by proposal_id (G4 join key).
func TestCompose_Alerts_EnrichByProposalID(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)

	reader := proposals.NewMemoryReader()
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p1", ActionName: "REFUND_ORDER", Outcome: "blocked", CreatedAt: now})

	deliveries := alert.NewMemoryDeliveryStore()
	_ = deliveries.RecordDelivery(context.Background(), alert.Delivery{ID: "d1", TenantID: tenantID, ProposalID: "p1", TargetID: "t-ok", Status: alert.DeliveryDelivered, AttemptedAt: now})
	_ = deliveries.RecordDelivery(context.Background(), alert.Delivery{ID: "d2", TenantID: tenantID, ProposalID: "p1", TargetID: "t-blk", Status: alert.DeliveryBlocked, AttemptedAt: now})

	c := evidence.NewComposer(
		evidence.WithDecisions(reader),
		evidence.WithDeliveries(deliveries),
		evidence.WithClock(fixedClock(now)),
	)
	ev, err := c.Compose(context.Background(), evidence.Input{
		TenantID: tenantID, Action: "REFUND_ORDER",
		Window:    evidence.Window{From: now.Add(-time.Hour), To: now},
		Decisions: proposals.DecisionQuery{Action: "REFUND_ORDER", From: now.Add(-time.Hour), To: now},
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if ev.Alerts.Status != evidence.StatusComplete || len(ev.Alerts.Deliveries) != 2 {
		t.Fatalf("alerts: %+v", ev.Alerts)
	}
}

// An empty window honestly reports empty (not unavailable) when the
// stores are wired but have nothing to show.
func TestCompose_EmptySections(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	reader := proposals.NewMemoryReader()
	recs := receipt.NewMemoryStore()
	pr := posture.NewMemoryReader()

	c := evidence.NewComposer(
		evidence.WithDecisions(reader),
		evidence.WithReceipts(recs),
		evidence.WithPosture(pr),
		evidence.WithClock(fixedClock(now)),
	)
	ev, err := c.Compose(context.Background(), evidence.Input{
		TenantID: tenantID, Action: "REFUND_ORDER",
		Window:    evidence.Window{From: now.Add(-time.Hour), To: now},
		Decisions: proposals.DecisionQuery{Action: "REFUND_ORDER", From: now.Add(-time.Hour), To: now},
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if ev.Decisions.Status != evidence.StatusEmpty {
		t.Errorf("decisions status = %q, want empty", ev.Decisions.Status)
	}
	if ev.Receipts.Status != evidence.StatusEmpty {
		t.Errorf("receipts status = %q, want empty", ev.Receipts.Status)
	}
	if ev.Posture.Status != evidence.StatusEmpty {
		t.Errorf("posture status = %q, want empty", ev.Posture.Status)
	}
}

// errPosture is a posture.Reader that always errors, to exercise the
// secondary-section degradation path.
type errPosture struct{}

func (errPosture) Transitions(context.Context, string) ([]posture.Transition, error) {
	return nil, errBoom
}

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom" }

// Control-scope receipts are resolved per decision via the explicit
// proposal->receipt link. A receipt tied to a decision
// in scope surfaces; a receipt with no tying decision does NOT — that's
// the mechanical association that replaced the cross-control-leaky
// action+window list.
func TestCompose_ControlScope_ReceiptsTiedToDecisions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	win := evidence.Window{From: now.Add(-24 * time.Hour), To: now}
	controls := domain.NewMemoryControlStore()
	seedControl(t, controls, "ctrl-1", []string{"REFUND_ORDER"})

	// One allowed decision for ctrl-1, proposal p-tied.
	reader := proposals.NewMemoryReader()
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p-tied", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", EntityID: "order-1", CreatedAt: now.Add(-time.Hour)})

	recs := receipt.NewMemoryStore()
	// Receipt tied to the decision (proposal_id p-tied) — must surface.
	if err := recs.Save(context.Background(), receipt.Receipt{
		SchemaVersion: receipt.SchemaVersion, TenantID: tenantID, TraceID: "exec-tied", ProposalID: "p-tied",
		EntityID: "order-1", ActionName: "REFUND_ORDER", IssuedAt: now.Add(-50 * time.Minute),
		Attestation: &receipt.Attestation{Algorithm: receipt.AttestationAlgorithm, Signature: "0xsig"},
	}); err != nil {
		t.Fatalf("save tied receipt: %v", err)
	}
	// Receipt NOT tied to any decision in scope — must NOT surface.
	if err := recs.Save(context.Background(), receipt.Receipt{
		SchemaVersion: receipt.SchemaVersion, TenantID: tenantID, TraceID: "exec-untied", ProposalID: "p-other",
		EntityID: "order-9", ActionName: "REFUND_ORDER", IssuedAt: now.Add(-40 * time.Minute),
		Attestation: &receipt.Attestation{Algorithm: receipt.AttestationAlgorithm, Signature: "0xsig"},
	}); err != nil {
		t.Fatalf("save untied receipt: %v", err)
	}

	c := evidence.NewComposer(
		evidence.WithControls(controls),
		evidence.WithDecisions(reader),
		evidence.WithReceipts(recs),
		evidence.WithClock(fixedClock(now)),
	)
	ev, err := c.Compose(context.Background(), evidence.Input{
		TenantID: tenantID, ControlID: "ctrl-1", Window: win,
		Decisions: proposals.DecisionQuery{ControlID: "ctrl-1", From: win.From, To: win.To},
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if ev.Receipts.Status != evidence.StatusComplete {
		t.Fatalf("receipts status = %q, want complete", ev.Receipts.Status)
	}
	if ev.Receipts.Count != 1 || len(ev.Receipts.Samples) != 1 {
		t.Fatalf("receipts count = %d (want 1, only the tied receipt)", ev.Receipts.Count)
	}
	if ev.Receipts.Samples[0].TraceID != "exec-tied" {
		t.Errorf("surfaced the wrong receipt: %q (want exec-tied)", ev.Receipts.Samples[0].TraceID)
	}
}

// Control-scope receipts inherit the decisions sample's truncation: the
// section covers the decisions shown, not a window-complete receipt list.
func TestCompose_ControlScope_ReceiptsTruncationMirrorsDecisions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	win := evidence.Window{From: now.Add(-24 * time.Hour), To: now}
	controls := domain.NewMemoryControlStore()
	seedControl(t, controls, "ctrl-1", []string{"REFUND_ORDER"})

	reader := proposals.NewMemoryReader()
	for i := 0; i < 5; i++ {
		reader.Append(tenantID, proposals.DecisionRow{
			ProposalID: "p" + string(rune('a'+i)), ActionName: "REFUND_ORDER", ControlID: "ctrl-1",
			Outcome: "allowed", CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	c := evidence.NewComposer(
		evidence.WithControls(controls),
		evidence.WithDecisions(reader),
		evidence.WithReceipts(receipt.NewMemoryStore()),
		evidence.WithClock(fixedClock(now)),
	)
	ev, err := c.Compose(context.Background(), evidence.Input{
		TenantID: tenantID, ControlID: "ctrl-1", Window: win,
		Decisions: proposals.DecisionQuery{ControlID: "ctrl-1", From: win.From, To: win.To, Limit: 2}, // cap < total
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if !ev.Decisions.Truncated {
		t.Fatal("precondition: decisions should be truncated (limit 2 < 5)")
	}
	if !ev.Receipts.Truncated {
		t.Error("receipts.Truncated = false, want true (mirrors the capped decision sample)")
	}
}

// A posture read error degrades the section to Unavailable and does NOT
// fail the composition — posture is decoration on the decisions
// payload, not the payload (restores the prior /decisions contract).
//
// Invariant: When a source behind the evidence object is unavailable,
// that section is marked unavailable and the rest still answers.
// Evidence degrades section by section rather than failing whole or,
// worse, reporting an empty section as a clean one.
// Category: failure-modes
// Attack: One source behind the evidence object is unavailable when a
// report is composed
// Refused: That section is marked unavailable and the rest still answers,
// rather than an empty section reading as a clean one
func TestCompose_PostureReadError_Degrades(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	controls := domain.NewMemoryControlStore()
	seedControl(t, controls, "ctrl-1", []string{"REFUND_ORDER"})
	reader := proposals.NewMemoryReader()
	reader.Append(tenantID, proposals.DecisionRow{ProposalID: "p1", ActionName: "REFUND_ORDER", ControlID: "ctrl-1", Outcome: "allowed", CreatedAt: now.Add(-time.Hour)})

	c := evidence.NewComposer(
		evidence.WithControls(controls),
		evidence.WithDecisions(reader),
		evidence.WithPosture(errPosture{}),
		evidence.WithClock(fixedClock(now)),
	)
	ev, err := c.Compose(context.Background(), evidence.Input{
		TenantID:  tenantID,
		ControlID: "ctrl-1",
		Window:    evidence.Window{From: now.Add(-24 * time.Hour), To: now},
		Decisions: proposals.DecisionQuery{ControlID: "ctrl-1", From: now.Add(-24 * time.Hour), To: now},
	})
	if err != nil {
		t.Fatalf("Compose must not fail on a posture read error: %v", err)
	}
	if ev.Posture.Status != evidence.StatusUnavailable {
		t.Errorf("posture status = %q, want unavailable (degraded)", ev.Posture.Status)
	}
	// The primary spine still landed.
	if ev.Decisions.Total != 1 {
		t.Errorf("decisions total = %d, want 1 (spine unaffected by posture error)", ev.Decisions.Total)
	}
}

// Every receipt in the window is evidence, whatever its entity is named: a
// real `proof-of-delivery-…` entity is kept. (A demo's receipts were once
// dropped by that prefix; the demo is gone, and the filter hid real ones.)
func TestCompose_KeepsAReceiptWhateverItsEntityIsNamed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	win := evidence.Window{From: now.Add(-24 * time.Hour), To: now}

	recs := receipt.NewMemoryStore()
	for i, entity := range []string{"proof-of-delivery-7781", "invoice-9001"} {
		r := receipt.Receipt{
			SchemaVersion: receipt.SchemaVersion, TenantID: tenantID, TraceID: fmt.Sprintf("trace-%d", i), EntityID: entity,
			ActionName: "PAY_INVOICE", IssuedAt: now.Add(-time.Hour),
			Attestation: &receipt.Attestation{Algorithm: receipt.AttestationAlgorithm, Signature: "0xsig"},
		}
		if err := recs.Save(context.Background(), r); err != nil {
			t.Fatalf("save receipt: %v", err)
		}
	}
	c := evidence.NewComposer(evidence.WithReceipts(recs), evidence.WithClock(fixedClock(now)))
	ev, err := c.Compose(context.Background(), evidence.Input{
		TenantID: tenantID, Action: "PAY_INVOICE", Window: win,
		Decisions: proposals.DecisionQuery{Action: "PAY_INVOICE", From: win.From, To: win.To},
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if ev.Receipts.Count != 2 || ev.Receipts.SignedCount != 2 {
		t.Fatalf("receipts = %d (signed %d), want both", ev.Receipts.Count, ev.Receipts.SignedCount)
	}
}
