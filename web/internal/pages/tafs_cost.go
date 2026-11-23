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
	case "en", "ko", "ja", "th", "zh", "ar", "he", "mn", "ne", "si", "ta", "dv", "dz", "hi":
		return fmt.Sprintf("€%s.%02d", w.number(whole), frac)
	case "nl":
		return fmt.Sprintf("€ %s,%02d", w.number(whole), frac)
	case "id", "lo":
		return fmt.Sprintf("€%s,%02d", w.number(whole), frac)
	case "km":
		return fmt.Sprintf("%s.%02d€", w.number(whole), frac)
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
	case "bg":
		sep = " "
	case "hr":
		sep = "."
	case "dz":
		sep = ","
	case "dv":
		sep = ","
	case "tg":
		sep = "\u00a0"
	case "tk":
		sep = "\u00a0"
	case "ta":
		sep = ","
	case "hi":
		sep = ","
	case "si":
		sep = ","
	case "lo":
		sep = "."
	case "km":
		sep = ","
	case "ne":
		sep = ","
	case "mn":
		sep = ","
	case "mk":
		sep = "."
	case "ca":
		sep = "."
	case "he":
		sep = ","
	case "bn":
		sep = "."
	case "az":
		sep = "."
	case "hy":
		sep = "."
	case "ka":
		sep = "."
	case "sq":
		sep = "."
	case "sr":
		sep = "."
	case "ar":
		sep = ","
	case "zh":
		sep = ","
	case "th":
		sep = ","
	case "vi":
		sep = "."
	case "id":
		sep = "."
	case "tr":
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

// tafsCostBG writes the prices tafs's terms state in Bulgarian: "12,50 €",
// "1 500". Bulgarian has no /tafs page, so it has no footer line of its own.
var tafsCostBG = tafsCostWords{Lang: "bg"}

// tafsCostHR writes the prices tafs's terms state in Croatian: "12,50 €",
// "1.500". Croatian has no /tafs page, so it has no footer line of its own.
var tafsCostHR = tafsCostWords{Lang: "hr"}

// tafsCostTR writes the prices tafs's terms state in Turkish: "12,50 €", "1.500". It has no /tafs page, so no footer line of its own.
var tafsCostTR = tafsCostWords{Lang: "tr"}

// tafsCostID writes the prices tafs's terms state in Indonesian: "€12,50", "1.500". It has no /tafs page, so no footer line of its own.
var tafsCostID = tafsCostWords{Lang: "id"}

// tafsCostVI writes the prices tafs's terms state in Vietnamese: "12,50 €", "1.500". It has no /tafs page, so no footer line of its own.
var tafsCostVI = tafsCostWords{Lang: "vi"}

// tafsCostTH writes the prices tafs's terms state in Thai: "€12.50", "1,500". It has no /tafs page, so no footer line of its own.
var tafsCostTH = tafsCostWords{Lang: "th"}

// tafsCostZH writes the prices tafs's terms state in Traditional Chinese: "€12.50", "1,500". It has no /tafs page, so no footer line of its own.
var tafsCostZH = tafsCostWords{Lang: "zh"}

// tafsCostAR writes the prices tafs's terms state in Arabic: "€12.50", "1,500". It has no /tafs page, so no footer line of its own.
var tafsCostAR = tafsCostWords{Lang: "ar"}

// tafsCostSR writes the prices tafs's terms state in Serbian: "12,50 €", "1.500". It has no /tafs page, so no footer line of its own.
var tafsCostSR = tafsCostWords{Lang: "sr"}

// tafsCostSQ writes the prices tafs's terms state in Albanian: "12,50 €", "1.500". It has no /tafs page, so no footer line of its own.
var tafsCostSQ = tafsCostWords{Lang: "sq"}

// tafsCostKA writes the prices tafs's terms state in Georgian: "12,50 €", "1.500". It has no /tafs page, so no footer line of its own.
var tafsCostKA = tafsCostWords{Lang: "ka"}

// tafsCostHY writes the prices tafs's terms state in Armenian: "12,50 €", "1.500". It has no /tafs page, so no footer line of its own.
var tafsCostHY = tafsCostWords{Lang: "hy"}

// tafsCostAZ writes the prices tafs's terms state in Azerbaijani: "12,50 €", "1.500". It has no /tafs page, so no footer line of its own.
var tafsCostAZ = tafsCostWords{Lang: "az"}

// tafsCostBN writes the prices tafs's terms state in Bengali: "12,50 €", "1.500". It has no /tafs page, so no footer line of its own.
var tafsCostBN = tafsCostWords{Lang: "bn"}

// tafsCostHE writes the prices tafs's terms state in Hebrew: "€12.50", "1,500". It has no /tafs page, so no footer line of its own.
var tafsCostHE = tafsCostWords{Lang: "he"}

// tafsCostCA writes the prices tafs's terms state in Catalan: "12,50 €", "1.500". It has no /tafs page, so no footer line of its own.
var tafsCostCA = tafsCostWords{Lang: "ca"}

// tafsCostMK writes the prices tafs's terms state in Macedonian: "12,50 €", "1.500". It has no /tafs page, so no footer line of its own.
var tafsCostMK = tafsCostWords{Lang: "mk"}

// tafsCostMN writes the prices tafs's terms state in Mongolian: "€12.50", "1,500". It has no /tafs page, so no footer line of its own.
var tafsCostMN = tafsCostWords{Lang: "mn"}

// tafsCostNE writes the prices tafs's terms state in Nepali: "€12.50", "1,500". It has no /tafs page, so no footer line of its own.
var tafsCostNE = tafsCostWords{Lang: "ne"}

// tafsCostKM writes the prices tafs's terms state in Khmer: "12.50€", "1,500". It has no /tafs page, so no footer line of its own.
var tafsCostKM = tafsCostWords{Lang: "km"}

// tafsCostLO writes the prices tafs's terms state in Lao: "€12,50", "1.500". It has no /tafs page, so no footer line of its own.
var tafsCostLO = tafsCostWords{Lang: "lo"}

// tafsCostSI writes the prices tafs's terms state in Sinhala: "€12.50", "1,500". It has no /tafs page, so no footer line of its own.
var tafsCostSI = tafsCostWords{Lang: "si"}

// tafsCostTA writes the prices tafs's terms state in Tamil: "€12.50", "1,500". It has no /tafs page, so no footer line of its own.
var tafsCostTA = tafsCostWords{Lang: "ta"}

// tafsCostTG writes the prices tafs's terms state in Tajik: "12,50 €", "1 500". It has no /tafs page, so no footer line of its own.
var tafsCostTG = tafsCostWords{Lang: "tg"}

// tafsCostTK writes the prices tafs's terms state in Turkmen: "12,50 €", "1 500". It has no /tafs page, so no footer line of its own.
var tafsCostTK = tafsCostWords{Lang: "tk"}

// tafsCostHI writes the prices tafs's terms state in Hindi: "€12.50", "1,500". It has no /tafs page, so no footer line of its own.
var tafsCostHI = tafsCostWords{Lang: "hi"}

// tafsCostDV writes the prices tafs's terms state in Dhivehi: "€12.50", "1,500". It has no /tafs page, so no footer line of its own.
var tafsCostDV = tafsCostWords{Lang: "dv"}

// tafsCostDZ writes the prices tafs's terms state in Dzongkha: "€12.50", "1,500". It has no /tafs page, so no footer line of its own.
var tafsCostDZ = tafsCostWords{Lang: "dz"}
