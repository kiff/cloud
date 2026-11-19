package whitepaper

import (
	"strings"
	"testing"
)

func TestMarkdownNotEmpty(t *testing.T) {
	t.Parallel()

	src := Markdown()
	if len(src) < 1024 {
		t.Fatalf("Cards whitepaper looks empty or truncated: %d bytes", len(src))
	}
	want := []string{
		// Title and product thesis from the canonical Cards paper.
		"# KIFF Cards",
		"## Delegated work with enforceable authority",
		"CC BY 4.0",
		"github.com/kiff/kiff",
	}
	for _, w := range want {
		if !strings.Contains(string(src), w) {
			t.Errorf("whitepaper.md missing %q — Cards paper may be stale or wrong", w)
		}
	}
}

func TestHTMLRenders(t *testing.T) {
	t.Parallel()

	got, err := HTML()
	if err != nil {
		t.Fatalf("HTML(): %v", err)
	}
	if len(got) == 0 {
		t.Fatal("HTML() returned empty output")
	}
	wantFragments := []string{
		"<h1",  // title
		"<h2",  // section heading
		"<p>",  // body paragraphs
		"KIFF", // content carried through
	}
	for _, w := range wantFragments {
		if !strings.Contains(string(got), w) {
			t.Errorf("rendered HTML missing %q", w)
		}
	}
}

func TestHTMLMemoized(t *testing.T) {
	t.Parallel()

	a, err := HTML()
	if err != nil {
		t.Fatalf("HTML(): %v", err)
	}
	b, err := HTML()
	if err != nil {
		t.Fatalf("HTML(): %v", err)
	}
	// Same string in both calls; the underlying memoization is
	// per-process so this just sanity-checks the API.
	if string(a) != string(b) {
		t.Fatal("HTML() returned different content on repeated calls")
	}
}
