package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/notify"
	"github.com/kiff/cloud/api/internal/owneremail"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/tenant"
)

// the cards-for-any-agent design: each held action is emailed to the account owner once, as
// it is first held. The email links to the hold on Needs you, which
// requires the owner's session; it cannot answer by itself.

// OwnerEmailResolver resolves a tenant's owner email when it is needed
// (owneremail.Resolver).
type OwnerEmailResolver interface {
	Email(ctx context.Context, tenantID string) (string, error)
}

// HoldRecipientsResolver resolves everyone a held action is emailed to
// (the governance-roles design: the account's approvers, and its admins and editors while
// separation of duties is off). owneremail.Resolver implements it.
type HoldRecipientsResolver interface {
	HoldRecipients(ctx context.Context, tenantID string) ([]string, error)
}

// HoldRecipientSubjects also says whose each address is, so the email is
// in that person's language (personlang).
type HoldRecipientSubjects interface {
	HoldRecipientsWithSubjects(ctx context.Context, tenantID string) ([]owneremail.Recipient, error)
}

// WithProposalsHoldEmail emails the owner about each newly held action.
// appBaseURL is the dashboard origin (KIFF_APP_BASE_URL) the review link
// is made absolute against. Either of owners or notifier nil disables it.
func WithProposalsHoldEmail(owners OwnerEmailResolver, notifier notify.Notifier, appBaseURL string) ProposalsOption {
	return func(h *ProposalsHandler) {
		if owners == nil || notifier == nil {
			return
		}
		h.holdOwners, h.holdNotifier = owners, notifier
		h.appBaseURL = strings.TrimRight(strings.TrimSpace(appBaseURL), "/")
	}
}

// WithProposalsHeldWords makes the held-action email speak in people's words:
// the action as it is said in the apps, the amount in the person's currency
// format, and why it is held, from the hold's own view. Without it, or when a
// hold cannot be read, the email says the action and amount as they were asked.
func WithProposalsHeldWords(src HeldWordsSource) ProposalsOption {
	return func(h *ProposalsHandler) { h.heldWords = src }
}

// SetHeldWords is WithProposalsHeldWords for a source built after the handler,
// at start-up, before the first request.
func (h *ProposalsHandler) SetHeldWords(src HeldWordsSource) { h.heldWords = src }

// notifyHeld emails the owner about an action just held for them. Best
// effort and detached: a send never delays or changes the decision.
func (h *ProposalsHandler) notifyHeld(tenantID string, prop kiffproposal.ActionProposal, d mandate.Decision) {
	if h.holdOwners == nil || h.holdNotifier == nil || !d.NewlyHeld {
		return
	}
	held := heldActionFor(prop, d, h.appBaseURL)
	dispatch := h.dispatch
	if dispatch == nil {
		dispatch = func(f func()) { go f() }
	}
	logger := h.logger
	if logger == nil {
		logger = slog.Default()
	}
	dispatch(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// The account turned these emails off in Settings → Notifications.
		// When its settings can't be read, the email goes: it is on by default.
		var account tenant.Tenant
		if h.tenantStore != nil {
			if t, err := h.tenantStore.Get(ctx, tenantID); err == nil {
				if !notifyPrefsFromMeta(t)[ruleHeldEmail] {
					logger.InfoContext(ctx, "proposals: held-action email off for the account",
						slog.String("tenant", tenantID), slog.String("exception", d.ExceptionID))
					return
				}
				account = t
			}
		}
		var recipients []owneremail.Recipient
		var err error
		switch owners := h.holdOwners.(type) {
		case HoldRecipientSubjects:
			recipients, err = owners.HoldRecipientsWithSubjects(ctx, tenantID)
		case HoldRecipientsResolver:
			var emails []string
			emails, err = owners.HoldRecipients(ctx, tenantID)
			for _, e := range emails {
				recipients = append(recipients, owneremail.Recipient{Email: e})
			}
		default:
			var to string
			to, err = h.holdOwners.Email(ctx, tenantID)
			recipients = []owneremail.Recipient{{Email: to}}
		}
		if err != nil {
			// No owner email resolvable: Needs you and the bell still
			// show it. Logged without an address.
			logger.InfoContext(ctx, "proposals: held-action email skipped",
				slog.String("tenant", tenantID), slog.String("exception", d.ExceptionID), slog.String("reason", err.Error()))
			return
		}
		for _, to := range recipients {
			// Each in their own language: the one their app last showed them.
			held.Lang = personlang.Of(account, to.Subject)
			held.Zone = accountZone(account)
			if h.heldWords != nil {
				if w, ok := h.heldWords.HeldCallWords(ctx, tenantID, d.ExceptionID, held.Lang); ok {
					held.ActionWords, held.AmountText, held.Reason = w.Action, w.Amount, w.Reason
				}
			}
			if err := h.holdNotifier.ActionHeld(ctx, to.Email, held); err != nil {
				logger.ErrorContext(ctx, "proposals: held-action email failed",
					slog.String("tenant", tenantID), slog.String("exception", d.ExceptionID), slog.String("err", err.Error()))
			}
		}
	})
}

// heldActionFor is what the email says about a held action.
func heldActionFor(prop kiffproposal.ActionProposal, d mandate.Decision, appBaseURL string) notify.HeldAction {
	h := notify.HeldAction{Agent: prop.ActorID, Action: prop.ActionName,
		ReviewURL: appBaseURL + "/needs-you/" + url.PathEscape(d.ExceptionID), NextSteps: d.NextSteps}
	if d.ExceptionExpiresAt != nil {
		h.ExpiresAt = *d.ExceptionExpiresAt
	}
	for i, b := range d.Breaches {
		unit := b.Quantity.Parameter
		if b.Quantity.Kind == mandate.KindCount {
			unit = "actions"
		}
		if i == 0 && b.Quantity.Kind == mandate.KindSum {
			h.Amount = fmt.Sprintf("%d %s", b.Draw, unit)
		}
		switch b.Outcome {
		case mandate.OutcomeResourceOutside:
			h.Outside = append(h.Outside, fmt.Sprintf("%s does not name %s %s", b.MandateID, b.Resource, quotedOrNone(b.Value)))
		case mandate.OutcomePerActionExceeded:
			h.Outside = append(h.Outside, fmt.Sprintf("%s allows up to %d %s per action", b.MandateID, b.Limit, unit))
		default:
			left := b.Limit - b.Used
			if left < 0 {
				left = 0
			}
			h.Outside = append(h.Outside, fmt.Sprintf("%s allows %d %s in its period, %d left", b.MandateID, b.Limit, unit, left))
		}
	}
	return h
}

// quotedOrNone is a resource value as the owner reads it.
func quotedOrNone(v string) string {
	if v == "" {
		return "(none sent)"
	}
	return strconv.Quote(v)
}

// accountZone is the account's own time zone, UTC when it has none or it can't be loaded.
func accountZone(t tenant.Tenant) *time.Location {
	if loc, err := time.LoadLocation(tenant.TimeZoneFor(t)); err == nil {
		return loc
	}
	return time.UTC
}
