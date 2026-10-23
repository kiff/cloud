// Package executors — cafe.go ships the cloud-side executors that
// back the ai-cafe-ops worked example (kiff/kiff#3). Tenants
// reference these by string key from kiff.yaml; the binary's
// DefaultRegistry binds them.
//
// Key contracts:
//
//	cafe.start_shift          opens a NEW shift into OPEN.
//	cafe.auto_order_inventory low-amount inventory order, no approval.
//	cafe.order_inventory      high-amount inventory order, approval required.
//	cafe.request_specialty    request a specialty item; rejects items
//	                          not on the catalog allow-list.
//	cafe.send_staff_message   message a staff member; rejects
//	                          times outside the working-hours window.
//	cafe.escalate_supplier    park the shift in AWAITING_HUMAN.
//
// The auto-vs-approval routing for inventory orders lives client-side
// — the cloud's runtime gates by approval requirement on
// the chosen action contract; the agent picks which of the two
// inventory contracts to invoke based on the daily running total.
//
// Configuration:
//
// Catalog and working-hours window are cloud-managed defaults at
// v0.1, hard-coded to match the framework's reference values. When
// per-tenant overrides land (post-M1) they will move to a tenant
// settings table; the executor code already reads through the
// helpers below so that swap is mechanical.
package executors

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kiff/kiff/pkg/kiff/action"
	"github.com/kiff/kiff/pkg/kiff/event"
)

// CafeAdapter is the adapter name the cloud expects on
// cafe.kiff.yaml-driven tenants when ingesting raw events. It
// matches the framework's AdapterCafe so cafe-ops's own seed events
// transit unchanged.
const CafeAdapter = "ai-cafe-ops"

// CafeCatalog is the cloud's default allow-list for specialty
// requests. Mirrors examples/ai-cafe-ops/domain.go's CatalogItems.
// Production will replace this with per-tenant configuration; for
// v0.1, it is global.
var CafeCatalog = map[string]struct{}{
	"napkins":          {},
	"coffee_beans":     {},
	"oat_milk":         {},
	"sugar_packets":    {},
	"paper_cups":       {},
	"to_go_lids":       {},
	"chocolate_powder": {},
}

// Working-hours window in local wall-clock hours. Inclusive start,
// exclusive end. The check reads either an integer hour 0..23 or
// the hour portion of an RFC3339 timestamp on `sent_at_local`.
const (
	cafeWorkingHoursStart = 7
	cafeWorkingHoursEnd   = 22
)

// ErrCafeNotInCatalog is returned by request_specialty when the
// requested item is missing or absent from CafeCatalog. Callers
// classifying the error can distinguish a catalog block from an
// approval-required outcome by matching errors.Is.
var ErrCafeNotInCatalog = errors.New("cafe: item is not in the cafe catalog")

// ErrCafeAfterHours is returned by send_staff_message when the
// proposal's local time falls outside the working-hours window.
var ErrCafeAfterHours = errors.New("cafe: staff messaging is blocked outside working hours")

// ErrCafeAmountInvalid is returned by an inventory executor when
// the amount_cents parameter is missing, non-positive, or not
// representable as an integer.
var ErrCafeAmountInvalid = errors.New("cafe: amount_cents must be a positive integer")

// cafeEvent constructs a follow-up event for the cafe domain.
// Mirrors the framework helper so the on-the-wire shape stays
// identical between local-mode and cloud-mode runs.
func cafeEvent(entityID, eventType, actorID string, payload map[string]any) event.Event {
	return event.Event{
		ID:         fmt.Sprintf("ev-%s-%d", eventType, time.Now().UnixNano()),
		Type:       eventType,
		EntityID:   entityID,
		EntityType: "Shift",
		Source:     "cloud-executor",
		ActorID:    actorID,
		OccurredAt: time.Now().UTC(),
		Payload:    payload,
	}
}

// CafeStartShift opens a NEW shift into OPEN.
func CafeStartShift(_ context.Context, ctx action.ActionContext) (action.ActionResult, error) {
	openedBy, _ := ctx.Parameters["opened_by"].(string)
	if strings.TrimSpace(openedBy) == "" {
		return action.FailedResult(ctx.ActionName, ctx.EntityID, ErrMissingParameter), ErrMissingParameter
	}
	return action.ActionResult{
		ActionName:     ctx.ActionName,
		EntityID:       ctx.EntityID,
		Status:         action.ExecutionSucceeded,
		Executed:       true,
		Message:        fmt.Sprintf("shift started by %s", openedBy),
		EffectsSummary: "shift opened",
		FollowUpEvents: []event.Event{
			cafeEvent(ctx.EntityID, "SHIFT_STARTED", ctx.Actor.ID, map[string]any{
				"opened_by": openedBy,
			}),
		},
		ExecutedAt: time.Now().UTC(),
	}, nil
}

