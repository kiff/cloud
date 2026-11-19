package pages

// settingsTab is one tab on Settings.
type settingsTab struct{ ID, Label string }

// settingsTabs are the Settings tabs, one topic each.
var settingsTabs = []settingsTab{
	{"account", "Account"},
	{"notifications", "Notifications"},
	{"view", "Dashboard view"},
	{"workspace", "Workspace"},
	{"usage", "Usage"},
	{"data", "Data & deletion"},
}

// SettingsTabFor is the tab to show for ?tab=, Account when unknown.
func SettingsTabFor(tab string) string {
	for _, t := range settingsTabs {
		if t.ID == tab {
			return tab
		}
	}
	return "account"
}
