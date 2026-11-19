package pages

import "github.com/a-h/templ"

// Portuguese (pt-PT): the pages a tafs user reaches, at /pt/<page>.
func init() {
	registerTranslation("pt", "/acceptable-use", func(OfferLinks) templ.Component { return AcceptableUsePT() })
	registerTranslation("pt", "/subprocessors", func(OfferLinks) templ.Component { return SubprocessorsPT() })
	registerTranslation("pt", "/cookies", func(OfferLinks) templ.Component { return CookiesPT() })
	registerTranslation("pt", "/tafs", func(o OfferLinks) templ.Component { return TafsPT(o) })
	registerTranslation("pt", "/security", func(OfferLinks) templ.Component { return SecurityPT() })
	registerTranslation("pt", "/privacy", func(OfferLinks) templ.Component { return PrivacyPT() })
	registerTranslation("pt", "/terms", func(OfferLinks) templ.Component { return TermsPT() })
	registerTranslation("pt", "/legal/dpa", func(OfferLinks) templ.Component { return DPAPT() })
	registerTranslation("pt", "/support", func(OfferLinks) templ.Component { return SupportPT() })
	registerTranslation("pt", "/tafs/download", func(o OfferLinks) templ.Component { return downloadIn(downloadPT, o) })
	registerTranslation("pt", "/tafs/watch", func(OfferLinks) templ.Component { return Watch(watchPT) })
	registerTranslation("pt", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyPT() })
	registerTranslation("pt", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsPT() })
}
