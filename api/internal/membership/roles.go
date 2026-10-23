// Package membership is who a person is on an account: a
// tenant has an explicit set of memberships, each a (tenant, subject)
// pair with one governance role, and every request that touches tenant
// data resolves the current membership server-side before anything
// else (M1, M3, M13).
//
// This file is the single policy table. Handlers never compare role
// strings; they ask whether a role holds a Capability.
package membership

// Role is a membership's governance role (M2).
type Role string

const (
	// RoleAdmin runs the account: people, keys, billing, policy,
	// deletion, and everything an editor can do.
	RoleAdmin Role = "admin"
	// RoleEditor shapes what agents may do: Cards, domains, tools.
	RoleEditor Role = "editor"
	// RoleApprover answers holds. It cannot change what a Card allows.
	RoleApprover Role = "approver"
	// RoleViewer sees everything and changes nothing.
	RoleViewer Role = "viewer"
)

// Roles lists every role, most authority first.
var Roles = []Role{RoleAdmin, RoleEditor, RoleApprover, RoleViewer}

// ParseRole returns the role named by s, or false.
func ParseRole(s string) (Role, bool) {
	for _, r := range Roles {
		if string(r) == s {
			return r, true
		}
	}
	return "", false
}

// Capability is one thing a request may need to be allowed to do.
type Capability string

const (
	// CapRead: see all account data, including proposal parameters.
	// Every role holds it; roles govern changes, not reading.
	CapRead Capability = "read"
	// CapPersonal: acts that touch only the caller's own things (their
	// wallet link, their CLI sessions, their support tickets, their
	// notification bell). Every role holds it.
	CapPersonal Capability = "personal"
	// CapCardsManage: issue, edit, share, move, revoke or pause a Card,
	// raise its limits, manage card holders, and answer an exception
	// with change-card (a Card edit reached from a hold).
	CapCardsManage Capability = "cards.manage"
	// CapDomainsManage: author, validate and publish domains.
	CapDomainsManage Capability = "domains.manage"
	// CapToolsConnect: connect tools to the gateway and supply their
	// credentials.
	CapToolsConnect Capability = "tools.connect"
	// CapActionsPropose: propose or execute actions from a person's own
	// session (the dashboard's proof actions, raw events).
	CapActionsPropose Capability = "actions.propose"
	// CapHoldsAnswer: approve once or reject a hold. Admins and editors
	// hold it only while separation of duties is off; that filter is
	// applied where a hold is answered (AnswersHolds), not here.
	CapHoldsAnswer Capability = "holds.answer"
	// CapKeysManage: mint and revoke API keys, bind keys to agents.
	CapKeysManage Capability = "keys.manage"
	// CapBilling: plan, payment method, invoices, budget.
	CapBilling Capability = "billing.manage"
	// CapMembersManage: invite, change role, suspend, reactivate,
	// remove members.
	CapMembersManage Capability = "members.manage"
	// CapGovernance: turn separation of duties on or off.
	CapGovernance Capability = "governance.manage"
	// CapAccountSettings: account-wide settings (notification
	// preferences, chat approvals, account kind, alert targets).
	CapAccountSettings Capability = "account.settings"
	// CapExport: export the account. A bulk copy, not a view.
	CapExport Capability = "account.export"
	// CapDelete: delete the account.
	CapDelete Capability = "account.delete"
)

// matrix is the capability table. A capability missing from a
// role's set is refused.
var matrix = map[Role]map[Capability]bool{
	RoleAdmin: set(CapRead, CapPersonal, CapCardsManage, CapDomainsManage, CapToolsConnect,
		CapActionsPropose, CapHoldsAnswer, CapKeysManage, CapBilling, CapMembersManage,
		CapGovernance, CapAccountSettings, CapExport, CapDelete),
	RoleEditor: set(CapRead, CapPersonal, CapCardsManage, CapDomainsManage, CapToolsConnect,
		CapActionsPropose, CapHoldsAnswer),
	RoleApprover: set(CapRead, CapPersonal, CapHoldsAnswer),
	RoleViewer:   set(CapRead, CapPersonal),
}

func set(cs ...Capability) map[Capability]bool {
	m := make(map[Capability]bool, len(cs))
	for _, c := range cs {
		m[c] = true
	}
	return m
}

// Can reports whether role holds capability c. An unknown role holds
// nothing.
func Can(role Role, c Capability) bool {
	return matrix[role][c]
}

// Capabilities lists what role holds, in a stable order, for /v1/me
// and the dashboard's capability-aware controls.
func Capabilities(role Role) []Capability {
	all := []Capability{CapRead, CapPersonal, CapCardsManage, CapDomainsManage, CapToolsConnect,
		CapActionsPropose, CapHoldsAnswer, CapKeysManage, CapBilling, CapMembersManage,
		CapGovernance, CapAccountSettings, CapExport, CapDelete}
	out := make([]Capability, 0, len(all))
	for _, c := range all {
		if Can(role, c) {
			out = append(out, c)
		}
	}
	return out
}

// AnswersHolds reports whether a member with role may answer a hold
// under the tenant's separation-of-duties policy (M11): with it on,
// only approvers answer; with it off, anyone holding CapHoldsAnswer.
// The proposer and Card-changer rules are checked separately, where
// the hold is known.
func AnswersHolds(role Role, separationOfDuties bool) bool {
	if !Can(role, CapHoldsAnswer) {
		return false
	}
	return !separationOfDuties || role == RoleApprover
}

// IdentityRoles is the role set a member's requests carry into domain
// permission checks. Generated
// domains name tenant_owner and operator; admin keeps today's owner
// set so every published domain keeps working.
func IdentityRoles(role Role) []string {
	switch role {
	case RoleAdmin:
		return []string{"admin", "tenant_owner", "operator"}
	case RoleEditor:
		return []string{"editor", "operator"}
	case RoleApprover:
		return []string{"approver"}
	case RoleViewer:
		return []string{"viewer"}
	}
	return nil
}
