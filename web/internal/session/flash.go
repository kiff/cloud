package session

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// FlashCookieName is the short-lived cookie carrying one-shot
// messages between handlers (e.g. "key minted, here's the
// plaintext"). Read-once: any call to ReadFlash deletes the
// cookie after returning the payload.
const FlashCookieName = "kiff_flash"

// FlashKind labels the kind of one-shot payload the cookie
// carries. Handlers branch on Kind to render the right banner.
type FlashKind string

const (
	FlashKeyRevealed   FlashKind = "key_revealed"
	FlashKeyRevoked    FlashKind = "key_revoked"
	FlashTemplateSet   FlashKind = "template_set"
	FlashEventSent     FlashKind = "event_sent"
	FlashApprovalGrant FlashKind = "approval_granted"
	FlashApprovalDeny  FlashKind = "approval_denied"
	FlashError         FlashKind = "error"
	// FlashKeyReauth says a read-only account key needs a recent
	// sign-in, with the way to sign in again and come back to API keys.
	FlashKeyReauth FlashKind = "key_reauth"
	// FlashProposalDecided carries the live result of a first
	// proposal sent from the onboarding page. The payload is the
	// JSON the gate returned (outcome + reasons + message) so
	// the onboarding page can render the actual response shape
	// the user's code will see.
	FlashProposalDecided FlashKind = "proposal_decided"
	// FlashDomainValidated carries a successful YAML validate
	// or save back to the editor. The page reads it to render a
	// "your domain looks good" / "your domain was activated"
	// banner above the editor.
	FlashDomainValidated FlashKind = "domain_validated"
	// FlashDomainInvalid carries a validation failure plus the
	// raw YAML the user just submitted, so the editor can
	// re-render their input alongside the issues. Without this,
	// a redirect after a 422 would erase the textarea.
	FlashDomainInvalid FlashKind = "domain_invalid"
	// FlashDomainCreated marks a successful create of an additional
	// domain, carried to the domain's home so the apex shows a
	// "created domain X" success banner.
	FlashDomainCreated FlashKind = "domain_created"

	// FlashDomainDeleted marks a successful domain delete, carried
	// to the apex so it shows a "deleted domain X" confirmation.
	FlashDomainDeleted FlashKind = "domain_deleted"

	// FlashDomainUpdated marks a successful in-place domain edit,
	// carried to the domain's home as a success banner.
	FlashDomainUpdated FlashKind = "domain_updated"

	// FlashYAMLLoaded carries a worked-example YAML body into
	// the editor when the user picks "load this example into
	// the editor". Page-side: pre-fill the textarea, do not
	// auto-save.
	FlashYAMLLoaded FlashKind = "yaml_loaded"

	// FlashEventIngested previously carried tenant event-
	// ingestion results from the retired Onboarding tab's
	// step 3. Currently unconsumed since the Onboarding
	// handlers retired; preserved as part of the typed
	// payload set so a future flow that ingests events from
	// the dashboard side can reuse the cookie shape. If
	// still unused after the next backlog sweep, drop it
	// then.
	FlashEventIngested FlashKind = "event_ingested_onboarding"

	// FlashSupportCreated marks a successfully filed support
	// ticket, carried to the ticket's page as a success banner.
	FlashSupportCreated FlashKind = "support_created"
	// FlashSupportReplied marks a reply the tenant just added to a
	// support ticket, carried back to the ticket's page.
	FlashSupportReplied FlashKind = "support_replied"

	// FlashNotificationSaved marks a saved change to the tenant's
	// notification preferences, shown on the settings page.
	FlashNotificationSaved FlashKind = "notification_saved"

	// FlashMandateIssued marks a limit just put in force, carried to
	// the statement for the mandate it named.
	FlashMandateIssued FlashKind = "mandate_issued"

	// FlashKeyRoleGranted marks a role just added to an agent's
	// existing key, instead of a second key being minted.
	FlashKeyRoleGranted FlashKind = "key_role_granted"

	// FlashMandateRevoked marks a limit whose authority just ended.
	FlashMandateRevoked FlashKind = "mandate_revoked"
	// FlashCardTestAllowed / FlashCardTestRefused carry the result of a
	// test request sent from a card's page: one real decision against
	// the card, which is either allowed (and draws) or refused.
	FlashCardTestAllowed FlashKind = "card_test_allowed"
	FlashCardTestRefused FlashKind = "card_test_refused"
	// FlashCardTestHeld: the test request fell outside the card and is
	// held for the owner's answer; nothing was refused.
	FlashCardTestHeld FlashKind = "card_test_held"
	// FlashOwnerAnswered: the owner answered an action held outside a
	// card (approve once, change the card, reject).
	FlashOwnerAnswered FlashKind = "owner_answered"
	// FlashToolsChanged: a connected tool was added, tested, given a
	// new credential or removed (the cards-for-any-agent design,
	// /tools).
	FlashToolsChanged FlashKind = "tools_changed"
	// FlashGatewayKeyRevealed: an agent's gateway key was created; the
	// agent's page shows it once, inside its MCP configuration.
	FlashGatewayKeyRevealed FlashKind = "gateway_key_revealed"
	// FlashCardHoldersChanged: a Card was shared with an agent, or
	// stopped being shared with one.
	FlashCardHoldersChanged FlashKind = "card_holders_changed"
	// FlashAccountDeleted: the account's deletion was accepted. Message
	// carries the grace window's end (RFC 3339), for /account/deleted.
	FlashAccountDeleted FlashKind = "account_deleted"
)

