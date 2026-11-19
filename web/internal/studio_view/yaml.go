package studio_view

import (
	"fmt"

	studio "github.com/kiff/kiff-studio"
	"gopkg.in/yaml.v3"
)

// yaml.go — YAML round-trip helpers for the Studio surface.
//
// kiff-studio v0.1.0 ships an emit-only path (Blueprint →
// kiff.yaml bytes via studio.BuildYAML). The web binary needs
// the inverse for the auto-save loop:
//
//   1. GET /v1/me/domain/draft → YAML bytes
//   2. ParseYAML(bytes) → Blueprint
//   3. handler mutates Blueprint per the user's edit
//   4. studio.BuildYAML(Blueprint) → new YAML bytes
//   5. PUT /v1/me/domain/draft (new bytes)
//
// This file is the bridge between (1) and (2). It uses
// yaml.v3 directly because the package's parsing surface
// would either come from kiff-studio v0.2 (out of scope) or
// the cloud-api's domain.ParseAndBuild (which validates against
// the executor registry — too strict for drafts mid-edit).
//
// The shape this parses MUST match what studio.BuildYAML emits.
// A round-trip test (yaml_test.go) pins parity.

// yamlBlueprint mirrors the structure studio.BuildYAML produces.
// Fields use yaml tags directly so unmarshalling is a single
// yaml.Unmarshal call.
type yamlBlueprint struct {
	Domain      string           `yaml:"domain"`
	Entity      string           `yaml:"entity"`
	Events      []string         `yaml:"events"`
	States      []string         `yaml:"states"`
	Transitions []yamlTransition `yaml:"transitions"`
	Actions     []yamlAction     `yaml:"actions"`
	Permissions *yamlPermissions `yaml:"permissions,omitempty"`
}

