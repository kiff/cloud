package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/purchase"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

// The owner's side of "the work comes home" (the mandates design amendment): the
// actions held because they fall outside a Card, and the three answers.
// Answering is a management act; an agent's own credential cannot
// approve its own exception.

func (h *MandatesHandler) registerExceptions(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/card-exceptions", h.listExceptions)
	mux.HandleFunc("GET /v1/me/card-exceptions/{id}", h.getException)
	mux.HandleFunc("POST /v1/me/card-exceptions/{id}/approve-once", h.answerException("approve-once"))
	mux.HandleFunc("POST /v1/me/card-exceptions/{id}/reject", h.answerException("reject"))
	mux.HandleFunc("POST /v1/me/card-exceptions/{id}/change-card", h.answerException("change-card"))
}

// exceptionsIdentity admits a caller to the exceptions surface. A person
// on an account needs capability c of their role (reading is every
// role's; answering and changing the Card are checked here and again by
// the hold gate); a key keeps the rule from before memberships: the
// account roles, never an agent's key.
func (h *MandatesHandler) exceptionsIdentity(w http.ResponseWriter, r *http.Request, c membership.Capability) (auth.Identity, string, bool) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return auth.Identity{}, "", false
	}
	allowed := id.ManagesAccount() || (id.ReadOnlyAccountKey() && c == membership.CapRead)
	if id.MemberRole != "" {
		allowed = membership.Can(id.MemberRole, c)
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "answering work outside a card is a person's act on the account, not an agent's, and not every role's"})
		return auth.Identity{}, "", false
	}
	return id, tenantID, true
}

