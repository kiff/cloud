package pages

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// The Card page is four tabs: Overview fits one screen, Settings is a
// list of rows that each open one step of the Card's walkthrough,
// Approvers, and History, which holds the four record views behind one
// segmented control. The record views keep their own URLs.

// cardHistoryTabs are History's segments, each its own route.
var cardHistoryTabs = []struct{ ID, Label, Suffix string }{
	{"decisions", "Decisions", "/decisions"},
	{"statement", "Statement", "/statement"},
	{"approved", "Approved once", "/approved"},
	{"changes", "Changes", "/changes"},
}

// cardInHistory reports whether tab is one of History's segments.
func cardInHistory(tab string) bool {
	for _, t := range cardHistoryTabs {
		if t.ID == tab {
			return true
		}
	}
	return false
}

func cardHref(m apiclient.Mandate, suffix string) string {
	return "/cards/" + url.PathEscape(m.ID) + suffix
}

// cardEditHref opens the Card's walkthrough on one step, then its review.
// Steps: an issued Card is agent, actions, limit, past it, review; a tool
// Card is agent, limit, past it, review.
func cardEditHref(m apiclient.Mandate, what string) string {
	tool := map[string]string{"limit": "2", "past": "3", "review": "4"}
	card := map[string]string{"actions": "2", "limit": "3", "past": "4", "review": "5"}
	if IsToolCard(m) {
		step, ok := tool[what]
		if !ok {
			step = "4"
		}
		return toolCardEditHref(m) + "&step=" + step + "&one=1"
	}
	return "/cards/new?card=" + url.QueryEscape(m.ID) + "&step=" + card[what] + "&one=1"
}

// settingsRow is one Settings row: an icon, what it is, its value now,
// and where changing it happens.
type settingsRow struct {
	Label, Value, Href, Icon string
	Mono                     bool
}

type settingsGroup struct {
	Title string
	Rows  []settingsRow
}

// Row icons: 24px stroke paths.
const (
	iconGauge    = "M12 14l4-4M3.3 19a10 10 0 1 1 17.4 0"
	iconCoin     = "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM9 10h4.5a1.5 1.5 0 0 1 0 3H10a1.5 1.5 0 0 0 0 3h5M12 7v2M12 16v2"
	iconShield   = "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"
	iconList     = "M10 6h10M10 12h10M10 18h10M4 6l1 1 2-2M4 12l1 1 2-2M4 18l1 1 2-2"
	iconFilter   = "M22 3H2l8 9.5V19l4 2v-8.5L22 3z"
	iconCal      = "M8 2v4M16 2v4M3 10h18M5 4h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2z"
	iconPerson   = "M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2M12 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8z"
	iconPlus     = "M12 5v14M5 12h14"
	iconShare    = "M4 12v8h16v-8M12 3v13M7 8l5-5 5 5"
	iconBan      = "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM5.6 5.6l12.8 12.8"
	iconTool     = "M14.7 6.3a4 4 0 0 0 5 5L22 14l-8 8-2.3-2.3a4 4 0 0 0-5-5L2 10l8-8z"
	iconPulse    = "M22 12h-4l-3 9L9 3l-3 9H2"
	iconKey      = "M15.5 7.5 19 4M21 2l-2 2 3 3-3 3-3-3M11.4 12.6a5.5 5.5 0 1 1-7.8 7.8 5.5 5.5 0 0 1 7.8-7.8z"
	iconLink     = "M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"
	iconTerminal = "m4 17 6-6-6-6M12 19h8"
)

