package pages

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
)

// IsToolCard reports whether m is a Card for a tool connected through
// the KIFF gateway: one on the account's gateway domain, whose action is
// the tool's name.
func IsToolCard(m apiclient.Mandate) bool { return m.Domain == "gateway" && len(m.Grants) == 1 }

// ToolCardTool is the connected tool a tool Card is for.
func ToolCardTool(m apiclient.Mandate) string { return toolCardTool(m) }

// toolCardTool is the connected tool a tool Card is for.
func toolCardTool(m apiclient.Mandate) string {
	if len(m.Grants) == 0 {
		return ""
	}
	return m.Grants[0].Action
}

// toolCardEditHref opens the tool Card form for this Card, prefilled.
func toolCardEditHref(m apiclient.Mandate) string {
	return toolHref(toolCardTool(m)) + "/card?agent=" + url.QueryEscape(m.Subject)
}

// ToolCardFormFrom is the form as m stands, so changing a Card starts
// from its current terms.
func ToolCardFormFrom(m apiclient.Mandate) ToolCardForm {
	f := ToolCardForm{Agent: m.Subject, Window: "calendar_day", OnExceed: "owner", Hold: HoldFromSeconds(m.HoldExpirySeconds)}
	if m.OnExceed == "refuse" {
		f.OnExceed = "refuse"
	}
	if len(m.Grants) > 0 {
		// The exact identifiers, never their labels: a label read back
		// could change one (return__first) and break the agents and
		// approvers that expect it.
		f.NextSteps = strings.Join(m.Grants[0].NextSteps, ", ")
		for _, p := range m.Grants[0].PerActionMax {
			if strings.HasPrefix(p.Quantity, "sum(") {
				f.PerCall = strconv.FormatInt(p.Max, 10)
			}
		}
	}
	for _, a := range m.Aggregates {
		f.Window = a.Window
		if a.Quantity == "count" {
			f.Calls = strconv.FormatInt(a.Limit, 10)
			f.CountMeasure = a.Measure
		} else if strings.HasPrefix(a.Quantity, "sum(") {
			f.Total = strconv.FormatInt(a.Limit, 10)
			f.AmountMeasure = a.Measure
		}
	}
	return f
}

// evidenceScope names what the Activity page covers: the domain in a
// company's workspace, the account in a personal one, which never sees
// a domain.
func evidenceScope(ctx context.Context) string {
	if components.IsPersonal(ctx) {
		return "for this account"
	}
	return "in this domain"
}

// evidenceTitle is the Activity page's title: named after the domain in
// a company's workspace, plain in a personal one.
func evidenceTitle(ctx context.Context, domain string) string {
	if components.IsPersonal(ctx) {
		return "KIFF Cloud — Activity"
	}
	return "KIFF Cloud — " + domain + " evidence"
}

// cardHolderLabel is who holds a Card, for its face: the agent, or the
// first one and how many more share it.
func cardHolderLabel(m apiclient.Mandate) string {
	hs := m.Holders()
	if len(hs) <= 1 {
		return m.Subject
	}
	return hs[0] + " + " + strconv.Itoa(len(hs)-1) + " more"
}
