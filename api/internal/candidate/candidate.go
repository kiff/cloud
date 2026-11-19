// Package candidate derives candidate controls from a CONNECTED kiff-guard
// runtime's ToolMap / observation metadata — the same governance seam the
// adapter uses to intercept execution (guard.evaluate).
//
// History: this began as a throwaway evaluation loop, since proven
// and closed. The derivation core
// (DeriveFromRuntime + Validate) graduated into a production path: the
// API's GET /v1/guard/candidates endpoint calls DeriveForObservation
// (see derive.go) over a tenant's stored observations. What stays dev-only:
// cmd/spike-candidate (the CLI), the on-disk testdata fixtures, and Path B
// (Bedrock) synthesis — none are imported by the production endpoint.
//
// Scope of the deterministic core:
//   - Path A only (deterministic). Bedrock Path B risk_reasons synthesis
//     is a follow-up; here risk_reasons are mechanical and clearly so.
//
// Honesty boundary (enforced by validate.go): every field of a candidate
// is grounded in the runtime binding. Policy thresholds and the state
// machine are NEVER invented — they render as null for a human to fill.
package candidate

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/kiff/cloud/api/internal/guard"
)

// SourceRuntime is the evidence source string for every candidate derived
// from a connected guard runtime. The spike's whole point is that the
// candidate is runtime-grounded, not manifest-uploaded.
const SourceRuntime = "kiff-guard runtime"

// Field is one parameter of a tool, derived from the tool's parameter
// schema. Required mirrors the schema's required set exactly — never
// added to or omitted.
type Field struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// Evidence cites where a candidate came from. The source is always the
// connected runtime; the binding fields let a reviewer trace the candidate
// back to the exact guard.evaluate boundary. Project/Environment complete
// the runtime coordinate so a Studio consumer can group candidates by the
// same key the observation was stored under (tenant/project/env/agent/workflow).
type Evidence struct {
	Source      string `json:"source"`      // always SourceRuntime
	Tool        string `json:"tool"`        // the bound tool name
	Adapter     string `json:"adapter"`     // langgraph, agno, …
	Project     string `json:"project"`     // cookbook, …
	Environment string `json:"environment"` // aws, dev, …
	Workflow    string `json:"workflow"`    // refund-ceiling, …
	Mode        string `json:"mode"`        // observe | enforce
}

// Control is one reviewable candidate control derived from a runtime
// tool binding. PolicyThresholds and States are pointers left nil so they
// marshal to JSON null: KIFF derives the action surface, never the
// thresholds or the state machine.
type Control struct {
	Tool        string   `json:"tool"`
	Action      string   `json:"action"`
	EntityType  string   `json:"entity_type"`
	EntityArg   string   `json:"entity_arg"`
	Fields      []Field  `json:"fields"`
	Risk        string   `json:"risk"`         // high | medium | low
	RiskReasons []string `json:"risk_reasons"` // Path A: mechanical, deterministic
	Evidence    Evidence `json:"evidence"`

	// ObservedCallCount is how many times the connected runtime reported
	// calling this tool. Grounded in the observation (never synthesised);
	// omitted when the runtime did not report a count.
	ObservedCallCount uint64 `json:"observed_call_count,omitempty"`

	// Never derived. Always null. A human sets these.
	PolicyThresholds *json.RawMessage `json:"policy_thresholds"`
	States           *json.RawMessage `json:"states"`
}