// CafeAutoOrderInventory records a low-amount inventory order.
// Approval required is encoded on the contract (kiff.yaml), not in
// the executor; the cloud's runtime enforces it.
func CafeAutoOrderInventory(_ context.Context, ctx action.ActionContext) (action.ActionResult, error) {
	itemID, quantity, amount, err := readOrderParams(ctx.Parameters)
	if err != nil {
		return action.FailedResult(ctx.ActionName, ctx.EntityID, err), err
	}
	return action.ActionResult{
		ActionName:     ctx.ActionName,
		EntityID:       ctx.EntityID,
		Status:         action.ExecutionSucceeded,
		Executed:       true,
		Message:        fmt.Sprintf("auto-order %d × %s for %d cents", quantity, itemID, amount),
		EffectsSummary: "auto inventory order placed (under cap)",
		Output: map[string]any{
			"item_id":      itemID,
			"quantity":     quantity,
			"amount_cents": amount,
			"path":         "auto",
		},
		FollowUpEvents: []event.Event{
			cafeEvent(ctx.EntityID, "INVENTORY_ORDERED", ctx.Actor.ID, map[string]any{
				"item_id":      itemID,
				"quantity":     quantity,
				"amount_cents": amount,
				"path":         "auto",
			}),
		},
		ExecutedAt: time.Now().UTC(),
	}, nil
}

// CafeOrderInventory is the high-amount counterpart. Identical
// payload shape to auto; the runtime gates approval based on the
// contract.
func CafeOrderInventory(_ context.Context, ctx action.ActionContext) (action.ActionResult, error) {
	itemID, quantity, amount, err := readOrderParams(ctx.Parameters)
	if err != nil {
		return action.FailedResult(ctx.ActionName, ctx.EntityID, err), err
	}
	return action.ActionResult{
		ActionName:     ctx.ActionName,
		EntityID:       ctx.EntityID,
		Status:         action.ExecutionSucceeded,
		Executed:       true,
		Message:        fmt.Sprintf("ordered %d × %s for %d cents (approved)", quantity, itemID, amount),
		EffectsSummary: "approved inventory order placed",
		Output: map[string]any{
			"item_id":      itemID,
			"quantity":     quantity,
			"amount_cents": amount,
			"path":         "approved",
		},
		FollowUpEvents: []event.Event{
			cafeEvent(ctx.EntityID, "INVENTORY_ORDERED", ctx.Actor.ID, map[string]any{
				"item_id":      itemID,
				"quantity":     quantity,
				"amount_cents": amount,
				"path":         "approved",
			}),
		},
		ExecutedAt: time.Now().UTC(),
	}, nil
}

// CafeRequestSpecialty rejects items not on the catalog allow-list
// before any approval gate fires. The runtime catches the error
// before opening an approval; callers classify ErrCafeNotInCatalog
// distinctly from approval_required.
func CafeRequestSpecialty(_ context.Context, ctx action.ActionContext) (action.ActionResult, error) {
	if err := CafeCheckCatalog(ctx.Parameters); err != nil {
		return action.FailedResult(ctx.ActionName, ctx.EntityID, err), err
	}
	itemID, _ := ctx.Parameters["item_id"].(string)
	rationale, _ := ctx.Parameters["rationale"].(string)
	return action.ActionResult{
		ActionName:     ctx.ActionName,
		EntityID:       ctx.EntityID,
		Status:         action.ExecutionSucceeded,
		Executed:       true,
		Message:        fmt.Sprintf("specialty request recorded for %s", itemID),
		EffectsSummary: "specialty request approved",
		Output:         map[string]any{"item_id": itemID, "rationale": rationale},
		FollowUpEvents: []event.Event{
			cafeEvent(ctx.EntityID, "SPECIALTY_REQUESTED", ctx.Actor.ID, map[string]any{
				"item_id":   itemID,
				"rationale": rationale,
			}),
		},
		ExecutedAt: time.Now().UTC(),
	}, nil
}

// CafeSendStaffMessage rejects times outside the working window
// before any approval gate fires.
func CafeSendStaffMessage(_ context.Context, ctx action.ActionContext) (action.ActionResult, error) {
	if err := CafeCheckWorkingHours(ctx.Parameters); err != nil {
		return action.FailedResult(ctx.ActionName, ctx.EntityID, err), err
	}
	recipient, _ := ctx.Parameters["recipient"].(string)
	message, _ := ctx.Parameters["message"].(string)
	return action.ActionResult{
		ActionName:     ctx.ActionName,
		EntityID:       ctx.EntityID,
		Status:         action.ExecutionSucceeded,
		Executed:       true,
		Message:        fmt.Sprintf("staff message sent to %s", recipient),
		EffectsSummary: "staff message sent",
		Output:         map[string]any{"recipient": recipient, "message": message},
		FollowUpEvents: []event.Event{
			cafeEvent(ctx.EntityID, "STAFF_MESSAGED", ctx.Actor.ID, map[string]any{
				"recipient": recipient,
			}),
		},
		ExecutedAt: time.Now().UTC(),
	}, nil
}

