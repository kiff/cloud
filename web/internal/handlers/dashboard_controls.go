package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
)

// controls renders the Control Room: one Protected Control's real
// evidence over a window, plus passive launch states before traffic arrives.
func (h *DashboardHandler) controls(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	view := pages.DashboardControlsView{
		Email:       s.Email,
		Subject:     s.Subject,
		DisplayName: s.DisplayName,
		Flash:       convertFlash(flash),
	}

	if h.api == nil {
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to enable Govern evidence."
		_ = pages.DashboardControls(view).Render(r.Context(), w)
		return
	}

	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard controls: refresh bearer failed", slog.String("err", err.Error()))
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
		_ = pages.DashboardControls(view).Render(r.Context(), w)
		return
	}
	view.Me = me

	// Load control records — the product audit trail. Re-activations
	// supersede the prior record but keep the same logical identity, so
	// the raw list carries many superseded versions of one control. The
	// nav shows one row per logical control (collapseControls); selection
	// still resolves against the full list so an explicit (e.g.
	// bookmarked) control_id of any version still loads.
	//
	// Scope to a domain when ?domain= is present: controls carry their
	// domain, so the spine's Govern facet can show just the selected
	// domain's controls. Without the param the surface lists every
	// control (back-compatible).
	domainFilter := strings.TrimSpace(r.URL.Query().Get("domain"))
	view.CurrentDomain = domainFilter

	var rememberDomainSlug string
	var fullControls []apiclient.Control
	controls, err := h.api.ListControlsBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard controls: list controls failed", slog.String("err", err.Error()))
	} else {
		if domainFilter != "" {
			controls = filterControlsByDomain(controls, domainFilter)
			// Remember the selection (only when the domain actually has
			// controls, so a stale/garbage ?domain= never sticks) — the
			// spine survives a hop through a non-domain tab (Runtimes →
			// Govern). Persisted together with the window below in one
			// session write.
			if len(controls) > 0 {
				rememberDomainSlug = domainFilter
			}
		}
		fullControls = controls
		view.Controls = collapseControls(controls)
	}

	// Evidence window selector (7 / 30 / 90 days), wired to the API's
	// existing ?from=&?to=. An explicit ?window= is remembered; otherwise
	// the last selection is restored, so the window survives a hop to
	// another tab and back. Persisted with the domain in one write.
	winFrom, winTo, winPreset, winPersist := effectiveWindow(r, s, time.Now().UTC())
	view.WindowOptions = evidenceWindowOptions(r, winPreset)
	view.Window = winPreset
	h.rememberScope(w, s, rememberDomainSlug, winPersist)

	if selected := selectedControl(fullControls, strings.TrimSpace(r.URL.Query().Get("control_id"))); selected != nil {
		view.SelectedControlID = selected.ID
		if timeline, err := h.api.GetControlDecisionsBearer(r.Context(), bearer, selected.ID, apiclient.ControlDecisionsOptions{Limit: 20, From: winFrom, To: winTo}); err != nil {
			if errors.Is(err, apiclient.ErrReceiptNotFound) {
				view.EvidenceUnavailable = "The control-scoped decisions endpoint is not available for this control yet."
			} else {
				view.EvidenceUnavailable = "Could not load this control's decision timeline. Try again in a moment."
				h.logger.WarnContext(r.Context(), "dashboard controls: get control decisions failed",
					slog.String("control_id", selected.ID),
					slog.String("err", err.Error()))
			}
		} else {
			view.Evidence = pages.ControlEvidenceFromAPI(timeline)
		}

	}

	_ = pages.DashboardControls(view).Render(r.Context(), w)
}

// collapseControls reduces the control list to one entry per logical
// control. A logical control is identified by (domain, primary action,
// environment, workflow); re-activating it supersedes the prior record
// and mints a new one (G3 versioning), so the raw list can carry many
// superseded versions of a single control. The Control Room nav shows
// one row per logical control, preferring the active version (falling
// back to first-seen when none is active), so a tenant that re-activated
// a control eight times sees one entry, not eight. First-seen order is
// preserved so the nav is stable across renders.
func collapseControls(controls []apiclient.Control) []apiclient.Control {
	type logicalKey struct{ domain, action, env, workflow string }
	keyOf := func(c apiclient.Control) logicalKey {
		action := ""
		if len(c.Actions) > 0 {
			action = c.Actions[0]
		}
		return logicalKey{c.Domain, action, c.Environment, c.Workflow}
	}
	order := make([]logicalKey, 0, len(controls))
	rep := make(map[logicalKey]apiclient.Control, len(controls))
	for _, c := range controls {
		k := keyOf(c)
		existing, seen := rep[k]
		if !seen {
			rep[k] = c
			order = append(order, k)
			continue
		}
		if existing.Status != "active" && c.Status == "active" {
			rep[k] = c
		}
	}
	out := make([]apiclient.Control, 0, len(order))
	for _, k := range order {
		out = append(out, rep[k])
	}
	return out
}

// filterControlsByDomain keeps only the controls belonging to the
// named domain, case-insensitively. Used to scope the Govern
// surface to the selected domain when the spine routes here with
// ?domain=.
func filterControlsByDomain(controls []apiclient.Control, domain string) []apiclient.Control {
	out := make([]apiclient.Control, 0, len(controls))
	for _, c := range controls {
		if strings.EqualFold(c.Domain, domain) {
			out = append(out, c)
		}
	}
	return out
}

func selectedControl(controls []apiclient.Control, requested string) *apiclient.Control {
	if requested != "" {
		for i := range controls {
			if controls[i].ID == requested {
				return &controls[i]
			}
		}
	}
	for i := range controls {
		if controls[i].Status == "active" {
			return &controls[i]
		}
	}
	if len(controls) == 0 {
		return nil
	}
	return &controls[0]
}

func (h *DashboardHandler) exportControlProofPack(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if h.api == nil {
		http.Error(w, "API not configured", http.StatusServiceUnavailable)
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	action := strings.TrimSpace(r.PathValue("action"))
	environment := strings.TrimSpace(r.URL.Query().Get("environment"))
	body, err := h.api.GetProofPackBearer(r.Context(), bearer, action, environment)
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			h.sessions.Clear(w)
			http.Redirect(w, r, "/sign-in", http.StatusFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	filename := "kiff-proof-pack-" + safeDownloadName(action) + ".json"
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	_, _ = w.Write(body)
}

func safeDownloadName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "control"
	}
	return b.String()
}
