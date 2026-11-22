package pages

import "github.com/a-h/templ"

// Czech: the legal texts a tafs user agrees to, at /cs/<page>: KIFF's and
// tafs's privacy policies and terms. Czech has these pages only
// (components.HasPage); their links to any other page go to English.
func init() {
	registerTranslation("cs", "/privacy", func(OfferLinks) templ.Component { return PrivacyCS() })
	registerTranslation("cs", "/terms", func(OfferLinks) templ.Component { return TermsCS() })
	registerTranslation("cs", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyCS() })
	registerTranslation("cs", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsCS() })
}
