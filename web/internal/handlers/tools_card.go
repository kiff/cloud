package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/gatewayclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// Cards for connected tools: the owner gives an agent a Card for one
// connected tool, in the tool's own terms (a limit per call, a total per
// period, a number of calls), and never sees the gateway domain or its
// contract. The Card is an ordinary Card on the account's gateway
// domain; the tool's name is its action, and the tool's amount argument
// is the quantity it limits.

// gatewayDomain is the domain the gateway applies for the account's
// connected tools (gateway/internal/gateway.Domain).
const gatewayDomain = "gateway"

// toolCardWindows are the periods a total can be counted over, in the
// order the form offers them.
var toolCardWindows = map[string]bool{"calendar_day": true, "rolling_24h": true, "rolling_1h": true, "rolling_7d": true, "lifetime": true}

// validHold accepts a hold expiry the API accepts: the form's choices,
// or a Card's own when the form keeps it (the cards-for-any-agent
// design, 1m to 168h).
func validHold(s string) bool {
	d, err := time.ParseDuration(s)
	return err == nil && d >= time.Minute && d <= 168*time.Hour
}

func (h *DashboardHandler) toolCardForm(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.toolsBearer(w, r)
	if !ok {
		return
	}
	v := pages.ToolCardView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName,
		Form: pages.ToolCardForm{Agent: strings.TrimSpace(r.URL.Query().Get("agent")), Window: "calendar_day", OnExceed: "owner", Hold: "10m"}, Step: r.URL.Query().Get("step"), Single: r.URL.Query().Get("step") != "" && r.URL.Query().Get("one") == "1"}
	if !h.loadToolCard(w, r, bearer, &v) {
		return
	}
	// Changing a Card starts from its current terms.
	if v.Form.Agent != "" && !v.NotConfigured && v.LoadError == "" {
		if cards, err := h.api.ListMandatesBearer(r.Context(), bearer); err == nil {
			if id, updating := toolCardID(v.Form.Agent, v.Tool.Name, cards, time.Now()); updating {
				for _, m := range cards {
					if m.ID == id {
						v.Form = pages.ToolCardFormFrom(m)
						v.Editing = m.ID
					}
				}
			}
		}
	}
	// One step on its own only changes a Card that exists.
	v.Single = v.Single && v.Editing != ""
	h.renderToolCard(w, r, http.StatusOK, v)
}

// loadToolCard fills the tool and the account's agents; false means the
// response is written.
func (h *DashboardHandler) loadToolCard(w http.ResponseWriter, r *http.Request, bearer string, v *pages.ToolCardView) bool {
	if h.gateway == nil || h.api == nil {
		v.NotConfigured = true
		return true
	}
	name := r.PathValue("name")
	tools, err := h.gateway.List(r.Context(), bearer)
	if err != nil {
		v.LoadError = "Your tools could not be loaded, so no Card can be issued for one right now. " + gatewayMessage(err)
		return true
	}
	for _, t := range tools {
		if t.Name == name {
			v.Tool = t
			break
		}
	}
	if v.Tool.Name == "" {
		http.NotFound(w, r)
		return false
	}
	if agents, err := h.api.ListAgentsBearer(r.Context(), bearer); err == nil {
		for _, a := range agents {
			if a.ArchivedAt == nil {
				v.Agents = append(v.Agents, a.ID)
			}
		}
	}
	return true
}

func (h *DashboardHandler) renderToolCard(w http.ResponseWriter, r *http.Request, status int, v pages.ToolCardView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = pages.ToolCard(v).Render(r.Context(), w)
}