// attribution reads who answered each exception, best effort.
func (h *MandatesHandler) attribution(r *http.Request, tenantID string, es []mandate.Exception) map[string]membership.HoldAnswer {
	if h.holds == nil || h.holds.Memberships == nil {
		return nil
	}
	ids := make([]string, 0, len(es))
	for _, e := range es {
		if e.ResolvedAt != nil {
			ids = append(ids, e.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	m, err := h.holds.Memberships.HoldAnswers(r.Context(), tenantID, ids)
	if err != nil {
		return nil
	}
	return m
}

// toolOutcomes reads how each answered hold's call ended at the tool,
// by the proposal id it was held under. Best effort.
func (h *MandatesHandler) toolOutcomes(r *http.Request, tenantID string, es []mandate.Exception) map[string]tooloutcome.Outcome {
	if h.outcomes == nil {
		return nil
	}
	var ids []string
	for _, e := range es {
		if e.ResolvedAt != nil && e.ProposalID != "" {
			ids = append(ids, e.ProposalID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	m, err := h.outcomes.Outcomes(r.Context(), tenantID, ids)
	if err != nil {
		h.logger.WarnContext(r.Context(), "card exceptions: tool outcomes unavailable", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return nil
	}
	for id, o := range m {
		m[id] = forReader(o)
	}
	return m
}

// heldArguments reads what each still-held gateway call will be sent
// with, by the proposal id it was held under. ok is false when
// they could not be read, so the page says so instead of showing a
// call with no arguments.
func (h *MandatesHandler) heldArguments(r *http.Request, tenantID string, es []mandate.Exception) (args map[string]json.RawMessage, ok bool) {
	var ids []string
	for _, e := range es {
		if heldGatewayCall(e) {
			ids = append(ids, e.ProposalID)
		}
	}
	if len(ids) == 0 {
		return nil, true
	}
	ar, can := h.outcomes.(tooloutcome.ArgumentsReader)
	if !can {
		return nil, false
	}
	m, err := ar.Arguments(r.Context(), tenantID, ids)
	if err != nil {
		h.logger.WarnContext(r.Context(), "card exceptions: held call arguments unavailable", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return nil, false
	}
	return m, true
}

// heldGatewayCall reports a hold on a call through the KIFF gateway,
// whose arguments the gateway keeps until it is answered. A hold from
// code has no such record.
func heldGatewayCall(e mandate.Exception) bool {
	return (e.ResolvedAt == nil || e.Status == mandate.ExceptionApproved) && e.ConsumedAt == nil && e.ProposalID != "" && e.Request.Domain == "gateway"
}

// withArguments adds what a held gateway call will be sent with, or
// arguments_unavailable when that could not be read or the gateway has
// no record of the call. Once answered, the record of what ran is the
// decision's.
func withArguments(v map[string]any, args map[string]json.RawMessage, ok bool, e mandate.Exception) map[string]any {
	if !heldGatewayCall(e) {
		return v
	}
	if a, found := args[e.ProposalID]; ok && found {
		v["arguments"] = a
	} else {
		v["arguments_unavailable"] = true
	}
	return v
}

// withOutcome adds how the hold's call ended at the tool, when known. It
// sits next to the answer: an approval used is not a call done.
func withOutcome(v map[string]any, outcomes map[string]tooloutcome.Outcome, e mandate.Exception) map[string]any {
	if o, ok := outcomes[e.ProposalID]; ok && e.ResolvedAt != nil {
		v["tool_outcome"] = o
	}
	return v
}

func withAttribution(v map[string]any, a membership.HoldAnswer, ok bool) map[string]any {
	if ok {
		// change-card is its own resolution: a Card edit that let the
		// action through, not an approval.
		if a.Answer != "" {
			v["resolution"] = a.Answer
		}
		if a.Role != "" {
			v["resolved_role"] = string(a.Role)
		}
		v["resolved_under_separation_of_duties"] = a.SeparationOfDuties
	}
	return v
}

// amountUnits says what the amounts on holds mean (unit and decimal
// places), so the person answering sees 25.50 USD, not 2550. It
// reads the tenant's Cards once and each domain's actions once per
// request. Units are resolved per domain and Card: an
// unscoped Card can cover two domains whose amounts mean different
// things. A read that fails is said, never guessed: the breach carries
// unit_unavailable, not the old euros-in-cents reading.
type amountUnits struct {
	h        *MandatesHandler
	r        *http.Request
	tenantID string
	cards    map[string]mandate.Mandate
	cardsOK  bool
	actions  map[string][]domain.ActionSpec
	domainOK map[string]bool
	perCard  map[string]cardUnitKinds
	// lang is the language the reasons are written in (kiff/tafs#75).
	lang string
}

// cardUnitKinds is what a Card's summed parameters mean in one domain:
// units holds those its actions agree on (cardUnits); bare those that
// every action declaring them leaves without a unit, a confirmed plain
// number. Any other parameter is ambiguous: its actions disagree, mix a
// unit with none, or none declares it.
type cardUnitKinds struct {
	units map[string]domain.ParameterSpec
	bare  map[string]bool
}

func unitKindsOf(actions []domain.ActionSpec, m mandate.Mandate) cardUnitKinds {
	k := cardUnitKinds{units: cardUnits(actions, m), bare: map[string]bool{}}
	declared := map[string]bool{}
	for _, a := range actions {
		if !grants(m, a.Name) {
			continue
		}
		for _, p := range a.Parameters {
			n := strings.ToLower(p.Name)
			if _, seen := declared[n]; !seen {
				declared[n] = true
			}
			if p.Unit != "" {
				declared[n] = false
			}
		}
	}
	for n, none := range declared {
		if none {
			k.bare[n] = true
		}
	}
	return k
}

func (h *MandatesHandler) amountUnits(r *http.Request, tenantID string) *amountUnits {
	u := &amountUnits{h: h, r: r, tenantID: tenantID, actions: map[string][]domain.ActionSpec{},
		domainOK: map[string]bool{}, perCard: map[string]cardUnitKinds{}, lang: replyLanguage(r)}
	if ms, err := h.store.List(r.Context(), tenantID); err == nil {
		u.cards, u.cardsOK = make(map[string]mandate.Mandate, len(ms)), true
		for _, m := range ms {
			u.cards[m.ID] = m
		}
	}
	return u
}

// domain is the exception's domain's actions, read once; false when they
// could not be read.
func (u *amountUnits) domain(e mandate.Exception) (string, []domain.ActionSpec, bool) {
	sel := e.Request.DomainID
	if sel == "" {
		sel = e.Request.Domain
	}
	if ok, read := u.domainOK[sel]; read {
		return sel, u.actions[sel], ok
	}
	as, _, err := u.h.actionsFor(u.r, u.tenantID, sel)
	u.actions[sel], u.domainOK[sel] = as, err == nil
	return sel, as, err == nil
}

// annotate adds to an exception's view:
//   - unit and scale on each breach that sums a parameter its Card
//     declares a unit for, in this exception's domain; unit_unavailable
//     when the Card or the domain could not be read; unit_ambiguous when
//     the Card's actions do not agree on one (cardUnitKinds);
//   - parameter_units, the units the held action itself declares for its
//     parameters, so an amount reads right on a hold that only a count
//     caused; units_unavailable when the domain could not be read.
//
// A parameter that is read and declares no unit gets neither: that is a
// confirmed bare number.
func (u *amountUnits) annotate(v map[string]any, e mandate.Exception) map[string]any {
	sel, as, ok := u.domain(e)
	if !ok {
		v["units_unavailable"] = true
	}
	bs, _ := v["breaches"].([]map[string]any)
	for _, b := range bs {
		if r := resourceReasonIn(b, u.lang); r != "" {
			b["reason"] = r
		}
		if r := ownerAskReasonIn(b, u.lang); r != "" {
			b["reason"] = r
		}
		id, _ := b["mandate_id"].(string)
		q, _ := b["quantity"].(string)
		if !strings.HasPrefix(q, "sum(") || !strings.HasSuffix(q, ")") {
			continue
		}
		m, found := u.cards[id]
		if !ok || !u.cardsOK || !found {
			b["unit_unavailable"] = true
			continue
		}
		key := sel + "\x00" + id
		kinds, done := u.perCard[key]
		if !done {
			kinds = unitKindsOf(as, m)
			u.perCard[key] = kinds
		}
		param := strings.ToLower(q[len("sum(") : len(q)-1])
		switch p, has := kinds.units[param]; {
		case has:
			b["unit"], b["scale"] = p.Unit, p.Scale
			// The reason in the owner's unit (kiff/tafs#102): "150.00 EUR is
			// above the 100.00 EUR limit for one call", not "sum(amount) of 15000".
			if r := reasonInUnit(b, p.Unit, p.Scale, u.lang); r != "" {
				b["reason"] = r
			}
		case !kinds.bare[param]:
			// The Card's actions disagree on what the amount means, or
			// none declares it: its total is not in one
			// unit, so it must not be read as euros.
			b["unit_ambiguous"] = true
		}
	}
	for _, a := range as {
		if !strings.EqualFold(a.Name, e.Action) {
			continue
		}
		pu := map[string]any{}
		for _, p := range a.Parameters {
			if p.Unit != "" {
				pu[p.Name] = map[string]any{"unit": p.Unit, "scale": p.Scale}
			}
		}
		if len(pu) > 0 {
			v["parameter_units"] = pu
		}
		break
	}
	return v
}

// Attribution is read separately from the projection. If a newer answer
// has replaced it, omit that enrichment instead of mixing two answers.
func matchingAttribution(e mandate.Exception, a membership.HoldAnswer, found bool) bool {
	if !found || e.ResolvedAt == nil || a.At.Before(*e.ResolvedAt) {
		return false
	}
	// New answers use the decision timestamp; later attribution cannot
	// accidentally attach a role from a different review of this hold.
	if e.AccountingVersion >= mandate.CumulativeAccountingVersion && !a.At.Equal(*e.ResolvedAt) {
		return false
	}
	switch e.Status {
	case mandate.ExceptionApproved, mandate.ExceptionConsumed:
		return a.Answer == "approve-once"
	case mandate.ExceptionRejected:
		return a.Answer == "reject"
	case mandate.ExceptionChanged:
		return a.Answer == "change-card"
	default:
		return false
	}
}

func (h *MandatesHandler) listExceptions(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.exceptionsIdentity(w, r, membership.CapRead)
	if !ok {
		return
	}
	// A person reads the holds of the agents they manage, their own runs' and
	// those they may answer; an admin all.
	scope, err := h.ownUse.Scope(r.Context(), id, tenantID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "card exceptions: reader's scope unreadable", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list exceptions failed"})
		return
	}
	status := mandate.ExceptionStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	// Expiry and approved-no-room are projected from current authority,
	// not stored owner answers. Filter them after the current room read.
	stored := status
	if status != mandate.ExceptionConsumed && status != mandate.ExceptionRejected {
		stored = ""
	}
	es, err := h.store.Exceptions(r.Context(), tenantID, stored)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list exceptions failed"})
		return
	}
	now := h.now()
	projections := make([]mandate.ExceptionProjection, 0, len(es))
	fresh := make([]mandate.Exception, 0, len(es))
	for _, e := range es {
		if !scope.Hold(e.Subject, e.Request.AskedBy) {
			continue
		}
		p, err := mandate.ReadException(r.Context(), h.store, e, now)
		if err != nil && !clientLeft(r, err) {
			h.logger.WarnContext(r.Context(), "card exception projection unavailable", slog.String("exception", e.ID), slog.String("err", err.Error()))
		}
		if status != "" && p.Status != status && p.Status != mandate.ExceptionUnavailable {
			continue
		}
		projections = append(projections, p)
		fresh = append(fresh, p.Exception)
	}
	steps := h.nextSteps(r, tenantID, fresh)
	answers := h.attribution(r, tenantID, fresh)
	outcomes := h.toolOutcomes(r, tenantID, fresh)
	args, argsOK := h.heldArguments(r, tenantID, fresh)
	units := h.amountUnits(r, tenantID)
	titles := h.toolTitles(r, tenantID)
	vendors := h.vendorTools(r, tenantID)
	w.Header().Set("Content-Language", units.lang)
	w.Header().Add("Vary", "Accept-Language")
	out := make([]map[string]any, 0, len(fresh))
	for _, p := range projections {
		e := p.Exception
		a, ok := answers[e.ID]
		ok = matchingAttribution(e, a, ok)
		v := withNextSteps(units.annotate(withArguments(withOutcome(withAttribution(projectedExceptionView(p, now), a, ok), outcomes, e), args, argsOK, e), e), steps, e)
		// The held call's tool, as the owner reads it.
		if t := titles[e.Action]; t != "" {
			v["title"] = t
		}
		if vt := vendors[e.Action]; vt != "" {
			v["vendor_tool"] = vt
		}
		v = h.withBrowserBlock(r.Context(), tenantID, v, e)
		v = h.withPurchase(r.Context(), tenantID, v, e)
		v = h.withPurchase(r.Context(), tenantID, v, e)
		out = append(out, withHeldWords(v, e, h.appBaseURL, wordsIn(replyLanguage(r))))
	}
	writeJSON(w, http.StatusOK, map[string]any{"exceptions": out})
}

func (h *MandatesHandler) getException(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.exceptionsIdentity(w, r, membership.CapRead)
	if !ok {
		return
	}
	scope, err := h.ownUse.Scope(r.Context(), id, tenantID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "card exception: reader's scope unreadable", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load exception failed"})
		return
	}
	e, err := h.store.Exception(r.Context(), tenantID, r.PathValue("id"))
	if err == nil && !scope.Hold(e.Subject, e.Request.AskedBy) {
		err = mandate.ErrExceptionNotFound // another person's: as if there were none
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
	p, err := mandate.ReadException(r.Context(), h.store, e, now)
	if err != nil && !clientLeft(r, err) {
		h.logger.WarnContext(r.Context(), "card exception projection unavailable", slog.String("exception", e.ID), slog.String("err", err.Error()))
	}
	e = p.Exception
	one := []mandate.Exception{e}
	a, found := h.attribution(r, tenantID, one)[e.ID]
	found = matchingAttribution(e, a, found)
	args, argsOK := h.heldArguments(r, tenantID, one)
	units := h.amountUnits(r, tenantID)
	w.Header().Set("Content-Language", units.lang)
	w.Header().Add("Vary", "Accept-Language")
	v := withNextSteps(units.annotate(withArguments(withOutcome(withAttribution(projectedExceptionView(p, now), a, found),
		h.toolOutcomes(r, tenantID, one), e), args, argsOK, e), e), h.nextSteps(r, tenantID, one), e)
	if t := h.toolTitles(r, tenantID)[e.Action]; t != "" {
		v["title"] = t // as the list says it
	}
	if vt := h.vendorTools(r, tenantID)[e.Action]; vt != "" {
		v["vendor_tool"] = vt
	}
	v = h.withBrowserBlock(r.Context(), tenantID, v, e)
	v = h.withPurchase(r.Context(), tenantID, v, e)
	writeJSON(w, http.StatusOK, withHeldWords(v, e, h.appBaseURL, wordsIn(replyLanguage(r))))
}

// nextSteps reads, for each held exception, the next steps its Cards offer
// as answers besides approve and reject. A Card that cannot be read
// offers none: the owner can still approve or refuse.
func (h *MandatesHandler) nextSteps(r *http.Request, tenantID string, es []mandate.Exception) map[string][]string {
	out := map[string][]string{}
	cards := map[string][]mandate.Mandate{}
	for _, e := range es {
		if e.Status != mandate.ExceptionHeld {
			continue
		}
		ms, read := cards[e.Subject]
		if !read {
			var err error
			if ms, err = h.store.ForSubject(r.Context(), tenantID, e.Subject); err != nil {
				h.logger.WarnContext(r.Context(), "next steps unavailable", slog.String("exception", e.ID), slog.String("err", err.Error()))
			}
			cards[e.Subject] = ms
		}
		if steps := mandate.NextStepsFor(e, ms); len(steps) > 0 {
			out[e.ID] = steps
		}
	}
	return out
}

func withNextSteps(v map[string]any, steps map[string][]string, e mandate.Exception) map[string]any {
	if s, ok := steps[e.ID]; ok && v["status"] == string(mandate.ExceptionHeld) {
		v["next_steps"] = s
	}
	return v
}

func (h *MandatesHandler) answerException(answer string) http.HandlerFunc {
	need := membership.CapHoldsAnswer
	if answer == "change-card" {
		need = membership.CapCardsManage
	}
	return func(w http.ResponseWriter, r *http.Request) {
		ident, tenantID, ok := h.exceptionsIdentity(w, r, need)
		if !ok {
			return
		}
		by := issuerOf(ident)
		id, now := r.PathValue("id"), h.now()
		var body answerBody
		if r.Body != nil && r.ContentLength != 0 {
			// Optional: a refusal's {"next_step", "note"}, and a
			// phone's {"signature"}. An empty body is a plain
			// answer, as before. Other answers ignore what they don't use.
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
			if answer == "reject" {
				dec.DisallowUnknownFields()
			}
			if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an answer takes an optional JSON body: {\"next_step\", \"note\", \"signature\"} to reject, {\"signature\"} otherwise"})
				return
			}
		}
		var refusal mandate.Refusal
		if answer == "reject" {
			refusal = mandate.Refusal{NextStep: body.NextStep, Note: body.Note}
		}
		device, ok := h.checkDeviceSignature(w, r, ident, tenantID, id, answer, refusal, body.Signature, now)
		if !ok {
			return
		}
		// A sealed fill is allowed only once, with the phone's release.
		refused, sealed := h.refuseSealedFill(w, r, tenantID, id, answer, body.Release)
		if refused {
			return
		}
		gov := membership.Governance{}
		if h.holds != nil {
			held, err := h.store.Exception(r.Context(), tenantID, id)
			if errors.Is(err, mandate.ErrExceptionNotFound) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such exception"})
				return
			}
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load exception failed"})
				return
			}
			// The person whose run asked for the call approves it only with
			// separation of duties off; they may always refuse it.
			hold := Hold{Proposers: []string{held.Subject}, AskedBy: held.Request.AskedBy, Refusal: answer == "reject"}
			for _, b := range held.Breaches {
				hold.MandateIDs = append(hold.MandateIDs, b.MandateID)
			}
			gov, err = h.holds.Check(r.Context(), ident, tenantID, hold, answer == "change-card")
			if err != nil {
				writeHoldGateError(w, err)
				return
			}
		}
		// A held purchase's case is decided with the answer: approving
		// reserves the Card's room first, and a room another purchase took
		// approves nothing.
		if stop := h.decidePurchase(w, r, tenantID, id, answer); stop {
			return
		}
		var e mandate.Exception
		var err error
		switch answer {
		case "approve-once":
			e, err = h.store.ApproveOnce(r.Context(), tenantID, id, by, now)
		case "reject":
			e, err = h.store.Reject(r.Context(), tenantID, id, by, now, refusal)
		case "change-card":
			e, err = h.store.ChangeCard(r.Context(), tenantID, id, by, now)
		}
		switch {
		case errors.Is(err, mandate.ErrNextStepNotOnCard):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "next_step_not_on_card",
				"detail": "the Card this call is held for does not offer that next step; choose one it lists, or refuse without one"})
		case errors.Is(err, mandate.ErrInvalidRefusal):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_note",
				"detail": fmt.Sprintf("the note must be at most %d characters, without control characters", mandate.MaxRefusalNote)})
		case errors.Is(err, mandate.ErrCumulativeApproval):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		case errors.Is(err, mandate.ErrExceptionNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such exception"})
		case errors.Is(err, mandate.ErrExceptionExpired):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "this hold expired before it was answered, so nothing was authorized; the agent has to ask again"})
		case errors.Is(err, mandate.ErrExceptionState):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "this exception was already answered, or its card changed; reload it"})
		case err != nil:
			h.logger.ErrorContext(r.Context(), "mandates: answer exception failed",
				slog.String("tenant", tenantID), slog.String("exception", id), slog.String("answer", answer), slog.String("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "answer failed"})
		default:
			answeredAt := now
			if e.ResolvedAt != nil {
				answeredAt = *e.ResolvedAt
			}
			if rerr := h.holds.RecordSigned(r.Context(), ident, tenantID, id, answer, gov, answeredAt, device); rerr != nil {
				h.logger.ErrorContext(r.Context(), "mandates: answer attribution not recorded",
					slog.String("tenant", tenantID), slog.String("exception", id), slog.String("err", rerr.Error()))
			}
			if sealed && answer == "approve-once" {
				h.keepSealedRelease(r.Context(), tenantID, id, body.Release)
			}
			if answer == "change-card" && gov.SeparationOfDuties && h.cardChangeNotice != nil {
				cards := make([]string, 0, len(e.Breaches))
				for _, b := range e.Breaches {
					cards = append(cards, b.MandateID)
				}
				changer := strings.TrimSpace(ident.Email)
				if changer == "" {
					changer = ident.Subject
				}
				h.cardChangeNotice(r.Context(), tenantID, CardChange{Changer: changer, Agent: e.Subject,
					Action: e.Action, Entity: e.EntityID, Cards: cards})
			}
			h.alertAnswered(e, now)
			a := membership.HoldAnswer{Answer: answer, Role: ident.MemberRole, SeparationOfDuties: gov.SeparationOfDuties}
			v := withAttribution(exceptionView(e, now), a, h.holds != nil)
			// The call's Record line as it now reads, the answerer's own:
			// the app shows it, and the activity reads the
			// same until KIFF records what came of the call.
			rw := wordsIn(replyLanguage(r))
			kind, detail := answeredWords(answer, rw.ByYou, rw)
			v["record"] = map[string]string{"kind": kind, "detail": detail}
			writeJSON(w, http.StatusOK, v)
		}
	}
}

