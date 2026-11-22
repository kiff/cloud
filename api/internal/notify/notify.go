// Package notify sends customer-facing operational emails for KIFF
// Cloud. It is the "customer is notified" step of the
// control-plane loop: detect risk → operator acts → audit records →
// customer notified.
//
// Two implementations: ResendNotifier (real sends via Resend's HTTP
// API) and NoopNotifier (disabled / tests / local). The interface is
// small and domain-shaped so callers never assemble email bodies — the
// customer-safe copy lives here, in one place, and can never
// accidentally include an internal operator note.
//
// Every email is drawn by one layout (layout.go, the email design guide) from the
// person's own words (internal/words), in their language.
package notify

import (
	"context"

	"github.com/kiff/cloud/api/internal/words"
)

// Notifier sends customer-facing operational emails. All methods are
// best-effort from the caller's perspective: a send failure must never
// roll back the operator action that triggered it. Implementations
// return a non-nil error on failure so the caller can log the outcome.
type Notifier interface {
	// TenantSuspended notifies the account owner that their account was
	// suspended. to is the resolved owner email; lang is the account's
	// language (English when empty or unwritten).
	TenantSuspended(ctx context.Context, to string, lang words.Lang) error

	// TenantUnsuspended notifies the account owner that their account was
	// reactivated.
	TenantUnsuspended(ctx context.Context, to string, lang words.Lang) error

	// SupportTicketUpdated notifies the ticket submitter that an
	// operator has responded to (or resolved) their private support
	// ticket. It is a close-the-loop nudge only: ticketURL links back
	// to the in-app thread, and the email deliberately carries NO
	// ticket content — the reply body lives behind auth, never in the
	// email. to is the submitter's email; ticketURL is the absolute
	// (or, when the app base URL is unset, relative) link to the
	// ticket; lang is the submitter's language.
	SupportTicketUpdated(ctx context.Context, to, ticketURL string, lang words.Lang) error

	// ActionHeld tells a Card's owner that an agent's action outside the
	// Card is waiting for their answer. The email carries
	// links to the hold in KIFF and no way to answer by itself: the owner
	// signs in and answers there.
	ActionHeld(ctx context.Context, to string, h HeldAction) error
}

// NoopNotifier is the disabled Notifier: it sends nothing and always
// succeeds. Used when Resend is not configured (local/dev/tests) so
// the control-plane flow runs identically without email.
type NoopNotifier struct{}

// NewNoopNotifier returns a disabled notifier.
func NewNoopNotifier() NoopNotifier { return NoopNotifier{} }

// TenantSuspended implements Notifier (no-op).
func (NoopNotifier) TenantSuspended(context.Context, string, words.Lang) error { return nil }

// TenantUnsuspended implements Notifier (no-op).
func (NoopNotifier) TenantUnsuspended(context.Context, string, words.Lang) error { return nil }

// SupportTicketUpdated implements Notifier (no-op).
func (NoopNotifier) SupportTicketUpdated(context.Context, string, string, words.Lang) error {
	return nil
}

// Compile-time interface assertions.
var _ Notifier = NoopNotifier{}
var _ Notifier = (*ResendNotifier)(nil)
