package pages

import "github.com/a-h/templ"

// German: the pages a tafs user reaches, at /de/<page>.
func init() {
	registerTranslation("de", "/tafs", func(o OfferLinks) templ.Component { return TafsDE(o) })
	registerTranslation("de", "/privacy", func(OfferLinks) templ.Component { return PrivacyDE() })
	registerTranslation("de", "/acceptable-use", func(OfferLinks) templ.Component { return AcceptableUseDE() })
	registerTranslation("de", "/cookies", func(OfferLinks) templ.Component { return CookiesDE() })
	registerTranslation("de", "/security", func(OfferLinks) templ.Component { return SecurityDE() })
	registerTranslation("de", "/subprocessors", func(OfferLinks) templ.Component { return SubprocessorsDE() })
	registerTranslation("de", "/terms", func(OfferLinks) templ.Component { return TermsDE() })
	registerTranslation("de", "/legal/dpa", func(OfferLinks) templ.Component { return DPADE() })
	registerTranslation("de", "/support", func(OfferLinks) templ.Component { return SupportDE() })
	registerTranslation("de", "/tafs/download", func(o OfferLinks) templ.Component { return downloadIn(downloadDE, o) })
	registerTranslation("de", "/tafs/watch", func(OfferLinks) templ.Component { return Watch(watchDE) })
	registerTranslation("de", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyDE() })
	registerTranslation("de", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsDE() })
}
