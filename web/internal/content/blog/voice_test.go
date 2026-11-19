package blog

import (
	"regexp"
	"strings"
	"testing"
)

// Voice checks.
//
// These exist because the posts are drafted by an assistant, and the
// failure mode is not factual error — it is register. Writing that
// stages a bug as a discovery, announces its own insights, and lands a
// polished line every paragraph reads as machine-made, and a reader who
// notices discounts the whole post, including the parts that are true.
//
// A model asked to tidy its own register mostly fails at it, so the
// highest-signal patterns are asserted here instead of left to
// judgement. The list is deliberately short: a lint that fires on good
// writing is worse than no lint, so it covers phrases with no
// legitimate use in a technical post and one density heuristic.
//
// Everything subtler — symmetry, false modesty, resolving every thread —
// stays in skills/publishing as guidance, because it cannot be checked
// without false positives.

// bannedPhrases stage a revelation rather than reporting a finding.
var bannedPhrases = []string{
	"here's where it gets interesting",
	"here's the thing",
	"the kicker",
	"but here's the twist",
	"what's fascinating",
	"what's remarkable",
	"the part that actually matters",
	"the transferable part",
	"the takeaway",
	"let that sink in",
	"plot twist",
	"buckle up",
	"load-bearing",
	"it turns out that",
	"little did i know",
	"spoiler",
	"deep dive",
	"game changer",
	"game-changer",
	"rabbit hole",
}

// reactionPhrases mark a post that frames itself as a response to someone
// else's writing rather than stating kiff's own claim. Per
// skills/publishing/SKILL.md, the source that prompted a post stays in the
// discovery step; the published piece reads as a first-person position, not
// agreement or rebuttal.
var reactionPhrases = []string{
	"a recent post",
	"a recent piece",
	"a recent article",
	"a widely shared post",
	"made the rounds",
	"a post on",
	"a piece on",
	"an article on",
	"someone wrote",
	"someone recently wrote",
	"recently argued",
	"recently made the case",
}

func TestPostsDoNotFrameThemselvesAsReactions(t *testing.T) {
	for _, p := range mustPosts(t) {
		body := strings.ToLower(p.Body + " " + p.Summary)
		for _, phrase := range reactionPhrases {
			if strings.Contains(body, phrase) {
				t.Errorf("%s: contains %q — state the claim as kiff's own, "+
					"don't frame the post as a reaction to a source", p.Slug, phrase)
			}
		}
	}
}

func TestPostsAvoidStagedRevelationPhrases(t *testing.T) {
	for _, p := range mustPosts(t) {
		body := strings.ToLower(p.Body + " " + p.Title + " " + p.Summary)
		for _, phrase := range bannedPhrases {
			if strings.Contains(body, phrase) {
				t.Errorf("%s: contains %q — report the finding instead of staging it",
					p.Slug, phrase)
			}
		}
	}
}

// Em dashes are the strongest single tell in generated prose: the
// balanced parenthetical clause, several times a paragraph. A few are
// fine, which is why the threshold is loose. Sustained use is not.
func TestEmDashDensityIsHuman(t *testing.T) {
	const perThousand = 9.0
	for _, p := range mustPosts(t) {
		words := len(strings.Fields(p.Body))
		if words < 200 {
			continue // too short for the ratio to mean anything
		}
		dashes := strings.Count(p.Body, "—")
		density := float64(dashes) * 1000 / float64(words)
		if density > perThousand {
			t.Errorf("%s: %d em dashes in %d words (%.1f per 1000, limit %.0f) — "+
				"rewrite some as separate sentences",
				p.Slug, dashes, words, density, perThousand)
		}
	}
}

// A heading that promises a reveal is the structural version of the
// same problem. These match whole headings, not substrings, so a
// heading that happens to contain "why" is unaffected.
var theatricalHeading = regexp.MustCompile(`(?i)^#{2,}\s*(the (real|actual|surprising|interesting) \w+|what (i|we) learned|the (plot )?twist|and then|but wait|the reveal|why this matters more than you think)\s*$`)

func TestHeadingsDoNotPromiseAReveal(t *testing.T) {
	for _, p := range mustPosts(t) {
		for _, line := range strings.Split(p.Body, "\n") {
			if theatricalHeading.MatchString(strings.TrimSpace(line)) {
				t.Errorf("%s: heading %q stages a reveal — name the content instead",
					p.Slug, strings.TrimSpace(line))
			}
		}
	}
}
