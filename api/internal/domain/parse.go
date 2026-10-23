package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	kiffaction "github.com/kiff/kiff/pkg/kiff/action"
	kiffdomain "github.com/kiff/kiff/pkg/kiff/domain"
	kiffpermission "github.com/kiff/kiff/pkg/kiff/permission"

	"github.com/kiff/cloud/api/internal/executors"
)

// ParseResult is what ParseAndBuild returns on success: the
// definition the framework's domain.Builder produced, plus the
// permission policy assembled from the YAML's permissions block.
//
// Spec is the raw YAML structure pre-build, kept around so
// callers (in particular the cloud's HTTP-level domain handlers)
// can echo back the original event/state names + per-action
// executor keys without re-deriving them from the framework's
// opaque accessor surface.
//
// Policy is a ContextPolicy: authority is resolved from the
// trusted identity on the request context, not from caller-supplied
// actor.Roles. It satisfies the framework's permission.Policy
// interface, so the runtime consumes it unchanged.
type ParseResult struct {
	Definition kiffdomain.Definition
	Policy     kiffpermission.Policy
	Spec       Spec
}

// ValidationError represents one YAML validation error. It carries
// a path string for the offending field so tenants can locate the
// problem in their YAML.
type ValidationError struct {
	Path    string
	Message string
}

