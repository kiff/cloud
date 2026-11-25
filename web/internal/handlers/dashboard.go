package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/gatewayclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// BearerSource mints a fresh credential the dashboard can forward
// to apps/api. Implementations:
//
//   - Clerk: calls Backend API session.CreateToken(SessionID) so
//     each apps/api call gets a freshly-signed ~60s JWT.
//   - Stub: returns the cached bearer (the literal subject) verbatim.
//
// Returning an error from BearerFor means the session is no
// longer usable and the dashboard should bounce to /sign-in.
type BearerSource interface {
	BearerFor(ctx context.Context, s session.Session) (string, error)
}

// staticBearerSource hands back the bearer stored at sign-in
// time. Used in stub mode where the bearer is the subject string
// and never expires.
type staticBearerSource struct{}

// BearerFor implements BearerSource.
func (staticBearerSource) BearerFor(_ context.Context, s session.Session) (string, error) {
	if strings.TrimSpace(s.Bearer) == "" {
		return "", errors.New("session has no bearer")
	}
	return s.Bearer, nil
}

// StaticBearerSource exposes the no-refresh BearerSource for
// callers that don't need refresh semantics.
func StaticBearerSource() BearerSource { return staticBearerSource{} }

// DashboardHandler is the user-facing surface that proves the
// product. v0.1 ships:
//
//   - tenant card (id, slug)
//   - one-time API-key reveal banner (post-create flash)
//   - starter-domain picker when no domain is set, "switch
//     domain" affordance once one is in place
//   - "send your first event" curl block + a Send button that
//     ingests the template's sample event
//   - decisions table (most recent receipts) with anchored / pending
//     status and a link to the receipt page
//   - approve / deny inline buttons on pending-approval rows
//
// Everything stays server-rendered (templ + plain forms +
// redirects). No JS.
type DashboardHandler struct {
	api      *apiclient.Client
	sessions *session.Manager
	bearers  BearerSource
	logger   *slog.Logger
	// gateway reaches the KIFF MCP gateway for /tools; nil when this
	// deployment has none.
	gateway *gatewayclient.Client
	// PublicGatewayURL is the MCP endpoint agents are pointed at
	// (https://mcp.kiff.dev/mcp). Like PublicAPIURL, it is what users
	// see, not how the web reaches the gateway.
	PublicGatewayURL string

	// PublicAPIURL is the user-facing API endpoint shown in the
	// "Connect an agent" snippet (e.g. https://api.kiff.dev). It is
	// distinct from h.api's base URL, which is how the WEB reaches the
	// API server-side (localhost on the same host in production) and
	// must never be shown to a user as their connect endpoint. Empty
	// falls back to the template default (https://api.kiff.dev).
	PublicAPIURL string

	// now is the clock a page's "today" is read by; nil is time.Now. Tests
	// pin it (SetNow in dashboard_export_test.go), so a count of today's
	// decisions can't change when a run crosses UTC midnight.
	now func() time.Time
}

// clock is the time a page's "today" is read by.
func (h *DashboardHandler) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// NewDashboardHandler wires the per-session apiclient + session
// manager + a BearerSource that refreshes the upstream bearer on
// every apps/api call. apiClient may be nil; the dashboard renders
// an "API not configured" notice in that case so the auth chain
// can still be smoke-tested without apps/api. bearers may be nil;
// the handler falls back to StaticBearerSource (no refresh).
func NewDashboardHandler(apiClient *apiclient.Client, sessions *session.Manager, bearers BearerSource, logger *slog.Logger) *DashboardHandler {
	if logger == nil {
		logger = slog.Default()
	}
	if bearers == nil {
		bearers = StaticBearerSource()
	}
	return &DashboardHandler{
		api:      apiClient,
		sessions: sessions,
		bearers:  bearers,
		logger:   logger,
	}
}

