package pages

import "github.com/a-h/templ"

// Thai: the legal texts a tafs user agrees to, at /th/<page>: KIFF's and tafs's
// privacy policies and terms. Thai has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("th", "/privacy", func(OfferLinks) templ.Component { return PrivacyTH() })
	registerTranslation("th", "/terms", func(OfferLinks) templ.Component { return TermsTH() })
	registerTranslation("th", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyTH() })
	registerTranslation("th", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsTH() })
}
