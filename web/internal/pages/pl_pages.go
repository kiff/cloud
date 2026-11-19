package pages

import "github.com/a-h/templ"

// Polish: the pages a tafs user reaches, at /pl/<page>.
func init() {
	registerTranslation("pl", "/tafs", func(o OfferLinks) templ.Component { return TafsPL(o) })
	registerTranslation("pl", "/privacy", func(OfferLinks) templ.Component { return PrivacyPL() })
	registerTranslation("pl", "/acceptable-use", func(OfferLinks) templ.Component { return AcceptableUsePL() })
	registerTranslation("pl", "/subprocessors", func(OfferLinks) templ.Component { return SubprocessorsPL() })
	registerTranslation("pl", "/cookies", func(OfferLinks) templ.Component { return CookiesPL() })
	registerTranslation("pl", "/security", func(OfferLinks) templ.Component { return SecurityPL() })
	registerTranslation("pl", "/terms", func(OfferLinks) templ.Component { return TermsPL() })
	registerTranslation("pl", "/legal/dpa", func(OfferLinks) templ.Component { return DPAPL() })
	registerTranslation("pl", "/support", func(OfferLinks) templ.Component { return SupportPL() })
	registerTranslation("pl", "/tafs/download", func(o OfferLinks) templ.Component { return downloadIn(downloadPL, o) })
	registerTranslation("pl", "/tafs/watch", func(OfferLinks) templ.Component { return Watch(watchPL) })
	registerTranslation("pl", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyPL() })
	registerTranslation("pl", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsPL() })
}
