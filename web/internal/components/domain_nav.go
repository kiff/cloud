package components

import "context"

// DomainNavItem is one domain in the sidebar's persistent domain
// selector: the slug used in routes, its display name, its real Status
// from the domain-apex list (e.g. "active"), and its per-domain
// governance Posture ("observe" | "enforce", empty when the control
// predates per-domain posture). The pill renders the posture when set —
// it is the mode the decide path actually honors for this domain — and
// falls back to the status otherwise.
type DomainNavItem struct {
	Slug    string
	Name    string
	Status  string
	Posture string
	// Draft marks an in-progress domain that isn't active yet — it lives
	// in Build's draft store, not the active domains list. A draft item
	// renders a "draft" pill and links to Build (its setup context)
	// rather than a domain home page, which doesn't exist until it's
	// activated.
	Draft bool
}

// DomainNav is the model behind the sidebar domain selector: the
// tenant's domains and which one is currently in focus. Current is the
// in-focus slug ("" = none selected / fresh tenant). Built once per GET
// by the chrome middleware and read by the shell, so the selector
// renders on every surface (Runtimes, Notifications, Studio, …).
type DomainNav struct {
	Current string
	Items   []DomainNavItem
}

// CurrentItem returns the in-focus domain, if the current slug matches
// one of the tenant's domains.
func (n DomainNav) CurrentItem() (DomainNavItem, bool) {
	for _, it := range n.Items {
		if it.Slug == n.Current {
			return it, true
		}
	}
	return DomainNavItem{}, false
}

// domainNavItemHref is where a selector row points: a draft goes to Build
// (its setup context; it has no domain home yet), an active domain to its
// home page.
func domainNavItemHref(it DomainNavItem) string {
	if it.Draft {
		return "/dashboard/build"
	}
	return "/dashboard/domains/" + it.Slug
}

type domainNavKey struct{}

// WithDomainNav attaches the domain selector model to ctx.
func WithDomainNav(ctx context.Context, n DomainNav) context.Context {
	return context.WithValue(ctx, domainNavKey{}, n)
}

// DomainNavFrom returns the domain selector model, or a zero value (no
// domains) when none is attached — so the shell renders the "no domain
// yet" state on surfaces that don't enrich.
func DomainNavFrom(ctx context.Context) DomainNav {
	n, _ := ctx.Value(domainNavKey{}).(DomainNav)
	return n
}
