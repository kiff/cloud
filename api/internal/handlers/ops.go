package handlers

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/notify"
	"github.com/kiff/cloud/api/internal/operatoralerts"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/owneremail"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/posture"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/support"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// OpsHandler serves the operator console's read-only, cross-tenant
// endpoints under /v1/ops/*. It is the ONLY place in
// apps/api where cross-tenant reads are sanctioned, and every route
// is gated by an operator allow-list (KIFF_OPS_SUBJECTS) re-checked
// here independently of the apps/web gate — the API gate is the real
// security boundary.
//
// A non-operator (any tenant session, or an API key) gets 404, not
// 403: the surface must not be discoverable by tenant callers. The
// handler is registered only when the allow-list is non-empty, so a
// deployment with no operators configured exposes no /v1/ops/*
// surface at all.
//
// All read routes are GET; the control-plane mutations are
// POST and run through OpsMutator so the state change and its audit
// row commit atomically. The per-surface handlers live in sibling
// files: ops_overview.go, ops_tenants.go, ops_activity.go,
// ops_actions.go, ops_alerts.go.
type OpsHandler struct {
	operators map[string]struct{}
	tenants   tenant.Store
	usage     usage.Store
	receipts  receipt.Store
	decisions proposals.Reader    // optional; nil omits proposals in detail
	posture   posture.Reader      // optional; nil omits posture in detail
	alerts    alert.DeliveryStore // optional; nil omits alert health in detail
	support   support.Store       // optional; nil omits the /v1/ops/support surface

	// supportLinkBase is the app origin (e.g. https://app.kiff.dev)
	// used to absolutize the ticket link in close-the-loop support
	// emails. Empty leaves the link relative.
	supportLinkBase string

	// Control-plane deps. audit and resetter are required
	// for the mutation routes (suspend/unsuspend); when audit is nil
	// the mutation routes are not registered (read-only console).
	audit      operatoraudit.Store
	resetter   RuntimeResetter
	alertQueue operatoralerts.Store // optional; nil omits the alert queue
	mutator    OpsMutator           // atomic state-change + audit
	// bonus records the bonus credits an operator grants, as a pack of
	// credits that never expires; nil leaves the route unmounted.
	bonus packs.CreditLedger
	// keys and runs show an account's API keys and its runs (ops round,
	// batch 1); each nil leaves its part out.
	keys auth.APIKeyStore
	runs OpsRunDays
	// proofs is the integration proofs' monitoring page, replay and
	// overrides (ops_proofs.go); nil leaves them unmounted.
	proofs *opsProofs

	// Customer notifications. Both optional and wired
	// together: when set, suspend/unsuspend resolve the owner email
	// (Clerk, never stored) and send a customer email best-effort.
	ownerEmail *owneremail.Resolver
	notifier   notify.Notifier
	logger     *slog.Logger

	// dispatch runs a detached side effect (customer notification).
	// Production sends it to a goroutine; tests set a synchronous
	// dispatch so the notification outcome is deterministic.
	dispatch func(func())

	now func() time.Time
}

// NewOpsHandler builds the operator handler from the allow-list and
// the cross-tenant read stores. operatorSubjects is the set of Clerk
// subjects (or stub subjects) permitted to reach the console. tenants,
// usage, and receipts are required; the per-tenant detail readers
// (decisions, posture, alerts) are optional and degrade gracefully
// (their sections are omitted, not errored) when nil.
//
// Returns nil when operatorSubjects is empty or a required store is
// nil — the caller treats nil as "do not mount the ops surface."
func NewOpsHandler(operatorSubjects []string, tenants tenant.Store, usageStore usage.Store, receipts receipt.Store, decisions proposals.Reader, postureReader posture.Reader, alerts alert.DeliveryStore, opts ...OpsOption) *OpsHandler {
	allow := make(map[string]struct{}, len(operatorSubjects))
	for _, s := range operatorSubjects {
		if s != "" {
			allow[s] = struct{}{}
		}
	}
	if len(allow) == 0 || tenants == nil || usageStore == nil || receipts == nil {
		return nil
	}
	h := &OpsHandler{
		operators: allow,
		tenants:   tenants,
		usage:     usageStore,
		receipts:  receipts,
		decisions: decisions,
		posture:   postureReader,
		alerts:    alerts,
		logger:    slog.Default(),
		dispatch:  func(f func()) { go f() },
		now:       time.Now,
	}
	for _, opt := range opts {
		opt(h)
	}
	// Default to an in-memory atomic mutator over the handler's own
	// stores when no Postgres mutator was injected (tests / no-PG).
	// Needs the audit store to be meaningful.
	if h.mutator == nil && h.audit != nil {
		h.mutator = newMemOpsMutator(h.tenants, h.audit, h.alertQueue)
	}
	return h
}

// OpsOption configures optional OpsHandler dependencies.
type OpsOption func(*OpsHandler)

// WithOpsControlPlane wires the operator-audit store and the runtime
// resetter that the mutation routes (suspend/unsuspend, plan change)
// need. Without it, only the read routes are registered.
func WithOpsControlPlane(audit operatoraudit.Store, resetter RuntimeResetter) OpsOption {
	return func(h *OpsHandler) {
		h.audit = audit
		h.resetter = resetter
	}
}

