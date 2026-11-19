package handlers

import (
	"encoding/json"
	"sort"
	"strings"

	studio "github.com/kiff/kiff-studio"
)

// studio_scaffold_descriptor.go bridges the cloud's derived domain to the
// framework's `kiff scaffold` command (kiff/kiff#27). The cloud derives a
// domain (observe→candidate) and governs its contract; the executable
// domain itself lives in the developer's codebase. `kiff scaffold` turns a
// JSON *descriptor* into a framework-faithful Go domain package with TODO
// executor stubs and passing tests. This emits that descriptor from the
// Studio draft's Blueprint so "scaffold to your codebase" hands the
// developer a seed their AI agent finishes in-repo.
//
// The field set mirrors the framework's descriptor exactly — `kiff
// scaffold` decodes with DisallowUnknownFields, so any drift here is a
// hard error there. Keep this in lockstep with the descriptor type in
// kiff/kiff cmd/kiff/scaffold.go.

type scaffoldDescriptor struct {
	Domain      string                     `json:"domain"`
	Entity      string                     `json:"entity"`
	Adapter     string                     `json:"adapter"`
	Events      []string                   `json:"events"`
	States      []string                   `json:"states"`
	Transitions []scaffoldDescriptorTrans  `json:"transitions"`
	Actions     []scaffoldDescriptorAction `json:"actions"`
	Roles       map[string][]string        `json:"roles,omitempty"`
}

type scaffoldDescriptorTrans struct {
	On   string `json:"on"`
	From string `json:"from"`
	To   string `json:"to"`
}

type scaffoldDescriptorAction struct {
	Name                string   `json:"name"`
	AllowedStates       []string `json:"allowed_states"`
	RequiredParameters  []string `json:"required_parameters"`
	RequiredPermissions []string `json:"required_permissions"`
	Risk                string   `json:"risk"`
	Approval            string   `json:"approval"`
	FollowUpEvents      []string `json:"follow_up_events"`
}

// scaffoldDescriptorFromBlueprint maps a Studio Blueprint to a `kiff
// scaffold` descriptor. The one derivation the Blueprint can't supply
// directly is follow_up_events: an action executed from one of its
// allowed states should advance the entity, so we attribute the events
// of the transitions leaving those states. This makes the generated
// project's happy-path test reach its terminal state without hand-edits.
func scaffoldDescriptorFromBlueprint(bp studio.Blueprint) scaffoldDescriptor {
	adapter := strings.TrimSpace(bp.Domain)

	actions := make([]scaffoldDescriptorAction, 0, len(bp.Actions))
	for _, a := range bp.Actions {
		approval := "never"
		if a.ApprovalRequired {
			approval = "required"
		}
		actions = append(actions, scaffoldDescriptorAction{
			Name:                a.Name,
			AllowedStates:       nonNil(a.AllowedStates),
			RequiredParameters:  nonNil(a.RequiredParameters),
			RequiredPermissions: nonNil(a.RequiredPermissions),
			Risk:                strings.TrimSpace(a.Risk),
			Approval:            approval,
			FollowUpEvents:      followUpEvents(bp, a.AllowedStates),
		})
	}

	transitions := make([]scaffoldDescriptorTrans, 0, len(bp.Transitions))
	for _, t := range bp.Transitions {
		transitions = append(transitions, scaffoldDescriptorTrans{On: t.On, From: t.From, To: t.To})
	}

	return scaffoldDescriptor{
		Domain:      bp.Domain,
		Entity:      bp.Entity,
		Adapter:     adapter,
		Events:      nonNil(bp.Events),
		States:      nonNil(bp.States),
		Transitions: transitions,
		Actions:     actions,
		Roles:       bp.Roles,
	}
}

// followUpEvents returns the events of the transitions leaving any of the
// given allowed states — the events an action's executor should emit to
// advance the entity. Deterministic (sorted, de-duped) so the emitted
// descriptor is stable.
func followUpEvents(bp studio.Blueprint, allowedStates []string) []string {
	from := make(map[string]bool, len(allowedStates))
	for _, s := range allowedStates {
		from[s] = true
	}
	seen := make(map[string]bool)
	var out []string
	for _, t := range bp.Transitions {
		if from[t.From] && t.On != "" && !seen[t.On] {
			seen[t.On] = true
			out = append(out, t.On)
		}
	}
	sort.Strings(out)
	return nonNil(out)
}

// scaffoldDescriptorJSON renders the descriptor as indented JSON suitable
// for piping into `kiff scaffold -descriptor`.
func scaffoldDescriptorJSON(bp studio.Blueprint) ([]byte, error) {
	return json.MarshalIndent(scaffoldDescriptorFromBlueprint(bp), "", "  ")
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
