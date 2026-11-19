// Package notify composes the dashboard's notification feed from
// signals KIFF already produces — it is a *view* over existing
// tenant-scoped state, not a new event bus or persistence layer.
//
// v1 sources (honest, no new storage):
//
//   - Approvals: decisions held for a reviewer (approval_required).
//     These are actionable — a runtime is blocked until someone
//     decides — so they drive the topbar badge count.
//   - Runtime health: a connected guard runtime that has gone quiet
//     (idle) surfaces as an informational item, not a badge count,
//     because "quiet" is a state, not a fresh event, and without a
//     read/seen store we can't honestly claim it is "new".
//
// Read/unread tracking and per-tenant notification rules need their
// own table and are deliberately out of this package for now; the
// badge means "items needing attention right now", which holds
// without any persistence.
package notify

import (
	"fmt"
	"sort"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// Kind classifies a notification for filtering + iconography.
type Kind string

const (
	KindApproval Kind = "approval"
	KindRuntime  Kind = "runtime"
	KindBudget   Kind = "budget"
)

// Rule keys are the stable wire contract shared with the API's
// notification-prefs surface (same JSON keys). Each gates one source in
// Compose so a tenant can mute a notification kind.
const (
	RuleApproval    = "approval"
	RuleRuntimeIdle = "runtime_idle"
	// RuleHeldEmail gates no feed item: it turns off the API's email about
	// each held action, for the whole account.
	RuleHeldEmail = "held_email"
)

// Prefs is a rule-key → enabled map (the tenant's notification
// preferences). Absent rules are enabled — default-on — so a nil/zero
// Prefs surfaces every kind, matching a fresh tenant.
type Prefs map[string]bool

// Enabled reports whether a rule is on. Absent = on.
func (p Prefs) Enabled(rule string) bool {
	if p == nil {
		return true
	}
	v, ok := p[rule]
	return !ok || v
}

// Severity drives the pill styling. warn = needs a decision;
// error = something is wrong/degraded; info = FYI.
type Severity string

const (
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
	SeverityInfo  Severity = "info"
)

// Item is one notification, already shaped for rendering: no
// apiclient types leak past Compose, so the templ layer stays a
// pure presentation surface.
type Item struct {
	ID          string
	Revision    int64
	Persistent  bool
	Seen        bool
	Kind        Kind
	Severity    Severity
	Title       string
	Subtitle    string
	Scope       string // domain/area label (e.g. an entity type), may be empty
	Href        string // where clicking the item takes the user
	ActionLabel string // the row's CTA label ("Review", "Inspect")
	At          time.Time
	// Since is when this notification first appeared in the feed, which
	// is not always At: a runtime item is stamped with its last traffic
	// but only surfaces RuntimeIdleAfter later. Clearing compares Since.
	Since     time.Time
	TimeLabel string // relative time, precomputed ("8m", "24h", "3d")
	// Actionable marks items that need a human decision (approvals).
	// Only actionable items count toward the badge.
	Actionable bool
}

// Summary is the composed feed plus the badge count. Items are
// newest-first. Count is the number of actionable items (the badge);
// len(Items) may be larger because informational items are included
// in the feed but not counted.
type Summary struct {
	Durable bool
	Count   int
	Items   []Item
	// ClearedAt is when the user last pressed "Clear all" in the bell.
	// Zero means never. The full feed ignores it; only the bell hides
	// what came before it (see Uncleared).
	ClearedAt time.Time
}

// Uncleared returns the summary the bell shows: only items that first
// appeared (Since) after ClearedAt, with the badge count recomputed over them. The feed is a
// view over live state, so clearing hides, it does not resolve: a
// pending approval stays pending and stays on the notifications page.
func (s Summary) Uncleared() Summary {
	if s.ClearedAt.IsZero() && !s.Durable {
		return s
	}
	out := Summary{ClearedAt: s.ClearedAt, Durable: s.Durable}
	for _, it := range s.Items {
		if it.Persistent {
			if !it.Seen {
				out.Items = append(out.Items, it)
				if it.Actionable {
					out.Count++
				}
			}
			continue
		}
		if it.Since.After(s.ClearedAt) {
			out.Items = append(out.Items, it)
			if it.Actionable {
				out.Count++
			}
		}
	}
	return out
}

// RuntimeIdleAfter is the age past which a connected runtime is
// considered idle. Mirrors the runtimes page's "idle" threshold so
// the notification and the status pill agree.
const RuntimeIdleAfter = 24 * time.Hour

// Compose builds the notification feed for one tenant from its pending
// approvals and guard connections, as of now, honoring the tenant's
// prefs (a muted rule contributes nothing). Pure and deterministic:
// same inputs, same feed — so it is unit-tested without the HTTP stack.
func Compose(approvals []apiclient.PendingApproval, conns []apiclient.GuardConnection, prefs Prefs, now time.Time) Summary {
	items := make([]Item, 0, len(approvals)+len(conns))

	if !prefs.Enabled(RuleApproval) {
		approvals = nil
	}
	if !prefs.Enabled(RuleRuntimeIdle) {
		conns = nil
	}

	for _, a := range approvals {
		title := a.ActionName + " is waiting for approval"
		if a.ActionName == "" {
			title = "An action is waiting for approval"
		}
		scope := a.EntityType
		if scope == "" {
			scope = "All domains"
		}
		href := "/dashboard/activity?tab=approvals"
		if a.ID != "" {
			// Deep-link to the specific approval card so the reviewer lands
			// on the item to act on, not just the tab. The fragment rides
			// through the Activity redirect (browser re-applies it).
			href += "#ap-" + a.ID
		}
		items = append(items, Item{
			Kind:        KindApproval,
			Severity:    SeverityWarn,
			Title:       title,
			Subtitle:    approvalSubtitle(a),
			Scope:       scope,
			Href:        href,
			ActionLabel: "Review",
			At:          a.CreatedAt,
			Since:       a.CreatedAt,
			TimeLabel:   relTime(a.CreatedAt, now),
			Actionable:  true,
		})
	}

	for _, c := range conns {
		if c.LastSeenAt.IsZero() || now.Sub(c.LastSeenAt) < RuntimeIdleAfter {
			continue
		}
		name := runtimeName(c)
		items = append(items, Item{
			Kind:        KindRuntime,
			Severity:    SeverityInfo,
			Title:       name + " has gone quiet",
			Subtitle:    "No traffic since " + relTime(c.LastSeenAt, now) + " ago",
			Scope:       "Workspace",
			Href:        "/dashboard/runtimes",
			ActionLabel: "Inspect",
			At:          c.LastSeenAt,
			Since:       c.LastSeenAt.Add(RuntimeIdleAfter),
			TimeLabel:   relTime(c.LastSeenAt, now),
		})
	}

	sort.SliceStable(items, func(i, j int) bool { return items[i].At.After(items[j].At) })

	count := 0
	for _, it := range items {
		if it.Actionable {
			count++
		}
	}
	return Summary{Count: count, Items: items}
}

func approvalSubtitle(a apiclient.PendingApproval) string {
	switch {
	case a.EntityID != "" && a.RequestedBy != "":
		return fmt.Sprintf("%s · requested by %s", a.EntityID, a.RequestedBy)
	case a.EntityID != "":
		return a.EntityID
	case a.RequestedBy != "":
		return "requested by " + a.RequestedBy
	default:
		return ""
	}
}

// runtimeName mirrors the runtimes page's label preference: workflow,
// then agent, then project.
func runtimeName(c apiclient.GuardConnection) string {
	for _, s := range []string{c.Workflow, c.AgentID, c.Project} {
		if s != "" {
			return s
		}
	}
	return "A runtime"
}

// relTime renders a compact relative age ("8m", "3h", "2d"). Sub-minute
// ages read as "just now". Future timestamps (clock skew) clamp to "0m".
func relTime(t, now time.Time) string {
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}
