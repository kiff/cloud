package pages

import "github.com/a-h/templ"

// Dutch: the pages a tafs user reaches, at /nl/<page>.
func init() {
	registerTranslation("nl", "/tafs", func(o OfferLinks) templ.Component { return TafsNL(o) })
	registerTranslation("nl", "/privacy", func(OfferLinks) templ.Component { return PrivacyNL() })
	registerTranslation("nl", "/terms", func(OfferLinks) templ.Component { return TermsNL() })
	registerTranslation("nl", "/acceptable-use", func(OfferLinks) templ.Component { return AcceptableUseNL() })
	registerTranslation("nl", "/cookies", func(OfferLinks) templ.Component { return CookiesNL() })
	registerTranslation("nl", "/security", func(OfferLinks) templ.Component { return SecurityNL() })
	registerTranslation("nl", "/subprocessors", func(OfferLinks) templ.Component { return SubprocessorsNL() })
	registerTranslation("nl", "/legal/dpa", func(OfferLinks) templ.Component { return DPANL() })
	registerTranslation("nl", "/support", func(OfferLinks) templ.Component { return SupportNL() })
	registerTranslation("nl", "/tafs/download", func(o OfferLinks) templ.Component { return downloadIn(downloadNL, o) })
	registerTranslation("nl", "/tafs/watch", func(OfferLinks) templ.Component { return Watch(watchNL) })
	registerTranslation("nl", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyNL() })
	registerTranslation("nl", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsNL() })
}
