package studio_view

import (
	"strconv"
	"strings"
)

// FieldError is one inline validation message rendered next to a
// builder block's field. The templ partials key on BlockID +
// Field to decide which row gets the red treatment.
type FieldError struct {
	BlockID string // stable per-render id of the block (entity, action[0], role[1], …)
	Field   string // name of the field within that block (e.g. "name", "allowed_states")
	Message string // human-readable error
}

// ParseValidationError walks the package's validation error and
// emits one or more FieldError records the templ surface can
// render inline. The package's Validate returns a flat
// fmt.Errorf-formatted string with deterministic prefixes; this
// function reverses those prefixes back into block + field
// addresses.
//
// The mapping is best-effort. Unknown error formats collapse to
// a single FieldError on the entity block with the raw message
// — at least the user sees the error, even if not on the right
// row.
//
// Patterns recognized (matching kiff-studio v0.1.0's
// Blueprint.Validate output):
//
//	"blueprint: domain ..."         → entity block, "domain" field
//	"blueprint: entity ..."         → entity block, "entity" field
//	"blueprint: at least two states ..." → state-machine, "states"
//	"blueprint: state X must be ..."     → state-machine, state X
//	"blueprint: event X must be ..."     → state-machine, event X
//	"blueprint: transition.on/.from/.to ..." → state-machine, transitions
//	"blueprint: action[N] X: ..."        → action card N, parsed sub-field
//	"blueprint: role X permission Y ..." → policy block, role X
//	"blueprint: at least one action ..." → entity block (drives "+ add action")
func ParseValidationError(err error) []FieldError {
	if err == nil {
		return nil
	}

	// errors.Join unwrapping support — walk the joined errors
	// and accumulate.
	type unwrapper interface{ Unwrap() []error }
	if u, ok := err.(unwrapper); ok {
		var out []FieldError
		for _, e := range u.Unwrap() {
			out = append(out, parseOne(e.Error())...)
		}
		return out
	}
	return parseOne(err.Error())
}

// parseOne maps one error string to FieldError(s).
// Most error strings correspond to exactly one FieldError;
// the function returns a slice for forward compatibility with
// errors that legitimately span multiple fields.
func parseOne(msg string) []FieldError {
	// All package errors from Blueprint.Validate are prefixed
	// with "blueprint: ". Strip it for matching.
	const prefix = "blueprint: "
	body := strings.TrimPrefix(msg, prefix)

	switch {
	case strings.HasPrefix(body, "domain "):
		return []FieldError{{BlockID: blockIDEntity, Field: "domain", Message: body}}

	case strings.HasPrefix(body, "entity "):
		return []FieldError{{BlockID: blockIDEntity, Field: "entity", Message: body}}

	case strings.Contains(body, "at least two states"):
		return []FieldError{{BlockID: blockIDStateMachine, Field: "states", Message: body}}

	case strings.Contains(body, "at least one action"):
		// Drives the "+ add action" affordance on the entity card.
		return []FieldError{{BlockID: blockIDEntity, Field: "actions", Message: body}}

	case strings.HasPrefix(body, "state "):
		// "state X must be UPPER_SNAKE_CASE" / "duplicate state X"
		return []FieldError{{BlockID: blockIDStateMachine, Field: extractName(body, "state "), Message: body}}

	case strings.HasPrefix(body, "event "):
		return []FieldError{{BlockID: blockIDStateMachine, Field: extractName(body, "event "), Message: body}}

	case strings.HasPrefix(body, "transition.on") ||
		strings.HasPrefix(body, "transition.from") ||
		strings.HasPrefix(body, "transition.to"):
		return []FieldError{{BlockID: blockIDStateMachine, Field: "transitions", Message: body}}

	case strings.HasPrefix(body, "action[") ||
		strings.HasPrefix(body, "action "):
		idx, sub, rest := splitActionPath(body)
		return []FieldError{{BlockID: blockIDAction(idx), Field: sub, Message: rest}}

	case strings.HasPrefix(body, "role "):
		// "role X permission Y must be dotted.lowercase" /
		// "role X must be snake_case"
		role, field, rest := splitRolePath(body)
		return []FieldError{{BlockID: blockIDRole(role), Field: field, Message: rest}}
	}

	// Unknown shape: surface as a generic entity-block error so
	// the user at least sees something.
	return []FieldError{{BlockID: blockIDEntity, Field: "_unknown", Message: msg}}
}

