package handlers

import "testing"

func TestClearNotificationsReturn(t *testing.T) {
	cases := map[string]string{
		"": "/dashboard",
		"https://app.kiff.dev/dashboard/runtimes":           "/dashboard/runtimes",
		"https://app.kiff.dev/cards?view=list":              "/cards?view=list",
		"https://app.kiff.dev/dashboard/activity?window=7d": "/dashboard/activity?window=7d",
		"https://evil.example/dashboard":                    "/dashboard",
		"https://app.kiff.dev//evil.example/x":              "/dashboard",
	}
	for in, want := range cases {
		if got := clearNotificationsReturn(in); got != want {
			t.Errorf("clearNotificationsReturn(%q) = %q, want %q", in, got, want)
		}
	}
}
