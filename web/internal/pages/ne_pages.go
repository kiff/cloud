package pages

import "github.com/a-h/templ"

// Nepali: the legal texts a tafs user agrees to, at /ne/<page>: KIFF's and tafs's
// privacy policies and terms. Nepali has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("ne", "/privacy", func(OfferLinks) templ.Component { return PrivacyNE() })
	registerTranslation("ne", "/terms", func(OfferLinks) templ.Component { return TermsNE() })
	registerTranslation("ne", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyNE() })
	registerTranslation("ne", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsNE() })
}
