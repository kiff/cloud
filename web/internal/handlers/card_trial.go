package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// sendCardTest makes one real decision against a card from its page:
// a fresh test entity is put into a state the job allows, then the
// card's agent proposes the job's action on it with the smallest
// parameters. It proves the card decides before anyone has changed
// their agent's code, and the decision, its receipt and the draw are
// as real as any other.
func (h *DashboardHandler) sendCardTest(w http.ResponseWriter, r *http.Request) {
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
	// A test request that got an answer lands on the Decisions tab,
	// where that answer is listed with its receipt.
	decided := target + "/decisions"
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
		fail("The card could not be read, so no test request was sent.")
		return
	}
	var m apiclient.Mandate
	for _, c := range cards {
		if c.ID == id {
			m = c
			break
		}
	}
	if m.ID == "" {
		http.NotFound(w, r)
		return
	}
	now := time.Now().UTC()
	if !cardInForce(m, now) {
		fail("This card is no longer in force, so there is nothing to test.")
		return
	}
	task, ok := pages.CardTaskFor(m)
	if !ok {
		fail("A test request is only available for cards issued for one of the standard jobs.")
		return
	}

	entity := "kiff-test-" + strconv.FormatInt(now.UnixMilli(), 36)
	event, _ := json.Marshal(map[string]any{
		"id": "evt-" + entity, "adapter": "cloud", "type": task.SetupEvent,
		"source": "kiff-card-test", "received_at": now.Format(time.RFC3339),
		"entity_id": entity, "entity_type": task.EntityType, "actor_id": m.Subject,
		"payload": map[string]any{},
	})
	if _, err := h.api.IngestEventForDomainBearer(r.Context(), bearer, m.Domain, event); err != nil {
		h.logger.WarnContext(r.Context(), "card test: setup event failed", slog.String("card", m.ID), slog.String("err", err.Error()))
		fail("The test " + strings.ToLower(task.EntityType) + " could not be created, so no decision was requested: " + err.Error())
		return
	}
	proposal, _ := json.Marshal(map[string]any{
		"id": "card-test-" + entity, "domain": m.Domain, "entity_id": entity, "entity_type": task.EntityType,
		"action_name": task.Action, "actor_id": m.Subject, "parameters": task.TestParams,
		"reasoning_summary": "Test request sent from the card's page",
	})
	out, err := h.api.DecideBearer(r.Context(), bearer, proposal)
	if err != nil {
		h.logger.WarnContext(r.Context(), "card test: decide failed", slog.String("card", m.ID), slog.String("err", err.Error()))
		fail("KIFF could not be asked for a decision: " + err.Error())
		return
	}
	if out.Outcome == "allowed" {
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashCardTestAllowed,
			Message: "KIFF allowed " + m.Subject + " to " + strings.ToLower(task.Label[:1]) + task.Label[1:] +
				" on test " + strings.ToLower(task.EntityType) + " " + entity + ". It drew " + task.TestDraw +
				" from today's balance. The decision is listed below with its signed receipt, and the draw is on the statement.",
		})
		http.Redirect(w, r, decided, http.StatusSeeOther)
		return
	}
	if out.Outcome == "owner_required" && out.Exception != nil {
		// Outside the card: held for the owner, not refused. The answer
		// is given where every held action is answered.
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashCardTestHeld,
			Message: "This test request is outside the card, so it came home to you instead of being refused. " +
				"Nothing has happened yet: approve it once, change the card to allow it, or reject it.",
		})
		http.Redirect(w, r, pages.NeedsYouHref(out.Exception.ID), http.StatusSeeOther)
		return
	}
	msg := "KIFF answered " + out.Outcome
	if len(out.Reasons) > 0 {
		msg += " (" + strings.Join(out.Reasons, ", ") + ")"
	}
	if out.Message != "" {
		msg += ": " + out.Message
	}
	msg += ". Nothing was drawn from the card"
	switch out.Outcome {
	case "blocked", "approval_required":
		// A governance decision: KIFF signs a receipt for it.
		msg += "; the decision is listed below with its signed receipt."
	default:
		msg += ", and the request was rejected before a decision, so it has no receipt."
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashCardTestRefused, Message: msg})
	http.Redirect(w, r, decided, http.StatusSeeOther)
}

