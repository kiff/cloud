package pages

import (
	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
)

// tenantLabel derives the workspace label shown in the dashboard
// topbar from the session's Me. Uses the tenant slug (a stable
// workspace identifier like "cookbook"), never the auto-generated
// tenant display name — that name must not read as a human
// identity in the chrome. Empty when Me is nil.
func tenantLabel(me *apiclient.Me) string {
	if me == nil {
		return ""
	}
	return me.TenantSlug
}

// chromeFrom builds the dashboard shell chrome from the identity
// fields every dashboard view carries plus its Me lookup.
func chromeFrom(displayName, email string, me *apiclient.Me) components.DashboardChrome {
	return components.NewDashboardChrome(displayName, email, tenantLabel(me))
}

// chromeWith builds the chrome from an explicit tenant label, for
// views (like Studio) that carry the tenant slug directly rather
// than a *Me.
func chromeWith(displayName, email, tenant string) components.DashboardChrome {
	return components.NewDashboardChrome(displayName, email, tenant)
}

// chromeWithDomain is chromeWith plus a domain in focus, for the Build
// (Studio) surface which carries the tenant slug directly but, when
// editing a selected domain, must keep the domain-scoped spine
// (Domain / Govern / Activity) pointed at that domain. currentDomain ""
// is identical to chromeWith.
func chromeWithDomain(displayName, email, tenant, currentDomain string) components.DashboardChrome {
	c := components.NewDashboardChrome(displayName, email, tenant)
	c.CurrentDomain = currentDomain
	return c
}

// chromeForDomain builds the chrome with a domain in focus: the
// spine's domain-scoped facets — Domain home, Govern, Activity —
// follow currentDomain so navigating between them stays within the
// selected domain. currentDomain "" is identical to chromeFrom.
func chromeForDomain(displayName, email string, me *apiclient.Me, currentDomain string) components.DashboardChrome {
	c := components.NewDashboardChrome(displayName, email, tenantLabel(me))
	c.CurrentDomain = currentDomain
	return c
}
