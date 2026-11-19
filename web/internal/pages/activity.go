package pages

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// Activity is the record: every call the account's agents made through
// their Cards, across every domain, one row a call, each opening the
// call and its receipt. Needs you is where the owner acts; Activity is
// what happened.

// ActivityPerPage is how many calls a page lists.
const ActivityPerPage = 50

// maxActivityPage keeps a page's offset within the API's int32.
const maxActivityPage = (1<<31 - 1) / ActivityPerPage

// ActivityFilter is ?show= (allowed, refused, held), ?agent= and ?page=.
type ActivityFilter struct {
	Show, Agent string
	Page        int
}

// NormalizeActivityFilter reads the filter; anything unknown is all,
// every agent, the first page.
func NormalizeActivityFilter(show, agent, page string) ActivityFilter {
	switch show {
	case "allowed", "refused", "held":
	default:
		show = ""
	}
	agent = strings.TrimSpace(agent)
	if len(agent) > 200 {
		agent = ""
	}
	// Any page the counts reach: the API pages past 10,000 calls.
	p, err := strconv.Atoi(page)
	if err != nil || p < 0 || p > maxActivityPage {
		p = 0
	}
	return ActivityFilter{Show: show, Agent: agent, Page: p}
}

// Query is the API query for this filter.
func (f ActivityFilter) Query() apiclient.ActivityQuery {
	return apiclient.ActivityQuery{Actor: f.Agent, Show: f.Show, Limit: ActivityPerPage, Offset: f.Page * ActivityPerPage}
}

// href is the list with this filter changed to show, agent and page.
func activityHref(show, agent string, page int) string {
	q := url.Values{}
	if show != "" {
		q.Set("show", show)
	}
	if agent != "" {
		q.Set("agent", agent)
	}
	if page > 0 {
		q.Set("page", strconv.Itoa(page))
	}
	if len(q) == 0 {
		return "/activity"
	}
	return "/activity?" + q.Encode()
}

// ActivityAgentHref is an agent's activity, for links from a Card.
func ActivityAgentHref(agent string) string { return activityHref("", agent, 0) }

// activityCallHref opens a call that was not held; a held call opens its
// story on Needs you, the one view for it.
func activityCallHref(proposalID string) string {
	return "/activity/" + url.PathEscape(proposalID)
}

// ActivityRow is a call and, when a Card held it for its owner, the held
// call with the owner's answer (joined by proposal id).
type ActivityRow struct {
	Call      apiclient.ActivityCall
	Exception *apiclient.CardException
}

// JoinActivity attaches to each call the latest held call with its
// proposal id.
func JoinActivity(calls []apiclient.ActivityCall, held []apiclient.CardException) []ActivityRow {
	byProposal := map[string]apiclient.CardException{}
	for _, e := range held {
		if prev, ok := byProposal[e.ProposalID]; !ok || e.CreatedAt.After(prev.CreatedAt) {
			byProposal[e.ProposalID] = e
		}
	}
	out := make([]ActivityRow, 0, len(calls))
	for _, c := range calls {
		r := ActivityRow{Call: c}
		if e, ok := byProposal[c.ProposalID]; ok && c.ProposalID != "" {
			r.Exception = &e
		}
		out = append(out, r)
	}
	return out
}

// activityHref opens a row: a held call's story on Needs you, any other
// call its own page.
func (r ActivityRow) href() string {
	if r.Exception != nil {
		return NeedsYouHref(r.Exception.ID)
	}
	return activityCallHref(r.Call.ProposalID)
}

// pendingOutcome is a latest decision that waits for a person: a domain
// approval, or a Card holding the call for its owner.
func pendingOutcome(outcome string) bool {
	switch outcome {
	case "approval_required", "owner_required", "approved_no_room":
		return true
	}
	return false
}

// decided is what KIFF decided, as a chip: label and tone (ok, warn,
// wait, off). A call still waiting reads as waiting even with no held
// call to join: a domain approval has none, and the held calls may not
// have been readable. It is never a refusal.
func (r ActivityRow) decided() (string, string) {
	switch {
	case r.Exception != nil && r.Exception.NeedsOwner():
		return "Waiting for you", "wait"
	case r.Exception == nil && r.Call.Outcome == "approval_required":
		return "Waiting for approval", "wait"
	case r.Exception == nil && r.Call.Outcome == "approved_no_room":
		// The owner's approval is kept; the Card has no room for it yet.
		// Nobody needs to answer again.
		return "Approved, waiting for room", "wait"
	case r.Exception == nil && pendingOutcome(r.Call.Outcome):
		return "Waiting for you", "wait"
	case r.Call.Held:
		return "Held", "warn"
	case r.Call.Outcome == "allowed":
		return "Allowed", "ok"
	}
	return decisionOutcomeLabel(r.Call.Outcome), "off"
}

// answer is what the owner answered a held call, "" when nobody has.
func (r ActivityRow) answer() string {
	e := r.Exception
	if e == nil || e.NeedsOwner() {
		return ""
	}
	if e.Status == "rejected" && e.Refusal != nil && e.Refusal.NextStep != "" {
		return "Rejected: " + NextStepLabel(e.Refusal.NextStep)
	}
	return exceptionAnswerLabel(*e)
}

// amount is what the call carries, when a held call recorded it.
func (r ActivityRow) amount() string {
	if r.Exception == nil {
		return ""
	}
	return exceptionAmount(*r.Exception)
}

// toolLine is what the tool reported. Only a recorded outcome is said: a
// missing one may be a failed read, so it is never "not sent".
func (r ActivityRow) toolLine() string {
	if l := toolOutcomeLabel(r.Call.ToolOutcome); l != "" {
		return sentenceCase(l)
	}
	return "—"
}

