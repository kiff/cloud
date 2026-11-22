package pages

import "github.com/a-h/templ"

// Dhivehi: the legal texts a tafs user agrees to, at /dv/<page>: KIFF's and tafs's
// privacy policies and terms. Dhivehi has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("dv", "/privacy", func(OfferLinks) templ.Component { return PrivacyDV() })
	registerTranslation("dv", "/terms", func(OfferLinks) templ.Component { return TermsDV() })
	registerTranslation("dv", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyDV() })
	registerTranslation("dv", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsDV() })
}
