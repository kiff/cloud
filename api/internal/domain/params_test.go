package domain

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/executors"
	kiffaction "github.com/kiff/kiff/pkg/kiff/action"
)

// The typed-parameter block exists so a domain can reject a malformed value
// as `invalid` rather than leaving the integration to check it locally. These
// tests pin what the grammar accepts, and — just as important — what it
// cannot express, so nobody reads more safety into it than it has.

// parseYAML returns the result and the joined validation error (nil when the
// domain is valid).
func parseYAML(t *testing.T, y string) (ParseResult, error) {
	t.Helper()
	return ParseAndBuild([]byte(y), executors.DefaultRegistry())
}

const paramsHeader = `domain: t
entity: Order
events: [PLACED]
states: [OPEN]
transitions:
  - {on: PLACED, from: "", to: OPEN}
actions:
  - name: ACT
    allowed_states: [OPEN]
    executor: cloud.proxy
`

func TestTypedParametersReachTheContract(t *testing.T) {
	res, err := parseYAML(t, paramsHeader+`    parameters:
      - {name: amount_minor, type: int, min: 1, max: 1000000}
      - {name: currency, type: enum, allowed_values: [EUR]}
      - {name: reason, type: string, min_length: 1, max_length: 200}
`)
	if err != nil {
		t.Fatalf("unexpected errors: %v", err)
	}
	contract, ok := res.Definition.Actions.Get("ACT")
	if !ok {
		t.Fatal("ACT not in the catalog")
	}
	if len(contract.Parameters) != 3 {
		t.Fatalf("want 3 typed parameters on the contract, got %d", len(contract.Parameters))
	}
	amount := contract.Parameters[0]
	if amount.Name != "amount_minor" || string(amount.Type) != "int" {
		t.Errorf("amount mapped wrong: %+v", amount)
	}
	if amount.Min == nil || *amount.Min != 1 || amount.Max == nil || *amount.Max != 1_000_000 {
		t.Errorf("int bounds did not survive the mapping: %+v", amount)
	}
	// Omitted `required` defaults to true: a parameter worth describing is
	// one the contract expects.
	if !amount.Required {
		t.Error("required should default to true when omitted")
	}
}

func TestRequiredCanBeExplicitlyFalse(t *testing.T) {
	res, err := parseYAML(t, paramsHeader+`    parameters:
      - {name: note, type: string, required: false}
`)
	if err != nil {
		t.Fatalf("unexpected errors: %v", err)
	}
	c, _ := res.Definition.Actions.Get("ACT")
	if c.Parameters[0].Required {
		t.Error("required: false should survive the mapping")
	}
}

func TestInvalidParameterSpecsFailAtAuthorTime(t *testing.T) {
	cases := []struct{ name, block, want string }{
		{"unknown type", "      - {name: x, type: money}", "unknown type"},
		{"missing type", "      - {name: x}", "is required"},
		{"missing name", "      - {type: int}", "is required"},
		{"min above max", "      - {name: x, type: int, min: 10, max: 1}", "min is greater than max"},
		{"enum without values", "      - {name: x, type: enum}", "allowed values"},
		{"allowed_values on int", "      - {name: x, type: int, allowed_values: [a]}", "not valid for an int"},
		{"min on a string", "      - {name: x, type: string, min: 1}", "min/max apply to int only"},
		{"constraints on a bool", "      - {name: x, type: bool, min: 1}", "min/max apply to int only"},
		{"bad regex", "      - {name: x, type: string, pattern: \"([\"}", "not a valid regular expression"},
		{"duplicate name", "      - {name: x, type: int}\n      - {name: x, type: int}", "duplicate parameter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseYAML(t, paramsHeader+"    parameters:\n"+tc.block+"\n")
			if err == nil {
				t.Fatalf("expected a validation error containing %q", tc.want)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Errorf("want an error containing %q, got:\n%s", tc.want, err)
			}
		})
	}
}

// A domain with no parameters block behaves exactly as before. Every domain
// authored before this grammar existed must keep parsing unchanged.
func TestParametersAreOptional(t *testing.T) {
	res, err := parseYAML(t, paramsHeader+"    required_parameters: [amount_minor]\n")
	if err != nil {
		t.Fatalf("unexpected errors: %v", err)
	}
	c, _ := res.Definition.Actions.Get("ACT")
	if len(c.Parameters) != 0 {
		t.Errorf("no parameters block should mean no typed parameters, got %d", len(c.Parameters))
	}
	if len(c.RequiredParameters) != 1 {
		t.Errorf("required_parameters must keep working alongside: %+v", c.RequiredParameters)
	}
}

