package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/cardwords"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/permit"
	"github.com/kiff/cloud/api/internal/words"
)

// Agent drafts: a draft of a new agent and its Cards,
// which grants nothing until an owner confirms it. Confirming issues the
// Cards; the agent's key is minted only when the holder of the draft's
// claim secret collects it, once, so it goes straight to the runtime
// that will use it and KIFF never stores it in plain text.
//
// Anyone on the account can submit a draft, an agent's or runtime's key
// included: a draft is only a request. A person who may issue Cards and
// mint keys confirms it by submitting it (from a phone, the call is
// signed); anyone else's draft waits for such a person.

// WithMandatesAgentDrafts enables agent drafts: their store, and the key
// store a claim mints into.
func WithMandatesAgentDrafts(drafts agentdrafts.Store, keys auth.APIKeyStore) MandatesOption {
	return func(h *MandatesHandler) { h.drafts, h.draftKeys = drafts, keys }
}

// WithMandatesPermitTargets gives the handler the account's permit targets,
// whose amount argument a draft's Card must limit (kiff/tafs#138). Nil: only
// units and gateway tools' amounts are known.
func WithMandatesPermitTargets(t permit.Targets) MandatesOption {
	return func(h *MandatesHandler) { h.permitTargets = t }
}

// amountOf says which of a domain's action parameters is an amount, from
// what KIFF knows explicitly: a unit, a permit target's amount argument,
// or a gateway tool's amount argument: the gateway's contract declares, for
// each connected tool, only its amount argument as an integer (and its
// resource argument as a string), so in gatewayDomain an integer parameter
// is the amount (gateway/internal/gateway/contract.go). A target that
// can't be read counts as none: the other two still apply.
func (h *MandatesHandler) amountOf(r *http.Request, tenantID, domainName string) func(string, domain.ParameterSpec) bool {
	targets := map[string]string{}
	return func(action string, p domain.ParameterSpec) bool {
		if p.Unit != "" || (domainName == gatewayDomain && p.Type == "int") {
			return true
		}
		if h.permitTargets == nil {
			return false
		}
		arg, seen := targets[action]
		if !seen {
			if t, ok, err := h.permitTargets.Get(r.Context(), tenantID, domainName, action); err == nil && ok {
				arg = t.AmountArg
			}
			targets[action] = arg
		}
		return arg != "" && strings.EqualFold(arg, p.Name)
	}
}

func (h *MandatesHandler) registerAgentDrafts(mux *http.ServeMux) {
	if h.drafts == nil {
		return
	}
	mux.HandleFunc("POST /v1/me/agent-drafts", h.createDraft)
	mux.HandleFunc("POST /v1/me/agent-drafts/restore", h.restoreDraft)
	mux.HandleFunc("POST /v1/me/agent-drafts/preview", h.previewDraft)
	mux.HandleFunc("POST /v1/me/agent-drafts/{id}/redraft", h.redraftDraft)
	mux.HandleFunc("GET /v1/me/agent-drafts", h.waitingDrafts)
	mux.HandleFunc("GET /v1/me/agent-drafts/{id}", h.getDraft)
	mux.HandleFunc("POST /v1/me/agent-drafts/{id}/confirm", h.confirmDraftRoute)
	mux.HandleFunc("DELETE /v1/me/agent-drafts/{id}", h.discardDraft)
}

// RegisterAgentDraftClaim mounts the claim, outside the session and key
// middleware: it authenticates the draft's own claim secret and reaches
// nothing else.
func (h *MandatesHandler) RegisterAgentDraftClaim(mux *http.ServeMux) {
	if h == nil || h.drafts == nil || h.draftKeys == nil {
		return
	}
	mux.HandleFunc("POST /v1/agent-drafts/{id}/claim", h.claimDraft)
}

// maxDraftCards bounds a draft: a taf has one Card per tool.
const maxDraftCards = 20

type agentDraftBody struct {
	AgentID string            `json:"agent_id"`
	Label   string            `json:"label"`
	Cards   []json.RawMessage `json:"cards"`
	// NoTools asks for an agent that holds no Card (kiff/agents#91): the
	// setup agent, which acts through planque and drafts, never through an
	// account's tools. Confirming it mints its key and issues nothing. A
	// draft without it needs a Card, so an empty one is never a mistake.
	NoTools bool `json:"no_tools,omitempty"`
	// ContractDigest is the gateway contract the draft's Cards were worded
	// over, as its preview answered it: given, the draft is taken only while
	// that contract is in force, so what the owner read is what is checked
	// (draft_preview_contract.go).
	ContractDigest string `json:"contract_digest,omitempty"`
}

// draftPlan is what a draft would issue: its Cards, and for an agent that
// already holds Cards (a revision, kiff/tafs#107) which of them it revises.
type draftPlan struct {
	cards    []mandate.Mandate
	revision bool
	revises  []agentdrafts.Revised
	retires  []agentdrafts.Revised // by id
}

