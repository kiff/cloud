package handlers

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strings"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/halts"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/proposals"
)

// activityResponse is GET /v1/me/activity: a page of calls, newest
// first, and how many calls each filter shows.
type activityResponse struct {
	Calls  []proposals.CallRow  `json:"calls"`
	Counts proposals.CallCounts `json:"counts"`
	// Shown is how many calls the requested filter selects in all, so a
	// page can say whether there is another.
	Shown int `json:"shown"`
	// Agents are every agent with a call in the account, whatever the
	// filter, for a page's agent picker.
	Agents []string `json:"agents"`
	// Halts are the owner's latest halts and resumes, newest first, each
	// with its receipt. On the first page only.
	Halts []halts.Event `json:"halts,omitempty"`
}

// MaxHaltEvents bounds the halts and resumes the Record shows.
const MaxHaltEvents = 50

// activity handles GET /v1/me/activity: every call the account's
// agents made, across every domain, one row a call. ?actor= narrows to
// one agent, ?proposal= to one call, ?show= to allowed, refused or held,
// ?limit= and ?offset= page. Only a person signed in may read it: no API key, tenant-wide or
// bound. An admin reads the whole account's; anyone else their own part of it (OwnUse, the shared-tafs design
// decision 12): every call of the agents they manage, and their own runs' calls.
func (h *ControlsHandler) activity(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	// A person, or a read-only account key (it reads what the admin who
	// minted it reads); never an agent's or a runtime's key.
	if ((id.APIKey || id.Method == auth.MethodAPIKey) && !id.ReadOnlyAccountKey()) || id.DomainID != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "human session required"})
		return
	}
	calls, ok := h.reader.(proposals.CallsReader)
	if !ok || h.reader == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "activity unavailable"})
		return
	}
	show := r.URL.Query().Get("show")
	switch show {
	case proposals.CallsAll, proposals.CallsAllowed, proposals.CallsRefused, proposals.CallsHeld:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid show"})
		return
	}
	q := proposals.CallQuery{
		Actor:    strings.TrimSpace(r.URL.Query().Get("actor")),
		Proposal: strings.TrimSpace(r.URL.Query().Get("proposal")),
		Show:     show,
		Limit:    parseLimit(r.URL.Query().Get("limit"), proposals.DefaultLimit, proposals.MaxLimit),
		// Not the evidence reader's 10,000 cap: the counts cover every
		// call, so every call is reachable.
		Offset: parseOffset(r.URL.Query().Get("offset"), math.MaxInt32),
	}
	scope, err := h.ownUse.Scope(r.Context(), id, id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "activity: reader's scope unreadable", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list activity failed"})
		return
	}
	q.Scope = scope.Calls()
	page, err := calls.ListCalls(r.Context(), id.Tenant, q)
	if err != nil {
		if clientLeft(r, err) {
			return // nobody is waiting for the answer
		}
		h.logger.ErrorContext(r.Context(), "activity: list calls failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list activity failed"})
		return
	}
	rows, counts := page.Calls, page.Counts
	// How each allowed call ended at the tool, as on the domain
	// evidence page. Best effort: a call without it reads as unknown.
	decisions := make([]proposals.DecisionRow, len(rows))
	for i := range rows {
		decisions[i] = rows[i].DecisionRow
	}
	h.withToolOutcomes(r.Context(), id.Tenant, decisions)
	h.withCallAbout(r.Context(), id.Tenant, decisions)
	var titles map[string]string
	if h.titles != nil {
		// Best effort, as for Cards: without them a row shows its action.
		titles, _ = h.titles.ToolTitles(r.Context(), id.Tenant)
	}
	var readOnly map[string]bool
	if ro, ok := h.titles.(GatewayToolsReadOnly); ok {
		// Best effort too: without it a read shows as any other call.
		readOnly, _ = ro.ReadOnlyTools(r.Context(), id.Tenant)
	}
	for i := range rows {
		rows[i].DecisionRow = decisions[i]
		rows[i].Title = titles[rows[i].ActionName]
		rows[i].ReadOnly = readOnly[rows[i].ActionName]
		if t := rows[i].ReceiptTraceID; t != "" {
			status := "pending"
			if rows[i].ReceiptSigned {
				status = "signed"
			}
			rows[i].Receipt = &proposals.CallReceipt{TraceID: t, Status: status}
		}
	}
	h.withAnswers(r.Context(), id.Tenant, id.ActorID(), rows)
	// Why a call wasn't sent, in the reader's words (kiff/tafs#138).
	lang := replyLanguage(r)
	for i := range rows {
		rows[i].Reason = callReasonIn(rows[i].Outcome, rows[i].Reasons, lang)
		withCallWords(&rows[i], wordsIn(lang))
	}
	resp := activityResponse{Calls: rows, Counts: counts, Agents: page.Agents}
	switch show {
	case proposals.CallsAllowed:
		resp.Shown = counts.Allowed
	case proposals.CallsRefused:
		resp.Shown = counts.Refused
	case proposals.CallsHeld:
		resp.Shown = counts.Held
	default:
		resp.Shown = counts.All
	}
	if resp.Calls == nil {
		resp.Calls = []proposals.CallRow{}
	}
	if resp.Agents == nil {
		resp.Agents = []string{}
	}
	// Halts are the account's, for those who read all of it.
	if h.halts != nil && q.Offset == 0 && scope.Whole() {
		if evs, err := h.halts.Events(r.Context(), id.Tenant, MaxHaltEvents); err == nil {
			resp.Halts = evs
		} else {
			h.logger.WarnContext(r.Context(), "activity: halts unreadable", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// GatewayToolsReadOnly reads which of an account's gateway tools are
// marked as only reading, by the name a Card grants them under. The
// tool titles reader may implement it.
type GatewayToolsReadOnly interface {
	ReadOnlyTools(ctx context.Context, tenantID string) (map[string]bool, error)
}

// HeldCallExceptions reads a held call's Card exception (mandate.Store).
type HeldCallExceptions interface {
	Exception(ctx context.Context, tenantID, id string) (mandate.Exception, error)
}

// HoldAnswers reads who answered holds (membership.Store).
type HoldAnswers interface {
	HoldAnswers(ctx context.Context, tenantID string, holdIDs []string) (map[string]membership.HoldAnswer, error)
}

// WithControlsHoldAnswers shows, on each answered held call, how it was
// answered, by whom and from which phone (kiff/tafs#167). answers may be
// nil: the answer then names no one and no phone.
func WithControlsHoldAnswers(holds HeldCallExceptions, answers HoldAnswers) ControlsOption {
	return func(h *ControlsHandler) { h.holds, h.answers = holds, answers }
}

// withAnswers sets Answer on the held calls a person approved once or
// refused, and whether reader (the caller's actor id) gave it. Best
// effort, like the tool outcomes: a call whose answer can't be read shows
// without one.
func (h *ControlsHandler) withAnswers(ctx context.Context, tenantID, reader string, rows []proposals.CallRow) {
	if h.holds == nil {
		return
	}
	held := map[string]mandate.Exception{}
	var ids []string
	for _, row := range rows {
		if !row.Held {
			continue
		}
		e, err := h.holds.Exception(ctx, tenantID, mandate.ExceptionID(tenantID, row.ProposalID))
		if err != nil {
			if !errors.Is(err, mandate.ErrExceptionNotFound) {
				h.logger.WarnContext(ctx, "activity: held call unavailable", slog.String("tenant", tenantID), slog.String("err", err.Error()))
			}
			continue
		}
		if answerDecision(e.Status) == "" || e.ResolvedAt == nil {
			continue
		}
		held[row.ProposalID] = e
		ids = append(ids, e.ID)
	}
	if len(ids) == 0 {
		return
	}
	answers := map[string]membership.HoldAnswer{}
	if h.answers != nil {
		if a, err := h.answers.HoldAnswers(ctx, tenantID, ids); err == nil {
			answers = a
		} else {
			h.logger.WarnContext(ctx, "activity: hold answers unavailable", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		}
	}
	for i := range rows {
		e, ok := held[rows[i].ProposalID]
		if !ok {
			continue
		}
		decision := answerDecision(e.Status)
		ans := &proposals.CallAnswer{Decision: decision, At: e.ResolvedAt.UTC()}
		// Only the answer this exception records: an earlier one the
		// person changed is not it.
		if a, ok := answers[e.ID]; ok && a.Answer == holdAnswerFor(decision) {
			ans.By = string(a.Role)
			ans.You = reader != "" && a.Answerer == reader
			if d := a.Device; d != nil {
				ans.Signed, ans.Device = true, d.Name
			}
		}
		rows[i].Answer = ans
	}
}

// answerDecision is how the activity names a held call's answer: approved
// once (used or not yet), or refused. "" for every other state: still
// held, expired, withdrawn, or settled by changing the Card.
func answerDecision(s mandate.ExceptionStatus) string {
	switch s {
	case mandate.ExceptionApproved, mandate.ExceptionConsumed:
		return "approved_once"
	case mandate.ExceptionRejected:
		return "refused"
	}
	return ""
}

// holdAnswerFor is the answer a person gave for decision, as hold answers
// record it.
func holdAnswerFor(decision string) string {
	if decision == "refused" {
		return "reject"
	}
	return "approve-once"
}
