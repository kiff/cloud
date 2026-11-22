package pages

import "github.com/a-h/templ"

// Khmer: the legal texts a tafs user agrees to, at /km/<page>: KIFF's and tafs's
// privacy policies and terms. Khmer has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("km", "/privacy", func(OfferLinks) templ.Component { return PrivacyKM() })
	registerTranslation("km", "/terms", func(OfferLinks) templ.Component { return TermsKM() })
	registerTranslation("km", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyKM() })
	registerTranslation("km", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsKM() })
}
