package handlers

import (
	"context"
	"github.com/kiff/cloud/api/internal/mandate"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/cardwords"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/tooloutcome"
	"github.com/kiff/cloud/api/internal/words"
	"strings"
)

// A Record line in the reader's words, from KIFF: a call is
// done only when the tool said so, and who approved it is said as it was.
func TestACallSaysItselfInTheReadersWords(t *testing.T) {
	row := func(outcome string, held bool, status tooloutcome.Status, amount *proposals.CallAmount, answer *proposals.CallAnswer) proposals.CallRow {
		r := proposals.CallRow{Held: held, Answer: answer}
		r.ActionName, r.Title, r.Outcome, r.Amount = "create_refund", "Refund a payment", outcome, amount
		if status != "" {
			r.ToolOutcome = &tooloutcome.Outcome{Status: status}
		}
		return r
	}
	usd := &proposals.CallAmount{Value: "45.00", Unit: "USD"}
	for _, c := range []struct {
		name               string
		row                proposals.CallRow
		kind, detail, text string
	}{
		{"within the Card", row("allowed", false, tooloutcome.Done, usd, nil), "done", "Done, within its Card", "$45"},
		{"approved by you", row("allowed", true, tooloutcome.Done, usd, &proposals.CallAnswer{You: true, By: "admin"}), "approved", "Approved by you, then done", "$45"},
		{"approved by an admin", row("allowed", true, tooloutcome.Done, usd, &proposals.CallAnswer{By: "admin"}), "approved", "Approved by an admin, then done", "$45"},
		{"refused by the tool", row("allowed", false, tooloutcome.RefusedByTool, usd, nil), "not_done", "The app refused it. It counts against today's total until KIFF can confirm no money moved.", "$45"},
		{"no outcome", row("allowed", false, "", usd, nil), "unknown", "Allowed by its Card, but whether the tool did it wasn't recorded: check the tool's own records", "$45"},
		{"held", row("owner_required", true, "", nil, nil), "held", "Waiting for you", ""},
		{"approved, not yet sent", row("owner_required", true, "", nil, &proposals.CallAnswer{Decision: "approved_once", You: true}), "approved", "Approved once by you", ""},
		{"refused by an editor", row("owner_required", true, "", nil, &proposals.CallAnswer{Decision: "refused", By: "editor"}), "refused", "Refused by an editor", ""},
		{"refused", row("blocked", false, "", nil, nil), "refused", "Not sent", ""},
	} {
		r := c.row
		withCallWords(&r, wordsIn(langEN))
		if r.Kind != c.kind || r.Detail != c.detail || r.AmountText != c.text || r.Name != "Refund a payment" {
			t.Errorf("%s: %q %q %q %q", c.name, r.Kind, r.Detail, r.AmountText, r.Name)
		}
	}
	// The connection refused it before sending: said so, with its reason
	// when there are words for it; the limits still count as before.
	for code, detail := range map[string]string{
		"halt_unknown":      "Not sent: the connection refused it before sending: it couldn't check whether the agent was halted. It still counts toward today's limits until KIFF can confirm nothing was done.",
		"operation_changed": "Not sent: the connection refused it before sending: the app changed this action since it was approved; the connection sends nothing until it's connected again. It still counts toward today's limits until KIFF can confirm nothing was done.",
		"signed_out":        "Not sent: the connection refused it before sending: the app ended the sign-in; the connection sends nothing until you sign in again. It still counts toward today's limits until KIFF can confirm nothing was done.",
		"something_new":     "Not sent: the connection refused it before sending. It still counts toward today's limits until KIFF can confirm nothing was done.",
	} {
		r := row("allowed", false, tooloutcome.RefusedByTool, usd, nil)
		r.ToolOutcome.RefusedBy, r.ToolOutcome.Code = "connection", code
		withCallWords(&r, wordsIn(langEN))
		if r.Kind != "not_done" || r.Detail != detail {
			t.Errorf("refused by the connection, %s: %q %q", code, r.Kind, r.Detail)
		}
	}
	if got := cardwords.MoneyDecimal("1250.5", "EUR", wordsIn(langEN)); got != "€1,250.50" {
		t.Errorf("MoneyDecimal: %q", got)
	}
}

