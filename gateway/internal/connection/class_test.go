package connection

import "testing"

// A connector's app is kept only as a slug, so a name that isn't one never
// selects a class table.
func TestAnAppIsKeptOnlyAsASlug(t *testing.T) {
	for in, want := range map[string]string{
		"qonto": "qonto", " Qonto ": "qonto", "google-sheets": "google-sheets",
		"": "", "qonto/../x": "", "qo nto": "", "qonto\n": "qonto", "a" + string(make([]byte, 40)): "",
	} {
		if got := appSlug(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