// CafeEscalateSupplier parks a shift in AWAITING_HUMAN. No approval.
func CafeEscalateSupplier(_ context.Context, ctx action.ActionContext) (action.ActionResult, error) {
	supplierID, _ := ctx.Parameters["supplier_id"].(string)
	reason, _ := ctx.Parameters["reason"].(string)
	if supplierID == "" || reason == "" {
		return action.FailedResult(ctx.ActionName, ctx.EntityID, ErrMissingParameter), ErrMissingParameter
	}
	return action.ActionResult{
		ActionName:     ctx.ActionName,
		EntityID:       ctx.EntityID,
		Status:         action.ExecutionSucceeded,
		Executed:       true,
		Message:        fmt.Sprintf("supplier %s escalated: %s", supplierID, reason),
		EffectsSummary: "supplier escalation recorded; shift parked awaiting human",
		FollowUpEvents: []event.Event{
			cafeEvent(ctx.EntityID, "SUPPLIER_ESCALATED", ctx.Actor.ID, map[string]any{
				"supplier_id": supplierID,
				"reason":      reason,
			}),
		},
		ExecutedAt: time.Now().UTC(),
	}, nil
}

// ─── Helpers ──────────────────────────────────────────────────────

// CafeCheckCatalog enforces that item_id is present and on the
// allow-list. Exported so other code can pre-check before invoking
// the executor.
func CafeCheckCatalog(params map[string]any) error {
	value, ok := params["item_id"]
	if !ok {
		return ErrCafeNotInCatalog
	}
	itemID, ok := value.(string)
	if !ok || itemID == "" {
		return ErrCafeNotInCatalog
	}
	if _, ok := CafeCatalog[itemID]; !ok {
		return fmt.Errorf("%w: %q", ErrCafeNotInCatalog, itemID)
	}
	return nil
}

// CafeCheckWorkingHours enforces that sent_at_local sits inside the
// working window. Accepts either an integer hour 0..23 or the hour
// portion of an RFC3339 timestamp string.
func CafeCheckWorkingHours(params map[string]any) error {
	value, ok := params["sent_at_local"]
	if !ok {
		return ErrCafeAfterHours
	}
	hour, ok := readCafeHour(value)
	if !ok {
		return fmt.Errorf("%w: cannot read hour from sent_at_local", ErrCafeAfterHours)
	}
	if hour < cafeWorkingHoursStart || hour >= cafeWorkingHoursEnd {
		return fmt.Errorf("%w: hour %02d is outside %02d:00–%02d:00",
			ErrCafeAfterHours, hour, cafeWorkingHoursStart, cafeWorkingHoursEnd)
	}
	return nil
}

// readOrderParams extracts (item_id, quantity, amount_cents) with
// the same validation rules the framework's executors use. Quantity
// defaults to 1 when missing.
func readOrderParams(params map[string]any) (string, int64, int64, error) {
	itemID, _ := params["item_id"].(string)
	if strings.TrimSpace(itemID) == "" {
		return "", 0, 0, ErrMissingParameter
	}
	amount, err := readCafeIntCents(params, "amount_cents")
	if err != nil {
		return "", 0, 0, err
	}
	if amount <= 0 {
		return "", 0, 0, ErrCafeAmountInvalid
	}
	quantity := readCafeQuantity(params)
	return itemID, quantity, amount, nil
}

func readCafeIntCents(params map[string]any, key string) (int64, error) {
	value, ok := params[key]
	if !ok {
		return 0, ErrCafeAmountInvalid
	}
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case float64:
		// JSON unmarshals integers as float64; coerce when the value
		// is integer-valued.
		if v != float64(int64(v)) {
			return 0, ErrCafeAmountInvalid
		}
		return int64(v), nil
	}
	return 0, ErrCafeAmountInvalid
}

func readCafeQuantity(params map[string]any) int64 {
	value, ok := params["quantity"]
	if !ok {
		return 1
	}
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 1
}

// readCafeHour returns the hour component of value. Accepts an int
// 0..23 or an RFC3339 timestamp string; returns ok=false otherwise.
func readCafeHour(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		if v < 0 || v > 23 {
			return 0, false
		}
		return v, true
	case int64:
		if v < 0 || v > 23 {
			return 0, false
		}
		return int(v), true
	case float64:
		if v != float64(int64(v)) || v < 0 || v > 23 {
			return 0, false
		}
		return int(v), true
	case string:
		if v == "" {
			return 0, false
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.Hour(), true
		}
		// Allow "HH:MM" — the cafe-ops fixture uses this short form
		// for readability. Anything that doesn't parse as a 0..23
		// integer hour is rejected.
		var h, m int
		if _, err := fmt.Sscanf(v, "%d:%d", &h, &m); err == nil && h >= 0 && h <= 23 {
			return h, true
		}
		return 0, false
	}
	return 0, false
}
