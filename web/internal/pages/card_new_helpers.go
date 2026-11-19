package pages

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// CardNewView backs the issuance form.
//
// The form's job is to make the shape of a card visible — a holder, the
// actions it covers, one thing to count, a ceiling, a window — and to
// offer only choices the domain actually declares. It does not
// re-implement the cloud's validation: a card that cannot be issued is
// refused by the api, whose sentence is shown verbatim in Rejected.
type CardNewView struct {
	Email       string
	Subject     string
	DisplayName string
	Flash       *DashboardFlash

	Me *apiclient.Me

	// Domains is the tenant's domains; SelectedDomain is the one whose
	// actions the form offers. A tenant with one domain never sees the
	// picker.
	Domains        []apiclient.DomainListItem
	SelectedDomain string
	Actions        []apiclient.DomainAction

	// Agents are identities observed proposing against this domain.
	// Offered as suggestions because a limit is worth issuing for an
	// agent that exists, and a typo in a subject produces a mandate
	// that binds nothing — the quietest way for this feature to fail.
	Agents []apiclient.DomainAgent

	// Form carries back what was submitted, so a refusal does not empty
	// the fields someone just filled in.
	// Editing names the card being changed, when this form was opened
	// from one. A change is a re-issue under the same id, so the only
	// difference is that the page must say so: someone who thinks they
	// are creating a second card would be surprised to find they moved
	// the first one's ceiling.
	Editing string
	// Step opens a Card being changed on one step, from its Settings
	// row; the review and Save follow it.
	Step string
	// Single opens that one step on its own, to change it and save.
	Single bool

	Form MandateFormValues

	// Rejected is the cloud's own explanation of why this limit cannot
	// exist. LoadError means the form could not be built at all.
	Rejected         string
	LoadError        string
	NotConfiguredMsg string
}

// MandateFormValues is the submitted form, echoed back on refusal.
type MandateFormValues struct {
	ID           string
	MandateSubj  string
	Environment  string
	Actions      []string
	Measure      string
	QuantityKind string // "count" | "sum"
	Parameter    string
	Limit        string
	// PerActionMax is the optional most any single action may carry of
	// the summed parameter ("up to €500 each"). Empty means none.
	PerActionMax string
	// PreservedGrants carries an existing Card's complete grant list, as
	// JSON, when the form cannot represent it exactly: the same action
	// granted more than once (different state scopes), different caps
	// per action, several capped parameters, or a cap on a parameter
	// other than the one the ceiling sums. The form then shows the
	// grants read-only and the edit reissues them unchanged; only the
	// ceiling, window, expiry and holder can be changed here. It never
	// merges or flattens grants, which could loosen a limit.
	PreservedGrants string
	Window          string
	// States narrows a granted action to some of the entity states the
	// domain already allows it in, keyed by action name. An action
	// absent here, or present with nothing selected, keeps the
	// contract's own states — a card never widens them.
	States map[string][]string
	// ValidUntil is the day the card stops, as the date input submits
	// it (YYYY-MM-DD). Empty means it holds until revoked.
	ValidUntil string
	// OnExceed is what happens to work outside the card: "owner" (it
	// comes home) or "refuse". Empty means the form did not ask, and
	// the API keeps the card's policy (or uses owner for a new one).
	OnExceed string
	// Hold is how long an action held for an approver waits ("10m",
	// "1h", "24h", "168h", or a Card's own value the form keeps).
	// Empty keeps the Card's current wait, 10 minutes when new.
	Hold string
}

// HoldFromSeconds is a Card's hold expiry as the forms offer it. A wait
// set through the API that the forms do not offer is kept as it is,
// never silently replaced by the default.
func HoldFromSeconds(s int64) string {
	switch s {
	case 0, 600:
		return "10m"
	case 3600:
		return "1h"
	case 86400:
		return "24h"
	case 604800:
		return "168h"
	default:
		return (time.Duration(s) * time.Second).String()
	}
}

// SummableParameter is one parameter a mandate could count, with how
// widely it is declared.
//
// A mandate may sum a parameter only where *every* granted action
// declares it numeric, so a parameter present on some actions is a
// refusal waiting to happen. Saying how many actions declare it lets
// someone see that before submitting rather than after.
type SummableParameter struct {
	Name       string
	DeclaredOn int
	OfActions  int
}

// Universal reports whether every action in the domain declares it.
func (p SummableParameter) Universal() bool { return p.DeclaredOn == p.OfActions }

// Label describes the parameter and its reach in one line.
func (p SummableParameter) Label() string {
	if p.Universal() {
		return fmt.Sprintf("%s — declared on all %d actions", p.Name, p.OfActions)
	}
	return fmt.Sprintf("%s — declared on %d of %d actions", p.Name, p.DeclaredOn, p.OfActions)
}