// breachView is one Card a hold is outside of, as the API shows it.
func breachView(b mandate.Breach) map[string]any {
	bv := map[string]any{
		"mandate_id": b.MandateID, "outcome": string(b.Outcome), "quantity": b.Quantity.String(), "quantity_of": quantityOf(b.Quantity),
		"limit": b.Limit, "used": b.Used, "draw": b.Draw, "reason": b.Reason,
	}
	if b.Window != "" {
		bv["window"] = string(b.Window)
	}
	if b.OwnerAsk != "" {
		bv["owner_ask"] = b.OwnerAsk
	}
	if b.Outcome == mandate.OutcomeResourceOutside {
		bv["quantity"] = ""
		delete(bv, "quantity_of")
		bv["resource"], bv["value"] = b.Resource, b.Value
	}
	if b.Quantity.Per != "" {
		// A per-item total: which parameter it counts by, and, for a total
		// reached, the item the call was on ("invoice", "in_42"). A call
		// that named none reads as a resource it didn't name.
		bv["per"] = b.Quantity.Per
		if b.Outcome == mandate.OutcomeLimitReached {
			bv["resource"], bv["value"] = b.Resource, b.Value
		}
	}
	if b.Unit != "" {
		bv["quantity"] = "amount"
		bv["unit"], bv["card_unit"] = b.Unit, b.CardUnit
	}
	return bv
}

