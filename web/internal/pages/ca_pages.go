package pages

import "github.com/a-h/templ"

// Catalan: the legal texts a tafs user agrees to, at /ca/<page>: KIFF's and tafs's
// privacy policies and terms. Catalan has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("ca", "/privacy", func(OfferLinks) templ.Component { return PrivacyCA() })
	registerTranslation("ca", "/terms", func(OfferLinks) templ.Component { return TermsCA() })
	registerTranslation("ca", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyCA() })
	registerTranslation("ca", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsCA() })
}
