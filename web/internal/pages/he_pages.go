package pages

import "github.com/a-h/templ"

// Hebrew: the legal texts a tafs user agrees to, at /he/<page>: KIFF's and tafs's
// privacy policies and terms. Hebrew has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("he", "/privacy", func(OfferLinks) templ.Component { return PrivacyHE() })
	registerTranslation("he", "/terms", func(OfferLinks) templ.Component { return TermsHE() })
	registerTranslation("he", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyHE() })
	registerTranslation("he", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsHE() })
}
