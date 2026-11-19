package pages

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// Needs you is one place with three tabs: what waits for an answer,
// the Cards running low, and what was answered.
const (
	NeedsYouWaiting  = "waiting"
	NeedsYouWarnings = "warnings"
	NeedsYouBlocked  = "blocked"
	NeedsYouAnswered = "answered"
)

// NormalizeNeedsYouTab reads ?tab=. A filter on the answers, from a link
// made before the tabs, opens Answered.
func NormalizeNeedsYouTab(tab string, f AnsweredFilter) string {
	switch tab {
	case NeedsYouWarnings, NeedsYouBlocked, NeedsYouAnswered:
		return tab
	}
	if f.Answer != "" || f.Agent != "" {
		return NeedsYouAnswered
	}
	return NeedsYouWaiting
}

// needsYouTabHref is a tab's address; Answered keeps its filter.
func needsYouTabHref(tab string, f AnsweredFilter) string {
	switch tab {
	case NeedsYouWarnings:
		return "/needs-you?tab=" + NeedsYouWarnings
	case NeedsYouBlocked:
		return "/needs-you?tab=" + NeedsYouBlocked
	case NeedsYouAnswered:
		q := url.Values{"tab": {NeedsYouAnswered}}
		if f.Answer != "" {
			q.Set("answer", f.Answer)
		}
		if f.Agent != "" {
			q.Set("agent", f.Agent)
		}
		return "/needs-you?" + q.Encode()
	}
	return "/needs-you"
}

// warningsPerPage is how many Card warnings the API lists a page.
const warningsPerPage = 100

// needsYouWarningsHref is a page of the Warnings tab; the first has none.
func needsYouWarningsHref(page int) string {
	if page <= 0 {
		return needsYouTabHref(NeedsYouWarnings, AnsweredFilter{})
	}
	return needsYouTabHref(NeedsYouWarnings, AnsweredFilter{}) + "&page=" + strconv.Itoa(page)
}

// needsYouBlockedHref is a page of the Blocked tab; the first has none.
func needsYouBlockedHref(page int) string {
	if page <= 0 {
		return needsYouTabHref(NeedsYouBlocked, AnsweredFilter{})
	}
	return needsYouTabHref(NeedsYouBlocked, AnsweredFilter{}) + "&page=" + strconv.Itoa(page)
}

// blockedRange says which of the blocked calls a page shows.
func blockedRange(v NeedsYouView) string {
	first := v.BlockedPage*warningsPerPage + 1
	return strconv.Itoa(first) + "–" + strconv.Itoa(first+len(v.Blocked)-1) + " of " + strconv.Itoa(v.BlockedOpen) + " blocked calls"
}

// waitingCount is what the Waiting tab counts: Card holds and domain
// approvals.
func waitingCount(v NeedsYouView) int { return len(v.Held) + v.ApprovalsOpen }

// needsYouLiveHref re-reads the tab shown, on the page shown.
func needsYouLiveHref(v NeedsYouView) string {
	if v.Tab == NeedsYouWarnings {
		return needsYouWarningsHref(v.WarningsPage)
	}
	if v.Tab == NeedsYouBlocked {
		return needsYouBlockedHref(v.BlockedPage)
	}
	if v.Tab == NeedsYouWaiting {
		return approvalsHref(v.ApprovalsPage)
	}
	return needsYouTabHref(v.Tab, v.Filter)
}

// warningsRange says which of the open warnings a page shows.
func warningsRange(v NeedsYouView) string {
	first := v.WarningsPage*warningsPerPage + 1
	last := first + len(v.BudgetWarnings) - 1
	return strconv.Itoa(first) + "–" + strconv.Itoa(last) + " of " + strconv.Itoa(v.WarningsOpen) + " open warnings"
}

// OpenWarnings keeps the Card warnings still open: a resolved one no
// longer needs anyone.
func OpenWarnings(items []apiclient.AttentionItem) []apiclient.AttentionItem {
	var out []apiclient.AttentionItem
	for _, it := range items {
		if it.Status == "" || it.Status == "open" {
			out = append(out, it)
		}
	}
	return out
}