// draftCards decodes and checks a draft's Cards as issuing would: each is
// held by the draft's agent alone. A Card with a new id is a new Card. A
// Card with an id the agent already holds, alone and in force, revises it
// (kiff/tafs#107): re-issued under the same id, it keeps its usage and
// statement, and the change is a revision. Any other existing id is
// refused. A draft for an agent with no tools (noTools) has no Cards, and
// its agent must be new. With retireRest, the agent's other Cards, held
// alone and in force, are retired on confirmation: a restore of the Cards
// a version ran under brings back exactly those (kiff/tafs#107).
func (h *MandatesHandler) draftCards(r *http.Request, tenantID, agentID string, raw []json.RawMessage, noTools, retireRest bool) (draftPlan, int, string) {
	switch {
	case noTools && len(raw) > 0:
		return draftPlan{}, http.StatusBadRequest, "a draft with no_tools has no Cards"
	case !noTools && (len(raw) == 0 || len(raw) > maxDraftCards):
		return draftPlan{}, http.StatusBadRequest, "a draft has between 1 and 20 Cards, or none with no_tools"
	}
	existing, err := h.store.List(r.Context(), tenantID)
	if err != nil {
		return draftPlan{}, http.StatusInternalServerError, "list Cards failed"
	}
	taken := map[string]bool{}
	held := map[string]mandate.Mandate{}
	for _, m := range existing {
		taken[m.ID] = true
		if slices.Contains(m.HolderList(), agentID) {
			held[m.ID] = m
		}
	}
	var plan draftPlan
	plan.revision = len(held) > 0
	if noTools && plan.revision {
		return draftPlan{}, http.StatusConflict, "this agent already holds Cards; a draft with no_tools starts a new agent"
	}
	now := h.now()
	seen := map[string]bool{}
	for i, c := range raw {
		var body mandateBody
		dec := json.NewDecoder(bytes.NewReader(c))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			return draftPlan{}, http.StatusBadRequest, "card " + strconv.Itoa(i) + ": " + err.Error()
		}
		m, status, msg := h.prepareCard(r, tenantID, body)
		if status != 0 {
			return draftPlan{}, status, "card " + strconv.Itoa(i) + ": " + msg
		}
		// The owner signs each total's words: a window KIFF
		// has no words for is refused here, never shown with a guess.
		for _, a := range m.Aggregates {
			if !cardwords.KnownWindow(string(a.Window)) {
				return draftPlan{}, http.StatusBadRequest, "card " + strconv.Itoa(i) + ": a total's window must be calendar_day, rolling_1h, rolling_24h, rolling_7d or lifetime, not " + strconv.Quote(string(a.Window))
			}
		}
		if m.Subject != agentID || len(m.HolderList()) != 1 {
			return draftPlan{}, http.StatusBadRequest, "card " + strconv.Itoa(i) + ": a draft's Cards are held by its agent alone"
		}
		// A draft comes from a runtime, not a person writing the Card: a tool
		// that moves an amount is limited on that amount, never by its calls
		// alone (kiff/tafs#138).
		if m.Domain != "" {
			actions, _, err := h.actionsFor(r, tenantID, m.Domain)
			if err != nil {
				return draftPlan{}, http.StatusServiceUnavailable, "card " + strconv.Itoa(i) + ": the domain's action contracts could not be read"
			}
			if open := mandate.UncappedAmounts(m, actions, h.amountOf(r, tenantID, m.Domain)); len(open) > 0 {
				return draftPlan{}, http.StatusBadRequest, "card " + strconv.Itoa(i) + ": limit the amount of " + strings.Join(open, ", ") +
					" with per_action_max or an aggregate on sum(<amount>); a call count alone lets each call move any amount"
			}
			if msg := lifetimeCapOnWrite(m, actions); msg != "" {
				return draftPlan{}, http.StatusBadRequest, "card " + strconv.Itoa(i) + ": " + msg
			}
		}
		if seen[m.ID] {
			return draftPlan{}, http.StatusBadRequest, "card " + strconv.Itoa(i) + ": the draft names this Card twice"
		}
		seen[m.ID] = true
		if taken[m.ID] {
			cur, ours := held[m.ID]
			if !ours || len(cur.HolderList()) != 1 || !cur.Active(now) {
				return draftPlan{}, http.StatusConflict, "card " + strconv.Itoa(i) + ": a Card with this id exists, and it isn't this agent's alone and in force"
			}
			revs, err := h.store.Revisions(r.Context(), tenantID, m.ID)
			if err != nil {
				return draftPlan{}, http.StatusInternalServerError, "read the Card's revisions failed"
			}
			plan.revises = append(plan.revises, agentdrafts.Revised{ID: m.ID, Revision: latestRevision(revs)})
		}
		plan.cards = append(plan.cards, m)
	}
	if retireRest {
		for _, id := range slices.Sorted(maps.Keys(held)) {
			if m := held[id]; seen[id] || len(m.HolderList()) != 1 || !m.Active(now) {
				continue
			}
			revs, err := h.store.Revisions(r.Context(), tenantID, id)
			if err != nil {
				return draftPlan{}, http.StatusInternalServerError, "read the Card's revisions failed"
			}
			plan.retires = append(plan.retires, agentdrafts.Revised{ID: id, Revision: latestRevision(revs)})
		}
	}
	return plan, 0, ""
}

// lifetimeCapOnWrite says why a draft's Card can't count a write over its
// whole life across every item, or "". A runtime drafts "one invoice per
// order, ever" as one count, lifetime, on the tool: signed so, the taf stops
// after its first order. A limit for each item is a total per the action's
// resource argument (its one string parameter); with none, or several,
// the limit is the taf's instructions, which KIFF can't count. A read's lifetime count, a
// lifetime sum (a budget) and a count per item are all drafted as asked.
func lifetimeCapOnWrite(m mandate.Mandate, actions []domain.ActionSpec) string {
	for _, a := range m.Aggregates {
		if a.Quantity.Kind != mandate.KindCount || a.Window != mandate.WindowLifetime || a.PerItem() {
			continue
		}
		for _, g := range m.Grants {
			i := slices.IndexFunc(actions, func(s domain.ActionSpec) bool { return s.Name == g.Action })
			if i < 0 || actions[i].Risk == "low" {
				continue
			}
			// The item is the action's resource argument: in the gateway's
			// contract, its one string parameter (pins aren't parameters).
			// With none, or several, nothing says which, so none is guessed.
			var strs []string
			for _, p := range actions[i].Parameters {
				if p.Type == "string" {
					strs = append(strs, p.Name)
				}
			}
			per := ""
			if len(strs) == 1 {
				per = strs[0]
			}
			why := fmt.Sprintf("it counts %s %d in its whole life, across every item, so the taf would stop after its first", g.Action, a.Limit)
			if per == "" {
				return why + "; " + g.Action + " names no single item to count per, so a limit for each item goes in the taf's instructions, and its rules say KIFF can't count it"
			}
			return why + fmt.Sprintf("; a limit for each item is a total per %s: {\"quantity\": \"count\", \"window\": \"lifetime\", \"limit\": %d, \"per\": %q}, beside the day's total", per, a.Limit, per)
		}
	}
	return ""
}

// latestRevision is the number of a Card's latest revision, 0 with none.
func latestRevision(revs []mandate.Revision) int64 {
	if len(revs) == 0 {
		return 0
	}
	return revs[len(revs)-1].Number
}

// mayConfirm is who can confirm a draft: a person or account key that may
// issue Cards and mint keys.
func mayConfirm(id auth.Identity) bool {
	return can(id, membership.CapCardsManage) && can(id, membership.CapKeysManage)
}

func (h *MandatesHandler) createDraft(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	var body agentDraftBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body must be {agent_id, label, cards}: " + err.Error()})
		return
	}
	agentID, err := agents.NormalizeID(body.AgentID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_id: " + err.Error()})
		return
	}
	label := strings.TrimSpace(body.Label)
	if len([]rune(label)) > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "label is at most 100 characters"})
		return
	}
	if body.ContractDigest != "" && body.ContractDigest != h.contractDigest(r, tenantID) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "contract_changed",
			"message": "the account's tools changed since these Cards were worded; read them again"})
		return
	}
	// The person who may confirm it has, by submitting it.
	h.submitDraft(w, r, id, tenantID, agentID, label, body.Cards, body.NoTools, mayConfirm(id), false)
}