// The limitation, pinned deliberately. Constraints compare a parameter to a
// CONSTANT. "Refund must not exceed this order's captured amount" is
// per-entity and is not expressible — the runtime carries no such fact. If
// this test ever needs changing, the grammar gained real power and the docs
// claiming otherwise must change with it.
func TestConstraintsCannotReferenceEntityState(t *testing.T) {
	res, err := parseYAML(t, paramsHeader+`    parameters:
      - {name: amount_minor, type: int, max: 1000000}
`)
	if err != nil {
		t.Fatalf("unexpected errors: %v", err)
	}
	c, _ := res.Definition.Actions.Get("ACT")
	if c.Parameters[0].Max == nil || *c.Parameters[0].Max != 1_000_000 {
		t.Fatal("static max should be the only bound available")
	}
	// There is no field through which a bound could name entity state.
	// Asserting the shape keeps the claim honest rather than aspirational.
	if c.ValidateParameters != nil {
		t.Error("the YAML grammar must not be able to install a semantic hook")
	}
}

// The proof pack is a customer-facing evidence artifact, so adding a field to
// ActionSpec changes evidence for every tenant — including those not using the
// feature. This asserts the omitempty that keeps a parameterless domain
// serializing exactly as it did before the grammar existed. It is the unit-level
// guard for handlers.TestProofPack_GoldenByteForByte, which caught this.
func TestParameterlessActionSerializesUnchanged(t *testing.T) {
	res, err := parseYAML(t, paramsHeader+"    required_parameters: [amount_minor]\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var spec ActionSpec
	for _, a := range res.Spec.Actions {
		if a.Name == "ACT" {
			spec = a
		}
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "Parameters\":null") ||
		strings.Contains(string(encoded), "\"Parameters\"") {
		t.Errorf("an action with no typed parameters must not emit a Parameters key:\n%s", encoded)
	}
}

func TestDeclaredParametersDoAppearInTheProjection(t *testing.T) {
	res, err := parseYAML(t, paramsHeader+`    parameters:
      - {name: amount_minor, type: int, min: 1}
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	encoded, _ := json.Marshal(res.Spec.Actions[0])
	for _, want := range []string{`"Parameters"`, `"amount_minor"`, `"Min":1`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("projection missing %s:\n%s", want, encoded)
		}
	}
	// Unset constraints stay out, so the evidence shows what was declared.
	if strings.Contains(string(encoded), `"Pattern"`) || strings.Contains(string(encoded), `"MaxLen"`) {
		t.Errorf("unset constraints should not appear:\n%s", encoded)
	}
}

// The mapping is only worth having if the framework then enforces it. This
// runs the validator the proposal gate runs, against a domain authored purely
// in YAML, and checks that a violating value is refused.
func TestConstraintsAreEnforcedByTheValidator(t *testing.T) {
	res, err := parseYAML(t, paramsHeader+`    parameters:
      - {name: amount_minor, type: int, min: 1, max: 100000}
      - {name: currency, type: enum, allowed_values: [EUR, GBP]}
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	contract, ok := res.Definition.Actions.Get("ACT")
	if !ok {
		t.Fatal("ACT missing")
	}

	validator := kiffaction.NewDefaultValidator()
	base := kiffaction.ActionContext{
		ActionName: "ACT", EntityID: "e1", EntityType: "Order", CurrentState: "OPEN",
	}

	cases := []struct {
		name    string
		params  map[string]any
		wantErr bool
	}{
		{"within bounds", map[string]any{"amount_minor": 45000, "currency": "EUR"}, false},
		{"above max", map[string]any{"amount_minor": 5000000, "currency": "EUR"}, true},
		{"below min", map[string]any{"amount_minor": 0, "currency": "EUR"}, true},
		{"currency outside the enum", map[string]any{"amount_minor": 45000, "currency": "USD"}, true},
		{"wrong type", map[string]any{"amount_minor": "lots", "currency": "EUR"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := base
			ctx.Parameters = tc.params
			_, err := validator.Validate(context.Background(), ctx, contract, res.Policy)
			if tc.wantErr && err == nil {
				t.Fatalf("expected a validation error for %v", tc.params)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %v: %v", tc.params, err)
			}
			// A malformed value is `invalid` (a bad request), never `blocked`
			// (a governance refusal). The outcome vocabulary depends on it.
			if tc.wantErr && !errors.Is(err, kiffaction.ErrInvalidParameter) {
				t.Errorf("want ErrInvalidParameter, got %v", err)
			}
		})
	}
}
