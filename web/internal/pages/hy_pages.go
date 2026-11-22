package pages

import "github.com/a-h/templ"

// Armenian: the legal texts a tafs user agrees to, at /hy/<page>: KIFF's and tafs's
// privacy policies and terms. Armenian has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("hy", "/privacy", func(OfferLinks) templ.Component { return PrivacyHY() })
	registerTranslation("hy", "/terms", func(OfferLinks) templ.Component { return TermsHY() })
	registerTranslation("hy", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyHY() })
	registerTranslation("hy", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsHY() })
}