// SummableParameters lists the parameters the domain's actions declare
// as numeric, most widely declared first.
//
// Deliberately conservative, matching the api: a parameter whose type
// the domain does not declare is not offered, because a limit that
// silently sums nothing is worse than one that could not be created.
func (v CardNewView) SummableParameters() []SummableParameter {
	counts := map[string]int{}
	for _, a := range v.Actions {
		seen := map[string]bool{}
		for _, p := range a.Parameters {
			if !p.Summable() || seen[p.Name] {
				continue
			}
			seen[p.Name] = true
			counts[p.Name]++
		}
	}
	out := make([]SummableParameter, 0, len(counts))
	for name, n := range counts {
		out = append(out, SummableParameter{Name: name, DeclaredOn: n, OfActions: len(v.Actions)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeclaredOn != out[j].DeclaredOn {
			return out[i].DeclaredOn > out[j].DeclaredOn
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// State collapses the render conditions, so the template carries no
// else-if chain and every branch is named.
func (v CardNewView) State() string {
	switch {
	case v.NotConfiguredMsg != "":
		return "unconfigured"
	case v.LoadError != "":
		return "error"
	case len(v.Actions) == 0:
		return "nodomain"
	default:
		return "ready"
	}
}

// CountingOnly reports whether the domain declares nothing summable, in
// which case the form offers counting and says why that is all.
func (v CardNewView) CountingOnly() bool {
	return len(v.SummableParameters()) == 0
}

// IsChecked reports whether an action was selected on the last submit,
// so a refusal does not clear the boxes.
func (v CardNewView) IsChecked(action string) bool {
	for _, a := range v.Form.Actions {
		if strings.EqualFold(a, action) {
			return true
		}
	}
	return false
}

// StateIsChecked reports whether a state was selected for an action on
// the last submit, so a refusal does not clear the boxes.
func (v CardNewView) StateIsChecked(action, state string) bool {
	for _, s := range v.Form.States[action] {
		if strings.EqualFold(s, state) {
			return true
		}
	}
	return false
}

// SelectedWindow is the window to pre-select, defaulting to the
// calendar day: the unit a person means by "a day" when they say what
// an agent may spend.
func (v CardNewView) SelectedWindow() string {
	if v.Form.Window != "" {
		return v.Form.Window
	}
	return "calendar_day"
}

// SummingIsSelected reports whether the sum radio should be checked.
func (v CardNewView) SummingIsSelected() bool {
	if v.CountingOnly() {
		return false
	}
	return v.Form.QuantityKind != "count"
}

// mandateWindowOptions is the set the api accepts. Listed here rather
// than fetched because the api refuses an unknown window anyway; the
// cost of drift is a form offering something that gets refused, not a
// limit that silently does the wrong thing.
var mandateWindowOptions = []struct{ Value, Label string }{
	{"calendar_day", "each calendar day (resets at midnight)"},
	{"rolling_24h", "any 24 hours (rolling)"},
	{"rolling_1h", "any hour (rolling)"},
	{"rolling_7d", "any 7 days (rolling)"},
	{"lifetime", "the Card's whole life (never resets)"},
}

func mandateActionLine(a apiclient.DomainAction) string {
	parts := []string{}
	if a.Risk != "" {
		parts = append(parts, a.Risk+" risk")
	}
	if a.Approval != "" && a.Approval != "never" {
		parts = append(parts, "approval "+a.Approval)
	}
	if len(a.AllowedStates) > 0 {
		parts = append(parts, "from "+strings.Join(a.AllowedStates, ", "))
	}
	return strings.Join(parts, " · ")
}

func mandateAgentHint(a apiclient.DomainAgent) string {
	return fmt.Sprintf("%s · %d operations seen", a.ID, a.Operations)
}

// revisionHolder names who held the Card after revision i, and says so
// when it changed: a move is a revision whose holder differs from the
// one before it. Revisions recorded before holders were read "—".
func revisionHolder(revs []apiclient.MandateRevision, i int) string {
	cur := strings.TrimSpace(revs[i].Subject)
	if cur == "" {
		return "—"
	}
	if i > 0 {
		if prev := strings.TrimSpace(revs[i-1].Subject); prev != "" && prev != cur {
			return cur + " (moved from " + prev + ")"
		}
	}
	return cur
}

// PreservedGrantLines renders preserved grants for reading, one line
// each: "REFUND_ORDER · only when PAID · at most 10000 of sum(amount)".
func PreservedGrantLines(preserved string) []string {
	var gs []apiclient.MandateGrant
	if err := json.Unmarshal([]byte(preserved), &gs); err != nil {
		return []string{"unreadable"}
	}
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		line := g.Action
		if len(g.EntityStates) > 0 {
			line += " · only when " + strings.Join(g.EntityStates, ", ")
		}
		for _, p := range g.PerActionMax {
			line += " · at most " + strconv.FormatInt(p.Max, 10) + " of " + p.Quantity
		}
		out = append(out, line)
	}
	return out
}

// revisionPerAction names the per-action limits a revision carried, or
// "none" when it had none, so a change to a cap is visible between two
// history rows that share a ceiling.
func revisionPerAction(r apiclient.MandateRevision) string {
	var out []string
	for _, g := range r.Grants {
		for _, p := range g.PerActionMax {
			out = append(out, g.Action+" ≤ "+strconv.FormatInt(p.Max, 10)+" of "+p.Quantity)
		}
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, "; ")
}

// ActionsForForm is the actions to offer as checkboxes: none when the
// Card's grants are preserved and shown read-only instead.
func (v CardNewView) ActionsForForm() []apiclient.DomainAction {
	if v.Form.PreservedGrants != "" {
		return nil
	}
	return v.Actions
}
