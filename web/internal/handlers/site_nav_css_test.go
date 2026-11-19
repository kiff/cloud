package handlers

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The mobile menu has collapsed to a sliver more than once. The cause is
// always the same: the open menu is position: fixed inside the header,
// and a property on the header makes the header its containing block, so
// a 56px-tall box is all it gets.
//
// Neither half of this can be checked without a phone, so both halves
// are checked as text. It is a weak test of appearance and a strong test
// of the two things that have actually broken.
func TestMobileMenuCannotCollapse(t *testing.T) {
	t.Parallel()

	b, err := os.ReadFile(filepath.Join("..", "assets", "static", "css", "site.css"))
	if err != nil {
		t.Fatalf("read site.css: %v", err)
	}
	css := string(b)

	// 1. The open overlay states its height rather than deriving it from
	//    bottom: 0, so a containing-block surprise cannot flatten it.
	open := ruleFor(t, css, `.site-nav-checkbox:checked ~ .site-primary-nav`)
	if !strings.Contains(open, "height: calc(100dvh - 56px)") {
		t.Error("the open menu does not state an explicit dvh height; " +
			"deriving it from bottom: 0 is what collapses it to the header's 56px")
	}
	if !strings.Contains(open, "height: calc(100vh - 56px)") {
		t.Error("no 100vh fallback before the dvh height, so browsers without dvh get no height at all")
	}
	if regexp.MustCompile(`(^|[^-])inset:`).MatchString(open) {
		t.Error("the open menu uses inset, which reintroduces the derived bottom edge; " +
			"set top and an explicit height instead")
	}

	// 2. Nothing on the header may create a containing block for it. A
	//    single one of these reaching .site-nav from any stylesheet is
	//    enough to break the menu on mobile WebKit.
	nav := ruleFor(t, css, `.site-nav`)
	for _, prop := range []string{
		"backdrop-filter: none",
		"filter: none",
		"transform: none",
		"perspective: none",
		"contain: none",
		"will-change: auto",
	} {
		if !strings.Contains(nav, prop) {
			t.Errorf("the mobile .site-nav rule does not pin %q; "+
				"that property creates a containing block for the fixed menu", prop)
		}
	}
}

// ruleFor returns the declaration block of the last rule whose selector
// list ends with sel. Last, because the mobile overrides come after the
// base rule and they are the ones under test.
func ruleFor(t *testing.T, css, sel string) string {
	t.Helper()
	var found string
	for i := 0; ; {
		j := strings.Index(css[i:], sel+" {")
		if j < 0 {
			break
		}
		start := i + j + len(sel) + 2
		end := strings.Index(css[start:], "}")
		if end < 0 {
			break
		}
		found = css[start : start+end]
		i = start + end
	}
	if found == "" {
		t.Fatalf("no rule found for selector %q", sel)
	}
	return found
}
