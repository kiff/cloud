// tenant_runtime_execute.go contains the cloud's trust-boundary gate for
// framework requests whose JSON body carries an actor. The public framework
// accepts actor identity and roles from that body; Cloud must instead derive
// both from its authenticated identity before permission checks or audit writes.
package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var (
	errActorBodyTooLarge = errors.New("actor request body is too large")
	errActorBodyRead     = errors.New("actor request body could not be read")
)

// isExecutePath reports whether the (already-prefix-stripped) request
// targets the framework's action-execute surface. The framework routes
// POST /entities/{id}/actions/{name}/execute (and tolerates the
// /entities/{id}/execute alias), matched the same way isExecuteOrIngest
// matches for the post-execute hook.
func isExecutePath(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	p := r.URL.Path
	return strings.HasPrefix(p, "/entities/") && strings.HasSuffix(p, "/execute")
}

// actorBodyShape describes how a proxied request body carries the actor that
// Cloud must replace with the authenticated principal.
//
// The framework uses two shapes on the routes Cloud proxies, and they are not
// interchangeable: rewriting the wrong one silently leaves the caller's value in
// place, because the framework ignores the key it does not read.
type actorBodyShape int

const (
	// actorShapeNone means the route carries no caller-supplied actor.
	actorShapeNone actorBodyShape = iota
	// actorShapeObject is {"actor": {"id": ..., "roles": [...]}} — the action
	// and approval-review bodies.
	actorShapeObject
	// actorShapeID is {"actor_id": "..."} — adapter.RawInput on /events/raw.
	actorShapeID
)

// authenticatedActorShape reports how a framework route carries its actor, and
// therefore how Cloud must rewrite it before delegation.
//
// This is a default-deny list by intent: every proxied POST that carries an
// identity-shaped field must appear here. A route that reaches the framework
// without a shape keeps whatever actor the caller asserted, which is an audit
// attribution forgery even when the route performs no authorization of its own.
func authenticatedActorShape(r *http.Request) actorBodyShape {
	if r.Method != http.MethodPost {
		return actorShapeNone
	}
	if isApprovalReviewPath(r) {
		return actorShapeObject
	}
	p := r.URL.Path
	// Event ingestion carries actor_id, not an actor object. Events drive state
	// transitions and land in the audit trail, so an unrewritten actor_id lets
	// an authenticated tenant attribute state changes to any identity.
	if p == "/events/raw" {
		return actorShapeID
	}
	if !strings.HasPrefix(p, "/entities/") {
		return actorShapeNone
	}
	if strings.HasSuffix(p, "/execute") ||
		strings.HasSuffix(p, "/validate") ||
		strings.HasSuffix(p, "/approvals") {
		return actorShapeObject
	}
	return actorShapeNone
}

// isAuthenticatedActorPath reports whether a framework route consumes an
// actor supplied in its JSON body. These are the routes where Cloud must replace
// caller assertions with the authenticated principal before delegation.
func isAuthenticatedActorPath(r *http.Request) bool {
	return authenticatedActorShape(r) != actorShapeNone
}

// maxActorBodyBytes caps the body buffered at the trust boundary. Oversized
// bodies are rejected rather than delegated unchanged: passing one through
// would restore the caller-controlled actor the gate exists to remove.
const maxActorBodyBytes = 1 << 20 // 1 MiB

// rewriteAuthenticatedActor replaces the caller's actor with the server-resolved
// subject, in whichever shape the target route reads. It deliberately discards
// caller-supplied type and display metadata as well: audit attribution must not
// contain an identity-shaped value supplied by the caller.
//
// Malformed JSON is delegated unchanged so the framework returns its canonical
// parse error. Read failures and oversized bodies fail closed.
func rewriteAuthenticatedActor(r *http.Request, subject string, roles []string, shape actorBodyShape) (*http.Request, error) {
	if shape == actorShapeNone {
		return r, nil
	}
	if r.Body == nil {
		return r, nil
	}

	limited := io.LimitReader(r.Body, maxActorBodyBytes+1)
	raw, err := io.ReadAll(limited)
	_ = r.Body.Close()
	if err != nil {
		return r, fmt.Errorf("%w: %v", errActorBodyRead, err)
	}
	if len(raw) > maxActorBodyBytes {
		return r, errActorBodyTooLarge
	}
	if len(raw) == 0 {
		return r, nil
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		return r, nil
	}
	if doc == nil {
		// JSON null decodes successfully into a nil map. Preserve it so the
		// framework returns its normal missing-actor validation error.
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		return r, nil
	}

	switch shape {
	case actorShapeID:
		newActorID, err := json.Marshal(subject)
		if err != nil {
			return r, fmt.Errorf("encode authenticated actor id: %w", err)
		}
		doc["actor_id"] = newActorID
	default:
		actorObj := struct {
			ID    string   `json:"id"`
			Roles []string `json:"roles,omitempty"`
		}{ID: subject, Roles: append([]string(nil), roles...)}
		newActor, err := json.Marshal(actorObj)
		if err != nil {
			return r, fmt.Errorf("encode authenticated actor: %w", err)
		}
		doc["actor"] = newActor
	}

	rewritten, err := json.Marshal(doc)
	if err != nil {
		return r, fmt.Errorf("encode authenticated request: %w", err)
	}

	r.Body = io.NopCloser(bytes.NewReader(rewritten))
	r.ContentLength = int64(len(rewritten))
	return r, nil
}
