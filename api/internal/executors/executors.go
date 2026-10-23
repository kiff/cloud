// Package executors contains cloud-side action executors that
// tenant-authored kiff.yaml files reference by string key.
//
// The executor registry is controlled by the cloud binary. Tenants
// reference executors by the keys exported here (refund.issue_refund,
// support.escalate, and so on); they cannot register their own Go
// code. This is the trust boundary.
//
// These are deliberate reimplementations of the framework's example
// executors, not imports. The framework's demo code is unexported
// (closures inside Builder calls) and ships under a different release
// schedule. Reimplementing here keeps the cloud independent.
package executors

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kiff/kiff/pkg/kiff/action"
	"github.com/kiff/kiff/pkg/kiff/event"
)

// ErrMissingParameter is returned by an executor when a required
// parameter is absent. The runtime would normally catch this through
// the contract's RequiredParameters check; the executors guard
// regardless because the contract may evolve.
var ErrMissingParameter = errors.New("executor: required parameter missing")

// orderEvent constructs a follow-up event for the refund domain. The
// runtime ingests follow-up events on successful execution per the
// framework's contract.
func orderEvent(entityID, eventType, actorID string, payload map[string]any) event.Event {
	return event.Event{
		ID:         fmt.Sprintf("ev-%s-%d", eventType, time.Now().UnixNano()),
		Type:       eventType,
		EntityID:   entityID,
		EntityType: "order",
		Source:     "cloud-executor",
		ActorID:    actorID,
		OccurredAt: time.Now().UTC(),
		Payload:    payload,
	}
}

// supportEvent constructs a follow-up event for the support-ops
// domain.
func supportEvent(entityID, eventType, actorID string, payload map[string]any) event.Event {
	return event.Event{
		ID:         fmt.Sprintf("ev-%s-%d", eventType, time.Now().UnixNano()),
		Type:       eventType,
		EntityID:   entityID,
		EntityType: "ticket",
		Source:     "cloud-executor",
		ActorID:    actorID,
		OccurredAt: time.Now().UTC(),
		Payload:    payload,
	}
}

// MarkPaid records that an order has been paid. Low risk, no
// approval required.
func MarkPaid(_ context.Context, ctx action.ActionContext) (action.ActionResult, error) {
	paymentID, ok := ctx.Parameters["payment_id"].(string)
	if !ok || paymentID == "" {
		return action.FailedResult(ctx.ActionName, ctx.EntityID, ErrMissingParameter), ErrMissingParameter
	}
	return action.ActionResult{
		ActionName:     ctx.ActionName,
		EntityID:       ctx.EntityID,
		Status:         action.ExecutionSucceeded,
		Executed:       true,
		Message:        fmt.Sprintf("payment %s captured", paymentID),
		EffectsSummary: "marked order paid",
		FollowUpEvents: []event.Event{
			orderEvent(ctx.EntityID, "ORDER_PAID", ctx.Actor.ID, map[string]any{"payment_id": paymentID}),
		},
		ExecutedAt: time.Now().UTC(),
	}, nil
}

// IssueRefund processes a refund. High risk, approval required.
func IssueRefund(_ context.Context, ctx action.ActionContext) (action.ActionResult, error) {
	amount, _ := ctx.Parameters["amount"].(float64)
	reason, _ := ctx.Parameters["reason"].(string)
	if amount <= 0 || reason == "" {
		return action.FailedResult(ctx.ActionName, ctx.EntityID, ErrMissingParameter), ErrMissingParameter
	}
	return action.ActionResult{
		ActionName:     ctx.ActionName,
		EntityID:       ctx.EntityID,
		Status:         action.ExecutionSucceeded,
		Executed:       true,
		Message:        fmt.Sprintf("refund of $%.2f issued: %s", amount, reason),
		EffectsSummary: "refund processed",
		Output:         map[string]any{"amount": amount, "reason": reason},
		FollowUpEvents: []event.Event{
			orderEvent(ctx.EntityID, "ORDER_REFUNDED", ctx.Actor.ID, map[string]any{
				"amount": amount,
				"reason": reason,
			}),
		},
		ExecutedAt: time.Now().UTC(),
	}, nil
}

