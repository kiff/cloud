// tenant_runtime_approvals.go — the cloud's permission gate for the
// approval-review surface (POST /v1/approvals/{id}/grant and
// /v1/approvals/{id}/deny). The framework's runtime.ReviewApproval
// takes `reviewedBy` as a string and trusts the caller; the cloud
// owns tenant-scoped role bindings, so the permission check belongs
// here, before delegation to kiffhttpapi.Handler.
//
// This is the runtime-enforcement half of the Studio-cloud seam design ("studio v0
// corrections"). The YAML-shape half ships in, where the
// builder emits the canonical paired permission shape:
// `<workflow>.<action>` (proposing) and `<workflow>.<action>.approve`
// (granting) — both bound to the tenant_owner role for solo signups.
//
// Behavior:
//
//   - On a builder-built or refund-shaped domain with an explicit
//     `.approve` permission on the action's contract, the gate
//     verifies the call principal holds it. If not, 403 with a
//     permission_denied body.
//   - An action that declares no `.approve` permission cannot be
//     reviewed by any API key. It used to be a no-op for such
//     legacy domains, which let any key able to request an approval
//     also grant it. Only the signed-in tenant owner may review it
//     until the domain declares a reviewer permission.
//
// For solo-signup tenants, tenant_owner holds both
// permissions, so the gate passes silently. When M2's multi-user
// role split lands, the same gate enforces proposer/approver
// separation without further changes.
package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	kiffactor "github.com/kiff/kiff/pkg/kiff/actor"
	kiffpermission "github.com/kiff/kiff/pkg/kiff/permission"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/membership"
)

// errApprovalNotFound and errApprovalReviewDenied are the two
// non-success outcomes the gate produces. Other store-level errors
// surface as 502 (the credential is valid; the data plane failed),
// matching the existing handler's posture.
var (
	errApprovalNotFound      = errors.New("handlers: approval not found")
	errApprovalReviewDenied  = errors.New("handlers: caller lacks approve permission")
	errApprovalActionMissing = errors.New("handlers: action contract missing for approval")
	// errApprovalReviewerUndeclared: the action requires approval but
	// its domain names no reviewer permission, so nobody's authority
	// to review it can be established.
	errApprovalReviewerUndeclared = errors.New("handlers: action declares no reviewer permission")
	// errApprovalReviewByAgent: the caller is a key bound to an agent.
	errApprovalReviewByAgent = errors.New("handlers: a key bound to an agent cannot review approvals")
	// errApprovalSelfReview: the caller's key asked for this approval.
	errApprovalSelfReview = errors.New("handlers: a key cannot review an approval it asked for")
	// errApprovalReviewUnavailable: the domain spec or permission
	// policy could not be read, so the reviewer cannot be checked.
	errApprovalReviewUnavailable = errors.New("handlers: approval review cannot be authorized")
)

// requesterKeyReader is the cloud approval store's record of which API
// key asked for an approval (approvals.DomainScopedStore).
type requesterKeyReader interface {
	RequesterKey(ctx context.Context, approvalID string) (string, error)
}

// reviewDeniedError carries the missing permission name out of the
// gate so the HTTP response can name it. The gate returns this as
// errors.As-friendly so the dispatcher in ServeHTTP can branch on
// type rather than string.
type reviewDeniedError struct {
	Permission kiffpermission.Permission
}

func (e *reviewDeniedError) Error() string {
	return "handlers: caller lacks approve permission " + string(e.Permission)
}

func (e *reviewDeniedError) Unwrap() error { return errApprovalReviewDenied }

// isApprovalReviewPath reports whether the (already-prefix-stripped)
// request targets the framework's approval-review surface
// (grant or deny). The framework defines two paths:
//
//	POST /approvals/{id}/grant
//	POST /approvals/{id}/deny
func isApprovalReviewPath(r *http.Request) bool {
	return r.Method == http.MethodPost && isApprovalReviewSuffix(r.URL.Path)
}

// isApprovalReviewSuffix is the path half of the check, split out so a
// caller that has the framework-side path but not a rewritten request
// can reuse it.
func isApprovalReviewSuffix(p string) bool {
	if !strings.HasPrefix(p, "/approvals/") {
		return false
	}
	return strings.HasSuffix(p, "/grant") || strings.HasSuffix(p, "/deny")
}

