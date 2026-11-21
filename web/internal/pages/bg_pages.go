package pages

import "github.com/a-h/templ"

// Bulgarian: the legal texts a tafs user agrees to, at /bg/<page>: KIFF's and
// tafs's privacy policies and terms. Bulgarian has these pages only
// (components.HasPage); their links to any other page go to English.
func init() {
	registerTranslation("bg", "/privacy", func(OfferLinks) templ.Component { return PrivacyBG() })
	registerTranslation("bg", "/terms", func(OfferLinks) templ.Component { return TermsBG() })
	registerTranslation("bg", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyBG() })
	registerTranslation("bg", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsBG() })
}
