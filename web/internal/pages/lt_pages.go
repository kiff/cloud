package pages

import "github.com/a-h/templ"

// Lithuanian: the legal texts a tafs user agrees to, at /lt/<page>: KIFF's
// and tafs's privacy policies and terms. Lithuanian has these pages only
// (components.HasPage); their links to any other page go to English.
func init() {
	registerTranslation("lt", "/privacy", func(OfferLinks) templ.Component { return PrivacyLT() })
	registerTranslation("lt", "/terms", func(OfferLinks) templ.Component { return TermsLT() })
	registerTranslation("lt", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyLT() })
	registerTranslation("lt", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsLT() })
}
