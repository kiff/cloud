package pages

import "github.com/a-h/templ"

// Turkmen: the legal texts a tafs user agrees to, at /tk/<page>: KIFF's and tafs's
// privacy policies and terms. Turkmen has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("tk", "/privacy", func(OfferLinks) templ.Component { return PrivacyTK() })
	registerTranslation("tk", "/terms", func(OfferLinks) templ.Component { return TermsTK() })
	registerTranslation("tk", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyTK() })
	registerTranslation("tk", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsTK() })
}