// previewDraft serves POST /v1/me/agent-drafts/preview:
// the body a submit would take, checked as a submit checks it, answered as
// the submitted draft would read (its card_title, and each Card with its
// units, titles and lines), and stored nowhere. tafs cloud shows the phone
// KIFF's own words for the draft the phone then signs and submits, so no
// app or service keeps a copy of how a Card reads. It confirms nothing and
// mints no claim secret.
//
// With contract (the gateway domain's YAML) and titles, the draft is read
// against that contract and those tool titles in place of the stored ones:
// the gateway's batch preview sends the contract a batch would apply, so a
// first start words Cards over tools the same signature connects
// (draft_preview_contract.go).
func (h *MandatesHandler) previewDraft(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	var in struct {
		agentDraftBody
		Contract string            `json:"contract,omitempty"`
		Titles   map[string]string `json:"titles,omitempty"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body must be {agent_id, label, cards}, with contract and titles for a batch's preview: " + err.Error()})
		return
	}
	body := in.agentDraftBody
	if in.Contract != "" {
		o, status, msg := overlayFor(id, in.Contract, in.Titles)
		if status != 0 {
			writeJSON(w, status, map[string]string{"error": msg})
			return
		}
		r = withOverlay(r, o)
	} else if len(in.Titles) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "titles go with contract"})
		return
	}
	agentID, err := agents.NormalizeID(body.AgentID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_id: " + err.Error()})
		return
	}
	label := strings.TrimSpace(body.Label)
	if len([]rune(label)) > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "label is at most 100 characters"})
		return
	}
	plan, status, msg := h.draftCards(r, tenantID, agentID, body.Cards, body.NoTools, false)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	raw := body.Cards
	if raw == nil {
		raw = []json.RawMessage{}
	}
	cards, _ := json.Marshal(raw)
	d := agentdrafts.D{TenantID: tenantID, AgentID: agentID, Label: label, Cards: cards,
		Revision: plan.revision, Revises: plan.revises, Retires: plan.retires}
	full := h.draftViewFor(r, tenantID, d, nil, h.toolTitles(r, tenantID))
	v := map[string]any{"agent_id": agentID, "label": label, "card_title": full["card_title"], "cards": full["cards"]}
	if d := h.contractDigest(r, tenantID); d != "" {
		v["contract_digest"] = d
	}
	for _, k := range []string{"no_tools", "revision", "retires"} {
		if x, ok := full[k]; ok {
			v[k] = x
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// submitDraft checks raw as issuing would, stores the draft, and confirms
// it when confirm is set. A draft that only waits (restore, redraft) is
// never confirmed by submitting it, whoever submits it: the owner confirms
// it where they see its terms, with the phone's signature. retireRest
// retires the agent's Cards raw doesn't name (draftCards).
func (h *MandatesHandler) submitDraft(w http.ResponseWriter, r *http.Request, id auth.Identity, tenantID, agentID, label string, raw []json.RawMessage, noTools, confirm, retireRest bool) {
	d, secret, status, msg := h.newDraft(r, id, tenantID, agentID, label, raw, noTools, retireRest, "")
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	if confirm {
		var status int
		var msg string
		if d, status, msg = h.confirmDraft(r, id, d.ID, d.Version); status != 0 {
			writeJSON(w, status, draftError(msg))
			return
		}
	}
	v := h.draftViewFor(r, tenantID, d, nil, h.toolTitles(r, tenantID))
	if !d.Revision {
		v["claim_secret"] = secret // shown once, to whoever submitted it
	}
	writeJSON(w, http.StatusCreated, v)
}

// newDraft checks and saves a draft of raw's Cards for agentID, and
// returns it with its claim secret, or why it can't be made. It confirms
// nothing. fromHold is the held call it's made for, or "".
func (h *MandatesHandler) newDraft(r *http.Request, id auth.Identity, tenantID, agentID, label string, raw []json.RawMessage, noTools, retireRest bool, fromHold string) (agentdrafts.D, string, int, string) {
	plan, status, msg := h.draftCards(r, tenantID, agentID, raw, noTools, retireRest)
	if status != 0 {
		return agentdrafts.D{}, "", status, msg
	}
	if raw == nil {
		raw = []json.RawMessage{}
	}
	cards, _ := json.Marshal(raw)
	now := h.now()
	secret, hash := agentdrafts.NewClaimSecret()
	retiring := make([]string, len(plan.retires))
	for i, rv := range plan.retires {
		retiring[i] = rv.ID
	}
	d := agentdrafts.D{ID: "drf_" + ulid.MustNew(ulid.Timestamp(now), ulid.DefaultEntropy()).String(), TenantID: tenantID,
		AgentID: agentID, Label: label, Cards: cards, Version: agentdrafts.VersionOf(agentID, label, cards, retiring...), Status: agentdrafts.Draft,
		CreatedBy: issuerOf(id), CreatedAt: now, ExpiresAt: now.Add(agentdrafts.DefaultTTL), ClaimHash: hash,
		Revision: plan.revision, Revises: plan.revises, Retires: plan.retires, RetireRest: retireRest, FromHold: fromHold}
	if err := h.drafts.Create(r.Context(), d); err != nil {
		return agentdrafts.D{}, "", http.StatusInternalServerError, "save draft failed"
	}
	return d, secret, 0, ""
}

func draftView(d agentdrafts.D, w *words.Words) map[string]any {
	var cards []json.RawMessage
	_ = json.Unmarshal(d.Cards, &cards)
	v := map[string]any{"id": d.ID, "agent_id": d.AgentID, "label": d.Label, "status": d.Status, "version": d.Version,
		"cards": cards, "created_by": d.CreatedBy, "created_at": d.CreatedAt, "expires_at": d.ExpiresAt, "review_url": "/agent-drafts/" + d.ID,
		"card_title": cardTitle(d.Label, w)}
	if len(cards) == 0 {
		v["no_tools"] = true // an agent that holds no Card (kiff/agents#91)
	}
	if d.Revision {
		v["revision"] = true // it revises an agent's Cards; no key comes out of it (kiff/tafs#107)
	}
	if d.FromHold != "" {
		v["from_hold"] = d.FromHold // made for a held call ("Allow this from now on")
	}
	if len(d.Retires) > 0 {
		retires := make([]map[string]any, len(d.Retires))
		for i, rv := range d.Retires {
			retires[i] = map[string]any{"id": rv.ID, "revision": rv.Revision}
		}
		v["retires"] = retires // revoked on confirmation (kiff/tafs#107)
	}
	if d.ConfirmedAt != nil {
		v["confirmed_by"], v["confirmed_at"], v["card_ids"] = d.ConfirmedBy, *d.ConfirmedAt, d.CardIDs
	}
	if d.ClaimedAt != nil {
		v["claimed_at"], v["key_id"] = *d.ClaimedAt, d.KeyID
	}
	if d.Reason != "" {
		v["reason"] = d.Reason
	}
	return v
}

// draftViewFor is draftView with each Card's amounts in their units:
// the unit and scale a Card's own view gives, from its domain, so
// a phone approving the draft can say €100.00, not 10000. Best effort, as
// for a Card: one whose domain can't be read keeps bare numbers.
// actionsByDomain caches each domain's actions across drafts (nil: one
// draft, its own cache), so a list reads each domain once. titles are the
// account's tool titles (toolTitles), which each grant gets as a Card's do.
func (h *MandatesHandler) draftViewFor(r *http.Request, tenantID string, d agentdrafts.D, actionsByDomain map[string][]domain.ActionSpec, titles map[string]string) map[string]any {
	v := draftView(d, wordsIn(replyLanguage(r)))
	if t := trialSummary(d, h.now()); t != nil {
		v["trial"] = t // its latest trial (kiff/tafs#173)
	}
	cards, _ := v["cards"].([]json.RawMessage)
	if d.Status == agentdrafts.Draft || d.Status == agentdrafts.Trying {
		// Which key confirming it takes (kiff/tafs#494): a tap inside an
		// approval window when it widens no Card in force, else the
		// fingerprint, with each way it widens in words.
		v["confirm_key"] = "window"
		var drafted []mandate.Mandate
		for _, raw := range cards {
			var body mandateBody
			if json.Unmarshal(raw, &body) != nil {
				continue
			}
			if m, err := body.draftMandate(tenantID); err == nil {
				drafted = append(drafted, m)
			}
		}
		ws, err := h.widensInForce(r.Context(), tenantID, drafted)
		switch {
		case err != nil || len(drafted) != len(cards):
			v["confirm_key"] = "critical" // unread: never offered as a tap
		case len(ws) > 0:
			v["confirm_key"], v["widens"] = "critical", ws
		}
	}
	if len(cards) == 0 {
		return v
	}
	if actionsByDomain == nil {
		actionsByDomain = map[string][]domain.ActionSpec{}
	}
	// A revision draft's Cards say which Card each revises and how it
	// reads now, so the owner sees now → after before confirming, and
	// each Card it retires reads as it is now (kiff/tafs#107).
	current := map[string]mandate.Mandate{}
	if d.Revision && (len(d.Revises) > 0 || len(d.Retires) > 0) {
		if ms, err := h.store.List(r.Context(), tenantID); err == nil {
			for _, m := range ms {
				if d.RevisesCard(m.ID) || d.RetiresCard(m.ID) {
					current[m.ID] = m
				}
			}
		}
	}
	if retires, ok := v["retires"].([]map[string]any); ok {
		for _, rt := range retires {
			cur, ok := current[rt["id"].(string)]
			if !ok {
				continue
			}
			var now map[string]any
			b, _ := json.Marshal(mandateView(cur, h.now()))
			if json.Unmarshal(b, &now) == nil {
				var body mandateBody
				_ = json.Unmarshal(b, &body)
				h.shapeCard(r, tenantID, body, now, actionsByDomain, titles)
				withOwnLines(now, wordsIn(replyLanguage(r)))
				rt["current"] = now
			}
		}
	}
	out := make([]any, 0, len(cards))
	for _, raw := range cards {
		var body mandateBody
		var shown map[string]any
		if json.Unmarshal(raw, &body) != nil || json.Unmarshal(raw, &shown) != nil {
			out = append(out, raw)
			continue
		}
		h.shapeCard(r, tenantID, body, shown, actionsByDomain, titles)
		// What happens past a limit, as the Card would be issued: the draft's
		// own policy, else, on a revision, the Card in force's (omitting it
		// keeps it), else the owner's, as for any new Card.
		policy := strings.TrimSpace(body.OnExceed)
		shown["on_exceed"] = mandate.OnExceedOwner
		if policy != "" {
			shown["on_exceed"] = policyView(policy)
		}
		if cur, ok := current[body.ID]; ok {
			if policy == "" {
				shown["on_exceed"] = policyView(cur.OnExceed)
			}
			var now map[string]any
			b, _ := json.Marshal(mandateView(cur, h.now()))
			if json.Unmarshal(b, &now) == nil {
				h.shapeCard(r, tenantID, body, now, actionsByDomain, titles)
				shown["revises"], shown["current"] = cur.ID, now
			}
			// Re-issued exactly as it is (kiff/tafs#138): the app can say so instead of
			// a now and after. Only when every term is the same.
			if m, err := body.draftMandate(tenantID); err == nil && sameTerms(mandate.AsIssuedOver(m, &cur), cur) {
				shown["unchanged"] = true
			}
		}
		out = append(out, shown)
	}
	// The lines the owner reads before confirming, KIFF's own.
	var shownCards []map[string]any
	for _, c := range out {
		if m, ok := c.(map[string]any); ok {
			shownCards = append(shownCards, m)
		}
	}
	withLines(shownCards, wordsIn(replyLanguage(r)))
	v["cards"] = out
	return v
}

// sameTerms reports whether m, a draft's Card as it would be issued
// (mandate.AsIssuedOver), has exactly the terms of cur, the Card in force
// (kiff/tafs#138): its holders, domain and environment, every grant (caps,
// resources, states, next steps), every total (quantity and the item it
// counts each of apart, limit, window, measure), what happens past them, how long a hold waits, when it starts
// and ends, and the time zone its daily totals reset in. A term it doesn't
// compare, or one written differently, reads as a change: the owner then
// sees now and after, never "unchanged" over a change. Subject and DomainID
// aren't compared on their own: the holders start with the subject, and the
// domain is compared by name, which a revision can't move to another.
func sameTerms(m, cur mandate.Mandate) bool {
	type total struct {
		Quantity, Window, Measure string
		Limit                     int64
	}
	totals := func(as []mandate.Aggregate) []total {
		out := make([]total, len(as))
		for i, a := range as {
			// Key, not String: one per order, ever, and one in all aren't the same total.
			out[i] = total{a.Quantity.Key(), string(a.Window), string(a.EffectiveMeasure()), a.Limit}
		}
		return out
	}
	grants := func(gs []mandate.Grant) string {
		v := grantsView(gs)
		for _, g := range v {
			if s, _ := g["entity_states"].([]string); len(s) == 0 {
				delete(g, "entity_states")
			}
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	return slices.Equal(m.HolderList(), cur.HolderList()) &&
		strings.EqualFold(m.Domain, cur.Domain) && strings.EqualFold(m.Environment, cur.Environment) &&
		policyView(m.OnExceed) == policyView(cur.OnExceed) &&
		hold0(m.HoldExpiry) == hold0(cur.HoldExpiry) &&
		m.ValidUntil.Equal(cur.ValidUntil) && m.ValidFrom.Equal(cur.ValidFrom) &&
		m.Location == cur.Location && m.RevokedAt == nil && cur.RevokedAt == nil &&
		slices.Equal(totals(m.Aggregates), totals(cur.Aggregates)) &&
		grants(m.Grants) == grants(cur.Grants)
}

// hold0 is a Card's hold expiry, its default when unset.
func hold0(d time.Duration) time.Duration {
	if d == 0 {
		return mandate.DefaultHoldExpiry
	}
	return d
}

// shapeCard puts each amount's unit and each tool's title on a Card as
// shown, read from the domain the Card body names.
func (h *MandatesHandler) shapeCard(r *http.Request, tenantID string, body mandateBody, shown map[string]any, actionsByDomain map[string][]domain.ActionSpec, titles map[string]string) {
	m, err := body.toMandate(tenantID)
	sel := ""
	if err == nil {
		sel = cardDomain(m)
	}
	withQuantityOf(jsonObjects(shown["aggregates"]))
	for _, g := range jsonObjects(shown["grants"]) {
		withQuantityOf(jsonObjects(g["per_action_max"]))
	}
	if sel == "" {
		withTitles(jsonObjects(shown["grants"]), titles)
		return
	}
	actions, seen := actionsByDomain[sel]
	if !seen {
		actions, _, _ = h.actionsFor(r, tenantID, sel)
		actionsByDomain[sel] = actions
	}
	units := cardUnits(actions, m)
	withUnits(jsonObjects(shown["aggregates"]), units, wordsIn(replyLanguage(r)))
	for _, g := range jsonObjects(shown["grants"]) {
		withUnits(jsonObjects(g["per_action_max"]), units, wordsIn(replyLanguage(r)))
	}
	withTitles(jsonObjects(shown["grants"]), titles)
}

// jsonObjects is a decoded JSON array's objects, which share their maps
// with the array; anything else in it is left out.
func jsonObjects(v any) []map[string]any {
	arr, _ := v.([]any)
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		if o, ok := e.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}

func (h *MandatesHandler) getDraft(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	d, err := h.drafts.Get(r.Context(), tenantID, r.PathValue("id"))
	// An agent's key reads only its own agent's drafts: tafs cloud
	// finishes a version switch from the draft's status without the
	// owner's session (kiff/tafs#107).
	if err == nil && id.APIKey && id.AgentID != "" && d.AgentID != id.AgentID {
		err = agentdrafts.ErrNotFound
	}
	if err != nil {
		writeDraftError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.draftViewFor(r, tenantID, d, nil, h.toolTitles(r, tenantID)))
}

// waitingDrafts serves GET /v1/me/agent-drafts: the account's drafts
// waiting for confirmation, newest first. Only someone who may
// confirm them sees the list, so an editor isn't shown drafts they can't
// act on.
func (h *MandatesHandler) waitingDrafts(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if !mayConfirm(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "listing drafts waiting for confirmation needs the rights to confirm them: issue Cards and give keys"})
		return
	}
	ds, err := h.drafts.Waiting(r.Context(), tenantID, h.now())
	if err != nil {
		h.logger.ErrorContext(r.Context(), "agent drafts: list waiting failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list drafts failed"})
		return
	}
	out := make([]map[string]any, 0, len(ds))
	actionsByDomain := map[string][]domain.ActionSpec{} // one read per domain for the whole list
	titles := h.toolTitles(r, tenantID)
	for _, d := range ds {
		out = append(out, h.draftViewFor(r, tenantID, d, actionsByDomain, titles))
	}
	writeJSON(w, http.StatusOK, map[string]any{"drafts": out})
}

// asTrial is the confirm body's "as" that starts a trial (kiff/tafs#173).
const asTrial = "trial"

func (h *MandatesHandler) confirmDraftRoute(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	var body struct {
		Version string `json:"version"`
		// As "trial" confirms it as a trial (kiff/tafs#173): its Cards are
		// issued as trial Cards, and the live ones stay as they are.
		As string `json:"as"`
		// Hold is the held install this confirm also answers: one
		// fingerprint for the version's Card and the call that installs it.
		Hold *draftHold `json:"hold"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err == nil {
		err = json.Unmarshal(raw, &body)
	}
	if err != nil || body.Version == "" || (body.As != "" && body.As != asTrial) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body must be {\"version\"}, the version you were shown, and optionally \"as\": \"trial\" or \"hold\": {\"id\", \"accounting_version\"}"})
		return
	}
	if body.Hold != nil && (body.As == asTrial || body.Hold.ID == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a hold is answered only with a plain confirm, by its id and accounting_version: a trial installs nothing"})
		return
	}
	// Inside an approval window (kiff/tafs#494), a draft is confirmed with a
	// tap only if it widens no Card in force (the widening rule).
	if windowSigned(r) {
		ws, status, msg := h.draftWidens(r, tenantID, r.PathValue("id"))
		if status != 0 {
			writeJSON(w, status, draftError(msg))
			return
		}
		if len(ws) > 0 {
			criticalKeyRequired(w, ws)
			return
		}
	}
	confirm := h.confirmDraft
	if body.As == asTrial {
		confirm = h.startTrial
	}
	d, status, msg := confirm(r, id, r.PathValue("id"), body.Version)
	if status != 0 {
		writeJSON(w, status, draftError(msg))
		return
	}
	v := h.draftViewFor(r, tenantID, d, nil, h.toolTitles(r, tenantID))
	// The draft is confirmed whatever comes of the hold: the app answers a
	// hold this didn't take the usual way.
	if body.Hold != nil {
		v["hold"] = h.answerDraftHold(r, id, *body.Hold, raw)
	}
	writeJSON(w, http.StatusOK, v)
}

