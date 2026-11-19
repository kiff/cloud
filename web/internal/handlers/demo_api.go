package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// demoAPI is a deterministic, in-process implementation of the API reads the
// real dashboard uses. It never opens a socket and it never forwards a request.
type demoAPI struct {
	now time.Time
}

func newDemoAPI() *demoAPI {
	return &demoAPI{now: time.Date(2026, time.August, 15, 9, 42, 0, 0, time.UTC)}
}

func (a *demoAPI) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := newMemoryResponseWriter()
	a.ServeHTTP(rec, req)
	return &http.Response{
		StatusCode: rec.statusCode(),
		Status:     http.StatusText(rec.statusCode()),
		Header:     rec.header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(rec.body.Bytes())),
		Request:    req,
	}, nil
}

func (a *demoAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if r.Header.Get("Authorization") != "Bearer demo-session" {
		a.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	switch {
	case r.Method == http.MethodGet && path == "/v1/me":
		a.writeJSON(w, http.StatusOK, apiclient.Me{
			Subject: "demo-user", Email: "alex@northstar.example", TenantID: "tenant-demo",
			TenantSlug: "northstar-operations", DisplayName: "Northstar Operations",
			Roles: []string{"admin", "finance-approver"}, Plan: "team",
			CanonicalPlan: "team", MaxDomains: 10,
			// The demo is the full workspace.
			AccountKind: "company",
		})
	case r.Method == http.MethodGet && path == "/v1/me/domains":
		a.writeJSON(w, http.StatusOK, map[string]any{"domains": a.domains()})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/yaml") && strings.HasPrefix(path, "/v1/me/domains/"):
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = io.WriteString(w, demoDomainYAML)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/usage") && strings.HasPrefix(path, "/v1/me/domains/"):
		a.writeJSON(w, http.StatusOK, a.usage(3420))
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/evidence") && strings.HasPrefix(path, "/v1/me/domains/"):
		ev := a.evidence()
		if actor := r.URL.Query().Get("actor"); actor == "support-agent" {
			// A card page asks for its holder's decisions.
			ev.Decisions.Rows = a.cardDecisions()
		}
		a.writeJSON(w, http.StatusOK, ev)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/activity/series") && strings.HasPrefix(path, "/v1/me/domains/"):
		a.writeJSON(w, http.StatusOK, a.activitySeries())
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/me/domains/"):
		slug := strings.TrimPrefix(path, "/v1/me/domains/")
		a.writeJSON(w, http.StatusOK, a.domainDetail(slug))
	case r.Method == http.MethodPost && path == "/v1/me/domains":
		a.writeJSON(w, http.StatusCreated, a.domainSummary("new-operational-domain"))
	case (r.Method == http.MethodPut || r.Method == http.MethodDelete) && strings.HasPrefix(path, "/v1/me/domains/"):
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		a.writeJSON(w, http.StatusOK, a.domainSummary("accounts-payable"))
	case r.Method == http.MethodGet && path == "/v1/me/domain":
		a.writeJSON(w, http.StatusOK, a.domainSummary("accounts-payable"))
	case r.Method == http.MethodGet && path == "/v1/me/domain/draft":
		a.writeJSON(w, http.StatusOK, map[string]any{
			"yaml": demoDomainYAML, "updated_at": a.now.Unix(), "parsed": a.domainSummary("accounts-payable"),
		})
	case r.Method == http.MethodPut && path == "/v1/me/domain/draft":
		a.writeJSON(w, http.StatusOK, map[string]any{
			"yaml": demoDomainYAML, "updated_at": a.now.Unix(), "parsed": a.domainSummary("accounts-payable"),
		})
	case r.Method == http.MethodGet && path == "/v1/me/controls":
		a.writeJSON(w, http.StatusOK, map[string]any{"controls": a.controls()})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/me/controls/") && strings.HasSuffix(path, "/decisions"):
		a.writeJSON(w, http.StatusOK, a.evidence())
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/me/proof-pack/"):
		a.writeJSON(w, http.StatusOK, map[string]any{"demo": true, "domain": "accounts-payable", "receipts": 3388})
	case r.Method == http.MethodGet && path == "/v1/me/usage/counters":
		a.writeJSON(w, http.StatusOK, a.usage(3420))
	case r.Method == http.MethodGet && path == "/v1/me/usage":
		a.writeJSON(w, http.StatusOK, a.meters())
	case r.Method == http.MethodGet && path == "/v1/me/billing-budget":
		a.writeJSON(w, http.StatusOK, map[string]int{"budget_ops": 80000})
	case r.Method == http.MethodGet && path == "/v1/me/mandates":
		a.writeJSON(w, http.StatusOK, map[string]any{"mandates": a.mandates()})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/me/mandates/") && strings.HasSuffix(path, "/statement"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/v1/me/mandates/"), "/statement")
		statement, ok := a.mandateStatement(id)
		if !ok {
			a.writeJSON(w, http.StatusNotFound, map[string]string{"error": "card not found"})
			return
		}
		a.writeJSON(w, http.StatusOK, statement)
	case r.Method == http.MethodGet && path == "/v1/templates":
		a.writeJSON(w, http.StatusOK, map[string]any{"templates": a.templates()})
	case r.Method == http.MethodPost && path == "/v1/me/domain/template":
		a.writeJSON(w, http.StatusOK, map[string]any{"slug": "card-refund", "name": "Refund on an issued card", "tenant": "tenant-demo", "version": 1})
	case r.Method == http.MethodGet && path == "/v1/me/agents":
		a.writeJSON(w, http.StatusOK, map[string]any{"agents": a.agents()})
	case r.Method == http.MethodGet && path == "/v1/keys":
		a.writeJSON(w, http.StatusOK, map[string]any{"keys": a.keys()})
	case r.Method == http.MethodPost && path == "/v1/keys":
		a.writeJSON(w, http.StatusCreated, map[string]any{"id": "key-demo-new", "tenant": "tenant-demo", "subject": "demo-user", "label": "demo key", "roles": []string{"admin"}, "plaintext": "kiff_demo_not_a_real_key"})
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/v1/keys/"):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && path == "/v1/guard/connections":
		a.writeJSON(w, http.StatusOK, map[string]any{"connections": a.connections()})
	case r.Method == http.MethodGet && path == "/v1/guard/observations":
		a.writeJSON(w, http.StatusOK, map[string]any{"observation": a.observation()})
	case r.Method == http.MethodGet && path == "/v1/guard/candidates":
		a.writeJSON(w, http.StatusOK, map[string]any{"source": "kiff-guard runtime", "runtimes": a.candidates()})
	case r.Method == http.MethodGet && path == "/v1/proposals":
		rows := a.decisions()
		a.writeJSON(w, http.StatusOK, map[string]any{"proposals": rows, "last_received_at": a.now.Add(-2 * time.Minute)})
	case r.Method == http.MethodPost && path == "/v1/proposals/decide":
		a.writeJSON(w, http.StatusOK, map[string]any{"proposal_id": "proposal-demo-new", "outcome": "approval_required", "reasons": []string{"amount exceeds automatic limit"}})
	case r.Method == http.MethodGet && path == "/v1/approvals":
		a.writeJSON(w, http.StatusOK, map[string]any{"approvals": a.approvals(), "last_received_at": a.now.Add(-4 * time.Minute)})
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/v1/approvals/"):
		a.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case r.Method == http.MethodGet && path == "/v1/receipts":
		a.writeJSON(w, http.StatusOK, map[string]any{"receipts": a.receipts()})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/receipts/"):
		a.writeJSON(w, http.StatusOK, a.receipt(strings.TrimPrefix(path, "/v1/receipts/")))
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/entities/") && strings.HasSuffix(path, "/timeline"):
		a.writeJSON(w, http.StatusOK, map[string]any{"timeline": a.timeline()})
	case r.Method == http.MethodPost && strings.Contains(path, "/actions/"):
		a.writeJSON(w, http.StatusOK, map[string]any{"trace_id": "trace-demo-action", "outcome": "allowed"})
	case r.Method == http.MethodPost && path == "/v1/events/raw":
		a.writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
	case (r.Method == http.MethodGet || r.Method == http.MethodPut) && path == "/v1/me/notification-prefs":
		a.writeJSON(w, http.StatusOK, map[string]any{"prefs": map[string]bool{"approval.pending": true, "runtime.idle": true}})
	case r.Method == http.MethodGet && path == "/v1/me/support/tickets":
		a.writeJSON(w, http.StatusOK, map[string]any{"tickets": a.tickets()})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/me/support/tickets/"):
		a.writeJSON(w, http.StatusOK, a.ticketDetail())
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/v1/me/support/tickets"):
		a.writeJSON(w, http.StatusCreated, a.tickets()[0])
	case r.Method == http.MethodGet && path == "/v1/me/export":
		a.writeJSON(w, http.StatusOK, map[string]any{"demo": true, "workspace": "northstar-operations"})
	default:
		a.writeJSON(w, http.StatusNotFound, map[string]string{"error": "demo fixture has no route for " + r.Method + " " + path})
	}
}

