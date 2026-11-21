package main

import "testing"

// The web reads KIFF_APP_SIGNIN as the API does: the page is up for "on" and
// "test" (the API's approve then decides who gets through), and off for
// anything else, so no value opens the page while the API refuses.
func TestTheAppSignInPageFollowsTheAPIsSwitch(t *testing.T) {
	for v, want := range map[string]bool{
		"on": true, "test": true, " test ": true,
		"": false, "off": false, "ON": false, "Test": false, "true": false, "1": false,
	} {
		if got := appSignInOn(v); got != want {
			t.Errorf("appSignInOn(%q) = %v, want %v", v, got, want)
		}
	}
}
