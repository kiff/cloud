package pages

import "github.com/a-h/templ"

// Urdu: the legal texts a tafs user agrees to, at /ur/<page>: KIFF's and tafs's
// privacy policies and terms. Urdu has these pages only (components.HasPage);
// their links to any other page go to English. Written right to left.
func init() {
	registerTranslation("ur", "/privacy", func(OfferLinks) templ.Component { return PrivacyUR() })
	registerTranslation("ur", "/terms", func(OfferLinks) templ.Component { return TermsUR() })
	registerTranslation("ur", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyUR() })
	registerTranslation("ur", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsUR() })
}
