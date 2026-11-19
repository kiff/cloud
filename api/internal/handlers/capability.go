package handlers

import (
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
)

// can is the management check every handler shares. A person
// on an account answers by their membership's role; a key, or a person
// in a deploy without memberships, keeps the rule from before
// memberships: the account roles, on a credential not bound to an agent.
func can(id auth.Identity, c membership.Capability) bool {
	if id.MemberRole != "" {
		return membership.Can(id.MemberRole, c)
	}
	return id.ManagesAccount()
}
