// Package region keeps KIFF to the countries where the AI it runs on is
// offered: a pinned copy of that list, the country of a caller's address, and
// a check on the routes that start a session.
package region

// ListSource is where Supported was copied from, and ListEffective the date
// that page gave. A change to the list is a change here, by hand, with both
// updated: nothing fetches it at run or test time.
const (
	ListSource    = "https://www.anthropic.com/supported-countries"
	ListEffective = "2026-11-12"
)

// Supported holds the ISO 3166-1 alpha-2 codes of the countries and regions on
// that list (185), plus the territories and dependencies of a listed country,
// which the list says are supported too. Ukraine is listed except Crimea,
// Donetsk, Kherson, Luhansk and Zaporizhzhia; a country-level address can't
// tell those apart, so the terms carry that part.
var Supported = set(
	// The list, in its order.
	"AL", "DZ", "AD", "AO", "AG", "AR", "AM", "AU", "AT", "AZ",
	"BS", "BH", "BD", "BB", "BE", "BZ", "BJ", "BT", "BO", "BA",
	"BW", "BR", "BN", "BG", "BF", "BI", "CV", "KH", "CM", "CA",
	"CF", "TD", "CL", "CO", "KM", "CG", "CD", "CR", "CI", "HR",
	"CY", "CZ", "DK", "DJ", "DM", "DO", "EC", "EG", "SV", "GQ",
	"ER", "EE", "SZ", "ET", "FJ", "FI", "FR", "GA", "GM", "GE",
	"DE", "GH", "GR", "GD", "GT", "GN", "GW", "GY", "HT", "HN",
	"HU", "IS", "IN", "ID", "IQ", "IE", "IL", "IT", "JM", "JP",
	"JO", "KZ", "KE", "KI", "KW", "KG", "LA", "LV", "LB", "LS",
	"LR", "LY", "LI", "LT", "LU", "MG", "MW", "MY", "MV", "ML",
	"MT", "MH", "MR", "MU", "MX", "FM", "MD", "MC", "MN", "ME",
	"MA", "MZ", "NA", "NR", "NP", "NL", "NZ", "NI", "NE", "NG",
	"MK", "NO", "OM", "PK", "PW", "PS", "PA", "PG", "PY", "PE",
	"PH", "PL", "PT", "QA", "RO", "RW", "KN", "LC", "VC", "WS",
	"SM", "ST", "SA", "SN", "RS", "SC", "SL", "SG", "SK", "SI",
	"SO", "SB", "ZA", "KR", "SS", "ES", "LK", "SD", "SR", "SE",
	"CH", "TW", "TJ", "TZ", "TH", "TL", "TG", "TO", "TT", "TN",
	"TR", "TM", "TV", "UG", "UA", "AE", "GB", "US", "UY", "UZ",
	"VU", "VA", "VN", "ZM", "ZW",
	// Territories and dependencies of a listed country.
	"PR", "GU", "VI", "AS", "MP", "UM", // United States
	"GI", "IM", "JE", "GG", "BM", "KY", "VG", "AI", "MS", "TC", "FK", "SH", "IO", "GS", "PN", // United Kingdom
	"GF", "GP", "MQ", "RE", "YT", "PM", "BL", "MF", "PF", "NC", "WF", "TF", // France
	"AW", "CW", "SX", "BQ", // Netherlands
	"GL", "FO", // Denmark
	"SJ", "BV", // Norway
	"CX", "CC", "NF", "HM", // Australia
	"TK", // New Zealand
	"AX", // Finland
)

// ListedCount is how many countries the list itself names, before territories.
const ListedCount = 185

// IsSupported reports whether code (ISO 3166-1 alpha-2, upper case) is on the
// list or a listed country's territory.
func IsSupported(code string) bool {
	_, ok := Supported[code]
	return ok
}

func set(codes ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		m[c] = struct{}{}
	}
	return m
}
