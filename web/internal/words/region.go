package words

import "strings"

// Region is the refusal a sign-in gets where KIFF isn't offered: only in the
// countries and regions where Anthropic's Claude is available.
type Region struct {
	// Here: the country the request comes from isn't one (region.unsupported_here).
	Here string
	// CountryF: the account's stated country isn't one, {country} its name in
	// the page's language (region.unsupported_country).
	CountryF string
}

// Country is the refusal for a stated country, by its name.
func (r Region) Country(name string) string { return strings.ReplaceAll(r.CountryF, "{country}", name) }

var region = map[string]Region{
	"en": {
		Here:     "KIFF isn't available where you are right now. We offer it only in the countries and regions where Anthropic's Claude is available.",
		CountryF: "KIFF isn't available in this country: {country}. We offer it only in the countries and regions where Anthropic's Claude is available.",
	},
	"de": {
		Here:     "KIFF ist dort, wo du gerade bist, nicht verfügbar. Wir bieten es nur in den Ländern und Regionen an, in denen Claude von Anthropic verfügbar ist.",
		CountryF: "KIFF ist in diesem Land nicht verfügbar: {country}. Wir bieten es nur in den Ländern und Regionen an, in denen Claude von Anthropic verfügbar ist.",
	},
	"es": {
		Here:     "KIFF no está disponible donde estás ahora. Solo lo ofrecemos en los países y regiones donde Claude de Anthropic está disponible.",
		CountryF: "KIFF no está disponible en este país: {country}. Solo lo ofrecemos en los países y regiones donde Claude de Anthropic está disponible.",
	},
	"fr": {
		Here:     "KIFF n'est pas disponible là où vous êtes en ce moment. Nous le proposons uniquement dans les pays et régions où Claude d'Anthropic est disponible.",
		CountryF: "KIFF n'est pas disponible dans ce pays : {country}. Nous le proposons uniquement dans les pays et régions où Claude d'Anthropic est disponible.",
	},
	"it": {
		Here:     "KIFF non è disponibile dove ti trovi adesso. Lo offriamo solo nei paesi e nelle regioni in cui Claude di Anthropic è disponibile.",
		CountryF: "KIFF non è disponibile in questo paese: {country}. Lo offriamo solo nei paesi e nelle regioni in cui Claude di Anthropic è disponibile.",
	},
	"nl": {
		Here:     "KIFF is niet beschikbaar waar je nu bent. We bieden het alleen aan in de landen en regio's waar Claude van Anthropic beschikbaar is.",
		CountryF: "KIFF is niet beschikbaar in dit land: {country}. We bieden het alleen aan in de landen en regio's waar Claude van Anthropic beschikbaar is.",
	},
	"pl": {
		Here:     "KIFF nie jest dostępny tam, gdzie teraz jesteś. Oferujemy go tylko w krajach i regionach, w których dostępny jest Claude firmy Anthropic.",
		CountryF: "KIFF nie jest dostępny w tym kraju: {country}. Oferujemy go tylko w krajach i regionach, w których dostępny jest Claude firmy Anthropic.",
	},
	"pt": {
		Here:     "O KIFF não está disponível onde se encontra neste momento. Só o disponibilizamos nos países e regiões onde o Claude da Anthropic está disponível.",
		CountryF: "O KIFF não está disponível neste país: {country}. Só o disponibilizamos nos países e regiões onde o Claude da Anthropic está disponível.",
	},
}

// RegionIn is the refusal in lang, English when it has none.
func RegionIn(lang string) Region {
	if r, ok := region[lang]; ok {
		return r
	}
	return region["en"]
}
