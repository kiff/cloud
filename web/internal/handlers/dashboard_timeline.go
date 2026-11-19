package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// dashboard_timeline.go — per-entity timeline view. The cloud
// equivalent of runtime.Timeline(ctx, entityID): a tenant
// clicking an entity row in the Latest proposals table lands
// on /dashboard/entities/{id}/timeline, which renders the
// audit chain — events ingested, state transitions,
// decisions, validations, approvals, executions, failures —
// for that one entity, in chronological order.
//
// Tenant scoping is enforced by the api side: the per-tenant
// runtime resolution means a tenant viewing another tenant's
// entity id gets the empty timeline of their own runtime, not
// the other tenant's data. The dashboard handler does not need
// to add a check; it renders whatever the api returns.

// entityTimeline handles GET /dashboard/entities/{id}/timeline.
// The id path component is required and URL-decoded by the
// stdlib mux. Empty / whitespace-only ids 404 immediately.
func (h *DashboardHandler) entityTimeline(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	entityID := strings.TrimSpace(r.PathValue("id"))
	if entityID == "" {
		w.WriteHeader(http.StatusNotFound)
		_ = pages.DashboardEntityTimeline(pages.DashboardEntityTimelineView{
			Email:       s.Email,
			Subject:     s.Subject,
			DisplayName: s.DisplayName,
			ErrMsg:      "entity id is required",
		}).Render(r.Context(), w)
		return
	}

	view := pages.DashboardEntityTimelineView{
		Email:       s.Email,
		Subject:     s.Subject,
		DisplayName: s.DisplayName,
		EntityID:    entityID,
	}

	if h.api == nil {
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to enable timeline reads."
		_ = pages.DashboardEntityTimeline(view).Render(r.Context(), w)
		return
	}

	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard timeline: refresh bearer failed",
			slog.String("err", err.Error()))
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	// Pull /v1/me alongside the timeline so the dashboard shell
	// renders its workspace label correctly. Failure here is
	// non-fatal — the page still renders, with a degraded label.
	if me, err := h.api.GetMeBearer(r.Context(), bearer); err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			h.sessions.Clear(w)
			http.Redirect(w, r, "/sign-in", http.StatusFound)
			return
		}
		h.logger.WarnContext(r.Context(), "dashboard timeline: get me failed",
			slog.String("err", err.Error()))
	} else {
		view.Me = me
	}

	timeline, err := h.api.GetEntityTimelineBearer(r.Context(), bearer, entityID)
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			h.sessions.Clear(w)
			http.Redirect(w, r, "/sign-in", http.StatusFound)
			return
		}
		// ErrReceiptNotFound is the apiclient's collapsed 404
		// signal; for a timeline read it means "no audit records
		// for this entity in this tenant's runtime." Render the
		// empty state rather than a 5xx.
		if errors.Is(err, apiclient.ErrReceiptNotFound) {
			view.Timeline = nil
			_ = pages.DashboardEntityTimeline(view).Render(r.Context(), w)
			return
		}
		h.logger.WarnContext(r.Context(), "dashboard timeline: fetch failed",
			slog.String("entity_id", entityID),
			slog.String("err", err.Error()),
		)
		view.ErrMsg = "could not load timeline: " + err.Error()
		_ = pages.DashboardEntityTimeline(view).Render(r.Context(), w)
		return
	}
	view.Timeline = timeline
	_ = pages.DashboardEntityTimeline(view).Render(r.Context(), w)
}

// receipt renders /dashboard/receipts/{trace_id} for a signed-in
// user, fetching the receipt through their own session bearer
// rather than the operator key. This is the gated counterpart to
// the public handlers.Receipt: a logged-in user can open their own
// receipts from the Activity table without the deploy needing
// KIFF_DEMO_API_KEY set (the cause of the "Dashboard not
// configured" page when clicking a receipt link).
//
// Outcomes mirror the public handler: found -> 200 receipt page;
// missing -> 404 not-found page; api unset -> not-configured page;
// transport/other -> error page.
func (h *DashboardHandler) receipt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	// Render the receipt inside the signed-in dashboard shell (topbar +
	// sidebar) rather than the marketing layout, so a receipt opened from
	// Activity/Govern reads as part of the app. The workspace label is
	// enriched from /v1/me below (non-fatal); until then the chrome shows
	// the user's email + avatar.
	chrome := components.NewDashboardChrome(s.DisplayName, s.Email, "")
	traceID := strings.TrimSpace(r.PathValue("trace_id"))
	if traceID == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = pages.DashboardReceiptError(chrome).Render(r.Context(), w)
		return
	}
	if h.api == nil {
		_ = pages.DashboardReceiptNotConfigured(chrome).Render(r.Context(), w)
		return
	}

	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	// Enrich the chrome with the workspace label. Non-fatal: a failed or
	// unauthorized me-lookup leaves the email-only chrome; an expired
	// session is caught by the receipt read below, which redirects.
	if me, meErr := h.api.GetMeBearer(r.Context(), bearer); meErr == nil && me != nil {
		chrome = components.NewDashboardChrome(s.DisplayName, s.Email, me.TenantSlug)
	}

	rec, err := h.api.GetReceiptBearer(r.Context(), bearer, traceID)
	if err != nil {
		switch {
		case errors.Is(err, apiclient.ErrUnauthorized):
			h.sessions.Clear(w)
			http.Redirect(w, r, "/sign-in", http.StatusFound)
		case errors.Is(err, apiclient.ErrReceiptNotFound):
			// Decision receipts are minted asynchronously by the
			// anchor sweep (KIFF_ANCHOR_INTERVAL), so a click right
			// after a gallery run can arrive before the receipt
			// exists. Show an honest "being prepared" state that
			// auto-refreshes, rather than the generic not-found
			// dead-end on the demo happy path.
			if strings.HasPrefix(traceID, "decision-") {
				w.WriteHeader(http.StatusAccepted)
				_ = pages.DashboardReceiptPending(traceID, chrome).Render(r.Context(), w)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_ = pages.DashboardReceiptNotFound(traceID, chrome).Render(r.Context(), w)
		default:
			h.logger.WarnContext(r.Context(), "dashboard receipt: fetch failed",
				slog.String("trace_id", traceID),
				slog.String("err", err.Error()))
			w.WriteHeader(http.StatusBadGateway)
			_ = pages.DashboardReceiptError(chrome).Render(r.Context(), w)
		}
		return
	}

	v := pages.ReceiptView{R: rec, Signed: "/dashboard/receipts/" + url.PathEscape(rec.TraceID) + "/signed"}
	// The call's other receipt: a settled receipt follows the one signed
	// at the hold. Best effort: none found, no link.
	if pid := receiptProposal(rec); pid != "" {
		other := "settled-" + pid
		if strings.HasPrefix(rec.TraceID, "settled-") {
			other = "decision-" + pid
		}
		if _, err := h.api.GetReceiptBearer(r.Context(), bearer, other); err == nil {
			v.Related = other
		}
	}
	_ = pages.DashboardReceipt(v, chrome).Render(r.Context(), w)
}