// OpenOfKind keeps the open work items of one kind: a feed asked for one
// kind shows nothing of another, whatever the API sent.
func OpenOfKind(items []apiclient.AttentionItem, kind string) []apiclient.AttentionItem {
	var out []apiclient.AttentionItem
	for _, it := range OpenWarnings(items) {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}

// WithoutCardWarnings drops Card warnings from the attention queue: they
// are on Needs you › Warnings.
func WithoutCardWarnings(items []apiclient.AttentionItem) []apiclient.AttentionItem {
	var out []apiclient.AttentionItem
	for _, it := range items {
		if it.Kind != "card_warning" {
			out = append(out, it)
		}
	}
	return out
}

// cardWarningsPointer says where the attention queue's Card warnings went.
func cardWarningsPointer(n int) string {
	if n == 1 {
		return "1 open Card warning is in"
	}
	return strconv.Itoa(n) + " open Card warnings are in"
}

// warningTitle names a Card warning in two or three words.
func warningTitle(reason string) string {
	if strings.Contains(reason, "is spent") {
		return "Used up"
	}
	return "Running low"
}

// warningsLine is the Waiting tab's one line about open Card warnings.
func warningsLine(n int) string {
	if n == 1 {
		return "1 Card is running low or used up. Nothing is held because of it."
	}
	return strconv.Itoa(n) + " Cards are running low or used up. Nothing is held because of it."
}

// storyWho is who answered, to start a sentence: "You", an email, or an
// external approver; "" when it isn't recorded.
func storyWho(e apiclient.CardException) string {
	return storyBy(e.ResolvedBy, e.ExternalAnswer)
}

func storyBy(by string, ext *apiclient.ExternalAnswer) string {
	if ext != nil {
		return externalAnswerName(ext) + ", an external approver,"
	}
	if strings.HasPrefix(by, "you") {
		return "You"
	}
	return by
}

// storyApproval is the last approval recorded before the call was
// withdrawn, nil when it was withdrawn before anyone approved it. A
// withdrawal moves the approval into PriorApprovals and stamps
// ResolvedAt with the withdrawal, and an external approver's defer
// is evidence, never an approval.
func storyApproval(e apiclient.CardException) *apiclient.CardApprovalRecord {
	for i := len(e.PriorApprovals) - 1; i >= 0; i-- {
		a := e.PriorApprovals[i]
		if a.ExternalAnswer == nil || a.ExternalAnswer.Decision != "defer" {
			return &a
		}
	}
	return nil
}

// storyWithdrawn is why a call was withdrawn, after a colon.
func storyWithdrawn(reason string) string {
	switch reason {
	case "card_revoked":
		return "the Card was revoked"
	case "holder_changed":
		return "the Card moved to another agent"
	case "card_expired":
		return "the Card expired"
	}
	return reason
}

// storyLead is an answered call in one or two sentences: who asked,
// for what, who answered and how, and what the agent was told.
func storyLead(e apiclient.CardException) string {
	asked := e.Subject + " asked to run " + e.Action
	if amt := exceptionAmount(e); amt != "" {
		asked += " for " + amt
	}
	asked += "."
	who := storyWho(e)
	var answer string
	switch e.Status {
	case "approved":
		answer = orPassive(who, "approved it once", "It was approved once") + ". The agent hasn't collected it yet."
	case "consumed":
		answer = orPassive(who, "approved it once", "It was approved once") + ", and the agent used the approval."
	case "rejected":
		answer = orPassive(who, "rejected it", "It was rejected")
		if r := e.Refusal; r != nil && r.NextStep != "" {
			answer += " and told the agent: " + NextStepLabel(r.NextStep)
		}
		answer += "."
	case "changed":
		// Change Card stores the change and returns: nothing is decided
		// again until the agent asks again. Only a recorded outcome
		// says a later call ran.
		switch {
		case storyRefit(e):
			answer = "Before anyone answered, the agent asked again and the call fit the Card as it then stood."
		case e.ToolOutcome != nil:
			answer = orPassive(who, "changed the Card instead of approving it", "The Card was changed instead") + ". The agent asked again, and the call was checked against the changed Card."
		default:
			answer = orPassive(who, "changed the Card instead of approving it", "The Card was changed instead") + ". If the agent asks again, the call is checked against the Card as it is then."
		}
	case "expired":
		answer = "Nobody answered before the hold ran out, so it was refused."
	case "invalidated":
		why := ""
		if r := storyWithdrawn(e.Reason); r != "" {
			why = ": " + r
		}
		if a := storyApproval(e); a != nil {
			answer = orPassive(storyBy(a.By, a.ExternalAnswer), "approved it once", "It was approved once") + ", then it was withdrawn before the agent used it" + why + "."
		} else {
			answer = "It was withdrawn before anyone answered" + why + "."
		}
	}
	return strings.TrimSpace(asked + " " + answer)
}

func orPassive(who, active, passive string) string {
	if who == "" {
		return passive
	}
	return who + " " + active
}

// storyAfter is what followed the answer, in one sentence.
func storyAfter(e apiclient.CardException) string {
	switch e.Status {
	case "rejected", "expired", "invalidated":
		return "Nothing was sent to the tool, and nothing counted against the Card."
	case "approved":
		return "Nothing has been sent yet. When the agent collects it, every Card is checked again."
	case "consumed":
		if l := toolOutcomeLine(e.ToolOutcome); l != "" {
			return l
		}
		return sentenceCase(answeredOutcomeLabel(e)) + "."
	case "changed":
		// The API reads outcomes best effort and leaves them out when it
		// can't, so a missing one proves nothing either way: say what is
		// known, never that nothing was sent.
		if o := e.ToolOutcome; o != nil {
			if l := toolOutcomeLine(o); l != "" {
				return l
			}
			if l := toolOutcomeLabel(o); l != "" {
				return sentenceCase(l) + "."
			}
			return "A tool outcome is recorded, but it can't be shown here."
		}
		if storyRefit(e) {
			return "The call was allowed. No tool outcome is available here, so this page can't say whether the tool ran."
		}
		return "No tool outcome is available here. A Card change alone doesn't say whether the agent asked again or the tool ran."
	}
	return ""
}

// storyRefit is a call nobody answered: on a retry it fit the Card as it
// then stood, and the authorizer let it through.
func storyRefit(e apiclient.CardException) bool {
	return e.Status == "changed" && e.ResolvedBy == "" && e.Reason == "fits the card as it now stands"
}

// storyReason is one limit the call was outside of, in plain words.
type storyReason struct{ Title, Text, Card string }

func storyValue(b apiclient.CardBreach, n int64) string {
	if b.Unit == "" && !b.UnitUnavailable && !b.UnitAmbiguous && b.Quantity == "count" {
		return countOf(n, "call", "calls")
	}
	return breachValue(b, n)
}

// storyReasons says why the call came to the owner, one limit a row.
func storyReasons(e apiclient.CardException) []storyReason {
	out := make([]storyReason, 0, len(e.Breaches))
	for _, b := range e.Breaches {
		r := storyReason{Card: b.MandateID}
		switch b.Outcome {
		case "per_action_exceeded":
			r.Title = "Bigger than one call may be"
			r.Text = "The Card allows up to " + storyValue(b, b.Limit) + " per call. This one was " + storyValue(b, b.Draw) + "."
		case "limit_reached":
			left := b.Limit - b.Used
			if left < 0 {
				left = 0
			}
			r.Title = "More than " + windowLeftName(b.Window) + " had left"
			was := " was left."
			if b.Unit == "" && b.Quantity == "count" && left != 1 {
				was = " were left."
			}
			r.Text = "Of the Card's " + storyValue(b, b.Limit) + " " + windowPer(b.Window) + ", " + storyValue(b, left) + was +
				" This one needed " + storyValue(b, b.Draw) + "."
		case "resource_not_on_card":
			r.Title = "Not one this Card covers"
			r.Text = breachLine(b) + "."
		default:
			r.Title, r.Text = "Outside the Card", b.Reason
		}
		out = append(out, r)
	}
	return out
}

// windowPer reads a total's window after its amount: "300.00 EUR a day".
func windowPer(window string) string {
	switch window {
	case "calendar_day":
		return "a day"
	case "rolling_24h":
		return "in any 24 hours"
	case "rolling_1h":
		return "in any hour"
	case "rolling_7d":
		return "in any 7 days"
	case "lifetime":
		return "in its whole life"
	}
	return "in its window"
}

func windowLeftName(window string) string {
	switch window {
	case "calendar_day":
		return "the day"
	case "rolling_24h":
		return "the last 24 hours"
	case "rolling_1h":
		return "the last hour"
	case "rolling_7d":
		return "the last 7 days"
	case "lifetime":
		return "the Card's whole life"
	}
	return "its total"
}

// storyStep is one moment of an answered call, oldest first.
type storyStep struct {
	Title, Detail string
	At            *time.Time
	// Kind colours the dot: "asked", "yes", "no" or "done".
	Kind string
}

func storySteps(e apiclient.CardException) []storyStep {
	created := e.CreatedAt
	steps := []storyStep{{Title: e.Subject + " asked", Detail: "Held for you. Nothing was sent.", At: &created, Kind: "asked"}}
	// Earlier answers come from their own records, with their own
	// times: a withdrawal moves the approval here.
	for _, a := range e.PriorApprovals {
		if a.ExternalAnswer != nil && a.ExternalAnswer.Decision == "defer" {
			steps = append(steps, storyStep{Title: externalAnswerName(a.ExternalAnswer) + ", an external approver, left it to you",
				Detail: externalAnswerReason(a.ExternalAnswer), At: a.At, Kind: "asked"})
			continue
		}
		detail := ""
		if a.By != "" {
			detail = "Answered by " + a.By
		}
		if a.ExternalAnswer != nil {
			detail = externalAnswerName(a.ExternalAnswer) + ", an external approver: " + externalAnswerReason(a.ExternalAnswer)
		}
		steps = append(steps, storyStep{Title: "Approved once", Detail: detail, At: a.At, Kind: "yes"})
	}
	answered := storyStep{At: e.ResolvedAt}
	byline := answeredByLine(e)
	if e.ExternalAnswer != nil {
		byline = externalAnswerName(e.ExternalAnswer) + ", an external approver: " + externalAnswerReason(e.ExternalAnswer)
	}
	switch e.Status {
	case "approved", "consumed":
		answered.Title, answered.Kind = "Approved once", "yes"
	case "rejected":
		answered.Title, answered.Kind = "Rejected", "no"
		if r := e.Refusal; r != nil && r.NextStep != "" {
			answered.Title += ": " + NextStepLabel(r.NextStep)
		}
		if r := e.Refusal; r != nil && (r.NextStep != "" || r.Note != "") {
			told := "The agent is told this when it asks again."
			if r.Note != "" {
				told = "The agent is told this, with the note: “" + r.Note + "”"
			}
			byline = strings.TrimSpace(byline + " " + told)
		}
	case "changed":
		answered.Title, answered.Kind = "Card changed instead", "yes"
		if storyRefit(e) {
			answered.Title, byline = "Asked again, and it fit the Card", "Nobody had to answer."
		}
	case "expired":
		answered.Title, answered.Kind, answered.At = "The hold ran out", "no", e.ExpiresAt
		byline = "Nobody answered in time, so it was refused."
	}
	answered.Detail = byline
	if answered.Title != "" {
		steps = append(steps, answered)
	}
	if e.Status == "invalidated" {
		// ResolvedAt is when it was withdrawn, not an answer.
		title := "Withdrawn before anyone answered"
		if storyApproval(e) != nil {
			title = "Withdrawn before it was used"
		}
		w := storyStep{Title: title, At: e.ResolvedAt, Kind: "no"}
		if r := storyWithdrawn(e.Reason); r != "" {
			w.Detail = sentenceCase(r) + "."
		}
		steps = append(steps, w)
	}
	if e.ConsumedAt != nil {
		steps = append(steps, storyStep{Title: "The agent collected it", At: e.ConsumedAt, Kind: "yes"})
	}
	if o := e.ToolOutcome; o != nil {
		at := o.At
		steps = append(steps, storyStep{Title: sentenceCase(toolOutcomeLabel(o)), Detail: toolOutcomeDetail(o), At: &at, Kind: storyOutcomeKind(o.Status)})
	} else if e.Status == "rejected" || e.Status == "expired" {
		steps = append(steps, storyStep{Title: "Nothing ran", Detail: "The tool was never called.", Kind: "done"})
	}
	return steps
}

func storyOutcomeKind(status string) string {
	if toolOutcomeWarns(status) {
		return "no"
	}
	return "done"
}

// storyCards are the Cards the call was outside of, each once.
func storyCards(e apiclient.CardException) []string {
	var out []string
	for _, b := range e.Breaches {
		if b.MandateID != "" && !slices.Contains(out, b.MandateID) {
			out = append(out, b.MandateID)
		}
	}
	return out
}

// waitingLine is the Waiting tab's count of what waits for an answer.
func waitingLine(n int) string {
	if n == 1 {
		return "1 call is waiting for an answer."
	}
	return strconv.Itoa(n) + " calls are waiting for an answer."
}

// workKindLabel names a domain approval or a blocked call on Needs you.
func workKindLabel(it apiclient.AttentionItem) string {
	if it.Kind == "approval" {
		return "Domain approval"
	}
	return "Blocked"
}

// approvalsHref is a page of domain approvals on Waiting; the first has
// none.
func approvalsHref(page int) string {
	if page <= 0 {
		return "/needs-you"
	}
	return "/needs-you?apage=" + strconv.Itoa(page)
}

// approvalsRange says which domain approvals a page shows.
func approvalsRange(v NeedsYouView) string {
	first := v.ApprovalsPage*warningsPerPage + 1
	return strconv.Itoa(first) + "–" + strconv.Itoa(first+len(v.Approvals)-1) + " of " + strconv.Itoa(v.ApprovalsOpen) + " domain approvals"
}
