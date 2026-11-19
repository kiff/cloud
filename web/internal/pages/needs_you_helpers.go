package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
)

func needsYouHref(id string) string { return "/needs-you/" + url.PathEscape(id) }

// NeedsYouHref is exported for the handlers and the bell.
func NeedsYouHref(id string) string { return needsYouHref(id) }

func exceptionWhen(e apiclient.CardException, loc *time.Location) string {
	t := e.CreatedAt
	if e.ResolvedAt != nil {
		t = *e.ResolvedAt
	}
	return t.In(loc).Format("2006-01-02 15:04 MST")
}

// exceptionAmount is what the action carries, in this order:
//   - its own amount parameter in the unit the action declares
//     ("25.50 USD"), which holds even when only a count held it;
//   - a summed breach in the unit its Card declares;
//   - when what the amount means could not be read, the stored number
//     exactly ("2,550"), never a guess;
//   - for an amount confirmed to declare no unit, as before ("€1,500"
//     for a refund in cents).
//
// A count's "1" is never the action's amount: a hold only a count caused,
// with no amount whose meaning is known, shows none.
func exceptionAmount(e apiclient.CardException) string {
	names := make([]string, 0, len(e.ParameterUnits))
	for k := range e.ParameterUnits {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		// The API stores a request's numeric parameters by lowercase name;
		// the action declares its own spelling (amountCents).
		if n, ok := e.Parameters[strings.ToLower(k)]; ok {
			u := e.ParameterUnits[k]
			return scaledNumber(n, u.Scale) + " " + u.Unit
		}
	}
	for _, withUnit := range []bool{true, false} {
		for _, b := range e.Breaches {
			if strings.HasPrefix(b.Quantity, "sum(") && (!withUnit || b.Unit != "") {
				return breachValue(b, b.Draw)
			}
		}
	}
	return ""
}

// breachValue is n in the breach's own terms: "25.50 EUR" with the unit
// and decimal places its Card declares; the stored number exactly when
// that could not be read or the Card's actions disagree on it;
// authorityValue's reading only when the Card confirms no unit.
func breachValue(b apiclient.CardBreach, n int64) string {
	switch {
	case b.Unit != "":
		return scaledNumber(n, b.Scale) + " " + b.Unit
	case b.UnitUnavailable, b.UnitAmbiguous:
		return scaledNumber(n, 0)
	}
	return authorityValue(n, b.Quantity)
}

// mixedUnitCards reports a breach on a Card whose actions disagree on
// what its amount means: that Card's totals are shown as stored numbers.
func mixedUnitCards(e apiclient.CardException) bool {
	for _, b := range e.Breaches {
		if b.UnitAmbiguous {
			return true
		}
	}
	return false
}

// amountsUnread reports a hold whose amounts could not be given a
// meaning: they are shown as stored numbers, and the page says so.
func amountsUnread(e apiclient.CardException) bool {
	if e.UnitsUnavailable {
		return true
	}
	for _, b := range e.Breaches {
		if b.UnitUnavailable {
			return true
		}
	}
	return false
}

// scaledNumber writes n with scale decimal places and thousands
// separators: 255000 at scale 2 is "2,550.00". Every digit is kept.
func scaledNumber(n int64, scale int) string {
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	s := strconv.FormatInt(n, 10)
	if scale <= 0 {
		return sign + commaNumber(n)
	}
	for len(s) <= scale {
		s = "0" + s
	}
	whole, _ := strconv.ParseInt(s[:len(s)-scale], 10, 64)
	return sign + commaNumber(whole) + "." + s[len(s)-scale:]
}

// breachAllows says what the Card allows on the line the action is
// outside of: "up to €500 per action" or "€30,000 per window, €29,450
// left".
// externalDecisionLabel is an external approver's answer in words.
func externalDecisionLabel(d string) string {
	switch d {
	case "approve":
		return "approved once"
	case "reject":
		return "rejected"
	case "defer":
		return "left it for a person"
	}
	return strings.ReplaceAll(d, "_", " ")
}

