package pages

import "github.com/a-h/templ"

// Azerbaijani: the legal texts a tafs user agrees to, at /az/<page>: KIFF's and tafs's
// privacy policies and terms. Azerbaijani has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("az", "/privacy", func(OfferLinks) templ.Component { return PrivacyAZ() })
	registerTranslation("az", "/terms", func(OfferLinks) templ.Component { return TermsAZ() })
	registerTranslation("az", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyAZ() })
	registerTranslation("az", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsAZ() })
}
