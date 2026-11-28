package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/permit"
	"github.com/kiff/cloud/api/internal/profile"
	"github.com/kiff/cloud/api/internal/proofs"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/tooloutcome"
	"github.com/kiff/cloud/api/internal/words"
)

// MandatesHandler issues, revokes and reports on mandates.
//
// Issuing is a management act, never something a runtime credential can
// do. A mandate *is* the authority, so a key that can mint its own
// would make every limit advisory — the same escalation the decide path
// refuses when a caller decorates the actor on a request body.
type MandatesHandler struct {
	// appBaseURL is KIFF's app (https://app.kiff.dev), for the page a held
	// call is answered on; "" gives the path alone.
	appBaseURL string
	// titles reads each gateway tool's title for people. Nil: none shown.
	titles  GatewayToolTitles
	tenants tenant.Store
	store   mandate.Store
	domains domain.Source
	// agents records a card's holder as an agent when it is issued.
	// Nil skips it.
	agents agents.Store
	// holds applies the governance-roles design's rules to answering a Card's held action,
	// and records who answered. Nil keeps the rules from before.
	holds *HoldGate
	// cardChangeNotice tells the account's approvers when, with
	// separation of duties on, someone changed a Card to let a held
	// action through. Nil skips it.
	cardChangeNotice func(ctx context.Context, tenantID string, c CardChange)
	holdAlert        func(ctx context.Context, tenantID, holdID string, err error)
	// outcomes says how an answered hold's call ended at the tool.
	// Nil leaves it out.
	outcomes tooloutcome.Reader
	// drafts and draftKeys hold agent drafts and mint a claimed draft's
	// key. Nil disables drafts.
	drafts    agentdrafts.Store
	draftKeys auth.APIKeyStore
	// permitTargets says which argument of a permit tool is its amount, so
	// a draft limits it (kiff/tafs#138). Nil: units and gateway tools only.
	permitTargets permit.Targets
	// calls are the account's calls, a trial's counts are read from
	// (kiff/tafs#173). Nil: a trial shows none.
	calls proposals.CallsReader
	// ownUse keeps each person's held calls to their part of the account.
	// Nil: everyone reads all of them.
	ownUse *OwnUse
	// alerts tells the account's alert targets that subscribe to
	// hold_answered when a held call is answered. Nil skips it.
	alerts alert.Notifier
	// setupReads issues Setup's Card for its reads on the owner's approval
	// (kiff/tafs#138). Nil leaves POST /v1/me/setup-reads unregistered.
	setupReads *setupReads
	logger     *slog.Logger
	now        func() time.Time
	// passkeys and rp check the held-call answers of a session that approves
	// with its device's own passkey (auth.ApprovalModePasskey).
	passkeys auth.PasskeyStore
	rp       auth.RelyingParty
	access   access.Store
	// proofKeys and proofTenants: a hold answer signed with one of the
	// proof runner's test phone keys counts only on a managed test tenant.
	proofKeys    proofs.TestKeys
	proofTenants map[string]bool
}

// WithMandatesProofTestKeys refuses, with 403 and nothing approved, every
// held-call answer from a session holding one of the proof runner's test
// phone keys (KIFF_PROOF_TEST_KEYS) on any tenant but the managed test
// tenants (KIFF_PROOF_TEST_TENANTS).
func WithMandatesProofTestKeys(keys proofs.TestKeys, testTenants []string) MandatesOption {
	return func(h *MandatesHandler) {
		h.proofKeys, h.proofTenants = keys, map[string]bool{}
		for _, t := range testTenants {
			if t = strings.TrimSpace(t); t != "" {
				h.proofTenants[t] = true
			}
		}
	}
}

// WithMandatesPasskeys checks the approve-once and change-card answers of a
// session that approves with its device's own passkey, for rp.
func WithMandatesPasskeys(store auth.PasskeyStore, rp auth.RelyingParty) MandatesOption {
	return func(h *MandatesHandler) { h.passkeys, h.rp = store, rp }
}

// WithMandatesAlerts sends hold_answered to the account's alert targets
// that list it, so an app can clear a held call it is showing.
func WithMandatesAlerts(n alert.Notifier) MandatesOption {
	return func(h *MandatesHandler) { h.alerts = n }
}

// alertAnswered tells the alert targets that a held call was answered.
// Best effort and detached, like every alert.
func (h *MandatesHandler) alertAnswered(e mandate.Exception, now time.Time) {
	notifyHoldDone(h.alerts, e, now)
}

// notifyHoldDone tells the account's alert targets that a held call stopped
// waiting for the owner: answered, withdrawn by its agent, or expired. It is
// hold_answered in every case, with the hold's status saying which, so a
// phone registered for held calls drops it from Needs you without
// subscribing to anything new.
func notifyHoldDone(n alert.Notifier, e mandate.Exception, now time.Time) {
	if n == nil {
		return
	}
	at := now
	if e.ResolvedAt != nil {
		at = *e.ResolvedAt
	}
	n.Notify(alert.Event{TenantID: e.TenantID, ProposalID: e.ProposalID, Action: e.Action, EntityID: e.EntityID,
		Outcome: alert.HoldAnswered, DecidedAt: at,
		Hold: &alert.Hold{ID: e.ID, Status: string(e.Status), ReviewURL: "/needs-you/" + url.PathEscape(e.ID)}})
}

// HoldExpiryGrace is how long after its expiry a held call is swept: well
// past the moment, so an answer given just in time is never overtaken.
const HoldExpiryGrace = time.Minute

// maxExpiredPerSweep bounds one sweep; the next tick takes the rest.
const maxExpiredPerSweep = 200

