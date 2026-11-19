package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	studio "github.com/kiff/kiff-studio"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
	"github.com/kiff/cloud/web/internal/studio_view"
)

// StudioHandler ships the progressive domain-authoring surface
// at /dashboard/build. It is the web-half of MVP-2; the
// api-half (an earlier change) is the draft store this handler
// wraps.
//
// Behavior in one paragraph: every block-level POST mutates the
// in-memory Blueprint, persists it via PUT to the api's draft
// store, and 303-redirects to the GET that re-renders. The page
// is the source of truth on every render — no client-side
// state, no JS. "Save and activate" promotes the draft to the
// active domain via the api's transactional activate path.
//
// All routes inherit the dashboard mux's RequireSession
// middleware. The handler does NOT register itself outside
// /dashboard/build/*; the entry link lives on the Studio top-
// level tab in the dashboard nav.
type StudioHandler struct {
	api      *apiclient.Client
	sessions *session.Manager
	bearers  BearerSource
	logger   *slog.Logger
}

// NewStudioHandler wires the same dependencies the existing
// onboarding handler uses. apiClient may be nil to render an
// "API not configured" notice without needing apps/api.
func NewStudioHandler(apiClient *apiclient.Client, sessions *session.Manager, bearers BearerSource, logger *slog.Logger) *StudioHandler {
	if logger == nil {
		logger = slog.Default()
	}
	if bearers == nil {
		bearers = StaticBearerSource()
	}
	return &StudioHandler{
		api: apiClient, sessions: sessions, bearers: bearers, logger: logger,
	}
}

// Register attaches the studio routes to mux. The caller mounts
// mux behind RequireSession; this handler does not enforce it
// directly.
func (h *StudioHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /dashboard/build", h.show)
	mux.HandleFunc("POST /dashboard/build/activate-yaml", h.activateYAML)
	mux.HandleFunc("POST /dashboard/build/candidate/confirm", h.confirmCandidate)
	mux.HandleFunc("POST /dashboard/build/candidate/replay", h.replayCandidate)
	mux.HandleFunc("GET /dashboard/build/scaffold/{file}", h.scaffoldFile)
}

// bearer hands back a fresh upstream credential per call.
func (h *StudioHandler) bearer(r *http.Request, s session.Session) (string, error) {
	return h.bearers.BearerFor(r.Context(), s)
}

// ---------- GET ----------

