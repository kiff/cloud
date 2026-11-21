package pages

import (
	"fmt"
	"strings"
)

// The prices /tafs states and the models it names, in one place: a price
// changes here, once, for all eight languages.

// Today's prices, in euro cents, VAT included: Solo's month and its credits,
// and each pack of credits by its size. The same as the plans show.
const (
	TafsSoloCents   = 1250
	TafsSoloCredits = 500
)

// TafsPackCents is each pack of credits' price, by its credits.
var TafsPackCents = map[int]int{500: 1149, 1500: 2999, 5000: 8499}

// The models tafs run on, as kiff/tafs' definitions name them: a taf's work
// (tafdef.ts' MODEL) and Coach (setupdef.ts' SETUP_MODEL). Named as facts,
// where the cost is explained: no logo, no claim about Anthropic.
const (
	TafsWorkModel  = "Claude Sonnet 5.5"
	TafsCoachModel = "Claude Sonnet 5.5"
)

// tafsCostWords write money and numbers as one language does, with the line
// that says KIFF has no tie to Anthropic.
type tafsCostWords struct {
	// Lang writes money and numbers: "en", "de", "fr", "es", "it", "pt",
	// "nl", "pl", "ko" or "ja".
	Lang string
	// NotAffiliated closes the page's footer, after "Part of Claude for
	// Startups.": KIFF has no tie to Anthropic, said in plain words.
	NotAffiliated string
}

// money writes cents as the language does: "€42.49", "42,49 €", "€ 42,49".
func (w tafsCostWords) money(cents int) string {
	whole, frac := cents/100, cents%100
	switch w.Lang {
	case "en", "ko", "ja":
		return fmt.Sprintf("€%s.%02d", w.number(whole), frac)
	case "nl":
		return fmt.Sprintf("€ %s,%02d", w.number(whole), frac)
	}
	return fmt.Sprintf("%s,%02d €", w.number(whole), frac)
}

// number writes n with the language's thousands mark: "2,000", "2.000",
// "2 000", or none ("2000"), as its page already writes the packs.
func (w tafsCostWords) number(n int) string {
	sep := ""
	switch w.Lang {
	case "en", "ko", "ja":
		sep = ","
	case "de", "es", "it", "nl":
		sep = "."
	case "fr":
		sep = " "
	case "lv":
		sep = " "
	case "el":
		sep = "."
	case "cs":
		sep = " "
	case "sl":
		sep = "."
	case "ro":
		sep = "."
	}
	s := fmt.Sprint(n)
	if sep == "" || len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	return s + sep + strings.Join(parts, sep)
}

var tafsCostEN = tafsCostWords{Lang: "en", NotAffiliated: "KIFF is not affiliated with or endorsed by Anthropic."}
var tafsCostDE = tafsCostWords{Lang: "de", NotAffiliated: "KIFF steht in keiner Verbindung zu Anthropic und wird von Anthropic nicht unterstützt."}
var tafsCostFR = tafsCostWords{Lang: "fr", NotAffiliated: "KIFF n'est ni affilié à Anthropic ni approuvé par Anthropic."}
var tafsCostES = tafsCostWords{Lang: "es", NotAffiliated: "KIFF no está afiliado a Anthropic ni cuenta con su respaldo."}
var tafsCostIT = tafsCostWords{Lang: "it", NotAffiliated: "KIFF non è affiliato ad Anthropic né approvato da Anthropic."}
var tafsCostPT = tafsCostWords{Lang: "pt", NotAffiliated: "O KIFF não é afiliado à Anthropic nem aprovado por ela."}
var tafsCostNL = tafsCostWords{Lang: "nl", NotAffiliated: "KIFF is niet verbonden aan Anthropic en wordt niet door Anthropic onderschreven."}
var tafsCostPL = tafsCostWords{Lang: "pl", NotAffiliated: "KIFF nie jest powiązany z firmą Anthropic ani przez nią popierany."}

// tafsCostKO writes the prices tafs's terms state in Korean. Korean has no
// /tafs page, so it has no footer line of its own.
var tafsCostKO = tafsCostWords{Lang: "ko"}

// tafsCostJA writes the prices tafs's terms state in Japanese. Japanese has
// no /tafs page, so it has no footer line of its own.
var tafsCostJA = tafsCostWords{Lang: "ja"}

// tafsCostET writes the prices tafs's terms state in Estonian: "12,50 €".
// Estonian has no /tafs page, so it has no footer line of its own.
var tafsCostET = tafsCostWords{Lang: "et"}

// tafsCostLV writes the prices tafs's terms state in Latvian, as "12,50 €".
// Latvian has no /tafs page, so it has no footer line of its own.
var tafsCostLV = tafsCostWords{Lang: "lv"}

// tafsCostEL writes the prices tafs's terms state in Greek: "12,50 €",
// "1.500". Greek has no /tafs page, so it has no footer line of its own.
var tafsCostEL = tafsCostWords{Lang: "el"}

// tafsCostLT writes the prices tafs's terms state in Lithuanian: "12,50 €",
// money's default, and the credits as they stand ("500"). Lithuanian has no
// /tafs page, so it has no footer line of its own.
var tafsCostLT = tafsCostWords{Lang: "lt"}

// tafsCostCS writes the prices tafs's terms state in Czech: "12,50 €",
// "1 500". Czech has no /tafs page, so it has no footer line of its own.
var tafsCostCS = tafsCostWords{Lang: "cs"}

// tafsCostSK writes the prices tafs's terms state in Slovak: "12,50 €",
// money's default, and the credits as they stand ("500"). Slovak has no
// /tafs page, so it has no footer line of its own.
var tafsCostSK = tafsCostWords{Lang: "sk"}

// tafsCostSL writes the prices tafs's terms state in Slovene: "12,50 €",
// "1.500". Slovene has no /tafs page, so it has no footer line of its own.
var tafsCostSL = tafsCostWords{Lang: "sl"}

// tafsCostHU writes the prices tafs's terms state in Hungarian: "12,50 €",
// money's default, and the credits as they stand ("500"): Hungarian groups
// no four-digit number. Hungarian has no /tafs page, so it has no footer
// line of its own.
var tafsCostHU = tafsCostWords{Lang: "hu"}

// tafsCostRO writes the prices tafs's terms state in Romanian: "12,50 €",
// "1.500". Romanian has no /tafs page, so it has no footer line of its own.
var tafsCostRO = tafsCostWords{Lang: "ro"}