// cardSettingsGroups are the Card's terms as rows, in the order someone
// reads them: limits, what it covers, who holds it.
func cardSettingsGroups(v CardDetailView) []settingsGroup {
	m := *v.Card
	live := m.RevokedAt == nil
	href := func(h string) string {
		if !live {
			return ""
		}
		return h
	}
	limits := []settingsRow{{Label: "Total", Value: cardTotalLabel(v.Balance), Href: href(cardEditHref(m, "limit")), Icon: iconGauge}}
	if per := cardPerCallLabel(m, v.Balance); per != "" {
		limits = append(limits, settingsRow{Label: "Most in one call", Value: per, Href: href(cardEditHref(m, "limit")), Icon: iconCoin})
	}
	if zone := cardDayZone(m); zone != "" {
		limits = append(limits, settingsRow{Label: "Day boundary", Value: "Midnight · " + zone, Icon: iconCal})
	}
	limits = append(limits, settingsRow{Label: "Past the limit", Value: cardPastLabel(m), Href: href(cardEditHref(m, "past")), Icon: iconShield})
	if live {
		limits = append(limits, settingsRow{Label: "Extra room", Value: "Review", Href: cardHref(m, "/extra-room"), Icon: iconPlus})
	}
	warningValue, warningHref := "Admin only", ""
	if v.WarningAdmin {
		warningValue = "Unavailable"
		warningHref = href(cardHref(m, "/warnings"))
		if v.WarningConfig != nil {
			warningValue = warningSettingsLabel(v.WarningConfig.Settings)
		}
	}
	limits = append(limits, settingsRow{Label: "Budget warnings", Value: warningValue, Href: warningHref, Icon: iconPulse})
	covers := []settingsRow{}
	if IsToolCard(m) {
		covers = append(covers, settingsRow{Label: "Tool", Value: toolCardTool(m), Href: toolHref(toolCardTool(m)), Icon: iconTool, Mono: true})
	} else {
		covers = append(covers,
			settingsRow{Label: "Actions", Value: cardActions(m), Href: href(cardEditHref(m, "actions")), Icon: iconList, Mono: true},
			settingsRow{Label: "Only when", Value: cardStatesLabel(m), Href: href(cardEditHref(m, "review")), Icon: iconFilter})
	}
	// A tool Card's scope is set on its own sheet; any other Card's
	// is read only here, and every edit carries it unchanged.
	if scope := cardResourcesLabel(m); IsToolCard(m) && len(m.Grants) == 1 {
		row := settingsRow{Label: "Only for", Value: scope, Href: href(cardHref(m, "/only-for")), Icon: iconFilter, Mono: scope != ""}
		if scope == "" {
			row.Value = "Any"
		}
		covers = append(covers, row)
	} else if scope != "" {
		covers = append(covers, settingsRow{Label: "Only for", Value: scope, Icon: iconFilter, Mono: true})
	}
	// Read only as well: set through the API, kept by every edit.
	if steps := cardNextStepsLabel(m); steps != "" {
		covers = append(covers, settingsRow{Label: "Other answers", Value: steps, Icon: iconList})
	}
	ends := settingsRow{Label: "Ends", Value: cardEndsLabel(m), Icon: iconCal}
	if !IsToolCard(m) {
		ends.Href = href(cardEditHref(m, "review"))
	}
	covers = append(covers, ends)
	holders := strings.Join(m.Holders(), ", ")
	who := []settingsRow{{Label: "Agent", Value: holders, Href: href(cardHref(m, "/holder")), Icon: iconPerson, Mono: true}}
	if m.Domain != "" {
		who = append(who, settingsRow{Label: "Its activity", Value: "Open", Href: cardActivityHref(m), Icon: iconPulse})
	}
	return []settingsGroup{{"Limits", limits}, {"What it covers", covers}, {"Who holds it", who}}
}

// cardResourcesLabel names the resources a Card's grants are limited to,
// "order_id ord_1, ord_2"; "" when none is limited.
func cardResourcesLabel(m apiclient.Mandate) string {
	var out []string
	for _, g := range m.Grants {
		if r := g.Resources; r != nil {
			out = append(out, r.Parameter+" "+strings.Join(r.Values, ", "))
		}
	}
	return strings.Join(out, "; ")
}

// cardNextStepsLabel lists the answers besides approve and reject that a
// held call under this Card can get: "Return first, Proof first".
func cardNextStepsLabel(m apiclient.Mandate) string {
	var out []string
	for _, g := range m.Grants {
		for _, s := range g.NextSteps {
			if l := NextStepLabel(s); !slices.Contains(out, l) {
				out = append(out, l)
			}
		}
	}
	return strings.Join(out, ", ")
}

// cardTotalLabel is the Card's first readable limit: "1,000.00 EUR per
// calendar day", or "2 per calendar day" for a count.
func cardTotalLabel(b CardBalance) string {
	ls := b.Readable()
	if len(ls) == 0 {
		return "Unavailable"
	}
	return balanceValue(ls[0], ls[0].Limit) + " " + windowLabel(ls[0].Window)
}

// balanceValue is an amount in the limit's own unit when the API resolved
// one, else as the Card image shows it (€ for cents, a bare number).
func balanceValue(l apiclient.MandateLimit, n int64) string {
	if l.Unit != "" {
		return scaledNumber(n, l.Scale) + " " + l.Unit
	}
	return authorityValue(n, l.Quantity)
}

