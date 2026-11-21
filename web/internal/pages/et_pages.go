package pages

import "github.com/a-h/templ"

// Estonian: the legal texts a tafs user agrees to, at /et/<page>: KIFF's and
// tafs's privacy policies and terms. Estonian has these pages only
// (components.HasPage); their links to any other page go to English.
func init() {
	registerTranslation("et", "/privacy", func(OfferLinks) templ.Component { return PrivacyET() })
	registerTranslation("et", "/terms", func(OfferLinks) templ.Component { return TermsET() })
	registerTranslation("et", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyET() })
	registerTranslation("et", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsET() })
}