func (a *demoAPI) mandates() []apiclient.Mandate {
	validUntil := a.now.AddDate(0, 6, 0)
	return []apiclient.Mandate{
		{
			ID: "card-refunds-eu", Subject: "support-agent", Domain: "card-refund", Environment: "production", Active: true,
			Grants:     []apiclient.MandateGrant{{Action: "REFUND_ORDER", EntityStates: []string{"PAID"}}},
			Aggregates: []apiclient.MandateAggregate{{Quantity: "sum(amount_cents)", Limit: 500000, Window: "calendar_day"}},
			ValidUntil: &validUntil,
		},
		{
			ID: "card-store-credits", Subject: "support-agent", Domain: "retention-credit", Environment: "production", Active: true,
			Grants:     []apiclient.MandateGrant{{Action: "ISSUE_STORE_CREDIT", EntityStates: []string{"ELIGIBLE"}}},
			Aggregates: []apiclient.MandateAggregate{{Quantity: "sum(amount_cents)", Limit: 200000, Window: "calendar_day"}},
			ValidUntil: &validUntil,
		},
		{
			ID: "card-plan-changes", Subject: "support-agent", Domain: "subscription-plan-change", Environment: "production", Active: true,
			Grants:     []apiclient.MandateGrant{{Action: "CHANGE_PLAN", EntityStates: []string{"ACTIVE"}}},
			Aggregates: []apiclient.MandateAggregate{{Quantity: "count", Limit: 40, Window: "calendar_day"}},
			ValidUntil: &validUntil,
		},
	}
}

