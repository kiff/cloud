package notify

import "context"

// summaryCtxKey is the context key under which the notifications
// middleware stashes the per-request Summary. The shared dashboard
// shell reads it to render the topbar bell, and the notifications
// page reads it instead of refetching.
type summaryCtxKey struct{}

// WithSummary attaches the composed notification Summary to ctx.
// Called once per request by the notifications middleware.
func WithSummary(ctx context.Context, s Summary) context.Context {
	return context.WithValue(ctx, summaryCtxKey{}, s)
}

// SummaryFrom returns the Summary attached by the middleware, or a
// zero Summary (no badge, no items) when none is present — so the
// shell renders a quiet bell on surfaces that never enrich.
func SummaryFrom(ctx context.Context) Summary {
	s, _ := ctx.Value(summaryCtxKey{}).(Summary)
	return s
}
