package pages

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// domainEvidenceHeading is the page H1 for a domain's evidence view.
// It is deliberately NOT the decision counts — those live once, in the
// outcome tiles right below (which are also the filter). Repeating
// "N allowed, N blocked, N held" in the H1 was redundant. A clean,
// non-count title keeps the counts in exactly one place.
func domainEvidenceHeading(v DashboardDomainEvidenceView) string {
	if v.Evidence == nil {
		return "No evidence yet for " + v.DomainName + "."
	}
	return "Activity"
}

// domainEvidenceHref is this page with no actor filter — every agent
// again.
func domainEvidenceHref(domain string) string {
	return "/dashboard/domains/" + url.PathEscape(domain) + "/evidence"
}

// outcomeTileSub is the sublabel under an outcome tile's count, matching
// the Domain home's "Allowed / authorized, Blocked / withheld" pattern
// so the tiles read as a designed summary, not bare numbers.
func outcomeTileSub(outcome string) string {
	switch outcome {
	case "":
		return "submitted"
	case "allowed":
		return "authorized"
	case "blocked":
		return "withheld"
	case "approval_required":
		return "awaiting a person"
	case "limit_exceeded":
		return "throttled"
	case "invalid":
		return "malformed"
	}
	return ""
}

// evidenceTabActive reports whether the given tab is the active one.
// An empty current (e.g. a render outside the handler) is treated as
// the default "decisions" tab so exactly one tab is always marked.
func evidenceTabActive(current, tab string) bool {
	if current == "" {
		current = "decisions"
	}
	return current == tab
}

// outcomeTileActive reports whether an outcome-summary tile is the live
// filter: only while the Decisions tab is showing (the tiles link into
// it), and only for the currently-applied outcome.
func outcomeTileActive(v DashboardDomainEvidenceView, outcome string) bool {
	return evidenceTabActive(v.Tab, "decisions") && v.Outcome == outcome
}

// decisionsPanelEyebrow labels the decisions list by the active filter
// ("All decisions" / "Blocked decisions"), a light heading that names
// the view without repeating the counts (those live in the tiles).
func decisionsPanelEyebrow(v DashboardDomainEvidenceView) string {
	if v.Outcome == "" {
		return "All decisions"
	}
	return evidenceOutcomeLabel(v.Outcome) + " decisions"
}

// evidenceScopeQuery seeds a link's query with the scope this view is
// under: the window, and the actor filter when one is applied.
//
// Every link on this page has to carry the actor. A filter that
// survives the first click and then quietly drops on the second —
// paging, switching tab, choosing an outcome — is worse than no filter
// at all: the reader believes they are still looking at one agent and
// the page has silently widened to every agent, with the way back to
// the card gone from the screen.
func evidenceScopeQuery(v DashboardDomainEvidenceView) url.Values {
	q := url.Values{}
	if w := windowQueryValue(v.Window); w != "" {
		q.Set("window", w)
	}
	if v.Actor != "" {
		q.Set("actor", v.Actor)
	}
	// The card the filter came from, so the way back survives too.
	if v.Card != "" {
		q.Set("card", v.Card)
	}
	return q
}

// evidenceDecisionsHref builds a Decisions-tab URL preserving the
// outcome filter, the page and the scope. Page 1 and the empty (all)
// outcome are omitted so the canonical links stay clean.
func evidenceDecisionsHref(v DashboardDomainEvidenceView, outcome string, page int) string {
	q := evidenceScopeQuery(v)
	q.Set("tab", "decisions")
	if outcome != "" {
		q.Set("outcome", outcome)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	return "/dashboard/domains/" + url.PathEscape(v.DomainName) + "/evidence?" + q.Encode()
}

// evidenceTabHref builds an Activity sub-view (?tab=) URL, preserving
// the window and the actor scope so switching tabs resets neither.
func evidenceTabHref(v DashboardDomainEvidenceView, tab string) string {
	q := evidenceScopeQuery(v)
	q.Set("tab", tab)
	return "/dashboard/domains/" + url.PathEscape(v.DomainName) + "/evidence?" + q.Encode()
}

// windowQueryValue returns the window preset to carry on a link, or ""
// when it is empty or the 30-day default (which the handler resolves to
// anyway) so canonical links stay clean.
func windowQueryValue(window string) string {
	if window == "" || window == "30d" {
		return ""
	}
	return window
}

// evidenceOutcomeLabel maps a raw outcome to a friendly chip label.
func evidenceOutcomeLabel(o string) string {
	switch o {
	case "":
		return "All"
	case "approval_required":
		return "Held"
	case "limit_exceeded":
		return "Rate-limited"
	default:
		// allowed → Allowed, blocked → Blocked, invalid → Invalid.
		if o == "" {
			return "All"
		}
		return strings.ToUpper(o[:1]) + o[1:]
	}
}

// evidenceMatchingCount is the number of decisions matching the active
// filter — the window-complete per-outcome count when filtered, else the
// window total. Basis for the page count.
func evidenceMatchingCount(v DashboardDomainEvidenceView) int {
	if v.Evidence == nil {
		return 0
	}
	if v.Outcome != "" {
		return v.Evidence.Decisions.Counts[v.Outcome]
	}
	return v.Evidence.Decisions.Total
}

func evidenceHasPrev(v DashboardDomainEvidenceView) bool { return v.Page > 1 }

func evidenceHasNext(v DashboardDomainEvidenceView) bool {
	if v.PageSize <= 0 {
		return false
	}
	return v.Page*v.PageSize < evidenceMatchingCount(v)
}

// evidencePageLabel is the "Page N of M" indicator for the decisions pager.
func evidencePageLabel(v DashboardDomainEvidenceView) string {
	total := evidenceMatchingCount(v)
	pages := 1
	if v.PageSize > 0 && total > 0 {
		pages = (total + v.PageSize - 1) / v.PageSize
	}
	page := v.Page
	if page < 1 {
		page = 1
	}
	return fmt.Sprintf("Page %d of %d", page, pages)
}
