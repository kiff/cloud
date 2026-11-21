package pages

import "github.com/a-h/templ"

// Vietnamese: the legal texts a tafs user agrees to, at /vi/<page>: KIFF's and tafs's
// privacy policies and terms. Vietnamese has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("vi", "/privacy", func(OfferLinks) templ.Component { return PrivacyVI() })
	registerTranslation("vi", "/terms", func(OfferLinks) templ.Component { return TermsVI() })
	registerTranslation("vi", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyVI() })
	registerTranslation("vi", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsVI() })
}