// Register attaches the dashboard's authenticated routes to mux.
// Callers are expected to wrap mux with RequireSession before
// mounting it.
func (h *DashboardHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /dashboard", h.index)
	mux.HandleFunc("GET /dashboard/", h.index)
	mux.HandleFunc("GET /dashboard/domains", h.domains)
	mux.HandleFunc("GET /dashboard/domains/new", h.newDomain)
	mux.HandleFunc("POST /dashboard/domains", h.createDomain)
	mux.HandleFunc("GET /dashboard/domains/{domain}", h.domainDetail)
	mux.HandleFunc("GET /dashboard/domains/{domain}/edit", h.editDomain)
	mux.HandleFunc("POST /dashboard/domains/{domain}/edit", h.updateDomain)
	mux.HandleFunc("POST /dashboard/domains/{domain}/delete", h.deleteDomain)
	mux.HandleFunc("POST /dashboard/domains/{domain}/mode", h.setDomainMode)
	mux.HandleFunc("GET /dashboard/domains/{domain}/evidence", h.domainEvidence)
	mux.HandleFunc("GET /dashboard/proposals", h.redirectToDomainActivity)
	mux.HandleFunc("GET /dashboard/receipts", h.redirectToDomainActivity)
	mux.HandleFunc("GET /dashboard/approvals", h.redirectToDomainActivity)
	mux.HandleFunc("GET /dashboard/govern", h.controls)
	// Cards: the product surface over mandates. Mounted outside
	// /dashboard because a card is the thing the customer holds,
	// not a dashboard section about configuration.
	//
	// One surface, deliberately. The Limits page presented the same
	// rows as configuration and shipping both was shipping two
	// products, so its routes 301 here and its page is gone. The
	// POST targets move too; the old ones stay registered because a
	// redirect cannot carry a form body, and a stale tab posting to
	// them should still work rather than 404.
	mux.HandleFunc("GET /agents", h.agentsIndex)
	mux.HandleFunc("GET /agents/new", h.agentNew)
	mux.HandleFunc("POST /agents", h.createAgent)
	mux.HandleFunc("GET /agents/{id}", h.agentOverview)
	mux.HandleFunc("GET /agents/{id}/cards", h.agentCards)
	mux.HandleFunc("GET /agents/{id}/keys", h.agentKeys)
	mux.HandleFunc("GET /agents/{id}/tools", h.agentTools)
	mux.HandleFunc("GET /agents/{id}/tools/gateway", h.agentSheet("gateway"))
	mux.HandleFunc("GET /agents/{id}/tools/link", h.agentSheet("link"))
	mux.HandleFunc("GET /agents/{id}/tools/how", h.agentSheet("how"))
	mux.HandleFunc("GET /agents/{id}/keys/new", h.agentSheet("key-new"))
	mux.HandleFunc("POST /agents/{id}/keys/new", h.createAgentKeyOfKind)
	mux.HandleFunc("GET /agents/{id}/keys/{key}", h.agentSheet("key"))
	mux.HandleFunc("GET /agents/{id}/keys/{key}/revoke", h.agentSheet("key-revoke"))
	mux.HandleFunc("POST /agents/{id}/key", h.createAgentKeyForAgent)
	mux.HandleFunc("POST /agents/{id}/gateway-key", h.createGatewayKey)
	mux.HandleFunc("GET /connections", h.connections)
	mux.HandleFunc("POST /connections/grants/{id}/revoke", h.revokeConnection)
	mux.HandleFunc("POST /agents/{id}/connect-links", h.createConnectLink)
	mux.HandleFunc("POST /agents/{id}/connect-links/{link}/revoke", h.revokeConnectLink)
	mux.HandleFunc("GET /cards", h.cards)
	mux.HandleFunc("GET /dashboard/bell", h.bell)
	mux.HandleFunc("GET /activity", h.activity)
	mux.HandleFunc("GET /activity/{id}", h.activityCall)
	mux.HandleFunc("GET /needs-you", h.needsYou)
	mux.HandleFunc("GET /needs-you/{id}", h.needsYouDetail)
	mux.HandleFunc("POST /needs-you/{id}/{answer}", h.answerNeedsYou)
	mux.HandleFunc("POST /needs-you/warnings/{id}/dismiss", h.dismissWarning)
	h.registerTools(mux)
	mux.HandleFunc("GET /welcome", h.welcome)
	mux.HandleFunc("POST /welcome", h.setAccountKind)
	mux.HandleFunc("POST /dashboard/account-kind", h.setAccountKind)
	mux.HandleFunc("POST /dashboard/time-zone", h.setTimeZone)
	mux.HandleFunc("POST /dashboard/account-name", h.setAccountName)
	mux.HandleFunc("POST /dashboard/chat-approvals", h.setChatApprovals)
	mux.HandleFunc("GET /cards/new", h.cardNew)
	mux.HandleFunc("GET /cards/{id}", h.cardDetail)
	mux.HandleFunc("GET /cards/{id}/decisions", h.cardDecisionsTab)
	mux.HandleFunc("GET /cards/{id}/statement", h.cardStatement)
	mux.HandleFunc("GET /cards/{id}/approved", h.cardApproved)
	mux.HandleFunc("GET /cards/{id}/changes", h.cardChanges)
	mux.HandleFunc("GET /cards/{id}/settings", h.cardSettings)
	mux.HandleFunc("GET /cards/{id}/approvers", h.cardApproversTab)
	mux.HandleFunc("GET /cards/{id}/approvers/new", h.cardSheet("approver-new"))
	mux.HandleFunc("GET /cards/{id}/approvers/{grant}/revoke", h.cardSheet("approver-revoke"))
	mux.HandleFunc("GET /cards/{id}/holder", h.cardSheet("holder"))
	mux.HandleFunc("GET /cards/{id}/only-for", h.cardSheet("only-for"))
	mux.HandleFunc("POST /cards/{id}/only-for", h.cardOnlyFor)
	mux.HandleFunc("GET /cards/{id}/revoke", h.cardSheet("revoke"))
	mux.HandleFunc("POST /cards", h.issueMandate)
	mux.HandleFunc("GET /cards/{id}/extra-room", h.cardRoom)
	mux.HandleFunc("POST /cards/{id}/extra-room/{step}", h.cardRoom)
	mux.HandleFunc("POST /cards/{id}/extra-room", h.cardRoom)
	mux.HandleFunc("POST /cards/quick", h.issueTaskCard)
	mux.HandleFunc("POST /cards/{id}/test", h.sendCardTest)
	mux.HandleFunc("POST /cards/{id}/warnings", h.cardWarningSettings)
	mux.HandleFunc("GET /cards/{id}/warnings", h.cardSheet("warnings"))
	mux.HandleFunc("POST /cards/{id}/approvers", h.connectApprover)
	mux.HandleFunc("POST /cards/{id}/approvers/{grant}/revoke", h.revokeCardApprover)
	mux.HandleFunc("POST /cards/{id}/key", h.createCardAgentKey)
	mux.HandleFunc("POST /cards/{id}/key/role", h.grantCardAgentKeyRole)
	mux.HandleFunc("POST /cards/{id}/revoke", h.revokeMandate)
	mux.HandleFunc("POST /cards/{id}/move", h.moveMandate)
	mux.HandleFunc("POST /cards/{id}/holders", h.shareCard)
	mux.HandleFunc("POST /cards/{id}/archive", h.archiveMandate)
	mux.HandleFunc("GET /agent-cards", redirectPermanent("/cards"))
	mux.HandleFunc("GET /dashboard/mandates", redirectPermanent("/cards"))
	mux.HandleFunc("GET /dashboard/mandates/new", redirectPermanent("/cards/new"))
	mux.HandleFunc("POST /dashboard/mandates", h.issueMandate)
	mux.HandleFunc("POST /dashboard/mandates/{id}/revoke", h.revokeMandate)
	// Live event stream (SSE) proxied from the API, so the dashboard
	// updates without a manual reload.
	mux.HandleFunc("GET /dashboard/stream", h.stream)
	mux.HandleFunc("GET /dashboard/govern/proof-pack/{action}", h.exportControlProofPack)
	mux.HandleFunc("GET /dashboard/keys", h.keysIndex)
	mux.HandleFunc("GET /dashboard/runtimes", h.runtimes)
	mux.HandleFunc("GET /dashboard/notifications", h.notifications)
	mux.HandleFunc("GET /dashboard/attention", h.attentionQueue)
	mux.HandleFunc("POST /dashboard/attention/{id}/{action}", h.attentionAction)
	mux.HandleFunc("GET /dashboard/notifications/settings", h.notificationSettings)
	mux.HandleFunc("POST /dashboard/notifications/settings", h.updateNotificationSettings)
	mux.HandleFunc("POST /dashboard/notifications/clear", h.clearNotifications)
	// Examples (the sandbox gallery) is retired: a card's real test
	// request is how a customer sees a decision now (the first-decision
	// design amendment). Its POST routes and pages are gone; a bookmark
	// still lands.
	mux.HandleFunc("GET /dashboard/examples", redirectPermanent("/cards"))
	mux.HandleFunc("GET /dashboard/settings", h.settings)
	mux.HandleFunc("GET /dashboard/support", h.supportTickets)
	mux.HandleFunc("GET /dashboard/support/new", h.newSupportTicket)
	mux.HandleFunc("POST /dashboard/support", h.createSupportTicket)
	mux.HandleFunc("GET /dashboard/support/{id}", h.supportTicket)
	mux.HandleFunc("POST /dashboard/support/{id}/reply", h.replySupportTicket)
	mux.HandleFunc("GET /dashboard/account/export", h.exportData)
	mux.HandleFunc("POST /dashboard/account/delete", h.deleteAccount)
	mux.HandleFunc("POST /dashboard/billing/portal", h.billingPortal)
	mux.HandleFunc("GET /dashboard/checkout", h.checkout)
	mux.HandleFunc("POST /dashboard/pack-requests/{id}/decline", h.declinePackRequest)
	mux.HandleFunc("GET /dashboard/entities/{id}/timeline", h.entityTimeline)
	mux.HandleFunc("GET /dashboard/receipts/{trace_id}", h.receipt)
	mux.HandleFunc("GET /dashboard/receipts/{trace_id}/signed", h.receiptSigned)
	mux.HandleFunc("GET /dashboard/receipts/{trace_id}/download", h.receiptDownload)
	mux.HandleFunc("POST /dashboard/keys", h.createKey)
	mux.HandleFunc("POST /dashboard/keys/{id}/revoke", h.revokeKey)
	mux.HandleFunc("POST /dashboard/guard/draft", h.seedGuardDraft)
	mux.HandleFunc("POST /dashboard/approvals/{id}/grant", h.grantApproval)
	mux.HandleFunc("POST /dashboard/approvals/{id}/deny", h.denyApproval)

	// Legacy /dashboard/start* paths from the retired Onboarding
	// tab. Bookmarks and external links should redirect to the
	// new IA, not 404.
	mux.HandleFunc("GET /dashboard/start", redirectPermanent("/dashboard"))
	mux.HandleFunc("GET /dashboard/start/", redirectPermanent("/dashboard"))

	// Legacy /dashboard/activity from when proposals + receipts shared
	// one "Activity" tab. The tenant-wide Proposals / Receipts /
	// Approvals surfaces folded into the domain's Activity view, so this
	// lands there too — no double hop.
	mux.HandleFunc("GET /dashboard/activity", h.redirectToDomainActivity)
}

