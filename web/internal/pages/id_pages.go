package pages

import "github.com/a-h/templ"

// Indonesian: the legal texts a tafs user agrees to, at /id/<page>: KIFF's and tafs's
// privacy policies and terms. Indonesian has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("id", "/privacy", func(OfferLinks) templ.Component { return PrivacyID() })
	registerTranslation("id", "/terms", func(OfferLinks) templ.Component { return TermsID() })
	registerTranslation("id", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyID() })
	registerTranslation("id", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsID() })
}