// cardPerCallLabel is the Card's per-action maximum in its unit, read from
// the limit that adds up the same parameter. Empty when it has none.
func cardPerCallLabel(m apiclient.Mandate, b CardBalance) string {
	var out []string
	for _, g := range m.Grants {
		for _, p := range g.PerActionMax {
			out = append(out, perActionValue(p.Quantity, p.Max, b))
		}
	}
	return strings.Join(out, ", ")
}

func perActionValue(quantity string, n int64, b CardBalance) string {
	for _, l := range b.Limits {
		if strings.EqualFold(l.Quantity, quantity) && l.Unit != "" {
			return scaledNumber(n, l.Scale) + " " + l.Unit
		}
	}
	if strings.Contains(strings.ToLower(quantity), "cent") {
		return authorityValue(n, quantity)
	}
	return strconv.FormatInt(n, 10) + " " + strings.TrimSuffix(strings.TrimPrefix(quantity, "sum("), ")")
}

// cardPastLabel is the Card's route past its limits, as the walkthrough
// names it: "Ask · 10 min" or "Stop".
func cardPastLabel(m apiclient.Mandate) string {
	if !m.ComesHome() {
		return "Stop"
	}
	for _, o := range wkHoldChoices(HoldFromSeconds(m.HoldExpirySeconds)) {
		if o.Checked {
			return "Ask · " + o.Label
		}
	}
	return "Ask"
}

// cardRouteConsequence is what happens to a call past the Card, in plain
// words beside its Stop or Ask badge: "Refused outside limits." or "Held
// for an authorized approver · 10 min".
func cardRouteConsequence(m apiclient.Mandate) string {
	if !m.ComesHome() {
		return "Refused outside limits."
	}
	for _, o := range wkHoldChoices(HoldFromSeconds(m.HoldExpirySeconds)) {
		if o.Checked {
			return "Held for an authorized approver · " + o.Label
		}
	}
	return "Held for an authorized approver"
}

// cardActionsShort is the Card's middle line: one action, or the first
// and how many more.
func cardActionsShort(m apiclient.Mandate) string {
	if IsToolCard(m) {
		return toolCardTool(m)
	}
	switch len(m.Grants) {
	case 0:
		return "No actions"
	case 1:
		return m.Grants[0].Action
	}
	return m.Grants[0].Action + " +" + strconv.Itoa(len(m.Grants)-1)
}

// cardLimitLine is the Card's limit as its bottom line reads: "1,000.00
// EUR a day · ≤ 50.00 EUR a call". From the statement when it was read,
// else from the Card's own terms.
func cardLimitLine(m apiclient.Mandate, b CardBalance) string {
	short := map[string]string{"calendar_day": "a day", "rolling_24h": "in any 24 h", "rolling_1h": "an hour", "rolling_7d": "in any 7 days", "lifetime": "in total"}
	var parts []string
	if ls := b.Readable(); len(ls) > 0 {
		for _, l := range ls {
			parts = append(parts, balanceValue(l, l.Limit)+" "+short[l.Window])
		}
		for _, l := range b.Limits {
			if l.PerItem() {
				parts = append(parts, balanceValue(l, l.Limit)+" "+short[l.Window]+eachItem(l.Per))
			}
		}
	} else {
		for _, a := range m.Aggregates {
			parts = append(parts, authorityValue(a.Limit, a.Quantity)+" "+short[a.Window]+eachItem(a.Per))
		}
	}
	s := strings.Join(parts, " · ")
	if per := cardPerCallLabel(m, b); per != "" && !strings.Contains(per, ",") {
		if s != "" {
			s += " · "
		}
		s += "≤ " + per + " a call"
	}
	if s == "" {
		return "—"
	}
	return s
}

func itoa(n int) string { return strconv.Itoa(n) }

// cardRouteTag is the Card's route as its tag reads: ASK or STOP.
func cardRouteTag(m apiclient.Mandate) string {
	if m.ComesHome() {
		return "ASK"
	}
	return "STOP"
}

func cardStatesLabel(m apiclient.Mandate) string {
	for _, g := range m.Grants {
		if len(g.EntityStates) > 0 {
			return strings.Join(g.EntityStates, ", ")
		}
	}
	return "Any state"
}

func cardEndsLabel(m apiclient.Mandate) string {
	if m.RevokedAt != nil {
		return "Revoked " + m.RevokedAt.Format("2 Jan 2006")
	}
	if m.ValidUntil == nil {
		return "No end date"
	}
	return m.ValidUntil.UTC().Format("2 Jan 2006")
}

