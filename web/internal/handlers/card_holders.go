package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/session"
)

// shareCard handles POST /cards/{id}/holders: share the Card with another
// agent, or stop sharing it with one (the mandates design amendment, "a
// Card can be shared"). The new list is built from the Card as the API
// returns it and sent whole; the API checks it under the Card's lock, and
// removing an agent cancels only that agent's held and approved actions.
func (h *DashboardHandler) shareCard(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	target := "/cards/" + url.PathEscape(id)
	fail := func(msg string) {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: msg})
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		fail("The form could not be read.")
		return
	}
	action, agent := r.PostFormValue("action"), strings.TrimSpace(r.PostFormValue("agent"))
	// The holder sheet offers moving beside sharing; a move is POST
	// /cards/{id}/move's, with the agent as its subject.
	if action == "move" && agent != "" {
		r.PostForm.Set("subject", agent)
		h.moveMandate(w, r)
		return
	}
	if (action != "add" && action != "remove") || agent == "" {
		fail("Name the agent to share this Card with.")
		return
	}
	if h.api == nil {
		fail("apps/api is not configured.")
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	cards, err := h.api.ListMandatesBearer(r.Context(), bearer)
	if err != nil {
		fail("The Card could not be read, so its holders were not changed.")
		return
	}
	var card *apiclient.Mandate
	for i := range cards {
		if cards[i].ID == id {
			card = &cards[i]
		}
	}
	if card == nil {
		http.NotFound(w, r)
		return
	}
	holders := slices.Clone(card.Holders())
	var done string
	switch action {
	case "add":
		if slices.Contains(holders, agent) {
			fail(agent + " already holds this Card.")
			return
		}
		holders = append(holders, agent)
		done = agent + " now shares this Card: every holder draws on the same limits, and each action still names the agent that took it."
	case "remove":
		if !slices.Contains(holders, agent) {
			fail(agent + " does not hold this Card.")
			return
		}
		if len(holders) == 1 {
			fail("A Card needs at least one holder. Move it to another agent, or revoke it.")
			return
		}
		holders = slices.DeleteFunc(holders, func(h string) bool { return h == agent })
		done = agent + " no longer holds this Card. Anything held or approved for it is cancelled; the other holders' answers stand."
	}
	if _, err := h.api.SetHoldersBearer(r.Context(), bearer, id, holders, card.Holders()); err != nil {
		if errors.Is(err, apiclient.ErrHoldersChanged) {
			fail("The holders changed since you opened this Card, so nothing was changed. Here is who holds it now.")
			return
		}
		var rejected *apiclient.MandateRejectedError
		if errors.As(err, &rejected) {
			fail("The holders were not changed: " + rejected.Message)
			return
		}
		h.logger.WarnContext(r.Context(), "cards: set holders failed", slog.String("mandate", id), slog.String("err", err.Error()))
		fail("The holders could not be changed. Nothing was changed; try again in a moment.")
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashCardHoldersChanged, Message: done})
	http.Redirect(w, r, target, http.StatusSeeOther)
}