func (a *demoAPI) mandateStatement(id string) (apiclient.MandateStatement, bool) {
	for _, mandate := range a.mandates() {
		if mandate.ID != id {
			continue
		}
		limit := mandate.Aggregates[0]
		used := int64(9)
		amount := int64(1)
		action := "CHANGE_PLAN"
		entity := "sub-4471"
		if id == "card-refunds-eu" {
			used = 400000
			amount = 100000
			action = "REFUND_ORDER"
			entity = "ord-8812"
		} else if id == "card-store-credits" {
			used = 75000
			amount = 25000
			action = "ISSUE_STORE_CREDIT"
			entity = "case-2048"
		}
		windowStarted := time.Date(a.now.Year(), a.now.Month(), a.now.Day(), 0, 0, 0, 0, time.UTC)
		return apiclient.MandateStatement{
			Mandate: mandate,
			Limits: []apiclient.MandateLimit{{
				Quantity: limit.Quantity, Limit: limit.Limit, Window: limit.Window,
				Used: used, Remaining: limit.Limit - used, WindowStarted: &windowStarted, Status: "ok",
			}},
			Draws: []apiclient.MandateDraw{{
				ProposalID: "proposal-demo-" + id, Quantity: limit.Quantity, Amount: amount,
				DrawnAt: a.now.Add(-20 * time.Minute), Action: action, EntityID: entity, Effect: "authorized",
				DrawnTotals: []apiclient.MandateDrawTotal{{Window: limit.Window, DrawnTotal: used}},
			}},
			Note:      "A draw records authority consumed when KIFF authorizes an action. Execution remains in the caller's system.",
			Revisions: []apiclient.MandateRevision{{Revision: 1, ChangedAt: a.now.AddDate(0, 0, -7), ChangedBy: "alex@northstar.example", Aggregates: mandate.Aggregates, ValidUntil: mandate.ValidUntil}},
		}, true
	}
	return apiclient.MandateStatement{}, false
}

func (a *demoAPI) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (a *demoAPI) domains() []apiclient.DomainListItem {
	active := a.now.AddDate(0, -2, 0)
	return []apiclient.DomainListItem{
		{Name: "accounts-payable", Entity: "Invoice", Version: 7, Status: "active", ActionCount: 3, ActivatedAt: &active, Posture: "enforce"},
		{Name: "card-refund", Entity: "Order", Version: 4, Status: "active", ActionCount: 2, ActivatedAt: &active, Posture: "observe"},
		{Name: "retention-credit", Entity: "RetentionCase", Version: 1, Status: "active", ActionCount: 2, ActivatedAt: &active, Posture: "observe"},
		{Name: "subscription-plan-change", Entity: "Subscription", Version: 1, Status: "active", ActionCount: 2, ActivatedAt: &active, Posture: "observe"},
	}
}

