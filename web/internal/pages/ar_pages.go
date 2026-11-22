package pages

import "github.com/a-h/templ"

// Arabic: the legal texts a tafs user agrees to, at /ar/<page>: KIFF's and tafs's
// privacy policies and terms. Arabic has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("ar", "/privacy", func(OfferLinks) templ.Component { return PrivacyAR() })
	registerTranslation("ar", "/terms", func(OfferLinks) templ.Component { return TermsAR() })
	registerTranslation("ar", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyAR() })
	registerTranslation("ar", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsAR() })
}
