package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// "Allow this from now on" on a held call (kiff/tafs/docs/allow-from-now-on.md):
// KIFF drafts the narrowest lasting change to the hold's Cards on its own
// model (mandate.Allow), with the held call as the draft's from_hold, and
// replays the last days' held calls against a draft. tafs cloud checks the
// change against the same breaches and writes the words; the phone reads the
// draft from KIFF and confirms it as any draft.

// ReplayDays bounds how far back a replay reads.
const (
	replayDefaultDays = 30
	replayMaxDays     = 90
)

func (h *MandatesHandler) registerAllowFromHold(mux *http.ServeMux) {
	if h.drafts == nil {
		return
	}
	mux.HandleFunc("POST /v1/me/card-exceptions/{id}/allow-draft", h.allowDraft)
	mux.HandleFunc("POST /v1/me/agent-drafts/{id}/replay", h.replayDraft)
}

// allowDraft serves POST /v1/me/card-exceptions/{id}/allow-draft, {} or
// {"round": true}: the draft of the narrowest lasting change that lets the
// held call go on its own, or {"none": "unit" | "no_change"} and no draft.
func (h *MandatesHandler) allowDraft(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.exceptionsIdentity(w, r, membership.CapCardsManage)
	if !ok {
		return
	}
	var body struct {
		Round bool `json:"round"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `the body is {} or {"round": true}`})
		return
	}
	scope, err := h.ownUse.Scope(r.Context(), id, tenantID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load exception failed"})
		return
	}
	e, err := h.store.Exception(r.Context(), tenantID, r.PathValue("id"))
	if err == nil && !scope.Hold(e.Subject, e.Request.AskedBy) {
		err = mandate.ErrExceptionNotFound
	}
	if errors.Is(err, mandate.ErrExceptionNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such exception"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load exception failed"})
		return
	}
	now := h.now()
	if e.Status != mandate.ExceptionHeld || e.Expired(now) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this call doesn't wait any more"})
		return
	}
	if _, trial := mandate.TrialAgent(e.Subject); trial {
		writeJSON(w, http.StatusOK, map[string]string{"none": mandate.AllowNoneNoChange})
		return
	}
	// No lasting change lets a sealed fill go: each is allowed once, with the
	// phone's release (sealed_fill.go).
	if sealed, err := h.sealedFillHold(r.Context(), tenantID, e); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "KIFF couldn't read the held call to check it: try again"})
		return
	} else if sealed {
		writeJSON(w, http.StatusOK, map[string]string{"none": mandate.AllowNoneNoChange})
		return
	}
	ms, err := h.store.ForSubject(r.Context(), tenantID, e.Subject)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load Cards failed"})
		return
	}
	e = withBreachUnits(h.amountUnits(r, tenantID), e)
	cards, changes, roundAfter, why := mandate.Allow(ms, e, body.Round, now)
	if why != "" {
		writeJSON(w, http.StatusOK, map[string]string{"none": why})
		return
	}
	raw := make([]json.RawMessage, len(cards))
	for i, m := range cards {
		raw[i], _ = json.Marshal(cardBodyOf(m))
	}
	d, _, status, msg := h.newDraft(r, id, tenantID, e.Subject, "", raw, false, false, e.ID)
	if status != 0 {
		if status == http.StatusInternalServerError {
			h.logger.ErrorContext(r.Context(), "allow draft not made", slog.String("exception", e.ID), slog.String("err", msg))
		}
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	v := h.draftViewFor(r, tenantID, d, nil, nil)
	out := map[string]any{"draft": d.ID, "version": d.Version, "confirm_key": v["confirm_key"], "changes": changes}
	if out["confirm_key"] == nil {
		out["confirm_key"] = "critical"
	}
	if roundAfter > 0 {
		out["round"] = map[string]int64{"after": roundAfter}
	}
	writeJSON(w, http.StatusCreated, out)
}

// replayDraft serves POST /v1/me/agent-drafts/{id}/replay, {"days": 30}:
// the calls held over the last days that the draft's Cards would have let
// go on their own. Read only.
func (h *MandatesHandler) replayDraft(w http.ResponseWriter, r *http.Request) {
	_, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	var body struct {
		Days int `json:"days"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `the body is {"days": 1 to 90}`})
		return
	}
	if body.Days == 0 {
		body.Days = replayDefaultDays
	}
	if body.Days < 1 || body.Days > replayMaxDays {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `the body is {"days": 1 to 90}`})
		return
	}
	d, err := h.drafts.Get(r.Context(), tenantID, r.PathValue("id"))
	if err != nil {
		writeDraftError(w, err)
		return
	}
	if d.Status != agentdrafts.Draft || !d.Revision {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "only a waiting draft of an agent's new Cards is replayed"})
		return
	}
	now := h.now()
	from := now.AddDate(0, 0, -body.Days)
	cards, err := h.draftOverInForce(r, tenantID, d)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read the draft's Cards failed"})
		return
	}
	all, err := h.store.Exceptions(r.Context(), tenantID, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read the held calls failed"})
		return
	}
	units := h.amountUnits(r, tenantID)
	var held []mandate.Exception
	for _, e := range all {
		if e.Subject == d.AgentID && !e.CreatedAt.Before(from) && e.KindOf() != mandate.KindCardUnit {
			held = append(held, withBreachUnits(units, e))
		}
	}
	since := mandate.ReplaySince(cards, from)
	var draws []mandate.Draw
	for _, m := range cards {
		ds, err := h.store.Draws(r.Context(), tenantID, m.ID, since)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read the Cards' draws failed"})
			return
		}
		draws = append(draws, ds...)
	}
	res, err := mandate.Replay(cards, held, draws)
	if err != nil {
		h.logger.WarnContext(r.Context(), "replay not decided", slog.String("draft", d.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the replay couldn't be decided"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"would_allow": res, "days": body.Days, "from": from.UTC(), "to": now.UTC()})
}

// draftOverInForce is the agent's Cards in force with the draft's over them,
// as confirming it would leave them: each draft Card replaces the one of its
// id, or is added, and each Card it retires is gone.
func (h *MandatesHandler) draftOverInForce(r *http.Request, tenantID string, d agentdrafts.D) ([]mandate.Mandate, error) {
	ms, err := h.store.ForSubject(r.Context(), tenantID, d.AgentID)
	if err != nil {
		return nil, err
	}
	retire := map[string]bool{}
	for _, rv := range d.Retires {
		retire[rv.ID] = true
	}
	out := slices.DeleteFunc(slices.Clone(ms), func(m mandate.Mandate) bool { return retire[m.ID] })
	var bodies []mandateBody
	if err := json.Unmarshal(d.Cards, &bodies); err != nil {
		return nil, err
	}
	for _, b := range bodies {
		c, err := b.draftMandate(tenantID)
		if err != nil {
			return nil, err
		}
		if i := slices.IndexFunc(out, func(m mandate.Mandate) bool { return m.ID == c.ID }); i >= 0 {
			out[i] = mandate.AsIssuedOver(c, &out[i])
		} else {
			out = append(out, mandate.AsIssuedOver(c, nil))
		}
	}
	return out, nil
}

// withBreachUnits is e with each amount breach's unit as the hold's own view
// gives it (amountUnits): KIFF records a breach without one.
func withBreachUnits(units *amountUnits, e mandate.Exception) mandate.Exception {
	bs := make([]map[string]any, len(e.Breaches))
	for i, b := range e.Breaches {
		bs[i] = breachView(b)
	}
	units.annotate(map[string]any{"breaches": bs}, e)
	out := slices.Clone(e.Breaches)
	for i := range out {
		if u, _ := bs[i]["unit"].(string); u != "" && out[i].Unit == "" {
			out[i].Unit = strings.ToUpper(u)
		}
	}
	e.Breaches = out
	return e
}

// cardBodyOf is a Card as a draft's Card body: its grants and totals whole,
// its holder, domain, environment, location and validity. What the body
// leaves out (its policy past its limits, hold expiry and title) a draft
// keeps from the Card in force, and extra room isn't carried: a revision's
// limit is the whole figure.
func cardBodyOf(m mandate.Mandate) mandateBody {
	b := mandateBody{ID: m.ID, Subject: m.Subject, Domain: m.Domain, Environment: m.Environment, Location: m.Location,
		Grants: []mandateGrantBody{}, Aggregates: []mandateAggregateBody{}}
	if !m.ValidFrom.IsZero() {
		from := m.ValidFrom
		b.ValidFrom = &from
	}
	if !m.ValidUntil.IsZero() {
		until := m.ValidUntil
		b.ValidUntil = &until
	}
	for _, g := range m.Grants {
		gb := mandateGrantBody{Action: g.Action, EntityStates: g.EntityStates, NextSteps: g.NextSteps}
		for _, l := range g.PerAction {
			gb.PerActionMax = append(gb.PerActionMax, mandatePerActionBody{Quantity: l.Quantity.String(), Max: l.Max})
		}
		if g.Resources != nil {
			gb.Resources = &mandateResourcesBody{Parameter: g.Resources.Parameter, Values: slices.Clone(g.Resources.Values)}
		}
		b.Grants = append(b.Grants, gb)
	}
	for _, a := range m.Aggregates {
		q := a.Quantity
		per := q.Per
		q.Per = ""
		b.Aggregates = append(b.Aggregates, mandateAggregateBody{Quantity: q.String(), Limit: a.Limit, Window: string(a.Window), Measure: string(a.Measure), Per: per})
	}
	return b
}