func exceptionView(e mandate.Exception, now time.Time) map[string]any {
	breaches := make([]map[string]any, 0, len(e.Breaches))
	for _, b := range e.Breaches {
		breaches = append(breaches, breachView(b))
	}
	v := map[string]any{
		"id": e.ID, "proposal_id": e.ProposalID, "subject": e.Subject, "action": e.Action,
		"entity_id": e.EntityID, "status": string(e.StatusAt(now)), "breaches": breaches,
		"created_at": e.CreatedAt, "parameters": e.Request.Parameters, "resources": e.Request.Resources,
		"accounting_version": e.AccountingVersion, "can_approve_once": e.CanApproveOnce(),
		"covering_cards": e.Covering, "prior_approvals": e.PriorApprovals,
		"external_answer": e.ExternalAnswer, "kind": e.KindOf(),
	}
	if e.KindOf() == mandate.KindCardUnit {
		v["count"], v["last_at"] = e.Count, e.LastAt
	}
	withTrialOf(v, e)
	if e.Refusal != nil {
		v["refusal"] = e.Refusal
	}
	if e.LegacyApproval != nil {
		v["legacy_approval"] = e.LegacyApproval
	}
	if e.Reason != "" {
		v["reason"] = e.Reason
	}
	if e.ResolvedAt != nil {
		v["resolved_at"], v["resolved_by"] = e.ResolvedAt, e.ResolvedBy
	}
	if e.ConsumedAt != nil {
		v["consumed_at"] = e.ConsumedAt
	}
	if e.ExpiresAt != nil {
		v["expires_at"] = e.ExpiresAt
	}
	return v
}

