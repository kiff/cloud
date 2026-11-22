package pages

import "github.com/a-h/templ"

// Sinhala: the legal texts a tafs user agrees to, at /si/<page>: KIFF's and tafs's
// privacy policies and terms. Sinhala has these pages only (components.HasPage);
// their links to any other page go to English.
func init() {
	registerTranslation("si", "/privacy", func(OfferLinks) templ.Component { return PrivacySI() })
	registerTranslation("si", "/terms", func(OfferLinks) templ.Component { return TermsSI() })
	registerTranslation("si", "/tafs/privacy", func(OfferLinks) templ.Component { return TafsPrivacySI() })
	registerTranslation("si", "/tafs/terms", func(OfferLinks) templ.Component { return TafsTermsSI() })
}
