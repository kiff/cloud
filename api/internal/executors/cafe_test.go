package executors_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kiff/kiff/pkg/kiff/action"

	"github.com/kiff/cloud/api/internal/executors"
)

// Test_Cafe_StartShift_HappyPath asserts the start-shift executor
// emits a SHIFT_STARTED follow-up event with opened_by carried
// through.
func Test_Cafe_StartShift_HappyPath(t *testing.T) {
	t.Parallel()
	res, err := executors.CafeStartShift(context.Background(), action.ActionContext{
		ActionName: "START_SHIFT",
		EntityID:   "shift-1",
		Parameters: map[string]any{"opened_by": "shift-manager"},
	})
	if err != nil {
		t.Fatalf("CafeStartShift: %v", err)
	}
	if res.Status != action.ExecutionSucceeded {
		t.Fatalf("Status: got %q, want succeeded", res.Status)
	}
	if len(res.FollowUpEvents) != 1 || res.FollowUpEvents[0].Type != "SHIFT_STARTED" {
		t.Fatalf("expected single SHIFT_STARTED follow-up, got %+v", res.FollowUpEvents)
	}
	if res.FollowUpEvents[0].Payload["opened_by"] != "shift-manager" {
		t.Errorf("opened_by not propagated: %+v", res.FollowUpEvents[0].Payload)
	}
}

