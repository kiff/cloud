package pages

import "github.com/a-h/templ"

// French: the pages a tafs user reaches, at /fr/<page>.
func init() {
	registerTranslation("fr", "/tafs", func(o OfferLinks) templ.Component { return TafsFR(o) })
	registerTranslation("fr", "/privacy", func(OfferLinks) templ.Component { return PrivacyFR() })
	registerTranslation("fr", "/terms", func(OfferLinks) templ.Component { return TermsFR() })
	registerTranslation("fr", "/acceptable-use", func(OfferLinks) templ.Component { return AcceptableUseFR() })
	registerTranslation("fr", "/cookies", func(OfferLinks) templ.Component { return CookiesFR() })
	registerTranslation("fr", "/security", func(OfferLinks) templ.Component { return SecurityFR() })
	registerTranslation("fr", "/subprocessors", func(OfferLinks) templ.Component { return SubprocessorsFR() })
	registerTranslation("fr", "/legal/dpa", func(OfferLinks) templ.Component { return DPAFR() })
	registerTranslation("fr", "/support", func(OfferLinks) templ.Component { return SupportFR() })
	registerTranslation("fr", "/tafs/download", func(o OfferLinks) templ.Component { return downloadIn(downloadFR, o) })
	registerTranslation("fr", "/tafs/watch", func(OfferLinks) templ.Component { return Watch(watchFR) })
	registerTranslation("fr", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyFR() })
	registerTranslation("fr", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsFR() })
}