// extractName pulls the first whitespace-delimited token after
// the prefix. Used for "state X must be ..." → "X".
func extractName(s, prefix string) string {
	tail := strings.TrimPrefix(s, prefix)
	// The token may be quoted (kiff-studio formats names with %q in
	// some paths and bare in others). Strip surrounding quotes
	// defensively.
	tail = strings.TrimPrefix(tail, `"`)
	if i := strings.IndexAny(tail, ` "`); i > 0 {
		return tail[:i]
	}
	return tail
}

// splitActionPath parses an action[N] / action X message into:
//
//	(blockIDAction(idx), sub-field name, remaining message)
//
// Examples:
//
//	"action[0] \"ISSUE_REFUND\": required_permission ..."
//	  → (0, "required_permissions", "required_permission ...")
//
//	"action[1] \"FOO\": risk \"x\" must be one of ..."
//	  → (1, "risk", "risk \"x\" must be one of ...")
//
// Falls back to (0, "_unknown", body) when the pattern doesn't
// match.
func splitActionPath(body string) (idx int, sub, rest string) {
	rest = body

	// "action[N]" → idx
	if strings.HasPrefix(body, "action[") {
		closing := strings.Index(body, "]")
		if closing > len("action[") {
			if n, err := strconv.Atoi(body[len("action["):closing]); err == nil {
				idx = n
				rest = strings.TrimSpace(body[closing+1:])
			}
		}
	}

	// Drop the action name and the leading colon if present.
	if i := strings.Index(rest, ":"); i >= 0 {
		rest = strings.TrimSpace(rest[i+1:])
	}

	// First word of the remainder maps to a known sub-field.
	first := firstWord(rest)
	switch first {
	case "name":
		sub = "name"
	case "allowed_state", "allowed_states":
		sub = "allowed_states"
	case "required_parameter", "required_parameters":
		sub = "required_parameters"
	case "required_permission", "required_permissions":
		sub = "required_permissions"
	case "risk":
		sub = "risk"
	case "approval":
		sub = "approval"
	case "executor":
		sub = "executor"
	default:
		sub = "_unknown"
	}
	return idx, sub, rest
}

// splitRolePath parses a "role X permission Y ..." message into:
//
//	(role name, field-within-role, remaining message)
//
// Patterns:
//
//	"role X must be snake_case"               → ("X", "name", body)
//	"role X permission Y must be ..."         → ("X", "permissions", body)
//
// Falls back to ("", "_unknown", body) on shape mismatch.
func splitRolePath(body string) (role, field, rest string) {
	rest = body
	tail := strings.TrimPrefix(body, "role ")
	tail = strings.TrimPrefix(tail, `"`)
	closing := strings.IndexAny(tail, ` "`)
	if closing < 0 {
		return "", "_unknown", body
	}
	role = tail[:closing]
	tail = strings.TrimSpace(strings.TrimLeft(tail[closing:], `" `))

	switch firstWord(tail) {
	case "must":
		field = "name"
	case "permission":
		field = "permissions"
	default:
		field = "_unknown"
	}
	return role, field, body
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i > 0 {
		return s[:i]
	}
	return s
}

// ---------- block ID helpers ----------

const (
	// blockIDEntity is the stable id of the (only) entity card.
	// One per Blueprint by definition.
	blockIDEntity = "entity"

	// blockIDStateMachine is the stable id of the state-machine
	// card (states + events + transitions, all rendered as one
	// composite block per the mockup).
	blockIDStateMachine = "state-machine"

	// blockIDAudit and blockIDPolicy are the singletons.
	blockIDAudit  = "audit"
	blockIDPolicy = "policy"
)

// blockIDAction returns the per-action block id. Stable across
// renders because the template iterates in slice order; the
// index is the source of truth.
func blockIDAction(idx int) string {
	return "action-" + strconv.Itoa(idx)
}

// blockIDRole is keyed on the role name. Roles are unique by
// name in the Blueprint shape.
func blockIDRole(name string) string {
	return "role-" + name
}