// confirmDraft activates exactly the version shown: once, however many
// confirmations race (the store's compare-and-set), issuing its Cards.
// The draft is confirming, which nothing can claim or discard, until every
// Card is issued; and the confirmation runs to its end even if the caller
// goes away. A Card that fails to issue rolls the draft back: its Cards
// are revoked before it is failed, and if that can't finish now the draft
// stays unclaimable until a retry or the sweep does. No key exists until
// the claim, so nothing can act on the Cards before then.
func (h *MandatesHandler) confirmDraft(r *http.Request, id auth.Identity, draftID, version string) (agentdrafts.D, int, string) {
	if !mayConfirm(id) {
		return agentdrafts.D{}, http.StatusForbidden, "confirming a draft needs a person or key that may issue Cards and mint keys"
	}
	tenantID := id.Tenant
	cur, err := h.drafts.Get(r.Context(), tenantID, draftID)
	// A version being tried is installed by confirming it (kiff/tafs#173).
	if err == nil && cur.Status != agentdrafts.Draft && cur.Status != agentdrafts.Trying {
		err = agentdrafts.ErrState
	}
	if err != nil {
		return agentdrafts.D{}, draftStatus(err), draftMessage(err)
	}
	var raw []json.RawMessage
	_ = json.Unmarshal(cur.Cards, &raw)
	// A draft is stored with no Cards only when it asked for no tools.
	plan, status, msg := h.draftCards(r, tenantID, cur.AgentID, raw, len(raw) == 0, cur.RetireRest)
	if status != 0 {
		return agentdrafts.D{}, status, "the draft could not be confirmed: " + msg
	}
	// The Cards it revises and retires must be as the owner was shown them.
	if plan.revision != cur.Revision || !slices.Equal(plan.revises, cur.Revises) || !slices.Equal(plan.retires, cur.Retires) {
		return agentdrafts.D{}, http.StatusConflict, cardRevisedMsg
	}
	cards := plan.cards
	ids := make([]string, len(cards))
	for i, m := range cards {
		ids[i] = m.ID
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), agentdrafts.ConfirmTimeout)
	defer cancel()
	d, err := h.drafts.Confirm(ctx, tenantID, draftID, version, id.Subject, string(id.MemberRole), ids, h.now())
	if err != nil {
		return agentdrafts.D{}, draftStatus(err), draftMessage(err)
	}
	// All of its Cards, or none (kiff/tafs#107), and only over the revisions
	// the owner was shown, checked under the Cards' locks.
	expect := make(map[string]int64, len(cur.Revises)+len(cur.Retires))
	for _, rv := range cur.Revises {
		expect[rv.ID] = rv.Revision
	}
	retire := make([]string, len(cur.Retires))
	for i, rv := range cur.Retires {
		expect[rv.ID], retire[i] = rv.Revision, rv.ID
	}
	// Installing a version being tried ends its trial: its trial Cards are
	// revoked with the live ones issued (kiff/tafs#173).
	var trialCards []string
	if d.Trial != nil && d.Trial.EndedBy == agentdrafts.EndedInstall {
		trialCards = d.Trial.CardIDs
		if len(cards) > 0 {
			retire = append(retire, trialCards...)
		}
	}
	// Confirmed, its Cards answer the currency asks they cover, in the same
	// transaction; a draft not yet confirmed closes nothing.
	issue := h.store.IssueAll
	if covers := h.unitCovers(r, tenantID, cards); len(covers) > 0 {
		issue = func(ctx context.Context, ms []mandate.Mandate, by string, expect map[string]int64, retire []string, at time.Time) error {
			return h.store.IssueAllCovering(ctx, ms, by, expect, retire, at, covers)
		}
	}
	for i := range cards {
		cards[i].FromHold = cur.FromHold // the revision says where it came from
		cards[i].DraftID = cur.ID        // and the Card which draft issued it
	}
	if err := issue(ctx, cards, issuerOf(id), expect, retire, h.now()); err != nil {
		if errors.Is(err, mandate.ErrCardRevised) {
			if _, status, msg := h.rollBackDraft(r, d, "a Card it revises changed since it was made"); status != http.StatusInternalServerError || !strings.Contains(msg, "nothing it would grant") {
				return agentdrafts.D{}, status, msg
			}
			return agentdrafts.D{}, http.StatusConflict, cardRevisedMsg
		}
		h.logger.ErrorContext(ctx, "agent draft: issue failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return h.rollBackDraft(r, d, "its Cards could not be issued")
	}
	if len(cards) == 0 && len(trialCards) > 0 {
		// Nothing issued to retire them with.
		if err := h.revokeTrialCards(ctx, d); err != nil {
			h.logger.ErrorContext(ctx, "agent draft: revoke trial Cards failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
			return h.rollBackDraft(r, d, "its trial's Cards could not be revoked")
		}
	}
	// The agent is read by the name its draft was submitted with.
	h.ensureNamedHolder(r.WithContext(ctx), tenantID, d.AgentID, d.Label, issuerOf(id))
	done, err := h.drafts.Finish(ctx, tenantID, d.ID)
	if err != nil {
		h.logger.ErrorContext(ctx, "agent draft: finish failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return h.rollBackDraft(r, d, "the confirmation could not be recorded")
	}
	return done, 0, ""
}

// rollBackDraft fails a confirmation partway: the draft can't be claimed
// from here on, and its Cards are revoked before it is marked failed. It
// says nothing was left in force only when that is so.
func (h *MandatesHandler) rollBackDraft(r *http.Request, d agentdrafts.D, reason string) (agentdrafts.D, int, string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), agentdrafts.ConfirmTimeout)
	defer cancel()
	failed, err := h.drafts.Fail(ctx, d.TenantID, d.ID, reason)
	if err != nil {
		h.logger.ErrorContext(ctx, "agent draft: fail failed", slog.String("tenant", d.TenantID), slog.String("draft", d.ID), slog.String("err", err.Error()))
		return agentdrafts.D{}, http.StatusInternalServerError, "the draft could not be confirmed (" + reason + "), and how it stands could not be recorded; check the draft before claiming it"
	}
	if err := agentdrafts.Undo(ctx, h.drafts, h.store, failed, h.now()); err != nil {
		h.logger.ErrorContext(ctx, "agent draft: roll back failed", slog.String("tenant", d.TenantID), slog.String("draft", d.ID), slog.String("err", err.Error()))
		return agentdrafts.D{}, http.StatusInternalServerError, "the draft could not be confirmed (" + reason + "). It can't be claimed, and the Cards it issued are still being revoked"
	}
	return agentdrafts.D{}, http.StatusInternalServerError, "the draft could not be confirmed, and nothing it would grant was left in force: " + reason
}

