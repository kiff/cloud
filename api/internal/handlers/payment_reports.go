package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/payreports"
	"github.com/kiff/cloud/api/internal/profile"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

// PaymentReportsHandler is "Report this payment": the owner, or an
// admin, reports a payment a taf made that they didn't expect.
//
//	POST /v1/receipts/{trace_id}/report
//
// The receipt is marked reported, the call's taf is halted with the
// owner's halt (planque confirms it, the approval windows end, the halt
// is on the Record with its own receipt), and a test account's prune keeps
// the receipt. Reporting stops things only, so it is one tap, as a halt
// is; resuming the taf stays behind Face ID. The request carries no body:
// a report keeps no free text. Reporting again answers the first report,
// and halts the taf again if it was resumed since.
type PaymentReportsHandler struct {
	receipts receipt.Store
	calls    proposals.CallsReader
	profiles GatewayToolProfiles
	outcomes tooloutcome.Reader // optional: without it, a call with a receipt counts as sent
	reports  payreports.Store
	halts    *HaltsHandler
	logger   *slog.Logger
	now      func() time.Time
}

// NewPaymentReportsHandler serves reports. outcomes may be nil.
func NewPaymentReportsHandler(receipts receipt.Store, calls proposals.CallsReader, profiles GatewayToolProfiles, outcomes tooloutcome.Reader,
	reports payreports.Store, halts *HaltsHandler, logger *slog.Logger) *PaymentReportsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &PaymentReportsHandler{receipts: receipts, calls: calls, profiles: profiles, outcomes: outcomes, reports: reports, halts: halts, logger: logger, now: time.Now}
}

func (h *PaymentReportsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/receipts/{trace_id}/report", h.report)
}

// paymentReportView is a report and where its taf stands now.
type paymentReportView struct {
	TraceID    string    `json:"trace_id"`
	ProposalID string    `json:"proposal_id"`
	Agent      string    `json:"agent"`
	ReportedAt time.Time `json:"reported_at"`
	Halt       haltView  `json:"halt"`
}

func (h *PaymentReportsHandler) report(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a session is required"})
		return
	}
	// A person who may halt the taf, as the halt route asks: never an
	// agent's key, nor a key of the account's.
	if id.AgentID != "" || id.APIKey || id.Method == auth.MethodAPIKey || !can(id, membership.CapCardsManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the account's owner or an admin can report a payment"})
		return
	}
	trace := r.PathValue("trace_id")
	rc, err := h.receipts.Get(r.Context(), id.Tenant, trace)
	if errors.Is(err, receipt.ErrReceiptNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such receipt"})
		return
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "payment reports: receipt unread", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the receipt could not be read"})
		return
	}
	call, ok := h.paymentCall(w, r, id.Tenant, rc)
	if !ok {
		return
	}
	// Only a taf KIFF knows made the call is halted: a call made with a
	// key not bound to it names its taf on the caller's word alone.
	agent, ok := mandate.TrialAgent(call.ActorID)
	if !ok {
		agent = call.ActorID
	}
	if n, err := agents.NormalizeID(agent); !call.ActorVerified || err != nil || n != agent {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "agent_unknown"})
		return
	}
	// The halt first: it is what protects the account, and a report that
	// fails after it leaves the taf halted, to report again.
	by := issuerOf(id)
	haltTrace, err := h.halts.haltNow(r.Context(), id.Tenant, agent, by)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the taf could not be halted: nothing was reported"})
		return
	}
	rep, err := h.reports.Report(r.Context(), payreports.Report{TenantID: id.Tenant, TraceID: rc.TraceID, ProposalID: call.ProposalID,
		AgentID: agent, ReportedAt: h.now().UTC(), ReportedBy: by})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "payment reports: report not kept", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the taf is halted, but the report could not be kept"})
		return
	}
	v, err := h.halts.view(r.Context(), id.Tenant, agent, haltTrace)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the taf is halted and the report kept, but the halt could not be read"})
		return
	}
	h.logger.InfoContext(r.Context(), "payment reported", slog.String("tenant", id.Tenant), slog.String("agent", agent), slog.String("trace", rc.TraceID))
	writeJSON(w, http.StatusOK, paymentReportView{TraceID: rc.TraceID, ProposalID: rep.ProposalID, Agent: rep.AgentID, ReportedAt: rep.ReportedAt, Halt: v})
}

// paymentCall is the call rc is the receipt of, when it is a payment: its
// tool moves money by its owner-approved profile, KIFF allowed it, and the
// tool didn't refuse it nor was it left unsent. Otherwise it answers.
func (h *PaymentReportsHandler) paymentCall(w http.ResponseWriter, r *http.Request, tenant string, rc receipt.Receipt) (proposals.CallRow, bool) {
	pid := rc.ProposalID
	if pid == "" {
		pid = rc.TraceID // receipts from before the explicit link carry the call's id as theirs
	}
	page, err := h.calls.ListCalls(r.Context(), tenant, proposals.CallQuery{Proposal: pid, Show: proposals.CallsAll, Limit: 1})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "payment reports: call unread", slog.String("tenant", tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the payment could not be read"})
		return proposals.CallRow{}, false
	}
	if len(page.Calls) == 0 || page.Calls[0].ProposalID != pid {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "not_a_payment"})
		return proposals.CallRow{}, false
	}
	call := page.Calls[0]
	profiles, err := h.profiles.ToolProfiles(r.Context(), tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "payment reports: profiles unread", slog.String("tenant", tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the payment could not be read"})
		return proposals.CallRow{}, false
	}
	if call.Outcome != "allowed" || !movesMoney(profiles, call.ActionName) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "not_a_payment"})
		return proposals.CallRow{}, false
	}
	if h.outcomes != nil {
		found, err := h.outcomes.Outcomes(r.Context(), tenant, []string{pid})
		if err != nil {
			h.logger.ErrorContext(r.Context(), "payment reports: outcome unread", slog.String("tenant", tenant), slog.String("err", err.Error()))
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the payment could not be read"})
			return proposals.CallRow{}, false
		}
		if o, ok := found[pid]; ok && !paid(o) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "not_a_payment"})
			return proposals.CallRow{}, false
		}
	}
	return call, true
}

// movesMoney is whether action's owner-approved profile says it moves
// money. No profile, or one that can't be read, says it doesn't.
func movesMoney(profiles map[string][]byte, action string) bool {
	raw, ok := profiles[action]
	if !ok || action == "" {
		return false
	}
	p, err := profile.Parse(raw)
	if err != nil {
		return false
	}
	for _, op := range p.Operations {
		if op.Tool == action {
			return op.Effect == profile.EffectMovesMoney
		}
	}
	return false
}

// paid is whether a call that ended so may have moved money: anything but
// the tool's refusal or a call never sent.
func paid(o tooloutcome.Outcome) bool {
	return o.Status != tooloutcome.RefusedByTool && o.Status != tooloutcome.NotSent
}