// A call never sent, or refused by its connection before sending, is said in
// KIFF's words alone, in every language: the connection's and the gateway's
// own text (planque's, in English) reaches the reader neither in the line nor
// in the tool outcome the apps read. The record keeps it. An app's own
// refusal keeps its message, which the apps quote only when it's plain.
func TestAConnectionsOwnTextNeverReachesTheReader(t *testing.T) {
	const said = "This app isn't offered through tafs any more, so this connection is closed. Nothing was sent."
	for _, lang := range words.All {
		w := wordsIn(string(lang))
		for name, o := range map[string]tooloutcome.Outcome{
			"never sent": {Status: tooloutcome.NotSent, Code: "connection_down", Message: said},
			// A code whose own reason shares no words with planque's sentence, so
			// the check is for planque's words alone (app_closed's own reason is
			// KIFF's wording of the same fact).
			"refused by its connection": {Status: tooloutcome.RefusedByTool, RefusedBy: "connection", Code: "halt_unknown", Message: said},
			"its answer lost":           {Status: tooloutcome.Unknown, Message: said},
		} {
			kept := o
			var r proposals.CallRow
			r.Outcome, r.ToolOutcome = "allowed", &kept
			r.ActionName, r.Title = "append_row", "Add a row"
			withCallWords(&r, w)
			for _, word := range []string{"offered", "tafs any more", "connection is closed", said} {
				if strings.Contains(r.Detail, word) {
					t.Errorf("%s, %s: the line carries the connection's own words: %q", lang, name, r.Detail)
				}
			}
			if r.ToolOutcome == nil || r.ToolOutcome.Message != "" {
				t.Errorf("%s, %s: the reader's tool outcome still carries its text: %+v", lang, name, r.ToolOutcome)
			}
			if kept.Message != said {
				t.Errorf("%s, %s: the record lost its text", lang, name)
			}
		}
		// Never sent: the line is the plain fact, as with no text at all.
		allowed := func(o tooloutcome.Outcome) proposals.CallRow {
			var r proposals.CallRow
			r.Outcome, r.ToolOutcome = "allowed", &o
			return r
		}
		bare := allowed(tooloutcome.Outcome{Status: tooloutcome.NotSent})
		with := allowed(tooloutcome.Outcome{Status: tooloutcome.NotSent, Message: said})
		withCallWords(&bare, w)
		withCallWords(&with, w)
		if with.Detail != bare.Detail || with.Kind != "not_done" {
			t.Errorf("%s: never sent reads %q, want %q", lang, with.Detail, bare.Detail)
		}
		// The app's own refusal keeps what it said.
		vendor := allowed(tooloutcome.Outcome{Status: tooloutcome.RefusedByTool, Message: "Forbidden."})
		withCallWords(&vendor, w)
		if vendor.ToolOutcome.Message != "Forbidden." {
			t.Errorf("%s: an app's own refusal lost its message", lang)
		}
	}
}

