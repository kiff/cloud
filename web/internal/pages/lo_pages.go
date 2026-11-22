package pages

import "github.com/a-h/templ"

// Lao: the legal texts a tafs user agrees to, at /lo/<page>: KIFF's and tafs's
// privacy policies and terms. Lao has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("lo", "/privacy", func(OfferLinks) templ.Component { return PrivacyLO() })
	registerTranslation("lo", "/terms", func(OfferLinks) templ.Component { return TermsLO() })
	registerTranslation("lo", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyLO() })
	registerTranslation("lo", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsLO() })
}
