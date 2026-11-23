package words

import "strings"

// A regional variant is a language's lines for one region where some read differently: pt-BR beside pt, as the apps
// have it ("Regional variants" in the apps' language guide). Its emails are its base's, with its own lines on top: a
// line it doesn't change reads as the base's, never as English. The code sign-in emails, the account emails, the
// footer and a receipt's steps are whole sets in the variant (CodeSignInEmailMore, AccountEmails, EmailFooters and
// ReceiptStepsMore, under its tag); the other emails' lines are only those that differ (EmailVariants).
var variantBase = map[Lang]Lang{"pt-BR": PT}

// EmailVariants are a variant's lines for the emails drawn from Words (EmailMore's names), only where they differ
// from its base's.
var EmailVariants = map[string]EmailLines{}

// variantOf is a tag's regional variant ("pt-BR", "pt_br"), when KIFF writes one.
func variantOf(tag string) (Lang, bool) {
	t := strings.ReplaceAll(strings.TrimSpace(tag), "_", "-")
	for v := range variantBase {
		if strings.EqualFold(t, string(v)) {
			return v, true
		}
	}
	return "", false
}

// VariantBase is the language a variant is drawn over ("pt-BR" → "pt"); false for a language that isn't one.
func VariantBase(l Lang) (Lang, bool) {
	b, ok := variantBase[l]
	return b, ok
}

// variantWords is the base's words with the variant's lines laid over them.
func variantWords(l Lang) *Words {
	base := variantBase[l]
	moreMu.Lock()
	defer moreMu.Unlock()
	if w, ok := moreWords[string(l)]; ok {
		return w
	}
	w := *catalogs[base] // a copy: the base's own lines stay the base's
	setEmailLines(&w, string(l), EmailVariants[string(l)])
	moreWords[string(l)] = &w
	return &w
}
