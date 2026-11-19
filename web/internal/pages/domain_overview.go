package pages

import (
	"net/url"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// DomainTabs are the domain page's sections, in order. Overview answers
// "what can agents do here, and is it working?"; the rest is the
// machinery, visited when configuring or investigating.
var DomainTabs = []struct{ Key, Label string }{
	{"overview", "Overview"},
	{"contract", "Contract"},
	{"integration", "Integration"},
	{"settings", "Settings"},
}

// NormalizeDomainTab maps a ?tab= value onto a known tab.
func NormalizeDomainTab(tab string) string {
	for _, t := range DomainTabs {
		if t.Key == tab {
			return tab
		}
	}
	return "overview"
}

func domainTabHref(name, tab string) string {
	if tab == "overview" {
		return "/dashboard/domains/" + name
	}
	return "/dashboard/domains/" + name + "?tab=" + tab
}

// domainIssueCardHref issues a card on this domain: the simple form with
// the job preselected when the domain is a standard job's, else the full
// form with the domain selected.
func domainIssueCardHref(name string) string {
	for _, t := range CardTasks {
		if t.Template == name {
			return "/cards/new?task=" + t.Slug
		}
	}
	return "/cards/new?advanced=1&domain=" + url.QueryEscape(name)
}

// domainActionCards is one governed action and the cards in force that
// grant it.
type domainActionCards struct {
	Action apiclient.DomainAction
	Cards  []apiclient.Mandate
}

func domainCardsByAction(v DashboardDomainsView) []domainActionCards {
	if v.Selected == nil {
		return nil
	}
	out := make([]domainActionCards, 0, len(v.Selected.Actions))
	for _, a := range v.Selected.Actions {
		row := domainActionCards{Action: a}
		for _, m := range v.DomainCards {
			for _, g := range m.Grants {
				if g.Action == a.Name {
					row.Cards = append(row.Cards, m)
					break
				}
			}
		}
		out = append(out, row)
	}
	return out
}

// domainAttentionItem is one thing on the domain that needs a person.
type domainAttentionItem struct {
	Text, Href, CTA string
}

// domainAttention lists only what needs someone now: an action that
// requires a card no agent holds (every agent is refused it), a card
// with nothing left in its window, and an action no agent has called
// yet. Nothing here is shown when all is well.
func domainAttention(v DashboardDomainsView) []domainAttentionItem {
	if v.Selected == nil {
		return nil
	}
	var out []domainAttentionItem
	for _, row := range domainCardsByAction(v) {
		if row.Action.RequiresAuthority && len(row.Cards) == 0 {
			out = append(out, domainAttentionItem{
				Text: row.Action.Name + " needs a Card and no agent holds one, so it's refused for every agent.",
				Href: domainIssueCardHref(v.Selected.Name), CTA: "Issue a card",
			})
		}
	}
	for _, m := range v.DomainCards {
		for _, l := range v.CardUsage[m.ID].Readable() {
			if l.Limit > 0 && l.Remaining <= 0 {
				out = append(out, domainAttentionItem{
					Text: m.Subject + " has no room left on " + m.ID + " " + windowLabel(l.Window) + ". " + spentUntil(l.Window),
					Href: "/cards/" + url.PathEscape(m.ID), CTA: "Open card",
				})
				break
			}
		}
	}
	if v.Verify.Available {
		for _, r := range v.Verify.Rows {
			if !r.Observed && r.Backing == BackingClient {
				out = append(out, domainAttentionItem{
					Text: "No agent has asked KIFF about " + r.ActionName + " yet, so nothing that runs it is governed.",
					Href: domainTabHref(v.Selected.Name, "integration"), CTA: "Integration",
				})
			}
		}
	}
	return out
}

// domainCardRemaining is the headline balance for a card row.
func domainCardRemaining(v DashboardDomainsView, m apiclient.Mandate) string {
	ls := v.CardUsage[m.ID].Readable()
	if len(ls) == 0 {
		return "balance unknown"
	}
	return authorityValue(ls[0].Remaining, ls[0].Quantity) + " of " + authorityValue(ls[0].Limit, ls[0].Quantity) + " left " + windowLabel(ls[0].Window)
}

func domainLastDecision(v DashboardDomainsView) string {
	if v.LastReceivedAt == nil {
		return "no decisions yet"
	}
	return "last decision " + timeAgo(*v.LastReceivedAt)
}

// transitionLabel reads one lifecycle transition. The first transition
// of an entity has no from-state; it is shown as "new" rather than as
// an empty field.
func transitionLabel(from, on, to string) string {
	if from == "" {
		from = "new"
	}
	return "On " + on + ": " + from + " → " + to
}

// spentUntil says what brings a spent limit back. A lifetime total never
// resets; only a raised limit or extra room does.
func spentUntil(window string) string {
	if window == "lifetime" {
		return "Its next request is refused until the limit is raised; this total never resets."
	}
	return "Its next request is refused until the window resets or the limit is raised."
}
