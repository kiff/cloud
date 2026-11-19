package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/evidence"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
)

// ProofPackHandler exposes the per-control proof-pack export:
//
//	GET /v1/me/proof-pack/{action}?from=&to=
//
// It assembles a curated evidence artifact for one Protected Control
// (one action, within the tenant's active domain) over a time
// window, from data that already exists: the action contract + state
// machine + policy (the tenant's domain), the allowed / blocked /
// approval-required decisions (the decisions reader), and the signed
// receipts (the receipt store). It is not a new subsystem — it is the
// curation layer's raw export was missing.
//
// Honesty (the load-bearing constraint, aligned with the enforcement-posture design): the
// pack proves WHAT KIFF AUTHORIZED and under what state/policy — not
// that the downstream side effect ran. The action's executor is the
// tenant's own system (or cloud.proxy); KIFF is the decision boundary,
// not the runner. The verifiability section is computed from the
// receipts' actual attestation state, so the "signed vs anchored"
// claim is always tier-accurate rather than asserted.
type ProofPackHandler struct {
	tenants  tenant.Store
	controls domain.ControlStore
	source   domain.Source
	registry *executors.Registry
	reader   proposals.Reader
	receipts receipt.Store
	composer *evidence.Composer
	clock    func() time.Time
}

// ProofPackOption configures a ProofPackHandler.
type ProofPackOption func(*ProofPackHandler)

// WithProofPackClock overrides the evidence composer's clock so the
// pack's generated_at is deterministic (byte-for-byte golden tests).
func WithProofPackClock(now func() time.Time) ProofPackOption {
	return func(h *ProofPackHandler) { h.clock = now }
}

// NewProofPackHandler wires the read stores the pack draws from. Any
// of controls/source/registry/reader/receipts may be nil; the pack
// renders the sections it can and marks the rest absent rather than
// failing (mirrors the export handler's posture).
//
// The pack is assembled from the shared evidence.Composer (the evidence-timeline design
// "one view model, two renderers"): the handler resolves identity +
// window and then projects ControlEvidence into kiff.proofpack.v1.
func NewProofPackHandler(tenants tenant.Store, controls domain.ControlStore, source domain.Source, registry *executors.Registry, reader proposals.Reader, receipts receipt.Store, opts ...ProofPackOption) *ProofPackHandler {
	h := &ProofPackHandler{
		tenants:  tenants,
		controls: controls,
		source:   source,
		registry: registry,
		reader:   reader,
		receipts: receipts,
	}
	for _, opt := range opts {
		opt(h)
	}
	copts := []evidence.Option{
		evidence.WithControls(controls),
		evidence.WithDecisions(reader),
		evidence.WithReceipts(receipts),
		evidence.WithDomainSource(source, registry),
	}
	if h.clock != nil {
		copts = append(copts, evidence.WithClock(h.clock))
	}
	h.composer = evidence.NewComposer(copts...)
	return h
}

// Register attaches the proof-pack route.
func (h *ProofPackHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/proof-pack/{action}", h.export)
}

// ProofPackSchemaVersion identifies the pack shape for forward-compat.
const ProofPackSchemaVersion = "kiff.proofpack.v1"

// proofPackDisclaimer is the canonical honesty statement. It is part
// of the artifact, not decoration: an auditor reading the pack must
// not infer KIFF moved money. Derived from the enforcement-posture design's posture.
const proofPackDisclaimer = "This proof pack evidences what KIFF authorized for this control — the decision made against the entity's recorded state and the declared policy, before execution — and the signed receipts of those decisions. It does not assert that the downstream side effect executed: the action's executor is the customer's own system (or cloud.proxy). KIFF is the decision boundary, not the runner. Receipts are cryptographically signed and locally verifiable; on-chain anchoring is reflected per receipt and only where the tenant is on the anchored tier."

// proofPackDecisionCap bounds decisions pulled into a pack. The query
// is window-native and action-scoped, so this cap applies to
// the matching control+window, not the whole tenant: truncated means
// "more than this many decisions for this action in this window."
const proofPackDecisionCap = proposals.MaxLimit