func (a *demoAPI) domainSummary(slug string) apiclient.Domain {
	return apiclient.Domain{Domain: slug, Entity: "Invoice", Version: 7,
		Events: []string{"invoice_received", "approval_granted", "payment_completed"},
		States: []string{"received", "approved", "paid", "rejected"},
		Actions: []apiclient.DomainAction{
			{Name: "PAY_INVOICE", AllowedStates: []string{"approved"}, RequiredParameters: []string{"amount", "currency"}, Risk: "high", Approval: "finance_manager", Executor: "erp.pay_invoice"},
			{Name: "REQUEST_APPROVAL", AllowedStates: []string{"received"}, Risk: "medium", Executor: "workflow.request_approval"},
			{Name: "REJECT_INVOICE", AllowedStates: []string{"received", "approved"}, Risk: "medium", Executor: "erp.reject_invoice"},
		}}
}

func (a *demoAPI) domainDetail(slug string) apiclient.DomainDetail {
	if slug == "customer-refunds" {
		active := a.now.AddDate(0, -1, -8)
		return apiclient.DomainDetail{Name: slug, Entity: "Order", Version: 4, Status: "active", ActivatedAt: &active, Posture: "observe",
			Actions:   []apiclient.DomainAction{{Name: "ISSUE_REFUND", AllowedStates: []string{"paid"}, Risk: "high", Approval: "support_lead"}, {Name: "CANCEL_ORDER", AllowedStates: []string{"created"}, Risk: "medium"}},
			Lifecycle: apiclient.DomainLifecycle{States: []string{"created", "paid", "refunded", "cancelled"}, Events: []string{"payment_captured", "refund_issued"}, Transitions: []apiclient.DomainTransition{{On: "payment_captured", From: "created", To: "paid"}, {On: "refund_issued", From: "paid", To: "refunded"}}},
			Agents:    apiclient.DomainAgentsSection{Status: "observed", Agents: []apiclient.DomainAgent{{ID: "support-refund-agent", Kind: "actor", Operations: 284, LastSeen: a.now.Add(-14 * time.Minute)}}},
			Evidence:  apiclient.DomainEvidence{ControlID: "ctrl-refunds"}}
	}
	active := a.now.AddDate(0, -2, 0)
	return apiclient.DomainDetail{Name: "accounts-payable", Entity: "Invoice", Version: 7, Status: "active", ActivatedAt: &active, Posture: "enforce", Actions: a.domainSummary("accounts-payable").Actions,
		Lifecycle: apiclient.DomainLifecycle{States: []string{"received", "approved", "paid", "rejected"}, Events: []string{"invoice_received", "approval_granted", "payment_completed"}, Transitions: []apiclient.DomainTransition{{On: "approval_granted", From: "received", To: "approved"}, {On: "payment_completed", From: "approved", To: "paid"}}},
		Agents:    apiclient.DomainAgentsSection{Status: "observed", Agents: []apiclient.DomainAgent{{ID: "ap-payment-agent", Kind: "actor", Operations: 2108, LastSeen: a.now.Add(-2 * time.Minute)}, {ID: "invoice-review-service", Kind: "api_key", Operations: 1312, LastSeen: a.now.Add(-7 * time.Minute)}}},
		Evidence:  apiclient.DomainEvidence{ControlID: "ctrl-ap-pay"}}
}

func (a *demoAPI) controls() []apiclient.Control {
	return []apiclient.Control{{ID: "ctrl-ap-pay", Domain: "accounts-payable", Entity: "Invoice", Actions: []string{"PAY_INVOICE", "REQUEST_APPROVAL", "REJECT_INVOICE"}, Environment: "production", Workflow: "invoice-to-pay", ActivatedAt: a.now.AddDate(0, -2, 0), ActivatedBy: "alex@northstar.example", Status: "active"}, {ID: "ctrl-refunds", Domain: "customer-refunds", Entity: "Order", Actions: []string{"ISSUE_REFUND", "CANCEL_ORDER"}, Environment: "production", Workflow: "customer-resolution", ActivatedAt: a.now.AddDate(0, -1, -8), ActivatedBy: "alex@northstar.example", Status: "active"}}
}

func (a *demoAPI) decisions() []apiclient.ProposalDecision {
	return []apiclient.ProposalDecision{
		{ProposalID: "prop-3420", EntityID: "inv-2026-0842", EntityType: "Invoice", ActionName: "PAY_INVOICE", ControlID: "ctrl-ap-pay", Environment: "production", ActorID: "ap-payment-agent", Outcome: "approval_required", Reasons: []string{"amount exceeds automatic payment limit"}, CreatedAt: a.now.Add(-4 * time.Minute), StateAtDecision: "approved", AllowedStates: []string{"approved"}, ReceiptTraceID: "trace-pay-3420", ReceiptStatus: "signed"},
		{ProposalID: "prop-3419", EntityID: "inv-2026-0839", EntityType: "Invoice", ActionName: "PAY_INVOICE", ControlID: "ctrl-ap-pay", Environment: "production", ActorID: "ap-payment-agent", Outcome: "allowed", CreatedAt: a.now.Add(-11 * time.Minute), StateAtDecision: "approved", AllowedStates: []string{"approved"}, ReceiptTraceID: "trace-pay-3419", ReceiptStatus: "signed"},
		{ProposalID: "prop-3418", EntityID: "inv-2026-0831", EntityType: "Invoice", ActionName: "PAY_INVOICE", ControlID: "ctrl-ap-pay", Environment: "production", ActorID: "invoice-review-service", Outcome: "blocked", Reasons: []string{"invoice already paid"}, CreatedAt: a.now.Add(-18 * time.Minute), StateAtDecision: "paid", AllowedStates: []string{"approved"}, ReceiptTraceID: "trace-pay-3418", ReceiptStatus: "signed"},
	}
}