func (h *DashboardHandler) issueToolCard(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.toolsBearer(w, r)
	if !ok || !toolsForm(w, r) {
		return
	}
	f := pages.ToolCardForm{
		Agent:         toolCardAgent(r.PostFormValue("agent_choice"), r.PostFormValue("agent")),
		PerCall:       strings.TrimSpace(r.PostFormValue("per_call")),
		Total:         strings.TrimSpace(r.PostFormValue("total")),
		Calls:         strings.TrimSpace(r.PostFormValue("calls")),
		Window:        r.PostFormValue("window"),
		AmountMeasure: r.PostFormValue("amount_measure"), CountMeasure: r.PostFormValue("count_measure"),
		OnExceed:  r.PostFormValue("on_exceed"),
		Hold:      r.PostFormValue("hold"),
		NextSteps: r.PostFormValue("next_steps"),
	}
	_, stepsPosted := r.PostForm["next_steps"]
	v := pages.ToolCardView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, Form: f}
	if !h.loadToolCard(w, r, bearer, &v) {
		return
	}
	if v.NotConfigured || v.LoadError != "" {
		h.renderToolCard(w, r, http.StatusServiceUnavailable, v)
		return
	}
	refuse := func(msg string) {
		v.Error = msg
		h.renderToolCard(w, r, http.StatusBadRequest, v)
	}
	req, msg := toolCardRequest(v.Tool, f)
	if msg != "" {
		refuse(msg)
		return
	}
	cards, err := h.api.ListMandatesBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "tool card: list cards failed", slog.String("err", err.Error()))
		refuse("Your existing Cards could not be read, so a name for this one could not be chosen safely. Try again in a moment.")
		return
	}
	var updating bool
	req.ID, updating = toolCardID(f.Agent, v.Tool.Name, cards, time.Now())
	if updating {
		keepCardTerms(&req, cards)
		if stepsPosted {
			// The form shows the steps: what it says now is the Card's.
			// A form without the field keeps the Card's.
			req.Grants[0].NextSteps = parseNextSteps(f)
		}
	}
	m, err := h.api.IssueMandateBearer(r.Context(), bearer, req)
	if err != nil {
		var rejected *apiclient.MandateRejectedError
		if errors.As(err, &rejected) {
			refuse("The Card was not issued: " + rejected.Message)
			return
		}
		h.logger.WarnContext(r.Context(), "tool card: issue failed", slog.String("mandate", req.ID), slog.String("err", err.Error()))
		refuse("The Card could not be issued. Nothing was changed. Try again in a moment.")
		return
	}
	verb := "can now use "
	if updating {
		verb = "has new terms for "
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashMandateIssued,
		Message: m.Subject + " " + verb + v.Tool.Name + ": " + pages.ToolCardTerms(v.Tool, f) + "."})
	http.Redirect(w, r, "/cards/"+url.PathEscape(m.ID), http.StatusSeeOther)
}

// toolCardRequest turns the form into a Card on the gateway domain, or
// returns the sentence saying what to fix.
func toolCardRequest(t gatewayclient.Connection, f pages.ToolCardForm) (apiclient.IssueMandateRequest, string) {
	var req apiclient.IssueMandateRequest
	for _, m := range []string{f.AmountMeasure, f.CountMeasure} {
		if m != "" && m != "attempts" && m != "effects" {
			return req, "Choose attempts or effects for each cumulative limit."
		}
	}
	if f.Agent == "" {
		return req, "Choose the agent this Card is for."
	}
	if !toolCardWindows[f.Window] {
		return req, "Choose the period the totals are counted over."
	}
	if f.OnExceed != "owner" && f.OnExceed != "refuse" {
		return req, "Choose what happens to a call outside the Card."
	}
	if f.OnExceed == "owner" && !validHold(f.Hold) {
		return req, "Choose how long a held call waits for an answer."
	}
	amount := "sum(" + t.AmountArg + ")"
	grant := apiclient.MandateGrant{Action: t.Name}
	var aggs []apiclient.MandateAggregate
	if t.AmountArg != "" {
		perCall, ok, msg := optionalPositive(f.PerCall, "The limit per call")
		if msg != "" {
			return req, msg
		}
		if ok {
			grant.PerActionMax = []apiclient.MandatePerAction{{Quantity: amount, Max: perCall}}
		}
		total, ok, msg := optionalPositive(f.Total, "The total")
		if msg != "" {
			return req, msg
		}
		if ok {
			aggs = append(aggs, apiclient.MandateAggregate{Quantity: amount, Limit: total, Window: f.Window, Measure: f.AmountMeasure})
		}
	}
	calls, ok, msg := optionalPositive(f.Calls, "The number of calls")
	if msg != "" {
		return req, msg
	}
	if ok {
		aggs = append(aggs, apiclient.MandateAggregate{Quantity: "count", Limit: calls, Window: f.Window, Measure: f.CountMeasure})
	}
	if len(aggs) == 0 {
		if t.AmountArg != "" {
			return req, "Set a total or a number of calls for the period. A Card with no total limits nothing over time."
		}
		return req, "Set how many calls the agent may make in the period."
	}
	grant.NextSteps = parseNextSteps(f)
	req = apiclient.IssueMandateRequest{Subject: f.Agent, Domain: gatewayDomain,
		Grants: []apiclient.MandateGrant{grant}, Aggregates: aggs, OnExceed: f.OnExceed}
	if f.OnExceed == "owner" {
		req.HoldExpiry = f.Hold
	}
	return req, ""
}

