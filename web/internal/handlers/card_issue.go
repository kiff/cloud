package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// cardNew renders the issuance form.
//
// The form offers only what the domain declares — its actions, and the
// parameters it types as numeric — because a card naming something the
// contract does not have is refused at issue, and a person should see
// that before filling in a ceiling rather than after.
func (h *DashboardHandler) cardNew(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	// A new card starts from the job, not the contract. The full form
	// is for changing a card and for anyone who asks for it.
	q := r.URL.Query()
	if q.Get("card") == "" && q.Get("advanced") == "" {
		_ = pages.CardQuick(pages.CardQuickView{
			Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName,
			Flash: convertFlash(flash),
			Form:  pages.CardQuickForm{Subject: strings.TrimSpace(q.Get("subject")), Task: strings.TrimSpace(q.Get("task")), Limit: pages.PrefillLimit(q.Get("limit"))},
		}).Render(r.Context(), w)
		return
	}
	view := pages.CardNewView{
		Email:       s.Email,
		Subject:     s.Subject,
		DisplayName: s.DisplayName,
		Flash:       convertFlash(flash),
	}
	// ?card=<id> is the change-a-ceiling path. Changing a card is
	// re-issuing it under its own id, so it is this form with the
	// card's current terms already in it — not a second mechanism, and
	// not a blank form someone has to retype the rest of the card into
	// in order to move one number.
	if id := strings.TrimSpace(r.URL.Query().Get("card")); id != "" {
		h.prefillFromCard(r, s, id, &view)
	} else if subject := strings.TrimSpace(r.URL.Query().Get("subject")); subject != "" {
		// Arriving from "this agent holds no card": the one thing
		// already known is who it would be for.
		view.Form.MandateSubj = subject
	}
	h.renderCardForm(w, r, s, view)
}

// prefillFromCard fills the form from a card that already exists.
//
// A failure to load it is not fatal: the form still works, and the
// worst case is someone retyping terms rather than being told the card
// cannot be changed. It says so rather than silently presenting a
// blank form as if it were the card's current state, because
// submitting that would quietly narrow the card to whatever the blank
// form contained.
func (h *DashboardHandler) prefillFromCard(
	r *http.Request, s session.Session, id string, view *pages.CardNewView,
) {
	if h.api == nil {
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		return
	}
	cards, err := h.api.ListMandatesBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "card form: list for prefill failed", slog.String("err", err.Error()))
		view.LoadError = "That card's current terms could not be read. Issuing from this form would replace them with whatever is filled in here."
		return
	}
	for _, m := range cards {
		if m.ID != id {
			continue
		}
		view.Editing = m.ID
		view.Step = r.URL.Query().Get("step")
		view.Single = view.Step != "" && r.URL.Query().Get("one") == "1"
		view.SelectedDomain = m.Domain
		form := pages.MandateFormValues{
			ID:          m.ID,
			MandateSubj: m.Subject,
			Environment: m.Environment,
			States:      map[string][]string{},
		}
		for _, g := range m.Grants {
			form.Actions = append(form.Actions, g.Action)
			if len(g.EntityStates) > 0 {
				form.States[g.Action] = g.EntityStates
			}
		}
		if len(m.Aggregates) > 0 {
			a := m.Aggregates[0]
			form.Limit = strconv.FormatInt(a.Limit, 10)
			form.Window = a.Window
			if strings.HasPrefix(a.Quantity, "sum(") {
				form.Measure = m.Aggregates[0].Measure
				form.QuantityKind = "sum"
				form.Parameter = strings.TrimSuffix(strings.TrimPrefix(a.Quantity, "sum("), ")")
			} else {
				form.Measure = m.Aggregates[0].Measure
				form.QuantityKind = "count"
			}
		}
		if m.ValidUntil != nil {
			form.ValidUntil = m.ValidUntil.UTC().Add(-24 * time.Hour).Format("2006-01-02")
		}
		form.PerActionMax, form.PreservedGrants = grantsForForm(m)
		form.OnExceed = "refuse"
		if m.ComesHome() {
			form.OnExceed = "owner"
		}
		form.Hold = pages.HoldFromSeconds(m.HoldExpirySeconds)
		view.Form = form
		return
	}
	view.LoadError = "No card with that name was found, so nothing was filled in."
}

