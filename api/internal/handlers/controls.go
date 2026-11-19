package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/callabout"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/evidence"
	"github.com/kiff/cloud/api/internal/halts"
	"github.com/kiff/cloud/api/internal/posture"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

// ControlsHandler exposes the tenant's control records:
//
//	GET /v1/me/controls — list all controls for the authenticated
//	                      tenant, newest first.
//	GET /v1/me/controls/{control_id}/decisions — the control-scoped,
//	                      window-native decision timeline.
//
// Control records are created automatically when a domain is
// activated (Studio's POST /v1/me/domain/draft/activate or the
// direct PUT /v1/me/domain). This handler is read-only; writes
// go through the activation paths.
type ControlsHandler struct {
	controls   domain.ControlStore
	reader     proposals.Reader    // optional; powers the decisions timeline
	tenants    tenant.Store        // optional; resolves governance mode for the control header
	posture    posture.Reader      // optional; posture-change history for the control header
	receipts   receipt.Store       // optional; control-scoped receipts
	deliveries alert.DeliveryStore // optional; alert deliveries (G4)
	composer   *evidence.Composer
	outcomes   tooloutcome.Reader // optional; how allowed gateway calls ended
	callAbout  callabout.Store    // optional; what each call is about, in the taf's words
	titles     GatewayToolTitles  // optional; each call's tool title for people
	holds      HeldCallExceptions // optional; how held calls were answered (kiff/tafs#167)
	answers    HoldAnswers        // optional; who answered them, and from which phone
	halts      halts.Store        // optional; the owner's halts and resumes, on the Record
	ownUse     *OwnUse            // optional; what of the Record each person reads
	logger     *slog.Logger
}

// WithControlsOwnUse keeps each person's Record to their own part of the
// account. Nil: everyone reads all of it.
func WithControlsOwnUse(o *OwnUse) ControlsOption {
	return func(h *ControlsHandler) { h.ownUse = o }
}

// WithControlsHalts puts the owner's halts and resumes on the Record.
func WithControlsHalts(s halts.Store) ControlsOption {
	return func(h *ControlsHandler) { h.halts = s }
}

// WithControlsToolTitles gives each activity row its tool's title, as
// Cards and held calls have it (kiff/tafs#102).
func WithControlsToolTitles(t GatewayToolTitles) ControlsOption {
	return func(h *ControlsHandler) { h.titles = t }
}

// WithControlsToolOutcomes shows, on each allowed gateway decision, how
// the call ended at the tool.
func WithControlsToolOutcomes(r tooloutcome.Reader) ControlsOption {
	return func(h *ControlsHandler) { h.outcomes = r }
}

// WithControlsCallAbout shows on each call what it's about, in the taf's
// words, as kept beside its decision (callabout).
func WithControlsCallAbout(s callabout.Store) ControlsOption {
	return func(h *ControlsHandler) { h.callAbout = s }
}

// withCallAbout puts on each row what its call is about. Best effort:
// without it a row shows none.
func (h *ControlsHandler) withCallAbout(ctx context.Context, tenantID string, rows []proposals.DecisionRow) {
	if h.callAbout == nil || len(rows) == 0 {
		return
	}
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].ProposalID
	}
	found, err := h.callAbout.About(ctx, tenantID, ids)
	if err != nil {
		h.logger.WarnContext(ctx, "controls: what calls are about unavailable", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return
	}
	for i := range rows {
		rows[i].About = found[rows[i].ProposalID]
	}
}