// cardBalanceLine is the line under the Overview's big number: what the
// number is out of, and when it starts again.
func cardBalanceLine(l apiclient.MandateLimit) string {
	s := "left of " + balanceValue(l, l.Limit) + " " + windowLabel(l.Window)
	if r := resetIn(l); r != "" {
		s += " · resets " + r
	}
	return s
}

// approverTone is the badge colour of an approver's status.
func approverTone(g apiclient.ApproverGrant) string {
	switch g.ConnectionStatus {
	case "connected":
		return "ok"
	case "awaiting_connection":
		return "warn"
	case "not_responding":
		return "bad"
	}
	return "off"
}

// approverHint is the one thing to do about a status, when there is one.
func approverHint(g apiclient.ApproverGrant) string {
	switch g.ConnectionStatus {
	case "awaiting_connection":
		return "Start the connector with its credential. It turns Connected once it checks in."
	case "not_responding":
		return "It hasn’t checked in for over two minutes. Held calls wait in Needs you for a signed-in person who may approve."
	case "inactive":
		return "The Card changed, so this approver no longer applies. Revoke it and connect a new one."
	}
	return ""
}

func approverLive(g apiclient.ApproverGrant) bool {
	return g.RevokedAt == nil && g.ConnectionStatus != "expired" && g.ConnectionStatus != "revoked"
}

// ApproverExpiry is one end offered for a new approver. Values are the
// UTC datetime the form posts; the API refuses more than 90 days.
type ApproverExpiry struct {
	Value, Label, Hint string
	Checked            bool
}

// ApproverExpiries are the suggested ends: a week (the default), a month,
// and the longest the API allows, kept a few minutes inside it so a form
// left open still passes.
func ApproverExpiries(now time.Time) []ApproverExpiry {
	now = now.UTC()
	at := func(d time.Duration) time.Time { return now.Add(d) }
	f := func(t time.Time) string { return t.Format("2006-01-02T15:04") }
	week, month, longest := at(7*24*time.Hour), at(30*24*time.Hour), at(90*24*time.Hour-10*time.Minute)
	return []ApproverExpiry{
		{f(week), "In 7 days", week.Format("2 Jan 2006") + " · suggested", true},
		{f(month), "In 30 days", month.Format("2 Jan 2006"), false},
		{f(longest), "In 90 days", longest.Format("2 Jan 2006") + " · the longest an approver can last", false},
	}
}

// approverDateMax is the last day the date field offers: inside 90 days
// even at 23:59 UTC.
func approverDateMax(now time.Time) string {
	return now.UTC().Add(89 * 24 * time.Hour).Format("2006-01-02")
}

// Key is how the connect form names a scope in one field: the action,
// the parameter it adds up, and the unit and scale shown, which the
// handler checks again against the Card.
func (s ApproverScope) Key() string {
	return s.Action + "|" + s.Parameter + "|" + s.Unit + "|" + strconv.Itoa(s.Scale)
}

// SuggestedLabel is the Card's own per-action threshold in its unit.
func (s ApproverScope) SuggestedLabel() string { return s.Suggested() + " " + s.UnitLabel() }

// Input is the threshold as the amount field takes it back: digits and
// one decimal point at the scope's precision, no grouping, since
// parseApproverAmount accepts nothing else.
func (s ApproverScope) Input() string {
	if s.Scale <= 0 {
		return strconv.FormatInt(s.PerAction, 10)
	}
	p := int64(1)
	for i := 0; i < s.Scale; i++ {
		p *= 10
	}
	frac := strconv.FormatInt(s.PerAction%p, 10)
	for len(frac) < s.Scale {
		frac = "0" + frac
	}
	return strconv.FormatInt(s.PerAction/p, 10) + "." + frac
}

// UnitLabel is the unit an amount for this scope is entered in: the
// resolved unit, else the parameter it adds up.
func (s ApproverScope) UnitLabel() string {
	if s.Unit != "" {
		return s.Unit
	}
	return s.Parameter
}

// onlyForConsequence says what happens to a call outside the scope, as the
// Card's own past-the-limit setting decides.
func onlyForConsequence(m apiclient.Mandate) string {
	if m.ComesHome() {
		return "The agent may act only on the values listed here. A call on any other comes to you, like one over the limit."
	}
	return "The agent may act only on the values listed here. A call on any other is refused, like one over the limit."
}
