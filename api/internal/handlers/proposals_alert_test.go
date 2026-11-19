package handlers_test

import (
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/handlers"
)

// recordingAlerter captures the events the decide path notifies.
type recordingAlerter struct {
	mu     sync.Mutex
	events []alert.Event
}

func (a *recordingAlerter) Notify(ev alert.Event) {
	a.mu.Lock()
	a.events = append(a.events, ev)
	a.mu.Unlock()
}

func (a *recordingAlerter) all() []alert.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]alert.Event(nil), a.events...)
}

// Consequential outcomes (approval_required, blocked) fire an alert
// carrying the decision essentials; allowed does not.
func TestProposals_AlertsOnConsequentialOutcomes(t *testing.T) {
	t.Parallel()
	al := &recordingAlerter{}
	rig := newIdemRig(t, 24*time.Hour, handlers.WithProposalsAlerter(al))

	// allowed → no alert.
	_ = rig.driveJSON(t, map[string]any{
		"entity_id": "order-idem", "entity_type": "Order",
		"action_name": "AUTO_REFUND", "actor_id": "agent",
		"parameters": map[string]any{"amount": 100, "reason": "x"},
	}, "")

	// approval_required → alert with the handle.
	_ = rig.driveJSON(t, map[string]any{
		"entity_id": "order-idem", "entity_type": "Order",
		"action_name": "REFUND_ORDER", "actor_id": "agent",
		"parameters": map[string]any{"amount": 9900, "reason": "policy"},
	}, "")

	// blocked (state_not_allowed: order-new is not PAID) → alert.
	_ = rig.driveJSON(t, map[string]any{
		"entity_id": "order-new", "entity_type": "Order",
		"action_name": "AUTO_REFUND", "actor_id": "agent",
		"parameters": map[string]any{"amount": 100, "reason": "x"},
	}, "")

	events := al.all()
	if len(events) != 2 {
		t.Fatalf("expected 2 alerts (approval_required + blocked); got %d: %+v", len(events), events)
	}

	byOutcome := map[string]alert.Event{}
	for _, e := range events {
		byOutcome[e.Outcome] = e
	}
	ar, ok := byOutcome["approval_required"]
	if !ok {
		t.Fatal("missing approval_required alert")
	}
	if ar.Action != "REFUND_ORDER" || ar.EntityID != "order-idem" {
		t.Errorf("approval alert essentials wrong: %+v", ar)
	}
	if ar.Approval == nil || ar.Approval.ID == "" || ar.Approval.ReviewURL == "" {
		t.Errorf("approval alert should carry the handle: %+v", ar.Approval)
	}
	bl, ok := byOutcome["blocked"]
	if !ok {
		t.Fatal("missing blocked alert")
	}
	if bl.Action != "AUTO_REFUND" || len(bl.Reasons) == 0 {
		t.Errorf("blocked alert essentials wrong: %+v", bl)
	}
}

// No alerter wired → decide still works, just no notifications.
func TestProposals_NoAlerterIsFine(t *testing.T) {
	t.Parallel()
	rig := newIdemRig(t, 24*time.Hour)
	rec := rig.driveJSON(t, map[string]any{
		"entity_id": "order-idem", "entity_type": "Order",
		"action_name": "REFUND_ORDER", "actor_id": "agent",
		"parameters": map[string]any{"amount": 9900, "reason": "policy"},
	}, "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}
