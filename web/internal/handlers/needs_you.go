package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/kiff/cloud/web/internal/components"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
	"github.com/kiff/cloud/web/internal/words"
)

// needsYou renders GET /needs-you: the actions held because they fall
// outside a Card, oldest first, and the recent answers.
func (h *DashboardHandler) needsYou(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	v := pages.NeedsYouView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, Flash: convertFlash(flash)}
	if h.api == nil {
		v.LoadError = "apps/api is not configured."
		_ = pages.NeedsYou(v).Render(r.Context(), w)
		return
	}
	v.Filter = pages.NormalizeAnsweredFilter(r.URL.Query().Get("answer"), r.URL.Query().Get("agent"))
	v.Tab = pages.NormalizeNeedsYouTab(r.URL.Query().Get("tab"), v.Filter)
	es, err := h.api.ListCardExceptionsBearer(r.Context(), bearer, "")
	if err != nil {
		h.logger.WarnContext(r.Context(), "needs you: list failed", slog.String("err", err.Error()))
		// Only the Card holds are missing: domain approvals, blocked calls
		// and warnings load on their own and still show.
		v.HoldsError = "Calls a Card held for you could not be loaded. Nothing has been approved while they are unavailable."
	} else {
		// What waits is never filtered; the filter narrows the answers.
		v.Held, _ = pages.SplitExceptions(es, 0)
		v.Agents = pages.AnsweredAgents(es)
		v.AnsweredAny = len(v.Agents) > 0 || len(es) > len(v.Held)
		_, v.Answered = pages.SplitExceptions(pages.FilterAnswered(es, v.Filter), 50)
	}
	// The API lists 100 warnings a page; the tab pages through them and
	// counts them all.
	if v.Tab == pages.NeedsYouWarnings {
		v.WarningsPage, _ = strconv.Atoi(r.URL.Query().Get("page"))
		if v.WarningsPage < 0 || v.WarningsPage > 100000 {
			http.Error(w, "Invalid page", http.StatusBadRequest)
			return
		}
	}
	if feed, err := h.api.CardWarningAttentionBearer(r.Context(), bearer, v.WarningsPage); err != nil {
		v.BudgetWarningsError = "Budget warnings could not be read."
	} else {
		v.BudgetWarnings = pages.OpenWarnings(feed.Items)
		v.WarningsOpen, v.WarningsMore = feed.Open, feed.Truncated
		v.WarningsUnread = feed.Unread
		// Opening Warnings marks what it shows as seen, for this
		// person: the badge and the line on Waiting stop asking. The
		// rows still say New this once. Only when the rows are shown: a
		// page that renders only its load error shows none.
		if v.Tab == pages.NeedsYouWarnings && v.LoadError == "" {
			seen := map[string]int64{}
			for _, it := range v.BudgetWarnings {
				if it.Unread && it.Revision > 0 {
					seen[it.ID] = it.Revision
				}
			}
			if len(seen) > 0 {
				if err := h.api.SeeAttentionBearer(r.Context(), bearer, seen); err != nil {
					h.logger.WarnContext(r.Context(), "needs you: mark warnings seen failed", slog.String("err", err.Error()))
				} else {
					v.WarningsUnread = max(v.WarningsUnread-len(seen), 0)
				}
			}
		}
	}
	// Needs attention lives here now: domain approvals wait in Waiting
	// beside the Card holds, and blocked calls have their own tab, a page
	// of 100 at a time. Approvals page on Waiting (?apage=), 100 at a
	// time, so every one is reachable.
	if v.Tab == pages.NeedsYouWaiting {
		v.ApprovalsPage, _ = strconv.Atoi(r.URL.Query().Get("apage"))
		if v.ApprovalsPage < 0 || v.ApprovalsPage > 100000 {
			http.Error(w, "Invalid page", http.StatusBadRequest)
			return
		}
	}
	if feed, err := h.api.AttentionKindBearer(r.Context(), bearer, "approval", v.ApprovalsPage); err != nil {
		h.logger.WarnContext(r.Context(), "needs you: list approvals failed", slog.String("err", err.Error()))
		v.ApprovalsError = "Domain approvals could not be read."
	} else {
		v.Approvals = pages.OpenOfKind(feed.Items, "approval")
		v.ApprovalsOpen, v.ApprovalsMore = feed.Open, feed.Truncated
		// Decisions not yet projected into work items, or that failed to
		// be: the list may be missing some.
		v.ApprovalsIncomplete = feed.Pending > 0 || feed.Failed > 0
	}
	if v.Tab == pages.NeedsYouBlocked {
		v.BlockedPage, _ = strconv.Atoi(r.URL.Query().Get("page"))
		if v.BlockedPage < 0 || v.BlockedPage > 100000 {
			http.Error(w, "Invalid page", http.StatusBadRequest)
			return
		}
	}
	if feed, err := h.api.AttentionKindBearer(r.Context(), bearer, "blocked", v.BlockedPage); err != nil {
		h.logger.WarnContext(r.Context(), "needs you: list blocked failed", slog.String("err", err.Error()))
		v.BlockedError = "Blocked calls could not be read."
	} else {
		v.Blocked = pages.OpenOfKind(feed.Items, "blocked")
		v.BlockedOpen, v.BlockedMore = feed.Open, feed.Truncated
		v.BlockedIncomplete = feed.Pending > 0 || feed.Failed > 0
	}
	cards := h.listCards(r.Context(), bearer)
	v.SharedBy = pages.SharedCards(cards)
	for i := range v.Held {
		pages.FillBreachWindows(&v.Held[i], cards)
	}
	_ = pages.NeedsYou(v).Render(r.Context(), w)
}