// AutoRefund issues a refund without human approval, used for
// low-amount automatic refunds.
func AutoRefund(_ context.Context, ctx action.ActionContext) (action.ActionResult, error) {
	amount, _ := ctx.Parameters["amount"].(float64)
	if amount <= 0 {
		return action.FailedResult(ctx.ActionName, ctx.EntityID, ErrMissingParameter), ErrMissingParameter
	}
	return action.ActionResult{
		ActionName:     ctx.ActionName,
		EntityID:       ctx.EntityID,
		Status:         action.ExecutionSucceeded,
		Executed:       true,
		Message:        fmt.Sprintf("auto-refund of $%.2f issued", amount),
		EffectsSummary: "auto-refund processed",
		Output:         map[string]any{"amount": amount},
		FollowUpEvents: []event.Event{
			orderEvent(ctx.EntityID, "ORDER_REFUNDED", ctx.Actor.ID, map[string]any{
				"amount":    amount,
				"automatic": true,
			}),
		},
		ExecutedAt: time.Now().UTC(),
	}, nil
}

// Escalate marks a support ticket for senior review. Medium risk, no
// approval required at v0.1.
func Escalate(_ context.Context, ctx action.ActionContext) (action.ActionResult, error) {
	reason, _ := ctx.Parameters["reason"].(string)
	return action.ActionResult{
		ActionName:     ctx.ActionName,
		EntityID:       ctx.EntityID,
		Status:         action.ExecutionSucceeded,
		Executed:       true,
		Message:        "ticket escalated",
		EffectsSummary: "escalated to senior",
		Output:         map[string]any{"reason": reason},
		FollowUpEvents: []event.Event{
			supportEvent(ctx.EntityID, "TICKET_ESCALATED", ctx.Actor.ID, map[string]any{"reason": reason}),
		},
		ExecutedAt: time.Now().UTC(),
	}, nil
}

// CloseTicket closes a support ticket as resolved.
func CloseTicket(_ context.Context, ctx action.ActionContext) (action.ActionResult, error) {
	resolution, _ := ctx.Parameters["resolution"].(string)
	return action.ActionResult{
		ActionName:     ctx.ActionName,
		EntityID:       ctx.EntityID,
		Status:         action.ExecutionSucceeded,
		Executed:       true,
		Message:        "ticket closed",
		EffectsSummary: "ticket resolved",
		Output:         map[string]any{"resolution": resolution},
		FollowUpEvents: []event.Event{
			supportEvent(ctx.EntityID, "TICKET_CLOSED", ctx.Actor.ID, map[string]any{"resolution": resolution}),
		},
		ExecutedAt: time.Now().UTC(),
	}, nil
}

// SendOutreach contacts the customer outside the ticket thread.
func SendOutreach(_ context.Context, ctx action.ActionContext) (action.ActionResult, error) {
	channel, _ := ctx.Parameters["channel"].(string)
	body, _ := ctx.Parameters["body"].(string)
	if channel == "" || body == "" {
		return action.FailedResult(ctx.ActionName, ctx.EntityID, ErrMissingParameter), ErrMissingParameter
	}
	return action.ActionResult{
		ActionName:     ctx.ActionName,
		EntityID:       ctx.EntityID,
		Status:         action.ExecutionSucceeded,
		Executed:       true,
		Message:        fmt.Sprintf("outreach sent on %s", channel),
		EffectsSummary: "outreach sent",
		Output:         map[string]any{"channel": channel},
		FollowUpEvents: []event.Event{
			supportEvent(ctx.EntityID, "OUTREACH_SENT", ctx.Actor.ID, map[string]any{"channel": channel}),
		},
		ExecutedAt: time.Now().UTC(),
	}, nil
}

// WaiveFee waives a service fee for a customer. High risk, approval
// required.
func WaiveFee(_ context.Context, ctx action.ActionContext) (action.ActionResult, error) {
	amount, _ := ctx.Parameters["amount"].(float64)
	reason, _ := ctx.Parameters["reason"].(string)
	if amount <= 0 || reason == "" {
		return action.FailedResult(ctx.ActionName, ctx.EntityID, ErrMissingParameter), ErrMissingParameter
	}
	return action.ActionResult{
		ActionName:     ctx.ActionName,
		EntityID:       ctx.EntityID,
		Status:         action.ExecutionSucceeded,
		Executed:       true,
		Message:        fmt.Sprintf("fee of $%.2f waived: %s", amount, reason),
		EffectsSummary: "fee waived",
		Output:         map[string]any{"amount": amount, "reason": reason},
		FollowUpEvents: []event.Event{
			supportEvent(ctx.EntityID, "FEE_WAIVED", ctx.Actor.ID, map[string]any{
				"amount": amount,
				"reason": reason,
			}),
		},
		ExecutedAt: time.Now().UTC(),
	}, nil
}