func (h *MandatesHandler) discardDraft(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if !isMandateManagement(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "discarding a draft is a management action"})
		return
	}
	was, err := h.drafts.Discard(r.Context(), tenantID, r.PathValue("id"))
	if err != nil {
		writeDraftError(w, err)
		return
	}
	if was.Status == agentdrafts.Draft {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Confirmed but never claimed: its Cards go with it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), agentdrafts.ConfirmTimeout)
	defer cancel()
	if err := agentdrafts.Undo(ctx, h.drafts, h.store, was, h.now()); err != nil {
		h.logger.ErrorContext(ctx, "agent draft: discard failed", slog.String("tenant", tenantID), slog.String("draft", was.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the draft is discarded and can't be claimed, but some of its Cards are not revoked yet; try again"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SweepAgentDrafts finishes what interrupted confirmations and discards
// left (agentdrafts.Sweep), every interval until ctx ends.
func SweepAgentDrafts(ctx context.Context, drafts agentdrafts.Store, cards agentdrafts.Cards, every time.Duration, logger *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := agentdrafts.Sweep(ctx, drafts, cards, time.Now()); err != nil {
			logger.Warn("agent drafts: sweep failed", slog.Int("settled", n), slog.String("err", err.Error()))
		} else if n > 0 {
			logger.Info("agent drafts: settled interrupted drafts", slog.Int("settled", n))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// claimDraft mints the agent's key for whoever holds the claim secret,
// once, after confirmation and before the draft expires. The person who
// confirmed it must still be allowed to mint keys.
func (h *MandatesHandler) claimDraft(w http.ResponseWriter, r *http.Request) {
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !strings.HasPrefix(strings.TrimSpace(secret), "kiff_claim_") {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "the draft's claim secret is required"})
		return
	}
	now := h.now()
	d, err := h.drafts.Claim(r.Context(), r.PathValue("id"), agentdrafts.HashClaim(strings.TrimSpace(secret)), now)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	undo := func(status int, msg string) {
		_ = h.drafts.Unclaim(r.Context(), d.TenantID, d.ID)
		writeJSON(w, status, map[string]string{"error": msg})
	}
	// A person who confirmed must still hold a role that gives keys. A key
	// that confirmed is checked by its own revocation: a revoked key could
	// not have confirmed, and the minted key is bound to the agent alone.
	if h.holds != nil && h.holds.Memberships != nil && d.ConfirmedRole != "" {
		if m, err := h.holds.Memberships.Live(r.Context(), d.TenantID, d.ConfirmedBy); err == nil && !membership.Can(m.Role, membership.CapKeysManage) {
			undo(http.StatusForbidden, "the person who confirmed this draft can no longer give keys; ask an admin to confirm it again")
			return
		} else if err != nil && !errors.Is(err, membership.ErrNotFound) && !errors.Is(err, membership.ErrNotMember) {
			undo(http.StatusServiceUnavailable, "who confirmed this draft could not be checked; try again")
			return
		} else if err != nil {
			undo(http.StatusForbidden, "the person who confirmed this draft is no longer on the account")
			return
		}
	}
	gen, err := auth.NewAPIKey(d.TenantID, now)
	if err != nil {
		undo(http.StatusInternalServerError, "mint key failed")
		return
	}
	// A taf's key calls its tools through the gateway. The account's Setup
	// (Coach) gets an agent-bound key without that role: it reaches the
	// routes any runtime key does, such as submitting a draft for the owner
	// to confirm. It carries setup_reader instead, so it can read a
	// read-only tool on a Card of its own (kiff/tafs#138), and act on the
	// account's own control server as Coach (kiff/tafs#206).
	roles := []string{auth.RoleGatewayAgent}
	if setup, err := h.setupDraft(r.Context(), d); err != nil {
		undo(http.StatusServiceUnavailable, "whose agent this draft is for could not be checked; try again")
		return
	} else if setup {
		roles = []string{auth.RoleSetupReader}
	}
	rec := auth.APIKeyRecord{ID: ulid.MustNew(ulid.Timestamp(now), ulid.DefaultEntropy()).String(), Tenant: d.TenantID,
		Subject: d.ConfirmedBy, Label: strings.TrimSpace("draft " + d.Label), Roles: roles,
		AgentID: d.AgentID, KeyHash: gen.Hash, CreatedAt: now}
	if err := h.draftKeys.Insert(r.Context(), rec); err != nil {
		undo(http.StatusInternalServerError, "mint key failed")
		return
	}
	_ = h.drafts.SetKey(r.Context(), d.TenantID, d.ID, rec.ID)
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": d.AgentID, "key_id": rec.ID, "plaintext": gen.Plaintext,
		"card_ids": d.CardIDs, "roles": rec.Roles})
}

// setupDraft reports whether d is a draft for the account's Setup (Coach):
// one with no Cards (no_tools, kiff/agents#91), one for the agent id every
// app gives Setup (auth.SetupAgentID), or one for an agent a live
// setup_reader key is already bound to. A draft of Setup's own Cards is
// still Setup's: a first start drafts them at once (kiff/tafs#852), and a
// gateway key would keep Setup to the gateway's routes, refused its own
// drafts. What its key reaches is bounded by the role, never by the Cards:
// the gateway's contract grants setup_reader the read-only tools and the
// control server's, never a vendor's tool that writes.
func (h *MandatesHandler) setupDraft(ctx context.Context, d agentdrafts.D) (bool, error) {
	var drafted []json.RawMessage
	if json.Unmarshal(d.Cards, &drafted) == nil && len(drafted) == 0 {
		return true, nil
	}
	if d.AgentID == auth.SetupAgentID {
		return true, nil
	}
	keys, err := h.draftKeys.ListActive(ctx, d.TenantID)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(keys, func(k auth.APIKeyRecord) bool {
		return k.AgentID == d.AgentID && slices.Contains(k.Roles, auth.RoleSetupReader)
	}), nil
}

// cardRevisedMsg is a confirmation refused because a Card the draft revises
// changed since it was made (mandate.ErrCardRevised).
const cardRevisedMsg = "the agent's Cards changed since this draft was made; draft it again"

// draftError is a refused confirmation's body. One refused because a Card
// changed also says reason card_revised, so a client needn't match the text.
// draftWidens is how a draft's Cards widen the Cards in force.
func (h *MandatesHandler) draftWidens(r *http.Request, tenantID, draftID string) ([]cardWidening, int, string) {
	d, err := h.drafts.Get(r.Context(), tenantID, draftID)
	if err != nil {
		return nil, draftStatus(err), draftMessage(err)
	}
	var raw []json.RawMessage
	_ = json.Unmarshal(d.Cards, &raw)
	plan, status, msg := h.draftCards(r, tenantID, d.AgentID, raw, len(raw) == 0, d.RetireRest)
	if status != 0 {
		return nil, status, "the draft could not be confirmed: " + msg
	}
	ws, err := h.widensInForce(r.Context(), tenantID, plan.cards)
	if err != nil {
		return nil, http.StatusServiceUnavailable, "the Cards in force couldn't be read; try again"
	}
	return ws, 0, ""
}

func draftError(msg string) map[string]string {
	if msg == cardRevisedMsg {
		return map[string]string{"error": msg, "reason": "card_revised"}
	}
	return map[string]string{"error": msg}
}

func draftStatus(err error) int {
	switch {
	case errors.Is(err, agentdrafts.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, agentdrafts.ErrExpired):
		return http.StatusGone
	case errors.Is(err, agentdrafts.ErrState), errors.Is(err, agentdrafts.ErrVersion):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func draftMessage(err error) string {
	switch {
	case errors.Is(err, agentdrafts.ErrNotFound):
		return "no such draft"
	case errors.Is(err, agentdrafts.ErrExpired):
		return "the draft expired; submit it again"
	case errors.Is(err, agentdrafts.ErrVersion):
		return "the draft changed since you saw it; review it again"
	case errors.Is(err, agentdrafts.ErrState):
		return "the draft is being confirmed, or was already confirmed, claimed or discarded"
	}
	return "draft failed"
}

func writeDraftError(w http.ResponseWriter, err error) {
	writeJSON(w, draftStatus(err), map[string]string{"error": draftMessage(err)})
}

// restoreDraftBody is POST /v1/me/agent-drafts/restore: the agent's Cards
// as they were at the given revisions (kiff/tafs#107, going back to an
// earlier version).
type restoreDraftBody struct {
	AgentID   string `json:"agent_id"`
	Label     string `json:"label"`
	Revisions []struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	} `json:"revisions"`
}

// restoreDraft submits a revision draft that brings back the agent's
// Cards' terms as they were at the given revisions, read from KIFF's own
// history (kiff/tafs#107). It only waits: the owner confirms it when they
// switch back to the version that ran under those terms.
func (h *MandatesHandler) restoreDraft(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	var body restoreDraftBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body must be {agent_id, label?, revisions: [{id, revision}]}: " + err.Error()})
		return
	}
	agentID, err := agents.NormalizeID(body.AgentID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_id: " + err.Error()})
		return
	}
	label := strings.TrimSpace(body.Label)
	if len([]rune(label)) > 100 || len(body.Revisions) == 0 || len(body.Revisions) > maxDraftCards {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a restore names 1 to 20 Card revisions, and a label of at most 100 characters"})
		return
	}
	current, err := h.store.List(r.Context(), tenantID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list Cards failed"})
		return
	}
	cards := make([]json.RawMessage, 0, len(body.Revisions))
	for _, want := range body.Revisions {
		i := slices.IndexFunc(current, func(m mandate.Mandate) bool { return m.ID == want.ID })
		if i < 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no Card " + want.ID + " on the account"})
			return
		}
		revs, err := h.store.Revisions(r.Context(), tenantID, want.ID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read the Card's revisions failed"})
			return
		}
		j := slices.IndexFunc(revs, func(rv mandate.Revision) bool { return rv.Number == want.Revision })
		if j < 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Card " + want.ID + " has no revision " + strconv.FormatInt(want.Revision, 10)})
			return
		}
		b, _ := json.Marshal(revisionBody(revs[j], current[i]))
		cards = append(cards, b)
	}
	h.submitDraft(w, r, id, tenantID, agentID, label, cards, false, false, true)
}