// isApprovalReadPath reports whether a framework request reads approvals for
// an entity. Approval records do not yet carry domain_id, so a domain-bound
// credential cannot be safely served this route until the store is dimensioned.
func isApprovalReadPath(r *http.Request) bool {
	return r.Method == http.MethodGet &&
		strings.HasPrefix(r.URL.Path, "/entities/") &&
		strings.HasSuffix(r.URL.Path, "/approvals")
}

// approvalIDFromReviewPath extracts the approval id from a path
// like "/approvals/abc-123/grant". Returns the empty string when
// the shape doesn't match (caller already filtered with
// isApprovalReviewPath, but defensively handle malformed input).
func approvalIDFromReviewPath(path string) string {
	trimmed := strings.TrimPrefix(path, "/approvals/")
	for _, suffix := range []string{"/grant", "/deny"} {
		if strings.HasSuffix(trimmed, suffix) {
			return strings.TrimSuffix(trimmed, suffix)
		}
	}
	return ""
}

// enforceApprovalReviewPermission runs the cloud-side gate on a
// review request. Returns nil when the call principal is allowed
// to proceed; returns a typed error for the dispatcher to render.
//
// The implementation:
//
//  1. Pulls the approval id from the path.
//  2. Looks up the approval record to learn the action name.
//  3. Finds the action in the domain's cloud-side spec (the same parse
//     that built the runtime) and reads its reviewer permission, the
//     one ending in `.approve`.
//  4. If the action declares none, refuses every API key: treating
//     that as "anyone" let a key able to request an approval grant it
//     itself. Only the signed-in tenant owner may review.
//  5. Asks the runtime's permission policy whether an actor with
//     id.Roles holds that permission. If yes, return nil; if no,
//     return *reviewDeniedError naming the missing permission.
//
// Every "cannot tell" refuses — no spec, no action, no policy — for
// the same reason as step 4.
func enforceApprovalReviewPermission(
	ctx context.Context,
	rt *kiffruntime.Runtime,
	spec domain.Spec,
	specErr error,
	id auth.Identity,
	path string,
) error {
	if rt == nil {
		return errApprovalNotFound // defence in depth; caller resolved it
	}

	if id.AgentID != "" {
		// A key bound to an agent acts as that agent, and an agent
		// never answers an approval: it would be granting its own
		// request, or another agent's, with a key the account gave it
		// to act, not to review. Its roles do not change that.
		return errApprovalReviewByAgent
	}

	approvalID := approvalIDFromReviewPath(path)
	if approvalID == "" {
		// Path didn't match {id}/{grant|deny}. Let the framework
		// return its own 404; the gate has nothing to enforce.
		return nil
	}

	if rt.Approvals == nil {
		// No approval store wired. The framework will return its
		// own error when delegated. Don't shadow that here.
		return nil
	}

	record, ok, err := rt.Approvals.Get(ctx, approvalID)
	if err != nil {
		// Store error: surface as not-found-shaped so the
		// dispatcher renders 404, matching the framework's own
		// behavior on a missing record. The full error lands in
		// logs at the caller.
		return errApprovalNotFound
	}
	if !ok {
		return errApprovalNotFound
	}

	// A key never answers an approval it asked for. The approval
	// names the actor the caller sent, so the key is matched on what
	// the store recorded when the approval was saved, and on
	// requested_by for an approval asked for on the framework's own
	// route, which records the key itself.
	if keyID := id.APIKeyID(); keyID != "" {
		if record.RequestedBy == keyID {
			return errApprovalSelfReview
		}
		if rk, ok := rt.Approvals.(requesterKeyReader); ok {
			requester, err := rk.RequesterKey(ctx, approvalID)
			if err != nil {
				return errApprovalReviewUnavailable
			}
			if requester == keyID {
				return errApprovalSelfReview
			}
		}
	}

	if specErr != nil {
		return errApprovalReviewUnavailable
	}
	var action *domain.ActionSpec
	for i := range spec.Actions {
		if spec.Actions[i].Name == record.ActionName {
			action = &spec.Actions[i]
			break
		}
	}
	if action == nil {
		return errApprovalActionMissing
	}

	approvePerm := kiffpermission.Permission(action.ReviewerPermission())
	if approvePerm == "" {
		// Nothing names a reviewer. A credential an agent holds — any
		// API key — cannot review: that is the self-approval this gate
		// closes. The signed-in tenant owner still can: domains
		// built in Studio declare no reviewer yet, the owner can edit
		// the domain anyway, and refusing them would leave approvals
		// nobody can resolve. The owner is identified by the
		// server-assigned tenant_owner role on a human credential,
		// never by a key's roles.
		// With memberships the owner is a person whose role
		// answers holds; separation of duties is the hold gate's to apply.
		if !id.APIKey && id.MemberRole != "" {
			if membership.Can(id.MemberRole, membership.CapHoldsAnswer) {
				return nil
			}
			return errApprovalReviewerUndeclared
		}
		if !id.APIKey && id.HasRole("tenant_owner") {
			return nil
		}
		return errApprovalReviewerUndeclared
	}

	if rt.Permissions == nil {
		return errApprovalReviewUnavailable
	}

	caller := kiffactor.Actor{
		ID:    id.ActorID(),
		Roles: append([]string(nil), id.Roles...),
	}
	// The cloud's permission policy reads roles from the identity on the
	// context (domain.ContextPolicy), so the approver alias is applied
	// there, for this one check.
	reviewer := id
	reviewer.Roles = reviewerRoles(id)
	if rt.Permissions.Can(auth.WithIdentity(ctx, reviewer), caller, approvePerm) {
		return nil
	}
	return &reviewDeniedError{Permission: approvePerm}
}