// dismissWarning handles POST /needs-you/warnings/{id}/dismiss: it claims
// the Card warning, then resolves it, so it leaves Warnings for everyone
// on the account. It changes nothing on the Card. A warning this person
// already holds (claimed in Needs attention, or a dismiss interrupted
// after its claim) is resolved directly: the API refuses a second claim
// even by its holder. One held by someone else offers no Dismiss; the API
// refuses it either way.
func (h *DashboardHandler) dismissWarning(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	back := "/needs-you?tab=" + pages.NeedsYouWarnings
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	if p, err := strconv.Atoi(r.FormValue("page")); err == nil && p > 0 && p <= 100000 {
		back += "&page=" + strconv.Itoa(p)
	}
	id := r.PathValue("id")
	revision, err := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	if err != nil || revision < 1 || !strings.HasPrefix(id, "card-warning:") {
		http.Error(w, "Invalid warning", http.StatusBadRequest)
		return
	}
	if h.api == nil {
		http.Error(w, "Warnings unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.FormValue("held") == "mine" {
		err = h.api.ActAttentionBearer(r.Context(), bearer, id, "resolve", revision)
	} else if err = h.api.ActAttentionBearer(r.Context(), bearer, id, "claim", revision); err == nil {
		// If this fails the warning stays held by this person, and its
		// row offers Dismiss again, resolving the claim directly.
		err = h.api.ActAttentionBearer(r.Context(), bearer, id, "resolve", revision+1)
	}
	switch {
	case errors.Is(err, apiclient.ErrConflict):
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError,
			Message: "Someone changed this warning first, or it was already dismissed. Here is where Warnings stand now."})
	case err != nil:
		h.logger.WarnContext(r.Context(), "needs you: dismiss warning failed", slog.String("warning", id), slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "The warning could not be dismissed. Try again shortly."})
	default:
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashOwnerAnswered,
			Message: "Dismissed for everyone on the account. Nothing on the Card changed; a new warning shows here if it runs low again."})
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// needsYouDetail renders GET /needs-you/{id}: the dashboard's view, or,
// opened by the tafs app (?lang=), the held call's screen in its language.
func (h *DashboardHandler) needsYouDetail(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Has("lang") {
		h.heldScreen(w, r, bearer)
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	v := pages.NeedsYouView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, Flash: convertFlash(flash)}
	if h.api == nil {
		v.LoadError = "apps/api is not configured."
		_ = pages.NeedsYou(v).Render(r.Context(), w)
		return
	}
	e, err := h.api.CardExceptionBearer(r.Context(), bearer, r.PathValue("id"))
	if errors.Is(err, apiclient.ErrReceiptNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		v.LoadError = "This action could not be loaded."
	} else {
		// Who answered, by email rather than sign-in id. Best effort:
		// without the members it still says "you" for the viewer.
		var members []apiclient.Member
		if e.ResolvedBy != "" && e.ResolvedBy != s.Subject {
			if ms, err := h.api.MembersBearer(r.Context(), bearer); err == nil {
				members = ms
			}
		}
		pages.NameAnswerer(e, s.Subject, s.Email, members)
		cards := h.listCards(r.Context(), bearer)
		pages.FillBreachWindows(e, cards)
		v.Detail = e
		v.SharedBy = pages.SharedCards(cards)
	}
	_ = pages.NeedsYou(v).Render(r.Context(), w)
}

