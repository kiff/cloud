package studio_view

import (
	"slices"
	"strings"

	studio "github.com/kiff/kiff-studio"
)

// ReviewerRole is the role Studio grants an approval's reviewer
// permission to: the tenant owner, who reviews approvals today.
const ReviewerRole = "tenant_owner"

const reviewerSuffix = ".approve"

// WithReviewerPermissions returns b with a reviewer permission on every
// approval-required action that lacks one.
//
// Since the approval-review gate refuses every API key on an
// approval-required action that declares no `<workflow>.<action>.approve`
// permission; only the signed-in owner could still review it. Studio
// emitted none, so every domain built there relied on that fallback.
//
// The permission is named after the action's proposing permission when
// it has one, else `<domain>.<action>` as Build's drafts name it. It is
// granted to ReviewerRole only, never to a role that proposes the
// action, so the proposer cannot approve its own request. The result is
// independent of b: it shares no slice or map with it, whether or not a
// permission was added.
func WithReviewerPermissions(b studio.Blueprint) studio.Blueprint {
	out := cloneBlueprint(b)
	for i := range out.Actions {
		a := &out.Actions[i]
		if !a.ApprovalRequired || a.Name == "" || slices.ContainsFunc(a.RequiredPermissions, isReviewer) {
			continue
		}
		r := proposingPermission(out.Domain, *a) + reviewerSuffix
		a.RequiredPermissions = append(a.RequiredPermissions, r)
		if out.Roles == nil {
			out.Roles = map[string][]string{}
		}
		if !slices.Contains(out.Roles[ReviewerRole], r) {
			out.Roles[ReviewerRole] = append(out.Roles[ReviewerRole], r)
		}
	}
	return out
}

// cloneBlueprint deep-copies every slice and map in b. Nil stays nil, so
// an unchanged blueprint renders exactly as the original would.
func cloneBlueprint(b studio.Blueprint) studio.Blueprint {
	out := b
	out.Events = slices.Clone(b.Events)
	out.States = slices.Clone(b.States)
	out.Transitions = slices.Clone(b.Transitions) // Transition holds only strings
	if b.Actions != nil {
		out.Actions = make([]studio.Action, len(b.Actions))
		for i, a := range b.Actions {
			a.AllowedStates = slices.Clone(a.AllowedStates)
			a.RequiredParameters = slices.Clone(a.RequiredParameters)
			a.RequiredPermissions = slices.Clone(a.RequiredPermissions)
			out.Actions[i] = a
		}
	}
	if b.Roles != nil {
		out.Roles = make(map[string][]string, len(b.Roles))
		for role, perms := range b.Roles {
			out.Roles[role] = slices.Clone(perms)
		}
	}
	return out
}

func isReviewer(p string) bool { return strings.HasSuffix(p, reviewerSuffix) }

func proposingPermission(domainName string, a studio.Action) string {
	for _, p := range a.RequiredPermissions {
		if !isReviewer(p) {
			return p
		}
	}
	name := strings.ToLower(a.Name)
	if d := strings.TrimSpace(domainName); d != "" {
		return d + "." + name
	}
	return name
}
