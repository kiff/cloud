package pages

import "github.com/a-h/templ"

// Traditional Chinese: the legal texts a tafs user agrees to, at /zh/<page>: KIFF's and tafs's
// privacy policies and terms. Traditional Chinese has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("zh", "/privacy", func(OfferLinks) templ.Component { return PrivacyZH() })
	registerTranslation("zh", "/terms", func(OfferLinks) templ.Component { return TermsZH() })
	registerTranslation("zh", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacyZH() })
	registerTranslation("zh", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsZH() })
}
