package pages

import "github.com/a-h/templ"

// Romanian: the legal texts a tafs user agrees to, at /ro/<page>: KIFF's and
// tafs's privacy policies and terms. Romanian has these pages only
// (components.HasPage); their links to any other page go to English.
func init() {
	registerTranslation("ro", "/privacy", func(OfferLinks) templ.Component { return PrivacyRO() })
	registerTranslation("ro", "/terms", func(OfferLinks) templ.Component { return TermsRO() })
	registerTranslation("ro", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyRO() })
	registerTranslation("ro", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsRO() })
}