// heldScreen renders a held call's screen as the tafs app opens it: one
// job, answering it, in the app's language, with KIFF's own lines for the
// call asked in that language.
func (h *DashboardHandler) heldScreen(w http.ResponseWriter, r *http.Request, bearer string) {
	lang := components.RequestLanguage(r)
	ctx := components.WithScreenLanguage(r.Context(), lang)
	flash, _ := h.sessions.ReadFlash(w, r)
	v := pages.HeldScreenView{Lang: lang, W: words.HeldIn(lang), Flash: convertFlash(flash)}
	if h.api == nil {
		v.LoadError = v.W.LoadError
		_ = pages.HeldScreen(v).Render(ctx, w)
		return
	}
	e, err := h.api.CardExceptionBearer(apiclient.WithLanguage(r.Context(), lang), bearer, r.PathValue("id"))
	switch {
	case errors.Is(err, apiclient.ErrReceiptNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.logger.WarnContext(r.Context(), "held screen: load failed", slog.String("err", err.Error()))
		v.LoadError = v.W.LoadError
	default:
		v.E = e
	}
	_ = pages.HeldScreen(v).Render(ctx, w)
}

// answerNeedsYou handles POST /needs-you/{id}/{answer}: approve-once,
// change-card or reject. It always returns to the action's page with a
// sentence saying what happened: on the held call's screen (?lang=), in
// its language.
func (h *DashboardHandler) answerNeedsYou(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	answer := r.PathValue("answer")
	back := pages.NeedsYouHref(id)
	// From the held call's screen (?lang=): back to it, and its words.
	screen, lang := r.URL.Query().Has("lang"), components.RequestLanguage(r)
	hw := words.HeldIn(lang)
	ctx := r.Context()
	if screen {
		back += "?lang=" + url.QueryEscape(lang)
		ctx = apiclient.WithLanguage(ctx, lang)
	}
	say := func(screenWords, dashboardWords string) string {
		if screen {
			return screenWords
		}
		return dashboardWords
	}
	if h.api == nil || id == "" {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "apps/api is not configured."})
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	refusal := apiclient.Refusal{NextStep: strings.TrimSpace(r.FormValue("next_step")), Note: strings.TrimSpace(r.FormValue("note"))}
	if answer == "reject" && refusal != (apiclient.Refusal{}) {
		_, err = h.api.RejectCardExceptionBearer(ctx, bearer, id, refusal)
	} else {
		_, err = h.api.AnswerCardExceptionBearer(ctx, bearer, id, answer)
	}
	switch {
	case errors.Is(err, apiclient.ErrRefusalNotAccepted):
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError,
			Message: say(hw.StepNotAccepted, "That next step is not one this Card offers, or the note is too long. Nothing was answered; choose again.")})
	case errors.Is(err, apiclient.ErrExceptionConflict):
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError,
			Message: say(hw.Conflict, "This was already answered, it expired, or its card changed since you opened it. Here is where it stands now.")})
	case errors.Is(err, apiclient.ErrForbidden) && refusalMessage(err) != "":
		// A refusal the person can act on: their role, separation of
		// duties, or an action they proposed or a Card they changed.
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: refusalMessage(err)})
	case err != nil:
		h.logger.WarnContext(r.Context(), "needs you: answer failed", slog.String("exception", id), slog.String("answer", answer), slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: say(hw.NotSaved, "Your answer could not be saved: "+err.Error())})
	case answer == "reject" && refusal.NextStep != "":
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashOwnerAnswered,
			Message: say(hw.DoneRefuseStep, "Refused, with the next step: "+pages.NextStepLabel(refusal.NextStep)+". The agent is told what to do instead. Nothing happened, and the card is unchanged.")})
	case screen:
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashOwnerAnswered, Message: heldDone(hw, answer)})
	default:
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashOwnerAnswered, Message: answerMessage(answer)})
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// heldDone is what an answer did, in the held call's screen's words.
func heldDone(hw words.Held, answer string) string {
	switch answer {
	case "approve-once":
		return hw.DoneApproveOnce
	case "change-card":
		return hw.DoneChangeCard
	case "reject":
		return hw.DoneRefuse
	}
	return hw.DoneRefuse
}

func answerMessage(answer string) string {
	switch answer {
	case "approve-once":
		return "Approved once. The next time the agent asks, this one action may go past the limit and still counts toward the card's totals. If a total has no room left, it waits until there is. The card is unchanged."
	case "change-card":
		return "The card now allows this, for this action and everything after it. The change is recorded on the card."
	case "reject":
		return "Rejected. Nothing happened, and the card is unchanged."
	}
	return "Saved."
}

// listCards reads the account's Cards for the sharing note and the names
// of the totals a held call is past. Best effort: without them the
// answers still work, only those words are missing.
func (h *DashboardHandler) listCards(ctx context.Context, bearer string) []apiclient.Mandate {
	cards, err := h.api.ListMandatesBearer(ctx, bearer)
	if err != nil {
		h.logger.WarnContext(ctx, "needs you: list cards failed", slog.String("err", err.Error()))
		return nil
	}
	return cards
}

// refusalMessage is the API's own sentence for a 403, when it sent one
// ({"error": ..., "message": ...} after the status line), else "".
func refusalMessage(err error) string {
	msg := err.Error()
	i := strings.Index(msg, "{")
	if i < 0 {
		return ""
	}
	var body struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(msg[i:]), &body) != nil {
		return ""
	}
	return body.Message
}

// bell renders the notification bell alone, for the dashboard's 15 second
// re-check. WithChrome has already read its feed for this request.
func (h *DashboardHandler) bell(w http.ResponseWriter, r *http.Request) {
	if _, ok := SessionFrom(r.Context()); !ok {
		http.Redirect(w, r, "/sign-in", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = components.NotificationBell().Render(r.Context(), w)
}