// proofPackReceiptCap bounds receipt samples; generous because
// receipts are the primary signed evidence.
const proofPackReceiptCap = 10000

type proofPack struct {
	SchemaVersion string                 `json:"schema_version"`
	GeneratedAt   time.Time              `json:"generated_at"`
	Disclaimer    string                 `json:"disclaimer"`
	Tenant        proofPackTenant        `json:"tenant"`
	Control       *proofPackControl      `json:"control,omitempty"`
	Window        proofPackWindow        `json:"window"`
	Contract      *domain.ActionSpec     `json:"contract,omitempty"`
	StateMachine  *proofPackStateMachine `json:"state_machine,omitempty"`
	Policy        map[string][]string    `json:"policy,omitempty"`
	Decisions     proofPackDecisions     `json:"decisions"`
	Receipts      proofPackReceipts      `json:"receipts"`
	Verifiability proofPackVerifiability `json:"verifiability"`
	Summary       string                 `json:"summary"`
}

type proofPackTenant struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name,omitempty"`
}

type proofPackControl struct {
	Domain      string    `json:"domain"`
	Entity      string    `json:"entity"`
	Action      string    `json:"action"`
	Environment string    `json:"environment,omitempty"`
	Workflow    string    `json:"workflow,omitempty"`
	Status      string    `json:"status,omitempty"`
	ActivatedAt time.Time `json:"activated_at,omitempty"`
	ActivatedBy string    `json:"activated_by,omitempty"`
}

type proofPackWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type proofPackStateMachine struct {
	States      []string                `json:"states"`
	Transitions []domain.TransitionSpec `json:"transitions"`
}

type proofPackDecisions struct {
	Total     int                     `json:"total"`
	Counts    map[string]int          `json:"counts"`
	Truncated bool                    `json:"truncated"`
	Rows      []proposals.DecisionRow `json:"rows"`
}

type proofPackReceipts struct {
	Count         int               `json:"count"`
	SignedCount   int               `json:"signed_count"`
	AnchoredCount int               `json:"anchored_count"`
	Samples       []receipt.Receipt `json:"samples"`
}

type proofPackVerifiability struct {
	SignedAlgorithm string `json:"signed_algorithm"`
	SignedCount     int    `json:"signed_count"`
	AnchoredCount   int    `json:"anchored_count"`
	// Anchored is "none" | "partial" | "all" over the receipts in
	// this pack — computed, never asserted.
	Anchored string `json:"anchored"`
	Note     string `json:"note"`
}

