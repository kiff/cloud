package pages

import "github.com/a-h/templ"

// Mongolian: the legal texts a tafs user agrees to, at /mn/<page>: KIFF's and tafs's
// privacy policies and terms. Mongolian has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("mn", "/privacy", func(OfferLinks) templ.Component { return PrivacyMN() })
	registerTranslation("mn", "/terms", func(OfferLinks) templ.Component { return TermsMN() })
	registerTranslation("mn", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyMN() })
	registerTranslation("mn", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsMN() })
}