// renderCardForm loads the domain projection the form needs and
// renders it. Shared by the GET and by the POST's refusal path, so a
// rejected submission comes back with the same choices and the fields
// still filled in.
func (h *DashboardHandler) renderCardForm(
	w http.ResponseWriter, r *http.Request, s session.Session, view pages.CardNewView,
) {
	if h.api == nil {
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to issue a card."
		_ = pages.CardNew(view).Render(r.Context(), w)
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard mandate form: refresh bearer failed", slog.String("err", err.Error()))
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	domains, err := h.api.ListDomainsBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard mandate form: list domains failed", slog.String("err", err.Error()))
		view.LoadError = "The domains could not be read, so this form cannot offer the actions a limit would cover."
		_ = pages.CardNew(view).Render(r.Context(), w)
		return
	}
	view.Domains = domains
	if len(domains) == 0 {
		_ = pages.CardNew(view).Render(r.Context(), w)
		return
	}

	selected := strings.TrimSpace(r.URL.Query().Get("domain"))
	if selected == "" {
		selected = strings.TrimSpace(view.SelectedDomain)
	}
	if selected == "" {
		selected = domains[0].Name
	}
	view.SelectedDomain = selected

	detail, err := h.api.GetDomainDetailBearer(r.Context(), bearer, selected)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard mandate form: domain detail failed",
			slog.String("domain", selected), slog.String("err", err.Error()))
		view.LoadError = "That domain's actions could not be read, so this form cannot offer them."
		_ = pages.CardNew(view).Render(r.Context(), w)
		return
	}
	view.Actions = detail.Actions
	if detail.Agents.Observed() {
		view.Agents = detail.Agents.Agents
	}
	_ = pages.CardNew(view).Render(r.Context(), w)
}

// issueMandate creates or updates a mandate from the form.
//
// A refusal from the cloud is re-rendered on the form with the cloud's
// own sentence and the submitted values intact, rather than flashed and
// redirected: the refusals here explain why a limit is impossible, and
// someone reading one usually wants to change one field, not retype
// six.
func (h *DashboardHandler) issueMandate(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/cards/new", http.StatusSeeOther)
		return
	}

	form := mandateFormFromPost(r.PostForm)
	domainName := strings.TrimSpace(r.PostFormValue("domain"))

	view := pages.CardNewView{
		Email:          s.Email,
		Subject:        s.Subject,
		DisplayName:    s.DisplayName,
		Form:           form,
		SelectedDomain: domainName,
	}

	req, err := buildIssueMandateRequest(form, domainName)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		view.Rejected = err.Error()
		h.renderCardForm(w, r, s, view)
		return
	}

	if h.api == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to issue a card."
		_ = pages.CardNew(view).Render(r.Context(), w)
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	// The Card's existing terms: an unnamed Card is named after its agent,
	// and a Card being changed keeps the start this form does not show.
	cards, listErr := h.api.ListMandatesBearer(r.Context(), bearer)
	if form.ID == "" {
		// An unnamed Card is named after its agent, so issuing another
		// for the same agent changes the first. The name is a slug, so
		// it is checked against who holds the Card under it.
		err := listErr
		if err != nil {
			h.logger.WarnContext(r.Context(), "card form: list cards for a name failed", slog.String("err", err.Error()))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			view.Rejected = "Your existing cards could not be read, so a name for this one could not be chosen safely. Try again in a moment, or name the card."
			h.renderCardForm(w, r, s, view)
			return
		}
		req.ID, _ = ownCardID(req.ID, cards, time.Now(), func(m apiclient.Mandate) bool {
			return isCardOf(m, req.Subject, req.Domain)
		})
	} else if listErr != nil {
		h.logger.WarnContext(r.Context(), "card form: list cards before a change failed", slog.String("err", listErr.Error()))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		view.Rejected = "Your existing cards could not be read, so this one was not changed. Try again in a moment."
		h.renderCardForm(w, r, s, view)
		return
	}
	for _, c := range cards {
		if c.ID == req.ID {
			// Omitted, the API would record no start, and a Card dated to
			// start later would be in force at once.
			req.ValidFrom = c.ValidFrom
		}
	}

	m, err := h.api.IssueMandateBearer(r.Context(), bearer, req)
	if err != nil {
		var rejected *apiclient.MandateRejectedError
		if errors.As(err, &rejected) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			view.Rejected = rejected.Message
			h.renderCardForm(w, r, s, view)
			return
		}
		h.logger.WarnContext(r.Context(), "dashboard: issue mandate failed",
			slog.String("mandate", req.ID), slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind:    session.FlashError,
			Message: "The card could not be issued: " + err.Error(),
		})
		http.Redirect(w, r, "/cards/new", http.StatusSeeOther)
		return
	}

	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind:    session.FlashMandateIssued,
		Message: "Card in force for " + m.Subject + ". It binds from the next proposal; nothing already done is counted against it.",
	})
	http.Redirect(w, r, "/cards/"+url.PathEscape(m.ID), http.StatusSeeOther)
}