func projectedExceptionView(p mandate.ExceptionProjection, now time.Time) map[string]any {
	e, status := p.Exception, p.Status
	v := exceptionView(e, now)
	v["status"], v["owner_answer"] = string(status), string(p.OwnerAnswer)
	v["can_approve_once"] = status == mandate.ExceptionHeld && e.CanApproveOnce()
	if status == mandate.ExceptionApprovedNoRoom {
		v["reason"] = "approved, but the Card has no room left; change the Card or reject"
		reviewed := e
		reviewed.Breaches = p.ReviewedBreaches
		v["reviewed_breaches"] = exceptionView(reviewed, now)["breaches"]
	}
	if status == mandate.ExceptionUnavailable {
		v["reason"] = "current Card room could not be read; retry this view"
		v["room_unavailable"] = true
	}
	return v
}

// answerBody is an answer's optional JSON body: a refusal's next step and
// note, and the signature of a phone that answers.
type answerBody struct {
	NextStep  string           `json:"next_step,omitempty"`
	Note      string           `json:"note,omitempty"`
	Signature *answerSignature `json:"signature,omitempty"`
	// Release is the phone's tafs-chrome/release/v1 for a held sealed fill
	// (sealed_fill.go): required to allow one, ignored on any other hold.
	Release json.RawMessage `json:"release,omitempty"`
}

