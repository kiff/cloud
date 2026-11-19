package pages

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/gatewayclient"
)

// The Tools list groups tools by the server they come from, one line
// each, and can be searched and narrowed.
const (
	ToolsShowAll     = ""
	ToolsShowChanges = "changes"
	ToolsShowReads   = "reads"
	ToolsShowNoCard  = "nocard"
)

// ToolsFilter is what the list is narrowed to: ?q= and ?show=.
type ToolsFilter struct{ Q, Show string }

// NormalizeToolsFilter reads ?q= and ?show=; an unknown show is all.
func NormalizeToolsFilter(q, show string) ToolsFilter {
	switch show {
	case ToolsShowChanges, ToolsShowReads, ToolsShowNoCard:
	default:
		show = ToolsShowAll
	}
	q = strings.TrimSpace(q)
	if len(q) > 100 {
		q = q[:100]
	}
	return ToolsFilter{Q: q, Show: show}
}

func (f ToolsFilter) active() bool { return f.Q != "" || f.Show != ToolsShowAll }

// toolsHref is the list narrowed to show, keeping the search.
func (f ToolsFilter) href(show string) string {
	q := url.Values{}
	if f.Q != "" {
		q.Set("q", f.Q)
	}
	if show != ToolsShowAll {
		q.Set("show", show)
	}
	if len(q) == 0 {
		return "/tools"
	}
	return "/tools?" + q.Encode()
}

// ToolListRow is one tool on the list. Cards is how many live Cards name
// it; CardsKnown is false when the Cards could not be read.
type ToolListRow struct {
	Tool       gatewayclient.Connection
	Cards      int
	CardsKnown bool
}

// ToolGroup is one server's tools. Total counts them before the filter.
type ToolGroup struct {
	URL, Host string
	Custody   string
	Tools     []ToolListRow
	Total     int
	NoCard    int
}

// ToolChip is one filter on the list, with how many tools it shows.
type ToolChip struct {
	Label, Href string
	Count       int
	Current     bool
	Warn        bool
}

// GroupTools builds the list: servers by host, tools by name, each with
// its Card count, then narrowed by f. cards is nil when the Cards could
// not be read, which hides the No Card filter rather than call every
// tool uncovered.
func GroupTools(tools []gatewayclient.Connection, cards []apiclient.Mandate, cardsOK bool, f ToolsFilter, now time.Time) ([]ToolGroup, []ToolChip) {
	rows := make([]ToolListRow, 0, len(tools))
	for _, t := range tools {
		r := ToolListRow{Tool: t, CardsKnown: cardsOK}
		if cardsOK {
			r.Cards = cardsCovering(cards, t.Name, now)
		}
		rows = append(rows, r)
	}
	counts := map[string]int{}
	for _, r := range rows {
		for _, show := range []string{ToolsShowAll, ToolsShowChanges, ToolsShowReads, ToolsShowNoCard} {
			if toolShown(r, ToolsFilter{Show: show}) {
				counts[show]++
			}
		}
	}
	var chips []ToolChip
	for _, c := range []struct{ show, label string }{
		{ToolsShowAll, "All"}, {ToolsShowChanges, "Change things"}, {ToolsShowReads, "Read only"}, {ToolsShowNoCard, "No Card yet"},
	} {
		if c.show == ToolsShowNoCard && !cardsOK {
			continue
		}
		chips = append(chips, ToolChip{Label: c.label, Href: f.href(c.show), Count: counts[c.show], Current: f.Show == c.show, Warn: c.show == ToolsShowNoCard})
	}

	byURL := map[string]*ToolGroup{}
	var order []string
	for _, r := range rows {
		g, ok := byURL[r.Tool.URL]
		if !ok {
			g = &ToolGroup{URL: r.Tool.URL, Host: r.Tool.Host()}
			byURL[r.Tool.URL] = g
			order = append(order, r.Tool.URL)
		}
		g.Total++
		if r.CardsKnown && r.Cards == 0 {
			g.NoCard++
		}
		if g.Custody == "" {
			g.Custody = groupCustody(r.Tool)
		} else if g.Custody != groupCustody(r.Tool) {
			g.Custody = "Keys differ: see each tool"
		}
		if toolShown(r, f) {
			g.Tools = append(g.Tools, r)
		}
	}
	var groups []ToolGroup
	for _, u := range order {
		g := byURL[u]
		if len(g.Tools) == 0 {
			continue
		}
		sort.SliceStable(g.Tools, func(i, j int) bool { return g.Tools[i].Tool.Name < g.Tools[j].Tool.Name })
		groups = append(groups, *g)
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Host < groups[j].Host })
	return groups, chips
}