func Test_Cafe_StartShift_MissingOpenedBy(t *testing.T) {
	t.Parallel()
	_, err := executors.CafeStartShift(context.Background(), action.ActionContext{
		ActionName: "START_SHIFT",
		EntityID:   "shift-1",
		Parameters: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected ErrMissingParameter")
	}
}

func Test_Cafe_AutoOrderInventory_HappyPath(t *testing.T) {
	t.Parallel()
	res, err := executors.CafeAutoOrderInventory(context.Background(), action.ActionContext{
		ActionName: "AUTO_ORDER_INVENTORY",
		EntityID:   "shift-1",
		Parameters: map[string]any{
			"item_id":      "napkins",
			"quantity":     float64(50),
			"amount_cents": float64(4000),
		},
	})
	if err != nil {
		t.Fatalf("CafeAutoOrderInventory: %v", err)
	}
	if res.Output["path"] != "auto" {
		t.Errorf("expected path=auto, got %v", res.Output["path"])
	}
}

func Test_Cafe_OrderInventory_HappyPath(t *testing.T) {
	t.Parallel()
	res, err := executors.CafeOrderInventory(context.Background(), action.ActionContext{
		ActionName: "ORDER_INVENTORY",
		EntityID:   "shift-1",
		Parameters: map[string]any{
			"item_id":      "coffee_beans",
			"quantity":     float64(20),
			"amount_cents": float64(15000),
		},
	})
	if err != nil {
		t.Fatalf("CafeOrderInventory: %v", err)
	}
	if res.Output["path"] != "approved" {
		t.Errorf("expected path=approved, got %v", res.Output["path"])
	}
}

func Test_Cafe_OrderInventory_RejectsBadAmount(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"missing", map[string]any{"item_id": "napkins", "quantity": float64(1)}},
		{"zero", map[string]any{"item_id": "napkins", "amount_cents": float64(0)}},
		{"negative", map[string]any{"item_id": "napkins", "amount_cents": float64(-1)}},
		{"non-integer-float", map[string]any{"item_id": "napkins", "amount_cents": 12.5}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := executors.CafeOrderInventory(context.Background(), action.ActionContext{
				EntityID:   "shift-1",
				Parameters: c.params,
			})
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func Test_Cafe_RequestSpecialty_RejectsOffCatalog(t *testing.T) {
	t.Parallel()
	_, err := executors.CafeRequestSpecialty(context.Background(), action.ActionContext{
		ActionName: "REQUEST_SPECIALTY",
		EntityID:   "shift-1",
		Parameters: map[string]any{
			"item_id":   "single-origin-yirgacheffe",
			"rationale": "regular customer asked",
		},
	})
	if !errors.Is(err, executors.ErrCafeNotInCatalog) {
		t.Fatalf("expected ErrCafeNotInCatalog, got %v", err)
	}
}

func Test_Cafe_RequestSpecialty_AcceptsInCatalog(t *testing.T) {
	t.Parallel()
	res, err := executors.CafeRequestSpecialty(context.Background(), action.ActionContext{
		ActionName: "REQUEST_SPECIALTY",
		EntityID:   "shift-1",
		Parameters: map[string]any{
			"item_id":   "oat_milk",
			"rationale": "anticipated demand",
		},
	})
	if err != nil {
		t.Fatalf("CafeRequestSpecialty: %v", err)
	}
	if res.Status != action.ExecutionSucceeded {
		t.Errorf("Status: got %q, want succeeded", res.Status)
	}
}

func Test_Cafe_SendStaffMessage_AfterHoursRejected(t *testing.T) {
	t.Parallel()
	_, err := executors.CafeSendStaffMessage(context.Background(), action.ActionContext{
		ActionName: "SEND_STAFF_MESSAGE",
		EntityID:   "shift-1",
		Parameters: map[string]any{
			"recipient":     "barista-3",
			"message":       "please cover the close",
			"sent_at_local": "2026-05-23T23:30:00Z",
		},
	})
	if !errors.Is(err, executors.ErrCafeAfterHours) {
		t.Fatalf("expected ErrCafeAfterHours, got %v", err)
	}
}

func Test_Cafe_SendStaffMessage_InHoursAccepted(t *testing.T) {
	t.Parallel()
	res, err := executors.CafeSendStaffMessage(context.Background(), action.ActionContext{
		ActionName: "SEND_STAFF_MESSAGE",
		EntityID:   "shift-1",
		Parameters: map[string]any{
			"recipient":     "barista-3",
			"message":       "please cover the open",
			"sent_at_local": "2026-05-23T09:30:00Z",
		},
	})
	if err != nil {
		t.Fatalf("CafeSendStaffMessage: %v", err)
	}
	if res.Status != action.ExecutionSucceeded {
		t.Errorf("Status: got %q, want succeeded", res.Status)
	}
}

func Test_Cafe_SendStaffMessage_AcceptsHourInt(t *testing.T) {
	t.Parallel()
	_, err := executors.CafeSendStaffMessage(context.Background(), action.ActionContext{
		ActionName: "SEND_STAFF_MESSAGE",
		EntityID:   "shift-1",
		Parameters: map[string]any{
			"recipient":     "barista-3",
			"message":       "ok",
			"sent_at_local": float64(10), // JSON numbers come in as float64
		},
	})
	if err != nil {
		t.Fatalf("CafeSendStaffMessage with hour int: %v", err)
	}
}

func Test_Cafe_SendStaffMessage_AcceptsHHMM(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		sent      string
		wantError bool
	}{
		{"in-hours", "09:30", false},
		{"early-morning", "02:14", true},
		{"boundary-open", "07:00", false},
		{"boundary-close", "22:00", true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := executors.CafeSendStaffMessage(context.Background(), action.ActionContext{
				EntityID: "shift-1",
				Parameters: map[string]any{
					"recipient":     "barista-team",
					"message":       "x",
					"sent_at_local": c.sent,
				},
			})
			gotErr := err != nil
			if gotErr != c.wantError {
				t.Fatalf("sent=%q: got err=%v, wantErr=%v", c.sent, err, c.wantError)
			}
		})
	}
}

func Test_Cafe_EscalateSupplier_HappyPath(t *testing.T) {
	t.Parallel()
	res, err := executors.CafeEscalateSupplier(context.Background(), action.ActionContext{
		ActionName: "ESCALATE_SUPPLIER",
		EntityID:   "shift-1",
		Parameters: map[string]any{
			"supplier_id": "sup-42",
			"reason":      "delivery delay",
		},
	})
	if err != nil {
		t.Fatalf("CafeEscalateSupplier: %v", err)
	}
	if len(res.FollowUpEvents) != 1 || res.FollowUpEvents[0].Type != "SUPPLIER_ESCALATED" {
		t.Fatalf("expected SUPPLIER_ESCALATED follow-up, got %+v", res.FollowUpEvents)
	}
}

func Test_Cafe_EscalateSupplier_MissingFields(t *testing.T) {
	t.Parallel()
	_, err := executors.CafeEscalateSupplier(context.Background(), action.ActionContext{
		EntityID:   "shift-1",
		Parameters: map[string]any{"supplier_id": "sup-42"},
	})
	if err == nil {
		t.Fatal("expected ErrMissingParameter")
	}
}
