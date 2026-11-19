package pages

import "github.com/a-h/templ"

// Italian: the pages a tafs user reaches, at /it/<page>.
func init() {
	registerTranslation("it", "/tafs", func(o OfferLinks) templ.Component { return TafsIT(o) })
	registerTranslation("it", "/privacy", func(OfferLinks) templ.Component { return PrivacyIT() })
	registerTranslation("it", "/terms", func(OfferLinks) templ.Component { return TermsIT() })
	registerTranslation("it", "/acceptable-use", func(OfferLinks) templ.Component { return AcceptableUseIT() })
	registerTranslation("it", "/security", func(OfferLinks) templ.Component { return SecurityIT() })
	registerTranslation("it", "/subprocessors", func(OfferLinks) templ.Component { return SubprocessorsIT() })
	registerTranslation("it", "/cookies", func(OfferLinks) templ.Component { return CookiesIT() })
	registerTranslation("it", "/legal/dpa", func(OfferLinks) templ.Component { return DPAIT() })
	registerTranslation("it", "/support", func(OfferLinks) templ.Component { return SupportIT() })
	registerTranslation("it", "/tafs/download", func(o OfferLinks) templ.Component { return downloadIn(downloadIT, o) })
	registerTranslation("it", "/tafs/watch", func(OfferLinks) templ.Component { return Watch(watchIT) })
	registerTranslation("it", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyIT() })
	registerTranslation("it", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsIT() })
}