type yamlTransition struct {
	On   string `yaml:"on"`
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

type yamlAction struct {
	Name                string   `yaml:"name"`
	AllowedStates       []string `yaml:"allowed_states"`
	RequiredParameters  []string `yaml:"required_parameters"`
	RequiredPermissions []string `yaml:"required_permissions"`
	Risk                string   `yaml:"risk"`
	Approval            string   `yaml:"approval"`
	Executor            string   `yaml:"executor"`
}

type yamlPermissions struct {
	Roles map[string][]string `yaml:"roles"`
}

// ParseYAML decodes a kiff.yaml back into a Blueprint. Returns
// the parsed Blueprint plus any decoding error. The Blueprint is
// not validated here — drafts may be mid-edit and shape-incomplete.
// Callers that need validation should call Blueprint.Validate
// explicitly.
func ParseYAML(bytes []byte) (studio.Blueprint, error) {
	var raw yamlBlueprint
	if err := yaml.Unmarshal(bytes, &raw); err != nil {
		return studio.Blueprint{}, fmt.Errorf("studio_view: parse yaml: %w", err)
	}

	bp := studio.Blueprint{
		Domain: raw.Domain,
		Entity: raw.Entity,
		Events: append([]string(nil), raw.Events...),
		States: append([]string(nil), raw.States...),
	}
	for _, t := range raw.Transitions {
		bp.Transitions = append(bp.Transitions, studio.Transition{
			On:   t.On,
			From: t.From,
			To:   t.To,
		})
	}
	for _, a := range raw.Actions {
		bp.Actions = append(bp.Actions, studio.Action{
			Name:                a.Name,
			AllowedStates:       append([]string(nil), a.AllowedStates...),
			RequiredParameters:  append([]string(nil), a.RequiredParameters...),
			RequiredPermissions: append([]string(nil), a.RequiredPermissions...),
			Risk:                a.Risk,
			ApprovalRequired:    a.Approval == "required",
			Executor:            a.Executor,
		})
	}
	if raw.Permissions != nil {
		bp.Roles = make(map[string][]string, len(raw.Permissions.Roles))
		for r, perms := range raw.Permissions.Roles {
			bp.Roles[r] = append([]string(nil), perms...)
		}
	}
	return bp, nil
}

// renderPartialYAML is the fallback for YAMLPreview when
// Blueprint.Validate fails. Mirrors the shape studio.BuildYAML
// emits but tolerates incomplete data (empty states, empty
// risk, etc.) — the middle pane stays useful while the user
// types.
//
// MUST be round-trip-faithful: every populated field on the
// Blueprint emits, so a draft that fails validation today still
// reconstructs identically through ParseYAML on the next reload.
// Without this, the auto-save spine could lose user-typed data
// when the Blueprint drops below an earlier stage's criteria
// (e.g. blanking the entity name → fallback path → permissions
// silently dropped). Tests pin the property in
// TestPartialYAML_RoundTrip_PreservesAllPopulatedFields.
func renderPartialYAML(b studio.Blueprint) string {
	var out string
	out += "# Draft preview · validation will run on save\n"
	if b.Domain != "" {
		out += "domain: " + b.Domain + "\n"
	} else {
		out += "domain: <pick a name>\n"
	}
	if b.Entity != "" {
		out += "entity: " + b.Entity + "\n"
	} else {
		out += "entity: <pick an entity>\n"
	}
	out += "\n"
	if len(b.Events) > 0 {
		out += "events:\n"
		for _, e := range b.Events {
			out += "  - " + e + "\n"
		}
		out += "\n"
	}
	if len(b.States) > 0 {
		out += "states:\n"
		for _, s := range b.States {
			out += "  - " + s + "\n"
		}
		out += "\n"
	}
	if len(b.Transitions) > 0 {
		out += "transitions:\n"
		for _, t := range b.Transitions {
			out += "  - on: " + t.On + "\n"
			if t.From == "" {
				out += `    from: ""` + "\n"
			} else {
				out += "    from: " + t.From + "\n"
			}
			out += "    to: " + t.To + "\n"
		}
		out += "\n"
	}
	if len(b.Actions) > 0 {
		out += "actions:\n"
		for _, a := range b.Actions {
			out += "  - name: " + a.Name + "\n"
			// Always emit allowed_states, required_parameters,
			// and required_permissions — even when empty — so
			// ParseYAML reconstructs a Blueprint with the same
			// slice shapes (nil vs empty). yaml.v3 unmarshalling
			// treats absent fields as nil; emitting [] keeps the
			// shape stable through round-trip.
			if len(a.AllowedStates) > 0 {
				out += "    allowed_states: [" + joinComma(a.AllowedStates) + "]\n"
			} else {
				out += "    allowed_states: []\n"
			}
			if len(a.RequiredParameters) > 0 {
				out += "    required_parameters: [" + joinComma(a.RequiredParameters) + "]\n"
			} else {
				out += "    required_parameters: []\n"
			}
			if len(a.RequiredPermissions) > 0 {
				out += "    required_permissions: [" + joinComma(a.RequiredPermissions) + "]\n"
			} else {
				out += "    required_permissions: []\n"
			}
			if a.Risk != "" {
				out += "    risk: " + a.Risk + "\n"
			}
			// Always emit approval so the boolean round-trips.
			if a.ApprovalRequired {
				out += "    approval: required\n"
			} else {
				out += "    approval: never\n"
			}
			if a.Executor != "" {
				out += "    executor: " + a.Executor + "\n"
			}
		}
		out += "\n"
	}
	if len(b.Roles) > 0 {
		out += "permissions:\n"
		out += "  roles:\n"
		// Sort role names for deterministic output (matches
		// studio.BuildYAML's posture in v0.1.0).
		names := make([]string, 0, len(b.Roles))
		for name := range b.Roles {
			names = append(names, name)
		}
		// Insertion sort — small maps, simpler than importing
		// sort just for this.
		for i := 1; i < len(names); i++ {
			for j := i; j > 0 && names[j] < names[j-1]; j-- {
				names[j], names[j-1] = names[j-1], names[j]
			}
		}
		for _, name := range names {
			out += "    " + name + ":\n"
			for _, p := range b.Roles[name] {
				out += "      - " + p + "\n"
			}
		}
	}
	return out
}

func joinComma(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
