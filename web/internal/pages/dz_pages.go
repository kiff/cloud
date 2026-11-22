package pages

import "github.com/a-h/templ"

// Dzongkha: the legal texts a tafs user agrees to, at /dz/<page>: KIFF's and tafs's
// privacy policies and terms. Dzongkha has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("dz", "/privacy", func(OfferLinks) templ.Component { return PrivacyDZ() })
	registerTranslation("dz", "/terms", func(OfferLinks) templ.Component { return TermsDZ() })
	registerTranslation("dz", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyDZ() })
	registerTranslation("dz", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsDZ() })
}
