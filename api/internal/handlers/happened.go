package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

// "Did this happen?" with its context and three answers (kiff/tafs#498):
// each call KIFF asks about says which taf made it, at which vendor, on
// what, when, why KIFF can't tell, and where to look; and the owner
// answers Yes (routine), No (critical) or "not sure" (safe), which keeps
// the ask with them for a day while Coach helps them check.

// The three answers, as the apps send them.
const (
	answerYes     = "yes"
	answerNo      = "no"
	answerNotSure = "not_sure"
)

// happenedCall is one call to confirm, as the list carries it: the record
// (tooloutcome.Unproven) and KIFF's words around it, in the reader's
// language.
type happenedCall struct {
	tooloutcome.Unproven
	// Taf is the agent that made the call, with the name people read it by.
	Taf happenedTaf `json:"taf"`
	// AmountWords is the amount in its unit, as the Cards write it.
	AmountWords string `json:"amount_words"`
	// WhenWords is when it was sent, in the account's time zone.
	WhenWords string `json:"when_words"`
	// Why is why KIFF can't tell, in a sentence.
	Why string `json:"why"`
	// Answers are the three answers, with their words and the key each takes.
	Answers []happenedAnswer `json:"answers"`
}

type happenedTaf struct {
	Agent string `json:"agent"`
	Name  string `json:"name,omitempty"`
}

// happenedAnswer is one answer: its words, what follows from it, and the
// key it takes ("window": a tap inside an approval window; "critical":
// the approval key behind the fingerprint; "device": the device key).
type happenedAnswer struct {
	Answer string `json:"answer"`
	Words  string `json:"words"`
	Detail string `json:"detail"`
	Key    string `json:"key"`
}

// happenedAnswers are the three answers in lang.
func happenedAnswers(lang string) []happenedAnswer {
	in := wordsIn(lang)
	return []happenedAnswer{
		{Answer: answerYes, Words: in.HappenedYes, Detail: in.HappenedYesDetail, Key: "window"},
		{Answer: answerNo, Words: in.HappenedNo, Detail: in.HappenedNoDetail, Key: "critical"},
		{Answer: answerNotSure, Words: in.HappenedNotSure, Detail: in.HappenedNotSureDetail, Key: "device"},
	}
}

// happenedViews is calls with KIFF's words around each, in lang: the
// taf's name from the account's agents, the vendor's name from the
// gateway where the profile had none, the amount and the time as the
// account reads them, why KIFF can't tell, and the answers.
func (h *MandatesHandler) happenedViews(r *http.Request, tenantID string, calls []tooloutcome.Unproven) []happenedCall {
	lang := replyLanguage(r)
	names := h.agentNames(r.Context(), tenantID)
	var vendors map[string]string
	loc := h.tenantZone(r.Context(), tenantID)
	now := h.now()
	out := make([]happenedCall, 0, len(calls))
	for _, c := range calls {
		if c.Vendor == "" {
			if vendors == nil {
				vendors = h.vendorTools(r, tenantID)
				if vendors == nil {
					vendors = map[string]string{}
				}
			}
			c.Vendor = vendors[c.Tool]
		}
		amount := inUnit(c.Amount, c.Unit, c.Scale, lang)
		// The status line and the call's line in the reader's language, as the
		// rest of the card is: the store's are English.
		c.StatusWords = happenedStatus(c, lang)
		if c.Title != "" {
			c.Words = c.Title + ": " + amount + "."
		}
		out = append(out, happenedCall{
			Unproven:    c,
			Taf:         happenedTaf{Agent: c.Agent, Name: names[c.Agent]},
			AmountWords: amount,
			WhenWords:   whenWords(c.At, now, loc, lang),
			Why:         happenedWhy(c, lang),
			Answers:     happenedAnswers(lang),
		})
	}
	return out
}