// breachLine is why an action is held, named as the Card's limit and in
// its unit: "25.00 EUR over this Card's 50.00 EUR per-call limit".
func breachLine(b apiclient.CardBreach) string {
	if b.Outcome == "resource_not_on_card" {
		if b.Value == "" {
			return "No " + b.Resource + " named, and this Card applies only to the ones it lists"
		}
		return b.Resource + " " + b.Value + " is not one this Card applies to"
	}
	if b.Outcome == "per_action_exceeded" {
		return breachValue(b, b.Over()) + " over this Card’s " + breachValue(b, b.Limit) + " per-call limit"
	}
	return breachValue(b, b.Over()) + " past this Card’s " + breachValue(b, b.Limit) + " total"
}

func breachAllows(b apiclient.CardBreach) string {
	if b.Outcome == "resource_not_on_card" {
		return "this action only for the " + b.Resource + " values it lists"
	}
	if b.Outcome == "per_action_exceeded" {
		return "up to " + breachValue(b, b.Limit) + " per action"
	}
	left := b.Limit - b.Used
	if left < 0 {
		left = 0
	}
	return breachValue(b, b.Limit) + " in its window, " + breachValue(b, left) + " left"
}

func exceptionStatusLabel(e apiclient.CardException) string {
	switch e.Status {
	case "held":
		return "Needs you"
	case "approved_no_room":
		return "Approved, but no room"
	case "unavailable":
		return "Current Card room unavailable"
	case "approved":
		return "Approved once, not used yet"
	case "consumed":
		// The approval was used; what the tool did with the call is its
		// own fact, never implied by the approval.
		if l := toolOutcomeLabel(e.ToolOutcome); l != "" {
			return "Approved once, " + l
		}
		return "Approved once, used"
	case "rejected":
		return "Rejected"
	case "changed":
		return "Changed the Card"
	case "invalidated":
		return "Withdrawn"
	case "expired":
		return "Expired"
	}
	return e.Status
}

func exceptionReasonLabel(reason string) string {
	switch reason {
	case "card_revoked":
		return "Withdrawn: the Card was revoked before the approval was used."
	case "holder_changed":
		return "Withdrawn: the Card moved to another agent before the approval was used."
	case "card_expired":
		return "Withdrawn: the Card expired before the approval was used."
	case "not answered before the hold expired":
		return "Expired: nobody answered before the hold ran out, so nothing was authorized. The agent has to ask again."
	}
	return reason
}

// FillBreachWindows names, for each total an action is past, the window
// of that total, read from the Card. A Card with two totals on the same
// quantity is matched by the limit too; when it still can't tell, the
// window stays unnamed.
func FillBreachWindows(e *apiclient.CardException, cards []apiclient.Mandate) {
	for i, b := range e.Breaches {
		if b.Outcome != "limit_reached" || b.Window != "" {
			continue
		}
		var windows []string
		for _, m := range cards {
			if m.ID != b.MandateID {
				continue
			}
			for _, a := range m.Aggregates {
				if a.Quantity == b.Quantity {
					windows = append(windows, a.Window)
				}
			}
			if len(windows) > 1 {
				windows = windows[:0]
				for _, a := range m.Aggregates {
					if a.Quantity == b.Quantity && a.Limit == b.Limit {
						windows = append(windows, a.Window)
					}
				}
			}
		}
		if len(windows) == 1 {
			e.Breaches[i].Window = windows[0]
		}
	}
}

// totalName is a Card total by its window: "daily total".
func totalName(window string) string {
	switch window {
	case "calendar_day":
		return "daily total"
	case "rolling_24h":
		return "total for any 24 hours"
	case "rolling_1h":
		return "total for any hour"
	case "rolling_7d":
		return "total for any 7 days"
	case "lifetime":
		return "whole-life total"
	}
	return ""
}