// withToolOutcomes sets ToolOutcome on the allowed rows the gateway has a
// record of. Best effort: without the read the rows show the decision
// alone, as before.
func (h *ControlsHandler) withToolOutcomes(ctx context.Context, tenantID string, rows []proposals.DecisionRow) {
	if h.outcomes == nil {
		return
	}
	var ids []string
	for _, r := range rows {
		if r.Outcome == "allowed" {
			ids = append(ids, r.ProposalID)
		}
	}
	if len(ids) == 0 {
		return
	}
	found, err := h.outcomes.Outcomes(ctx, tenantID, ids)
	if err != nil {
		h.logger.WarnContext(ctx, "controls: tool outcomes unavailable", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return
	}
	for i := range rows {
		if o, ok := found[rows[i].ProposalID]; ok && rows[i].Outcome == "allowed" {
			o = forReader(o)
			rows[i].ToolOutcome = &o
		}
	}
}

// ControlsOption configures a ControlsHandler.
type ControlsOption func(*ControlsHandler)

// NewControlsHandler wires the store and applies any options.
func NewControlsHandler(store domain.ControlStore, logger *slog.Logger, opts ...ControlsOption) *ControlsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &ControlsHandler{controls: store, logger: logger}
	for _, opt := range opts {
		opt(h)
	}
	// The decisions timeline is rendered from the shared evidence
	// composer. Control Room scope is by control_id; the
	// posture reader fills the header's posture block, the receipt store
	// powers control-scoped receipts, the delivery store powers
	// the alert-deliveries section (G4).
	h.composer = evidence.NewComposer(
		evidence.WithControls(store),
		evidence.WithDecisions(h.reader),
		evidence.WithPosture(h.posture),
		evidence.WithReceipts(h.receipts),
		evidence.WithDeliveries(h.deliveries),
		evidence.WithLogger(logger),
	)
	return h
}

// WithControlsDecisionsReader installs the decisions reader that
// powers GET /v1/me/controls/{control_id}/decisions. When nil, that
// route is still registered but returns the control with an empty
// decisions section (no reader wired).
func WithControlsDecisionsReader(r proposals.Reader) ControlsOption {
	return func(h *ControlsHandler) { h.reader = r }
}

// WithControlsTenantStore installs the tenant store used to resolve
// the governance mode (observe/enforce) for the decisions endpoint's
// control header. When nil, mode is omitted.
func WithControlsTenantStore(s tenant.Store) ControlsOption {
	return func(h *ControlsHandler) { h.tenants = s }
}

// WithControlsPostureReader installs the posture-change history reader
// that fills the control header's posture block — current
// mode plus "became enforce on <date> by <whom>". When nil, the
// posture block is omitted (renders as "not yet recorded" upstream),
// preserving the contract.
func WithControlsPostureReader(p posture.Reader) ControlsOption {
	return func(h *ControlsHandler) { h.posture = p }
}

// WithControlsReceipts installs the receipt store that powers the
// control-scoped receipts section: receipts resolved per
// in-scope decision via the proposal->receipt link. When nil, the
// receipts section reports "unavailable".
func WithControlsReceipts(s receipt.Store) ControlsOption {
	return func(h *ControlsHandler) { h.receipts = s }
}

// WithControlsAlertDeliveries installs the alert delivery store (G4)
// that powers the alert-deliveries section. When nil, the section
// reports "unavailable".
func WithControlsAlertDeliveries(d alert.DeliveryStore) ControlsOption {
	return func(h *ControlsHandler) { h.deliveries = d }
}

// Register attaches the routes to mux.
func (h *ControlsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/controls", h.list)
	mux.HandleFunc("GET /v1/me/controls/{control_id}/decisions", h.decisions)
	// Domain-keyed evidence view: the Control Room reached
	// from a domain and scoped to it. Served here because the Control
	// Room IS the evidence surface; resolves the domain's active control
	// and reuses the same composition as the control_id route.
	mux.HandleFunc("GET /v1/me/domains/{domain}/evidence", h.domainEvidence)
	// Every call across every domain, one row a call.
	mux.HandleFunc("GET /v1/me/activity", h.activity)
	// Requests-over-time series for the domain's Activity chart: the
	// same domain->control resolution, returning per-outcome counts
	// bucketed across the window so the web can draw a native SVG
	// time-series (no per-request payload, decisions-stream only).
	mux.HandleFunc("GET /v1/me/domains/{domain}/activity/series", h.domainActivitySeries)
	// Per-domain governance posture flip (observe <-> enforce). Authoring
	// action — requires the management role, like domain mutations.
	mux.HandleFunc("PUT /v1/me/domains/{domain}/mode", h.setDomainMode)
}

// domainModeRequest is the body of PUT /v1/me/domains/{domain}/mode.
type domainModeRequest struct {
	Mode string `json:"mode"`
}

