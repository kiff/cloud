package handlers

import (
	"log/slog"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
)

// A Card says how each tool it covers is counted in the words the owner
// approved for its profile (the connection-profiles design "How this is counted"), in v2: no
// line about KIFF's own checks. Nothing for a tool without one.
func TestACardSaysHowItsToolsAreCounted(t *testing.T) {
	raw, err := os.ReadFile("../profile/testdata/valid/notion-pages.json")
	if err != nil {
		t.Fatal(err)
	}
	h := &MandatesHandler{logger: slog.New(slog.DiscardHandler)}
	grants := []map[string]any{{"action": "notion-create-pages"}, {"action": "refund"}}
	got := h.counted(httptest.NewRequest("GET", "/v1/me/mandates", nil), "t1", map[string][]byte{"notion-create-pages": raw}, grants)
	if len(got) != 1 || got[0]["tool"] != "notion-create-pages" {
		t.Fatalf("counted %v", got)
	}
	want := []string{
		"Write a page: writes in Notion.",
		"Write a page: can act on several at once.",
		`Write a page: a Card's "Only for" can name which parent page it acts on.`,
	}
	if lines := got[0]["lines"].([]string); !slices.Equal(lines, want) {
		t.Fatalf("lines %q", lines)
	}
}