// Error implements error.
func (e ValidationError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

// ParseAndBuild parses the YAML bytes into a *Definition. Validation
// errors are accumulated; the function returns errors.Join of every
// problem found rather than failing on the first one. This is
// deliberate: tenants editing YAML benefit from seeing every problem
// at once.
//
// The registry is consulted for executor-key membership. Unknown
// keys produce a ValidationError pointing at the offending action.
//
// Successful parse returns a ParseResult whose Definition has
// already been validated by the framework's Build(); callers can
// pass it directly to runtime.NewForDomain.
func ParseAndBuild(yamlBytes []byte, registry *executors.Registry) (ParseResult, error) {
	if len(yamlBytes) == 0 {
		return ParseResult{}, errors.New("domain: yaml is empty")
	}

	var s spec
	dec := yaml.NewDecoder(strings.NewReader(string(yamlBytes)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return ParseResult{}, fmt.Errorf("domain: yaml decode: %w", err)
	}

	violations := validate(s, registry)
	if len(violations) > 0 {
		errs := make([]error, len(violations))
		for i, v := range violations {
			errs[i] = v
		}
		return ParseResult{}, errors.Join(errs...)
	}

	def, err := build(s, registry)
	if err != nil {
		return ParseResult{}, fmt.Errorf("domain: build: %w", err)
	}

	policy := newContextPolicy(s.Permissions.Roles)

	return ParseResult{Definition: def, Policy: policy, Spec: specToPublic(s)}, nil
}

// specToPublic copies the unexported spec into the public Spec
// projection. Slices are duplicated so callers who mutate the
// returned value do not affect the catalog.
func specToPublic(s spec) Spec {
	out := Spec{
		Domain:      s.Domain,
		Entity:      s.Entity,
		Events:      append([]string(nil), s.Events...),
		States:      append([]string(nil), s.States...),
		Transitions: make([]TransitionSpec, len(s.Transitions)),
		Actions:     make([]ActionSpec, len(s.Actions)),
		Permissions: PermissionsSpec{Roles: copyRoles(s.Permissions.Roles)},
	}
	for i, t := range s.Transitions {
		out.Transitions[i] = TransitionSpec{On: t.On, From: t.From, To: t.To}
	}
	for i, a := range s.Actions {
		out.Actions[i] = ActionSpec{
			Name:                a.Name,
			AllowedStates:       append([]string(nil), a.AllowedStates...),
			RequiredParameters:  append([]string(nil), a.RequiredParameters...),
			Parameters:          toPublicParams(a.Parameters),
			RequiredPermissions: append([]string(nil), a.RequiredPermissions...),
			Risk:                a.Risk,
			Approval:            a.Approval,
			Executor:            a.Executor,
			RunContext:          toPublicRunContext(a.RunContext),
			RequiresAuthority:   a.RequiresAuthority,
		}
	}
	return out
}

func copyRoles(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// validate walks the spec and returns every structural problem it
// finds. The framework's Build() does the deeper checks (state
// reachability, action contract completeness); validate covers
// shape-level concerns: required fields, set membership, executor
// key membership, internal consistency.
func validate(s spec, registry *executors.Registry) []ValidationError {
	var errs []ValidationError

	if strings.TrimSpace(s.Domain) == "" {
		errs = append(errs, ValidationError{Path: "domain", Message: "is required"})
	}
	if strings.TrimSpace(s.Entity) == "" {
		errs = append(errs, ValidationError{Path: "entity", Message: "is required"})
	}
	if len(s.Events) == 0 {
		errs = append(errs, ValidationError{Path: "events", Message: "must declare at least one event"})
	}
	if len(s.States) == 0 {
		errs = append(errs, ValidationError{Path: "states", Message: "must declare at least one state"})
	}

	eventSet := makeSet(s.Events)
	stateSet := makeSet(s.States)

	// Detect duplicates while we're at it.
	if dups := findDuplicates(s.Events); len(dups) > 0 {
		for _, d := range dups {
			errs = append(errs, ValidationError{Path: "events", Message: "duplicate event " + d})
		}
	}
	if dups := findDuplicates(s.States); len(dups) > 0 {
		for _, d := range dups {
			errs = append(errs, ValidationError{Path: "states", Message: "duplicate state " + d})
		}
	}

	for i, t := range s.Transitions {
		path := fmt.Sprintf("transitions[%d]", i)
		if strings.TrimSpace(t.On) == "" {
			errs = append(errs, ValidationError{Path: path + ".on", Message: "is required"})
		} else if _, ok := eventSet[t.On]; !ok {
			errs = append(errs, ValidationError{Path: path + ".on", Message: "unknown event " + t.On})
		}
		// from = "" means initial state, which is allowed.
		if t.From != "" {
			if _, ok := stateSet[t.From]; !ok {
				errs = append(errs, ValidationError{Path: path + ".from", Message: "unknown state " + t.From})
			}
		}
		if strings.TrimSpace(t.To) == "" {
			errs = append(errs, ValidationError{Path: path + ".to", Message: "is required"})
		} else if _, ok := stateSet[t.To]; !ok {
			errs = append(errs, ValidationError{Path: path + ".to", Message: "unknown state " + t.To})
		}
	}

	actionNames := map[string]struct{}{}
	for i, a := range s.Actions {
		path := fmt.Sprintf("actions[%d]", i)
		errs = append(errs, validateParams(path, a)...)
		if strings.TrimSpace(a.Name) == "" {
			errs = append(errs, ValidationError{Path: path + ".name", Message: "is required"})
		} else if _, dup := actionNames[a.Name]; dup {
			errs = append(errs, ValidationError{Path: path + ".name", Message: "duplicate action name " + a.Name})
		} else {
			actionNames[a.Name] = struct{}{}
		}

		for j, st := range a.AllowedStates {
			if _, ok := stateSet[st]; !ok {
				errs = append(errs, ValidationError{
					Path:    fmt.Sprintf("%s.allowed_states[%d]", path, j),
					Message: "unknown state " + st,
				})
			}
		}

		if a.Risk != "" && !validRisk(a.Risk) {
			errs = append(errs, ValidationError{
				Path:    path + ".risk",
				Message: "risk must be one of low|medium|high; got " + a.Risk,
			})
		}
		if a.Approval != "" && !validApproval(a.Approval) {
			errs = append(errs, ValidationError{
				Path:    path + ".approval",
				Message: "approval must be never|required; got " + a.Approval,
			})
		}
		// An unknown run-context disposition is refused rather than
		// ignored: silently dropping it would leave an action the
		// author believes is governed by run context with no rule at
		// all, which is the worst of the three possible states.
		if a.RunContext != nil {
			ui := strings.TrimSpace(a.RunContext.OnUntrustedInput)
			sr := strings.TrimSpace(a.RunContext.OnUntrustedSensitiveRead)
			// A run_context block that declares neither condition is
			// an action its author believes is governed and is not.
			// Refused at activation for the same reason an unknown
			// disposition is: the failure is silent otherwise.
			if ui == "" && sr == "" {
				errs = append(errs, ValidationError{
					Path: path + ".run_context",
					Message: "run_context must declare on_untrusted_input " +
						"and/or on_untrusted_sensitive_read",
				})
			}
			if ui != "" && !ValidRunContextDisposition(ui) {
				errs = append(errs, ValidationError{
					Path: path + ".run_context.on_untrusted_input",
					Message: "on_untrusted_input must be approval|block; got " +
						a.RunContext.OnUntrustedInput,
				})
			}
			if sr != "" && !ValidRunContextDisposition(sr) {
				errs = append(errs, ValidationError{
					Path: path + ".run_context.on_untrusted_sensitive_read",
					Message: "on_untrusted_sensitive_read must be approval|block; got " +
						a.RunContext.OnUntrustedSensitiveRead,
				})
			}
		}

		if strings.TrimSpace(a.Executor) == "" {
			errs = append(errs, ValidationError{Path: path + ".executor", Message: "executor key is required"})
		} else if registry != nil && !registry.Has(a.Executor) {
			errs = append(errs, ValidationError{
				Path:    path + ".executor",
				Message: "unknown executor key " + a.Executor + " (not in cloud registry)",
			})
		}
	}

	return errs
}

// build translates the validated spec into a framework
// domain.Definition by calling the framework's Builder.
func build(s spec, registry *executors.Registry) (kiffdomain.Definition, error) {
	b := kiffdomain.New(s.Domain).Entity(s.Entity)
	for _, ev := range s.Events {
		b = b.Event(ev)
	}
	for _, t := range s.Transitions {
		b = b.Transition(t.On, t.From, t.To)
	}
	for _, a := range s.Actions {
		fn, _ := registry.Get(a.Executor) // already validated to exist
		contract := kiffaction.ActionContract{
			Name:                a.Name,
			AllowedStates:       append([]string(nil), a.AllowedStates...),
			RequiredParameters:  append([]string(nil), a.RequiredParameters...),
			Parameters:          toKiffParams(a.Parameters),
			RequiredPermissions: toPermissions(proposerPermissions(a.RequiredPermissions, defaultApproval(a.Approval))),
			Risk:                kiffaction.RiskLevel(defaultRisk(a.Risk)),
			ApprovalRequirement: kiffaction.ApprovalRequirement(defaultApproval(a.Approval)),
			Executor:            fn,
		}
		b = b.Action(contract)
		for _, st := range a.AllowedStates {
			b = b.Allow(st, a.Name)
		}
	}
	return b.Build()
}

// ReviewerPermissionSuffix marks the permission that reviews an action's
// approval: `<workflow>.<action>.approve`, declared in
// the action's required_permissions.
const ReviewerPermissionSuffix = ".approve"

// ReviewerPermission returns the permission a reviewer must hold to
// grant or deny this action's approval, or "" when the action declares
// none. The approval-review gate refuses review when it is "".
func (a ActionSpec) ReviewerPermission() string {
	for _, p := range a.RequiredPermissions {
		if strings.HasSuffix(p, ReviewerPermissionSuffix) {
			return p
		}
	}
	return ""
}

// proposerPermissions is what an agent must hold to propose the action.
//
// For an action whose approval is required, the `.approve` permission is
// the reviewer's, not the proposer's: requiring the proposer to hold it
// too would mean any key able to propose could also approve, which is
// the self-approval the review gate exists to prevent. It stays
// in the spec, where the gate reads it.
//
// For any other action it stays a proposer requirement, exactly as
// before, so no action that runs without approval becomes reachable by
// a caller that could not reach it yet.
func proposerPermissions(perms []string, approval string) []string {
	if approval != "required" {
		return append([]string(nil), perms...)
	}
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		if !strings.HasSuffix(p, ReviewerPermissionSuffix) {
			out = append(out, p)
		}
	}
	return out
}

// toPermissions converts string slice into typed permission slice.
func toPermissions(in []string) []kiffpermission.Permission {
	out := make([]kiffpermission.Permission, len(in))
	for i, s := range in {
		out[i] = kiffpermission.Permission(s)
	}
	return out
}

func validRisk(r string) bool {
	switch r {
	case "low", "medium", "high":
		return true
	}
	return false
}

// toPublicRunContext projects the YAML run-context block, or nil when
// the action declares none.
func toPublicRunContext(rc *runContextSpec) *RunContextSpec {
	if rc == nil {
		return nil
	}
	return &RunContextSpec{
		OnUntrustedInput:         rc.OnUntrustedInput,
		OnUntrustedSensitiveRead: rc.OnUntrustedSensitiveRead,
	}
}

func validApproval(a string) bool {
	switch a {
	case "never", "required":
		return true
	}
	return false
}

// defaultRisk returns the YAML risk if set, otherwise "low" as a
// safe default. This mirrors the framework's behavior when an
// adopter omits Risk on an ActionContract.
func defaultRisk(r string) string {
	if r == "" {
		return "low"
	}
	return r
}

// defaultApproval returns the YAML approval if set, otherwise
// "never". Tenants who want approval gates set it explicitly.
func defaultApproval(a string) string {
	if a == "" {
		return "never"
	}
	return a
}

// makeSet converts a slice into a presence-only map for O(1) lookup.
func makeSet(xs []string) map[string]struct{} {
	out := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		out[x] = struct{}{}
	}
	return out
}

// findDuplicates returns each value that appears more than once.
func findDuplicates(xs []string) []string {
	seen := map[string]int{}
	var dups []string
	for _, x := range xs {
		seen[x]++
	}
	for x, n := range seen {
		if n > 1 {
			dups = append(dups, x)
		}
	}
	return dups
}

// toPublicParams projects the YAML parameter block onto the public Spec.
func toPublicParams(in []parameterSpec) []ParameterSpec {
	if len(in) == 0 {
		return nil
	}
	out := make([]ParameterSpec, len(in))
	for i, p := range in {
		out[i] = ParameterSpec{
			Name:          p.Name,
			Type:          p.Type,
			Required:      p.Required == nil || *p.Required,
			Min:           p.Min,
			Max:           p.Max,
			MinLen:        p.MinLen,
			MaxLen:        p.MaxLen,
			Pattern:       p.Pattern,
			AllowedValues: append([]string(nil), p.AllowedValues...),
			Unit:          strings.TrimSpace(p.Unit),
		}
		if p.Scale != nil {
			out[i].Scale = *p.Scale
		}
	}
	return out
}

// toKiffParams maps the YAML parameter block onto the framework's
// action.ParameterSpec. The framework already implements the checking; the
// cloud's grammar simply had no way to reach it until now.
//
// `required` defaults to true when omitted: a parameter you bothered to
// describe is one you expect, and the safer default for a governance
// contract is the stricter one.
func toKiffParams(in []parameterSpec) []kiffaction.ParameterSpec {
	if len(in) == 0 {
		return nil
	}
	out := make([]kiffaction.ParameterSpec, len(in))
	for i, p := range in {
		out[i] = kiffaction.ParameterSpec{
			Name:          p.Name,
			Type:          kiffaction.ParamType(p.Type),
			Required:      p.Required == nil || *p.Required,
			Min:           p.Min,
			Max:           p.Max,
			MinLen:        p.MinLen,
			MaxLen:        p.MaxLen,
			Pattern:       p.Pattern,
			AllowedValues: append([]string(nil), p.AllowedValues...),
		}
	}
	return out
}

// validParamTypes mirrors the framework's ParamType constants. Kept as a
// literal set so an unknown type fails at author time with a usable message
// rather than silently disabling every check on that parameter.
var validParamTypes = map[string]bool{"string": true, "int": true, "bool": true, "enum": true}

// validateParams checks a typed parameter block. Every failure here is a
// domain the author cannot activate — the alternative is a contract that
// looks stricter than it is, which is worse than no contract.
func validateParams(path string, a actionSpec) []ValidationError {
	var errs []ValidationError
	seen := make(map[string]struct{}, len(a.Parameters))
	for j, prm := range a.Parameters {
		pp := fmt.Sprintf("%s.parameters[%d]", path, j)
		name := strings.TrimSpace(prm.Name)
		if name == "" {
			errs = append(errs, ValidationError{Path: pp + ".name", Message: "is required"})
			continue
		}
		if _, dup := seen[name]; dup {
			errs = append(errs, ValidationError{Path: pp + ".name", Message: "duplicate parameter " + name})
		}
		seen[name] = struct{}{}

		typ := strings.TrimSpace(prm.Type)
		if typ == "" {
			errs = append(errs, ValidationError{Path: pp + ".type", Message: "is required (string, int, bool, enum)"})
			continue
		}
		if !validParamTypes[typ] {
			errs = append(errs, ValidationError{Path: pp + ".type", Message: "unknown type " + typ + " (want string, int, bool, enum)"})
			continue
		}

		switch typ {
		case "int":
			if prm.Min != nil && prm.Max != nil && *prm.Min > *prm.Max {
				errs = append(errs, ValidationError{Path: pp, Message: "min is greater than max"})
			}
			if len(prm.AllowedValues) > 0 {
				errs = append(errs, ValidationError{Path: pp + ".allowed_values", Message: "not valid for an int; use min/max"})
			}
		case "enum":
			if len(prm.AllowedValues) == 0 {
				errs = append(errs, ValidationError{Path: pp + ".allowed_values", Message: "an enum must list its allowed values"})
			}
		case "string":
			if prm.MinLen < 0 || prm.MaxLen < 0 {
				errs = append(errs, ValidationError{Path: pp, Message: "length bounds must not be negative"})
			}
			if prm.MaxLen > 0 && prm.MinLen > prm.MaxLen {
				errs = append(errs, ValidationError{Path: pp, Message: "min_length is greater than max_length"})
			}
			if prm.Pattern != "" {
				if _, err := regexp.Compile(prm.Pattern); err != nil {
					errs = append(errs, ValidationError{Path: pp + ".pattern", Message: "is not a valid regular expression: " + err.Error()})
				}
			}
		case "bool":
			if prm.Min != nil || prm.Max != nil || len(prm.AllowedValues) > 0 || prm.MinLen != 0 || prm.MaxLen != 0 || prm.Pattern != "" {
				errs = append(errs, ValidationError{Path: pp, Message: "a bool takes no constraints"})
			}
		}

		if typ != "int" && (prm.Min != nil || prm.Max != nil) {
			errs = append(errs, ValidationError{Path: pp, Message: "min/max apply to int only"})
		}
		errs = append(errs, validateUnit(pp, typ, prm)...)
	}
	return errs
}

// paramUnit is a unit name: a currency code (EUR, USD) or a short word
// (points, credits).
var paramUnit = regexp.MustCompile(`^[A-Za-z]{1,16}$`)

// validateUnit checks the display unit of an int amount: a unit name, and
// a scale (decimal places) of 0 to 6 that needs a unit to mean anything.
func validateUnit(pp, typ string, prm parameterSpec) []ValidationError {
	unit := strings.TrimSpace(prm.Unit)
	if unit == "" && prm.Scale == nil {
		return nil
	}
	if typ != "int" {
		return []ValidationError{{Path: pp, Message: "unit/scale apply to int only"}}
	}
	var errs []ValidationError
	if unit == "" {
		errs = append(errs, ValidationError{Path: pp + ".unit", Message: "is required with scale"})
	} else if !paramUnit.MatchString(unit) {
		errs = append(errs, ValidationError{Path: pp + ".unit", Message: "must be letters only, at most 16 (a currency code such as EUR, or a word such as points)"})
	}
	if prm.Scale != nil && (*prm.Scale < 0 || *prm.Scale > 6) {
		errs = append(errs, ValidationError{Path: pp + ".scale", Message: "must be 0 to 6 (the number of decimal places; 2 for cents)"})
	}
	return errs
}