// Flash is the cookie payload. Only one of the typed payload
// fields is set at a time; the dashboard's templ page branches
// on Kind to render the right banner. Message is a free-form
// short status line shown alongside the typed payload.
type Flash struct {
	Kind     FlashKind             `json:"kind"`
	Message  string                `json:"message,omitempty"`
	Key      *FlashKeyPayload      `json:"key,omitempty"`
	Template string                `json:"template,omitempty"`
	Body     string                `json:"body,omitempty"`
	Slug     string                `json:"slug,omitempty"` // Template slug for "Run decision" button
	Decision *FlashDecisionPayload `json:"decision,omitempty"`
	Domain   *FlashDomainPayload   `json:"domain,omitempty"`
	Event    *FlashEventPayload    `json:"event,omitempty"`
}

// FlashKeyPayload is the one-time-only API key reveal carried
// by FlashKeyRevealed.
//
// Plaintext never enters the cookie: IssueFlash moves it to the
// server-side reveal vault and writes only Ref, and ReadFlash restores
// it once, for Owner's session. After the reveal is taken or has
// expired, Plaintext reads back empty.
type FlashKeyPayload struct {
	ID        string `json:"id"`
	Plaintext string `json:"-"`
	Ref       string `json:"ref,omitempty"`
	Label     string `json:"label,omitempty"`
	// Owner is the session subject that minted the key, the only one
	// that may redeem its reveal. Set by the caller; kept server-side.
	Owner string `json:"-"`
}

// FlashDecisionPayload previously carried a proposal-gate
// response across the redirect from the retired Onboarding
// flow's POST /dashboard/start/proposal back to GET
// /dashboard/start (retired both routes). Currently
// unconsumed; preserved as part of the typed payload set so a
// future surface that hits the proposal gate from the
// dashboard side can reuse the cookie shape. Mirrors apps/api
// proposalResponse.
type FlashDecisionPayload struct {
	ProposalID string   `json:"proposal_id"`
	Outcome    string   `json:"outcome"`
	Reasons    []string `json:"reasons,omitempty"`
	Message    string   `json:"message,omitempty"`
	EntityID   string   `json:"entity_id,omitempty"`
}

