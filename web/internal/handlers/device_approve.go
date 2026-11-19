package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
	"github.com/kiff/cloud/web/internal/words"
)

// DeviceApproveHandler serves the browser half of `kiff auth login`
// at /auth/device: a signed-in human confirms the user_code their CLI
// printed, and approving binds their subject+ tenant to the pending
// device flow via the API's authenticated approve endpoint. It sits
// behind RequireSession, so an unauthenticated visitor is bounced
// through sign-in first (carrying the code in ?next) and lands back
// here to approve.
type DeviceApproveHandler struct {
	api      *apiclient.Client
	sessions *session.Manager
	bearers  BearerSource
	logger   *slog.Logger
}

// NewDeviceApproveHandler wires the handler. A nil api renders the
// "not configured" state; a nil bearers falls back to the session's
// stored bearer.
func NewDeviceApproveHandler(api *apiclient.Client, sessions *session.Manager, bearers BearerSource, logger *slog.Logger) *DeviceApproveHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeviceApproveHandler{api: api, sessions: sessions, bearers: bearers, logger: logger}
}

// Register attaches the GET (render) and POST (approve) routes.
func (h *DeviceApproveHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/device", h.show)
	mux.HandleFunc("POST /auth/device", h.approve)
}

// show is the lookup step: with a code it shows what that code signs in
// (the device and its key's fingerprint) and offers to approve exactly
// that; without one it asks for the code. Nothing is approved here.
func (h *DeviceApproveHandler) show(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		// Keep the code through sign-in (or sign-up), so the person lands
		// back on the device they are approving.
		http.Redirect(w, r, "/sign-in?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	lang := components.RequestLanguage(r)
	view := pages.DeviceApproveView{UserCode: strings.TrimSpace(r.URL.Query().Get("code")),
		Accounts: components.AccountSwitchFrom(r.Context()).Accounts, SignedIn: r.URL.Query().Get("signed_in") == "1",
		Lang: lang, W: words.DeviceIn(lang)}
	if h.api == nil {
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to enable device sign-in."
	} else if view.UserCode != "" && !h.review(w, r, s, &view) {
		return
	}
	// Approving needs a recent sign-in: sign in again first and come back
	// here, so Approve works when it's offered. Once only: back from
	// signing in, the page shows the device and approve says why.
	if view.Reviewed && !view.RecentSignIn && !view.SignedIn {
		http.Redirect(w, r, deviceReauthURL(view.UserCode, lang), http.StatusFound)
		return
	}
	_ = pages.DeviceApprove(view).Render(components.WithScreenLanguage(r.Context(), lang), w)
}

// review looks up what view.UserCode signs in and, when found, marks the
// view reviewed: the page then shows the device and its key's fingerprint,
// to compare with the one the device shows, and approving is a separate
// step tied to them. It returns false when it has already answered (a
// redirect to sign in).
func (h *DeviceApproveHandler) review(w http.ResponseWriter, r *http.Request, s session.Session, view *pages.DeviceApproveView) bool {
	back := "/sign-in?next=" + url.QueryEscape(components.KeepLang("/auth/device?code="+url.QueryEscape(view.UserCode), view.Lang))
	bearer, err := h.bearerFor(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, back, http.StatusFound)
		return false
	}
	p, err := h.api.DevicePendingBearer(r.Context(), bearer, view.UserCode)
	switch {
	case err == nil:
		view.Reviewed, view.DeviceName, view.KeyFingerprint = true, p.DeviceName, p.KeyFingerprint
		// A device that sends a signing key is the tafs app; the CLI sends none.
		view.FromTafs = p.HasKey || p.KeyFingerprint != "" || p.Relink
		view.RecentSignIn = p.RecentSignIn == nil || *p.RecentSignIn
		if p.Relink {
			// A re-link stays on the phone's own account: no choosing.
			view.Relink, view.ApprovalKeyFingerprint, view.Tenant = true, p.ApprovalKeyFingerprint, p.Tenant
		}
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, back, http.StatusFound)
		return false
	case errors.Is(err, apiclient.ErrDeviceCodeUnknown):
		view.ErrMsg = view.W.ErrUnknown
	case errors.Is(err, apiclient.ErrDeviceCodeApproved):
		// Opened again after approving (or approved in another tab): the
		// device signs in by itself, so the page says that, not "expired".
		view.Approved = true
	default:
		h.logger.WarnContext(r.Context(), "device lookup failed", slog.String("err", err.Error()))
		view.ErrMsg = view.W.ErrLookup
	}
	return true
}