// optionalPositive reads a whole number above zero; empty is "not set".
// It accepts "1,000" and "1 000".
func optionalPositive(raw, what string) (int64, bool, string) {
	s := strings.NewReplacer(",", "", " ", "", " ", "", "_", "").Replace(raw)
	if s == "" {
		return 0, false, ""
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false, what + " has to be a whole number above zero, in the tool's own units."
	}
	return n, true, ""
}

// toolCardID names the Card after its agent and tool, so an agent holds
// one Card per tool and issuing again changes it while it is in force.
// Only this agent's own Card for this tool is changed: the name is a
// slug, so "refund-agent" and "refund_agent", or "billing" with
// "refund_issue" and "billing-refund" with "issue", share one, and
// reissuing another agent's Card under it would move that Card to this
// agent. Any other Card using the name, or one no longer in force, is
// skipped for the next free name.
func toolCardID(agent, tool string, cards []apiclient.Mandate, now time.Time) (string, bool) {
	base := strings.TrimPrefix(mandateIDFromSubject(agent), "limit-")
	if base == "limit" {
		base = "agent"
	}
	base += "-" + strings.ReplaceAll(strings.ToLower(tool), "_", "-")
	return ownCardID(base, cards, now, func(m apiclient.Mandate) bool {
		return isToolCardOf(m, agent, tool)
	})
}

// isToolCardOf reports a Card this form may change for agent and tool: on
// the gateway domain, with agent as its first holder (a reissue naming
// any other holder would take the Card from the rest), and granting this
// tool alone (reissuing would drop any other grant).
func isToolCardOf(m apiclient.Mandate, agent, tool string) bool {
	return isCardOf(m, agent, gatewayDomain) &&
		len(m.Grants) == 1 && m.Grants[0].Action == tool
}

// toolCardAgent is the Card's holder: the agent chosen from the account's
// agents, or the typed name for "another agent" or when no choice was
// posted (an older form, or no agents yet).
func toolCardAgent(choice, typed string) string {
	if c := strings.TrimSpace(choice); c != "" && c != "other" {
		return c
	}
	return strings.TrimSpace(typed)
}

// keepCardTerms carries what the tool Card form does not show from the
// Card it changes: its resource scope and next steps (unless the form
// posts them), its start and its end. Omitted, the API would drop the
// scope, make a future Card start now and a dated Card never end.
func keepCardTerms(req *apiclient.IssueMandateRequest, cards []apiclient.Mandate) {
	for _, c := range cards {
		if c.ID == req.ID && len(c.Grants) == 1 && len(req.Grants) == 1 {
			req.Grants[0].Resources = c.Grants[0].Resources
			req.Grants[0].NextSteps = c.Grants[0].NextSteps
			req.ValidFrom, req.ValidUntil = c.ValidFrom, c.ValidUntil
			return
		}
	}
}

// parseNextSteps reads the form's other answers as Card next steps:
// "Return first, proof-first" is [return_first proof_first]. Spaces and
// hyphens become underscores; underscores already there are kept as they
// are, so an existing identifier (return__first) comes back unchanged.
// Only for a Card that asks: a refused call has no one to answer it. The
// API checks each step's shape and says what is wrong.
func parseNextSteps(f pages.ToolCardForm) []string {
	if f.OnExceed != "owner" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(f.NextSteps, ",") {
		s := strings.Join(strings.Fields(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(part), "-", " "))), "_")
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