// redirectPermanent returns a handler that 301s every request
// to the same target path. Used by the legacy /dashboard/start*
// routes after the Onboarding tab retired.
func redirectPermanent(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	}
}

// bearer returns a fresh credential for an apps/api call. Uses
// the BearerSource configured on the handler so Clerk-backed
// installs mint short-lived JWTs per call. Failure means the
// upstream session is no longer usable; callers bounce to
// /sign-in.
//
// A bearer WithChrome already minted for this request is reused: it
// was minted for the same session moments ago, and minting is a
// round trip to Clerk's Backend API.
func (h *DashboardHandler) bearer(ctx context.Context, s session.Session) (string, error) {
	if tok, ok := ctx.Value(requestBearerKey{}).(string); ok && tok != "" {
		return tok, nil
	}
	return h.bearers.BearerFor(ctx, s)
}

type requestBearerKey struct{}

func withRequestBearer(ctx context.Context, bearer string) context.Context {
	return context.WithValue(ctx, requestBearerKey{}, bearer)
}

// index renders /dashboard (Overview tab). Pulls everything in
// parallel-ish (sequential for now; v0.1 has small fan-out).
// Failure on any secondary fetch (templates / receipts / keys)
// renders a degraded page rather than a 5xx — the user still
// sees their tenant + can sign out.
// index handles /dashboard, where sign-in lands. Home is the cards:
// someone comes to KIFF to give an agent bounded authority, so the
// first screen is which agents hold which cards, not the domain
// machinery underneath them.
func (h *DashboardHandler) index(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/cards", http.StatusFound)
}