// receiptProposal is the call a receipt is about, for finding its other
// receipt; "" for a receipt that is neither a decision nor a settlement.
func receiptProposal(rec *apiclient.Receipt) string {
	if rec.ProposalID != "" && (strings.HasPrefix(rec.TraceID, "decision-") || strings.HasPrefix(rec.TraceID, "settled-")) {
		return rec.ProposalID
	}
	return ""
}

// rawReceipt reads the receipt exactly as the API holds it, for the
// signed view and the download. It answers a missing or expired session
// itself (ok false); other failures come back as err.
func (h *DashboardHandler) rawReceipt(w http.ResponseWriter, r *http.Request) (raw []byte, traceID string, s session.Session, ok bool, err error) {
	s, ok = SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return nil, "", s, false, nil
	}
	traceID = strings.TrimSpace(r.PathValue("trace_id"))
	if h.api == nil || traceID == "" {
		return nil, traceID, s, true, apiclient.ErrReceiptNotFound
	}
	bearer, err := h.bearer(r.Context(), s)
	if err == nil {
		raw, err = h.api.GetReceiptRawBearer(r.Context(), bearer, traceID)
		if !errors.Is(err, apiclient.ErrUnauthorized) {
			return raw, traceID, s, true, err
		}
	}
	h.sessions.Clear(w)
	http.Redirect(w, r, "/sign-in", http.StatusFound)
	return nil, traceID, s, false, nil
}

// receiptSigned serves GET /dashboard/receipts/{trace_id}/signed: the
// receipt as KIFF signed it, to read before choosing to download it.
func (h *DashboardHandler) receiptSigned(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	raw, traceID, s, ok, err := h.rawReceipt(w, r)
	if !ok {
		return
	}
	chrome := components.NewDashboardChrome(s.DisplayName, s.Email, "")
	switch {
	case errors.Is(err, apiclient.ErrReceiptNotFound):
		w.WriteHeader(http.StatusNotFound)
		_ = pages.DashboardReceiptNotFound(traceID, chrome).Render(r.Context(), w)
		return
	case err != nil:
		h.logger.WarnContext(r.Context(), "dashboard receipt: signed view failed", slog.String("trace_id", traceID), slog.String("err", err.Error()))
		w.WriteHeader(http.StatusBadGateway)
		_ = pages.DashboardReceiptError(chrome).Render(r.Context(), w)
		return
	}
	// A receipt is saved before KIFF signs it, so a direct or bookmarked
	// link can arrive while it is unsigned; the receipt page says "Not
	// signed yet" instead of this page claiming a signature.
	base := "/dashboard/receipts/" + url.PathEscape(traceID)
	var probe struct {
		Attestation *apiclient.Attestation `json:"attestation"`
	}
	if json.Unmarshal(raw, &probe) != nil || probe.Attestation == nil {
		http.Redirect(w, r, base, http.StatusSeeOther)
		return
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, raw, "", "  ") != nil {
		pretty.Reset()
		pretty.Write(raw)
	}
	_ = pages.DashboardReceiptSigned(pages.SignedReceiptView{
		TraceID: traceID, JSON: pretty.String(), Back: base, Download: base + "/download",
	}, chrome).Render(r.Context(), w)
}

// receiptDownload serves GET /dashboard/receipts/{trace_id}/download: the
// receipt exactly as the API holds it, as a file.
func (h *DashboardHandler) receiptDownload(w http.ResponseWriter, r *http.Request) {
	raw, traceID, _, ok, err := h.rawReceipt(w, r)
	switch {
	case !ok:
		return
	case errors.Is(err, apiclient.ErrReceiptNotFound):
		http.Error(w, "No receipt for this trace", http.StatusNotFound)
		return
	case err != nil:
		h.logger.WarnContext(r.Context(), "dashboard receipt: download failed", slog.String("trace_id", traceID), slog.String("err", err.Error()))
		http.Error(w, "The receipt could not be loaded. Try again shortly.", http.StatusBadGateway)
		return
	}
	name := "kiff-receipt-" + strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			return c
		}
		return '-'
	}, traceID) + ".json"
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}
