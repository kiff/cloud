package pages

import "github.com/a-h/templ"

// Slovak: the legal texts a tafs user agrees to, at /sk/<page>: KIFF's and
// tafs's privacy policies and terms. Slovak has these pages only
// (components.HasPage); their links to any other page go to English.
func init() {
	registerTranslation("sk", "/privacy", func(OfferLinks) templ.Component { return PrivacySK() })
	registerTranslation("sk", "/terms", func(OfferLinks) templ.Component { return TermsSK() })
	registerTranslation("sk", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacySK() })
	registerTranslation("sk", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsSK() })
}
