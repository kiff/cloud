package pages

import "github.com/a-h/templ"

// Korean: the legal texts a tafs user agrees to, at /ko/<page>: KIFF's and
// tafs's privacy policies and terms. Korean has these pages only
// (components.HasPage); their links to any other page go to English.
func init() {
	registerTranslation("ko", "/privacy", func(OfferLinks) templ.Component { return PrivacyKO() })
	registerTranslation("ko", "/terms", func(OfferLinks) templ.Component { return TermsKO() })
	registerTranslation("ko", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyKO() })
	registerTranslation("ko", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsKO() })
}