// FlashDomainPayload carries the result of a YAML validate /
// save / load-example interaction back to the editor across a
// redirect.
//
// YAML is preserved on the invalid + load-example paths so the
// textarea re-renders the body the user worked on (or the
// example they asked to see) without an extra round-trip.
//
// Issues carries the validation problems the cloud surfaced.
// Each issue carries a short path (e.g. "actions[0]") and a
// human-readable message.
type FlashDomainPayload struct {
	Domain  string             `json:"domain,omitempty"`
	Entity  string             `json:"entity,omitempty"`
	Version int                `json:"version,omitempty"`
	YAML    string             `json:"yaml,omitempty"`
	Issues  []FlashDomainIssue `json:"issues,omitempty"`
}

// FlashDomainIssue mirrors apiclient.DomainValidationIssue at
// the cookie boundary so the session package stays free of
// apiclient imports.
type FlashDomainIssue struct {
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// FlashEventPayload carries the result of a tenant event
// ingestion from the onboarding page across a redirect.
// EntityID names the entity the event targeted; NewState is
// the state the entity is in after the event applied (read
// from the timeline's most recent state_changed row's "to"
// field, or empty when no transition fired). EventType is the
// event type the user submitted, echoed back so the banner can
// say "ORDER_PAID ingested" rather than just "event ingested".
type FlashEventPayload struct {
	EntityID  string `json:"entity_id"`
	EventType string `json:"event_type,omitempty"`
	NewState  string `json:"new_state,omitempty"`
}

// IssueFlash writes a flash cookie. Same HMAC + base64url
// signing as the session cookie, sharing the manager's secret.
// Lifetime is 5 minutes — long enough to survive a redirect,
// short enough that a stolen cookie is mostly useless.
func (m *Manager) IssueFlash(w http.ResponseWriter, f Flash) error {
	if f.Kind == "" {
		return errors.New("session: flash kind is required")
	}
	if f.Key != nil && f.Key.Plaintext != "" {
		key := *f.Key
		ref, err := m.reveals.put(key.Plaintext, key.Owner, m.now())
		if err != nil {
			return err
		}
		key.Ref, key.Plaintext = ref, ""
		f.Key = &key
	}
	body, err := json.Marshal(f)
	if err != nil {
		return err
	}
	enc := encodeRaw(body, m.secret)
	http.SetCookie(w, &http.Cookie{
		Name:     FlashCookieName,
		Value:    enc,
		Path:     "/",
		Domain:   m.cookieDomain,
		Expires:  m.now().Add(5 * time.Minute),
		HttpOnly: true,
		Secure:   m.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// ReadFlash returns the flash payload (if any) and deletes the
// cookie by writing an expired clear. Returns (nil, false) when
// no cookie is present or the signature does not verify.
//
// Callers invoke ReadFlash exactly once per request; the second
// call returns (nil, false) because the cookie is gone.
func (m *Manager) ReadFlash(w http.ResponseWriter, r *http.Request) (*Flash, bool) {
	c, err := r.Cookie(FlashCookieName)
	if err != nil {
		return nil, false
	}
	body, err := decodeRaw(c.Value, m.secret)
	if err != nil {
		return nil, false
	}
	var f Flash
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, false
	}
	if f.Key != nil && f.Key.Ref != "" {
		owner := ""
		if s, err := m.Read(r); err == nil {
			owner = s.Subject
		}
		f.Key.Plaintext, _ = m.reveals.take(f.Key.Ref, owner, m.now())
		f.Key.Ref = ""
		if f.Key.Plaintext != "" {
			// The page about to render shows a key in the clear, once:
			// no browser or proxy may keep a copy, whichever page it is.
			w.Header().Set("Cache-Control", "no-store")
		}
	}
	// Clear the cookie immediately so the banner is one-shot.
	http.SetCookie(w, &http.Cookie{
		Name:     FlashCookieName,
		Value:    "",
		Path:     "/",
		Domain:   m.cookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	return &f, true
}
