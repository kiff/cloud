package pages

import (
	"strings"
	"time"
)

// supportStatusLabel maps a raw ticket status to a short human label
// for the dashboard. Unknown values pass through unchanged.
func supportStatusLabel(status string) string {
	switch status {
	case "open":
		return "Open"
	case "in_progress":
		return "In progress"
	case "answered":
		return "Answered"
	case "closed":
		return "Closed"
	default:
		return status
	}
}

// supportStatusClass returns the CSS pill modifier for a status so the
// list and detail color the state consistently. Empty (plain pill) for
// "open".
func supportStatusClass(status string) string {
	switch status {
	case "answered":
		return "dash-pill-ok"
	case "closed":
		return "dash-pill-muted"
	case "in_progress":
		return "dash-pill-info"
	default: // open
		return ""
	}
}

// supportAuthorLabel names the side of the thread a message came from.
func supportAuthorLabel(kind string) string {
	if kind == "operator" {
		return "KIFF Support"
	}
	return "You"
}

// fmtSupportTime renders a ticket/message timestamp compactly in UTC.
func fmtSupportTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2 Jan 2006, 15:04 MST")
}

// supportReplyOpen reports whether the tenant can still add a reply.
// Every status is repliable — a reply on an answered/closed ticket
// reopens it server-side — so this is always true today; kept as a
// seam in case a terminal status is added later.
func supportReplyOpen(status string) bool {
	return strings.TrimSpace(status) != ""
}
