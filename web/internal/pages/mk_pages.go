package pages

import "github.com/a-h/templ"

// Macedonian: the legal texts a tafs user agrees to, at /mk/<page>: KIFF's and tafs's
// privacy policies and terms. Macedonian has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("mk", "/privacy", func(OfferLinks) templ.Component { return PrivacyMK() })
	registerTranslation("mk", "/terms", func(OfferLinks) templ.Component { return TermsMK() })
	registerTranslation("mk", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyMK() })
	registerTranslation("mk", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsMK() })
}
