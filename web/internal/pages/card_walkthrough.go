package pages

import (
	"slices"
	"strconv"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// The issue form is a walkthrough: one decision per step, in the order
// someone says it. These helpers keep the template free of logic.

// AgentChoice is one agent offered on the first step.
type AgentChoice struct {
	ID, Hint string
	Checked  bool
}

// AgentChoices are the agents KIFF has seen act in this domain, offered
// as a choice so the holder is not a typo. The one already chosen stays
// chosen; a single seen agent is chosen for a new Card.
func (v CardNewView) AgentChoices() []AgentChoice {
	out := make([]AgentChoice, 0, len(v.Agents))
	for _, a := range v.Agents {
		hint := strconv.Itoa(a.Operations) + " operations seen"
		if a.Operations == 1 {
			hint = "1 operation seen"
		}
		checked := v.Form.MandateSubj == a.ID || (v.Form.MandateSubj == "" && len(v.Agents) == 1)
		out = append(out, AgentChoice{ID: a.ID, Hint: hint, Checked: checked})
	}
	return out
}

// OtherAgentChecked reports whether the typed id is the holder: a holder
// that is not among the seen agents.
func (v CardNewView) OtherAgentChecked() bool {
	if v.Form.MandateSubj == "" {
		return false
	}
	return !slices.ContainsFunc(v.Agents, func(a apiclient.DomainAgent) bool { return a.ID == v.Form.MandateSubj })
}

// OtherAgentValue is the typed id, shown only when it is the holder.
func (v CardNewView) OtherAgentValue() string {
	if v.OtherAgentChecked() {
		return v.Form.MandateSubj
	}
	return ""
}

// StartStep is the step the walkthrough opens on. A Card being changed,
// or one the cloud just refused, opens on the review, where every
// answer is visible and each can be edited.
func (v CardNewView) StartStep() string {
	if v.Rejected != "" {
		return "5"
	}
	if v.Editing != "" {
		return wkStep(v.Step, 5, 5)
	}
	return "1"
}

// OnlyParameter is the one parameter a total can add up, when the domain
// declares exactly one: there is nothing to choose, so none is offered.
func (v CardNewView) OnlyParameter() string {
	ps := v.SummableParameters()
	if len(ps) == 1 {
		return ps[0].Name
	}
	return ""
}

// SumUnit names what the total adds up, for the field's suffix.
func (v CardNewView) SumUnit() string {
	if v.Form.Parameter != "" {
		return v.Form.Parameter
	}
	if ps := v.SummableParameters(); len(ps) > 0 {
		return ps[0].Name
	}
	return ""
}

// ParameterSelected reports whether p is the parameter to pre-select.
func (v CardNewView) ParameterSelected(p string) bool {
	if v.Form.Parameter != "" {
		return v.Form.Parameter == p
	}
	return p == v.SumUnit()
}

// OnExceedAsk reports whether an action past the Card is held (Ask), the
// default for a new Card.
func (v CardNewView) OnExceedAsk() bool { return v.Form.OnExceed != "refuse" }

// WalkOption is one segmented choice.
type WalkOption struct {
	Value, Label, Note string
	Checked            bool
}

// WindowChoices are the periods a limit is counted over.
func (v CardNewView) WindowChoices() []WalkOption { return wkWindowChoices(v.SelectedWindow()) }

// HoldChoices are how long an action held for an approver waits.
func (v CardNewView) HoldChoices() []WalkOption { return wkHoldChoices(v.Form.Hold) }

// wkWindowChoices are the periods a limit is counted over, short enough
// for a phone, each with one line on how room comes back.
func wkWindowChoices(sel string) []WalkOption {
	if sel == "" {
		sel = "calendar_day"
	}
	return []WalkOption{
		{"calendar_day", "Each day", "Starts again at midnight in the Card’s time zone. New Cards use Settings → Account; existing Cards keep their time zone.", sel == "calendar_day"},
		{"rolling_24h", "Any 24 h", "Always the last 24 hours. Room comes back as earlier actions age out.", sel == "rolling_24h"},
		{"rolling_1h", "Any hour", "Always the last 60 minutes. Room comes back as earlier actions age out.", sel == "rolling_1h"},
		{"rolling_7d", "Any 7 days", "Always the last 7 days: once a week. Room comes back as earlier actions age out.", sel == "rolling_7d"},
		{"lifetime", "Whole life", "One total until the Card ends. It never starts again: for one job with an end date.", sel == "lifetime"},
	}
}

// wkHoldChoices are how long an action held for an approver waits. A
// Card's own wait that the forms do not offer is kept as a further choice.
func wkHoldChoices(sel string) []WalkOption {
	if sel == "" {
		sel = "10m"
	}
	out := []WalkOption{
		{Value: "10m", Label: "10 min"},
		{Value: "1h", Label: "1 hour"},
		{Value: "24h", Label: "24 hours"},
		{Value: "168h", Label: "7 days"},
	}
	if !slices.ContainsFunc(out, func(o WalkOption) bool { return o.Value == sel }) {
		out = append(out, WalkOption{Value: sel, Label: sel + " (current)"})
	}
	for i := range out {
		out[i].Checked = out[i].Value == sel
	}
	return out
}

// riskChip is the class for an action's risk, so higher risk reads at a
// glance and is never only a colour: the word is always shown.
func riskChip(risk string) string {
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "high", "critical":
		return "wk-chip wk-chip-high"
	case "medium":
		return "wk-chip wk-chip-medium"
	default:
		return "wk-chip"
	}
}