// show renders the Studio page. Loads the draft from the api
// (preferring an existing draft; falling back to the active
// domain promoted to a fresh draft; falling back to the
// placeholder default on first visit).
func (h *StudioHandler) show(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	view := pages.StudioView{
		Email:       s.Email,
		Subject:     s.Subject,
		DisplayName: s.DisplayName,
		Now:         time.Now(),
	}
	if flash != nil && flash.Message != "" {
		// Route the flash to the right banner slot. Success
		// flashes (e.g. a draft import that landed) read as a
		// notice; everything else is the page-wide error slot.
		// Per-block errors come through StudioDraft.FieldErrors.
		switch flash.Kind {
		case session.FlashDomainValidated:
			view.NoticeMsg = flash.Message
		default:
			view.ErrMsg = flash.Message
		}
	}

	if h.api == nil {
		// API-less preview mode. Rather than a dead single-stage
		// placeholder, render a read-only walk-through of all
		// four stages. The stage strip becomes ?stage=1..4 tabs;
		// edits are disabled. Mirrors the kiff-studio-evolution
		// design reference.
		view.Preview = true
		view.PreviewStage = parsePreviewStage(r.URL.Query().Get("stage"))
		view.Draft = studioPreviewDraft(view.PreviewStage)
		view.NotConfiguredMsg = "Design preview — apps/api is not configured, so edits are disabled. Use the stage tabs to step through all four stages."
		_ = pages.Studio(view).Render(r.Context(), w)
		return
	}

	bearer, err := h.bearer(r, s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	if me, err := h.api.GetMeBearer(r.Context(), bearer); err == nil {
		view.TenantSlug = me.TenantSlug
		view.TenantID = me.TenantID
	}

	// Domain-aware Build: when entered scoped to a selected domain
	// (?domain= or the remembered selection), edit THAT domain in place —
	// pre-fill the editor with its YAML, scope the nav, and target save at
	// it — instead of the singular default draft. Falls through to the
	// default draft flow if the domain can't be loaded or parsed.
	if editDomain := h.selectedBuildDomain(r, s, bearer); editDomain != "" {
		if yaml, yerr := h.api.GetDomainYAMLBearer(r.Context(), bearer, editDomain); yerr != nil {
			h.logger.WarnContext(r.Context(), "build: load selected domain yaml failed",
				slog.String("domain", editDomain), slog.String("err", yerr.Error()))
		} else if bp, perr := studio_view.ParseYAML([]byte(yaml)); perr != nil {
			h.logger.WarnContext(r.Context(), "build: parse selected domain yaml failed",
				slog.String("domain", editDomain), slog.String("err", perr.Error()))
		} else {
			h.rememberDomain(w, s, editDomain)
			draft := studio_view.StudioDraft{Blueprint: bp}
			fillSidecars(&draft)
			view.EditDomain = editDomain
			view.Draft = draft
			view.ObservedRuntime = h.loadObservedRuntimeProof(r, bearer, draft)
			view.ActiveControl = h.loadActiveControl(r, bearer, draft)
			_ = pages.Studio(view).Render(r.Context(), w)
			return
		}
	}

	draft, firstRun, err := h.loadDraftWithState(r, bearer)
	if err != nil {
		view.ErrMsg = "could not load draft: " + err.Error()
		view.Draft = placeholderDraft()
		_ = pages.Studio(view).Render(r.Context(), w)
		return
	}
	view.Draft = draft

	// First run: no saved draft and no active domain. Studio leads
	// with the actions KIFF observed from a connected runtime
	// rather than a cold authoring surface.
	if firstRun {
		h.renderActionFirst(w, r, bearer, view)
		return
	}

	view.ObservedRuntime = h.loadObservedRuntimeProof(r, bearer, draft)
	view.ActiveControl = h.loadActiveControl(r, bearer, draft)

	_ = pages.Studio(view).Render(r.Context(), w)
}

// selectedBuildDomain resolves the domain this Build session should edit,
// from the ?domain= filter (preferred) or the remembered selection
// (LastDomain), validated against the tenant's live domains list so a
// stale/garbage slug never routes Build at a dead domain. Empty means the
// default draft / new-domain flow.
func (h *StudioHandler) selectedBuildDomain(r *http.Request, s session.Session, bearer string) string {
	slug := strings.TrimSpace(r.URL.Query().Get("domain"))
	if slug == "" {
		slug = strings.TrimSpace(s.LastDomain)
	}
	if slug == "" {
		return ""
	}
	list, err := h.api.ListDomainsBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "build: list domains for selection failed", slog.String("err", err.Error()))
		return ""
	}
	if domainInList(list, slug) {
		return slug
	}
	return ""
}

// rememberDomain persists slug as the session's LastDomain so the
// domain-scoped spine survives a hop through a non-domain tab, mirroring
// the dashboard handler. Best-effort; no-op when unchanged.
func (h *StudioHandler) rememberDomain(w http.ResponseWriter, s session.Session, slug string) {
	if slug == "" || s.LastDomain == slug {
		return
	}
	s.LastDomain = slug
	_ = h.sessions.Issue(w, s)
}

// loadDraft returns the user's current StudioDraft. Tries
// (in order): existing draft → active domain promoted to a
// fresh draft → placeholder default.
func (h *StudioHandler) loadDraft(r *http.Request, bearer string) (studio_view.StudioDraft, error) {
	draft, _, err := h.loadDraftWithState(r, bearer)
	return draft, err
}

// loadDraftWithState is loadDraft plus a first-run signal:
// firstRun is true only when the tenant has neither a saved
// draft nor an active domain — i.e. the placeholder default was
// returned. The action-first onboarding branches on this to
// lead with observed-action candidates instead of the cold
// authoring ladder.
func (h *StudioHandler) loadDraftWithState(r *http.Request, bearer string) (draft studio_view.StudioDraft, firstRun bool, err error) {
	d, err := h.api.GetDraftBearer(r.Context(), bearer)
	switch {
	case err == nil:
		bp, parseErr := studio_view.ParseYAML([]byte(d.YAML))
		if parseErr != nil {
			h.logger.WarnContext(r.Context(), "studio: parse stored draft yaml failed",
				slog.String("err", parseErr.Error()))
			bp = placeholderBlueprint()
		}
		out := studio_view.StudioDraft{
			Blueprint: bp,
			UpdatedAt: d.UpdatedAt,
		}
		fillSidecars(&out)
		return out, false, nil
	case errors.Is(err, apiclient.ErrDraftNotFound):
		// Try the active domain as a starting point.
		if active, aerr := h.api.GetDomainBearer(r.Context(), bearer); aerr == nil {
			out := studio_view.StudioDraft{
				Blueprint: blueprintFromDomainSummary(active),
				UpdatedAt: 0, // freshly materialized; the user's first PUT bumps it
			}
			fillSidecars(&out)
			return out, false, nil
		}
		return placeholderDraft(), true, nil
	default:
		return studio_view.StudioDraft{}, false, err
	}
}