// WithOpsBonusCredits lets an operator grant an account bonus credits,
// recorded in ledger as a bonus pack of credits.
func WithOpsBonusCredits(ledger packs.CreditLedger) OpsOption {
	return func(h *OpsHandler) { h.bonus = ledger }
}

// WithOpsAlertQueue wires the persisted operator alert queue (the operator-console design
// phase 3). Without it the /v1/ops/alerts routes are not registered.
func WithOpsAlertQueue(store operatoralerts.Store) OpsOption {
	return func(h *OpsHandler) { h.alertQueue = store }
}

// WithOpsSupport wires the support-ticket store so the operator
// triage routes (/v1/ops/support*) are registered. Without it
// those routes are not mounted. linkBase is the app origin used to
// build the ticket link in close-the-loop emails (empty → relative
// link); the email itself is sent only when WithOpsNotifications is
// also wired.
func WithOpsSupport(store support.Store, linkBase string) OpsOption {
	return func(h *OpsHandler) {
		h.support = store
		h.supportLinkBase = strings.TrimRight(strings.TrimSpace(linkBase), "/")
	}
}

// WithOpsMutator installs the atomic state-change+audit mutator.
// Production passes the Postgres mutator; when omitted the
// handler builds an in-memory mutator over its own stores (tests /
// no-Postgres), preserving the same "no mutation without audit"
// guarantee.
func WithOpsMutator(m OpsMutator) OpsOption {
	return func(h *OpsHandler) { h.mutator = m }
}

// WithOpsNotifications wires customer email notifications:
// on a successful suspend/unsuspend, resolve the owner email (Clerk,
// never stored) and send a customer-safe email best-effort. Both args
// required; when this option is absent the control actions run exactly
// as before with no email. A nil logger keeps slog.Default().
func WithOpsNotifications(resolver *owneremail.Resolver, notifier notify.Notifier, logger *slog.Logger) OpsOption {
	return func(h *OpsHandler) {
		h.ownerEmail = resolver
		h.notifier = notifier
		if logger != nil {
			h.logger = logger
		}
	}
}

// Register attaches the operator routes to mux. No-op on a nil
// handler so callers can register unconditionally.
func (h *OpsHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("GET /v1/ops/overview", h.gate(h.overview))
	mux.HandleFunc("GET /v1/ops/tenants", h.gate(h.tenantsList))
	mux.HandleFunc("GET /v1/ops/tenants/{id}", h.gate(h.tenantDetail))
	mux.HandleFunc("GET /v1/ops/activity", h.gate(h.activity))
	if h.runs != nil {
		mux.HandleFunc("GET /v1/ops/consumption", h.gate(h.consumption))
	}

	// Control-plane routes — only when the audit store +
	// resetter are wired. Mutations and the audit-log read.
	if h.audit != nil {
		mux.HandleFunc("GET /v1/ops/audit", h.gate(h.auditLog))
		mux.HandleFunc("POST /v1/ops/tenants/{id}/suspend", h.gate(h.suspend))
		mux.HandleFunc("POST /v1/ops/tenants/{id}/unsuspend", h.gate(h.unsuspend))
		mux.HandleFunc("POST /v1/ops/tenants/{id}/restore", h.gate(h.restore))
		mux.HandleFunc("POST /v1/ops/tenants/{id}/plan", h.gate(h.changePlan))
		mux.HandleFunc("POST /v1/ops/tenants/{id}/label", h.gate(h.setLabel))
		if h.bonus != nil {
			mux.HandleFunc("POST /v1/ops/tenants/{id}/bonus", h.gate(h.grantBonus))
		}
	}
	// Operator alert queue — only when an alert
	// store is wired.
	if h.alertQueue != nil {
		mux.HandleFunc("GET /v1/ops/alerts", h.gate(h.listAlerts))
		mux.HandleFunc("POST /v1/ops/alerts/sync", h.gate(h.syncAlerts))
		mux.HandleFunc("POST /v1/ops/alerts/{id}/ack", h.gate(h.ackAlert))
		mux.HandleFunc("POST /v1/ops/alerts/{id}/resolve", h.gate(h.resolveAlert))
	}
	// Support-ticket triage — only when a support store is
	// wired. Read the queue, read one ticket + thread, reply, set
	// status.
	if h.support != nil {
		mux.HandleFunc("GET /v1/ops/support", h.gate(h.listSupport))
		mux.HandleFunc("GET /v1/ops/support/{id}", h.gate(h.supportDetail))
		mux.HandleFunc("POST /v1/ops/support/{id}/reply", h.gate(h.supportReply))
		mux.HandleFunc("POST /v1/ops/support/{id}/status", h.gate(h.supportStatus))
	}
	h.registerProofs(mux)
}

// isOperator reports whether subject is in the allow-list. The single
// predicate every gate funnels through — when KIFF_OPS_SUBJECTS is
// replaced by a Clerk staff role or a cloud_meta.operators table
// (the hardening design hardening path), only this function changes.
func (h *OpsHandler) isOperator(subject string) bool {
	_, ok := h.operators[subject]
	return ok
}

// gate wraps an operator handler with the authorization check. A
// missing identity or a non-operator subject both yield 404 so the
// surface stays invisible to non-operators.
func (h *OpsHandler) gate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := auth.IdentityFrom(r.Context())
		if err != nil || !h.isOperator(id.Subject) {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}