// The same holds on every page that shows a call's outcome: a control's
// decisions (the KIFF Cloud dashboard's tooltip) and answered holds carry no
// text from the connection or the gateway, and an app's own refusal keeps its
// message.
func TestEveryPageLeavesTheConnectionsTextOut(t *testing.T) {
	const said = "This app isn't offered through tafs any more, so this connection is closed. Nothing was sent."
	ctx := context.Background()
	m := map[string]tooloutcome.Outcome{
		"p-not-sent":   {Status: tooloutcome.NotSent, Code: "connection_down", Message: said},
		"p-connection": {Status: tooloutcome.RefusedByTool, RefusedBy: "connection", Code: "app_closed", Message: said},
		"p-vendor":     {Status: tooloutcome.RefusedByTool, Message: "Forbidden."},
		// The gateway's own sentence for a lost answer is English too: left out.
		"p-unknown": {Status: tooloutcome.Unknown, Message: "Sent to the tool, but its response was lost. KIFF will not send it again."},
	}
	want := map[string]string{"p-not-sent": "", "p-connection": "", "p-vendor": "Forbidden.", "p-unknown": ""}

	// A control's decisions, and Activity, which reads them.
	h := &ControlsHandler{outcomes: &outcomeMap{m: m}, logger: slog.Default()}
	var rows []proposals.DecisionRow
	for id := range m {
		rows = append(rows, proposals.DecisionRow{ProposalID: id, Outcome: "allowed"})
	}
	h.withToolOutcomes(ctx, "t1", rows)
	for _, r := range rows {
		if r.ToolOutcome == nil || r.ToolOutcome.Message != want[r.ProposalID] {
			t.Errorf("decisions, %s: %+v", r.ProposalID, r.ToolOutcome)
		}
	}

	// Answered holds.
	store := mandate.NewMemoryStore()
	mh := NewMandatesHandler(store, nil, WithMandatesToolOutcomes(&outcomeMap{m: m}))
	at := time.Date(2026, 11, 18, 21, 0, 0, 0, time.UTC)
	var es []mandate.Exception
	for id := range m {
		e := mandate.Exception{ID: "exc-" + id, ProposalID: id, TenantID: "t1", Status: mandate.ExceptionConsumed, ResolvedAt: &at, CreatedAt: at}
		if err := store.SaveException(ctx, e); err != nil {
			t.Fatal(err)
		}
		es = append(es, e)
	}
	got := mh.toolOutcomes(httptest.NewRequest(http.MethodGet, "/v1/me/card-exceptions", nil), "t1", es)
	for id, w := range want {
		if o, ok := got[id]; !ok || o.Message != w {
			t.Errorf("answered holds, %s: %+v", id, got[id])
		}
	}
	// The reader's copy never changes what's kept.
	if m["p-not-sent"].Message != said {
		t.Error("the record lost its text")
	}
}

// A connection's refusal is worded by its code in every language: planque's
// codes for a closed app, a connection that couldn't be used and an action
// planque never runs each say why, and every language has the same codes as
// English, no more and no fewer. An unknown code still reads, with no reason.
func TestAConnectionRefusalSaysWhyByItsCode(t *testing.T) {
	en := words.Of(words.EN).ConnectionWhy
	for _, l := range words.All {
		why := words.Of(l).ConnectionWhy
		for code := range why {
			if _, ok := en[code]; !ok {
				t.Errorf("%s: %s has no English", l, code)
			}
		}
		for code := range en {
			if strings.TrimSpace(why[code]) == "" {
				t.Errorf("%s: no reason for %s", l, code)
			}
		}
		w := wordsIn(string(l))
		lines := map[string]bool{}
		for _, code := range []string{"app_closed", "connection_unavailable", "tool_not_served", "waits_for_key", "waits_for_approval"} {
			var r proposals.CallRow
			r.Outcome, r.ToolOutcome = "allowed", &tooloutcome.Outcome{Status: tooloutcome.RefusedByTool, RefusedBy: "connection", Code: code,
				Message: "This app isn't offered through tafs any more, so this connection is closed. Nothing was sent."}
			withCallWords(&r, w)
			if r.Kind != "not_done" || why[code] == "" || !strings.Contains(r.Detail, why[code]) {
				t.Errorf("%s, %s: %q %q, want the code's reason %q", l, code, r.Kind, r.Detail, why[code])
			}
			lines[r.Detail] = true
		}
		if len(lines) != 5 {
			t.Errorf("%s: the five codes don't read five ways", l)
		}
		var r proposals.CallRow
		r.Outcome, r.ToolOutcome = "allowed", &tooloutcome.Outcome{Status: tooloutcome.RefusedByTool, RefusedBy: "connection", Code: "not_a_code"}
		withCallWords(&r, w)
		if r.Kind != "not_done" || r.Detail == "" {
			t.Errorf("%s: an unknown code reads %q %q", l, r.Kind, r.Detail)
		}
	}
}
