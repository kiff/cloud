package candidate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kiff/cloud/api/internal/guard"
)

// Check is one grounding assertion's result. Detail is human-legible so a
// failure says which candidate, which check, and what the binding said.
type Check struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// Report is the structured grounding verdict — never a bare boolean, so
// failures are inspectable. OK is the AND of every check.
type Report struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// Validate is the grounding gate: it asserts every candidate is anchored
// to the runtime observation and that nothing was invented. This is the
// spike's trust mechanism — the derived candidates are only as credible
// as this report.
//
// It does NOT validate risk_reasons (natural language, judged
// qualitatively) and does NOT assert ranking (a multi-tool concern left
// to Fixture B).
func Validate(obs guard.Observation, controls []Control) Report {
	// Index the observation's tools and their schema property sets.
	toolNames := map[string]bool{}
	toolByName := map[string]guard.ToolObservation{}
	schemaProps := map[string]map[string]bool{}
	schemaRequired := map[string]map[string]bool{}
	for _, t := range obs.Tools {
		toolNames[t.Name] = true
		toolByName[t.Name] = t
		props, req := schemaSets(t)
		schemaProps[t.Name] = props
		schemaRequired[t.Name] = req
	}

	var checks []Check
	add := func(name string, pass bool, detail string) {
		checks = append(checks, Check{Name: name, Pass: pass, Detail: detail})
	}

	// 1. No phantom tools.
	phantomTool := ""
	for _, c := range controls {
		if !toolNames[c.Tool] {
			phantomTool = c.Tool
			break
		}
	}
	add("no_phantom_tools", phantomTool == "",
		ifFail(phantomTool != "", fmt.Sprintf("candidate tool %q is not in the runtime observation", phantomTool)))

	// 2. No phantom fields — every field name exists in the tool's schema
	//    (skipped for tools with no schema, where fields come from the
	//    binding's required list and are grounded by construction).
	phantomField := ""
	for _, c := range controls {
		props := schemaProps[c.Tool]
		if len(props) == 0 {
			continue
		}
		for _, f := range c.Fields {
			if !props[f.Name] {
				phantomField = c.Tool + "." + f.Name
				break
			}
		}
		if phantomField != "" {
			break
		}
	}
	add("no_phantom_fields", phantomField == "",
		ifFail(phantomField != "", fmt.Sprintf("candidate field %q is not in the tool's parameter schema", phantomField)))

	// 3. Required fidelity — the required field set matches the schema's
	//    required set exactly (no additions, no omissions).
	reqMismatch := ""
	for _, c := range controls {
		want := schemaRequired[c.Tool]
		if want == nil {
			continue // no schema to compare against
		}
		got := map[string]bool{}
		for _, f := range c.Fields {
			if f.Required {
				got[f.Name] = true
			}
		}
		if !sameSet(got, want) {
			reqMismatch = fmt.Sprintf("%s: required got %s want %s", c.Tool, keys(got), keys(want))
			break
		}
	}
	add("required_fidelity", reqMismatch == "", reqMismatch)

	// 4. Thresholds null — never invented.
	badThreshold := ""
	for _, c := range controls {
		if c.PolicyThresholds != nil {
			badThreshold = c.Tool
			break
		}
	}
	add("thresholds_null", badThreshold == "",
		ifFail(badThreshold != "", fmt.Sprintf("candidate %q has non-null policy_thresholds", badThreshold)))

	// 5. States null — never invented.
	badState := ""
	for _, c := range controls {
		if c.States != nil {
			badState = c.Tool
			break
		}
	}
	add("states_null", badState == "",
		ifFail(badState != "", fmt.Sprintf("candidate %q has non-null states", badState)))

	// 6. Evidence grounded in the runtime — source is the runtime and the
	//    cited tool matches the candidate.
	badEvidence := ""
	for _, c := range controls {
		if c.Evidence.Source != SourceRuntime {
			badEvidence = fmt.Sprintf("%s: source %q != %q", c.Tool, c.Evidence.Source, SourceRuntime)
			break
		}
		if c.Evidence.Tool != c.Tool {
			badEvidence = fmt.Sprintf("%s: evidence.tool %q != candidate tool", c.Tool, c.Evidence.Tool)
			break
		}
	}
	add("evidence_runtime_grounded", badEvidence == "", badEvidence)

	// 7. Governance seam matches the observed binding. This is the core of
	//    the runtime-first proof: the candidate's action / entity_type /
	//    entity_arg must equal what the connected runtime bound, so a
	//    candidate cannot silently re-point ISSUE_REFUND at DELETE_ORDER
	//    or swap the entity. Compared against the resolved binding (same
	//    action fallback DeriveFromRuntime uses for empty Action).
	seamMismatch := ""
	for _, c := range controls {
		observed, ok := toolByName[c.Tool]
		if !ok {
			continue // already caught by no_phantom_tools
		}
		switch {
		case c.Action != actionOf(observed):
			seamMismatch = fmt.Sprintf("%s: action %q != bound %q", c.Tool, c.Action, actionOf(observed))
		case c.EntityType != observed.EntityType:
			seamMismatch = fmt.Sprintf("%s: entity_type %q != bound %q", c.Tool, c.EntityType, observed.EntityType)
		case c.EntityArg != observed.EntityArg:
			seamMismatch = fmt.Sprintf("%s: entity_arg %q != bound %q", c.Tool, c.EntityArg, observed.EntityArg)
		}
		if seamMismatch != "" {
			break
		}
	}
	add("governance_seam_matches", seamMismatch == "", seamMismatch)

	ok := true
	for _, c := range checks {
		if !c.Pass {
			ok = false
			break
		}
	}
	return Report{OK: ok, Checks: checks}
}

func schemaSets(tool guard.ToolObservation) (props map[string]bool, required map[string]bool) {
	props = map[string]bool{}
	required = map[string]bool{}
	if len(tool.ParameterSchema) == 0 {
		return props, required
	}
	var shape schemaShape
	if err := json.Unmarshal(tool.ParameterSchema, &shape); err != nil {
		return props, required
	}
	for name := range shape.Properties {
		props[name] = true
	}
	for _, r := range shape.Required {
		required[r] = true
	}
	return props, required
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func keys(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return "[" + strings.Join(out, ", ") + "]"
}

func ifFail(failed bool, detail string) string {
	if failed {
		return detail
	}
	return ""
}