// moveMandate gives a card to another agent. The API keeps the card's
// id, terms, balance and statement and records the move as a revision.
func (h *DashboardHandler) moveMandate(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	target := "/cards/" + url.PathEscape(id)
	to := strings.TrimSpace(r.PostFormValue("subject"))
	if h.api == nil || id == "" || to == "" {
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "choose the agent that should hold this card",
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
	if err := h.api.MoveMandateBearer(r.Context(), bearer, id, to); err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: move mandate failed",
			slog.String("mandate", id), slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "could not move that card: " + err.Error(),
		})
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind:    session.FlashMandateIssued,
		Message: "Card " + id + " now held by " + to + ". Same terms, same balance, same statement; the move is recorded.",
	})
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// revokeMandate ends a card's authority.
func (h *DashboardHandler) revokeMandate(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	target := "/cards/" + url.PathEscape(strings.TrimSpace(r.PathValue("id")))
	id := strings.TrimSpace(r.PathValue("id"))
	if h.api == nil || id == "" {
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "a card id is required",
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
	if err := h.api.RevokeMandateBearer(r.Context(), bearer, id); err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: revoke mandate failed",
			slog.String("mandate", id), slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "could not revoke that card: " + err.Error(),
		})
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind: session.FlashMandateRevoked,
		Message: "Card " + id + " revoked. The agent it named now has no authority under it, " +
			"and this cannot be undone by re-issuing the same name.",
	})
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (h *DashboardHandler) archiveMandate(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if h.api == nil || id == "" {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "a card id is required"})
		http.Redirect(w, r, "/cards?status=revoked", http.StatusSeeOther)
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if err := h.api.ArchiveMandateBearer(r.Context(), bearer, id); err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: archive mandate failed", slog.String("mandate", id), slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "could not archive that card: " + err.Error()})
		http.Redirect(w, r, "/cards/"+url.PathEscape(id), http.StatusSeeOther)
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashMandateRevoked, Message: "Card archived. Its statement, changes and audit evidence remain available."})
	http.Redirect(w, r, "/cards?status=archived", http.StatusSeeOther)
}