// export handles GET /v1/me/proof-pack/{action}. Read-only.
func (h *ProofPackHandler) export(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	action := strings.TrimSpace(r.PathValue("action"))
	if action == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action is required"})
		return
	}

	t, err := h.tenants.Get(r.Context(), id.Tenant)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "tenant not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if t.Status == tenant.StatusDeleted {
		writeJSON(w, http.StatusGone, map[string]string{"error": "tenant has been deleted; no proof pack to export"})
		return
	}

	window, err := parseProofWindow(r, time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	environment := strings.TrimSpace(r.URL.Query().Get("environment"))

	// Assemble the shared evidence object. The proof-pack
	// route is action-scoped on purpose: decisions/receipts are queried
	// by action + window, not control_id, so the pack doesn't drop
	// decisions recorded before G0b stamping. Control identity is
	// resolved best-effort from (action, environment).
	ev, err := h.composer.Compose(r.Context(), evidence.Input{
		TenantID:    id.Tenant,
		Action:      action,
		Environment: environment,
		Window:      evidence.Window{From: window.From, To: window.To},
		Decisions: proposals.DecisionQuery{
			Action: action,
			From:   window.From,
			To:     window.To,
			Limit:  proofPackDecisionCap,
		},
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	pack := proofPack{
		SchemaVersion: ProofPackSchemaVersion,
		GeneratedAt:   ev.GeneratedAt,
		Disclaimer:    proofPackDisclaimer,
		Tenant:        proofPackTenant{ID: t.ID, Slug: t.Slug, DisplayName: t.DisplayName},
		Window:        window,
		Decisions: proofPackDecisions{
			Total:     ev.Decisions.Total,
			Counts:    ev.Decisions.Counts,
			Truncated: ev.Decisions.Truncated,
			Rows:      ev.Decisions.Rows,
		},
		Receipts: proofPackReceipts{
			Count:         ev.Receipts.Count,
			SignedCount:   ev.Receipts.SignedCount,
			AnchoredCount: ev.Receipts.AnchoredCount,
			Samples:       ev.Receipts.Samples,
		},
	}
	if c := ev.Control; c != nil {
		pack.Control = &proofPackControl{
			Domain:      c.Domain,
			Entity:      c.Entity,
			Action:      action,
			Environment: c.Environment,
			Workflow:    c.Workflow,
			Status:      c.Status,
			ActivatedAt: c.ActivatedAt,
			ActivatedBy: c.ActivatedBy,
		}
	}
	if ev.Domain.Status == evidence.StatusComplete {
		pack.Contract = ev.Domain.Contract
		pack.StateMachine = &proofPackStateMachine{
			States:      ev.Domain.States,
			Transitions: ev.Domain.Transitions,
		}
		pack.Policy = ev.Domain.Policy
	}

	v := ev.Verifiability()
	pack.Verifiability = proofPackVerifiability{
		SignedAlgorithm: v.SignedAlgorithm,
		SignedCount:     v.SignedCount,
		AnchoredCount:   v.AnchoredCount,
		Anchored:        v.Anchored,
		Note:            v.Note,
	}
	pack.Summary = proofPackSummary(action, pack)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="kiff-proof-pack-`+sanitizeFilename(action)+`.json"`)
	writeJSON(w, http.StatusOK, pack)
}

// proofPackSummary is the executive line: counts + the authorized-not-
// performed framing in one sentence.
func proofPackSummary(action string, pack proofPack) string {
	allowed := pack.Decisions.Counts["allowed"]
	blocked := pack.Decisions.Counts["blocked"]
	approval := pack.Decisions.Counts["approval_required"]
	var b strings.Builder
	b.WriteString("Over the window, KIFF evaluated ")
	b.WriteString(strconv.Itoa(pack.Decisions.Total))
	b.WriteString(" decision(s) for ")
	b.WriteString(action)
	b.WriteString(": ")
	b.WriteString(strconv.Itoa(allowed))
	b.WriteString(" allowed, ")
	b.WriteString(strconv.Itoa(blocked))
	b.WriteString(" blocked, ")
	b.WriteString(strconv.Itoa(approval))
	b.WriteString(" approval-required, with ")
	b.WriteString(strconv.Itoa(pack.Receipts.SignedCount))
	b.WriteString(" signed receipt(s). Each allowed decision authorized the action against the entity's recorded state and the declared policy; execution remained the customer's responsibility.")
	return b.String()
}

// parseProofWindow reads ?from= & ?to= (RFC3339). Defaults to the
// last 30 days. Rejects to<from.
func parseProofWindow(r *http.Request, now time.Time) (proofPackWindow, error) {
	to := now
	from := now.AddDate(0, 0, -30)
	if v := strings.TrimSpace(r.URL.Query().Get("to")); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return proofPackWindow{}, errors.New("to must be an RFC3339 timestamp")
		}
		to = parsed.UTC()
	}
	if v := strings.TrimSpace(r.URL.Query().Get("from")); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return proofPackWindow{}, errors.New("from must be an RFC3339 timestamp")
		}
		from = parsed.UTC()
	}
	if to.Before(from) {
		return proofPackWindow{}, errors.New("to must not be before from")
	}
	return proofPackWindow{From: from, To: to}, nil
}

// sanitizeFilename keeps the Content-Disposition filename to safe
// characters so an action like "PAY_INVOICE" is fine and anything
// odd can't break the header.
func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		return "control"
	}
	return out
}
