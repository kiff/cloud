package pages

import "github.com/a-h/templ"

// Serbian: the legal texts a tafs user agrees to, at /sr/<page>: KIFF's and tafs's
// privacy policies and terms. Serbian has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("sr", "/privacy", func(OfferLinks) templ.Component { return PrivacySR() })
	registerTranslation("sr", "/terms", func(OfferLinks) templ.Component { return TermsSR() })
	registerTranslation("sr", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacySR() })
	registerTranslation("sr", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsSR() })
}