// buildIssueMandateRequest turns the form into the api's body, and
// refuses what the form itself can see is wrong.
//
// It checks only what a form can know without the domain in hand: that
// a subject was given, that at least one action was ticked, that the
// ceiling is a positive number. Everything about whether the mandate is
// *coherent with the contract* — a summed parameter that is not
// numeric, a grant wider than the action allows — belongs to the cloud,
// which is the only place that can answer it correctly, and is where
// the answer has to be enforced anyway.
func buildIssueMandateRequest(f pages.MandateFormValues, domainName string) (apiclient.IssueMandateRequest, error) {
	if f.Measure != "" && f.Measure != "attempts" && f.Measure != "effects" {
		return apiclient.IssueMandateRequest{}, errors.New("choose attempts or effects for this limit.")
	}
	if f.MandateSubj == "" {
		return apiclient.IssueMandateRequest{}, errors.New("name the agent this limit is for. It has to be the actor id the agent already sends when it proposes an action.")
	}
	// A Card whose grants the form cannot represent is reissued with
	// exactly the grants it has: the form showed them read-only and
	// posted none of its own. Changing a grant field alongside them
	// would be two answers to one question, so it is refused.
	var preserved []apiclient.MandateGrant
	if f.PreservedGrants != "" {
		if f.PerActionMax != "" {
			return apiclient.IssueMandateRequest{}, errors.New("this card's actions and per-action limits cannot be edited in this form, so they are kept as they are. Leave the per-action field empty, or change them through the API.")
		}
		if err := json.Unmarshal([]byte(f.PreservedGrants), &preserved); err != nil || len(preserved) == 0 {
			return apiclient.IssueMandateRequest{}, errors.New("this card's existing grants could not be read back, so it was not changed. Reload the card and try again.")
		}
	} else if len(f.Actions) == 0 {
		return apiclient.IssueMandateRequest{}, errors.New("tick at least one action. A limit over no actions would bound nothing.")
	}
	limit, err := strconv.ParseInt(f.Limit, 10, 64)
	if err != nil || limit <= 0 {
		return apiclient.IssueMandateRequest{}, errors.New("the ceiling has to be a whole number above zero. A ceiling of zero is a revocation, which has its own button.")
	}

	quantity := "count"
	if f.QuantityKind == "sum" {
		if f.Parameter == "" {
			return apiclient.IssueMandateRequest{}, errors.New("choose the parameter to add up, or count the actions instead.")
		}
		quantity = "sum(" + f.Parameter + ")"
	}

	// An optional per-action limit caps one action of the same summed
	// parameter the ceiling adds up. Counting actions has no per-action
	// size to cap: every action counts as one.
	var perAction []apiclient.MandatePerAction
	if f.PerActionMax != "" {
		if quantity == "count" {
			return apiclient.IssueMandateRequest{}, errors.New("a per-action limit caps the size of one action, so it needs a parameter to add up. Choose one, or leave the per-action limit empty.")
		}
		max, err := strconv.ParseInt(f.PerActionMax, 10, 64)
		if err != nil || max <= 0 {
			return apiclient.IssueMandateRequest{}, errors.New("the per-action limit has to be a whole number above zero, or empty for none.")
		}
		perAction = []apiclient.MandatePerAction{{Quantity: quantity, Max: max}}
	}

	window := f.Window
	if window == "" {
		window = "calendar_day"
	}

	id := f.ID
	if id == "" {
		id = mandateIDFromSubject(f.MandateSubj)
	}

	grants := make([]apiclient.MandateGrant, 0, len(f.Actions))
	if preserved != nil {
		grants = preserved
	}
	for _, a := range f.Actions {
		if preserved != nil {
			break
		}
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		// States left unticked are sent as none, which the api reads as
		// "the action's own states apply". A card narrows; it never
		// widens, so an empty list is the honest way to say "wherever
		// the domain already allows it".
		grants = append(grants, apiclient.MandateGrant{Action: a, EntityStates: f.States[a], PerActionMax: perAction})
	}

	var onExceed string
	switch f.OnExceed {
	case "", "owner", "refuse":
		onExceed = f.OnExceed
	default:
		return apiclient.IssueMandateRequest{}, errors.New("choose what happens to an action past the card: Ask (hold it for an approver) or Stop (refuse it).")
	}

	// How long a held action waits only means something when it is held.
	// Empty keeps the Card's current wait (10 minutes when new).
	var hold string
	if onExceed == "owner" && f.Hold != "" {
		if !validHold(f.Hold) {
			return apiclient.IssueMandateRequest{}, errors.New("choose how long a held action waits for an answer: from 1 minute to 7 days.")
		}
		hold = f.Hold
	}

	validUntil, err := parseCardExpiry(f.ValidUntil)
	if err != nil {
		return apiclient.IssueMandateRequest{}, err
	}

	return apiclient.IssueMandateRequest{
		ID:          id,
		Subject:     f.MandateSubj,
		Domain:      domainName,
		Environment: f.Environment,
		Grants:      grants,
		Aggregates: []apiclient.MandateAggregate{
			{Quantity: quantity, Limit: limit, Window: window, Measure: f.Measure},
		},
		ValidUntil: validUntil,
		OnExceed:   onExceed,
		HoldExpiry: hold,
	}, nil
}

