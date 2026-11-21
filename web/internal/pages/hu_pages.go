package pages

import "github.com/a-h/templ"

// Hungarian: the legal texts a tafs user agrees to, at /hu/<page>: KIFF's and
// tafs's privacy policies and terms. Hungarian has these pages only
// (components.HasPage); their links to any other page go to English.
func init() {
	registerTranslation("hu", "/privacy", func(OfferLinks) templ.Component { return PrivacyHU() })
	registerTranslation("hu", "/terms", func(OfferLinks) templ.Component { return TermsHU() })
	registerTranslation("hu", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyHU() })
	registerTranslation("hu", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsHU() })
}
