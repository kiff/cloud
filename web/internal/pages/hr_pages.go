package pages

import "github.com/a-h/templ"

// Croatian: the legal texts a tafs user agrees to, at /hr/<page>: KIFF's and
// tafs's privacy policies and terms. Croatian has these pages only
// (components.HasPage); their links to any other page go to English.
func init() {
	registerTranslation("hr", "/privacy", func(OfferLinks) templ.Component { return PrivacyHR() })
	registerTranslation("hr", "/terms", func(OfferLinks) templ.Component { return TermsHR() })
	registerTranslation("hr", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyHR() })
	registerTranslation("hr", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsHR() })
}
