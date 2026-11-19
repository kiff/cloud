package components

import "context"

// The account's kind: "personal", "company", or "" when its owner has not
// chosen yet or it could not be read. WithChrome puts it on the request
// so the shell can show a personal account its own sidebar.

type accountKindKey struct{}

// WithAccountKind records the account's kind for the shell.
func WithAccountKind(ctx context.Context, kind string) context.Context {
	return context.WithValue(ctx, accountKindKey{}, kind)
}

// AccountKindFrom is the account's kind, "" when unknown.
func AccountKindFrom(ctx context.Context) string {
	k, _ := ctx.Value(accountKindKey{}).(string)
	return k
}

// IsPersonal reports a personal account: it sees My Cards, My agents,
// Connected tools, Needs you and Activity, and no domains or contracts.
// An unknown kind shows the workspace as it has always been.
func IsPersonal(ctx context.Context) bool { return AccountKindFrom(ctx) == "personal" }