// cardDecisions is the card holder's side of the demo: the draws on the
// cards' statements, plus refusals that drew nothing, each with the
// receipt it was signed into.
func (a *demoAPI) cardDecisions() []apiclient.ProposalDecision {
	return []apiclient.ProposalDecision{
		{ProposalID: "proposal-demo-refund-refused", EntityID: "ord-8820", EntityType: "Order", ActionName: "REFUND_ORDER", ActorID: "support-agent", PrincipalAPIKeyID: "key-support-agent", ActorVerified: true, Outcome: "blocked", Reasons: []string{"mandate_limit_reached"}, CreatedAt: a.now.Add(-6 * time.Minute), ReceiptTraceID: "decision-proposal-demo-refund-refused", ReceiptStatus: "pending"},
		{ProposalID: "proposal-demo-card-refunds-eu", EntityID: "ord-8812", EntityType: "Order", ActionName: "REFUND_ORDER", ActorID: "support-agent", PrincipalAPIKeyID: "key-support-agent", ActorVerified: true, Outcome: "allowed", CreatedAt: a.now.Add(-20 * time.Minute), ReceiptTraceID: "decision-proposal-demo-card-refunds-eu", ReceiptStatus: "pending"},
		{ProposalID: "proposal-demo-card-store-credits", EntityID: "case-2048", EntityType: "RetentionCase", ActionName: "ISSUE_STORE_CREDIT", ActorID: "support-agent", PrincipalAPIKeyID: "key-support-agent", ActorVerified: true, Outcome: "allowed", CreatedAt: a.now.Add(-20 * time.Minute), ReceiptTraceID: "decision-proposal-demo-card-store-credits", ReceiptStatus: "pending"},
		{ProposalID: "proposal-demo-card-plan-changes", EntityID: "sub-4471", EntityType: "Subscription", ActionName: "CHANGE_PLAN", ActorID: "support-agent", PrincipalAPIKeyID: "key-support-agent", ActorVerified: true, Outcome: "allowed", CreatedAt: a.now.Add(-20 * time.Minute), ReceiptTraceID: "decision-proposal-demo-card-plan-changes", ReceiptStatus: "pending"},
	}
}

// agents joins the demo's names the way GET /v1/me/agents does.
func (a *demoAPI) agents() []apiclient.Agent {
	added := a.now.AddDate(0, 0, -7)
	return []apiclient.Agent{
		{ID: "support-agent", Recorded: true, CreatedAt: &added, Sources: []string{"record", "card", "key", "runtime"}},
		{ID: "ap-payment-agent", Sources: []string{"runtime"}},
		{ID: "support-refund-agent", Sources: []string{"runtime"}},
	}
}

