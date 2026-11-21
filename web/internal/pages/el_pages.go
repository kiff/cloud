package pages

import "github.com/a-h/templ"

// Greek: the legal texts a tafs user agrees to, at /el/<page>: KIFF's and
// tafs's privacy policies and terms. Greek has these pages only
// (components.HasPage); their links to any other page go to English.
func init() {
	registerTranslation("el", "/privacy", func(OfferLinks) templ.Component { return PrivacyEL() })
	registerTranslation("el", "/terms", func(OfferLinks) templ.Component { return TermsEL() })
	registerTranslation("el", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyEL() })
	registerTranslation("el", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsEL() })
}