// fillSidecars populates the StudioDraft's sidecar adapter +
// audit data when the page-side stage logic needs them. Today
// these aren't persisted (kiff-studio v0.1.0 doesn't carry
// adapters / audit on Blueprint), so the sidecars default to
// empty / "in-memory" + trace propagation on. Future versions
// may persist them in the YAML.
func fillSidecars(d *studio_view.StudioDraft) {
	if d.Audit.Store == "" {
		d.Audit = studio_view.AuditConfig{Store: "in-memory", TracePropagation: true}
	}
}

// draftDomainName resolves the draft's domain name: the parsed summary
// when the draft YAML is valid, else a best-effort parse of the raw
// YAML. Empty when neither yields a name (a blank/garbage draft).
func draftDomainName(d *apiclient.Draft) string {
	if d == nil {
		return ""
	}
	if d.Parsed != nil && strings.TrimSpace(d.Parsed.Domain) != "" {
		return strings.TrimSpace(d.Parsed.Domain)
	}
	if bp, err := studio_view.ParseYAML([]byte(d.YAML)); err == nil {
		return strings.TrimSpace(bp.Domain)
	}
	return ""
}

// placeholderDraft is what first-time visitors land on. A
// stage-1 entity + one action with the canonical refund-flow
// shape, nudging the user toward the canonical example.
func placeholderDraft() studio_view.StudioDraft {
	d := studio_view.StudioDraft{
		Blueprint: placeholderBlueprint(),
	}
	fillSidecars(&d)
	return d
}

func placeholderBlueprint() studio.Blueprint {
	return studio.Blueprint{
		Domain:  "refund-flow",
		Entity:  "Order",
		Actions: []studio.Action{{Name: "ISSUE_REFUND"}},
	}
}

// blueprintFromDomainSummary builds a Blueprint from the
// /v1/me/domain summary. The summary is what apps/api echoes
// back from its own Spec view; it's structurally close to the
// Blueprint shape but not identical.
func blueprintFromDomainSummary(d *apiclient.Domain) studio.Blueprint {
	bp := studio.Blueprint{
		Domain: d.Domain,
		Entity: d.Entity,
		Events: append([]string(nil), d.Events...),
		States: append([]string(nil), d.States...),
	}
	for _, a := range d.Actions {
		bp.Actions = append(bp.Actions, studio.Action{
			Name:                a.Name,
			AllowedStates:       append([]string(nil), a.AllowedStates...),
			RequiredParameters:  append([]string(nil), a.RequiredParameters...),
			RequiredPermissions: nil, // not surfaced in the summary; user can re-enter
			Risk:                a.Risk,
			ApprovalRequired:    a.Approval == "required",
			Executor:            a.Executor,
		})
	}
	return bp
}

// ---------- save / activate ----------

// flash issues a one-off error flash for the next render.
func (h *StudioHandler) flash(w http.ResponseWriter, msg string) {
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: msg})
}

