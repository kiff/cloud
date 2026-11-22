package pages

import "github.com/a-h/templ"

// Tamil: the legal texts a tafs user agrees to, at /ta/<page>: KIFF's and tafs's
// privacy policies and terms. Tamil has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("ta", "/privacy", func(OfferLinks) templ.Component { return PrivacyTA() })
	registerTranslation("ta", "/terms", func(OfferLinks) templ.Component { return TermsTA() })
	registerTranslation("ta", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyTA() })
	registerTranslation("ta", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsTA() })
}
