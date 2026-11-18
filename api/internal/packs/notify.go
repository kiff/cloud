package packs

import (
	"context"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
)

// Answered tells the account's alert targets that a member's request was
// answered, as declining and withdrawing do: the request (EntityID), its
// pack and status (Reasons), and in Request who asked and an admin's note,
// so the app tells the member alone.
func Answered(n alert.Notifier, r Request, at time.Time) {
	Tell(n, r, alert.PackRequestAnswered, at)
}

// Tell sends one of a request's alerts: pack_requested when a member asks,
// pack_request_answered when it's answered.
func Tell(n alert.Notifier, r Request, outcome string, at time.Time) {
	if n == nil {
		return
	}
	n.Notify(alert.Event{TenantID: r.TenantID, Action: "tafs.pack_request", EntityID: r.ID, EntityType: "pack_request",
		Outcome: outcome, Reasons: []string{r.Pack, r.Status}, DecidedAt: at,
		Request: &alert.PackRequest{ID: r.ID, Kind: r.kind(), Pack: r.Pack, Status: r.Status, AskedBy: r.AskedBy, Note: r.Note}})
}

// TellApproved tells the member that the pack recorded as entryID approved
// their request: only when the request reads approved by that pack, so a
// purchase that came after another answer says nothing. Best effort.
func TellApproved(ctx context.Context, requests Requests, n alert.Notifier, tenantID, requestID, entryID string, at time.Time) {
	if requests == nil || n == nil || requestID == "" {
		return
	}
	r, err := requests.Get(ctx, tenantID, requestID, at)
	if err != nil || r.Status != RequestApproved || r.PackID != entryID {
		return
	}
	r.TenantID = tenantID
	Answered(n, r, at)
}