// DeriveFromRuntime turns a connected runtime's observation into candidate
// controls. One candidate per observed tool, returned ranked by risk
// (high → medium → low, stable within a tier so observation order is
// preserved for ties). Deterministic and offline: no model call, no
// network. Connection supplies the evidence binding (adapter / workflow /
// mode); Observation supplies the tools.
func DeriveFromRuntime(conn guard.Connection, obs guard.Observation) []Control {
	out := make([]Control, 0, len(obs.Tools))
	for _, tool := range obs.Tools {
		fields := fieldsFromSchema(tool)
		risk, reasons := riskOf(tool)
		out = append(out, Control{
			Tool:        tool.Name,
			Action:      actionOf(tool),
			EntityType:  tool.EntityType,
			EntityArg:   tool.EntityArg,
			Fields:      fields,
			Risk:        risk,
			RiskReasons: reasons,
			Evidence: Evidence{
				Source:      SourceRuntime,
				Tool:        tool.Name,
				Adapter:     conn.Adapter,
				Project:     conn.Project,
				Environment: conn.Environment,
				Workflow:    conn.Workflow,
				Mode:        conn.Mode,
			},
			ObservedCallCount: tool.ObservedCallCount,
			// Never invented.
			PolicyThresholds: nil,
			States:           nil,
		})
	}
	// Path A ranking: surface higher-risk side-effecting tools above
	// lower-risk / read-only ones. Stable so ties keep observation order.
	sort.SliceStable(out, func(i, j int) bool {
		return RiskRank(out[i].Risk) > RiskRank(out[j].Risk)
	})
	return out
}

// RiskRank maps a risk tier to a comparable weight for ranking.
// Unknown tiers sort lowest.
func RiskRank(risk string) int {
	switch risk {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// actionOf prefers the binding's action; falls back to the upper-snake of
// the tool name so a candidate always has an action label.
func actionOf(tool guard.ToolObservation) string {
	if a := strings.TrimSpace(tool.Action); a != "" {
		return a
	}
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(tool.Name), "-", "_"))
}

// schemaShape is the minimal JSON-schema subset we read from a tool's
// parameter_schema: property names + types and the required set.
type schemaShape struct {
	Type       string `json:"type"`
	Properties map[string]struct {
		Type string `json:"type"`
	} `json:"properties"`
	Required []string `json:"required"`
}

// fieldsFromSchema reads the tool's parameter schema into Fields. Required
// is taken verbatim from the schema's required set (grounded, not guessed).
// When no schema is present we fall back to the binding's Required list so
// a candidate still lists the action's required params.
func fieldsFromSchema(tool guard.ToolObservation) []Field {
	var shape schemaShape
	if len(tool.ParameterSchema) > 0 {
		_ = json.Unmarshal(tool.ParameterSchema, &shape)
	}

	if len(shape.Properties) > 0 {
		required := map[string]bool{}
		for _, r := range shape.Required {
			required[r] = true
		}
		names := make([]string, 0, len(shape.Properties))
		for name := range shape.Properties {
			names = append(names, name)
		}
		sort.Strings(names) // deterministic order
		fields := make([]Field, 0, len(names))
		for _, name := range names {
			fields = append(fields, Field{
				Name:     name,
				Type:     orUnknown(shape.Properties[name].Type),
				Required: required[name],
			})
		}
		return fields
	}

	// No schema: surface the binding's required params as required fields
	// of unknown type. Honest about what we do and do not know.
	fields := make([]Field, 0, len(tool.Required))
	for _, name := range tool.Required {
		fields = append(fields, Field{Name: name, Type: "unknown", Required: true})
	}
	return fields
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

// riskOf is the deterministic Path A heuristic: scan the tool's
// name/action/description for risk signals and return a tier plus the
// MECHANICAL reasons that triggered it. These reasons are intentionally
// literal (e.g. "matched signal: refund") — turning them into readable
// rationale ("irreversible", "moves money") is the Bedrock Path B job the
// spike compares against, not done here.
func riskOf(tool guard.ToolObservation) (string, []string) {
	hay := strings.ToLower(tool.Name + " " + tool.Action + " " + tool.Description)

	highSignals := []string{"pay", "payment", "refund", "charge", "transfer", "delete", "wire", "payout"}
	medSignals := []string{"send", "email", "notify", "notification", "update", "create", "escalate", "contact", "offer"}

	var matched []string
	for _, s := range highSignals {
		if strings.Contains(hay, s) {
			matched = append(matched, "matched signal: "+s)
		}
	}
	if len(matched) > 0 {
		return "high", matched
	}
	for _, s := range medSignals {
		if strings.Contains(hay, s) {
			matched = append(matched, "matched signal: "+s)
		}
	}
	if len(matched) > 0 {
		return "medium", matched
	}
	return "low", []string{"no configured risk signal matched"}
}