// redirectToDomainActivity sends the retired tenant-wide Proposals /
// Receipts / Approvals routes into the domain's Activity view, where
// those surfaces now live scoped to the domain. It resolves the tenant's
// domain: exactly one → its Activity (evidence) view; zero or many → the
// domains apex (empty state / picker). The cross-domain rollup is
// deferred to the account layer at >1 domain, so >1 intentionally lands
// on the apex rather than a tenant-wide ledger. 303 so any
// POST-originated navigation becomes a GET.
func (h *DashboardHandler) redirectToDomainActivity(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	target := "/dashboard/domains"
	if h.api != nil {
		if bearer, err := h.bearer(r.Context(), s); err == nil {
			if list, lerr := h.api.ListDomainsBearer(r.Context(), bearer); lerr == nil {
				switch {
				case s.LastDomain != "" && domainInList(list, s.LastDomain):
					// The remembered selection survives a hop through a
					// non-domain tab (Runtimes, Examples). Validated
					// against the live list, so a deleted/stale slug
					// falls through to the single-domain or apex case.
					target = "/dashboard/domains/" + url.PathEscape(s.LastDomain) + "/evidence"
				case len(list) == 1:
					target = "/dashboard/domains/" + url.PathEscape(list[0].Name) + "/evidence"
				}
			}
		}
	}
	// Carry the incoming query (e.g. ?tab=approvals from a notification)
	// onto the resolved Activity view so the redirect lands on the right
	// tab instead of the default decisions tab. The URL fragment
	// (#ap-<id> deep link) is client-side and the browser re-applies it
	// to the redirect target on its own.
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// domainInList reports whether slug names one of the tenant's live
// domains — the validation gate before routing to a remembered
// LastDomain, so a deleted or stale slug never routes anywhere real.
func domainInList(list []apiclient.DomainListItem, slug string) bool {
	for _, d := range list {
		if d.Name == slug {
			return true
		}
	}
	return false
}

// rememberDomain persists slug as the session's LastDomain so the
// domain-scoped spine survives a hop through a non-domain tab
// (Runtimes, Examples). Best-effort: a failed re-issue just means the
// selection isn't remembered. Guarded on change to avoid a redundant
// Set-Cookie on every domain view; Issue preserves the existing
// (non-zero) ExpiresAt, so this does not slide the session lifetime.
func (h *DashboardHandler) rememberDomain(w http.ResponseWriter, s session.Session, slug string) {
	if slug == "" || s.LastDomain == slug {
		return
	}
	s.LastDomain = slug
	_ = h.sessions.Issue(w, s)
}

// rememberScope persists the remembered domain and/or window in one
// session write, so surfaces that update both (Govern, Activity) never
// issue two Set-Cookie headers in a request (which would race and drop
// one of the changes). An empty value leaves that field untouched;
// nothing is written unless something actually changed.
func (h *DashboardHandler) rememberScope(w http.ResponseWriter, s session.Session, domain, window string) {
	changed := false
	if domain != "" && s.LastDomain != domain {
		s.LastDomain = domain
		changed = true
	}
	if window != "" && s.LastWindow != window {
		s.LastWindow = window
		changed = true
	}
	if changed {
		_ = h.sessions.Issue(w, s)
	}
}

// keysIndex renders the API keys tab. POST routes (mint, revoke
// once lands) redirect back here so the user lands on the
// matching tab after the action.
func (h *DashboardHandler) keysIndex(w http.ResponseWriter, r *http.Request) {
	h.renderTab(w, r, pages.DashboardKeys)
}

// runtimes renders the guard-runtime inventory page. Reached from
// the Runtimes card on Settings (not a primary nav item), so it
// mirrors the keys page and highlights "settings" in the nav.
func (h *DashboardHandler) runtimes(w http.ResponseWriter, r *http.Request) {
	h.renderTab(w, r, pages.DashboardRuntimes)
}

// settings renders the Settings tab. Carries the tenant
// identity card, the plan card, and the M2 billing-portal slot
// — account-shaped surfaces that previously sat on Overview.
func (h *DashboardHandler) settings(w http.ResponseWriter, r *http.Request) {
	tab, switchTo := pages.SettingsTabFor(r.URL.Query().Get("tab")), r.URL.Query().Get("switch")
	// Settings › Notifications reads the notification rules.
	var prefs map[string]bool
	if tab == "notifications" && h.api != nil {
		if s, ok := SessionFrom(r.Context()); ok {
			if bearer, err := h.bearer(r.Context(), s); err == nil {
				if p, perr := h.api.GetNotificationPrefsBearer(r.Context(), bearer); perr != nil {
					h.logger.WarnContext(r.Context(), "settings: get notification prefs failed", slog.String("err", perr.Error()))
				} else {
					prefs = p
				}
			}
		}
	}
	h.renderTab(w, r, func(v pages.DashboardView) templ.Component {
		v.SettingsTab, v.SwitchTo, v.NotificationPrefs = tab, switchTo, prefs
		return pages.DashboardSettings(v)
	})
}

// exportData streams the caller's full data-export bundle
// (GET /v1/me/export) as a JSON download. Self-serve Article
// 15/20 surface. Read-only — no state change.
//
// Errors degrade gracefully: an expired session bounces to
// /sign-in; any other failure lands a flash on Settings rather
// than a bare 5xx, since this is reached from the Account card.
func (h *DashboardHandler) exportData(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "API not configured",
		})
		http.Redirect(w, r, "/dashboard/settings?tab=data", http.StatusSeeOther)
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	bundle, err := h.api.ExportBearer(r.Context(), bearer)
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			h.sessions.Clear(w)
			http.Redirect(w, r, "/sign-in", http.StatusFound)
			return
		}
		h.logger.WarnContext(r.Context(), "dashboard: data export failed", slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "could not generate your export: " + err.Error(),
		})
		http.Redirect(w, r, "/dashboard/settings?tab=data", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="kiff-data-export.json"`)
	_, _ = w.Write(bundle)
}

// billingPortal handles POST /dashboard/billing/portal: it asks
// apps/api to mint a Stripe Customer Portal session for the tenant
// and redirects the user there. The Stripe secret stays api-side;
// the web only forwards the user to the returned one-time URL.
//
// Errors degrade to a flash on Settings rather than a bare 5xx:
// an expired session bounces to /sign-in; a tenant with no billing
// account (ErrNoBilling — the action shouldn't have shown) lands a
// gentle notice; any other failure shows a retry message.
func (h *DashboardHandler) billingPortal(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "API not configured",
		})
		http.Redirect(w, r, "/dashboard/settings?tab=workspace", http.StatusSeeOther)
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	url, err := h.api.BillingPortalBearer(r.Context(), bearer)
	if err != nil {
		switch {
		case errors.Is(err, apiclient.ErrUnauthorized):
			h.sessions.Clear(w)
			http.Redirect(w, r, "/sign-in", http.StatusFound)
			return
		case errors.Is(err, apiclient.ErrNoBilling):
			_ = h.sessions.IssueFlash(w, session.Flash{
				Kind: session.FlashError, Message: "No billing account is linked to this workspace yet.",
			})
		default:
			h.logger.WarnContext(r.Context(), "dashboard: billing portal failed", slog.String("err", err.Error()))
			_ = h.sessions.IssueFlash(w, session.Flash{
				Kind: session.FlashError, Message: "Could not open the billing portal. Please try again.",
			})
		}
		http.Redirect(w, r, "/dashboard/settings?tab=workspace", http.StatusSeeOther)
		return
	}
	// Stripe-hosted portal; one-time URL. 303 so the POST becomes a GET.
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// deleteAccount handles POST /dashboard/account/delete.
// Self-serve Article 17 erasure surface.
//
// The form requires a confirm_slug field matching the tenant's
// slug — server-side validation, no JS required. A mismatch
// flashes an error back to Settings without calling the API. On
// success the session is cleared (the workspace is now
// suspended) and the user lands on /account/deleted, which says
// when the grace window ends, from a flash.
//
// What this does NOT do: hard-delete. The API moves the tenant to
// deleted_pending; the grace window and eventual scrub are handled
// by the operator CLI or an automated sweep.
func (h *DashboardHandler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "API not configured",
		})
		http.Redirect(w, r, "/dashboard/settings?tab=data", http.StatusSeeOther)
		return
	}

	// Server-side slug confirmation — no JS required.
	confirmSlug := strings.TrimSpace(r.PostFormValue("confirm_slug"))
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	// Fetch the tenant slug from Me so we never trust client input
	// for the comparison anchor.
	me, err := h.api.GetMeBearer(r.Context(), bearer)
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			h.sessions.Clear(w)
			http.Redirect(w, r, "/sign-in", http.StatusFound)
			return
		}
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "could not verify identity: " + err.Error(),
		})
		http.Redirect(w, r, "/dashboard/settings?tab=data", http.StatusSeeOther)
		return
	}
	if confirmSlug != me.TenantSlug {
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind:    session.FlashError,
			Message: "Workspace slug did not match. Type your slug exactly to confirm deletion.",
		})
		http.Redirect(w, r, "/dashboard/settings?tab=data", http.StatusSeeOther)
		return
	}

	resp, err := h.api.DeleteAccountBearer(r.Context(), bearer)
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			h.sessions.Clear(w)
			http.Redirect(w, r, "/sign-in", http.StatusFound)
			return
		}
		h.logger.WarnContext(r.Context(), "dashboard: delete account failed", slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "could not request deletion: " + err.Error(),
		})
		http.Redirect(w, r, "/dashboard/settings?tab=data", http.StatusSeeOther)
		return
	}

	h.logger.InfoContext(r.Context(), "dashboard: account deletion requested",
		slog.String("tenant_slug", me.TenantSlug),
		slog.String("grace_window_ends", resp.GraceWindowEnds),
	)

	// Clear the session — the workspace is now suspended and the
	// user's keys are revoked, so staying logged in is confusing.
	h.sessions.Clear(w)
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashAccountDeleted, Message: resp.GraceWindowEnds})
	http.Redirect(w, r, "/account/deleted", http.StatusSeeOther)
}

// renderTab is the shared GET-tab path. Loads the same
// DashboardView for every tab (cheap fan-out at this scale) so
// the top bar's workspace label, the flash banner, and the
// shared cards (plan card / activity card on Overview) read
// from a single source of truth.
//
// renderFn is the templ entry point for the specific tab —
// pages.DashboardKeys, pages.DashboardSettings, etc. Each one
// reuses the existing partials (decisionsTable, keyPanel) from
// dashboard.templ.
func (h *DashboardHandler) renderTab(w http.ResponseWriter, r *http.Request, renderFn func(pages.DashboardView) templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	view := pages.DashboardView{
		Email:       s.Email,
		Subject:     s.Subject,
		DisplayName: s.DisplayName,
		Flash:       convertFlash(flash),
	}

	if h.api == nil {
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to enable the dashboard."
		_ = renderFn(view).Render(r.Context(), w)
		return
	}
	// The connect snippet shows the PUBLIC API endpoint, not h.api's
	// internal reach-the-API URL (localhost in production). Empty →
	// the template defaults to https://api.kiff.dev.
	view.APIBaseURL = h.PublicAPIURL
	view.ConnectSnippet = connectSnippetParam(r)

	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: refresh bearer failed", slog.String("err", err.Error()))
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	me, err := h.api.GetMeBearer(r.Context(), bearer)
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			h.sessions.Clear(w)
			http.Redirect(w, r, "/sign-in", http.StatusFound)
			return
		}
		view.ErrMsg = "could not reach the api: " + err.Error()
		_ = renderFn(view).Render(r.Context(), w)
		return
	}
	view.Me = me

	// Templates list. A failure here is non-fatal; the user can
	// still see the rest of the dashboard.
	templates, err := h.api.ListTemplatesBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: list templates failed", slog.String("err", err.Error()))
	} else {
		view.Templates = templates
	}

	// Receipts (most recent 50).
	receipts, err := h.api.ListReceiptsBearer(r.Context(), bearer, 50)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: list receipts failed", slog.String("err", err.Error()))
	} else {
		view.Receipts = receipts
	}

	// Keys list (label + id metadata only — never plaintext).
	keys, err := h.api.ListKeysBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: list keys failed", slog.String("err", err.Error()))
	} else {
		view.Keys = keys
		view.KeyCount = len(keys)
	}

	// Usage snapshot for the plan card. A failure here is
	// non-fatal — the rest of the dashboard renders without
	// the counter, and the plan card falls back to "—" rather
	// than blocking the page.
	usage, err := h.api.GetUsageBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: get usage failed", slog.String("err", err.Error()))
	} else {
		view.Usage = usage
	}

	// The account's two meters for the usage card. Non-fatal: the
	// card says it can't read them.
	if meters, merr := h.api.GetMeterUsageBearer(r.Context(), bearer); merr != nil {
		h.logger.WarnContext(r.Context(), "dashboard: get meters failed", slog.String("err", merr.Error()))
	} else {
		view.Meters = meters
	}

	// Proposals list. Drives the Overview tab's "Latest
	// proposals" table and connection-status header. Non-fatal:
	// the Overview falls back to the activity card counters when
	// the proposals endpoint is unavailable (e.g. 503 from an
	// in-memory smoke run with no per-tenant pool).
	proposalsResp, err := h.api.ListProposalsBearer(r.Context(), bearer, 20)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: list proposals failed", slog.String("err", err.Error()))
	} else {
		view.Proposals = proposalsResp.Proposals
		view.LastReceivedAt = proposalsResp.LastReceivedAt
	}

	// Domain summary for the empty-state snippet on the Overview
	// tab. ErrNoDomain is the expected "fresh tenant" signal;
	// surface it as nil Domain (the next-action card branches on
	// this) rather than a logged warning.
	domain, err := h.api.GetDomainBearer(r.Context(), bearer)
	switch {
	case err == nil:
		view.Domain = domain
	case errors.Is(err, apiclient.ErrNoDomain):
		// Fresh tenant; leaves Domain nil.
	default:
		h.logger.WarnContext(r.Context(), "dashboard: get domain failed", slog.String("err", err.Error()))
	}

	guardConnections, err := h.api.ListGuardConnectionsBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: list guard connections failed", slog.String("err", err.Error()))
	} else {
		view.GuardConnections = guardConnections
	}

	// Domain draft for the observe-mode aha on the Overview tab.
	// Observe mode derives a starter catalog from real agent
	// traffic and saves it as the draft; a tenant with no active
	// Domain but a Draft has *observed* activity to show.
	// ErrDraftNotFound is the expected "no draft yet" signal —
	// leave Draft nil, not a logged warning.
	draft, err := h.api.GetDraftBearer(r.Context(), bearer)
	switch {
	case err == nil:
		view.Draft = draft
	case errors.Is(err, apiclient.ErrDraftNotFound):
		// No draft; leaves Draft nil.
	default:
		h.logger.WarnContext(r.Context(), "dashboard: get draft failed", slog.String("err", err.Error()))
	}

	// Pending-approvals count for the runtime-signal surfaces.
	// Tiny limit because we only need the count for branching;
	// the queue tab fetches the full list.
	approvals, err := h.api.ListApprovalsBearer(r.Context(), bearer,
		apiclient.ApprovalsListOptions{Status: "pending", Limit: 50})
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: list approvals failed", slog.String("err", err.Error()))
	} else {
		view.PendingApprovalsCount = len(approvals.Approvals)
	}

	// Scope the sidebar's domain facets (Domain / Govern / Activity) to
	// the remembered selection so a hop through a non-domain tab keeps
	// the nav pointed at the same domain — this is what stops Govern
	// flipping between "all domains" and "one domain" depending on where
	// you navigated from. Validated against the live list so a stale or
	// deleted LastDomain never scopes the nav to a dead slug.
	if s.LastDomain != "" {
		if list, lerr := h.api.ListDomainsBearer(r.Context(), bearer); lerr != nil {
			h.logger.WarnContext(r.Context(), "dashboard: list domains for nav scope failed", slog.String("err", lerr.Error()))
		} else if domainInList(list, s.LastDomain) {
			view.CurrentDomain = s.LastDomain
		}
	}

	_ = renderFn(view).Render(r.Context(), w)
}

// computeNextAction picks the right "what to do next" card for
// Overview's right column based on the tenant's state. The
// branch order matters — a tenant with no domain shouldn't see
// "send your first proposal" even if they happen to have a key.
//
// Branches, in priority order:
//
//  1. No domain, no draft          → Install the guard (docs)
//  2. No domain, draft observed    → Review & activate the draft
//  3. Domain saved, no API key     → Mint your first API key
//  4. Pending approvals waiting    → Review pending approvals
//  5. Domain + key, no proposal yet → Send your first proposal
//  6. All operating                 → Add a second action in Build
//
// Invariant relied on by decorateDomainHome (dashboard_domains.go):
// branches 1–2 fire only when v.Domain == nil. The domain home feeds a
// synthetic view with Domain set (it only runs when a domain is in
// focus), so it deterministically lands in branches 3–6. If the branch
// order is ever reshuffled, keep the no-domain cases reading v.Domain so
// that invariant holds.
func computeNextAction(v pages.DashboardView) *pages.NextAction {
	// 1) Brand-new tenant: no active domain and nothing observed
	// yet. The first move is to instrument an agent — point at
	// the docs quickstart, which delivers a real audit
	// trail in under five minutes with zero config.
	if v.Domain == nil && !viewHasObservedCatalog(v) {
		return &pages.NextAction{
			Kind:    "install-guard",
			Title:   "Install the guard",
			Body:    "Attach KIFF to the agent you already run in observe mode — no account, no config. You'll see a real audit trail of its tool calls in under five minutes.",
			CTAText: "Connect an agent",
			CTAHref: "/docs/connect-an-agent",
		}
	}

	// 2) Observe mode has derived a catalog (the draft) but no
	// active domain yet. The next step is the observe → activate
	// move: review the derived domain and turn on enforcement.
	if v.Domain == nil {
		return &pages.NextAction{
			Kind:    "activate-draft",
			Title:   "Review and activate your derived domain",
			Body:    "Observe mode turned your agent's real traffic into a starter domain. Review it in Studio, set risk and approvals, then activate to start governing.",
			CTAText: "Review in Studio",
			CTAHref: "/dashboard/build",
		}
	}

	// 3) Domain + no API key — code can't call the runtime yet.
	if v.KeyCount == 0 {
		return &pages.NextAction{
			Kind:    "mint-key",
			Title:   "Mint your first API key",
			Body:    "Your API key authenticates calls from your code to KIFF. We show the plaintext exactly once at mint time.",
			CTAText: "Open API keys",
			CTAHref: "/dashboard/keys",
		}
	}

	// 4) Pending approvals — high-signal interrupt that should
	// outrank the "send a proposal" nudge.
	if v.PendingApprovalsCount > 0 {
		return &pages.NextAction{
			Kind:    "review-approvals",
			Title:   "Review pending approvals",
			Body:    "High-risk actions wait for a human. Approve or deny what your agent has proposed.",
			CTAText: "Open queue",
			CTAHref: "/dashboard/approvals",
		}
	}

	// 5) Domain + key but no proposal yet — wire it up.
	if !tenantHasSubmittedProposal(v) {
		return &pages.NextAction{
			Kind:    "first-proposal",
			Title:   "Send your first proposal",
			Body:    "Call POST /v1/proposals/decide with your API key. The Proposals tab shows what KIFF decided.",
			CTAText: "Open Proposals",
			CTAHref: "/dashboard/proposals",
		}
	}

	// 6) Operating — nudge toward evolution.
	return &pages.NextAction{
		Kind:    "add-action",
		Title:   "Add a second action in Build",
		Body:    "Domains evolve. Add another risky action, deepen the state machine, or wire an inbound adapter.",
		CTAText: "Open Build",
		CTAHref: "/dashboard/build",
	}
}

// viewHasObservedCatalog reports whether observe mode has derived
// a non-empty action catalog for this tenant — i.e. the saved
// draft parsed into at least one action. This is the signal that
// the tenant has *observed* (not yet governed) activity to show
// on the Overview, distinct from having an active governed Domain.
func viewHasObservedCatalog(v pages.DashboardView) bool {
	return v.Draft != nil && v.Draft.Parsed != nil && len(v.Draft.Parsed.Actions) > 0
}

// tenantHasSubmittedProposal reports whether the tenant has at
// least one governed proposal on record. Reads two signals (in
// order of preference): the canonical usage counter, and the
// recent proposals list. Either signal positive ⇒ the tenant
// has crossed the activation line.
func tenantHasSubmittedProposal(v pages.DashboardView) bool {
	if v.Usage != nil && v.Usage.Counters["kiff.cloud.proposals.submitted"] > 0 {
		return true
	}
	return len(v.Proposals) > 0
}

// keyKindReadOnly is the Create form's choice for a read-only account
// key: the API mints it with the viewer role, bound to no agent, and it
// reads the account and changes nothing. Any other choice is today's
// key for an agent or a service.
const keyKindReadOnly = "read-only"

// keyRolesFor is what the API is asked to put on a new key of kind.
func keyRolesFor(kind string) []string {
	if kind == keyKindReadOnly {
		return []string{"viewer"}
	}
	return nil
}

// keyExpiryFor is the expiry the Create form's choice asks for: a number
// of days (30, 90, 365) from now, or nil for the cloud's default (90 days
// for a read-only account key; none for an agent or service key).
func keyExpiryFor(choice string, now time.Time) *time.Time {
	days := map[string]int{"30": 30, "90": 90, "365": 365}[choice]
	if days == 0 {
		return nil
	}
	at := now.Add(time.Duration(days) * 24 * time.Hour)
	return &at
}

// createKey mints a fresh API key for the tenant and stashes
// the plaintext in a one-shot flash cookie. Redirects back to
// /dashboard.
func (h *DashboardHandler) createKey(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	target := dashboardPostRedirectTarget(r, "/dashboard/keys")
	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "API not configured",
		})
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	label := strings.TrimSpace(r.PostFormValue("label"))
	if label == "" {
		label = "dashboard"
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	created, err := h.api.CreateExpiringKeyBearer(r.Context(), bearer, label, keyRolesFor(strings.TrimSpace(r.PostFormValue("kind"))),
		keyExpiryFor(strings.TrimSpace(r.PostFormValue("expires")), time.Now()))
	if apiclient.IsReauthRequired(err) {
		// A read-only account key reads the whole account, so the API
		// asks for a recent sign-in first; nothing was minted.
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind:    session.FlashKeyReauth,
			Message: "A read-only account key needs a recent sign-in. Sign in again, then create it within ten minutes.",
		})
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: mint key failed", slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "could not mint key: " + err.Error(),
		})
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind: session.FlashKeyRevealed,
		Key: &session.FlashKeyPayload{
			ID:        created.ID,
			Plaintext: created.Plaintext,
			Label:     created.Label,
			Owner:     s.Subject,
		},
	})
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func dashboardPostRedirectTarget(r *http.Request, fallback string) string {
	next := strings.TrimSpace(r.PostFormValue("next"))
	if next == "" || !isSafeNext(next) {
		return fallback
	}
	u, err := url.Parse(next)
	if err != nil {
		return fallback
	}
	// An agent's Keys tab revokes inline and lands back there.
	if u.Path == "/dashboard" || strings.HasPrefix(u.Path, "/dashboard/") || strings.HasPrefix(u.Path, "/agents/") {
		return u.RequestURI()
	}
	return fallback
}

func connectSnippetParam(r *http.Request) string {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("snippet"))) {
	case "assistant", "ai", "curl", "python", "javascript", "js", "go":
		return strings.ToLower(strings.TrimSpace(r.URL.Query().Get("snippet")))
	default:
		return "curl"
	}
}

// revokeKey expires an active API key for the tenant. The API
// stores this as revoked_at, so old audit rows still have a key
// id while the credential can no longer authenticate.
func (h *DashboardHandler) revokeKey(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	// Honor an optional ?next / form next so the runtimes page can revoke
	// a runtime's key inline and land back on /dashboard/runtimes; falls
	// back to the keys page. dashboardPostRedirectTarget validates the
	// target is an internal /dashboard path.
	target := dashboardPostRedirectTarget(r, "/dashboard/keys")
	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "API not configured",
		})
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	keyID := strings.TrimSpace(r.PathValue("id"))
	if keyID == "" {
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "key id is required",
		})
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if err := h.api.RevokeKeyBearer(r.Context(), bearer, keyID); err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: revoke key failed",
			slog.String("key_id", keyID),
			slog.String("err", err.Error()),
		)
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "could not revoke key: " + err.Error(),
		})
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind:    session.FlashKeyRevoked,
		Message: "API key revoked: " + keyID,
	})
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// randomSuffix returns 8 hex characters. Enough entropy to avoid
// collisions in a single dashboard session.
func randomSuffix() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Fallback to nanoseconds if rand fails (it shouldn't).
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(buf[:])
}

// grantApproval routes a POST /dashboard/approvals/{id}/grant
// click to the framework's /v1/approvals/{id}/grant endpoint.
func (h *DashboardHandler) grantApproval(w http.ResponseWriter, r *http.Request) {
	h.reviewApproval(w, r, true)
}

// denyApproval routes a POST /dashboard/approvals/{id}/deny click.
func (h *DashboardHandler) denyApproval(w http.ResponseWriter, r *http.Request) {
	h.reviewApproval(w, r, false)
}

func (h *DashboardHandler) reviewApproval(w http.ResponseWriter, r *http.Request, grant bool) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	approvalID := strings.TrimSpace(r.PathValue("id"))
	if approvalID == "" {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "approval id is required"})
		http.Redirect(w, r, "/dashboard/domains", http.StatusSeeOther)
		return
	}
	reason := strings.TrimSpace(r.PostFormValue("reason"))
	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "API not configured"})
		http.Redirect(w, r, "/dashboard/domains", http.StatusSeeOther)
		return
	}
	actor := "actor-" + s.Subject
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if grant {
		err = h.api.GrantApprovalBearer(r.Context(), bearer, approvalID, actor, reason)
	} else {
		err = h.api.DenyApprovalBearer(r.Context(), bearer, approvalID, actor, reason)
	}
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: review approval failed",
			slog.String("approval", approvalID), slog.Bool("grant", grant), slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: fmt.Sprintf("review approval: %v", err),
		})
		http.Redirect(w, r, approvalsRedirectTarget(r), http.StatusSeeOther)
		return
	}
	kind := session.FlashApprovalGrant
	if !grant {
		kind = session.FlashApprovalDeny
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: kind, Message: approvalID})
	http.Redirect(w, r, approvalsRedirectTarget(r), http.StatusSeeOther)
}

// approvalsRedirectTarget picks where to land after a grant/deny.
// Approvals now live on the domain's Activity view, so when the click
// came from there we send the reviewer back to the same Activity page
// (preserving their place). Anything else falls back to the domains
// apex, which resolves to the domain's Activity for a single-domain
// tenant.
func approvalsRedirectTarget(r *http.Request) string {
	// From Needs you, back to the page it was answered on.
	if back := strings.TrimSpace(r.PostFormValue("back")); back != "" {
		return attentionBack(back)
	}
	ref := r.Header.Get("Referer")
	if u, err := url.Parse(ref); err == nil {
		p := u.Path
		if strings.HasPrefix(p, "/dashboard/domains/") && strings.HasSuffix(p, "/evidence") {
			return p
		}
	}
	return "/dashboard/domains"
}

// convertFlash adapts the session flash payload to the templ
// view shape. Returns nil when no flash is present.
func convertFlash(f *session.Flash) *pages.DashboardFlash {
	if f == nil {
		return nil
	}
	out := &pages.DashboardFlash{
		Kind:    string(f.Kind),
		Message: f.Message,
	}
	if f.Key != nil {
		out.KeyID = f.Key.ID
		out.KeyPlaintext = f.Key.Plaintext
		out.KeyLabel = f.Key.Label
	}
	out.Template = f.Template
	out.Body = f.Body
	out.Slug = f.Slug
	return out
}

// checkout starts Company on the web (pricing phase 3): Settings' "Choose
// Company" points here (?plan=team). Solo is sold only in the tafs app, so
// ?plan=solo goes back to Settings saying so. Signed out, the dashboard's
// sign-in comes back here after; signed in, it opens Stripe's checkout: a
// card, VAT. An account with a plan already goes to the billing portal from
// Settings instead. ?pack=<id> opens a pack's one-time checkout, and with
// &request=<id> it answers a member's request.
func (h *DashboardHandler) checkout(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	plan := "team"
	back := func(kind session.FlashKind, msg string) {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: kind, Message: msg})
		http.Redirect(w, r, "/dashboard/settings?tab=workspace", http.StatusSeeOther)
	}
	if r.URL.Query().Get("plan") == "solo" {
		back(session.FlashError, "Solo is sold in the tafs app, through the App Store or Google Play.")
		return
	}
	if h.api == nil {
		back(session.FlashError, "API not configured")
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if pack := strings.TrimSpace(r.URL.Query().Get("pack")); pack != "" {
		dest, err := h.api.PackCheckoutBearer(r.Context(), bearer, pack, strings.TrimSpace(r.URL.Query().Get("request")))
		switch {
		case err == nil:
			http.Redirect(w, r, dest, http.StatusSeeOther)
		case errors.Is(err, apiclient.ErrUnauthorized):
			h.sessions.Clear(w)
			http.Redirect(w, r, "/sign-in", http.StatusFound)
		case errors.Is(err, apiclient.ErrNoCheckout):
			back(session.FlashError, "This pack can't be added here. The request may be answered already, or only the workspace's admin adds tasks.")
		default:
			h.logger.WarnContext(r.Context(), "dashboard: pack checkout failed", slog.String("err", err.Error()))
			back(session.FlashError, "Could not open checkout. Please try again.")
		}
		return
	}
	dest, err := h.api.CheckoutBearer(r.Context(), bearer, plan)
	switch {
	case err == nil:
		// Stripe-hosted checkout; a one-time URL.
		http.Redirect(w, r, dest, http.StatusSeeOther)
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
	case errors.Is(err, apiclient.ErrAlreadySubscribed):
		back(session.FlashError, "This workspace has a plan already. Change it with Manage billing.")
	case errors.Is(err, apiclient.ErrBilledByApple):
		back(session.FlashError, "Apple bills this workspace's plan. Cancel it with Apple first to choose a plan here.")
	case errors.Is(err, apiclient.ErrNoCheckout):
		back(session.FlashError, "The workspace's admin chooses the plan.")
	default:
		h.logger.WarnContext(r.Context(), "dashboard: checkout failed", slog.String("err", err.Error()))
		back(session.FlashError, "Could not open checkout. Please try again.")
	}
}

// declinePackRequest declines a member's request for a pack, with an
// optional note, and goes back to Settings.
func (h *DashboardHandler) declinePackRequest(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	back := func(kind session.FlashKind, msg string) {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: kind, Message: msg})
		http.Redirect(w, r, "/dashboard/settings?tab=workspace", http.StatusSeeOther)
	}
	if h.api == nil {
		back(session.FlashError, "API not configured")
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	switch err := h.api.DeclinePackRequestBearer(r.Context(), bearer, r.PathValue("id"), note); {
	case err == nil:
		back(session.FlashKind("pack_request_declined"), "Request declined.")
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
	case errors.Is(err, apiclient.ErrConflict):
		back(session.FlashError, "That request was answered already.")
	case errors.Is(err, apiclient.ErrForbidden):
		back(session.FlashError, "Only the workspace's admin answers requests.")
	default:
		h.logger.WarnContext(r.Context(), "dashboard: decline pack request failed", slog.String("err", err.Error()))
		back(session.FlashError, "Could not decline the request. Please try again.")
	}
}
