package pages

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// keyKind names what a key is, as the API decides it: a key bound to an
// agent acts as that agent whatever roles it holds; an unbound key with
// the account roles is an account key; with viewer only, a read-only
// account key; a connector key connects tools; any other is a key for a
// service (a runtime).
func keyKind(k apiclient.KeyListItem) string {
	switch {
	case k.AgentID != "":
		return "Agent"
	case slices.Contains(k.Roles, "connector"):
		return "Connector"
	case slices.Contains(k.Roles, "tenant_owner") || slices.Contains(k.Roles, "admin"):
		return "Account · " + strings.Join(accountRoles(k.Roles), ", ")
	case slices.Contains(k.Roles, "viewer"):
		return "Read-only account"
	default:
		return "Service"
	}
}

// keyExpiryWords says when a key stops working: "Never", "Expired",
// "Today" (under a day left), or "In N days" (whole days left, rounded
// down, so a key is never shown as lasting longer than it does).
func keyExpiryWords(k apiclient.KeyListItem, now time.Time) string {
	if k.ExpiresAt == nil || *k.ExpiresAt == "" {
		return "Never"
	}
	at, err := time.Parse(time.RFC3339Nano, *k.ExpiresAt)
	if err != nil {
		return *k.ExpiresAt
	}
	left := at.Sub(now)
	switch {
	case left <= 0:
		return "Expired"
	case left < 24*time.Hour:
		return "Today"
	}
	days := int(left / (24 * time.Hour))
	if days == 1 {
		return "In 1 day"
	}
	return fmt.Sprintf("In %d days", days)
}

// accountRoles are the account roles a key holds, in a stable order.
func accountRoles(roles []string) []string {
	var out []string
	for _, r := range []string{"tenant_owner", "admin"} {
		if slices.Contains(roles, r) {
			out = append(out, strings.ReplaceAll(r, "_", " "))
		}
	}
	return out
}

// formatKeyCreated turns the API's raw RFC3339 created_at string into a
// short, readable date ("Jan 2, 2006"). Falls back to the raw value if
// it doesn't parse, so a format change upstream never blanks the cell.
func formatKeyCreated(s string) string {
	if s == "" {
		return "—"
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Format("Jan 2, 2006")
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.Format("Jan 2, 2006")
	}
	return s
}

// shortKeyID trims a long key id to a glanceable head+tail so the table
// column stays tidy; the full id is still available via the title
// attribute on the cell.
func shortKeyID(id string) string {
	if len(id) <= 16 {
		return id
	}
	return id[:8] + "…" + id[len(id)-4:]
}