// agentNames is the account's agents by id, with the name people read
// them by. Best effort: none when they can't be read.
func (h *MandatesHandler) agentNames(ctx context.Context, tenantID string) map[string]string {
	names := map[string]string{}
	if h.agents == nil {
		return names
	}
	list, err := h.agents.List(ctx, tenantID)
	if err != nil {
		h.logger.WarnContext(ctx, "did this happen: agents unreadable", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return names
	}
	for _, a := range list {
		names[a.ID] = a.Name
	}
	return names
}

// tenantZone is the account's time zone; UTC when it has none or can't be read.
func (h *MandatesHandler) tenantZone(ctx context.Context, tenantID string) *time.Location {
	if h.tenants == nil {
		return time.UTC
	}
	t, err := h.tenants.Get(ctx, tenantID)
	if err != nil {
		return time.UTC
	}
	loc, err := time.LoadLocation(tenant.TimeZoneFor(t))
	if err != nil {
		return time.UTC
	}
	return loc
}

// whenWords says when at was, seen from now in loc: "today 09:14",
// "yesterday 18:02", else the day and month ("3 Nov 09:14").
func whenWords(at, now time.Time, loc *time.Location, lang string) string {
	at, now = at.In(loc), now.In(loc)
	clock := at.Format("15:04")
	ay, am, ad := at.Date()
	ny, nm, nd := now.Date()
	yesterday := now.AddDate(0, 0, -1)
	yy, ym, yd := yesterday.Date()
	in := wordsIn(lang)
	switch {
	case ay == ny && am == nm && ad == nd:
		return in.Today + " " + clock
	case ay == yy && am == ym && ad == yd:
		return in.Yesterday + " " + clock
	}
	return in.DayMonth(ad, am, clock)
}

// happenedStatus is c's status line in lang: what is known (the vendor
// refused it, or its answer was lost, or it's one of a new connection's first
// calls) and what to check, never a double negative.
func happenedStatus(c tooloutcome.Unproven, lang string) string {
	in := wordsIn(lang)
	switch c.Status {
	case tooloutcome.Unknown:
		return in.StatusLost(c.Vendor)
	case tooloutcome.Probation:
		return in.StatusProbation
	}
	return in.StatusRefused(c.Vendor)
}

// happenedWhy is why KIFF can't tell what became of c, in lang: how the
// call ended, and whether the vendor's records can be searched for it.
// The vendor is named when the profile names it.
func happenedWhy(c tooloutcome.Unproven, lang string) string {
	in := wordsIn(lang)
	switch c.Status {
	case tooloutcome.Unknown:
		return in.WhyLost(c.Vendor, c.HasStatusRead)
	case tooloutcome.Probation:
		return in.WhyProbation
	}
	return in.WhyRefused(c.Vendor, c.HasStatusRead)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// callWords is the call's facts in one line, in lang, for Coach's task:
// the action at the vendor, the amount, the counterparty, the taf, when.
func callWords(c happenedCall, lang string) string {
	parts := []string{c.Title}
	in := wordsIn(lang)
	if c.Vendor != "" {
		parts[0] = in.CallAtVendor(parts[0], c.Vendor)
	}
	parts = append(parts, c.AmountWords)
	if cp := c.Counterparty; cp != nil {
		words := cp.Noun
		if cp.Words != "" {
			words += " " + cp.Words
		} else if cp.ID != "" {
			words += " " + cp.ID
		}
		parts = append(parts, words)
	}
	taf := c.Taf.Name
	if taf == "" {
		taf = c.Taf.Agent
	}
	parts = append(parts, in.CallByTaf(taf), c.WhenWords)
	return strings.Join(parts, ", ") + "."
}

// checkHappened keeps that the person asked for help checking the call
// ("not sure", signed by the device key: it decides nothing). The ask
// stays theirs for a day, and the answer carries what the app hands
// Coach: the taf, the proposal and the call's facts.
func (h *MandatesHandler) checkHappened(w http.ResponseWriter, r *http.Request) {
	id, tenant, ok := h.identity(w, r)
	if !ok {
		return
	}
	if id.AgentID != "" || id.APIKey {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a person asks for help checking a call"})
		return
	}
	h.markChecking(w, r, id, tenant, r.PathValue("proposal"))
}

// markChecking marks proposal as being checked by id, for a day, and
// answers the hand-over to Coach. Only someone who manages the taf asks:
// the ask then leaves everyone else's list.
func (h *MandatesHandler) markChecking(w http.ResponseWriter, r *http.Request, id auth.Identity, tenant, proposal string) {
	s, ok := h.outcomes.(happenedStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "answers can't be kept here"})
		return
	}
	c, found, err := s.UnprovenCall(r.Context(), tenant, proposal)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the call couldn't be read"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no call waits for this answer"})
		return
	}
	// Who manages the taf decides who may take the ask on: with no way to read it, no one can.
	if h.ownUse == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "who manages the taf can't be read here"})
		return
	}
	scope, err := h.ownUse.Scope(r.Context(), id, tenant)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "your part of the account couldn't be read"})
		return
	}
	if !scope.Manages(c.Agent) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only someone who manages the taf asks Coach to check its call"})
		return
	}
	now := h.now()
	mark, err := s.MarkChecking(r.Context(), tenant, proposal, id.Subject, now.Add(tooloutcome.CheckingFor))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the answer was not kept"})
		return
	}
	c.Checking = &mark
	view := h.happenedViews(r, tenant, []tooloutcome.Unproven{c})[0]
	writeJSON(w, http.StatusOK, map[string]any{"proposal": proposal, "checking": mark,
		"coach": map[string]string{"taf": c.Agent, "proposal": proposal, "words": callWords(view, replyLanguage(r))}})
}
