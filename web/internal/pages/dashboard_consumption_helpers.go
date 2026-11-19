package pages

import (
	"fmt"
)

// Account-level consumption helpers. The units are the v1 offer's two
// meters, read from GET /v1/me/usage: tafs tasks and KIFF actions, each
// counted only when a call got something done. Never a second notion of
// usage.

// meterLine renders "X of Y tasks", or "X tasks" when nothing is included
// (company, internal).
func meterLine(counted, included int, one, many string) string {
	if included <= 0 {
		return fmt.Sprintf("%s %s", commaInt(counted), pluralize(counted, one, many))
	}
	return fmt.Sprintf("%s of %s %s", commaInt(counted), commaInt(included), many)
}

// consumptionTasks is the tasks line: used of what the period has, the
// plan's included tasks and its pack tasks.
func consumptionTasks(v DashboardView) string {
	if v.Meters == nil {
		return "—"
	}
	t := v.Meters.Tasks
	if t.Left == nil {
		return meterLine(t.Counted, 0, "task", "tasks")
	}
	return meterLine(t.Counted, t.Included+t.Packs, "task", "tasks")
}

// consumptionActions is the actions line.
func consumptionActions(v DashboardView) string {
	if v.Meters == nil {
		return "—"
	}
	return meterLine(v.Meters.Actions.Counted, v.Meters.Actions.Included, "action", "actions")
}

// consumptionPctLabel is the headline pill: no tasks left, the
// days left of the trial, or the share of the included tasks used.
func consumptionPctLabel(v DashboardView) string {
	m := v.Meters
	switch {
	case m == nil:
		return "—"
	case m.Tasks.CeilingReached:
		return "no tasks left"
	case m.Trial != nil && m.Trial.EndsAt != nil:
		return fmt.Sprintf("trial: %d %s left", m.Trial.DaysLeft, pluralize(m.Trial.DaysLeft, "day", "days"))
	case m.Trial != nil:
		return "trial not started"
	case m.Tasks.Included+m.Tasks.Packs > 0:
		return fmt.Sprintf("%d%% of tasks", m.Tasks.Counted*100/(m.Tasks.Included+m.Tasks.Packs))
	}
	return "—"
}

// consumptionPeriodLabel renders the billing period.
func consumptionPeriodLabel(v DashboardView) string {
	if v.Meters == nil || v.Meters.Period.Start.IsZero() || v.Meters.Period.End.IsZero() {
		return "this period"
	}
	return v.Meters.Period.Start.Format("Jan 2") + " – " + v.Meters.Period.End.Format("Jan 2")
}

// consumptionNote says what counts, what's free, and what happens past
// what's included, by plan.
func consumptionNote(v DashboardView) string {
	const counts = "A task is a call a taf made, and an action a call one of your own agents made, that KIFF allowed and that got something done. Refused, held then refused, failed, halted, reads, trials and retries are free."
	switch canonicalPlan(v.Me) {
	case "trial":
		return counts + " The trial includes 25 tasks and 500 actions; past them, KIFF refuses what would count, until a pack adds tasks."
	case "solo", "team":
		return counts + " Past the included tasks, tafs use pack tasks, and stop when none are left. Actions past what's included are €0.02 each and never stop."
	}
	return counts
}

// commaInt formats an integer with thousands separators (e.g. 50000 ->
// "50,000"), so the consumption figures read cleanly.
func commaInt(n int) string {
	if n < 0 {
		return "-" + commaInt(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	// Insert commas from the right.
	var out []byte
	pre := len(s) % 3
	if pre > 0 {
		out = append(out, s[:pre]...)
	}
	for i := pre; i < len(s); i += 3 {
		if len(out) > 0 {
			out = append(out, ',')
		}
		out = append(out, s[i:i+3]...)
	}
	return string(out)
}

// trialActionLabel is the button that starts the plan on the web: the
// 7-day trial for an account that hasn't started it, else the plan.
func trialActionLabel(DashboardView) string {
	// No trial (pricing phase 2): the web sells Company; Solo is in the app.
	return "Choose Company"
}