// redraftDraft submits a revision draft's Cards again, as a new draft that
// waits (kiff/tafs#107): for a version whose draft expired, or went stale
// when a Card it revises moved on. They are checked against the Cards as
// they are now, as any draft is.
func (h *MandatesHandler) redraftDraft(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	d, err := h.drafts.Get(r.Context(), tenantID, r.PathValue("id"))
	if err != nil {
		writeDraftError(w, err)
		return
	}
	if !d.Revision || (d.Status != agentdrafts.Draft && d.Status != agentdrafts.Failed) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "only a revision draft that is waiting, expired or failed is drafted again"})
		return
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(d.Cards, &raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the draft's Cards can't be read"})
		return
	}
	// A restore drafted again still retires what the version didn't hold.
	h.submitDraft(w, r, id, tenantID, d.AgentID, d.Label, raw, false, false, d.RetireRest)
}

// revisionBody is a Card's terms at one of its revisions, as a draft's Card
// body: its grants, limits, holder, policy and expiry from the revision;
// what a revision doesn't keep (domain, environment, location, hold
// expiry) from the Card as it is now.
func revisionBody(rv mandate.Revision, cur mandate.Mandate) mandateBody {
	b := mandateBody{ID: rv.MandateID, Subject: rv.Subject, Domain: cur.Domain, Environment: cur.Environment, Location: cur.Location}
	if b.Subject == "" {
		b.Subject = cur.Subject
	}
	if rv.OnExceed != "" {
		b.OnExceed = policyView(rv.OnExceed)
	}
	if !rv.ValidUntil.IsZero() {
		until := rv.ValidUntil
		b.ValidUntil = &until
	}
	for _, g := range rv.Grants {
		gb := mandateGrantBody{Action: g.Action, EntityStates: g.EntityStates, NextSteps: g.NextSteps}
		for _, l := range g.PerAction {
			gb.PerActionMax = append(gb.PerActionMax, mandatePerActionBody{Quantity: l.Quantity.String(), Max: l.Max})
		}
		if g.Resources != nil {
			gb.Resources = &mandateResourcesBody{Parameter: g.Resources.Parameter, Values: g.Resources.Values}
		}
		b.Grants = append(b.Grants, gb)
	}
	for _, a := range rv.Aggregates {
		b.Aggregates = append(b.Aggregates, mandateAggregateBody{Quantity: a.Quantity.String(), Limit: a.Limit, Window: string(a.Window), Measure: string(a.Measure)})
	}
	return b
}
