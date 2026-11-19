package components

import "context"

// AccountOption is one account the signed-in person belongs to.
type AccountOption struct {
	TenantID string
	Label    string
	Role     string
	Current  bool
}

// AccountSwitch is the topbar account switcher's model. It renders only
// when the person belongs to more than one account.
type AccountSwitch struct {
	Accounts []AccountOption
}

// Current is the account in use, or the zero option.
func (a AccountSwitch) Current() AccountOption {
	for _, o := range a.Accounts {
		if o.Current {
			return o
		}
	}
	return AccountOption{}
}

type accountSwitchKey struct{}

// WithAccountSwitch carries the switcher model to the shell.
func WithAccountSwitch(ctx context.Context, a AccountSwitch) context.Context {
	return context.WithValue(ctx, accountSwitchKey{}, a)
}

// AccountSwitchFrom returns the switcher model, empty when none was set.
func AccountSwitchFrom(ctx context.Context) AccountSwitch {
	a, _ := ctx.Value(accountSwitchKey{}).(AccountSwitch)
	return a
}

type capabilitiesKey struct{}

// WithCapabilities carries what the signed-in person may do on the
// account (from /v1/me), so pages hide controls their role cannot use.
// nil means unknown: everything is shown and the API decides.
func WithCapabilities(ctx context.Context, caps []string) context.Context {
	return context.WithValue(ctx, capabilitiesKey{}, caps)
}

// Can reports whether the signed-in person holds capability c. Unknown
// capabilities (no /v1/me read, or an older API) answer true: hiding a
// control is a courtesy, the API is the boundary.
func Can(ctx context.Context, c string) bool {
	caps, ok := ctx.Value(capabilitiesKey{}).([]string)
	if !ok || caps == nil {
		return true
	}
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

// RoleLabel is how a role reads in the dashboard.
func RoleLabel(role string) string {
	switch role {
	case "admin":
		return "Admin"
	case "editor":
		return "Editor"
	case "approver":
		return "Approver"
	case "viewer":
		return "Viewer"
	}
	return role
}
