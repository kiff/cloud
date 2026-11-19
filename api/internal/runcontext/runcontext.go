// Package runcontext carries the facts about an agent run that the
// runtime learns from the integrator's harness rather than from the
// model.
//
// Why this exists: every field on a proposal that describes *why* an
// action is being proposed — `ReasoningSummary`, `Confidence` — is
// written by the model. The cloud has a trustworthy answer to "who is
// asking" (resolved from the authenticated identity) and
// "what for", and no answer at all to "what happened earlier in this
// run that should change the answer". Run context is that missing
// input.
//
// Where it travels: as a sibling of the proposal in the decide request
// body, never as a field inside the proposal. `ActionProposal.Parameters`
// is a `map[string]any` and would carry it with no schema change at all,
// which is exactly the trap — parameters are the model-supplied payload,
// and a taint flag an injected instruction can clear is worse than no
// flag, because it reads as a control while providing none.
//
// The honest limit (see the threat model): this is
// harness-*asserted*, not proven. A compromised or careless integrator
// can assert a clean run for a run that consumed untrusted content,
// exactly as an integrator today can decline to call the gate at all.
// It defends against the model being manipulated, not against the
// integrator being compromised, and no surface may claim otherwise.
package runcontext

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

// MaxRunIDLen bounds the run id because it lands verbatim in audit
// rows that are evidence. An unbounded caller-supplied string in the
// decision record is a storage and rendering problem, not a governance
// one, but the cheapest place to refuse it is at the door.
const MaxRunIDLen = 128

// runIDRe keeps the run id to characters that survive a URL, a log
// line, and a JSON audit payload without escaping. Deliberately
// conservative: a caller whose identifier does not fit gets a clear
// `invalid` rather than a value we store and later cannot render.
var runIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// ErrRunIDTooLong and ErrRunIDCharset are returned by Validate.
var (
	ErrRunIDTooLong = errors.New("runcontext: run_id exceeds the maximum length")
	ErrRunIDCharset = errors.New("runcontext: run_id contains characters outside [A-Za-z0-9._:-]")
)

// RunContext is the closed set of runtime-asserted facts about one
// agent run. Closed, not free-form: a `map[string]any` here would
// become a second policy language by accretion and would move the
// definition of "what counts" from the runtime to whatever the
// integrator felt like sending.
//
// Scope is deliberately absent. An earlier draft of this design carried
// declared run scope alongside taint; it was moved to the mandate
// model because scope can be issued in advance and looked up
// server-side, where the caller cannot widen it, while taint must be
// asserted because only the component that fetched the untrusted
// content knows it arrived. The rule that split them: assert what only
// the harness can know; look up what an operator can declare in
// advance.
type RunContext struct {
	// RunID identifies the agent run this proposal belongs to, so
	// decisions within one run can be correlated without inferring
	// the grouping from timing. Optional: a harness that tracks no
	// run identity can still assert UntrustedInput.
	RunID string `json:"run_id,omitempty"`

	// UntrustedInput reports that the harness has, at some point in
	// this run, delivered content from a source it classifies as
	// untrusted (a fetched page, a third-party issue, a document, a
	// tool result from outside the run's remit) into the model's
	// context.
	//
	// What classifies a source as untrusted is the integrator's call
	// in v0, which is honest and also the weakest link: an integrator
	// who forgets to classify a source gets a clean run that is not
	// one. An open question in the design.
	UntrustedInput bool `json:"untrusted_input"`

	// SensitiveRead reports that the harness has, at some point in
	// this run, served the model content it classifies as sensitive —
	// a private repository, a secret store, a record outside the
	// run's remit.
	//
	// # Why a pointer
	//
	// This is the second fact, and it arrived after guards were
	// already asserting the first. A plain bool would default to
	// false for every guard that predates it, and an action gating on
	// "untrusted AND sensitive" would then read a stale guard's
	// silence as "no sensitive read happened" and allow. That is
	// fail-open, reached by the same route the revoked-mandate defect
	// took: absence rendering as the safe value.
	//
	// nil therefore means "not asserted" and is distinct from a
	// pointer to false, which means "asserted, and it did not
	// happen". Rules that depend on this fact refuse when it is nil;
	// see Conjunction.
	SensitiveRead *bool `json:"sensitive_read,omitempty"`
}

// SensitiveReadAsserted reports whether the harness said anything at
// all about sensitive reads, and what it said. The two return values
// are the tri-state this fact needs, kept in one place so no caller
// has to remember that a nil pointer is not a false.
func (rc RunContext) SensitiveReadAsserted() (value, asserted bool) {
	if rc.SensitiveRead == nil {
		return false, false
	}
	return *rc.SensitiveRead, true
}

// Validate reports whether the asserted context is storable. It does
// not judge the *truth* of the assertion — nothing here can.
func (rc RunContext) Validate() error {
	if rc.RunID == "" {
		return nil // optional; UntrustedInput alone is a valid assertion
	}
	if len(rc.RunID) > MaxRunIDLen {
		return fmt.Errorf("%w: %d > %d", ErrRunIDTooLong, len(rc.RunID), MaxRunIDLen)
	}
	if !runIDRe.MatchString(rc.RunID) {
		return ErrRunIDCharset
	}
	return nil
}

// AuditFields renders the context for the decision record. The key
// names live here so the write path and every future reader agree on
// them.
//
// UntrustedInput is always emitted, including when false, and callers
// must only reach this method for a context that was actually
// asserted. That is the load-bearing detail of this package: a
// decision record has to distinguish three states, not two —
//
//	absent            no assertion was made (no keys stamped)
//	asserted, clean   untrusted_input=false
//	asserted, tainted untrusted_input=true
//
// Collapsing the first two would manufacture a clean-run claim out of
// silence, which is the revoked-mandate defect shape (a revoked mandate
// produced an empty set, an empty set read as "nothing to check", and
// "nothing to check" meant allowed). Absence is not evidence of
// safety, and the enforcement slice that comes next depends on being
// able to tell these apart.
func (rc RunContext) AuditFields() map[string]any {
	fields := map[string]any{"untrusted_input": rc.UntrustedInput}
	if rc.RunID != "" {
		fields["run_id"] = rc.RunID
	}
	// Stamped only when the harness actually asserted it, so the
	// record keeps the same three-state distinction the field itself
	// carries. A `sensitive_read: false` written for a guard that
	// never mentioned it would be the audit trail inventing a fact.
	if v, asserted := rc.SensitiveReadAsserted(); asserted {
		fields["sensitive_read"] = v
	}
	return fields
}

// ctxKey is unexported so nothing outside this package can put a
// RunContext on a context by another route.
type ctxKey struct{}

// NewContext returns ctx carrying rc. Request-scoped: the decide
// handler attaches it after validating the request body, and the
// recording path reads it back rather than threading it through the
// evaluation call chain — the same idiom the control environment and
// resolved domain already use.
func NewContext(ctx context.Context, rc RunContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, rc)
}

// FromContext returns the asserted run context and whether one was
// asserted at all. The boolean is not a convenience: see AuditFields
// for why "absent" and "asserted clean" must stay distinguishable.
func FromContext(ctx context.Context) (RunContext, bool) {
	rc, ok := ctx.Value(ctxKey{}).(RunContext)
	return rc, ok
}
