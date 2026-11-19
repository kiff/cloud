package pages

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// View helpers for the operator console templ pages. Kept in plain
// Go so the templates stay declarative: sorting maps into stable
// slices, formatting times/counts, and labeling counters.

// opsKV is a stable key/value pair for rendering a map as a table.
type opsKV struct {
	Key string
	Val int
}

// opsKV64 is opsKV for uint64 counter totals.
type opsKV64 struct {
	Key   string
	Label string
	Val   uint64
}

// opsSortedInts returns the map entries sorted by key for stable
// rendering (Go map iteration order is randomized).
func opsSortedInts(m map[string]int) []opsKV {
	out := make([]opsKV, 0, len(m))
	for k, v := range m {
		out = append(out, opsKV{Key: k, Val: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// opsSortedCounters returns counter totals sorted by name, with a
// short label (the last dotted segment) for display.
func opsSortedCounters(m map[string]uint64) []opsKV64 {
	out := make([]opsKV64, 0, len(m))
	for k, v := range m {
		out = append(out, opsKV64{Key: k, Label: opsCounterLabel(k), Val: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// opsCounterLabel shortens a dotted counter name to its last segment
// (e.g. "kiff.cloud.proposals.blocked" -> "blocked").
func opsCounterLabel(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 && i < len(name)-1 {
		return name[i+1:]
	}
	return name
}

// opsTime formats a UTC timestamp for the console, or an em dash for
// the zero time.
func opsTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04 MST")
}

// opsTimePtr formats an optional timestamp.
func opsTimePtr(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return opsTime(*t)
}

func opsInt(n int) string { return strconv.Itoa(n) }

func opsU64(n uint64) string { return strconv.FormatUint(n, 10) }

// opsStatusClass maps a tenant lifecycle status to a dashboard pill
// class (reusing the dashboard's pill palette, not a bespoke one).
func opsStatusClass(status string) string {
	switch status {
	case "active":
		return "dash-pill dash-pill-ok"
	case "suspended", "deleted_pending":
		return "dash-pill dash-pill-warn"
	case "deleted":
		return "dash-pill dash-pill-error"
	default:
		return "dash-pill dash-pill-muted"
	}
}

// opsRiskClass maps a risk level to a dashboard pill class.
func opsRiskClass(level string) string {
	switch level {
	case "high":
		return "dash-pill dash-pill-error"
	case "warn":
		return "dash-pill dash-pill-warn"
	default:
		return "dash-pill dash-pill-muted"
	}
}

// opsAge renders a seconds duration as a compact "3h" / "5m" / "12s",
// or an em dash for zero.
func opsAge(seconds int64) string {
	if seconds <= 0 {
		return "—"
	}
	switch {
	case seconds >= 86400:
		return strconv.FormatInt(seconds/86400, 10) + "d"
	case seconds >= 3600:
		return strconv.FormatInt(seconds/3600, 10) + "h"
	case seconds >= 60:
		return strconv.FormatInt(seconds/60, 10) + "m"
	default:
		return strconv.FormatInt(seconds, 10) + "s"
	}
}

// opsInt64 formats an int64.
func opsInt64(n int64) string { return strconv.FormatInt(n, 10) }

// opsDash is s, or a dash when it's empty.
func opsDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// opsUSD is an AI cost in millionths of a US dollar, to the cent.
func opsUSD(micro int64) string {
	return "$" + strconv.FormatFloat(float64(micro)/1e6, 'f', 2, 64)
}

// opsAlertSlug returns the alert's tenant slug, falling back to the
// tenant id when the slug is absent.
func opsAlertSlug(a apiclient.OpsAlert) string {
	if a.TenantSlug != "" {
		return a.TenantSlug
	}
	return a.TenantID
}

// opsAuditDetail formats the metadata already recorded on an audit
// entry into a compact human string: "active → suspended",
// "free_dev → launch_pilot", and the alert kind when present. Returns
// "" when there is nothing useful to show (no overbuilding — just
// surfacing what's stored).
func opsAuditDetail(e apiclient.OpsAuditEntry) string {
	m := e.Metadata
	if len(m) == 0 {
		return ""
	}
	var parts []string
	if from, okF := m["from"]; okF {
		if to, okT := m["to"]; okT {
			parts = append(parts, fmt.Sprintf("%v → %v", from, to))
		}
	}
	if kind, ok := m["kind"]; ok {
		parts = append(parts, fmt.Sprintf("%v", kind))
	}
	return strings.Join(parts, " · ")
}

// opsOutcomeClass maps a decision/receipt outcome to a dashboard pill
// class.
func opsOutcomeClass(outcome string) string {
	switch outcome {
	case "allowed":
		return "dash-pill dash-pill-ok"
	case "approval_required", "shadowed", "limit_exceeded":
		return "dash-pill dash-pill-warn"
	case "blocked", "invalid":
		return "dash-pill dash-pill-error"
	default:
		return "dash-pill dash-pill-muted"
	}
}

// opsSupportStatusClass maps a support-ticket status to a dashboard
// pill class. "open" is the one that needs the operator's attention,
// so it reads as a warn; answered is resolved-side, closed is muted.
func opsSupportStatusClass(status string) string {
	switch status {
	case "open":
		return "dash-pill dash-pill-warn"
	case "in_progress":
		return "dash-pill dash-pill-info"
	case "answered":
		return "dash-pill dash-pill-ok"
	case "closed":
		return "dash-pill dash-pill-muted"
	default:
		return "dash-pill dash-pill-muted"
	}
}

// opsSupportStatusLabel gives a short human label for a ticket status.
func opsSupportStatusLabel(status string) string {
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

// opsSupportAuthorLabel names the side of the thread a message is from,
// from the operator's point of view.
func opsSupportAuthorLabel(kind string) string {
	if kind == "operator" {
		return "Operator"
	}
	return "Tenant"
}
