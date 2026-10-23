package tenant

// AccountKind is what the account is for: one person's
// agents, or a company's. It changes what the dashboard shows, not what
// any Card, exception or statement is: those are the same objects in
// both.
type AccountKind string

const (
	// AccountPersonal: My Cards, My agents, Connected tools, Needs you
	// and Activity; no domains or contracts are shown.
	AccountPersonal AccountKind = "personal"
	// AccountCompany: the workspace as it has been, plus Connected tools.
	AccountCompany AccountKind = "company"
)

// MetadataKeyAccountKind is the Tenant.Metadata key the kind lives
// under, set by the account's owner (PUT /v1/me/account-kind).
const MetadataKeyAccountKind = "account_kind"

// ParseAccountKind accepts exactly the two kinds.
func ParseAccountKind(s string) (AccountKind, bool) {
	switch k := AccountKind(s); k {
	case AccountPersonal, AccountCompany:
		return k, true
	}
	return "", false
}

// AccountKindFor is the account's kind, or "" when it has not been
// chosen yet: the dashboard asks once.
func AccountKindFor(t Tenant) AccountKind {
	if s, ok := t.Metadata[MetadataKeyAccountKind].(string); ok {
		if k, ok := ParseAccountKind(s); ok {
			return k
		}
	}
	return ""
}

// MetadataKeyChatApprovals is the Tenant.Metadata key for whether a
// held call is also offered in the agent's chat: "on"
// or "off", set by the account's owner (PUT /v1/me/chat-approvals).
const MetadataKeyChatApprovals = "chat_approvals"

// ChatApprovalsFor reports whether the gateway may ask the agent's chat
// client to open a held call's Needs you page. The owner's own choice
// wins. Otherwise it is on for a personal account, where the person in
// the chat is the owner, and off for a company's, where the person
// running the agent usually is not and could not answer. It only decides
// how the owner is told; who may answer never changes.
func ChatApprovalsFor(t Tenant) bool {
	switch t.Metadata[MetadataKeyChatApprovals] {
	case "on":
		return true
	case "off":
		return false
	}
	return AccountKindFor(t) == AccountPersonal
}
