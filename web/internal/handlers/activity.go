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

// activity renders GET /activity: every call the account's agents
// made, across every domain, newest first, each with what KIFF
// decided, the owner's answer when it was held, what the tool reported
// and its receipt.
func (h *DashboardHandler) activity(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	v := pages.ActivityView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, Now: time.Now(),
		Filter: pages.NormalizeActivityFilter(q.Get("show"), q.Get("agent"), q.Get("page"))}
	if h.api == nil {
		v.LoadError = "apps/api is not configured."
		_ = pages.Activity(v).Render(r.Context(), w)
		return
	}
	page, err := h.api.ActivityBearer(r.Context(), bearer, v.Filter.Query())
	if err != nil {
		h.logger.WarnContext(r.Context(), "activity: list calls failed", slog.String("err", err.Error()))
		v.LoadError = "Activity could not be loaded. Try again shortly."
		_ = pages.Activity(v).Render(r.Context(), w)
		return
	}
	v.Counts, v.Shown = page.Counts, page.Shown
	// The owner's answers live on the held calls; joined by proposal id.
	// Without them the rows still say a call was held, not how it ended.
	held, err := h.api.ListCardExceptionsBearer(r.Context(), bearer, "")
	if err != nil {
		h.logger.WarnContext(r.Context(), "activity: list held calls failed", slog.String("err", err.Error()))
		v.AnswersError = "Your answers to held calls could not be read, so those rows show only that the call was held."
	}
	v.Rows = pages.JoinActivity(page.Calls, held)
	v.Agents = pages.ActivityAgents(page.Agents, h.listCards(r.Context(), bearer), v.Filter.Agent)
	_ = pages.Activity(v).Render(r.Context(), w)
}

// activityCall renders GET /activity/{id}: one call. A call a Card held
// for its owner has its story on Needs you, the one view for it.
func (h *DashboardHandler) activityCall(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	v := pages.ActivityCallView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName}
	if h.api == nil || id == "" {
		v.LoadError = "apps/api is not configured."
		_ = pages.ActivityCall(v).Render(r.Context(), w)
		return
	}
	page, err := h.api.ActivityBearer(r.Context(), bearer, apiclient.ActivityQuery{Proposal: id, Limit: 1})
	switch {
	case err != nil:
		h.logger.WarnContext(r.Context(), "activity: read call failed", slog.String("call", id), slog.String("err", err.Error()))
		v.LoadError = "This call could not be loaded. Try again shortly."
	case len(page.Calls) == 0:
		w.WriteHeader(http.StatusNotFound)
		v.LoadError = "No call with that id in this account."
	default:
		c := page.Calls[0]
		if c.Held {
			if held, herr := h.api.ListCardExceptionsBearer(r.Context(), bearer, ""); herr == nil {
				if rows := pages.JoinActivity([]apiclient.ActivityCall{c}, held); rows[0].Exception != nil {
					http.Redirect(w, r, pages.NeedsYouHref(rows[0].Exception.ID), http.StatusSeeOther)
					return
				}
			} else {
				// Not a refusal: say the answer is unknown here and send
				// them to Needs you.
				if !errors.Is(herr, apiclient.ErrForbidden) {
					h.logger.WarnContext(r.Context(), "activity: list held calls failed", slog.String("err", herr.Error()))
				}
				v.AnswersError = "Your answers to held calls could not be read, so this page cannot show the details of any answer."
			}
		}
		v.Call = &c
	}
	_ = pages.ActivityCall(v).Render(r.Context(), w)
}
