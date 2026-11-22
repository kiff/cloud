package pages

import "github.com/a-h/templ"

// Bengali: the legal texts a tafs user agrees to, at /bn/<page>: KIFF's and tafs's
// privacy policies and terms. Bengali has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("bn", "/privacy", func(OfferLinks) templ.Component { return PrivacyBN() })
	registerTranslation("bn", "/terms", func(OfferLinks) templ.Component { return TermsBN() })
	registerTranslation("bn", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyBN() })
	registerTranslation("bn", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsBN() })
}
