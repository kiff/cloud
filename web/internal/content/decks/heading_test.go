package decks

import (
	"regexp"
	"strings"
	"testing"
)

// Slide headings are capped by character width, not by word count.
//
// The sales deck sets h1 to max-width 8.5ch at up to 6.4rem and h2 to
// 11ch at 3.35rem, so a heading wraps roughly every seven or eleven
// characters. A 49-character h1 became eight lines of display type and
// ran off the bottom of a slide that cannot scroll — .deck is a fixed
// 100vh grid, so overflow clips rather than scrolls.
//
// Word count alone missed this: the slide was inside the safe word
// budget and still unreadable. Headings get their own limit.
func TestSlideHeadingsFitTheSlide(t *testing.T) {
	// Measured against headings that render correctly, with margin.
	// h1 is the tighter cap because its max-width is smaller and its
	// font-size larger.
	const (
		maxH1 = 34
		maxH2 = 56
	)
	head := regexp.MustCompile(`(?m)^(#{1,2})\s+(.+)$`)
	br := regexp.MustCompile(`<br\s*/?>`)

	entries, err := slidesFS.ReadDir("slides")
	if err != nil {
		t.Fatalf("read slides dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no slide decks found — the guard would pass vacuously")
	}
	for _, e := range entries {
		src, err := slidesFS.ReadFile("slides/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		// Slides are separated by a thematic break on its own line,
		// the same split the renderer uses.
		for i, raw := range strings.Split(string(src), "\n---\n") {
			m := head.FindStringSubmatch(raw)
			if m == nil {
				continue
			}
			// <br> controls where lines break but does not shorten the
			// heading, so it is measured as a space.
			text := strings.TrimSpace(br.ReplaceAllString(m[2], " "))
			limit := maxH2
			level := "h2"
			if len(m[1]) == 1 {
				limit, level = maxH1, "h1"
			}
			if len(text) > limit {
				t.Errorf("%s slide %d: %s is %d chars (limit %d) and will overflow a fixed-height slide:\n  %q",
					e.Name(), i+1, level, len(text), limit, text)
			}
		}
	}
}
