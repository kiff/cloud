package pages

import "github.com/a-h/templ"

// Hindi: the legal texts a tafs user agrees to, at /hi/<page>: KIFF's and tafs's
// privacy policies and terms. Hindi has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("hi", "/privacy", func(OfferLinks) templ.Component { return PrivacyHI() })
	registerTranslation("hi", "/terms", func(OfferLinks) templ.Component { return TermsHI() })
	registerTranslation("hi", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyHI() })
	registerTranslation("hi", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsHI() })
}
