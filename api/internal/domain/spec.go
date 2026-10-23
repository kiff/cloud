// Package domain implements the cloud's YAML adapter to the
// framework's domain.Builder.
//
// YAML is a cloud surface, not a second canonical domain model. The
// parser translates a tenant-authored kiff.yaml into the framework's
// builder calls; the framework's domain.Builder.Build() remains the
// source of truth for structural validation. Unknown executor keys
// and structural violations are surfaced as a single accumulated
// list of errors so tenants editing YAML see every problem at once.
//
// Tenants cannot register their own Go executors; they reference
// the cloud's executor registry by string key. See
// internal/executors.
package domain

// spec is the structured intermediate the YAML decodes into. Field
// names use yaml tags so callers do not need a separate JSON
// representation. Empty slices and maps are tolerated; required
// fields are checked in Validate.
type spec struct {
	Domain      string           `yaml:"domain"`
	Entity      string           `yaml:"entity"`
	Events      []string         `yaml:"events"`
	States      []string         `yaml:"states"`
	Transitions []transitionSpec `yaml:"transitions"`
	Actions     []actionSpec     `yaml:"actions"`
	Permissions permissionsSpec  `yaml:"permissions"`
}

type transitionSpec struct {
	On   string `yaml:"on"`
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

type actionSpec struct {
	Name          string   `yaml:"name"`
	AllowedStates []string `yaml:"allowed_states"`
	// RequiredParameters names parameters that must be present and non-nil.
	// It is the simplest form and stays supported; Parameters below adds
	// type and value constraints for the same parameters.
	RequiredParameters []string `yaml:"required_parameters"`
	// Parameters declares typed parameter contracts. A value that violates
	// one is `invalid` (a malformed request), not `blocked` (a governance
	// refusal) — the distinction the outcome vocabulary already draws.
	//
	// Constraints here are STATIC: they compare a parameter against a
	// constant, not against the entity's own state. "Refund must not exceed
	// this order's captured amount" is therefore NOT expressible — that
	// needs per-entity facts in state, which the runtime does not carry
	// today. Keeping the limitation explicit is better than a grammar that
	// looks like it can do more than it can.
	Parameters          []parameterSpec `yaml:"parameters"`
	RequiredPermissions []string        `yaml:"required_permissions"`
	Risk                string          `yaml:"risk"`
	Approval            string          `yaml:"approval"`
	Executor            string          `yaml:"executor"`
	// RunContext declares this action's dependency on the trusted run
	// context the harness asserts. Absent means the action
	// does not consult it, which is every action authored before this
	// field existed.
	RunContext *runContextSpec `yaml:"run_context"`

	// RequiresAuthority makes an issued mandate a precondition for this
	// action rather than an optional cap on it.
	//
	// A mandate constrains without conferring: where none
	// covers a (subject, action), the check is a no-op. That was chosen
	// so a tenant's first mandate could not break a running
	// integration. This field is how an action opts out of that, and it
	// is declared per action rather than per tenant for the same reason
	// run_context is: the author of the domain knows which actions are
	// consequential, and a tenant-wide switch would make that judgement
	// for every action at once.
	//
	// False for every action authored before this existed, so adding
	// the field changes nothing until someone sets it.
	RequiresAuthority bool `yaml:"requires_authority"`
}

// runContextSpec is the YAML projection of an action's run-context
// dependency. One declared condition, not an expression language:
// Keep it narrow: the moment this grammar
// grows operators it becomes a worse OPA that an operations person
// can no longer read.
type runContextSpec struct {
	// OnUntrustedInput is what to do when this action is proposed in
	// a run the harness reported as having consumed untrusted content:
	// "approval" holds it for a human, "block" refuses it outright.
	//
	// The same outcome applies when NO run context was asserted at
	// all. An action that declares this dependency is saying the
	// answer depends on a fact about the run; a caller who supplies
	// no such fact has not established a clean run, they have
	// established nothing. See RunContextOutcome.
	OnUntrustedInput string `yaml:"on_untrusted_input"`

	// OnUntrustedSensitiveRead fires only when the run reported BOTH
	// untrusted input and a sensitive read — the exfiltration path
	// actually forming, rather than its first link.
	//
	// It exists because the first link alone does not discriminate.
	// The prompt-injection lab measured `on_untrusted_input` over 198
	// trials against an agent whose job begins by reading a public
	// issue: 90 of 90 benign runs were tainted, so the rule held every
	// benign write and was indistinguishable from requiring approval
	// on all of them. Gating on the conjunction instead held none of
	// them while still catching every run that reached for private
	// data.
	//
	// This is a second named condition, not an expression language.
	// It stays narrow: an operations person
	// reads one of two declared conditions, and the grammar grows a
	// new name when a new condition earns one rather than growing
	// operators.
	OnUntrustedSensitiveRead string `yaml:"on_untrusted_sensitive_read"`
}

// parameterSpec is the YAML projection of the framework's
// action.ParameterSpec. Min/Max are pointers so an omitted bound is
// distinguishable from a real zero.
type parameterSpec struct {
	Name          string   `yaml:"name"`
	Type          string   `yaml:"type"`
	Required      *bool    `yaml:"required"`
	Min           *int64   `yaml:"min"`
	Max           *int64   `yaml:"max"`
	MinLen        int      `yaml:"min_length"`
	MaxLen        int      `yaml:"max_length"`
	Pattern       string   `yaml:"pattern"`
	AllowedValues []string `yaml:"allowed_values"`
	// Unit and Scale say what an int amount means, for display only:
	// unit EUR with scale 2 makes 8000 read as €80.00. Enforcement never
	// reads them; a Card still sums the whole numbers.
	Unit  string `yaml:"unit"`
	Scale *int   `yaml:"scale"`
}

type permissionsSpec struct {
	Roles map[string][]string `yaml:"roles"`
}

// Spec is the public projection of the parsed YAML. ParseAndBuild
// returns it on the ParseResult so callers (notably the cloud's
// HTTP-level domain handlers) can echo back the tenant's
// declarations — event/state names, per-action executor keys —
// without reaching into the framework's opaque accessors.
//
// Fields mirror the unexported spec one-to-one. Mutating a Spec
// value does not affect the ParseResult's Definition.
type Spec struct {
	Domain      string
	Entity      string
	Events      []string
	States      []string
	Transitions []TransitionSpec
	Actions     []ActionSpec
	Permissions PermissionsSpec
}

// TransitionSpec is the public projection of one YAML transition.
type TransitionSpec struct {
	On   string
	From string
	To   string
}

// ActionSpec is the public projection of one YAML action.
// ActionSpec is serialized into the customer-facing proof pack, so its JSON
// shape is an evidence artifact. Parameters carries `omitempty` deliberately:
// a domain that declares no typed parameters must produce byte-identical
// evidence to one authored before this field existed. A golden test pins it.
type ActionSpec struct {
	Name                string
	AllowedStates       []string
	RequiredParameters  []string
	Parameters          []ParameterSpec `json:"Parameters,omitempty"`
	RequiredPermissions []string
	Risk                string
	Approval            string
	Executor            string
	// RunContext omits when unset so a domain authored before run contexts
	// produces byte-identical evidence to one authored after it. The
	// proof-pack golden test pins this.
	RunContext *RunContextSpec `json:"RunContext,omitempty"`

	// RequiresAuthority omits when false, for the same evidence-stability
	// reason as RunContext.
	RequiresAuthority bool `json:"RequiresAuthority,omitempty"`
}

// RunContextSpec is the public projection of an action's run-context
// dependency.
type RunContextSpec struct {
	// OnUntrustedInput is "approval" or "block". It governs both the
	// asserted-tainted case and the asserted-nothing case: absence of
	// an assertion is not evidence of a clean run.
	OnUntrustedInput string

	// OnUntrustedSensitiveRead is "approval" or "block", and fires on
	// the conjunction of untrusted input and a sensitive read. Omitted
	// when unset so a domain authored before it produces byte-identical
	// evidence to one authored after.
	OnUntrustedSensitiveRead string `json:"OnUntrustedSensitiveRead,omitempty"`
}

// Run-context dispositions. The vocabulary is closed and validated at
// parse time so a typo fails activation rather than silently leaving
// an action ungoverned — a misspelled disposition that parsed to "no
// rule" would be a control that reads as present and is not.
const (
	RunContextApproval = "approval"
	RunContextBlock    = "block"
)

// ValidRunContextDisposition reports whether d is a known disposition.
func ValidRunContextDisposition(d string) bool {
	switch d {
	case RunContextApproval, RunContextBlock:
		return true
	}
	return false
}

// ParameterSpec is the public projection of a typed parameter contract.
// Constraint fields omit when unset so the proof pack shows the constraints
// an author actually declared, not a wall of zero values.
type ParameterSpec struct {
	Name          string
	Type          string
	Required      bool
	Min           *int64   `json:"Min,omitempty"`
	Max           *int64   `json:"Max,omitempty"`
	MinLen        int      `json:"MinLen,omitempty"`
	MaxLen        int      `json:"MaxLen,omitempty"`
	Pattern       string   `json:"Pattern,omitempty"`
	AllowedValues []string `json:"AllowedValues,omitempty"`
	// Unit and Scale describe an int amount for display (unit EUR, scale 2:
	// 8000 is €80.00). Omitted when unset, like the constraints above.
	Unit  string `json:"Unit,omitempty"`
	Scale int    `json:"Scale,omitempty"`
}

// PermissionsSpec is the public projection of the permissions block.
type PermissionsSpec struct {
	Roles map[string][]string
}