// createCardAgentKey mints an API key for the card's agent holding the
// role its job requires, bound to the card's holder. A key minted from
// the API keys page carries no roles and no binding, and every standard
// job requires both: without the role the agent is refused on
// permissions, and without the binding it is refused because nothing
// ties it to the card's holder. The key is shown once, exactly as on
// the API keys page.
func (h *DashboardHandler) createCardAgentKey(w http.ResponseWriter, r *http.Request) {
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
		fail("The card could not be read, so no key was created.")
		return
	}
	var m apiclient.Mandate
	for _, c := range cards {
		if c.ID == id {
			m = c
			break
		}
	}
	if m.ID == "" {
		http.NotFound(w, r)
		return
	}
	task, ok := pages.CardTaskFor(m)
	if !ok || !cardInForce(m, time.Now()) {
		fail("A key with the right role can only be created here for a card in force that was issued for one of the standard jobs.")
		return
	}
	// Bound to the card's holder and, for an environment-scoped card, its
	// environment, so the key cannot choose another environment's card by
	// naming it.
	created, err := h.api.CreateBoundKeyBearer(r.Context(), bearer, "Agent key for card "+m.ID, []string{task.Role}, m.Subject, m.Environment)
	if err != nil {
		h.logger.WarnContext(r.Context(), "card key: mint failed", slog.String("card", m.ID), slog.String("err", err.Error()))
		fail("A key with the " + task.Role + " role could not be created from your account. Ask for guided setup and KIFF will issue one.")
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind: session.FlashKeyRevealed,
		Key:  &session.FlashKeyPayload{ID: created.ID, Plaintext: created.Plaintext, Label: created.Label, Owner: s.Subject},
	})
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// grantCardAgentKeyRole gives the key the card's agent already acts with
// the role the card's job needs, so one key carries the agent from
// observe to enforce. The key is chosen here, from the card and the
// tenant's keys, never from the form: the same rule the card page used
// to offer it. The cloud refuses the grant unless the owner may mint
// that role.
func (h *DashboardHandler) grantCardAgentKeyRole(w http.ResponseWriter, r *http.Request) {
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
		fail("The card could not be read, so no role was granted.")
		return
	}
	var m apiclient.Mandate
	for _, c := range cards {
		if c.ID == id {
			m = c
			break
		}
	}
	if m.ID == "" {
		http.NotFound(w, r)
		return
	}
	task, ok := pages.CardTaskFor(m)
	if !ok || !cardInForce(m, time.Now()) {
		fail("A role can only be granted here for a card in force that was issued for one of the standard jobs.")
		return
	}
	keys, err := h.api.ListKeysBearer(r.Context(), bearer)
	if err != nil {
		fail("This agent's keys could not be read, so no role was granted.")
		return
	}
	key := pages.HolderKeyFor(m, keys)
	if key == nil {
		fail(m.Subject + " has no key of its own yet. Create one for this agent instead.")
		return
	}
	if slices.Contains(key.Roles, task.Role) {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashKeyRoleGranted,
			Message: key.Label + " already has the " + task.Role + " role."})
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if _, err := h.api.AddKeyRolesBearer(r.Context(), bearer, key.ID, []string{task.Role}); err != nil {
		h.logger.WarnContext(r.Context(), "card key: grant role failed", slog.String("card", m.ID), slog.String("key", key.ID), slog.String("err", err.Error()))
		fail("The " + task.Role + " role could not be granted from your account. Ask for guided setup and KIFF will do it.")
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashKeyRoleGranted,
		Message: key.Label + " now has the " + task.Role + " role. " + m.Subject + " keeps using the same key."})
	http.Redirect(w, r, target, http.StatusSeeOther)
}
