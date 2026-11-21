package pages

import "github.com/a-h/templ"

// Japanese: the legal pages only, at /ja/<page>. Every other page stays
// English for a Japanese reader, and its language row lists no Japanese.
func init() {
	registerTranslation("ja", "/privacy", func(OfferLinks) templ.Component { return PrivacyJA() })
	registerTranslation("ja", "/terms", func(OfferLinks) templ.Component { return TermsJA() })
	registerTranslation("ja", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyJA() })
	registerTranslation("ja", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsJA() })
}
