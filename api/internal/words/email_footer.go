package words

// EmailFooter is the three lines every KIFF email ends with, beside the
// company's name and address (which are not translated): where to read the
// Privacy page, where to get help, and that KIFF never asks for a password or a
// code. The footer is mandatory on every email (the email design guide).
//
// English is the source. EmailFooters holds the other languages, keyed by the
// primary language tag, as legal-i18n writes them; a language with no entry reads
// English, and the layout then sets the footer left to right whatever the email's
// own direction is, so English never sits in a right-to-left line.
type EmailFooter struct {
	// Privacy and Help are the names of the two links.
	Privacy, Help string
	// NeverAsk is the sentence: KIFF will never ask for your password or code.
	NeverAsk string
}

var emailFooterEN = EmailFooter{
	Privacy:  "Privacy",
	Help:     "Help",
	NeverAsk: "KIFF will never ask for your password or code.",
}

// EmailFooters are the other languages' footers, by primary language tag.
var EmailFooters = map[string]EmailFooter{}

// EmailFooterFor is the footer's lines in lang, and whether lang has its own
// (false: English, for a language not written yet).
func EmailFooterFor(lang Lang) (EmailFooter, bool) {
	if lang == EN || lang == "" {
		return emailFooterEN, true
	}
	if f, ok := EmailFooters[string(lang)]; ok {
		return f, true
	}
	return emailFooterEN, false
}