// answerSignature is a phone's signature over auth.Answer.Message: value
// is base64 DER ECDSA, signed_at the RFC 3339 time inside the message.
type answerSignature struct {
	SignedAt string `json:"signed_at"`
	Value    string `json:"value"`
	// Passkey is, from a session that approves with its device's own
	// passkey, the assertion approving an approve-once or change-card: its
	// challenge is base64url(sha256(auth.Answer.Message)), in place of Value.
	Passkey *auth.Assertion `json:"passkey,omitempty"`
}

// checkDeviceSignature enforces. A session bound to a phone's key
// answers only with that key's signature over this exact answer: the
// hold, the answer, the refusal's next step and note, and the hold's
// accounting version. A stolen session token alone approves nothing.
// change-card is an answer too: it changes the Card so this call
// fits (room for this period on a reached total; a lasting edit for a
// per-action limit or a resource outside scope), so the phone signs it
// like approve-once, and the hold's state refuses a replay as it does
// for the other answers.
// Sessions without a key (the dashboard, the CLI) and API keys answer as
// before, and may not send a signature they cannot have. It writes the
// refusal and reports false when the answer must not go ahead; on a
// phone's signed answer it returns the proof the receipt carries.
func (h *MandatesHandler) checkDeviceSignature(w http.ResponseWriter, r *http.Request, ident auth.Identity, tenantID, id, answer string, refusal mandate.Refusal, sig *answerSignature, now time.Time) (*membership.DeviceProof, bool) {
	// The proof runner's test phone answers holds on managed test tenants
	// only: anywhere else its answer, whatever it is, is refused before
	// anything else is read, and nothing changes. First, so a session with
	// only an approval key is caught too. (The middleware refuses such a
	// session on every route already; this is the second check.)
	if holdsProofTestKey(h.proofKeys, ident) && !h.proofTenants[tenantID] {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "proof_test_key",
			"detail": "this phone's key is the integration proofs' test key; it answers only on a test account"})
		return nil, false
	}
	if len(ident.DeviceKey) == 0 {
		// A device's sign-in without a device key (the CLI's, or a device with
		// no secure hardware) doesn't approve: nothing proves the
		// person or the device. The dashboard's and API keys' answers are
		// unchanged.
		if ident.Method == auth.MethodDeveloper {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "device_required",
				"detail": "approve on a device with Face ID, Touch ID or Windows Hello"})
			return nil, false
		}
		if sig != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no_device_key",
				"detail": "this session has no device key to check a signature against; sign in from the phone to answer with one"})
			return nil, false
		}
		return nil, true
	}
	// Approving, or changing the Card so the call fits, grants authority: a
	// session that approves nothing (view-only, or a key in software) refuses
	// and nothing more (the desktop-client design, §2.6).
	grants := answer == "approve-once" || answer == "change-card"
	if grants && ident.ViewOnly() {
		writeJSON(w, http.StatusForbidden, auth.ViewOnlyRefusal())
		return nil, false
	}
	if sig == nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "signature_required",
			"detail": "this session belongs to a phone with a device key, so an answer needs that key's signature"})
		return nil, false
	}
	held, err := h.store.Exception(r.Context(), tenantID, id)
	if errors.Is(err, mandate.ErrExceptionNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such exception"})
		return nil, false
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load exception failed"})
		return nil, false
	}
	signed := auth.Answer{TenantID: tenantID, ExceptionID: id, Answer: answer, NextStep: refusal.NextStep, Note: refusal.Note,
		AccountingVersion: held.AccountingVersion, SignedAt: sig.SignedAt}
	// A held sealed fill's answer signs over the block the phone drew.
	if v := h.withBrowserBlock(r.Context(), tenantID, map[string]any{}, held); v["browser_sha256"] != nil {
		signed.BrowserSHA256, _ = v["browser_sha256"].(string)
	}
	// A held purchase's answer signs its case version's purchase line, so
	// the signature binds the case the phone drew; a case that can't be
	// read answers nothing rather than verify without it.
	if h.purchaseHolds != nil {
		pr, err := h.purchaseHolds.ByException(r.Context(), tenantID, id)
		switch {
		case err == nil:
			// A purchase is approved once or refused: no Card change and no
			// next step stand in for the owner's decision on its case.
			if (answer != "approve-once" && answer != "reject") || refusal.NextStep != "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "purchase_answer",
					"detail": "a purchase is answered approve-once or reject, with no next step"})
				return nil, false
			}
			signed.Purchase = purchase.AnswerLine(pr.Case.CaseID, pr.Case.Version, pr.Digest)
		case !errors.Is(err, purchase.ErrNoCase):
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load purchase failed"})
			return nil, false
		}
	}
	// A session that approves with its device's own passkey: an answer that
	// grants authority counts only with that passkey's assertion (§2.6); a
	// refusal is its device key's, as for any device.
	if grants && ident.ApprovalMode == auth.ApprovalModePasskey {
		return h.checkPasskeyAnswer(w, r, ident, signed, sig, now)
	}
	if sig.Passkey != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "passkey_not_expected",
			"detail": "only an approve-once or change-card from a session that approves with its passkey carries one"})
		return nil, false
	}
	// For a session held to its approval key, whose own access control
	// requires Face ID or the passcode, only that key's signature counts
	// (kiff/tafs#233); a session from before approval modes with none signs
	// with its device key. A refusal takes authority away: the device key.
	key, approval := ident.DeviceKey, grants && ident.HeldToApprovalKey()
	if approval {
		key = ident.ApprovalKey
	}
	switch err := auth.VerifyAnswer(key, signed, sig.Value, now); {
	case errors.Is(err, auth.ErrAnswerSignedAt):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "signed_at_out_of_range",
			"detail": "signed_at must be RFC 3339, within 5 minutes of KIFF's clock"})
		return nil, false
	case err != nil && approval:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "approval_key_required",
			"detail": "this answer grants authority: sign it with the phone's approval key, behind Face ID"})
		return nil, false
	case err != nil:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "signature_invalid",
			"detail": "the signature does not verify for this answer with this phone's key"})
		return nil, false
	}
	// The proof names the device, or the session for a phone not yet
	// linked to one.
	keyID := ident.DeviceSessionID
	if ident.DeviceID != "" {
		keyID = ident.DeviceID
	}
	return &membership.DeviceProof{Name: ident.DeviceName, KeyID: keyID, PublicKey: key,
		Message: string(signed.Message()), Signature: sig.Value}, true
}

