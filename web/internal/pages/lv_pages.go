package pages

import "github.com/a-h/templ"

// Latvian: the legal texts a tafs user agrees to, at /lv/<page>: KIFF's and
// tafs's privacy policies and terms. Latvian has these pages only
// (components.HasPage); their links to any other page go to English.
func init() {
	registerTranslation("lv", "/privacy", func(OfferLinks) templ.Component { return PrivacyLV() })
	registerTranslation("lv", "/terms", func(OfferLinks) templ.Component { return TermsLV() })
	registerTranslation("lv", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyLV() })
	registerTranslation("lv", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsLV() })
}