// mandateFormFromPost reads the issue form as posted. A pure function of
// the values, so what the walkthrough posts is tested without a server.
func mandateFormFromPost(v url.Values) pages.MandateFormValues {
	get := func(k string) string { return strings.TrimSpace(v.Get(k)) }
	form := pages.MandateFormValues{
		ID:              get("id"),
		MandateSubj:     get("subject"),
		Environment:     get("environment"),
		Actions:         v["actions"],
		QuantityKind:    get("quantity_kind"),
		Measure:         get("measure"),
		Parameter:       get("parameter"),
		Limit:           get("limit"),
		PerActionMax:    get("per_action_max"),
		PreservedGrants: get("preserved_grants"),
		Window:          get("window"),
		ValidUntil:      get("valid_until"),
		States:          statesFromForm(v),
		OnExceed:        get("on_exceed"),
		Hold:            get("hold"),
	}
	// The walkthrough offers the agents KIFF has seen as a choice; the
	// typed id is used only when "another agent" is chosen, or when no
	// choice was posted at all (an older form, or no agents seen yet).
	if c := get("subject_choice"); c != "" && c != "other" {
		form.MandateSubj = c
	}
	return form
}

// statesFromForm collects the per-action state boxes, which the form
// submits as states.<ACTION>. Kept out of the request builder so that
// builder stays a pure function of the form values, which is what its
// tests exercise.
func statesFromForm(values url.Values) map[string][]string {
	var out map[string][]string
	for k, v := range values {
		action, ok := strings.CutPrefix(k, "states.")
		if !ok || action == "" || len(v) == 0 {
			continue
		}
		if out == nil {
			out = map[string][]string{}
		}
		out[action] = v
	}
	return out
}

// parseCardExpiry reads the date input into the instant the card stops.
//
// The end of the chosen day rather than its start: someone who types
// today's date means "this card is good for today", not "this card
// expired at breakfast". UTC, because the input carries no zone and
// guessing one from the browser would make the same date mean
// different instants for two people looking at the same card.
func parseCardExpiry(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	d, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		return nil, errors.New("the expiry has to be a date, or blank for a card that holds until it is revoked.")
	}
	end := d.Add(24 * time.Hour)
	return &end, nil
}

// mandateIDFromSubject derives a name for a limit left unnamed.
//
// Deliberately derived from the subject alone, so issuing a second
// unnamed limit for the same agent updates the first rather than
// quietly accumulating two ceilings nobody named. Someone who wants two
// names one of them.
func mandateIDFromSubject(subject string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(subject) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	// Trim before prefixing: a subject with leading punctuation would
	// otherwise leave a doubled dash in the middle of the name.
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "limit"
	}
	return "limit-" + slug
}

// grantsForForm decides whether the form can edit an existing Card's
// grants. It can when each action appears once and the per-action caps,
// if any, are one cap on every action, on the parameter the ceiling
// sums, at one amount: then the checkboxes and the single field say it
// all. Anything else (the same action granted twice with different state
// scopes, different caps, several parameters, another parameter) is
// returned as the complete grant list, which the form shows read-only
// and the edit reissues exactly. Reviews on: editing through a
// representation that cannot hold the Card's terms merged caps and
// dropped grants, which could loosen a limit.
func grantsForForm(m apiclient.Mandate) (simple, preserved string) {
	seen := map[string]bool{}
	representable := true
	var capped int
	var first apiclient.MandatePerAction
	for _, g := range m.Grants {
		key := strings.ToUpper(g.Action)
		if seen[key] || g.Resources != nil || len(g.NextSteps) > 0 {
			// The form cannot show a resource scope or next steps;
			// reissuing through it would drop them, widening the Card or
			// taking the owner's answers away.
			representable = false
		}
		seen[key] = true
		if len(g.PerActionMax) == 0 {
			continue
		}
		capped++
		if len(g.PerActionMax) != 1 {
			representable = false
			continue
		}
		p := g.PerActionMax[0]
		if capped == 1 {
			first = p
		}
		if p != first || len(m.Aggregates) == 0 || p.Quantity != m.Aggregates[0].Quantity {
			representable = false
		}
	}
	if capped > 0 && capped != len(m.Grants) {
		representable = false
	}
	if representable {
		if capped == 0 {
			return "", ""
		}
		return strconv.FormatInt(first.Max, 10), ""
	}
	b, err := json.Marshal(m.Grants)
	if err != nil {
		return "", ""
	}
	return "", string(b)
}