// checkPasskeyAnswer verifies an approve-once or change-card from a session
// that approves with its device's own passkey: signed_at within
// MaxAnswerSkew, and the assertion approving exactly this answer's message.
// The hold's state refuses a replay, as it does for a key's signature.
func (h *MandatesHandler) checkPasskeyAnswer(w http.ResponseWriter, r *http.Request, ident auth.Identity, signed auth.Answer, sig *answerSignature, now time.Time) (*membership.DeviceProof, bool) {
	if sig.Passkey == nil || h.passkeys == nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "passkey_required",
			"detail": "this answer grants authority: approve it with this computer's passkey, behind Windows Hello"})
		return nil, false
	}
	at, err := time.Parse(time.RFC3339, sig.SignedAt)
	if d := now.Sub(at); err != nil || d > auth.MaxAnswerSkew || d < -auth.MaxAnswerSkew {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "signed_at_out_of_range",
			"detail": "signed_at must be RFC 3339, within 5 minutes of KIFF's clock"})
		return nil, false
	}
	ok, err := auth.VerifyOwnPasskey(r.Context(), h.passkeys, h.rp, ident, *sig.Passkey, signed.Message())
	switch {
	case errors.Is(err, auth.ErrPasskeySynced):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "passkey_synced",
			"detail": "this computer's own passkey must stay on it: a synced passkey (BE or BS) isn't this computer"})
		return nil, false
	case errors.Is(err, auth.ErrPasskeyDevice):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "passkey_not_this_device",
			"detail": "this passkey was registered with another device"})
		return nil, false
	case err != nil:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "passkey_invalid",
			"detail": "the passkey's assertion doesn't approve this answer: its challenge is base64url(sha256) of the answer's message"})
		return nil, false
	}
	assertion, _ := json.Marshal(sig.Passkey)
	return &membership.DeviceProof{Name: ident.DeviceName, KeyID: ident.DeviceID, PublicKey: ok.Key.PublicKey,
		Message: string(signed.Message()), Signature: base64.RawURLEncoding.EncodeToString(assertion)}, true
}

