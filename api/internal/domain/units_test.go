package domain

import (
	"strings"
	"testing"
)

// unitDomain is a minimal domain whose refund action declares amount_cents
// with the given extra parameter lines.
func unitDomain(extra string) []byte {
	return []byte(`domain: gateway
entity: ToolCall
events: [TOOL_CALL_REQUESTED]
states: [requested]
transitions:
  - on: TOOL_CALL_REQUESTED
    from: ""
    to: requested
actions:
  - name: refund
    allowed_states: [requested]
    required_parameters: [amount_cents]
    parameters:
      - name: amount_cents
` + extra + `
    required_permissions: [tools.refund]
    risk: medium
    approval: never
    executor: cloud.proxy
permissions:
  roles:
    gateway_agent: [tools.refund]
`)
}

// An int amount may say what it means (unit EUR, scale 2: cents); the
// public spec carries it for display, and a domain without it is unchanged.
func TestParameterUnit(t *testing.T) {
	res, err := ParseAndBuild(unitDomain("        type: int\n        min: 0\n        unit: EUR\n        scale: 2"), nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := res.Spec.Actions[0].Parameters[0]
	if p.Unit != "EUR" || p.Scale != 2 {
		t.Fatalf("unit/scale = %q/%d, want EUR/2", p.Unit, p.Scale)
	}
	res, err = ParseAndBuild(unitDomain("        type: int"), nil)
	if err != nil {
		t.Fatalf("parse without unit: %v", err)
	}
	if p := res.Spec.Actions[0].Parameters[0]; p.Unit != "" || p.Scale != 0 {
		t.Fatalf("unit/scale without a unit = %q/%d", p.Unit, p.Scale)
	}
}

func TestParameterUnitIsChecked(t *testing.T) {
	for _, tc := range []struct{ name, extra, want string }{
		{"not an int", "        type: string\n        unit: EUR", "unit/scale apply to int only"},
		{"scale without unit", "        type: int\n        scale: 2", "is required with scale"},
		{"scale too large", "        type: int\n        unit: EUR\n        scale: 7", "must be 0 to 6"},
		{"negative scale", "        type: int\n        unit: EUR\n        scale: -1", "must be 0 to 6"},
		{"not a word", "        type: int\n        unit: \"€\"", "must be letters only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAndBuild(unitDomain(tc.extra), nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to say %q", err, tc.want)
			}
		})
	}
}