// setDomainMode flips the active control's governance posture for a
// domain (observe <-> enforce) — the per-domain posture the decide path
// honors. Management-gated: a runtime key cannot change enforcement.
func (h *ControlsHandler) setDomainMode(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	if !requireManagement(w, id) {
		return
	}
	slug := strings.TrimSpace(r.PathValue("domain"))
	if slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domain is required"})
		return
	}
	var body domainModeRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
	}
	// "shadow" is the earlier spelling of "observe" and stays
	// accepted so a client pinned to the old vocabulary keeps
	// working; it is normalized before it reaches the store, so
	// nothing new is ever persisted under the legacy name.
	raw := strings.ToLower(strings.TrimSpace(body.Mode))
	if raw != domain.ControlModeObserve && raw != domain.LegacyControlModeShadow && raw != domain.ControlModeEnforce {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be 'observe' or 'enforce'"})
		return
	}
	mode := domain.NormalizeControlMode(raw)
	ctrl, err := h.activeControlForDomain(r.Context(), id.Tenant, slug)
	if err != nil {
		if errors.Is(err, domain.ErrControlNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "domain not found or has no active control"})
			return
		}
		h.logger.ErrorContext(r.Context(), "controls: resolve domain for mode flip failed",
			slog.String("tenant", id.Tenant), slog.String("domain", slug), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resolve domain failed"})
		return
	}
	if err := h.controls.SetMode(r.Context(), id.Tenant, ctrl.ID, mode); err != nil {
		if errors.Is(err, domain.ErrControlNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "control not found"})
			return
		}
		h.logger.ErrorContext(r.Context(), "controls: set domain mode failed",
			slog.String("tenant", id.Tenant), slog.String("control_id", ctrl.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "set mode failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"domain": slug, "mode": mode})
}

// controlResponse is the wire shape of one control record.
type controlResponse struct {
	ID          string    `json:"id"`
	Domain      string    `json:"domain"`
	Entity      string    `json:"entity"`
	Actions     []string  `json:"actions"`
	Environment string    `json:"environment"`
	Workflow    string    `json:"workflow"`
	Version     int       `json:"version"`
	ActivatedAt time.Time `json:"activated_at"`
	ActivatedBy string    `json:"activated_by"`
	Status      string    `json:"status"`
}

// controlsListResponse is the GET /v1/me/controls response body.
type controlsListResponse struct {
	Controls []controlResponse `json:"controls"`
}

func (h *ControlsHandler) list(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}

	controls, err := h.controls.List(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "controls: list failed",
			slog.String("tenant", id.Tenant),
			slog.String("err", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list controls failed"})
		return
	}

	resp := controlsListResponse{Controls: make([]controlResponse, 0, len(controls))}
	for _, c := range controls {
		actions := c.Actions
		if actions == nil {
			actions = []string{}
		}
		resp.Controls = append(resp.Controls, controlResponse{
			ID:          c.ID,
			Domain:      c.Domain,
			Entity:      c.Entity,
			Actions:     actions,
			Environment: c.Environment,
			Workflow:    c.Workflow,
			Version:     c.Version,
			ActivatedAt: c.ActivatedAt,
			ActivatedBy: c.ActivatedBy,
			Status:      c.Status,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- Control-scoped decision timeline ---

// controlDecisionsControl is the control-identity header. It carries
// enough to title the buyer-facing surface without a second guess:
// id (authoritative scope), domain/entity, the primary action this
// timeline scopes to plus the full actions list the control governs,
// environment/workflow, status, and the canonical governance mode
// (observe/enforce). The observe->enforce flip moment lands in Posture
// (G5/#380) when the posture reader is wired; nil/omitted until then.
//
// Action vs Actions: the timeline is scoped to the control (control_id),
// which may govern several actions. Action is the primary/first-declared
// action — exact for a single-action Protected Control (the Launch Pilot
// shape), the lead action otherwise; Actions carries the full set so the
// page never has to infer it. titles the surface with Action.
type controlDecisionsControl struct {
	ID          string          `json:"id"`
	Domain      string          `json:"domain"`
	Entity      string          `json:"entity"`
	Action      string          `json:"action"`
	Actions     []string        `json:"actions"`
	Environment string          `json:"environment"`
	Workflow    string          `json:"workflow"`
	Version     int             `json:"version"`
	Status      string          `json:"status"`
	Mode        string          `json:"mode,omitempty"`
	Posture     *posturePayload `json:"posture,omitempty"`
	ActivatedAt time.Time       `json:"activated_at"`
	ActivatedBy string          `json:"activated_by,omitempty"`
}

// posturePayload is the additive posture-history block on the control
// header. It states the current posture and, when recorded,
// when the gate last became enforce and by whom — the "observe trial →
// flip to enforce" narrative as evidence. Additive to the
// contract: absent when no posture reader is wired or no change has
// been recorded.
type posturePayload struct {
	Current         string     `json:"current"`
	BecameEnforceAt *time.Time `json:"became_enforce_at,omitempty"`
	BecameEnforceBy string     `json:"became_enforce_by,omitempty"`
	LastChangedAt   *time.Time `json:"last_changed_at,omitempty"`
}

// controlDecisionsResponse is the GET
// /v1/me/controls/{control_id}/decisions body. The decisions section
// matches the proof pack's proofPackDecisions (one object, two
// renderers): counts are window-complete, rows is a capped
// sample, truncated reflects the sample.
//
// Receipts, alerts, and approvals are additive sections. Each carries a
// status (complete | empty | unavailable) so the web renders honest
// empty/"not yet recorded" states from the wire rather than inferring
// from empty arrays.
type controlDecisionsResponse struct {
	Control   controlDecisionsControl `json:"control"`
	Window    proofPackWindow         `json:"window"`
	Decisions proofPackDecisions      `json:"decisions"`
	// ByAction is the per-action window-complete decision breakdown for a
	// control that governs several actions (a control mints from a whole
	// domain). It rides alongside decisions rather than inside the shared
	// proofPackDecisions shape so the proof pack stays byte-stable. Omitted
	// for single-action controls and action-scoped reads.
	ByAction  []actionCountsView       `json:"by_action,omitempty"`
	Receipts  controlDecisionsReceipts `json:"receipts"`
	Alerts    controlDecisionsAlerts   `json:"alerts"`
	Approvals controlDecisionsSection  `json:"approvals"`
}

// actionCountsView is one governed action's window-complete tally on the
// wire: the outcome counts (seeded across the known outcomes) and total.
type actionCountsView struct {
	Action string         `json:"action"`
	Total  int            `json:"total"`
	Counts map[string]int `json:"counts"`
}

// controlDecisionsSection is the minimal honest-status envelope a
// section carries when it has no payload yet (approvals in Phase 1/2).
type controlDecisionsSection struct {
	Status string `json:"status"` // complete | empty | unavailable
}

// controlDecisionsReceipts is the control-scoped receipts section:
// aggregate tally + a trimmed, newest-first sample, plus a
// scope-aware verifiability claim. Samples are trimmed by design — the
// full receipt (causal chain, compliance map) is the proof-pack
// download's job; trace_id links to the receipt detail view.
type controlDecisionsReceipts struct {
	Status        string                        `json:"status"`
	Count         int                           `json:"count"`
	SignedCount   int                           `json:"signed_count"`
	AnchoredCount int                           `json:"anchored_count"`
	Truncated     bool                          `json:"truncated"`
	Verifiability controlDecisionsVerifiability `json:"verifiability"`
	Samples       []receiptSampleView           `json:"samples"`
}

// receiptSampleView is the glanceable per-receipt view.
type receiptSampleView struct {
	TraceID    string    `json:"trace_id"`
	ProposalID string    `json:"proposal_id,omitempty"`
	EntityID   string    `json:"entity_id"`
	ActionName string    `json:"action_name"`
	IssuedAt   time.Time `json:"issued_at"`
	Signed     bool      `json:"signed"`
	Anchored   bool      `json:"anchored"`
	// Kind is "decision" for a pre-execution decision receipt — the
	// cloud's signed proof that KIFF decided before the action ran — or
	// "execution" for a receipt of an action that executed. Lets the UI
	// label the two without re-encoding the trace-id convention.
	Kind string `json:"kind"`
}

// controlDecisionsVerifiability carries the computed claim with its
// scope: "sample" means the counts are over the shown sample,
// not window-complete, so an "all" claim is never misread.
type controlDecisionsVerifiability struct {
	SignedAlgorithm string `json:"signed_algorithm"`
	SignedCount     int    `json:"signed_count"`
	AnchoredCount   int    `json:"anchored_count"`
	Anchored        string `json:"anchored"` // none | partial | all
	Note            string `json:"note"`
	Scope           string `json:"scope,omitempty"` // sample | window
}

// controlDecisionsAlerts is the alert-deliveries section (G4/#406 Q4):
// an aggregate list, each delivery carrying proposal_id so the web can
// cross-link back to its triggering decision row.
type controlDecisionsAlerts struct {
	Status     string              `json:"status"`
	Deliveries []alertDeliveryView `json:"deliveries"`
}

// alertDeliveryView is the wire view of one alert delivery attempt.
// status is one of delivered | failed | blocked.
type alertDeliveryView struct {
	ProposalID  string    `json:"proposal_id,omitempty"`
	TargetID    string    `json:"target_id"`
	TargetType  string    `json:"target_type"`
	Status      string    `json:"status"`
	AttemptedAt time.Time `json:"attempted_at"`
}

// knownDecisionOutcomes is the wire enum the counts map always carries,
// seeded to 0 so the header never has to nil-check (frozen enum).
var knownDecisionOutcomes = []string{"allowed", "blocked", "approval_required", "owner_required", "limit_exceeded", "invalid"}

// decisions handles GET /v1/me/controls/{control_id}/decisions.
// Read-only, control-scoped, window-native.
func (h *ControlsHandler) decisions(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	controlID := strings.TrimSpace(r.PathValue("control_id"))
	if controlID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "control_id is required"})
		return
	}
	h.writeControlEvidence(w, r, id.Tenant, controlID)
}

// writeControlEvidence composes and writes the control-scoped evidence
// payload for (tenantID, controlID) over the request's
// window. Shared by the control_id route (GET
// /v1/me/controls/{control_id}/decisions) and the domain-keyed
// evidence route (GET /v1/me/domains/{domain}/evidence, the authenticated-surface design):
// the Control Room rendered as a domain's evidence view. A missing
// control (resolved by the composer) is a 404.
func (h *ControlsHandler) writeControlEvidence(w http.ResponseWriter, r *http.Request, tenantID, controlID string) {
	window, err := parseProofWindow(r, time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Render from the shared evidence object, control_id-scoped.
	// The decision query carries the control_id (== the resolved control's
	// id), outcome filters, window, and limit; the composer resolves the
	// control and assembles decisions + posture for the header.
	// ?actor= narrows the timeline to one proposing identity — the
	// holder of a card, when this page was reached from one. The store
	// has filtered on actor since the per-runtime slice; this route
	// simply never read the parameter, so a card could name its holder
	// and had nowhere to send anyone.
	q := proposals.DecisionQuery{
		ControlID: controlID,
		Actor:     strings.TrimSpace(r.URL.Query().Get("actor")),
		Outcomes:  outcomesFromQuery(r),
		From:      window.From,
		To:        window.To,
		Limit:     parseLimit(r.URL.Query().Get("limit"), proposals.DefaultLimit, proposals.MaxLimit),
		Offset:    parseOffset(r.URL.Query().Get("offset"), proposals.MaxOffset),
	}
	ev, err := h.composer.Compose(r.Context(), evidence.Input{
		TenantID:  tenantID,
		ControlID: controlID,
		Window:    evidence.Window{From: window.From, To: window.To},
		Decisions: q,
	})
	if err != nil {
		if errors.Is(err, evidence.ErrControlNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "control not found"})
			return
		}
		h.logger.ErrorContext(r.Context(), "controls: compose evidence failed",
			slog.String("tenant", tenantID), slog.String("control_id", controlID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resolve control failed"})
		return
	}
	ctrl := ev.Control

	header := controlDecisionsControl{
		ID:          ctrl.ID,
		Domain:      ctrl.Domain,
		Entity:      ctrl.Entity,
		Actions:     append([]string(nil), ctrl.Actions...),
		Environment: ctrl.Environment,
		Workflow:    ctrl.Workflow,
		Version:     ctrl.Version,
		Status:      ctrl.Status,
		ActivatedAt: ctrl.ActivatedAt,
		ActivatedBy: ctrl.ActivatedBy,
	}
	if header.Actions == nil {
		header.Actions = []string{}
	}
	// Primary/first-declared action titles the single-control timeline.
	if len(ctrl.Actions) > 0 {
		header.Action = ctrl.Actions[0]
	}
	if h.tenants != nil {
		if t, terr := h.tenants.Get(r.Context(), tenantID); terr == nil {
			header.Mode = string(tenant.ModeFor(t))
		}
	}
	// Posture history: "became enforce on <date> by <whom>"
	// plus the last change, sourced from the composed evidence. The
	// block is emitted whenever a posture reader is wired (Status !=
	// Unavailable); current mode stays the authoritative tenant mode.
	if ev.Posture.Status != evidence.StatusUnavailable {
		current := header.Mode
		if current == "" {
			current = string(tenant.ModeEnforce)
		}
		pp := &posturePayload{Current: current}
		if !ev.Posture.LastChangedAt.IsZero() {
			at := ev.Posture.LastChangedAt
			pp.LastChangedAt = &at
		}
		if !ev.Posture.BecameEnforceAt.IsZero() {
			at := ev.Posture.BecameEnforceAt
			pp.BecameEnforceAt = &at
			pp.BecameEnforceBy = ev.Posture.BecameEnforceBy
		}
		header.Posture = pp
	}

	resp := controlDecisionsResponse{
		Control: header,
		Window:  proofPackWindow{From: window.From, To: window.To},
		Decisions: proofPackDecisions{
			Counts: seededOutcomeCounts(),
			Rows:   []proposals.DecisionRow{},
		},
	}
	// Merge the window-complete counts onto the seeded map so all five
	// outcomes stay present (seeded 0) even when absent.
	for k, v := range ev.Decisions.Counts {
		resp.Decisions.Counts[k] = v
	}
	resp.Decisions.Total = ev.Decisions.Total
	resp.Decisions.Truncated = ev.Decisions.Truncated
	resp.Decisions.Rows = append(resp.Decisions.Rows, ev.Decisions.Rows...)
	h.withToolOutcomes(r.Context(), tenantID, resp.Decisions.Rows)

	// Per-action breakdown (control governs several actions). Seed each
	// action's counts across the known outcomes so the renderer never has
	// to nil-check, mirroring the top-line counts.
	if len(ev.Decisions.ByAction) > 0 {
		resp.ByAction = make([]actionCountsView, 0, len(ev.Decisions.ByAction))
		for _, a := range ev.Decisions.ByAction {
			counts := seededOutcomeCounts()
			for k, v := range a.Counts {
				counts[k] = v
			}
			resp.ByAction = append(resp.ByAction, actionCountsView{Action: a.Action, Total: a.Total, Counts: counts})
		}
	}

	// Additive evidence sections (shape). Each
	// carries its honest status; the web branches on it for empty /
	// "not yet recorded" / unavailable states.
	resp.Receipts = receiptsWire(ev)
	resp.Alerts = alertsWire(ev)
	resp.Approvals = controlDecisionsSection{Status: string(ev.Approvals.Status)}

	writeJSON(w, http.StatusOK, resp)
}

// domainEvidence handles GET /v1/me/domains/{domain}/evidence — the
// Control Room rendered as a domain's evidence view. It
// resolves the domain's active control and returns the same
// control-scoped evidence payload as the control_id route, so the
// authenticated surface can reach a domain's decisions/receipts/alerts
// by domain slug rather than threading a control id.
//
// Today one active control governs a domain, so domain scope ==
// control scope. When a domain grows to several controls, this handler
// is where cross-control aggregation will live; the route contract
// (domain slug -> evidence) does not change.
func (h *ControlsHandler) domainEvidence(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	slug := strings.TrimSpace(r.PathValue("domain"))
	if slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domain is required"})
		return
	}

	ctrl, err := h.activeControlForDomain(r.Context(), id.Tenant, slug)
	if err != nil {
		if errors.Is(err, domain.ErrControlNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "domain not found"})
			return
		}
		h.logger.ErrorContext(r.Context(), "controls: resolve domain evidence failed",
			slog.String("tenant", id.Tenant), slog.String("domain", slug), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resolve domain failed"})
		return
	}
	h.writeControlEvidence(w, r, id.Tenant, ctrl.ID)
}

// --- Requests-over-time series (Activity chart) ---

// activitySeriesResponse is the GET
// /v1/me/domains/{domain}/activity/series body: the resolved window,
// the bucket width the server picked for that span, and the non-empty
// per-outcome buckets in ascending time order. The web fills gaps from
// from/to+bucket_seconds and draws the chart natively (no JS lib).
type activitySeriesResponse struct {
	Domain        string                 `json:"domain"`
	ControlID     string                 `json:"control_id"`
	Actor         string                 `json:"actor,omitempty"`
	From          time.Time              `json:"from"`
	To            time.Time              `json:"to"`
	BucketSeconds int                    `json:"bucket_seconds"`
	Buckets       []proposals.TimeBucket `json:"buckets"`
}

// domainActivitySeries handles GET
// /v1/me/domains/{domain}/activity/series?from=&to=&actor=. It resolves
// the domain's active control, buckets the control-scoped decisions over
// the window, and returns the per-outcome time series. An optional
// ?actor= narrows to one proposing identity (the per-runtime slice).
//
// The reader is optional (WithControlsDecisionsReader). When unwired the
// endpoint returns an empty series rather than 503, so the chart renders
// its honest empty state instead of a hard error.
func (h *ControlsHandler) domainActivitySeries(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	slug := strings.TrimSpace(r.PathValue("domain"))
	if slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domain is required"})
		return
	}
	window, err := parseProofWindow(r, time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	ctrl, err := h.activeControlForDomain(r.Context(), id.Tenant, slug)
	if err != nil {
		if errors.Is(err, domain.ErrControlNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "domain not found"})
			return
		}
		h.logger.ErrorContext(r.Context(), "controls: resolve domain series failed",
			slog.String("tenant", id.Tenant), slog.String("domain", slug), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resolve domain failed"})
		return
	}

	actor := strings.TrimSpace(r.URL.Query().Get("actor"))
	bucket := pickBucket(window.From, window.To)
	resp := activitySeriesResponse{
		Domain:        slug,
		ControlID:     ctrl.ID,
		Actor:         actor,
		From:          window.From,
		To:            window.To,
		BucketSeconds: int(bucket / time.Second),
		Buckets:       []proposals.TimeBucket{},
	}
	if h.reader != nil {
		q := proposals.DecisionQuery{
			ControlID: ctrl.ID,
			Actor:     actor,
			Outcomes:  outcomesFromQuery(r),
			From:      window.From,
			To:        window.To,
		}
		buckets, berr := h.reader.CountDecisionsOverTime(r.Context(), id.Tenant, q, bucket)
		if berr != nil {
			h.logger.ErrorContext(r.Context(), "controls: activity series failed",
				slog.String("tenant", id.Tenant), slog.String("control_id", ctrl.ID), slog.String("err", berr.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "compute series failed"})
			return
		}
		if len(buckets) > 0 {
			resp.Buckets = buckets
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// pickBucket chooses a bucket width from the window span so the series
// stays at a readable resolution (~30-120 buckets) across the range the
// web's presets expose (5m .. 7d). Wider windows get coarser buckets;
// the thresholds are inclusive upper bounds on the span.
func pickBucket(from, to time.Time) time.Duration {
	span := to.Sub(from)
	switch {
	case span <= 15*time.Minute:
		return 30 * time.Second
	case span <= time.Hour:
		return time.Minute
	case span <= 6*time.Hour:
		return 5 * time.Minute
	case span <= 24*time.Hour:
		return 15 * time.Minute
	case span <= 7*24*time.Hour:
		return time.Hour
	case span <= 30*24*time.Hour:
		return 6 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// activeControlForDomain returns the active control governing the named
// domain for the tenant. Returns domain.ErrControlNotFound when no
// active control matches — no domain configured, or a slug the tenant
// does not own (mechanically tenant-scoped via the store's List, so a
// cross-tenant slug is unreachable and 404s like an unknown one).
func (h *ControlsHandler) activeControlForDomain(ctx context.Context, tenantID, slug string) (domain.Control, error) {
	list, err := domain.ActiveControls(ctx, h.controls, tenantID)
	if err != nil {
		return domain.Control{}, err
	}
	for _, c := range list {
		if strings.EqualFold(c.Domain, slug) {
			return c, nil
		}
	}
	return domain.Control{}, domain.ErrControlNotFound
}

// receiptsWire projects the composed receipts section onto the wire:
// aggregate tally + trimmed, newest-first samples + scope-aware
// verifiability.
func receiptsWire(ev evidence.ControlEvidence) controlDecisionsReceipts {
	v := ev.Verifiability()
	out := controlDecisionsReceipts{
		Status:        string(ev.Receipts.Status),
		Count:         ev.Receipts.Count,
		SignedCount:   ev.Receipts.SignedCount,
		AnchoredCount: ev.Receipts.AnchoredCount,
		Truncated:     ev.Receipts.Truncated,
		Verifiability: controlDecisionsVerifiability{
			SignedAlgorithm: v.SignedAlgorithm,
			SignedCount:     v.SignedCount,
			AnchoredCount:   v.AnchoredCount,
			Anchored:        v.Anchored,
			Note:            v.Note,
			Scope:           v.Scope,
		},
		Samples: make([]receiptSampleView, 0, len(ev.Receipts.Samples)),
	}
	for _, rec := range ev.Receipts.Samples {
		kind := "execution"
		if receipt.IsDecisionReceiptTrace(rec.TraceID) {
			kind = "decision"
		}
		out.Samples = append(out.Samples, receiptSampleView{
			TraceID:    rec.TraceID,
			ProposalID: rec.ProposalID,
			EntityID:   rec.EntityID,
			ActionName: rec.ActionName,
			IssuedAt:   rec.IssuedAt,
			Signed:     rec.Attestation != nil && rec.Attestation.Signature != "",
			Anchored:   rec.Attestation != nil && rec.Attestation.AnchorTxHash != "",
			Kind:       kind,
		})
	}
	return out
}

// alertsWire projects the composed alerts section onto the wire: an
// aggregate delivery list, each carrying proposal_id for cross-linking.
func alertsWire(ev evidence.ControlEvidence) controlDecisionsAlerts {
	out := controlDecisionsAlerts{
		Status:     string(ev.Alerts.Status),
		Deliveries: make([]alertDeliveryView, 0, len(ev.Alerts.Deliveries)),
	}
	for _, d := range ev.Alerts.Deliveries {
		out.Deliveries = append(out.Deliveries, alertDeliveryView{
			ProposalID:  d.ProposalID,
			TargetID:    d.TargetID,
			TargetType:  d.TargetType,
			Status:      d.Status,
			AttemptedAt: d.AttemptedAt,
		})
	}
	return out
}

// seededOutcomeCounts returns the counts map with every known outcome
// present at 0, so the buyer-facing header renders all five without a
// nil-check (frozen enum).
func seededOutcomeCounts() map[string]int {
	m := make(map[string]int, len(knownDecisionOutcomes))
	for _, o := range knownDecisionOutcomes {
		m[o] = 0
	}
	return m
}

// outcomesFromQuery reads repeatable ?outcome= values, keeping only
// known outcomes (an unknown value is ignored rather than 400-ing, so
// a forward-looking client can't break the read). Empty → all.
func outcomesFromQuery(r *http.Request) []string {
	raw := r.URL.Query()["outcome"]
	if len(raw) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, o := range knownDecisionOutcomes {
		known[o] = true
	}
	out := make([]string, 0, len(raw))
	for _, o := range raw {
		o = strings.TrimSpace(o)
		if known[o] {
			out = append(out, o)
		}
	}
	return out
}

// activateControl records a control for an activation. A control that
// replaces an active one in the same lineage (tenant, domain,
// environment) keeps that control's governance posture: re-saving a
// domain changes its contract, not whether its decisions bind. Before
// this, every in-place update reset an enforced domain to observe,
// silently making its refusals advisory. Only a domain's first
// activation starts in NewControlFromParse's observe.
func activateControl(ctx context.Context, store domain.ControlStore, ctrl domain.Control) error {
	lineage := ctrl.WithIdentityDefaults()
	if list, err := store.List(ctx, ctrl.TenantID); err == nil {
		for _, prior := range list {
			prior = prior.WithIdentityDefaults()
			if prior.Status == "active" && prior.Domain == lineage.Domain && prior.Environment == lineage.Environment {
				// Inherited as is, including "" (follow the workspace
				// default), so the posture the domain had is the posture
				// it keeps.
				ctrl.Mode = domain.NormalizeControlMode(prior.Mode)
				break
			}
		}
	}
	return store.Create(ctx, ctrl)
}

// NewControlFromParse builds a Control from a parsed domain and
// the activating identity. Called by the activation paths.
func NewControlFromParse(tenantID, activatedBy string, parsed domain.ParseResult) domain.Control {
	actions := make([]string, 0, len(parsed.Spec.Actions))
	for _, a := range parsed.Spec.Actions {
		actions = append(actions, a.Name)
	}
	return domain.Control{
		ID:          tenant.NewID(),
		TenantID:    tenantID,
		Domain:      parsed.Spec.Domain,
		Entity:      parsed.Spec.Entity,
		Actions:     actions,
		ActivatedAt: time.Now().UTC(),
		ActivatedBy: activatedBy,
		Status:      "active",
		// New activations start in observe: watch real decisions first,
		// then flip to enforce deliberately (per-domain posture). The
		// decide path honors this over the tenant-wide mode.
		Mode: domain.ControlModeObserve,
	}
}