// activateYAML is the block-free completion path. A developer who
// finished their domain in the codebase (scaffold → fill → `kiff verify`)
// brings the completed kiff.yaml back here, and it is validated and
// activated in one shot via PUT /v1/me/domain — no block-form, no draft
// round-trip. This is the path that lets a derived draft (which lands at
// StageScratch, actions-only, and is not activatable as-is) reach active
// without the legacy editor — the prerequisite for retiring it. On a
// still-incomplete contract the api returns 422 and we surface exactly
// what is left to finish, so the repo → verify → import loop stays
// honest.
func (h *StudioHandler) activateYAML(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if h.api == nil {
		h.flash(w, "API not configured")
		http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
		return
	}
	yaml := strings.TrimSpace(r.PostFormValue("yaml"))
	if yaml == "" {
		h.flash(w, "Paste your finished kiff.yaml to activate — the box was empty.")
		http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
		return
	}
	bearer, err := h.bearer(r, s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	// Domain-aware save: when Build was editing a selected existing domain
	// (hidden `domain` field), update THAT domain in place via the
	// per-domain endpoint, rather than the singular default activate.
	if domain := strings.TrimSpace(r.PostFormValue("domain")); domain != "" {
		if _, err := h.api.UpdateDomainBearer(r.Context(), bearer, domain, []byte(yaml)); err != nil {
			var verr *apiclient.DomainValidationError
			switch {
			case errors.As(err, &verr):
				msg := "Not saved — the contract isn't valid: " + verr.Message
				if len(verr.Issues) > 0 {
					msg += " (" + verr.Issues[0].Message + ")"
				}
				h.flash(w, msg)
			case errors.Is(err, apiclient.ErrDomainRenameUnsupported):
				h.flash(w, "The domain name can't change here — in-place edit keeps the identity. Revert the name in the YAML and save again.")
			default:
				h.flash(w, "could not save: "+err.Error())
			}
			http.Redirect(w, r, "/dashboard/build?domain="+url.QueryEscape(domain), http.StatusSeeOther)
			return
		}
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind:    session.FlashDomainValidated,
			Message: domain + " validated and saved. The updated contract is governing.",
		})
		http.Redirect(w, r, "/dashboard/domains/"+url.PathEscape(domain), http.StatusSeeOther)
		return
	}

	if _, err := h.api.PutDomainBearer(r.Context(), bearer, []byte(yaml)); err != nil {
		var verr *apiclient.DomainValidationError
		if errors.As(err, &verr) {
			msg := "Not activated — the contract isn't complete yet: " + verr.Message
			if len(verr.Issues) > 0 {
				msg += " (" + verr.Issues[0].Message + ")"
			}
			msg += ". Finish it in your repo, run kiff verify, and import again."
			h.flash(w, msg)
		} else {
			h.flash(w, "could not activate: "+err.Error())
		}
		http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind:    session.FlashDomainValidated,
		Message: "Domain validated and activated from your codebase. It's now governing in observe.",
	})
	http.Redirect(w, r, "/dashboard/domains", http.StatusSeeOther)
}

// ---------- scaffold downloads ----------

func (h *StudioHandler) scaffoldFile(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if h.api == nil {
		http.Error(w, "API not configured", http.StatusServiceUnavailable)
		return
	}
	bearer, err := h.bearer(r, s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	draft, err := h.loadDraft(r, bearer)
	if err != nil {
		http.Error(w, "could not load draft: "+err.Error(), http.StatusInternalServerError)
		return
	}

	file, err := url.PathUnescape(r.PathValue("file"))
	if err != nil {
		http.Error(w, "bad file name", http.StatusBadRequest)
		return
	}

	// Every scaffold file comes from the same blueprint the YAML does,
	// reviewer permissions included.
	bp := studio_view.WithReviewerPermissions(draft.Blueprint)
	switch file {
	case "kiff.yaml":
		// Use YAMLPreview (graceful fallback) instead of strict
		// BuildYAML so the button works on incomplete drafts
		// (normal mid-authoring case). The live YAML pane uses
		// the same fallback; this makes the download consistent.
		yaml := []byte(draft.YAMLPreview())
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = w.Write(yaml)
	case "descriptor.json":
		// The `kiff scaffold` seed (kiff/kiff#27): the derived domain as
		// a descriptor the developer feeds to the framework CLI to
		// generate an executable domain package in their codebase. This
		// is the codebase end of the Build bridge (the
		// authenticated-surface design / A1).
		out, err := scaffoldDescriptorJSON(bp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	case "domain.go":
		out, err := studio.BuildConstants(bp, "domain")
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "text/x-go")
		_, _ = w.Write(out)
	case "actions.go":
		// kiff-studio v0.1.0 emits one combined executor file.
		// v0.2 will split actions / policy; until then we serve
		// the same content under both names so the tree on the
		// scaffold panel makes sense.
		out, err := studio.BuildExecutorStub(bp, "domain")
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "text/x-go")
		_, _ = w.Write(out)
	case "domain_test.go":
		out, err := studio.BuildTestScaffold(bp, "domain")
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "text/x-go")
		_, _ = w.Write(out)
	default:
		http.Error(w, "unknown scaffold file: "+file, http.StatusNotFound)
	}
}

// ---------- helpers ----------

// splitCSV splits a comma-separated form value into trimmed,
// non-empty entries. Returns nil for an empty input so callers
// can distinguish "no values" from "empty entries".
//
// Originally lived in onboarding.go; relocated here when the
// Onboarding handler retired.
func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parsePreviewStage clamps the ?stage= query param to 1..4,
// defaulting to 1. Used only in API-less preview mode.
func parsePreviewStage(raw string) int {
	switch raw {
	case "2":
		return 2
	case "3":
		return 3
	case "4":
		return 4
	default:
		return 1
	}
}
