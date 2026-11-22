package pages

import "github.com/a-h/templ"

// Georgian: the legal texts a tafs user agrees to, at /ka/<page>: KIFF's and tafs's
// privacy policies and terms. Georgian has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("ka", "/privacy", func(OfferLinks) templ.Component { return PrivacyKA() })
	registerTranslation("ka", "/terms", func(OfferLinks) templ.Component { return TermsKA() })
	registerTranslation("ka", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyKA() })
	registerTranslation("ka", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsKA() })
}
