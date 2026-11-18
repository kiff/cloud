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
package notify

import (
	"context"
	"html"
)

// Notifier sends customer-facing operational emails. All methods are
// best-effort from the caller's perspective: a send failure must never
// roll back the operator action that triggered it. Implementations
// return a non-nil error on failure so the caller can log the outcome.
type Notifier interface {
	// TenantSuspended notifies the workspace owner that their
	// workspace was suspended. to is the resolved owner email.
	TenantSuspended(ctx context.Context, to string) error

	// TenantUnsuspended notifies the workspace owner that their
	// workspace was reactivated.
	TenantUnsuspended(ctx context.Context, to string) error

	// AllowanceThresholdReached notifies the workspace owner that
	// their paid plan's included monthly governed-operation allowance
	// is being approached or exceeded (pct is the threshold crossed,
	// e.g. 80 or 100). It is a proactive billing-safety nudge, never a
	// service interruption — usage continues; overage may apply once
	// metered billing is live. to is the resolved owner email.
	AllowanceThresholdReached(ctx context.Context, to string, pct, used, limit int) error

	// BudgetReached notifies the workspace owner that the
	// governed-operation budget they set has been reached. Like the
	// allowance alert it is informational only — a budget never gates
	// the runtime; usage continues.
	BudgetReached(ctx context.Context, to string, used, budget int) error

	// SupportTicketUpdated notifies the ticket submitter that an
	// operator has responded to (or resolved) their private support
	// ticket. It is a close-the-loop nudge only: ticketURL links back
	// to the in-app thread, and the email deliberately carries NO
	// ticket content — the reply body lives behind auth, never in the
	// email. to is the submitter's email; ticketURL is the absolute
	// (or, when the app base URL is unset, relative) link to the
	// ticket.
	SupportTicketUpdated(ctx context.Context, to, ticketURL string) error

	// ActionHeld tells a Card's owner that an agent's action outside the
	// Card is waiting for their answer. The email carries
	// links to the hold in KIFF and no way to answer by itself: the owner
	// signs in and answers there.
	ActionHeld(ctx context.Context, to string, h HeldAction) error
}

// Customer-safe copy. Calm, honest, and deliberately free of any
// internal operator reason — the suspend/unsuspend reason stays in the
// operator audit log and never reaches the customer. These are the
// single source of truth for the wording.
const (
	subjectSuspended   = "Your KIFF workspace has been temporarily suspended"
	subjectUnsuspended = "Your KIFF workspace has been reactivated"

	bodySuspended = "Your KIFF workspace has been temporarily suspended. " +
		"This can happen when KIFF detects activity that needs review. " +
		"If you believe this is unexpected, reply to this email or contact support@kiff.dev."

	bodyUnsuspended = "Your KIFF workspace has been reactivated. " +
		"You can use KIFF again."

	subjectSupportUpdated = "There's an update on your KIFF support ticket"

	// Close-the-loop copy. Deliberately content-free: it says an
	// update exists and points back to the in-app thread. The reply
	// itself never travels by email.
	bodySupportUpdated = "There's a new update on your KIFF support ticket."
)

// htmlBody wraps a plain-text paragraph in a minimal, well-formed HTML
// document. No tracking, no images, no external assets — a calm
// transactional notice.
//
// SAFETY: paragraph is concatenated into the HTML without escaping, so
// it MUST be one of the package-level body constants above and never
// caller-supplied or customer-derived text. Those constants are static
// and contain no HTML metacharacters, so there is no injection surface.
// If this ever needs to render dynamic text, switch to html/template.
func htmlBody(paragraph string) string {
	return `<!doctype html><html><body style="font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;font-size:15px;line-height:1.5;color:#1a1a1a">` +
		`<p>` + paragraph + `</p>` +
		`<p style="color:#666;font-size:13px">— KIFF Operations</p>` +
		`</body></html>`
}

// htmlBodyLink renders intro text followed by a single call-to-action
// link. intro and cta MUST be static package copy (no metacharacters);
// url is the only dynamic input and is HTML-escaped before it reaches
// the href, so a stray metacharacter in the id could never break out
// of the attribute.
func htmlBodyLink(intro, url, cta string) string {
	safeURL := html.EscapeString(url)
	return `<!doctype html><html><body style="font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;font-size:15px;line-height:1.5;color:#1a1a1a">` +
		`<p>` + intro + `</p>` +
		`<p><a href="` + safeURL + `">` + cta + `</a></p>` +
		`<p style="color:#666;font-size:13px">— KIFF Operations</p>` +
		`</body></html>`
}

// NoopNotifier is the disabled Notifier: it sends nothing and always
// succeeds. Used when Resend is not configured (local/dev/tests) so
// the control-plane flow runs identically without email.
type NoopNotifier struct{}

// NewNoopNotifier returns a disabled notifier.
func NewNoopNotifier() NoopNotifier { return NoopNotifier{} }

// TenantSuspended implements Notifier (no-op).
func (NoopNotifier) TenantSuspended(context.Context, string) error { return nil }

// TenantUnsuspended implements Notifier (no-op).
func (NoopNotifier) TenantUnsuspended(context.Context, string) error { return nil }

// AllowanceThresholdReached implements Notifier (no-op).
func (NoopNotifier) AllowanceThresholdReached(context.Context, string, int, int, int) error {
	return nil
}

// BudgetReached implements Notifier (no-op).
func (NoopNotifier) BudgetReached(context.Context, string, int, int) error { return nil }

// SupportTicketUpdated implements Notifier (no-op).
func (NoopNotifier) SupportTicketUpdated(context.Context, string, string) error { return nil }

// Compile-time interface assertions.
var _ Notifier = NoopNotifier{}
var _ Notifier = (*ResendNotifier)(nil)