// pastTotalHint says why a held call has no Approve once: it is past one
// of the Card's totals, which only more room can cover. Only for a
// confirmed cumulative breach: a missing button for another reason (a
// role, unreadable room) is not explained as this.
func pastTotalHint(e apiclient.CardException) string {
	if e.Status != "held" || e.RoomUnavailable || e.MayApproveOnce() || !e.HasCumulativeBreach() {
		return ""
	}
	var names []string
	unnamed := false
	for _, b := range e.Breaches {
		if b.Outcome != "limit_reached" {
			continue
		}
		if n := totalName(b.Window); n == "" {
			unnamed = true
		} else if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	past, that := "one of the Card's totals", "that total"
	switch {
	case len(names) == 1 && !unnamed:
		past = "the Card's " + names[0]
	case len(names) > 1 && !unnamed:
		past, that = "the Card's "+strings.Join(names, " and its "), "those totals"
	case len(names) > 0:
		past, that = "more than one of the Card's totals", "those totals"
	}
	return "This call is past " + past + ". Approve once covers only a per-call limit or a resource the Card doesn't list, so it needs more room on " + that + " first."
}

// answerByLabel is when a held action stops waiting for an answer, in
// the account's time zone and labelled with it: in UTC it read as
// already past to an owner ahead of UTC. Empty for one held before
// holds expired.
func answerByLabel(e apiclient.CardException, loc *time.Location) string {
	if e.ExpiresAt == nil {
		return ""
	}
	return e.ExpiresAt.In(loc).Format("2 Jan 15:04 MST")
}

// SplitExceptions separates what is waiting from what was answered,
// oldest waiting first (the one waiting longest is at the top) and most
// recently answered first, keeping at most limit answered.
func SplitExceptions(es []apiclient.CardException, limit int) (held, answered []apiclient.CardException) {
	for _, e := range es {
		if e.NeedsOwner() {
			held = append(held, e)
		} else {
			answered = append(answered, e)
		}
	}
	sort.SliceStable(held, func(i, j int) bool { return held[i].CreatedAt.Before(held[j].CreatedAt) })
	sort.SliceStable(answered, func(i, j int) bool { return resolvedAt(answered[i]).After(resolvedAt(answered[j])) })
	if len(answered) > limit {
		answered = answered[:limit]
	}
	return held, answered
}

func resolvedAt(e apiclient.CardException) time.Time {
	if e.ResolvedAt != nil {
		return *e.ResolvedAt
	}
	return e.CreatedAt
}

// HeldForCard is the held exceptions that name a Card.
func HeldForCard(es []apiclient.CardException, cardID string) []apiclient.CardException {
	var out []apiclient.CardException
	for _, e := range es {
		if !e.NeedsOwner() {
			continue
		}
		for _, b := range e.Breaches {
			if b.MandateID == cardID {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// ExceptionSummary is one line for a list or the bell: "refund-agent
// wants to REFUND_ORDER €1,500, outside card-refunds".
func ExceptionSummary(e apiclient.CardException) string {
	cards := make([]string, 0, len(e.Breaches))
	for _, b := range e.Breaches {
		cards = append(cards, b.MandateID)
	}
	line := e.Subject + " wants to " + e.Action
	if amt := exceptionAmount(e); amt != "" {
		line += " " + amt
	}
	return line + ", outside " + strings.Join(cards, ", ")
}

func outsideCardsLine(n int) string {
	if n == 1 {
		return "1 action outside a Card is waiting for your answer"
	}
	return strconv.Itoa(n) + " actions outside a Card are waiting for your answer"
}

// exceptionStateClass picks the pill colour: amber while it waits, green
// once it ran, neutral otherwise.
func exceptionStateClass(e apiclient.CardException) string {
	if e.ToolOutcome != nil && toolOutcomeWarns(e.ToolOutcome.Status) {
		return "dash-portfolio-state-observe"
	}
	switch e.Status {
	case "held", "approved_no_room", "unavailable":
		return "dash-portfolio-state-observe"
	case "approved", "consumed", "changed":
		return "dash-portfolio-state-enforce"
	}
	return "dash-portfolio-state-active"
}

func needsYouCount(n int) string {
	if n == 0 {
		return "nothing waiting"
	}
	return strconv.Itoa(n) + " waiting"
}

// sentenceCase capitalises the first letter of an API note, which is
// written to follow other text ("…: every movement here…") but is shown
// on its own here.
func sentenceCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// needsYouTab is the sidebar item Needs you lights: its own in a
// personal account, Cards in a company's.
func needsYouTab(ctx context.Context) string {
	if components.IsPersonal(ctx) {
		return "needs-you"
	}
	return "cards"
}

// sharedCardsNote warns that changing a shared Card changes it for every
// agent holding it; "" when no Card the action is outside of is shared.
func sharedCardsNote(e apiclient.CardException, sharedBy map[string]int) string {
	var parts []string
	seen := map[string]bool{}
	for _, b := range e.Breaches {
		if n := sharedBy[b.MandateID]; n > 1 && !seen[b.MandateID] {
			seen[b.MandateID] = true
			parts = append(parts, b.MandateID+" is shared by "+strconv.Itoa(n)+" agents")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Changing the Card changes it for every agent that holds it: " + strings.Join(parts, "; ") + "."
}

// SharedCards counts the holders of each shared Card.
func SharedCards(cards []apiclient.Mandate) map[string]int {
	out := map[string]int{}
	for _, m := range cards {
		if m.Shared() {
			out[m.ID] = len(m.Holders())
		}
	}
	return out
}

// answeredByLine says who answered a held action and in which capacity,
// e.g. "Answered by ana@acme.test, an approver, with separation of duties
// on." Empty when the action is not answered. callArg is one argument of
// a held call, for the page. Value is the argument as JSON, exactly as
// the tool will receive it: a string in quotes with its spaces and
// escapes, a number with every digit. Shortened: Value is a preview, and
// the full value is on the detail.
type callArg struct {
	Name, Value string
	Shortened   bool
	// Means is an integer argument read in the unit the action declares
	// for it: "25.50 EUR" next to 2550.
	Means string
}

// maxArgPreview keeps one argument short in the list. The detail shows
// every value in full (a review: two values that differ after the cut
// must not look the same where the answer is given).
const maxArgPreview = 160

// callArgs are a held call's arguments by name. full is the detail page,
// where nothing is shortened. The agent wrote them, so they are shown as
// data and never as the page's own words.
func callArgs(e apiclient.CardException, full bool) []callArg {
	names := make([]string, 0, len(e.Arguments))
	for k := range e.Arguments {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]callArg, 0, len(names))
	for _, k := range names {
		var b bytes.Buffer
		v := string(e.Arguments[k])
		if json.Compact(&b, e.Arguments[k]) == nil {
			v = b.String()
		}
		a := callArg{Name: k, Value: v}
		if u, ok := e.ParameterUnits[k]; ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				a.Means = scaledNumber(n, u.Scale) + " " + u.Unit
			}
		}
		if r := []rune(v); !full && len(r) > maxArgPreview {
			a.Value, a.Shortened = string(r[:maxArgPreview-1])+"…", true
		}
		out = append(out, a)
	}
	return out
}

// anyShortened reports whether a preview left part of a value out.
func anyShortened(args []callArg) bool {
	for _, a := range args {
		if a.Shortened {
			return true
		}
	}
	return false
}

// isCallKey reports KIFF's own id for a gateway call ("tc-…"), which a
// held call carries as its entity: an id, not what the call is about.
func isCallKey(id string) bool { return strings.HasPrefix(id, "tc-") }

// callIDOf is KIFF's id for the held gateway call.
func callIDOf(e apiclient.CardException) string {
	if isCallKey(e.EntityID) {
		return e.EntityID
	}
	return e.ProposalID
}

// NameAnswerer replaces the sign-in id that answered e with a person:
// "you (email)" for the person looking, a member's email when the
// account's members were read, and the id otherwise. The API records
// the sign-in subject, which read as "user_3KQe…" (onboarding test,
// 2026-10-08).
func NameAnswerer(e *apiclient.CardException, mySubject, myEmail string, members []apiclient.Member) {
	if e == nil || e.ResolvedBy == "" {
		return
	}
	if e.ExternalAnswer != nil && e.ResolvedBy == e.ExternalAnswer.Principal {
		e.ResolvedBy = externalAnswerName(e.ExternalAnswer) + " (external approver)"
		return
	}
	if e.ResolvedBy == mySubject && mySubject != "" {
		if myEmail != "" {
			e.ResolvedBy = "you (" + myEmail + ")"
		} else {
			e.ResolvedBy = "you"
		}
		return
	}
	for _, m := range members {
		if m.Subject == e.ResolvedBy && m.Email != "" {
			e.ResolvedBy = m.Email
			return
		}
	}
}

func answeredByLine(e apiclient.CardException) string {
	if e.ResolvedAt == nil || e.ResolvedBy == "" {
		return ""
	}
	line := "Answered by " + e.ResolvedBy
	if e.Resolution == "change-card" {
		line = "Card changed by " + e.ResolvedBy
	}
	switch e.ResolvedRole {
	case "admin", "approver":
		line += ", an " + e.ResolvedRole
	case "editor":
		line += ", an editor"
	case "":
		if e.ResolvedUnderSoD != nil {
			line += ", with an API key"
		}
	}
	if e.ResolvedUnderSoD != nil {
		if *e.ResolvedUnderSoD {
			line += ", with separation of duties on"
		} else {
			line += ", with separation of duties off"
		}
	}
	return line + "."
}

// toolOutcomeLabel says how a call ended at the tool, in a few words that
// follow a decision: a refusal or an unknown result never reads as done.
// Empty when there is no record.
func toolOutcomeLabel(o *apiclient.ToolOutcome) string {
	if o == nil {
		return ""
	}
	switch o.Status {
	case "done":
		return "the tool reported it done"
	case "refused_by_tool":
		if o.ByConnection() {
			return "refused by the connection before sending"
		}
		return "refused by the tool"
	case "unknown":
		return "outcome unknown"
	case "not_sent":
		return "not sent to the tool"
	case "sending":
		return "sent, waiting for the tool"
	case "not_recorded":
		return "outcome not recorded"
	}
	return ""
}

// toolOutcomeDetail is an outcome's message for a timeline: only an app's
// own error is quoted, as the app's. A connection's text and the gateway's
// (never sent, an answer lost) are English meant for the record: the label
// says what happened, in KIFF's words, and nothing of theirs is quoted.
func toolOutcomeDetail(o *apiclient.ToolOutcome) string {
	if o == nil || o.Message == "" || o.Status != "refused_by_tool" || o.ByConnection() {
		return ""
	}
	return "The tool said: “" + o.Message + "”"
}

// toolOutcomeWarns is true for an outcome that is not the tool's success.
func toolOutcomeWarns(status string) bool {
	return status == "refused_by_tool" || status == "unknown" || status == "not_sent" || status == "not_recorded"
}

// toolOutcomeLine is the detail line: what the tool reported, in its own
// words when it refused, or the gateway's explanation.
func toolOutcomeLine(o *apiclient.ToolOutcome) string {
	if o == nil {
		return ""
	}
	switch o.Status {
	case "done":
		return "The tool reported the call done."
	case "refused_by_tool":
		if o.ByConnection() {
			// The connection's own text is never quoted.
			return "The connection refused it before sending, so nothing reached the tool."
		}
		// The message is the tool's own error, which may itself say
		// "not sent": quote it as the tool's, not as KIFF's.
		if o.Message == "" {
			return "The tool refused the call."
		}
		return "The tool refused the call. Its error: “" + o.Message + "”"
	case "unknown", "not_sent":
		// KIFF's words by status, the answered story's own, never the
		// gateway's sentence as written.
		return sentenceCase(toolOutcomeLabel(o)) + "."
	case "sending":
		return "Sent to the tool; its answer is not in yet."
	case "not_recorded":
		// a review: a call from before KIFF recorded outcomes, or from
		// an older gateway, whose result is no longer kept. Nothing
		// shows whether the tool did it.
		return "Sent to the tool, but how it ended wasn't recorded. Check the tool."
	}
	return ""
}

// exceptionAnswerLabel is the owner's answer alone, for the Answered
// table's Answer column. What the tool then did is its own column
// (answeredOutcomeLabel), so neither is squeezed into one pill.
func exceptionAnswerLabel(e apiclient.CardException) string {
	switch e.Status {
	case "approved", "consumed":
		return "Approved once"
	case "rejected":
		return "Rejected"
	case "changed":
		return "Card changed"
	case "invalidated":
		return "Withdrawn"
	case "expired":
		return "Expired"
	case "held":
		return "Needs you"
	}
	return e.Status
}

// answeredOutcomeLabel is what came of the answer: whether the agent used
// the approval and what the tool reported. A refusal or an unknown result
// never reads as done.
func answeredOutcomeLabel(e apiclient.CardException) string {
	switch e.Status {
	case "approved":
		return "Not used yet"
	case "consumed":
		if l := toolOutcomeLabel(e.ToolOutcome); l != "" {
			return sentenceCase(strings.TrimPrefix(l, "the "))
		}
		return "Used"
	case "changed":
		// A retry after Change Card is decided afresh and can still be
		// refused by the tool or lose its answer, so a recorded outcome
		// is what happened. Without one, the change itself is not
		// authorization: the next attempt is checked against the Card
		// as it is then.
		if l := toolOutcomeLabel(e.ToolOutcome); l != "" {
			return sentenceCase(strings.TrimPrefix(l, "the "))
		}
		return "Card changed; a retry is decided again"
	case "rejected":
		if r := e.Refusal; r != nil && r.NextStep != "" {
			return "Agent told: " + NextStepLabel(r.NextStep) + " · nothing ran"
		}
		return "Nothing ran"
	case "expired":
		return "Nothing ran"
	case "invalidated":
		return "Not used"
	}
	return ""
}

// answeredCard is the Card the action was outside of, or "" when none
// is named.
func answeredCard(e apiclient.CardException) string {
	if len(e.Breaches) == 0 {
		return ""
	}
	return e.Breaches[0].MandateID
}

// exceptionWhenShort is the answer's time for a table row: "8 Oct 09:46",
// in UTC like the rest of the page.
func exceptionWhenShort(e apiclient.CardException, loc *time.Location) string {
	return resolvedAt(e).In(loc).Format("2 Jan 15:04")
}

// AnsweredFilter narrows the Answered table: by answer, by the tool's
// outcome ("tool-problem": refused, unknown, not sent or not recorded),
// and by agent.
type AnsweredFilter struct {
	Answer, Agent string
}

// answeredFilters are the Answer chips, in order.
var answeredFilters = []struct{ Value, Label string }{
	{"", "All"},
	{"approved", "Approved once"},
	{"tool-problem", "Tool did not confirm"},
	{"rejected", "Rejected"},
	{"expired", "Expired"},
	{"changed", "Changed the Card"},
	{"withdrawn", "Withdrawn"},
}

// NormalizeAnsweredFilter keeps only a known answer; the agent is kept as
// given and matched exactly.
func NormalizeAnsweredFilter(answer, agent string) AnsweredFilter {
	f := AnsweredFilter{Agent: strings.TrimSpace(agent)}
	for _, c := range answeredFilters {
		if c.Value == answer {
			f.Answer = answer
		}
	}
	return f
}

// Matches reports whether an answered action passes the filter.
func (f AnsweredFilter) Matches(e apiclient.CardException) bool {
	if f.Agent != "" && e.Subject != f.Agent {
		return false
	}
	switch f.Answer {
	case "approved":
		return e.Status == "approved" || e.Status == "consumed"
	case "tool-problem":
		return e.ToolOutcome != nil && toolOutcomeWarns(e.ToolOutcome.Status)
	case "withdrawn":
		return e.Status == "invalidated"
	case "":
		return true
	}
	return e.Status == f.Answer
}

// Query is the filter as a query string, "" when it is empty.
func (f AnsweredFilter) Query() string {
	q := url.Values{}
	if f.Answer != "" {
		q.Set("answer", f.Answer)
	}
	if f.Agent != "" {
		q.Set("agent", f.Agent)
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

func (f AnsweredFilter) with(answer, agent string) string {
	return needsYouTabHref(NeedsYouAnswered, AnsweredFilter{Answer: answer, Agent: agent})
}

// FilterAnswered keeps the answered actions that pass f.
func FilterAnswered(es []apiclient.CardException, f AnsweredFilter) []apiclient.CardException {
	var out []apiclient.CardException
	for _, e := range es {
		if e.Status != "held" && f.Matches(e) {
			out = append(out, e)
		}
	}
	return out
}

// AnsweredAgents are the agents with an answered action, for the agent
// chips, sorted.
func AnsweredAgents(es []apiclient.CardException) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range es {
		if e.Status != "held" && e.Subject != "" && !seen[e.Subject] {
			seen[e.Subject] = true
			out = append(out, e.Subject)
		}
	}
	sort.Strings(out)
	return out
}

// exceptionAnswerClass colours the answer pill: green for an approval or
// a changed Card, neutral for a refusal, an expiry or a withdrawal.
func exceptionAnswerClass(e apiclient.CardException) string {
	switch e.Status {
	case "held", "approved_no_room", "unavailable":
		return "dash-portfolio-state-observe"
	case "approved", "consumed", "changed":
		return "dash-portfolio-state-enforce"
	}
	return "dash-portfolio-state-active"
}

// refusalLine is what a rejection told the agent to do instead, or "".
func refusalLine(e apiclient.CardException) string {
	r := e.Refusal
	if r == nil || (r.NextStep == "" && r.Note == "") {
		return ""
	}
	line := "Refused"
	if r.NextStep != "" {
		line += ", with the next step: " + NextStepLabel(r.NextStep)
	}
	if r.Note != "" {
		line += ". Note to the agent: \u201c" + r.Note + "\u201d"
	}
	return line + "."
}