// approve approves the code the person reviewed, and only that: the form
// carries the reviewed code and its key's fingerprint, and the code is
// looked up again. If it wasn't reviewed, or its key is not the one shown,
// the page shows the review again instead of approving.
func (h *DeviceApproveHandler) approve(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/auth/device", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	userCode := strings.TrimSpace(r.PostFormValue("user_code"))
	lang := components.RequestLanguage(r)
	view := pages.DeviceApproveView{UserCode: userCode, Accounts: components.AccountSwitchFrom(r.Context()).Accounts,
		Tenant: strings.TrimSpace(r.PostFormValue("tenant")), Lang: lang, W: words.DeviceIn(lang)}
	render := func(ctx context.Context) {
		_ = pages.DeviceApprove(view).Render(components.WithScreenLanguage(ctx, lang), w)
	}

	if h.api == nil {
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to enable device sign-in."
		render(r.Context())
		return
	}
	if userCode == "" {
		view.ErrMsg = view.W.ErrEnterCode
		render(r.Context())
		return
	}
	if !h.review(w, r, s, &view) {
		return
	}
	if !view.Reviewed {
		render(r.Context())
		return
	}
	if r.PostFormValue("reviewed") != "1" || r.PostFormValue("key_fingerprint") != view.KeyFingerprint {
		view.Notice = view.W.NoticeCheck
		render(r.Context())
		return
	}

	// The account the device signs in to: the one chosen, if it is one of
	// the person's own, else the one in use. The API checks the membership
	// again.
	ctx := r.Context()
	if view.Tenant != "" {
		mine := false
		for _, o := range view.Accounts {
			mine = mine || o.TenantID == view.Tenant
		}
		if !mine {
			view.Notice = view.W.NoticeChoose
			view.Tenant = ""
			render(ctx)
			return
		}
		ctx = apiclient.WithTenant(ctx, view.Tenant)
	}

	back := "/sign-in?next=" + url.QueryEscape(components.KeepLang("/auth/device", lang))
	bearer, err := h.bearerFor(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, back, http.StatusFound)
		return
	}
	switch err := h.api.ApproveDeviceBearer(ctx, bearer, userCode, "cli"); {
	case err == nil:
		view.Approved = true
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.sessions.Clear(w)
		http.Redirect(w, r, back, http.StatusFound)
		return
	case errors.Is(err, apiclient.ErrDeviceCodeUnknown):
		view.Reviewed = false
		view.ErrMsg = view.W.ErrUnknown
	case errors.Is(err, apiclient.ErrReauthRequired):
		// Approving a device needs a recent sign-in: nothing was
		// approved. Sign in again and come back to this same code, unless
		// that just happened.
		if r.PostFormValue("signed_in") != "1" {
			http.Redirect(w, r, deviceReauthURL(userCode, lang), http.StatusSeeOther)
			return
		}
		view.ErrMsg = view.W.ErrReauth
		view.ReauthURL = deviceReauthURL(userCode, lang)
	default:
		// The API's own words stay in the log: the page says what to do.
		h.logger.WarnContext(r.Context(), "device approve failed", slog.String("err", err.Error()))
		view.ErrMsg = view.W.ErrApprove
	}
	render(r.Context())
}

// deviceReauthURL signs the person in again and brings them back to the
// approval of the same code, in the same language.
func deviceReauthURL(userCode, lang string) string {
	return "/sign-out?next=" + url.QueryEscape(components.KeepLang("/auth/device?code="+url.QueryEscape(userCode)+"&signed_in=1", lang))
}

// bearerFor resolves the caller's API bearer, falling back to the
// session's stored bearer when no BearerSource is wired.
func (h *DeviceApproveHandler) bearerFor(ctx context.Context, s session.Session) (string, error) {
	if h.bearers != nil {
		return h.bearers.BearerFor(ctx, s)
	}
	return s.Bearer, nil
}
