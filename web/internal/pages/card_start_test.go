package pages

import "testing"

func TestCardStartHrefAndPrefillLimit(t *testing.T) {
	if got := CardStartHref("refunds", "support-agent", "1000"); got != "/cards/new?limit=1000&subject=support-agent&task=refunds" {
		t.Errorf("href %q", got)
	}
	if got := CardStartHref("", "", ""); got != "/cards/new" {
		t.Errorf("empty href %q", got)
	}
	for in, want := range map[string]string{"5000": "5000", " 40 ": "40", "1e9": "", "-1": "", "<b>": "", "1234567890": ""} {
		if got := PrefillLimit(in); got != want {
			t.Errorf("PrefillLimit(%q) = %q, want %q", in, got, want)
		}
	}
}