// reasonInUnit says a limit breach in the amount's unit, in lang, for the
// two outcomes whose numbers are amounts: one call over its limit, and a
// total over its limit. Any other breach keeps KIFF's reason.
func reasonInUnit(b map[string]any, unit string, scale int, lang string) string {
	n := func(k string) (int64, bool) {
		v, ok := b[k].(int64)
		return v, ok
	}
	draw, okD := n("draw")
	limit, okL := n("limit")
	if !okD || !okL {
		return ""
	}
	in := func(v int64) string { return inUnit(v, unit, scale, lang) }
	switch b["outcome"] {
	case string(mandate.OutcomePerActionExceeded):
		return wordsIn(lang).OverPerCall(in(draw), in(limit))
	case string(mandate.OutcomeLimitReached):
		used, _ := n("used")
		return wordsIn(lang).OverTotal(in(draw), in(limit), in(used))
	}
	return ""
}

// inUnit writes an amount in minor units in its unit: 15000 at scale 2 in
// EUR is "150.00 EUR", and "150,00 EUR" in a language with a decimal comma.
func inUnit(minor int64, unit string, scale int, lang string) string {
	return wordsIn(lang).Number(scaledDecimal(minor, scale)) + " " + unit
}

// GatewayToolVendors reads each gateway tool's own name at its vendor, when
// a relay named it. The tool titles reader may
// implement it.
type GatewayToolVendors interface {
	VendorTools(ctx context.Context, tenantID string) (map[string]string, error)
}

// vendorTools is the account's tools' vendor names, by the name a Card
// grants them under. Best effort: none when they can't be read.
func (h *MandatesHandler) vendorTools(r *http.Request, tenantID string) map[string]string {
	vt, ok := h.titles.(GatewayToolVendors)
	if !ok || h.titles == nil {
		return nil
	}
	m, err := vt.VendorTools(r.Context(), tenantID)
	if err != nil {
		h.logger.WarnContext(r.Context(), "card exceptions: vendor tool names unreadable", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return nil
	}
	return m
}

// decidePurchase decides the purchase case a hold was opened for, with the
// owner's answer: approve-once decides it, its room reserved against the
// Card's day; reject refuses it. stop is true once it has answered.
func (h *MandatesHandler) decidePurchase(w http.ResponseWriter, r *http.Request, tenantID, id, answer string) bool {
	if h.purchaseHolds == nil {
		return false
	}
	pr, err := h.purchaseHolds.ByException(r.Context(), tenantID, id)
	if errors.Is(err, purchase.ErrNoCase) {
		return false
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load purchase failed"})
		return true
	}
	c := pr.Case
	if answer == "reject" {
		err = h.purchaseHolds.Refuse(r.Context(), tenantID, c.CaseID, c.Version, pr.Digest)
	} else {
		// The Card must still let it buy; the owner's own approval reserves
		// the room and counts, and isn't refused by the within-limits day
		// ceiling: outside those limits is exactly what waits for them.
		if _, ok := h.cardPurchaseLimits(r.Context(), tenantID, c); !ok {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "purchase_card_changed",
				"detail": "the Card this purchase was made under no longer lets it buy; nothing was approved"})
			return true
		}
		err = h.purchaseHolds.Decide(r.Context(), tenantID, c.CaseID, c.Version, pr.Digest,
			purchase.Decision{By: purchase.ByOwner, Signature: id, ApproverRole: "owner", Card: c.Card})
	}
	switch {
	case err == nil:
		return false
	case errors.Is(err, purchase.ErrRoomTaken):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "purchase_room_taken",
			"detail": "Another purchase used this room. Nothing was approved; ask again."})
	case errors.Is(err, purchase.ErrStale), errors.Is(err, purchase.ErrNotHeld):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "purchase_changed",
			"detail": "this purchase changed or was already decided; reload it"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "purchase decision failed"})
	}
	return true
}

// cardPurchaseLimits reads the purchase rule of the Card in force the case
// was made under, never the request's: its day's room bounds the owner's
// decision too.
func (h *MandatesHandler) cardPurchaseLimits(ctx context.Context, tenantID string, c purchase.Case) (purchase.Limits, bool) {
	cards, err := h.store.List(ctx, tenantID)
	if err != nil {
		return purchase.Limits{}, false
	}
	for _, m := range cards {
		if m.ID != c.Card.ID || m.RevokedAt != nil {
			continue
		}
		for _, g := range m.Grants {
			if p := g.Purchase; p != nil && p.Currency == c.Currency {
				return purchase.LimitsFrom(p.PerPurchase, p.PerDay, p.PerHour, p.PerDayCount, p.Categories).In(m.Location), true
			}
		}
	}
	return purchase.Limits{}, false
}
