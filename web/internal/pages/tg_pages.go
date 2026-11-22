package pages

import "github.com/a-h/templ"

// Tajik: the legal texts a tafs user agrees to, at /tg/<page>: KIFF's and tafs's
// privacy policies and terms. Tajik has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("tg", "/privacy", func(OfferLinks) templ.Component { return PrivacyTG() })
	registerTranslation("tg", "/terms", func(OfferLinks) templ.Component { return TermsTG() })
	registerTranslation("tg", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyTG() })
	registerTranslation("tg", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsTG() })
}
