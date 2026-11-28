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

func TestAnAppIsKeptOnlyForAToolOnAVendorRelay(t *testing.T) {
	h := &Handler{RelayURLs: []string{"https://relay.example.com"}}
	if got := h.relayApp("https://relay.example.com/mcp", "Qonto"); got != "qonto" {
		t.Fatalf("a relay's tool: app %q, want qonto", got)
	}
	if got := h.relayApp("https://other.example.com/mcp", "qonto"); got != "" {
		t.Fatalf("a tool registered straight to KIFF: app %q, want none", got)
	}
}