func (a *demoAPI) evidence() apiclient.ControlDecisionsResponse {
	rows := a.decisions()
	return apiclient.ControlDecisionsResponse{
		Control:   apiclient.ControlDecisionsControl{ID: "ctrl-ap-pay", Domain: "accounts-payable", Entity: "Invoice", Action: "PAY_INVOICE", Actions: []string{"PAY_INVOICE", "REQUEST_APPROVAL", "REJECT_INVOICE"}, Environment: "production", Workflow: "invoice-to-pay", Status: "active", Mode: "enforce", ActivatedAt: a.now.AddDate(0, -2, 0), ActivatedBy: "alex@northstar.example", Posture: &apiclient.ControlDecisionsPosture{Current: "enforce"}},
		Window:    apiclient.ControlDecisionsWindow{From: a.now.AddDate(0, -1, 0), To: a.now},
		Decisions: apiclient.ControlDecisions{Total: 3420, Counts: map[string]int{"allowed": 3310, "approval_required": 78, "blocked": 32}, Rows: rows},
		ByAction:  []apiclient.ActionCountsView{{Action: "PAY_INVOICE", Total: 2210, Counts: map[string]int{"allowed": 2110, "approval_required": 72, "blocked": 28}}, {Action: "REQUEST_APPROVAL", Total: 810, Counts: map[string]int{"allowed": 806, "blocked": 4}}, {Action: "REJECT_INVOICE", Total: 400, Counts: map[string]int{"allowed": 394, "approval_required": 6}}},
		Receipts:  apiclient.ControlDecisionsReceipts{Status: "complete", Count: 3420, SignedCount: 3420, Verifiability: apiclient.ControlDecisionsVerifiability{SignedAlgorithm: "ed25519", SignedCount: 3420, Anchored: "none", Note: "Every decision in this demo carries a signed receipt.", Scope: "window"}, Samples: []apiclient.ReceiptSampleView{{TraceID: "trace-pay-3420", ProposalID: "prop-3420", EntityID: "inv-2026-0842", ActionName: "PAY_INVOICE", IssuedAt: a.now.Add(-4 * time.Minute), Signed: true, Kind: "decision"}}},
		Alerts:    apiclient.ControlDecisionsAlerts{Status: "complete", Deliveries: []apiclient.AlertDeliveryView{{ProposalID: "prop-3420", TargetID: "finance-approvers", TargetType: "email", Status: "delivered", AttemptedAt: a.now.Add(-3 * time.Minute)}}},
		Approvals: apiclient.ControlDecisionsSection{Status: "complete"},
	}
}

func (a *demoAPI) activitySeries() apiclient.ActivitySeriesResponse {
	return apiclient.ActivitySeriesResponse{Domain: "accounts-payable", ControlID: "ctrl-ap-pay", From: a.now.Add(-6 * time.Hour), To: a.now, BucketSeconds: 3600, Buckets: []apiclient.ActivityBucket{{Start: a.now.Add(-5 * time.Hour), Counts: map[string]int{"allowed": 412, "blocked": 3}, Total: 415}, {Start: a.now.Add(-4 * time.Hour), Counts: map[string]int{"allowed": 538, "approval_required": 14, "blocked": 6}, Total: 558}, {Start: a.now.Add(-3 * time.Hour), Counts: map[string]int{"allowed": 601, "approval_required": 18, "blocked": 8}, Total: 627}, {Start: a.now.Add(-2 * time.Hour), Counts: map[string]int{"allowed": 566, "approval_required": 21, "blocked": 7}, Total: 594}, {Start: a.now.Add(-time.Hour), Counts: map[string]int{"allowed": 471, "approval_required": 15, "blocked": 5}, Total: 491}}}
}

