package region

import (
	"net/http"
	"strconv"
	"strings"
)

// Here is region.unsupported_here, the refusal when the caller's address is
// outside the supported countries, in the 8 languages plus Korean and Japanese
// (kiff/cloud's web/legal-drafts/region.md). The apps show it as sent.
var Here = map[string]string{
	"en": "KIFF isn't available where you are right now. We offer it only in the countries and regions where Anthropic's Claude is available.",
	"de": "KIFF ist dort, wo du gerade bist, nicht verfügbar. Wir bieten es nur in den Ländern und Regionen an, in denen Claude von Anthropic verfügbar ist.",
	"es": "KIFF no está disponible donde estás ahora. Solo lo ofrecemos en los países y regiones donde Claude de Anthropic está disponible.",
	"fr": "KIFF n'est pas disponible là où vous êtes en ce moment. Nous le proposons uniquement dans les pays et régions où Claude d'Anthropic est disponible.",
	"it": "KIFF non è disponibile dove ti trovi adesso. Lo offriamo solo nei paesi e nelle regioni in cui Claude di Anthropic è disponibile.",
	"nl": "KIFF is niet beschikbaar waar je nu bent. We bieden het alleen aan in de landen en regio's waar Claude van Anthropic beschikbaar is.",
	"pl": "KIFF nie jest dostępny tam, gdzie teraz jesteś. Oferujemy go tylko w krajach i regionach, w których dostępny jest Claude firmy Anthropic.",
	"pt": "O KIFF não está disponível onde se encontra neste momento. Só o disponibilizamos nos países e regiões onde o Claude da Anthropic está disponível.",
	"ko": "지금 계신 곳에서는 KIFF를 이용할 수 없습니다. KIFF는 Anthropic의 Claude를 이용할 수 있는 국가와 지역에서만 제공됩니다.",
	"ja": "現在地ではKIFFをご利用いただけません。KIFFは、AnthropicのClaudeが利用できる国と地域でのみ提供しています。",
}

// Language is the request's Accept-Language tag with the highest q that has
// words here (the earlier on a tie; "pt-BR" counts as "pt"), else English.
func Language(r *http.Request) string {
	best, bestQ := "en", 0.0
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		code, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		if _, ok := Here[code]; !ok {
			continue
		}
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				continue
			}
			q = f
		}
		if q > bestQ {
			best, bestQ = code, q
		}
	}
	return best
}
