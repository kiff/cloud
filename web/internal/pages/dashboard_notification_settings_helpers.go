package pages

import "github.com/kiff/cloud/web/internal/notify"

// NotificationRuleRow is one row on the notification settings page: a
// rule the tenant can turn on or off, with the fixed delivery + cadence
// the product implements today.
type NotificationRuleRow struct {
	Key      string
	Label    string
	Desc     string
	Scope    string
	Delivery string
	Cadence  string
	Enabled  bool
}

// notificationRuleRows returns the settings rows for the rules the
// product actually implements, resolving each against the tenant's
// prefs (absent = enabled). The first two are in-app; held calls are
// also emailed, and that email has its own row.
func notificationRuleRows(prefs map[string]bool) []NotificationRuleRow {
	enabled := func(rule string) bool {
		if prefs == nil {
			return true
		}
		v, ok := prefs[rule]
		return !ok || v
	}
	return []NotificationRuleRow{
		{
			Key:      notify.RuleApproval,
			Label:    "Approval required",
			Desc:     "A governed action is held for a reviewer before it can run.",
			Scope:    "All domains",
			Delivery: "In-app",
			Cadence:  "Immediate",
			Enabled:  enabled(notify.RuleApproval),
		},
		{
			Key:      notify.RuleHeldEmail,
			Label:    "Email about held calls",
			Desc:     "Each held call is emailed to the people who answer it. Needs you, the bell and phone alerts still show it when this is off.",
			Scope:    "Workspace",
			Delivery: "Email",
			Cadence:  "Immediate",
			Enabled:  enabled(notify.RuleHeldEmail),
		},
		{
			Key:      notify.RuleRuntimeIdle,
			Label:    "Runtime went quiet",
			Desc:     "A previously active runtime has not called KIFF for 24 hours.",
			Scope:    "Workspace",
			Delivery: "In-app",
			Cadence:  "After 24h",
			Enabled:  enabled(notify.RuleRuntimeIdle),
		},
	}
}

// notificationRuleToggleLabel / notificationRuleTargetState render the
// per-row toggle: the button flips the rule to the opposite state, and
// the POST carries that target state.
func notificationRuleToggleLabel(enabled bool) string {
	if enabled {
		return "Turn off"
	}
	return "Turn on"
}

func notificationRuleTargetState(enabled bool) string {
	if enabled {
		return "false"
	}
	return "true"
}

func notificationRuleStatusLabel(enabled bool) string {
	if enabled {
		return "On"
	}
	return "Off"
}

func notificationRuleStatusClass(enabled bool) string {
	if enabled {
		return "dash-rule-status dash-rule-status-on"
	}
	return "dash-rule-status dash-rule-status-off"
}
