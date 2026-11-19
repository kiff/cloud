package pages

import "github.com/a-h/templ"

// /tafs in the seven other languages tafs speaks, at /<lang>/tafs: the same
// page, in each language's words.
func TafsDE(o OfferLinks) templ.Component { return tafsPage(tafsDE, o) }
func TafsFR(o OfferLinks) templ.Component { return tafsPage(tafsFR, o) }
func TafsES(o OfferLinks) templ.Component { return tafsPage(tafsES, o) }
func TafsIT(o OfferLinks) templ.Component { return tafsPage(tafsIT, o) }
func TafsPT(o OfferLinks) templ.Component { return tafsPage(tafsPT, o) }
func TafsNL(o OfferLinks) templ.Component { return tafsPage(tafsNL, o) }
func TafsPL(o OfferLinks) templ.Component { return tafsPage(tafsPL, o) }