// meters is the demo workspace's tasks and actions: a Team of three.
func (a *demoAPI) meters() map[string]any {
	free := map[string]int{"refused": 32, "held_refused": 4, "halted": 0, "failed": 6, "invalid": 1, "retry": 0, "read": 1210, "trial": 3, "setup": 9}
	return map[string]any{
		"plan": "team", "seats": 3,
		"period":  map[string]any{"start": time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "end": time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		"tasks":   map[string]any{"counted": 46, "included": 60, "ceiling": 80, "ceiling_reached": false, "by_agent": []any{}, "free": free},
		"actions": map[string]any{"counted": 980, "included": 1500, "free": free},
		"trial":   nil,
	}
}

func (a *demoAPI) usage(submitted uint64) apiclient.Usage {
	return apiclient.Usage{TenantID: "tenant-demo", Plan: "team", From: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Counters: map[string]uint64{"kiff.cloud.proposals.submitted": submitted, "kiff.cloud.proposals.allowed": 3310, "kiff.cloud.proposals.blocked": 32}}
}

func (a *demoAPI) templates() []apiclient.Template {
	return []apiclient.Template{
		{Slug: "duplicate-payment", Name: "Duplicate payment", Description: "Prevent repeated invoice payments across agents and retries.", SampleEvent: json.RawMessage(`{"entity_id":"inv-2026-0842","event":"invoice_received"}`)},
		{Slug: "card-refund", Name: "Refund on an issued card", Description: "Authorize paid-order refunds against a dedicated balance.", SampleEvent: json.RawMessage(`{"entity_id":"ord-8812","event":"order_paid"}`)},
		{Slug: "retention-credit", Name: "Retention credit", Description: "Authorize eligible store credits against their own balance.", SampleEvent: json.RawMessage(`{"entity_id":"case-2048","event":"case_marked_eligible"}`)},
		{Slug: "subscription-plan-change", Name: "Subscription plan change", Description: "Authorize active-subscription changes against a count limit.", SampleEvent: json.RawMessage(`{"entity_id":"sub-4471","event":"subscription_activated"}`)},
	}
}

func (a *demoAPI) keys() []apiclient.KeyListItem {
	lastUsed := a.now.Add(-2 * time.Minute).Format(time.RFC3339)
	return []apiclient.KeyListItem{{ID: "key-support-agent", Subject: "demo-user", Label: "support agent runtime", Roles: []string{"support"}, AgentID: "support-agent", CreatedAt: a.now.AddDate(0, 0, -7).Format(time.RFC3339), LastUsedAt: &lastUsed}, {ID: "key-prod-agent", Subject: "ap-payment-agent", Label: "production runtime", Roles: []string{"agent"}, CreatedAt: a.now.AddDate(0, -2, 0).Format(time.RFC3339), LastUsedAt: &lastUsed}, {ID: "key-ops-service", Subject: "invoice-review-service", Label: "invoice review service", Roles: []string{"agent"}, CreatedAt: a.now.AddDate(0, -1, -12).Format(time.RFC3339), LastUsedAt: &lastUsed}}
}

func (a *demoAPI) connections() []apiclient.GuardConnection {
	return []apiclient.GuardConnection{{TenantID: "tenant-demo", Project: "support-automation", Environment: "production", AgentID: "support-agent", Workflow: "customer-resolution", Adapter: "agno", SDKVersion: "1.1.0", Mode: "enforce", KeyID: "key-support-agent", FirstSeenAt: a.now.AddDate(0, 0, -7), LastSeenAt: a.now.Add(-40 * time.Second), SeenCount: 10080}, {TenantID: "tenant-demo", Project: "finance-operations", Environment: "production", AgentID: "ap-payment-agent", Workflow: "invoice-to-pay", Adapter: "openai-agents", SDKVersion: "0.4.2", Mode: "enforce", KeyID: "key-prod-agent", FirstSeenAt: a.now.AddDate(0, -2, 0), LastSeenAt: a.now.Add(-2 * time.Minute), SeenCount: 3420}, {TenantID: "tenant-demo", Project: "support-automation", Environment: "production", AgentID: "support-refund-agent", Workflow: "customer-resolution", Adapter: "langgraph", SDKVersion: "0.4.2", Mode: "observe", FirstSeenAt: a.now.AddDate(0, -1, -8), LastSeenAt: a.now.Add(-14 * time.Minute), SeenCount: 284}}
}

func (a *demoAPI) observation() apiclient.GuardObservation {
	return apiclient.GuardObservation{TenantID: "tenant-demo", Project: "finance-operations", Environment: "production", AgentID: "ap-payment-agent", Workflow: "invoice-to-pay", ObservedAt: a.now.AddDate(0, -2, 0), UpdatedAt: a.now.Add(-2 * time.Minute), Tools: []apiclient.GuardToolObservation{{Name: "pay_invoice", Description: "Pay an approved supplier invoice", EntityArg: "invoice_id", Action: "PAY_INVOICE", EntityType: "Invoice", Required: []string{"invoice_id", "amount", "currency"}, ObservedCallCount: 2210}}}
}

func (a *demoAPI) candidates() []apiclient.GuardRuntimeCandidates {
	return []apiclient.GuardRuntimeCandidates{{Runtime: apiclient.GuardRuntimeRef{AgentID: "ap-payment-agent", Adapter: "openai-agents", Project: "finance-operations", Environment: "production", Workflow: "invoice-to-pay", Mode: "enforce"}, Candidates: []apiclient.GuardCandidate{{Tool: "pay_invoice", Action: "PAY_INVOICE", EntityType: "Invoice", EntityArg: "invoice_id", Fields: []apiclient.GuardCandidateField{{Name: "amount", Type: "number", Required: true}, {Name: "currency", Type: "string", Required: true}}, Risk: "high", RiskReasons: []string{"moves company funds"}, Evidence: apiclient.GuardCandidateEvidence{Source: "kiff-guard runtime", Tool: "pay_invoice", Adapter: "openai-agents", Project: "finance-operations", Environment: "production", Workflow: "invoice-to-pay", Mode: "enforce"}, ObservedCallCount: 2210, PolicyThresholds: json.RawMessage("null"), States: json.RawMessage("null")}}, Grounding: apiclient.GuardGroundingReport{OK: true, Checks: []apiclient.GuardCandidateCheck{{Name: "runtime_observed", Pass: true}, {Name: "tool_schema_present", Pass: true}}}}}
}

func (a *demoAPI) approvals() []apiclient.PendingApproval {
	return []apiclient.PendingApproval{{ID: "approval-842", EntityID: "inv-2026-0842", EntityType: "Invoice", ActionName: "PAY_INVOICE", Status: "pending", RequestedBy: "ap-payment-agent", Reason: "€18,420 exceeds the €10,000 automatic limit", CreatedAt: a.now.Add(-4 * time.Minute)}, {ID: "approval-839", EntityID: "inv-2026-0839", EntityType: "Invoice", ActionName: "PAY_INVOICE", Status: "pending", RequestedBy: "ap-payment-agent", Reason: "new supplier bank account", CreatedAt: a.now.Add(-11 * time.Minute)}, {ID: "approval-831", EntityID: "order-2026-831", EntityType: "Order", ActionName: "ISSUE_REFUND", Status: "pending", RequestedBy: "support-refund-agent", Reason: "refund above support threshold", CreatedAt: a.now.Add(-18 * time.Minute)}}
}

func (a *demoAPI) receipts() []apiclient.Receipt {
	return []apiclient.Receipt{a.receipt("trace-pay-3420"), a.receipt("trace-pay-3419"), a.receipt("trace-pay-3418")}
}

func (a *demoAPI) receipt(traceID string) apiclient.Receipt {
	return apiclient.Receipt{SchemaVersion: "1.0", TraceID: traceID, TenantID: "tenant-demo", EntityID: "inv-2026-0842", EntityType: "Invoice", ActionName: "PAY_INVOICE", DomainID: "accounts-payable", Outcome: "approval_required", IssuedAt: a.now.Add(-4 * time.Minute), CausalChain: []apiclient.ChainStep{{Sequence: 1, Kind: "proposal_received", ActorID: "ap-payment-agent", OccurredAt: a.now.Add(-4 * time.Minute), Message: "Agent proposed PAY_INVOICE", DataHash: "sha256:demo-proposal"}, {Sequence: 2, Kind: "decision_recorded", ActorID: "kiff", OccurredAt: a.now.Add(-4*time.Minute + time.Second), Message: "Approval required", DataHash: "sha256:demo-decision"}}, Attestation: &apiclient.Attestation{Algorithm: "ed25519", Signature: "demo-signature-not-production", WalletAddress: "", Network: ""}}
}

func (a *demoAPI) timeline() []apiclient.TimelineEntry {
	return []apiclient.TimelineEntry{{Kind: "event", OccurredAt: a.now.Add(-8 * time.Minute), TraceID: "trace-pay-3420", Message: "Invoice approved", Data: map[string]any{"state": "approved"}}, {Kind: "proposal_decision", OccurredAt: a.now.Add(-4 * time.Minute), TraceID: "trace-pay-3420", Message: "Approval required for PAY_INVOICE", Data: map[string]any{"outcome": "approval_required"}}}
}

func (a *demoAPI) tickets() []apiclient.SupportTicket {
	return []apiclient.SupportTicket{{ID: "ticket-demo-1", AuthorSubject: "demo-user", AuthorEmail: "alex@northstar.example", Title: "Add a second approval group", Body: "How should we route invoices by business unit?", Category: "configuration", Status: "answered", CreatedAt: a.now.AddDate(0, 0, -3), UpdatedAt: a.now.AddDate(0, 0, -2)}}
}

func (a *demoAPI) ticketDetail() apiclient.SupportTicketDetail {
	return apiclient.SupportTicketDetail{Ticket: a.tickets()[0], Messages: []apiclient.SupportMessage{{ID: "message-demo-1", TicketID: "ticket-demo-1", AuthorKind: "operator", Body: "Use one authority rule per business unit, then map each reviewer group to its role.", CreatedAt: a.now.AddDate(0, 0, -2)}}}
}

type memoryResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newMemoryResponseWriter() *memoryResponseWriter {
	return &memoryResponseWriter{header: make(http.Header)}
}
func (w *memoryResponseWriter) Header() http.Header { return w.header }
func (w *memoryResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *memoryResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}
func (w *memoryResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

const demoDomainYAML = `domain: accounts-payable
entity: Invoice
states: [received, approved, paid, rejected]
events: [invoice_received, approval_granted, payment_completed]
transitions:
  - on: approval_granted
    from: received
    to: approved
  - on: payment_completed
    from: approved
    to: paid
actions:
  - name: PAY_INVOICE
    allowed_states: [approved]
    required_parameters: [amount, currency]
    required_permissions: [accounts-payable.pay_invoice]
    risk: high
    approval: finance_manager
    executor: erp.pay_invoice
  - name: REQUEST_APPROVAL
    allowed_states: [received]
    risk: medium
    executor: workflow.request_approval
  - name: REJECT_INVOICE
    allowed_states: [received, approved]
    risk: medium
    executor: erp.reject_invoice
permissions:
  roles:
    finance_manager:
      - accounts-payable.pay_invoice
`