// cardsCovering counts the Cards that can authorize a call to tool now,
// by the API's rules for gateway calls (mandates.go agentCard, a
// review): in force and not archived, scoped to the gateway domain or to
// none, with any of its grants naming the tool, case-insensitively. A
// Card counts once however many of its grants name the tool. ToolCards
// stays narrower: it finds the Cards the tool's own form edits.
func cardsCovering(cards []apiclient.Mandate, tool string, now time.Time) int {
	n := 0
	for _, m := range cards {
		if !cardActiveNow(m, now) || (m.Domain != "" && !strings.EqualFold(m.Domain, "gateway")) {
			continue
		}
		for _, g := range m.Grants {
			if strings.EqualFold(g.Action, tool) {
				n++
				break
			}
		}
	}
	return n
}

// cardActiveNow is the API's Mandate.Active, plus not archived: unlike
// cardInForce, a Card that has not started yet does not count.
func cardActiveNow(m apiclient.Mandate, now time.Time) bool {
	switch {
	case m.ArchivedAt != nil,
		m.RevokedAt != nil && !now.Before(*m.RevokedAt),
		m.ValidFrom != nil && now.Before(*m.ValidFrom),
		m.ValidUntil != nil && !now.Before(*m.ValidUntil):
		return false
	}
	return true
}

func toolShown(r ToolListRow, f ToolsFilter) bool {
	switch f.Show {
	case ToolsShowChanges:
		if r.Tool.ReadOnly {
			return false
		}
	case ToolsShowReads:
		if !r.Tool.ReadOnly {
			return false
		}
	case ToolsShowNoCard:
		if !r.CardsKnown || r.Cards > 0 {
			return false
		}
	}
	if f.Q == "" {
		return true
	}
	q := strings.ToLower(f.Q)
	return strings.Contains(strings.ToLower(r.Tool.Name), q) || strings.Contains(strings.ToLower(r.Tool.Description), q)
}

// groupCustody is who holds a server's key, as a group's second line.
func groupCustody(t gatewayclient.Connection) string {
	switch t.Mode {
	case "verify":
		return "Nobody at KIFF holds a key"
	case "relay":
		return "Your relay holds the key"
	}
	return "KIFF holds the key, encrypted"
}

// toolGroupLine is a group's second line: how many tools, who holds the
// key, and how many no Card covers.
func toolGroupLine(g ToolGroup, f ToolsFilter) string {
	n := countOf(int64(g.Total), "tool", "tools")
	if f.active() {
		n = strconv.Itoa(len(g.Tools)) + " of " + n
	}
	line := n + " · " + g.Custody
	if g.NoCard > 0 {
		line += " · " + strconv.Itoa(g.NoCard) + " with no Card"
	}
	return line
}

// toolLimit is what a Card limits on a tool, in two or three words.
func toolLimit(t gatewayclient.Connection) string {
	switch {
	case t.ReadOnly:
		return "Read only"
	case t.AmountArg != "":
		if u := amountUnitLabel(t.AmountUnit, t.AmountScale); u != "" {
			return "Amount · " + u
		}
		return "Amount"
	}
	return "Calls"
}

// toolCardsLabel is a tool's Cards column.
func toolCardsLabel(r ToolListRow) string {
	switch {
	case !r.CardsKnown:
		return "Cards ?"
	case r.Cards == 0:
		return "No Card"
	case r.Cards == 1:
		return "1 Card"
	}
	return strconv.Itoa(r.Cards) + " Cards"
}

// toolOneLine is a tool's description cut to its first sentence; the
// whole text is on the tool's page.
func toolOneLine(t gatewayclient.Connection) string {
	d := strings.TrimSpace(t.Description)
	if d == "" {
		return t.Host()
	}
	if i := strings.Index(d, ". "); i > 0 {
		return d[:i+1]
	}
	return d
}

// addFromServerHref starts Connect a tool with the server filled in.
func addFromServerHref(serverURL string) string {
	return "/tools/new?url=" + url.QueryEscape(serverURL)
}