// reviewerRoles are the roles a reviewer permission is checked against.
// An approver also counts as tenant_owner for this one check:
// published domains grant their .approve permissions to tenant_owner,
// and the governance-roles design decided approvers answer wherever tenant_owner does,
// without re-publishing any domain. It widens nothing else: the roles
// are used only to evaluate the action's reviewer permission.
func reviewerRoles(id auth.Identity) []string {
	roles := append([]string(nil), id.Roles...)
	if !id.APIKey && id.MemberRole == membership.RoleApprover {
		roles = append(roles, "tenant_owner")
	}
	return roles
}

// writeApprovalReviewError renders the gate's typed errors as JSON
// HTTP responses. Other errors (transport, framework store
// failures) flow through the existing handler paths.
func writeApprovalReviewError(w http.ResponseWriter, err error) {
	var denied *reviewDeniedError
	switch {
	case errors.As(err, &denied):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		// Hand-roll the JSON to keep the wire shape stable
		// regardless of struct-tag drift on internal types.
		_, _ = w.Write([]byte(`{"error":"permission_denied","permission":"` + jsonEscape(string(denied.Permission)) + `"}` + "\n"))
	case errors.Is(err, errApprovalReviewByAgent):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"review_by_agent","message":"a key bound to an agent cannot grant or deny approvals; a person on the account answers them"}` + "\n"))
	case errors.Is(err, errApprovalSelfReview):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"self_review","message":"this key asked for the approval, so it cannot grant or deny it; another reviewer, or a person on the account, answers it"}` + "\n"))
	case errors.Is(err, errApprovalNotFound):
		writeJSONStatus(w, http.StatusNotFound, "approval not found")
	case errors.Is(err, errApprovalActionMissing):
		// The approval refers to an action the catalog doesn't
		// know about. Treat as a 404 so the framework's own
		// behavior (which would also fail to validate) lines up.
		writeJSONStatus(w, http.StatusNotFound, "approval action contract not found")
	case errors.Is(err, errApprovalReviewerUndeclared):
		// Nobody can be shown to hold review authority, so nobody
		// reviews through this surface until the domain declares it.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"reviewer_permission_undeclared","message":"this action requires approval but its domain declares no reviewer permission (a <workflow>.<action>.approve permission on the action); declare one and grant it to the reviewing role"}` + "\n"))
	case errors.Is(err, errApprovalReviewUnavailable):
		writeJSONStatus(w, http.StatusServiceUnavailable, "approval review cannot be authorized right now")
	default:
		writeJSONStatus(w, http.StatusInternalServerError, "approval review check failed")
	}
}

// jsonEscape is a minimal escape for the permission string in the
// JSON body. Permissions are dotted.lowercase per the framework's
// conventions, so the escape exists only as defence in depth — a
// custom domain might use unexpected characters and we don't want
// to emit malformed JSON.
func jsonEscape(s string) string {
	if !strings.ContainsAny(s, "\"\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
