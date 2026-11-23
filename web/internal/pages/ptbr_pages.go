package pages

import "github.com/a-h/templ"

// Brazilian Portuguese (pt-BR), a regional variant of pt: the legal texts a tafs user agrees to, at /pt-br/<page>:
// KIFF's and tafs's privacy policies and terms. Every other page is pt's (components.LangPath sends a link there),
// never English's.
func init() {
	registerTranslation("pt-br", "/privacy", func(OfferLinks) templ.Component { return PrivacyPTBR() })
	registerTranslation("pt-br", "/terms", func(OfferLinks) templ.Component { return TermsPTBR() })
	registerTranslation("pt-br", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyPTBR() })
	registerTranslation("pt-br", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsPTBR() })
}
