package handlers

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

// The taf's own words about a call: what it's about, as the owner knows it
// ("Order 1042"), and why ("arrived broken, photo attached"). The gateway
// sends them from its kiff_about and kiff_reason arguments. They are kept
// with a held call for its owner to read, labelled as the taf's, and take
// no part in the decision, the fingerprint or the proposal's identity: a
// retry worded differently is the same call. They may come from a
// customer's text, so each is kept as one plain line, cut to its cap.
const (
	tafAboutMax  = 120
	tafReasonMax = 280
)

type tafWords struct{ about, reason string }

// tafText is one of the taf's words as a request carries it: they only
// show to the owner, so a value that isn't text is dropped and the call
// goes on, never refused over it.
type tafText string

func (t *tafText) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*t = tafText(s)
	}
	return nil
}

type tafWordsKey struct{}

// withTafWords has the call ctx decides carry the taf's words, made plain.
func withTafWords(ctx context.Context, about, reason string) context.Context {
	w := tafWords{about: tafLine(about, tafAboutMax), reason: tafLine(reason, tafReasonMax)}
	if w == (tafWords{}) {
		return ctx
	}
	return context.WithValue(ctx, tafWordsKey{}, w)
}

func tafWordsFrom(ctx context.Context) tafWords {
	w, _ := ctx.Value(tafWordsKey{}).(tafWords)
	return w
}

// emailAddress is an email address in the taf's words. KIFF's record is
// long-lived, and a business reference never needs one: it's taken out.
var emailAddress = regexp.MustCompile(`[^\s@<>()\[\]"',;:]+@[^\s@<>()\[\]"',;:]+\.[\p{L}]{2,}`)

// phoneNumber is a phone number in the taf's words: "+", then at least 8
// digits, with spaces, dashes, dots or brackets between them. Taken out as
// an email is. An order or invoice number has no "+", so it stays.
var phoneNumber = regexp.MustCompile(`\+\s*\d(?:[\s\-.()]*\d){7,}`)

// tafLine is s as one plain line: bidi formatting characters, which could
// reorder what the owner reads, are dropped, controls and line breaks are
// spaces, runs of space one, an email address or a phone number "…", and it is cut to max
// runes.
func tafLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Bidi_Control, r) {
			return -1
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = emailAddress.ReplaceAllString(s, "…")
	s = phoneNumber.ReplaceAllString(s, "…")
	return cut(strings.Join(strings.Fields(s), " "), max)
}