// actionDetail is the line under an action's name: what else the domain
// says about it, without the risk, which has its own chip.
func actionDetail(a apiclient.DomainAction) string {
	var parts []string
	if a.Approval != "" && a.Approval != "never" {
		parts = append(parts, "approval "+a.Approval)
	}
	if len(a.AllowedStates) > 0 {
		parts = append(parts, "when "+strings.Join(a.AllowedStates, ", "))
	}
	return strings.Join(parts, " · ")
}

// cardNewActionsCheck is step 2's check: at least one action, unless the
// Card's grants are kept as they are and there is nothing to pick.
func cardNewActionsCheck(v CardNewView) string {
	if v.Form.PreservedGrants != "" {
		return ""
	}
	return "choice:actions"
}

// StartStep opens a refused quick Card on its review.
func (v CardQuickView) StartStep() string {
	if v.Rejected != "" {
		return "4"
	}
	return "1"
}

// StartStep opens a refused tool Card on its review.
func (v ToolCardView) StartStep() string {
	if v.Error != "" {
		return "4"
	}
	return wkStep(v.Step, 4, 1)
}

// wkStep is step when it names one of the form's last steps (1..last),
// else def: a Settings row opens its own step; nothing else is trusted.
func wkStep(step string, last, def int) string {
	if n, err := strconv.Atoi(step); err == nil && n >= 1 && n <= last {
		return step
	}
	return strconv.Itoa(def)
}

// AgentChoices are the account's agents, offered as a choice; the one
// the form already names stays chosen.
func (v ToolCardView) AgentChoices() []AgentChoice {
	out := make([]AgentChoice, 0, len(v.Agents))
	for _, a := range v.Agents {
		out = append(out, AgentChoice{ID: a, Hint: "Connected agent", Checked: v.Form.Agent == a || (v.Form.Agent == "" && len(v.Agents) == 1)})
	}
	return out
}

// OtherAgentChecked reports whether the typed name is the holder.
func (v ToolCardView) OtherAgentChecked() bool {
	return v.Form.Agent != "" && !slices.Contains(v.Agents, v.Form.Agent)
}

// OtherAgentValue is the typed name, shown only when it is the holder.
func (v ToolCardView) OtherAgentValue() string {
	if v.OtherAgentChecked() {
		return v.Form.Agent
	}
	return ""
}

// LimitCheck is step 2's check: a total or a number of calls, since a
// Card with neither limits nothing over time.
func (v ToolCardView) LimitCheck() string {
	if v.Tool.AmountArg != "" {
		return "anypositive:total,calls"
	}
	return "positive:calls"
}
