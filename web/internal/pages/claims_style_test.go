package pages

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// .site-section carries a border-bottom and no vertical padding — every
// page section has to supply its own. Both claim pages shipped without
// it once and rendered flush against the section rules, so this pins it
// rather than trusting a future edit to remember.
//
// .pr-hero-sub is a hero subtitle with no bottom margin; using it for
// body copy made consecutive paragraphs collide. It is allowed once per
// page, in the hero.
func TestClaimPagesUseTheClaimStyles(t *testing.T) {
	t.Parallel()
	// onrails.templ moved to the homepage design (home.css) and is no
	// longer a claim page.
	for _, path := range []string{"invariants.templ"} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		src := string(b)

		if !strings.Contains(src, `"css/claims.css"`) {
			t.Errorf("%s: does not load css/claims.css, so its sections have no padding", path)
		}

		// Every non-hero section needs cl-section.
		for _, m := range regexp.MustCompile(`<section class="site-section([^"]*)"`).FindAllStringSubmatch(src, -1) {
			rest := m[1]
			if strings.Contains(rest, "pr-hero") || strings.Contains(rest, "cl-section") {
				continue
			}
			t.Errorf("%s: <section class=\"site-section%s\"> has no vertical padding — add cl-section", path, rest)
		}

		// The hero lede may use pr-hero-sub. Body copy may not.
		if n := strings.Count(src, "pr-hero-sub"); n > 1 {
			t.Errorf("%s: pr-hero-sub used %d times; it is a hero subtitle with no bottom margin, "+
				"so consecutive paragraphs collide. Use cl-prose for body copy.", path, n)
		}
	}
}