func (r ActivityRow) toolWarns() bool {
	return r.Call.ToolOutcome != nil && toolOutcomeWarns(r.Call.ToolOutcome.Status)
}

// receiptLabel is a decision's receipt state in two or three words.
func receiptLabel(d apiclient.ProposalDecision) string {
	switch {
	case d.ReceiptTraceID != "" && d.ReceiptStatus == "anchored":
		return "Signed and anchored"
	case d.ReceiptTraceID != "":
		return "Signed"
	case receiptComing(d):
		return "Being signed"
	}
	return "No receipt"
}

// receiptComing reports whether KIFF signs a receipt for this decision
// (receipt.DecisionOutcomeReceiptable): every governance decision,
// including a call held for its owner, but not a request rejected before
// it was decided.
func receiptComing(d apiclient.ProposalDecision) bool {
	switch d.Outcome {
	case "allowed", "blocked", "approval_required", "owner_required", "approved_no_room":
		for _, r := range d.Reasons {
			if r == "key_not_bound_to_agent" || r == "actor_not_key_holder" {
				return false
			}
		}
		return true
	}
	return false
}

// activityDay is one day's calls, in the account's time zone.
type activityDay struct {
	Label string
	Rows  []ActivityRow
}

// activityDays groups rows (newest first) by local day: Today,
// Yesterday, then the date.
func activityDays(rows []ActivityRow, loc *time.Location, now time.Time) []activityDay {
	today := now.In(loc).Format("2006-01-02")
	yesterday := now.In(loc).AddDate(0, 0, -1).Format("2006-01-02")
	var out []activityDay
	for _, r := range rows {
		t := r.Call.CreatedAt.In(loc)
		label := t.Format("2 Jan 2006")
		switch t.Format("2006-01-02") {
		case today:
			label = "Today, " + t.Format("2 Jan")
		case yesterday:
			label = "Yesterday, " + t.Format("2 Jan")
		}
		if len(out) == 0 || out[len(out)-1].Label != label {
			out = append(out, activityDay{Label: label})
		}
		out[len(out)-1].Rows = append(out[len(out)-1].Rows, r)
	}
	return out
}

// activityChip is one filter, with how many calls it shows.
type activityChip struct {
	Label, Href string
	Count       int
	Current     bool
}

func activityChips(f ActivityFilter, c apiclient.ActivityCounts) []activityChip {
	out := make([]activityChip, 0, 4)
	for _, x := range []struct {
		show, label string
		n           int
	}{{"", "All", c.All}, {"allowed", "Allowed", c.Allowed}, {"refused", "Refused", c.Refused}, {"held", "Held for you", c.Held}} {
		out = append(out, activityChip{Label: x.label, Href: activityHref(x.show, f.Agent, 0), Count: x.n, Current: f.Show == x.show})
	}
	return out
}

// ActivityAgents are the agents the list can be narrowed to: every agent
// with a call (from the API, so one with no Card is there too, a
// review), every holder of a Card, and the agent asked for, sorted.
func ActivityAgents(callers []string, cards []apiclient.Mandate, current string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(a string) {
		if a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, a := range callers {
		add(a)
	}
	for _, m := range cards {
		for _, h := range m.Holders() {
			add(h)
		}
	}
	add(current)
	sort.Strings(out)
	return out
}

// activityRange says which calls a page shows: "1–50 of 120".
func activityRange(f ActivityFilter, shown, n int) string {
	first := f.Page*ActivityPerPage + 1
	return strconv.Itoa(first) + "–" + strconv.Itoa(first+n-1) + " of " + strconv.Itoa(shown)
}

// activityCallLead is a call that was not held, in one sentence.
func activityCallLead(c apiclient.ActivityCall) string {
	who := c.ActorID
	if who == "" {
		who = "An agent"
	}
	asked := who + " asked to run " + c.ActionName
	// A gateway call's entity is its own call id: not worth repeating.
	if c.EntityID != "" && c.EntityID != c.ProposalID {
		asked += " on " + c.EntityID
	}
	switch c.Outcome {
	case "allowed":
		return asked + ". It fit the Cards that cover it, so KIFF allowed it."
	case "approval_required":
		return asked + ". KIFF held it for an approver: nothing is authorized until someone answers."
	case "owner_required":
		return asked + ". A Card held it for you: nothing is authorized until you answer."
	case "approved_no_room":
		return asked + ". It was approved, but the Card has no room for it yet."
	case "invalid":
		return asked + ". The request was not valid, so KIFF decided nothing and nothing was authorized."
	case "limit_exceeded":
		return asked + ". The account was over its plan limit, so nothing was authorized."
	}
	return asked + ". KIFF refused it, so nothing was authorized."
}

// activityReasons are a refused call's reasons in words.
func activityReasons(c apiclient.ActivityCall) []string {
	out := make([]string, 0, len(c.Reasons))
	for _, r := range c.Reasons {
		out = append(out, sentenceCase(strings.ReplaceAll(r, "_", " ")))
	}
	return out
}

// activityDecisionStep is the timeline's line for KIFF's decision.
func activityDecisionStep(c apiclient.ActivityCall, label string) string {
	if pendingOutcome(c.Outcome) {
		return "Held by KIFF"
	}
	return label + " by KIFF"
}

// activityWaitHref is where a call still waiting is answered: a domain
// approval in Needs attention, a Card's hold on Needs you.
func activityWaitHref(c apiclient.ActivityCall) (string, string) {
	switch c.Outcome {
	case "approval_required":
		return "/needs-you", "Answer it on Needs you"
	case "owner_required", "approved_no_room":
		return "/needs-you", "Open Needs you"
	}
	return "", ""
}
