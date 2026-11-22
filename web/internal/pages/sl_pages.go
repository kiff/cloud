package pages

import "github.com/a-h/templ"

// Slovene: the legal texts a tafs user agrees to, at /sl/<page>: KIFF's and
// tafs's privacy policies and terms. Slovene has these pages only
// (components.HasPage); their links to any other page go to English.
func init() {
	registerTranslation("sl", "/privacy", func(OfferLinks) templ.Component { return PrivacySL() })
	registerTranslation("sl", "/terms", func(OfferLinks) templ.Component { return TermsSL() })
	registerTranslation("sl", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacySL() })
	registerTranslation("sl", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsSL() })
}
