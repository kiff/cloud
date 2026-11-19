package pages

import "github.com/a-h/templ"

// Spanish: the pages a tafs user reaches, at /es/<page>.
func init() {
	registerTranslation("es", "/acceptable-use", func(OfferLinks) templ.Component { return AcceptableUseES() })
	registerTranslation("es", "/subprocessors", func(OfferLinks) templ.Component { return SubprocessorsES() })
	registerTranslation("es", "/cookies", func(OfferLinks) templ.Component { return CookiesES() })
	registerTranslation("es", "/tafs", func(o OfferLinks) templ.Component { return TafsES(o) })
	registerTranslation("es", "/security", func(OfferLinks) templ.Component { return SecurityES() })
	registerTranslation("es", "/privacy", func(OfferLinks) templ.Component { return PrivacyES() })
	registerTranslation("es", "/terms", func(OfferLinks) templ.Component { return TermsES() })
	registerTranslation("es", "/legal/dpa", func(OfferLinks) templ.Component { return DPAES() })
	registerTranslation("es", "/support", func(OfferLinks) templ.Component { return SupportES() })
	registerTranslation("es", "/tafs/download", func(o OfferLinks) templ.Component { return downloadIn(downloadES, o) })
	registerTranslation("es", "/tafs/watch", func(OfferLinks) templ.Component { return Watch(watchES) })
	registerTranslation("es", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyES() })
	registerTranslation("es", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsES() })
}
