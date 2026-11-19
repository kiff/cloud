package handlers

import (
	"strings"

	"github.com/kiff/cloud/api/internal/cardwords"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/tooloutcome"
	"github.com/kiff/cloud/api/internal/words"
)

// withCallWords says a call of the account's activity in the reader's
// words: its name, its kind, what came of it and its
// amount, the wording the tafs apps used (KIFFMapping.record, executed).
// KIFF's decision and what the tool did are kept apart: a call is done
// only when the tool said so, and no outcome never reads as done.
func withCallWords(row *proposals.CallRow, w *words.Words) {
	row.Name = cardwords.Name(row.ActionName, row.Title)
	if row.Amount != nil {
		row.AmountText = cardwords.MoneyDecimal(row.Amount.Value, row.Amount.Unit, w)
	}
	withoutConnectionText(row)
	switch row.Outcome {
	case "allowed":
		row.Kind, row.Detail = callExecuted(row, w)
	case "owner_required", "approval_required":
		// Answered, and KIFF hasn't yet recorded what came of it: the line
		// says the answer, so no app keeps its own until it does (item 17).
		if a := row.Answer; a != nil {
			row.Kind, row.Detail = answeredWords(a.Decision, answerer(a, w), w)
			return
		}
		row.Kind, row.Detail = "held", w.WaitingForYou
	default:
		row.Kind, row.Detail = "refused", w.NotSent
		if row.Reason != "" {
			row.Detail = w.NotSentBecause(row.Reason)
		}
	}
}

// answerer is who answered a held call, as the reader reads it: "by you",
// or by their role on the account, with w's "by" ("pelo proprietário").
func answerer(a *proposals.CallAnswer, w *words.Words) string {
	switch {
	case a == nil || a.You:
		return w.ByYou
	case a.By == "":
		return w.BySomeone
	}
	role := strings.ToLower(a.By) // admin, editor, approver, owner
	if by := w.ByRole[role]; by != "" {
		return by
	}
	return w.ByOtherRole(role)
}

// answeredWords is a held call's line once answered, before KIFF records
// what came of it.
func answeredWords(decision, by string, w *words.Words) (kind, detail string) {
	switch decision {
	case "approved_once", "approve-once":
		return "approved", w.ApprovedOnceBy(by)
	case "change-card", "card_changed":
		return "approved", w.ExtraRoomBy(by)
	}
	return "refused", w.RefusedBy(by)
}

// withoutConnectionText keeps the connection's and the gateway's own text out
// of what a reader gets (forReader). The row carries a copy; the record keeps
// the text.
func withoutConnectionText(row *proposals.CallRow) {
	if o := row.ToolOutcome; o != nil {
		c := forReader(*o)
		row.ToolOutcome = &c
	}
}

// forReader is a call's outcome as a reader gets it, on every page that shows
// one (Activity, a control's decisions, answered holds): for a call never
// sent, one whose answer was lost (Unknown), or one the connection refused
// before sending, the text is planque's or the gateway's, in English, so it's
// left out and the reader reads KIFF's words for it, chosen by its status and
// code, in their language, never it as written. An app's own refusal
// (RefusedByTool from the vendor) keeps its message, which the apps quote
// only when it's a short, plain sentence.
func forReader(o tooloutcome.Outcome) tooloutcome.Outcome { return o.ForReader() }

// callExecuted is what the tool did with an allowed call, and its words.
func callExecuted(row *proposals.CallRow, w *words.Words) (kind, detail string) {
	approved := row.Held
	by := answerer(row.Answer, w)
	allowed := w.AllowedByCard
	if approved {
		allowed = w.ApprovedBy(by)
	}
	why := ""
	status := tooloutcome.Status("")
	if o := row.ToolOutcome; o != nil {
		status = o.Status
		why = o.Message
	}
	switch status {
	case tooloutcome.Done:
		if row.ToolOutcome.Found {
			return "done", w.FoundAtVendor(allowed)
		}
		if approved {
			return "approved", w.ApprovedThenDone(by)
		}
		return "done", w.DoneWithinCard
	case tooloutcome.RefusedByTool:
		if o := row.ToolOutcome; o.RefusedBy == "connection" {
			return "not_done", w.ConnectionRefused(w.ConnectionWhy[o.Code])
		}
		switch {
		case row.ReadOnly || row.Amount == nil:
			return "not_done", w.AppRefusedCounts(why)
		case isCurrency(row.Amount.Unit):
			return "not_done", w.AppRefusedMoney
		default:
			return "not_done", w.AppRefusedOther
		}
	case tooloutcome.NotSent:
		return "not_done", w.NotSentToTool(allowed, why)
	case "sending":
		return "sending", w.Sending(allowed)
	case tooloutcome.Unknown:
		return "unknown", w.AnswerLost(allowed)
	}
	return "unknown", w.NotRecorded(allowed)
}

// isCurrency reports whether unit is a currency code: a gateway's unit can
// name anything, so only a currency is money.
func isCurrency(unit string) bool {
	return len(unit) == 3 && strings.ToUpper(unit) == unit && strings.Trim(unit, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") == ""
}
