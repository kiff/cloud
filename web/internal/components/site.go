package components

import (
	"net/url"
	"strings"
)

// siteNavClass returns the CSS classes for a primary nav link in the
// redesigned site nav (LayoutSite). The link matching the current
// section (activeNav) gets the active modifier so it reads as the
// current page; everything else is a plain link.
func siteNavClass(activeNav, key string) string {
	if activeNav == key {
		return "site-nav-link site-nav-active"
	}
	return "site-nav-link"
}

// DashboardChrome carries the identity bits the dashboard shell
// renders in its topbar and sidebar footer: the workspace/tenant
// label shown next to the logo, the signed-in user's display name,
// and the avatar initials. Built from a page's view fields via
// NewDashboardChrome so every dashboard surface renders the same
// chrome.
type DashboardChrome struct {
	TenantLabel string // e.g. "cookbook / duplicate-payment" or a tenant slug
	UserName    string // display name or email
	Email       string // signed-in email, shown beside the topbar avatar
	Initials    string // 1–2 char avatar initials

	// CurrentDomain is the slug of the domain currently in focus, or ""
	// when none. When set, the spine nav scopes its domain-scoped
	// facets to it — the Domain home, Govern, and Activity follow the
	// selected domain — so moving between those facets stays within the
	// domain. Build, Runtimes, Examples, and Settings are
	// workspace-level and ignore it (Build's per-domain authoring is a
	// Slice 3 follow-up, not faked here).
	CurrentDomain string
}

// DomainHomeHref is the "Domain" nav target: the selected domain's
// home when one is in focus, else the domains apex.
func (c DashboardChrome) DomainHomeHref() string {
	if c.CurrentDomain == "" {
		return "/dashboard/domains"
	}
	return "/dashboard/domains/" + url.PathEscape(c.CurrentDomain)
}

// GovernHref is the "Govern" nav target: scoped to the selected
// domain via ?domain= when one is in focus, else the unscoped
// (all-controls) Govern surface.
func (c DashboardChrome) GovernHref() string {
	if c.CurrentDomain == "" {
		return "/dashboard/govern"
	}
	return "/dashboard/govern?domain=" + url.QueryEscape(c.CurrentDomain)
}

// ActivityHref is the "Activity" nav target: every call across every
// domain, each with its receipt. A domain's own evidence view is under
// Setup › Domain.
func (c DashboardChrome) ActivityHref() string {
	return "/activity"
}

// NewDashboardChrome assembles the shell chrome from the identity
// fields every dashboard view carries (display name, email, tenant
// label). Falls back gracefully when fields are empty so the shell
// always renders something sensible.
func NewDashboardChrome(displayName, email, tenantLabel string) DashboardChrome {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = strings.TrimSpace(email)
	}
	label := strings.TrimSpace(tenantLabel)
	return DashboardChrome{
		TenantLabel: label,
		UserName:    name,
		Email:       strings.TrimSpace(email),
		Initials:    avatarInitials(name, email),
	}
}

// avatarInitials derives up to two uppercase initials from a display
// name ("Gabriel Sarmiento" -> "GS"), falling back to the first
// letter of the email local part, then "K".
func avatarInitials(name, email string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		parts := strings.Fields(name)
		switch len(parts) {
		case 0:
		case 1:
			return strings.ToUpper(firstRune(parts[0]))
		default:
			return strings.ToUpper(firstRune(parts[0]) + firstRune(parts[len(parts)-1]))
		}
	}
	if e := strings.TrimSpace(email); e != "" {
		return strings.ToUpper(firstRune(e))
	}
	return "K"
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}
