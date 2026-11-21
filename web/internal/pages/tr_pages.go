package pages

import "github.com/a-h/templ"

// Turkish: the legal texts a tafs user agrees to, at /tr/<page>: KIFF's and tafs's
// privacy policies and terms. Turkish has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("tr", "/privacy", func(OfferLinks) templ.Component { return PrivacyTR() })
	registerTranslation("tr", "/terms", func(OfferLinks) templ.Component { return TermsTR() })
	registerTranslation("tr", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyTR() })
	registerTranslation("tr", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsTR() })
}