// SweepExpiredHolds records, every tick, the held calls nobody answered
// before their expiry, and tells the alert targets: the agent's
// runtime can end the paused run, and the phone drops the item, as the hold
// expires rather than at the agent's next retry.
func SweepExpiredHolds(ctx context.Context, store mandate.HoldExpirer, alerts alert.Notifier, every time.Duration, logger *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		ExpireHolds(ctx, store, alerts, time.Now(), logger)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ExpireHolds is one sweep: it expires what is due as of now and alerts.
func ExpireHolds(ctx context.Context, store mandate.HoldExpirer, alerts alert.Notifier, now time.Time, logger *slog.Logger) int {
	es, err := store.ExpireHeld(ctx, now.Add(-HoldExpiryGrace), maxExpiredPerSweep)
	if err != nil {
		logger.Warn("holds: expiry sweep failed", slog.String("err", err.Error()))
		return 0
	}
	for _, e := range es {
		notifyHoldDone(alerts, e, now)
	}
	if len(es) > 0 {
		logger.Info("holds: expired unanswered held calls", slog.Int("expired", len(es)))
	}
	return len(es)
}

// WithMandatesToolOutcomes shows, on each answered hold, how its call
// ended at the tool.
// GatewayToolTitles reads the name people see for each of an account's
// gateway tools, by the name a Card grants it under.
type GatewayToolTitles interface {
	ToolTitles(ctx context.Context, tenantID string) (map[string]string, error)
}

// WithMandatesToolTitles gives each grant of a Card or a draft its tool's
// title, so an owner reads "Refund a payment", not create_refund_k3f9.
func WithMandatesToolTitles(t GatewayToolTitles) MandatesOption {
	return func(h *MandatesHandler) { h.titles = t }
}

// GatewayToolProfiles reads each gateway tool's profile in force.
type GatewayToolProfiles interface {
	ToolProfiles(ctx context.Context, tenantID string) (map[string][]byte, error)
}

// counted is a Card's "How this is counted": for each tool it
// covers that has an owner-approved profile, KIFF's words for that tool,
// in kiff.words.v2: the lines the owner approved, less KIFF's own
// bookkeeping. Best effort: none when unread.
func (h *MandatesHandler) counted(r *http.Request, tenantID string, profiles map[string][]byte, grants []map[string]any) []map[string]any {
	out := []map[string]any{}
	for _, g := range grants {
		action, _ := g["action"].(string)
		raw, ok := profiles[action]
		if !ok {
			continue
		}
		p, err := profile.Parse(raw)
		if err != nil {
			h.logger.WarnContext(r.Context(), "mandates: a stored profile doesn't parse", slog.String("tool", action), slog.String("err", err.Error()))
			continue
		}
		lines := []string{}
		words, _ := profile.RenderVersion(p, profile.WordsVersion2)
		for _, l := range words.Lines {
			if l.Op == action || l.Op == "" {
				lines = append(lines, l.Text)
			}
		}
		out = append(out, map[string]any{"tool": action, "lines": lines})
	}
	return out
}

// toolTitles is the account's tool titles, best effort: none when they
// can't be read, and the grants then show only their action.
func (h *MandatesHandler) toolTitles(r *http.Request, tenantID string) map[string]string {
	var m map[string]string
	if h.titles != nil {
		var err error
		if m, err = h.titles.ToolTitles(r.Context(), tenantID); err != nil {
			h.logger.WarnContext(r.Context(), "mandates: tool titles unread", slog.String("tenant", tenantID), slog.String("err", err.Error()))
			m = nil
		}
	}
	// A preview's batch names the tools it would connect (draft_preview_contract.go).
	if o := overlayOf(r); o != nil && len(o.titles) > 0 {
		all := make(map[string]string, len(m)+len(o.titles))
		for k, v := range m {
			all[k] = v
		}
		for k, v := range o.titles {
			all[k] = v
		}
		m = all
	}
	return m
}

// withTitles adds "title" to each grant whose action is a tool with one.
func withTitles(grants []map[string]any, titles map[string]string) {
	for _, g := range grants {
		if a, _ := g["action"].(string); titles[a] != "" {
			g["title"] = titles[a]
		}
	}
}

func WithMandatesToolOutcomes(r tooloutcome.Reader) MandatesOption {
	return func(h *MandatesHandler) { h.outcomes = r }
}

// WithMandatesCardChangeNotice emails the account's approvers when, with
// separation of duties on, a held action is resolved by changing its
// Card: the Card-changer rule only covers later holds, so this one is
// made visible instead.
func WithMandatesCardChangeNotice(f func(ctx context.Context, tenantID string, c CardChange)) MandatesOption {
	return func(h *MandatesHandler) { h.cardChangeNotice = f }
}

// CardChange is a held action let through by changing its Card, with
// separation of duties on: what the approvers' notice says.
type CardChange struct {
	// Changer is who changed the Card: their email when the session
	// carries one, else their subject (the notice resolves it to an
	// email where it can).
	Changer string
	// Agent is the agent the action was held for, which names the Card
	// for a reader (Cards have no name of their own).
	Agent  string
	Action string
	Entity string
	// Cards are the changed Cards' ids, kept as a reference.
	Cards []string
}

// WithMandatesHoldAlert is told when an answer's attribution could not
// be written (see HoldGate.OnRecordError).
func WithMandatesHoldAlert(f func(ctx context.Context, tenantID, holdID string, err error)) MandatesOption {
	return func(h *MandatesHandler) { h.holdAlert = f }
}

// WithMandatesMemberships turns on the governance-roles design's hold rules (separation of
// duties, proposer ≠ answerer, Card changers never answer) and answer
// attribution.
func WithMandatesMemberships(m membership.Store) MandatesOption {
	return func(h *MandatesHandler) {
		if m != nil {
			h.holds = &HoldGate{Memberships: m}
			if local, ok := h.store.(*mandate.MemoryStore); ok {
				local.SetApproverPolicy(func(ctx context.Context, t, subject string) error {
					member, err := m.Latest(ctx, t, subject)
					if err != nil {
						return err
					}
					gov, err := m.Governance(ctx, t)
					if err != nil {
						return err
					}
					if member.Role != membership.RoleAdmin || member.Status != membership.StatusActive || gov.SeparationOfDuties {
						return mandate.ErrApproverGrant
					}
					return nil
				})
			}
		}
	}
}

type MandatesOption func(*MandatesHandler)

func WithMandatesTenants(s tenant.Store) MandatesOption {
	return func(h *MandatesHandler) { h.tenants = s }
}

// WithMandatesAgents records each card's holder as an agent on issue.
func WithMandatesAgents(s agents.Store) MandatesOption {
	return func(h *MandatesHandler) { h.agents = s }
}

// WithMandatesAccess gives a taf whose Cards the app issues to the person
// who issued them, when no one has its use yet.
func WithMandatesAccess(s access.Store) MandatesOption {
	return func(h *MandatesHandler) { h.access = s }
}

func WithMandatesLogger(l *slog.Logger) MandatesOption {
	return func(h *MandatesHandler) {
		if l != nil {
			h.logger = l
		}
	}
}

// WithMandatesAppBaseURL sets KIFF's app URL, for each held call's
// answer_url.
func WithMandatesAppBaseURL(u string) MandatesOption {
	return func(h *MandatesHandler) { h.appBaseURL = strings.TrimRight(strings.TrimSpace(u), "/") }
}

func WithMandatesClock(now func() time.Time) MandatesOption {
	return func(h *MandatesHandler) {
		if now != nil {
			h.now = now
		}
	}
}

// NewMandatesHandler returns nil when the store is missing, so the
// routes are absent rather than half-working.
func NewMandatesHandler(store mandate.Store, domains domain.Source, opts ...MandatesOption) *MandatesHandler {
	if store == nil {
		return nil
	}
	h := &MandatesHandler{
		store:   store,
		domains: domains,
		logger:  slog.Default(),
		now:     func() time.Time { return time.Now().UTC() },
	}
	for _, o := range opts {
		o(h)
	}
	if h.holds != nil {
		h.holds.Revisions = store.Revisions
		h.holds.OnRecordError = h.holdAlert
	}
	return h
}

func (h *MandatesHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("POST /v1/me/mandates", h.issue)
	mux.HandleFunc("GET /v1/me/mandates", h.list)
	mux.HandleFunc("POST /v1/me/mandates/{id}/revoke", h.revoke)
	mux.HandleFunc("POST /v1/me/mandates/{id}/move", h.move)
	mux.HandleFunc("PUT /v1/me/mandates/{id}/holders", h.setHolders)
	h.registerExceptions(mux)
	h.registerApproverManagement(mux)
	h.registerWarnings(mux)
	h.registerAgentDrafts(mux)
	h.registerTrials(mux)
	h.registerAllowFromHold(mux)
	mux.HandleFunc("POST /v1/me/mandates/{id}/archive", h.archive)
	mux.HandleFunc("PUT /v1/me/mandates/{id}/title", h.setTitle)
	mux.HandleFunc("GET /v1/me/mandates/{id}/statement", h.statement)
	mux.HandleFunc("GET /v1/me/card", h.agentCard)
	mux.HandleFunc("GET /v1/me/holds", h.agentHolds)
	mux.HandleFunc("POST /v1/me/holds/{id}/withdraw", h.withdrawHold)
	mux.HandleFunc("POST /v1/me/mandates/{id}/extra-room", h.grantRoom)
	mux.HandleFunc("POST /v1/me/card-effects/{proposal}/reconcile", h.reconcileEffects)
	mux.HandleFunc("POST /v1/me/card-effects/{proposal}/surcharge", h.surchargeEffects)
	mux.HandleFunc("GET /v1/me/did-this-happen", h.unproven)
	mux.HandleFunc("POST /v1/me/did-this-happen/{proposal}", h.answerHappened)
	mux.HandleFunc("POST /v1/me/did-this-happen/{proposal}/check", h.checkHappened)
	if h.setupReads != nil {
		mux.HandleFunc("POST /v1/me/setup-reads", h.issueSetupReads)
	}
}

func (h *MandatesHandler) archive(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if !isMandateManagement(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "archiving a card is a management action"})
		return
	}
	if !h.mayChangeCard(w, r, id, tenantID, r.PathValue("id")) {
		return
	}
	at := h.now()
	if err := h.store.Archive(r.Context(), tenantID, r.PathValue("id"), at); err != nil {
		switch {
		case errors.Is(err, mandate.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such mandate"})
		case errors.Is(err, mandate.ErrActive):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "revoke or let the card expire before archiving it"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "archive failed"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "archived_at": at})
}

