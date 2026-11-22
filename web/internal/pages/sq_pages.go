package pages

import "github.com/a-h/templ"

// Albanian: the legal texts a tafs user agrees to, at /sq/<page>: KIFF's and tafs's
// privacy policies and terms. Albanian has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("sq", "/privacy", func(OfferLinks) templ.Component { return PrivacySQ() })
	registerTranslation("sq", "/terms", func(OfferLinks) templ.Component { return TermsSQ() })
	registerTranslation("sq", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacySQ() })
	registerTranslation("sq", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsSQ() })
}