// setTitle changes or clears a Card's title: {"title": "Refunds
// on Stripe"}, or "" to clear it. The title is the Card's name for
// people, not one of its terms, so no revision is written and its
// limits, holders and statement are untouched.
func (h *MandatesHandler) setTitle(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if !isMandateManagement(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "naming a card is a management action"})
		return
	}
	if !h.mayChangeCard(w, r, id, tenantID, r.PathValue("id")) {
		return
	}
	var body struct {
		Title *string `json:"title"`
	}
	if !decodeCardBody(w, http.MaxBytesReader(w, r.Body, 4<<10), &body) {
		return
	}
	if body.Title == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"title": "<at most 60 characters>"}, or "" to clear it`})
		return
	}
	title, err := mandate.CleanTitle(*body.Title)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cardID := r.PathValue("id")
	if err := h.store.SetTitle(r.Context(), tenantID, cardID, title); err != nil {
		if errors.Is(err, mandate.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such mandate"})
			return
		}
		h.logger.ErrorContext(r.Context(), "mandates: set title failed",
			slog.String("tenant", tenantID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "set title failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": cardID, "title": title})
}

type mandateGrantBody struct {
	Action       string   `json:"action"`
	EntityStates []string `json:"entity_states,omitempty"`
	// PerActionMax caps one action under this grant, e.g.
	// [{"quantity":"sum(amount_cents)","max":50000}] for "up to €500
	// each" (the mandates design amendment, per-action limits).
	PerActionMax []mandatePerActionBody `json:"per_action_max,omitempty"`
	// Resources limits the grant to named resources, e.g.
	// {"parameter":"order_id","values":["ord_123"]}.
	Resources *mandateResourcesBody `json:"resources,omitempty"`
	// NextSteps names the answers besides approve and reject that the
	// owner may give when a call under this grant is held, e.g.
	// ["return_first", "proof_first"]. The agent reads the chosen one.
	NextSteps []string `json:"next_steps,omitempty"`
	// Opens are the categories the owner opens on this action (K4):
	// [{"class":"irreversible","argument":"status","value":"finalize"}].
	Opens []mandateOpeningBody `json:"opens,omitempty"`
	// Purchase is how a purchase under this grant is decided (Thune):
	// {"mode":"within_limits","currency":"EUR","per_purchase":5000,...}.
	Purchase *mandatePurchaseBody `json:"purchase,omitempty"`
}

// mandatePurchaseBody is a grant's purchase rule as a Card body carries it,
// amounts in the currency's minor units.
type mandatePurchaseBody struct {
	Mode        string                `json:"mode"`
	Currency    string                `json:"currency"`
	PerPurchase int64                 `json:"per_purchase,omitempty"`
	PerDay      int64                 `json:"per_day,omitempty"`
	PerHour     int                   `json:"purchases_per_hour,omitempty"`
	PerDayCount int                   `json:"purchases_per_day,omitempty"`
	Categories  []string              `json:"categories,omitempty"`
	PriceRule   *mandatePriceRuleBody `json:"price_rule,omitempty"`
}

type mandatePriceRuleBody struct {
	UpTo int64    `json:"up_to"`
	For  []string `json:"for"`
}

// purchaseBody is a grant's purchase rule as a Card body carries it, so a
// Card rebuilt from one in force keeps it.
func purchaseBody(p *mandate.PurchaseRule) *mandatePurchaseBody {
	if p == nil {
		return nil
	}
	out := &mandatePurchaseBody{Mode: p.Mode, Currency: p.Currency, PerPurchase: p.PerPurchase, PerDay: p.PerDay, PerHour: p.PerHour,
		PerDayCount: p.PerDayCount, Categories: p.Categories}
	if r := p.PriceRule; r != nil {
		out.PriceRule = &mandatePriceRuleBody{UpTo: r.UpTo, For: r.For}
	}
	return out
}

// purchaseRule is a body's purchase rule as the Card keeps it.
func purchaseRule(b *mandatePurchaseBody) *mandate.PurchaseRule {
	if b == nil {
		return nil
	}
	out := &mandate.PurchaseRule{Mode: strings.TrimSpace(b.Mode), Currency: strings.TrimSpace(b.Currency), PerPurchase: b.PerPurchase, PerDay: b.PerDay,
		PerHour: b.PerHour, PerDayCount: b.PerDayCount, Categories: b.Categories}
	if r := b.PriceRule; r != nil {
		out.PriceRule = &mandate.PurchasePriceRule{UpTo: r.UpTo, For: r.For}
	}
	return out
}

type mandateOpeningBody struct {
	Class    string `json:"class"`
	Argument string `json:"argument,omitempty"`
	Value    string `json:"value,omitempty"`
}

// openingsBody is a grant's openings as a Card body carries them, so a Card
// rebuilt from one in force keeps what the owner opened.
func openingsBody(os []mandate.Opening) []mandateOpeningBody {
	var out []mandateOpeningBody
	for _, o := range os {
		out = append(out, mandateOpeningBody{Class: o.Class, Argument: o.Argument, Value: o.Value})
	}
	return out
}

type mandateResourcesBody struct {
	Parameter string   `json:"parameter"`
	Values    []string `json:"values"`
}

type mandatePerActionBody struct {
	Quantity string `json:"quantity"`
	Max      int64  `json:"max"`
}

type mandateAggregateBody struct {
	Quantity string `json:"quantity"`
	Limit    int64  `json:"limit"`
	Window   string `json:"window"`
	Measure  string `json:"measure,omitempty"`
	// Per names a string parameter: the limit is for each of its values
	// apart ("1 a week per invoice"). Empty is one limit for every call.
	Per string `json:"per,omitempty"`
}

type mandateBody struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	// Subjects shares the Card (the mandates design amendment): every holder, the
	// first being subject. Omitted, a reissue keeps the Card's holders
	// when subject is its first one.
	Subjects    []string               `json:"subjects,omitempty"`
	Domain      string                 `json:"domain,omitempty"`
	Environment string                 `json:"environment,omitempty"`
	Grants      []mandateGrantBody     `json:"grants"`
	Aggregates  []mandateAggregateBody `json:"aggregates"`
	ValidFrom   *time.Time             `json:"valid_from,omitempty"`
	ValidUntil  *time.Time             `json:"valid_until,omitempty"`
	Location    string                 `json:"location,omitempty"`
	RevokedAt   *time.Time             `json:"revoked_at,omitempty"`
	// OnExceed is what happens to work outside the Card: "owner" holds
	// it for the owner, "refuse" refuses it (the mandates design amendment). Omitted
	// on a new Card means owner; omitted on a reissue keeps the Card's
	// current policy, so an older client cannot change it by accident.
	OnExceed string `json:"on_exceed,omitempty"`
	// HoldExpiry is how long work held for the owner waits for an answer
	// before it is refused, as a duration such as "10m" or
	// "24h". Omitted on a new Card means 10 minutes; omitted on a reissue
	// keeps the Card's current value.
	HoldExpiry string `json:"hold_expiry,omitempty"`
	// Title is the Card's name for people, such as "Refunds on Stripe".
	// Plain text, at most 60 characters. Omitted on a reissue
	// keeps the Card's current title.
	Title string `json:"title,omitempty"`
}

func (h *MandatesHandler) issue(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	// Credential separation, the same gate the keys handler applies to
	// minting keys — with more force here, because a mandate is the
	// authority itself. A runtime key that could issue its own mandate
	// would make every limit advisory.
	if !isMandateManagement(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "issuing a mandate is a management action; a runtime credential cannot grant itself authority",
		})
		return
	}

	var body mandateBody
	if !decodeCardBody(w, http.MaxBytesReader(w, r.Body, 64<<10), &body) {
		return
	}
	m, status, msg := h.prepareCard(r, tenantID, body)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	// Inside an approval window (kiff/tafs#494), a tap issues or reissues
	// Cards only for the taf whose step opened the window (window_scope),
	// and a reissue only if it widens nothing (the widening rule).
	if windowSigned(r) {
		out, err := h.outsideWindow(r.Context(), tenantID, windowAgent(r), []mandate.Mandate{m})
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the Cards in force couldn't be read; try again"})
			return
		}
		if out {
			windowScope(w, windowAgent(r))
			return
		}
		ws, err := h.widensInForce(r.Context(), tenantID, []mandate.Mandate{m})
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the Cards in force couldn't be read; try again"})
			return
		}
		if len(ws) > 0 {
			criticalKeyRequired(w, ws)
			return
		}
	}
	if !h.mayChangeCard(w, r, id, tenantID, m.ID, m.HolderList()...) {
		return
	}

	// Who changed it. Email where the session carries one, else the
	// subject: a revision that cannot name a person is still worth
	// more than no revision, so this never refuses on attribution.
	// The store takes the Card's lock inside this write's transaction,
	// so a reissue that changes the holder or terms serializes with the
	// draws against it (the mandates design amendment, § Locking).
	// The currency asks it answers close with it (kiff/tafs#464 item 11).
	issued := func() error { return h.store.Issue(r.Context(), m, issuerOf(id)) }
	if covers := h.unitCovers(r, tenantID, []mandate.Mandate{m}); len(covers) > 0 {
		issued = func() error {
			return h.store.IssueAllCovering(r.Context(), []mandate.Mandate{m}, issuerOf(id), nil, nil, h.now(), covers)
		}
	}
	if err := issued(); err != nil {
		h.logger.ErrorContext(r.Context(), "mandates: issue failed",
			slog.String("tenant", tenantID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "issue mandate failed"})
		return
	}
	// Every card holder is an agent. Best effort: the card
	// is in force either way, and the holder is still listed as an
	// implicit agent if the record could not be written.
	h.ensureHolders(r, tenantID, m.HolderList(), issuerOf(id))
	// The policy may have been resolved by the store (an unnamed one
	// keeps the Card's current value), so answer with the Card as
	// written rather than as requested.
	if ms, err := h.store.List(r.Context(), tenantID); err == nil {
		for _, written := range ms {
			if written.ID == m.ID {
				m = written
				break
			}
		}
	}
	writeJSON(w, http.StatusCreated, mandateView(m, h.now()))
}

// prepareCard turns a Card body into the Card to issue, checked as
// issuing checks it: its terms, its holder, and the domain's action
// contracts. It returns the status and message of a refusal. Issuing and
// confirming an agent draft share it.
func (h *MandatesHandler) prepareCard(r *http.Request, tenantID string, body mandateBody) (mandate.Mandate, int, string) {
	m, err := body.toMandate(tenantID)
	if err != nil {
		return mandate.Mandate{}, http.StatusBadRequest, err.Error()
	}
	if m.Location == "" && h.tenants != nil {
		account, err := h.tenants.Get(r.Context(), tenantID)
		if err != nil {
			return mandate.Mandate{}, http.StatusServiceUnavailable, "the account time zone could not be read; no Card was issued"
		}
		m.DefaultLocation = tenant.TimeZoneFor(account)
	}
	if msg := body.policyInto(&m); msg != "" {
		return mandate.Mandate{}, http.StatusBadRequest, msg
	}
	// A card's holder is an agent id: the same rule a bound
	// key and the Agent API apply, so every new holder can be bound to a
	// key and recorded as an agent. Holders issued before this rule keep
	// their cards; see the agents list for how they appear.
	if _, err := agents.NormalizeID(m.Subject); err != nil {
		return mandate.Mandate{}, http.StatusBadRequest, "subject: " + err.Error()
	}
	for _, holder := range m.Holders {
		if _, err := agents.NormalizeID(holder); err != nil {
			return mandate.Mandate{}, http.StatusBadRequest, "subjects: " + err.Error()
		}
	}

	// Validate against the domain's action contracts, so a mandate
	// summing a parameter no action declares is refused here rather
	// than silently never binding at runtime.
	actions, domainID, err := h.actionsFor(r, tenantID, m.Domain)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "mandates: cannot load domain contracts",
			slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return mandate.Mandate{}, http.StatusServiceUnavailable, "the domain's action contracts could not be read, so this mandate cannot be validated"
	}
	if err := mandate.Validate(m, actions); err != nil {
		return mandate.Mandate{}, http.StatusBadRequest, err.Error()
	}
	// Record the domain's immutable id, so the card follows the
	// domain through a rename. A card with no domain stays unscoped.
	if m.Domain != "" {
		m.DomainID = domainID
	}
	return m, 0, ""
}

// issuerOf names the management identity behind a change, for the
// revision record.
func issuerOf(id auth.Identity) string {
	if e := strings.TrimSpace(id.Email); e != "" {
		return e
	}
	return strings.TrimSpace(id.Subject)
}

func (h *MandatesHandler) revoke(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if !isMandateManagement(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "revoking a mandate is a management action",
		})
		return
	}
	if !h.mayChangeCard(w, r, id, tenantID, r.PathValue("id")) {
		return
	}
	at := h.now()
	mandateID := r.PathValue("id")
	if err := h.store.Revoke(r.Context(), tenantID, mandateID, at); err != nil {
		if errors.Is(err, mandate.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such mandate"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "revoke failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "revoked_at": at})
}

// move gives a Card to another holder: the same id, terms, usage and
// statement, recorded as a revision that names the new holder (the mandates design
// amendment, "Moving a Card"). Reissuing with a new subject already did
// this; move is the explicit, checked form of it. It holds the Card's
// lock, and reads the Card under it, so a draw by the old holder cannot
// land after the move commits.
func (h *MandatesHandler) move(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if !isMandateManagement(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "moving a card is a management action"})
		return
	}
	var body struct {
		Subject string `json:"subject"`
	}
	if !decodeCardBody(w, http.MaxBytesReader(w, r.Body, 4096), &body) {
		return
	}
	to := strings.TrimSpace(body.Subject)
	if to == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "subject is required: the agent that will hold the card"})
		return
	}
	if !h.mayChangeCard(w, r, id, tenantID, r.PathValue("id"), to) {
		return
	}
	mandateID := r.PathValue("id")
	now := h.now()
	// The store reads and writes the Card in one transaction holding its
	// lock, so the checks below cannot go stale before the write.
	moved, err := h.store.Move(r.Context(), tenantID, mandateID, to, issuerOf(id), now)
	switch {
	case errors.Is(err, mandate.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such mandate"})
		return
	case errors.Is(err, mandate.ErrMoveInactive):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this card is revoked or outside its validity window; issue a new card instead of moving it"})
		return
	case errors.Is(err, mandate.ErrMoveSameHolder):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the card is already held by " + to})
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "mandates: move failed",
			slog.String("tenant", tenantID), slog.String("mandate", mandateID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "move failed"})
		return
	}
	h.ensureHolders(r, tenantID, []string{to}, issuerOf(id))
	writeJSON(w, http.StatusOK, mandateView(moved, now))
}

// ensureHolders records every holder as an agent. Best
// effort: the Card is in force either way, and a holder is still listed
// as an implicit agent if its record could not be written. From a phone's
// signed request (the tafs app issuing a taf's Card, or confirming its
// draft), the holder is a taf's agent.
func (h *MandatesHandler) ensureHolders(r *http.Request, tenantID string, holders []string, by string) {
	h.ensureNamed(r, tenantID, holders, "", by)
}

// ensureNamedHolder is ensureHolders for one agent, which takes name, the
// label of the draft confirmed for it, as what people read it by.
func (h *MandatesHandler) ensureNamedHolder(r *http.Request, tenantID, holder, name, by string) {
	h.ensureNamed(r, tenantID, []string{holder}, strings.TrimSpace(name), by)
}

func (h *MandatesHandler) ensureNamed(r *http.Request, tenantID string, holders []string, name, by string) {
	if h.agents == nil {
		return
	}
	kind := ""
	if auth.DeviceSigned(r.Context()) {
		kind = agents.KindTaf
	}
	for _, holder := range holders {
		if err := h.agents.Ensure(r.Context(), agents.Agent{
			TenantID: tenantID, ID: holder, CreatedAt: h.now(), CreatedBy: by, Kind: kind, Name: name,
		}); err != nil {
			h.logger.WarnContext(r.Context(), "mandates: record holder as agent failed",
				slog.String("tenant", tenantID), slog.String("err", err.Error()))
			continue
		}
		if id, err := auth.IdentityFrom(r.Context()); kind == agents.KindTaf && err == nil && id.IsPerson() {
			if err := access.GrantCreator(r.Context(), h.access, tenantID, holder, id.Subject, h.now()); err != nil {
				h.logger.WarnContext(r.Context(), "mandates: grant the taf to its creator failed",
					slog.String("tenant", tenantID), slog.String("err", err.Error()))
			}
		}
	}
}

// setHolders shares a Card or changes who holds it (the mandates design amendment, "a
// Card can be shared"): PUT {"subjects": [...], "expected": [...]}
// replaces the holders, the first becoming the Card's subject. expected
// is the list the change was built from and is required: under the
// Card's lock the store refuses the change (412) when the holders are no
// longer that list, so two owners editing at once cannot silently undo
// each other. Only the agents removed have their
// exceptions invalidated.
func (h *MandatesHandler) setHolders(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if !isMandateManagement(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "changing who holds a card is a management action"})
		return
	}
	var body struct {
		Subjects []string  `json:"subjects"`
		Expected *[]string `json:"expected"`
	}
	if !decodeCardBody(w, http.MaxBytesReader(w, r.Body, 8192), &body) {
		return
	}
	if body.Expected == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected is required: the holders the change was made from, as the card returned them"})
		return
	}
	expected := *body.Expected
	if expected == nil {
		expected = []string{}
	}
	holders := make([]string, 0, len(body.Subjects))
	for _, s := range body.Subjects {
		s = strings.TrimSpace(s)
		if _, err := agents.NormalizeID(s); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "subjects: " + err.Error()})
			return
		}
		holders = append(holders, s)
	}
	if !h.mayChangeCard(w, r, id, tenantID, r.PathValue("id"), holders...) {
		return
	}
	mandateID := r.PathValue("id")
	now := h.now()
	m, err := h.store.SetHolders(r.Context(), tenantID, mandateID, holders, expected, issuerOf(id), now)
	switch {
	case errors.Is(err, mandate.ErrHoldersChanged):
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "the card's holders changed since they were read; read the card again and make the change from its current holders"})
		return
	case errors.Is(err, mandate.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such mandate"})
		return
	case errors.Is(err, mandate.ErrMoveInactive):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this card is revoked or outside its validity window; issue a new card instead"})
		return
	case errors.Is(err, mandate.ErrMoveSameHolder):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the card already has exactly these holders"})
		return
	case errors.Is(err, mandate.ErrInvalidMandate):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "mandates: set holders failed",
			slog.String("tenant", tenantID), slog.String("mandate", mandateID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "changing the holders failed"})
		return
	}
	h.ensureHolders(r, tenantID, m.HolderList(), issuerOf(id))
	writeJSON(w, http.StatusOK, mandateView(m, now))
}

// visibleTo reports whether id may read m. A key bound to an agent reads
// only the Cards its agent holds, alone or shared; a person and
// an account key read every Card in the account.
func visibleTo(id auth.Identity, m mandate.Mandate) bool {
	return id.AgentID == "" || m.HeldBy(id.AgentID)
}

func (h *MandatesHandler) list(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	ms, err := h.store.List(r.Context(), tenantID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list mandates failed"})
		return
	}
	now := h.now()
	names := h.currentDomainNames(r, tenantID)
	// ?include=use adds each limit's use in its current window,
	// the same lines as the statement, so a phone listing Cards needs no
	// statement call per Card. Units are always on.
	withUse := r.URL.Query().Get("include") == "use"
	actionsByDomain := map[string][]domain.ActionSpec{}
	titles := h.toolTitles(r, tenantID)
	var profiles map[string][]byte
	if tp, ok := h.titles.(GatewayToolProfiles); ok {
		if profiles, err = tp.ToolProfiles(r.Context(), tenantID); err != nil {
			h.logger.WarnContext(r.Context(), "mandates: tool profiles unread", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		}
	}
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		if !visibleTo(id, m) {
			continue
		}
		v := mandateView(withCurrentDomainName(m, names), now)
		withDraftID(v, m)
		// What each summed amount means, from the Card's own domain, as
		// the statement says it. Best effort: without it numbers stay bare.
		var units map[string]domain.ParameterSpec
		if sel := cardDomain(m); sel != "" {
			actions, seen := actionsByDomain[sel]
			if !seen {
				actions, _, _ = h.actionsFor(r, tenantID, sel)
				actionsByDomain[sel] = actions
			}
			units = cardUnits(actions, m)
		}
		withUnits(v["aggregates"].([]map[string]any), units, wordsIn(replyLanguage(r)))
		for _, g := range v["grants"].([]map[string]any) {
			if per, ok := g["per_action_max"].([]map[string]any); ok {
				withUnits(per, units, wordsIn(replyLanguage(r)))
			}
		}
		withTitles(v["grants"].([]map[string]any), titles)
		v["counted"] = h.counted(r, tenantID, profiles, v["grants"].([]map[string]any))
		if withUse {
			lines, _ := h.limitLines(r.Context(), tenantID, m, now, wordsIn(replyLanguage(r)))
			withUnits(lines, units, wordsIn(replyLanguage(r)))
			if sel := cardDomain(m); sel != "" {
				withUnitGaps(lines, actionsByDomain[sel], m, titles)
			}
			v["limits"] = lines
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"mandates": out})
}

// statement is the corporate-card statement for a machine: what the
// limit is, what has been drawn against it in the current window, and
// what remains.
// limitLines are a Card's limits with what is used and left in each
// current window, and the earliest window start. The statement and the
// agent's own Card read (GET /v1/me/card) share it, so both show the same
// numbers.
func (h *MandatesHandler) limitLines(ctx context.Context, tenantID string, m mandate.Mandate, now time.Time, w *words.Words) ([]map[string]any, time.Time) {
	lines := make([]map[string]any, 0, len(m.Aggregates))
	earliest := now
	var history mandate.WarningHistory
	historyErr := mandate.ErrWarningHistory
	if hs, ok := h.store.(mandate.WarningHistoryStore); ok && len(m.Aggregates) > 0 {
		history, historyErr = hs.WarningHistory(ctx, tenantID, m.ID, now.Add(-historyReach(m)), now)
	}
	// Refused calls still counted, from the earliest window on: each line
	// takes its own (withPending).
	first := now
	for _, agg := range m.Aggregates {
		if since, err := agg.Window.Start(now, m.Loc()); err == nil && since.Before(first) {
			first = since
		}
	}
	var pending []mandate.Draw
	if len(m.Aggregates) > 0 {
		pending = h.pendingDraws(ctx, tenantID, m, first)
	}
	for _, agg := range m.Aggregates {
		since, err := agg.Window.Start(now, m.Loc())
		if err != nil {
			continue
		}
		if agg.PerItem() {
			// One balance for each item, so no single used or left: the
			// limit in words, and a held call names its item.
			lines = append(lines, map[string]any{
				"quantity": agg.Quantity.String(), "quantity_of": quantityOf(agg.Quantity), "per": agg.Quantity.Per,
				"limit": agg.Limit, "base_limit": agg.Limit, "measure": string(agg.EffectiveMeasure()),
				"window": string(agg.Window), "status": "per_item",
			})
			continue
		}
		if since.Before(earliest) {
			earliest = since
		}
		balance, err := mandate.ReadLimitBalance(ctx, h.store, tenantID, m, agg, now, "")
		usedAmt := balance.Net
		ceiling := agg.Ceiling(m, now)
		if err != nil {
			// The statement reports what it could not read rather than
			// showing a zero, which would read as an unused limit.
			lines = append(lines, map[string]any{
				"quantity": agg.Quantity.String(), "quantity_of": quantityOf(agg.Quantity), "limit": agg.Limit,
				"measure": string(agg.EffectiveMeasure()),
				"window":  string(agg.Window), "status": "unavailable",
			})
			continue
		}
		remaining := ceiling - usedAmt
		if remaining < 0 {
			remaining = 0
		}
		line := map[string]any{
			"quantity": agg.Quantity.String(), "quantity_of": quantityOf(agg.Quantity), "limit": ceiling, "base_limit": agg.Limit, "extra_room": agg.ActiveRoom(m, now), "room_grants": agg.ExtraRoom, "room_window_end": mandate.RoomWindowEnd(agg, m, now), "window": string(agg.Window),
			"gross_authorized": balance.Gross, "released": balance.Released, "net_counted": balance.Net,
			"measure": string(agg.EffectiveMeasure()),
			"used":    usedAmt, "remaining": remaining, "window_started": since, "status": "ok",
			"forecast": func() mandate.Forecast {
				if historyErr != nil {
					return mandate.Forecast{State: "unavailable", ObservedAt: now.UTC(), ObservationSeconds: int64(mandate.Observation(agg.Window) / time.Second)}
				}
				return mandate.Project(history, m, agg, now, balance)
			}(),
		}
		// When a day's total starts again: the next midnight
		// in the Card's time zone, so no app works out "today" in its own.
		// A rolling or lifetime total never starts again, so it has none.
		if agg.Window == mandate.WindowCalendarDay {
			line["window_end"] = since.In(m.Loc()).AddDate(0, 0, 1).UTC()
			line["location"] = m.Loc().String()
		}
		withPending(line, pending, agg, since, now, w)
		lines = append(lines, line)
	}
	return lines, earliest
}

// holderTotalKey is a draw's key as a holder's subtotal counts it: a
// per-item total's draws add up across its items, under "count per p".
func holderTotalKey(key string) string {
	if q, per := mandate.SplitKey(key); per != "" {
		return q + " per " + per
	}
	return key
}

// historyReach is how far back a Card's limit lines read its history: a
// day and an hour, or a week and an hour when it has a total over the
// last 7 days, so that total's forecast reads its whole window.
func historyReach(m mandate.Mandate) time.Duration {
	for _, a := range m.Aggregates {
		if a.Window == mandate.WindowRollingWeek && !a.PerItem() {
			return 7*24*time.Hour + time.Hour
		}
	}
	return 25 * time.Hour
}

// cardUnits says what each parameter this Card sums means, from the
// actions it grants that declare that parameter (the only ones whose calls
// add to the total). A parameter gets a unit only when every such action
// declares the same unit and scale; one without a unit, or a different
// one, makes the total ambiguous and it stays bare.
func cardUnits(actions []domain.ActionSpec, m mandate.Mandate) map[string]domain.ParameterSpec {
	contributors := map[string][]domain.ParameterSpec{}
	for _, a := range actions {
		if !grants(m, a.Name) {
			continue
		}
		for _, p := range a.Parameters {
			k := strings.ToLower(p.Name)
			contributors[k] = append(contributors[k], p)
		}
	}
	out := map[string]domain.ParameterSpec{}
	for k, ps := range contributors {
		agree := ps[0].Unit != ""
		for _, p := range ps[1:] {
			if p.Unit == "" || !strings.EqualFold(p.Unit, ps[0].Unit) || p.Scale != ps[0].Scale {
				agree = false
			}
		}
		if agree {
			out[k] = ps[0]
		}
	}
	return out
}

// grants reports whether the Card grants the action, in any state.
func grants(m mandate.Mandate, action string) bool {
	for _, g := range m.Grants {
		if strings.EqualFold(g.Action, action) {
			return true
		}
	}
	return false
}

// withUnits adds "unit" and "scale" to each limit line that sums a
// parameter with a declared unit, so a reader can show €80.00, not 8000.
// Lines are matched by their own quantity: limitLines skips a limit whose
// window cannot be computed, so positions do not line up with Aggregates.
func withUnits(lines []map[string]any, units map[string]domain.ParameterSpec, w *words.Words) {
	if len(units) == 0 {
		return
	}
	for _, line := range lines {
		q, _ := line["quantity"].(string)
		if !strings.HasPrefix(q, "sum(") || !strings.HasSuffix(q, ")") {
			continue
		}
		if p, ok := units[strings.ToLower(q[len("sum("):len(q)-1])]; ok {
			line["unit"] = p.Unit
			line["scale"] = p.Scale
			// Its pending use, as every other amount on it is written.
			if sum, ok := line["pending"].(int64); ok {
				calls, _ := line["pending_calls"].(int)
				line["pending_text"] = pendingText(sum, calls, false, p.Unit, p.Scale, w)
			}
		}
	}
}

// withUnitGaps says why a summed amount has no unit, where KIFF can tell:
// a tool the Card grants is no longer connected, so its amount's unit is
// gone with it (a vendor's grant was disconnected). The line names the
// tool (unit_missing) and says it in words (unit_words); the app adds how
// to reconnect. A line with a unit, or with no tool missing, says nothing
// more.
func withUnitGaps(lines []map[string]any, actions []domain.ActionSpec, m mandate.Mandate, titles map[string]string) {
	connected := map[string]bool{}
	for _, a := range actions {
		connected[strings.ToLower(a.Name)] = true
	}
	var missing []string
	for _, g := range m.Grants {
		if g.Action != "" && !connected[strings.ToLower(g.Action)] && !slices.Contains(missing, g.Action) {
			missing = append(missing, g.Action)
		}
	}
	if len(missing) == 0 {
		return
	}
	sort.Strings(missing)
	// The words name each tool as people read it: its title where KIFF still
	// has one, else its id in words. The ids stay in unit_missing.
	names := make([]string, len(missing))
	for i, a := range missing {
		names[i] = toolInWords(a, titles)
	}
	for _, line := range lines {
		q, _ := line["quantity"].(string)
		if _, has := line["unit"]; has || !strings.HasPrefix(q, "sum(") {
			continue
		}
		line["unit_missing"] = map[string]any{"tools": missing, "connected": false}
		line["unit_words"] = "Its currency comes from " + strings.Join(names, ", ") + ", which isn't connected."
	}
}

// toolInWords is a tool as an owner reads it: its title, else its id with
// underscores as spaces and a capital first letter ("create_refund" reads
// "Create refund").
func toolInWords(action string, titles map[string]string) string {
	if t := strings.TrimSpace(titles[action]); t != "" {
		return t
	}
	w := strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(action))
	if w == "" {
		return action
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

// cardDomain is the domain a Card bounds: its immutable id when it has
// one, else the name it was issued under; "" when it names none.
func cardDomain(m mandate.Mandate) string {
	if m.DomainID != "" {
		return m.DomainID
	}
	return m.Domain
}

// gatewayDomain is the domain the MCP gateway files every tool call
// under. An agent's own Card read shows the Cards that apply there.
const gatewayDomain = "gateway"

// agentCard is an agent's own read of the Cards that apply to its gateway
// calls, with what each limit has used and has left. Only a key
// bound to an agent may ask, and it sees only Cards it holds. The numbers
// are informational: every call is still checked when it is made.
//
// Setup's key (setup_reader) may also name another agent of the account
// (?agent=), to read the live Cards a taf acts under before it changes them
// (kiff/tafs#214): Coach sets tafs up and changes them, so it reads what
// they hold. It reads only; any other key that names an agent is refused.
func (h *MandatesHandler) agentCard(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if id.AgentID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a key bound to an agent can read its own Card"})
		return
	}
	other := strings.TrimSpace(r.URL.Query().Get("agent"))
	if other == id.AgentID {
		other = ""
	}
	if other != "" && !slices.Contains(id.Roles, auth.RoleSetupReader) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only Setup's key can read another agent's Card"})
		return
	}
	if other != "" && h.agents != nil {
		// A wrong id isn't an agent with no Card: Setup is told which, so it
		// never tells the owner a taf may do nothing when it named the wrong one.
		list, err := h.agents.List(r.Context(), tenantID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list agents failed"})
			return
		}
		if !slices.ContainsFunc(list, func(a agents.Agent) bool { return a.ID == other }) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no_such_agent", "message": "there is no agent " + other + " on this account"})
			return
		}
	}
	ms, err := h.store.List(r.Context(), tenantID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list mandates failed"})
		return
	}
	// During a trial against a draft (kiff/tafs#107), the Cards that draft
	// would leave: the agent is told what its calls are decided on, as the
	// trial decides them (trialDraft), not the Cards in force.
	// While its owner tries the draft (kiff/tafs#173), its trial Cards: its
	// calls are sent, decided on those alone.
	holder := id.AgentID
	trial := strings.TrimSpace(r.URL.Query().Get("trial"))
	if other != "" {
		// Another agent's live Cards: its trials are its own.
		holder, trial = other, ""
	}
	if strings.HasPrefix(trial, "drf_") && h.drafts != nil {
		if d, err := h.drafts.Get(r.Context(), tenantID, trial); err == nil && d.AgentID == id.AgentID && d.TrialOpen(h.now()) {
			holder, trial = mandate.TrialSubject(id.AgentID), ""
		}
	}
	if strings.HasPrefix(trial, "drf_") {
		cards, retire, reason, msg := trialDraft(r.Context(), h.drafts, h.now(), id, trial)
		if reason != "" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": msg, "reason": reason})
			return
		}
		ms = slices.DeleteFunc(slices.Clone(ms), func(m mandate.Mandate) bool { return slices.Contains(retire, m.ID) })
		for _, c := range cards {
			if i := slices.IndexFunc(ms, func(m mandate.Mandate) bool { return m.ID == c.ID }); i >= 0 {
				ms[i] = mandate.AsIssuedOver(c, &ms[i])
			} else {
				ms = append(ms, mandate.AsIssuedOver(c, nil))
			}
		}
	}
	// Without the domain list a Card scoped by domain id cannot be placed,
	// and leaving it out would tell the agent no Card covers a tool its
	// Card allows. Fail instead; the gateway then lists tools without the
	// Card line.
	names, err := h.domainNames(r, tenantID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list domains failed"})
		return
	}
	now := h.now()
	// The gateway domain's actions, to say what each Card's summed amounts
	// mean (unit, decimal places). Best effort: without them the numbers
	// stay bare.
	actions, _, err := h.actionsFor(r, tenantID, gatewayDomain)
	if err != nil {
		actions = nil
	}
	cards := make([]map[string]any, 0)
	// Tool titles once for every Card: a missing currency names its tool by them.
	var titles map[string]string
	for _, m := range ms {
		if !m.Active(now) || m.ArchivedAt != nil || !m.HeldBy(holder) {
			continue
		}
		// The same domain and environment rules the decision applies
		// (mandate.names): a Card scoped to another domain, or to an
		// environment the key is not in, does not cover these calls.
		switch {
		case m.DomainID != "":
			if !strings.EqualFold(names[m.DomainID], gatewayDomain) {
				continue
			}
		case m.Domain != "" && !strings.EqualFold(m.Domain, gatewayDomain):
			continue
		}
		// Another agent's Cards are shown whatever their environment, each
		// with it: Setup's key isn't the taf's.
		if other == "" && m.Environment != "" && !strings.EqualFold(m.Environment, id.Environment) {
			continue
		}
		lines, _ := h.limitLines(r.Context(), tenantID, m, now, wordsIn(replyLanguage(r)))
		withUnits(lines, cardUnits(actions, m), wordsIn(replyLanguage(r)))
		if titles == nil {
			titles = h.toolTitles(r, tenantID)
		}
		withUnitGaps(lines, actions, m, titles)
		card := map[string]any{
			"id": m.ID, "grants": grantsView(m.Grants), "limits": lines,
			"on_exceed": policyView(m.OnExceed), "location": m.Location,
		}
		if m.Environment != "" {
			card["environment"] = m.Environment
		}
		if m.Title != "" {
			card["title"] = m.Title
		}
		if !m.ValidUntil.IsZero() {
			card["valid_until"] = m.ValidUntil
		}
		cards = append(cards, card)
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": holderOf(id.AgentID, other), "cards": cards})
}

// holderOf is the agent a Card read is for: the one named, else the caller's.
func holderOf(own, other string) string {
	if other != "" {
		return other
	}
	return own
}

func (h *MandatesHandler) statement(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	mandateID := r.PathValue("id")

	ms, err := h.store.List(r.Context(), tenantID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load mandate failed"})
		return
	}
	var m *mandate.Mandate
	for i := range ms {
		// Another agent's Card is not found, the same answer as a Card
		// that does not exist, so its id confirms nothing.
		if ms[i].ID == mandateID && visibleTo(id, ms[i]) {
			m = &ms[i]
			break
		}
	}
	if m == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such mandate"})
		return
	}

	now := h.now()
	lines, earliest := h.limitLines(r.Context(), tenantID, *m, now, wordsIn(replyLanguage(r)))
	// What each summed amount means (unit, decimal places), from the
	// Card's own domain, as the agent's read says it. Best effort:
	// without the domain the numbers stay bare.
	if sel := cardDomain(*m); sel != "" {
		if actions, _, err := h.actionsFor(r, tenantID, sel); err == nil {
			withUnits(lines, cardUnits(actions, *m), wordsIn(replyLanguage(r)))
			withUnitGaps(lines, actions, *m, h.toolTitles(r, tenantID))
		}
	}

	// The issuance history, read before the movements because a
	// movement has to be counted against the terms that were in force
	// when it happened, not the ones in force now.
	//
	// A failure to read it does not fail the statement: the balance is
	// the load-bearing part, and dropping it because the history is
	// unavailable would be the worse trade. The absence is reported
	// rather than rendered as "never changed".
	revs, revErr := h.store.Revisions(r.Context(), tenantID, m.ID)

	// aggregatesAt returns the card's terms as they stood at an
	// instant. Nil when nothing is known about that instant — a draw
	// older than the first recorded revision — in which case the
	// statement says nothing about how much had been consumed rather
	// than guessing with today's terms.
	aggregatesAt := func(t time.Time) []mandate.Aggregate {
		var out []mandate.Aggregate
		for _, rev := range revs {
			if rev.At.After(t) {
				break
			}
			out = rev.Aggregates
		}
		return out
	}

	// Read further back than the current window. A rolling window that
	// applied to an older movement started before this one did: at
	// 12:00 the live window is 11:00–12:00, but the movement at 11:20
	// was counted against 10:20–11:20, and a draw at 10:40 was part of
	// its total. Fetching only the current window would drop it and
	// the row would understate what had been consumed by then.
	//
	// Every window the card has ever carried, not just today's: an
	// older movement may have been counted over a longer one.
	fetchFrom := earliest
	seen := append([]mandate.Aggregate(nil), m.Aggregates...)
	for _, rev := range revs {
		seen = append(seen, rev.Aggregates...)
	}
	for _, agg := range seen {
		windowOfWindow, werr := agg.Window.Start(earliest, m.Loc())
		if werr != nil {
			continue
		}
		if windowOfWindow.Before(fetchFrom) {
			fetchFrom = windowOfWindow
		}
	}
	// A failure here is reported, never flattened into an empty
	// window. The balance query and this one can fail independently,
	// so a statement that showed "nothing drew on this card" because
	// the ledger was unreadable would be making a claim about the
	// agent out of a fact about us — and it is the claim an owner is
	// least able to check.
	draws, drawsErr := h.store.Draws(r.Context(), tenantID, m.ID, fetchFrom)
	if drawsErr != nil {
		h.logger.ErrorContext(r.Context(), "mandates: movements unavailable",
			slog.String("tenant", tenantID), slog.String("mandate", m.ID),
			slog.String("err", drawsErr.Error()))
		draws = nil
	}
	sort.Slice(draws, func(i, j int) bool { return draws[i].DrawnAt.Before(draws[j].DrawnAt) })

	// Each movement carries what it was for, and — per limit — how much
	// of that limit's window it had consumed once it landed. A
	// statement of proposal ids is a log: nobody recognises their own
	// agent's work in it.
	//
	// Consumed-by-then, never a remaining. Remaining would need the
	// ceiling in force at that instant, and a card raised from 100 to
	// 200 would show an old movement with 180 left when 80 was the
	// truth at the time.
	//
	// One subtotal per limit, because one quantity can be capped over
	// more than one window at once — five an hour and twenty a day are
	// both true of the same movement, and a single figure could only
	// ever be one of them. Each subtotal names its own window, and both
	// the window and the terms come from the revision in force when the
	// movement happened, so re-editing the card later does not
	// retrospectively recount what already went through.
	drawnBy := func(d mandate.Draw) []map[string]any {
		out := []map[string]any{}
		terms := d.ChargedLimits
		if d.AccountingVersion == 0 {
			for _, agg := range aggregatesAt(d.DrawnAt) {
				if agg.Quantity.Key() != d.QuantityKey {
					continue
				}
				from, werr := agg.Window.Start(d.DrawnAt, m.Loc())
				if werr != nil {
					continue
				}
				terms = append(terms, mandate.ChargedLimit{Window: agg.Window, WindowStart: from})
			}
		}
		for _, term := range terms {
			var total int64
			for _, other := range draws {
				if other.QuantityKey != d.QuantityKey {
					continue
				}
				if other.DrawnAt.Before(term.WindowStart) || other.DrawnAt.After(d.DrawnAt) {
					continue
				}
				total += other.Amount
			}
			out = append(out, map[string]any{
				"window": string(term.Window), "drawn_total": total,
				"measure": string(term.Measure), "window_start": term.WindowStart,
				"window_end": term.WindowEnd, "location": term.Location,
			})
		}
		return out
	}

	drawView := make([]map[string]any, 0, len(draws))
	byHolder := map[string]map[string]int64{}
	for _, d := range draws {
		// Only the current window is rendered. The extra history
		// fetched above exists to make the older rows' subtotals
		// right, not to widen the statement.
		if d.DrawnAt.Before(earliest) {
			continue
		}
		quantity, per := mandate.SplitKey(d.QuantityKey)
		v := map[string]any{
			"proposal_id": d.ProposalID, "quantity": quantity,
			"amount": d.Amount, "drawn_at": d.DrawnAt,
			"accounting_version": d.AccountingVersion,
			// Always "authorized". KIFF decided; the caller's own
			// system runs the action afterwards and does not report
			// back, so this is the only thing the ledger can claim.
			// Every draw of one authorization is written together, so
			// a row is never here for a proposal that was refused.
			"effect": "authorized",
		}
		if totals := drawnBy(d); len(totals) > 0 {
			v["drawn_totals"] = totals
		}
		if per != "" {
			v["per"] = per
		}
		if d.Action != "" {
			v["action"] = d.Action
		}
		if d.EntityID != "" {
			v["entity_id"] = d.EntityID
		}
		if d.Subject != "" {
			// Which holder's action drew: on a shared Card the lines
			// are several agents'.
			v["subject"] = d.Subject
			if byHolder[d.Subject] == nil {
				byHolder[d.Subject] = map[string]int64{}
			}
			byHolder[d.Subject][holderTotalKey(d.QuantityKey)] += d.Amount
		}
		drawView = append(drawView, v)
	}

	// The issuance history. A card's ceiling is changed by re-issuing
	// its id, which overwrites the row, so this is the only place that
	// can answer "what was it before, and who changed it". Read above,
	// because the movements are counted against the terms that were in
	// force when each of them happened.
	revView := make([]map[string]any, 0, len(revs))
	for _, rev := range revs {
		v := map[string]any{
			"revision":   rev.Number,
			"changed_at": rev.At,
			"changed_by": rev.By,
			"aggregates": aggregatesView(rev.Aggregates),
			"grants":     grantsView(rev.Grants),
		}
		if rev.OnExceed != "" {
			v["on_exceed"] = rev.OnExceed
		}
		if rev.Subject != "" {
			v["subject"] = rev.Subject
		}
		if rev.FromHold != "" {
			v["from_hold"] = rev.FromHold // changed from a held call ("Allow this from now on")
		}
		if len(rev.Holders) > 0 {
			v["subjects"] = rev.Holders
		}
		if !rev.ValidUntil.IsZero() {
			v["valid_until"] = rev.ValidUntil
		}
		revView = append(revView, v)
	}

	card := mandateView(withCurrentDomainName(*m, h.currentDomainNames(r, tenantID)), now)
	withDraftID(card, *m)
	out := map[string]any{
		"mandate": card,
		"limits":  lines,
		"note":    "Draws record full authorized quantities, including approved per-action exceptions. Attempts remain charged; only trusted terminal no-effect evidence releases effect quantities. A release is separate evidence and never rewrites the authorization or permits another send.",
	}
	// Approved outside the Card: actions the owner approved once. They
	// did not draw on the balance above, and are totalled here so they
	// are never invisible (the mandates design amendment).
	if entries, err := h.store.ExceptionEntries(r.Context(), tenantID, m.ID, earliest); err != nil {
		out["approved_outside_error"] = "actions approved outside this card could not be read; they are unknown, not none"
	} else {
		totals := map[string]int64{}
		chargedTotals := map[string]int64{}
		chargedItems := []map[string]any{}
		items := make([]map[string]any, 0, len(entries))
		for _, en := range entries {
			quantity, per := mandate.SplitKey(en.QuantityKey)
			v := map[string]any{
				"exception_id": en.ExceptionID, "proposal_id": en.ProposalID, "quantity": quantity,
				"amount": en.Amount, "recorded_at": en.RecordedAt, "action": en.Action, "entity_id": en.EntityID,
				"subject": en.Subject, "accounting_version": en.AccountingVersion,
			}
			if per != "" {
				v["per"] = per
			}
			if en.AccountingVersion >= mandate.CumulativeAccountingVersion {
				chargedTotals[holderTotalKey(en.QuantityKey)] += en.Amount
				chargedItems = append(chargedItems, v)
			} else {
				totals[holderTotalKey(en.QuantityKey)] += en.Amount
				items = append(items, v)
			}
		}
		out["approved_outside"] = map[string]any{"entries": items, "totals": totals}
		out["approved_exceptions"] = map[string]any{"entries": chargedItems, "totals": chargedTotals, "included_in_authorized": true}
	}
	if releases, ok := h.store.(mandate.EffectReleaseStore); ok {
		if rs, err := releases.EffectReleases(r.Context(), tenantID, m.ID); err != nil {
			out["releases_error"] = "release evidence could not be read"
		} else {
			out["effect_releases"] = rs
		}
	}
	if drawsErr != nil {
		out["movements_error"] = "this card's movements could not be read; what it has drawn is unknown, not nothing"
	} else {
		if len(m.HolderList()) > 1 {
			// A shared Card's per-holder subtotals for the window; the
			// limits are the Card's, shared by all of them.
			out["by_holder"] = byHolder
		}
		out["draws"] = drawView
	}
	if revErr != nil {
		h.logger.WarnContext(r.Context(), "mandates: revisions unavailable",
			slog.String("tenant", tenantID), slog.String("mandate", m.ID),
			slog.String("err", revErr.Error()))
		out["revisions_error"] = "this card's issuance history could not be read; it is unknown, not empty"
	} else {
		out["revisions"] = revView
	}
	if warnings, ok := h.store.(mandate.WarningStore); ok {
		if events, err := warnings.CardWarnings(r.Context(), tenantID, m.ID); err != nil {
			out["warnings_error"] = "Budget warning history could not be read."
		} else {
			if lang := words.Lang(replyLanguage(r)); lang != words.EN {
				for i := range events {
					events[i].Reason = words.WarningReason(events[i], lang)
				}
			}
			out["warnings"] = events
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *MandatesHandler) identity(w http.ResponseWriter, r *http.Request) (auth.Identity, string, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return auth.Identity{}, "", false
	}
	tenantID := strings.TrimSpace(id.Tenant)
	if tenantID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return auth.Identity{}, "", false
	}
	return id, tenantID, true
}

// mayChangeCard answers 403 when an editor changes a Card held by a taf
// they didn't make, or gives it to one: the Card's
// holders now (cardID, when it exists) and those named (to). It reports
// whether to go on.
func (h *MandatesHandler) mayChangeCard(w http.ResponseWriter, r *http.Request, id auth.Identity, tenantID, cardID string, to ...string) bool {
	if id.MemberRole != membership.RoleEditor {
		return true
	}
	holders := append([]string{}, to...)
	if cardID != "" {
		ms, err := h.store.List(r.Context(), tenantID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "card lookup failed"})
			return false
		}
		for _, m := range ms {
			if m.ID == cardID {
				holders = append(holders, m.HolderList()...)
			}
		}
	}
	ok, err := mayChangeTafs(r.Context(), h.agents, id, tenantID, holders...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "agent lookup failed"})
		return false
	}
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": editorsOwnTafs})
	}
	return ok
}

// isMandateManagement: the owner's or an admin's session or key, never an
// agent's. A key bound to an agent acts as that agent; even holding
// admin, it must not issue, share, move or revoke Cards, or it could give
// itself authority (the gateway refuses such keys the same way).
func isMandateManagement(id auth.Identity) bool {
	return can(id, membership.CapCardsManage)
}

// actionsFor loads the domain the mandate claims to bound and returns
// its action contracts, so Validate can check that a summed parameter
// is one the action actually declares as numeric.
// It also returns the domain's immutable id, from the same load.
func (h *MandatesHandler) actionsFor(r *http.Request, tenantID, domainID string) ([]domain.ActionSpec, string, error) {
	if h.domains == nil {
		return nil, "", errors.New("no domain source wired")
	}
	if o := overlayOf(r); o != nil && strings.EqualFold(domainID, gatewayDomain) {
		// A preview reads the contract a batch would apply (draft_preview_contract.go);
		// the domain's id, when it exists already, from the stored one.
		id := ""
		if td, err := h.domains.LoadByDomain(r.Context(), tenantID, domainID); err == nil {
			id = td.DomainID
		}
		return o.actions, id, nil
	}
	td, err := h.domains.LoadByDomain(r.Context(), tenantID, domainID)
	if err != nil {
		return nil, "", err
	}
	parsed, err := domain.ParseAndBuild(td.YAML, nil)
	if err != nil {
		return nil, "", err
	}
	return parsed.Spec.Actions, td.DomainID, nil
}

// currentDomainNames maps each of the tenant's domain ids to its current
// name, so a card issued before a rename is shown under the name its
// domain has now. Best effort: nil when the domains cannot be
// read, and the card then shows the name it was issued under.
func (h *MandatesHandler) currentDomainNames(r *http.Request, tenantID string) map[string]string {
	names, _ := h.domainNames(r, tenantID)
	return names
}

// domainNames maps each domain id to its current slug, and says when the
// list could not be read.
func (h *MandatesHandler) domainNames(r *http.Request, tenantID string) (map[string]string, error) {
	if h.domains == nil {
		return nil, nil
	}
	list, err := h.domains.List(r.Context(), tenantID)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(list))
	for _, d := range list {
		if d.DomainID != "" && d.Slug != "" {
			names[d.DomainID] = d.Slug
		}
	}
	return names, nil
}

// withCurrentDomainName shows a card under its domain's current name.
func withCurrentDomainName(m mandate.Mandate, names map[string]string) mandate.Mandate {
	if name, ok := names[m.DomainID]; ok && m.DomainID != "" {
		m.Domain = name
	}
	return m
}

func (b mandateBody) toMandate(tenantID string) (mandate.Mandate, error) {
	m := mandate.Mandate{
		ID: strings.TrimSpace(b.ID), TenantID: tenantID,
		Subject: strings.TrimSpace(b.Subject), Domain: strings.TrimSpace(b.Domain),
		Environment: strings.TrimSpace(b.Environment), Location: strings.TrimSpace(b.Location),
	}
	if m.ID == "" {
		return mandate.Mandate{}, errors.New("id is required")
	}
	title, err := mandate.CleanTitle(b.Title)
	if err != nil {
		return mandate.Mandate{}, err
	}
	m.Title = title
	if len(b.Subjects) > 0 {
		for _, s := range b.Subjects {
			m.Holders = append(m.Holders, strings.TrimSpace(s))
		}
		if m.Subject == "" {
			m.Subject = m.Holders[0]
		}
		if m.Subject != m.Holders[0] {
			return mandate.Mandate{}, errors.New("subject must be the first of subjects")
		}
	}
	for _, g := range b.Grants {
		grant := mandate.Grant{Action: strings.TrimSpace(g.Action), EntityStates: g.EntityStates}
		for _, p := range g.PerActionMax {
			q, err := mandate.ParseQuantity(p.Quantity)
			if err != nil {
				return mandate.Mandate{}, err
			}
			grant.PerAction = append(grant.PerAction, mandate.PerActionLimit{Quantity: q, Max: p.Max})
		}
		if r := g.Resources; r != nil {
			grant.Resources = &mandate.ResourceScope{Parameter: strings.TrimSpace(r.Parameter), Values: r.Values}
		}
		for _, s := range g.NextSteps {
			grant.NextSteps = append(grant.NextSteps, strings.TrimSpace(s))
		}
		for _, o := range g.Opens {
			grant.Opens = append(grant.Opens, mandate.Opening{Class: strings.TrimSpace(o.Class), Argument: strings.TrimSpace(o.Argument), Value: o.Value})
		}
		grant.Purchase = purchaseRule(g.Purchase)
		m.Grants = append(m.Grants, grant)
	}
	for _, a := range b.Aggregates {
		q, err := mandate.ParseQuantity(a.Quantity)
		if err != nil {
			return mandate.Mandate{}, err
		}
		q.Per = strings.TrimSpace(a.Per)
		m.Aggregates = append(m.Aggregates, mandate.Aggregate{
			Quantity: q, Limit: a.Limit, Window: mandate.Window(strings.TrimSpace(a.Window)),
			Measure: mandate.Measure(strings.TrimSpace(a.Measure)),
		})
	}
	if b.ValidFrom != nil {
		m.ValidFrom = b.ValidFrom.UTC()
	}
	if b.ValidUntil != nil {
		m.ValidUntil = b.ValidUntil.UTC()
	}
	// RevokedAt is never accepted from a request body: revocation is an
	// act with its own endpoint and its own timestamp, and allowing a
	// caller to set it would let them un-revoke by re-issuing.
	return m, nil
}

// policyInto sets what happens past m's limits and how long a hold waits,
// from the body: left out, each is kept from the Card in force, resolved by
// the store under the Card's lock in the same transaction as the write (the
// current policy for an existing Card, owner for a new one; a review).
// It returns why the body is refused, or "".
func (b mandateBody) policyInto(m *mandate.Mandate) string {
	switch p := strings.TrimSpace(b.OnExceed); p {
	case mandate.OnExceedOwner, mandate.OnExceedRefuse:
		m.OnExceed = p
	case "":
		m.OnExceed = mandate.OnExceedKeep
	default:
		return `on_exceed must be "owner" or "refuse"`
	}
	if he := strings.TrimSpace(b.HoldExpiry); he == "" {
		m.HoldExpiry = mandate.HoldExpiryKeep
	} else {
		d, err := time.ParseDuration(he)
		if err != nil || d < mandate.MinHoldExpiry || d > mandate.MaxHoldExpiry {
			return fmt.Sprintf(`hold_expiry must be a duration between %s and %s, such as "10m" or "24h"`, mandate.MinHoldExpiry, mandate.MaxHoldExpiry)
		}
		m.HoldExpiry = d
	}
	return ""
}

// draftMandate is a draft's Card body as a Card, with its policy and hold
// expiry as issuing reads them (policyInto), for a trial of the draft and
// what KIFF shows of it (kiff/tafs#107). mandate.AsIssuedOver then
// resolves what it keeps from the Card in force.
func (b mandateBody) draftMandate(tenantID string) (mandate.Mandate, error) {
	m, err := b.toMandate(tenantID)
	if err != nil {
		return mandate.Mandate{}, err
	}
	if msg := b.policyInto(&m); msg != "" {
		return mandate.Mandate{}, errors.New(msg)
	}
	return m, nil
}

// aggregatesView renders the ceilings a mandate carries. Shared by the
// mandate view and the revision history, so a ceiling reads the same
// whichever of the two a client is looking at.
func aggregatesView(as []mandate.Aggregate) []map[string]any {
	out := make([]map[string]any, 0, len(as))
	for _, a := range as {
		v := map[string]any{
			"quantity": a.Quantity.String(), "quantity_of": quantityOf(a.Quantity), "limit": a.Limit, "window": string(a.Window),
			"measure": string(a.EffectiveMeasure()), "room_grants": a.ExtraRoom,
		}
		if a.PerItem() {
			v["per"] = a.Quantity.Per
		}
		out = append(out, v)
	}
	return out
}

// grantsView renders a Card's grants with their per-action limits and
// resource scopes.
// Shared by the Card view and the revision history, so a per-action
// limit reads the same in both, and a change to one is visible as a
// change between two revisions.
func grantsView(gs []mandate.Grant) []map[string]any {
	out := make([]map[string]any, 0, len(gs))
	for _, g := range gs {
		gv := map[string]any{"action": g.Action, "entity_states": g.EntityStates}
		if len(g.PerAction) > 0 {
			gv["per_action_max"] = perActionView(g.PerAction)
		}
		if g.Resources != nil {
			gv["resources"] = map[string]any{"parameter": g.Resources.Parameter, "values": g.Resources.Values}
		}
		if len(g.NextSteps) > 0 {
			gv["next_steps"] = g.NextSteps
		}
		if len(g.Opens) > 0 {
			opens := make([]map[string]any, 0, len(g.Opens))
			for _, o := range g.Opens {
				ov := map[string]any{"class": o.Class}
				if o.Argument != "" {
					ov["argument"], ov["value"] = o.Argument, o.Value
				}
				opens = append(opens, ov)
			}
			gv["opens"] = opens
		}
		if p := purchaseBody(g.Purchase); p != nil {
			gv["purchase"] = p
		}
		out = append(out, gv)
	}
	return out
}

// perActionView renders a grant's per-action limits.
func perActionView(ls []mandate.PerActionLimit) []map[string]any {
	out := make([]map[string]any, 0, len(ls))
	for _, l := range ls {
		out = append(out, map[string]any{"quantity": l.Quantity.String(), "quantity_of": quantityOf(l.Quantity), "max": l.Max})
	}
	return out
}

func mandateView(m mandate.Mandate, now time.Time) map[string]any {
	aggs := aggregatesView(m.Aggregates)
	for i, agg := range m.Aggregates {
		aggs[i]["measure"] = string(agg.EffectiveMeasure())
	}
	grants := grantsView(m.Grants)
	v := map[string]any{
		"id": m.ID, "subject": m.Subject, "subjects": m.HolderList(), "domain": m.Domain, "environment": m.Environment,
		"grants": grants, "aggregates": aggs, "active": m.Active(now),
		"location": m.Location, "on_exceed": policyView(m.OnExceed),
		"hold_expiry_seconds": int64(m.HoldFor() / time.Second),
	}
	if m.DomainID != "" {
		v["domain_id"] = m.DomainID
	}
	if !m.ValidFrom.IsZero() {
		v["valid_from"] = m.ValidFrom
	}
	if !m.ValidUntil.IsZero() {
		v["valid_until"] = m.ValidUntil
	}
	if m.RevokedAt != nil {
		v["revoked_at"] = *m.RevokedAt
	}
	if m.ArchivedAt != nil {
		v["archived_at"] = *m.ArchivedAt
	}
	if m.Title != "" {
		v["title"] = m.Title
	}
	return v
}

// withDraftID adds the agent draft that issued the Card's current terms,
// so a client can group Cards by draft. A Card issued directly has none,
// and no key.
func withDraftID(v map[string]any, m mandate.Mandate) {
	if m.DraftID != "" {
		v["draft_id"] = m.DraftID
	}
}

func policyView(p string) string {
	if p == mandate.OnExceedOwner {
		return mandate.OnExceedOwner
	}
	return mandate.OnExceedRefuse
}

// decodeCardBody decodes a Card request and refuses fields the API does
// not know. A Card is the authority itself: a misspelled limit
// ("per_action" for "per_action_max") must fail loudly rather than issue
// a Card without the limit its author meant to set.
func decodeCardBody(w http.ResponseWriter, r io.Reader, v any) bool {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil && dec.More() {
		err = errors.New("the body must be a single JSON object")
	}
	if err == nil {
		return true
	}
	msg := "invalid JSON body"
	if f, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		msg = "unknown field " + f + "; nothing was changed. Check the field name against the API (per-action limits are per_action_max)"
	} else if err.Error() == "the body must be a single JSON object" {
		msg = err.Error()
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
	return false
}

// holdView is how an agent sees one of its held calls.
func holdView(e mandate.Exception, status mandate.ExceptionStatus) map[string]any {
	v := map[string]any{"exception_id": e.ID, "action": e.Action, "status": string(status)}
	if e.Withdrawn() {
		v["withdrawn"] = true
	}
	if e.Refusal != nil && status == mandate.ExceptionRejected {
		// What the owner asked the agent to do instead.
		v["next_step"], v["note"] = e.Refusal.NextStep, e.Refusal.Note
	}
	if e.ExpiresAt != nil {
		v["expires_at"] = e.ExpiresAt
	}
	if e.ResolvedAt != nil {
		v["resolved_at"] = e.ResolvedAt
	}
	if e.Request.TrialDraft != "" {
		v["trial_draft"] = e.Request.TrialDraft // a trial's call (kiff/tafs#173)
	}
	return v
}

// withdrawHold serves POST /v1/me/holds/{id}/withdraw: the calling
// agent takes back one of its own held calls, because the run that made it
// was cancelled. The hold stops waiting; a later answer from the owner is
// refused and nothing is forwarded. A call the owner approved but the agent
// hasn't collected is withdrawn too: nothing was sent, and nobody will
// collect it. Repeating it answers the same. A call already sent, refused
// or expired is not withdrawn: 409, with where it stands, so the agent can
// record what KIFF did. Another agent's call answers like a missing one.
func (h *MandatesHandler) withdrawHold(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if id.AgentID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a key bound to an agent can withdraw its own held call"})
		return
	}
	now := h.now()
	before, err := h.store.Exception(r.Context(), tenantID, r.PathValue("id"))
	if errors.Is(err, mandate.ErrExceptionNotFound) || (err == nil && !agentsOwn(before.Subject, id.AgentID)) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such held call"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load held call failed"})
		return
	}
	e, err := h.store.Withdraw(r.Context(), tenantID, before.ID, id.AgentID, now)
	switch {
	case errors.Is(err, mandate.ErrExceptionNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such held call"})
		return
	case errors.Is(err, mandate.ErrExceptionExpired), errors.Is(err, mandate.ErrExceptionState):
		cur, rerr := h.store.Exception(r.Context(), tenantID, before.ID)
		if rerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load held call failed"})
			return
		}
		p, perr := mandate.ReadException(r.Context(), h.store, cur, now)
		if perr != nil {
			h.logger.WarnContext(r.Context(), "agent hold projection unavailable", slog.String("exception", cur.ID), slog.String("err", perr.Error()))
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": "already_answered",
			"detail": "this call was already sent, refused or re-decided, or it expired; nothing was changed",
			"hold":   holdView(p.Exception, p.Status)})
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "mandates: withdraw hold failed", slog.String("tenant", tenantID), slog.String("exception", before.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "withdraw failed"})
		return
	}
	if !before.Withdrawn() {
		// The phone's Needs you drops it, as when the owner answers.
		h.alertAnswered(e, now)
	}
	writeJSON(w, http.StatusOK, holdView(e, e.Status))
}

// maxHoldsPerRead bounds how many held calls one read looks up.
const maxHoldsPerRead = 20

// agentHolds is where the calling agent's own held calls stand: the
// exception ids the gateway recorded with each hold, comma separated in
// ids. The gateway's kiff_pending reads it so an agent learns the owner's
// answer without retrying. Read only: nothing is decided or redeemed. An
// id that is not one of this agent's exceptions is left out, the same as
// one that does not exist, so it confirms nothing about another agent.
func (h *MandatesHandler) agentHolds(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if id.AgentID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a key bound to an agent can read its own held calls"})
		return
	}
	now := h.now()
	holds := make([]map[string]any, 0)
	seen := map[string]bool{}
	for _, exID := range strings.Split(r.URL.Query().Get("ids"), ",") {
		exID = strings.TrimSpace(exID)
		if exID == "" || seen[exID] {
			continue
		}
		if len(seen) == maxHoldsPerRead {
			break
		}
		seen[exID] = true
		e, err := h.store.Exception(r.Context(), tenantID, exID)
		if errors.Is(err, mandate.ErrExceptionNotFound) || (err == nil && !agentsOwn(e.Subject, id.AgentID)) {
			continue
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load held call failed"})
			return
		}
		p, err := mandate.ReadException(r.Context(), h.store, e, now)
		if err != nil {
			h.logger.WarnContext(r.Context(), "agent hold projection unavailable", slog.String("exception", e.ID), slog.String("err", err.Error()))
		}
		e = p.Exception
		// Recheck the fresh subject; a stale view cannot disclose a moved call.
		if !agentsOwn(e.Subject, id.AgentID) {
			continue
		}
		holds = append(holds, holdView(e, p.Status))
	}
	writeJSON(w, http.StatusOK, map[string]any{"holds": holds})
}

// quantityOf is a limit's quantity as data: what it
// counts, "count" or "sum", and for a sum the parameter it adds up. The
// "quantity" string beside it ("sum(amount_cents)") stays for older
// readers; an app reads this one and parses nothing.
func quantityOf(q mandate.Quantity) map[string]any {
	v := map[string]any{"kind": string(q.Kind)}
	if q.Parameter != "" {
		v["parameter"] = q.Parameter
	}
	return v
}

// withQuantityOf puts quantity_of on each line of a Card as shown, read from
// its quantity string, for a Card KIFF shows from a draft's body.
func withQuantityOf(lines []map[string]any) {
	for _, l := range lines {
		s, _ := l["quantity"].(string)
		if q, err := mandate.ParseQuantity(s); err == nil {
			l["quantity_of"] = quantityOf(q)
		}
	}
}
