package region

// StoreCountries are the countries each app store offers tafs in, copied from
// the store's console. The consoles are the truth; this copy is what CI checks
// against Supported, so a country is never offered where the AI isn't. Adding a
// country in a console means adding it here first, and the test says whether
// it may be added.
var StoreCountries = map[string][]string{
	// Google Play, production track (the Wear track inherits it), 21 Nov 2026:
	// the EU, Iceland, Liechtenstein, Norway, the United Kingdom, Switzerland
	// and South Korea, plus Wave 1: the United States, Canada, Mexico, Panama,
	// Brazil, Argentina, Chile, Peru, Colombia, Australia, New Zealand,
	// Singapore, Malaysia, India and South Africa.
	"google-play": {
		"AT", "BE", "BG", "HR", "CY", "CZ", "DK", "EE", "FI", "FR",
		"DE", "GR", "HU", "IE", "IT", "LV", "LT", "LU", "MT", "NL",
		"PL", "PT", "RO", "SK", "SI", "ES", "SE",
		"IS", "LI", "NO", "GB", "CH", "KR",
		"US", "CA", "MX", "PA", "BR", "AR", "CL", "PE", "CO",
		"AU", "NZ", "SG", "MY", "IN", "ZA",
		// Wave 2, Group A, 21 Nov 2026: 30 countries, then 47 territories of
		// listed countries. Haiti and Burundi are held out (sanctions regimes, as
		// the Central African Republic and DR Congo are), pending the owner.
		"BO", "DO", "GT", "HN", "PY", "GQ", "MZ", "GW", "ST", "TL",
		"DM", "GD", "TT", "FJ", "KI", "MH", "FM", "NR", "PW", "PG",
		"WS", "SB", "TO", "TV", "VU", "LR", "NA", "SL", "PK", "SR",
		"PR", "GU", "VI", "AS", "MP", "UM", "GI", "IM", "JE", "GG",
		"BM", "KY", "VG", "AI", "MS", "TC", "FK", "SH", "IO", "GS",
		"PN", "GF", "GP", "MQ", "RE", "YT", "PM", "BL", "MF", "PF",
		"NC", "WF", "TF", "AW", "CW", "SX", "BQ", "CX", "CC", "NF",
		"HM", "TK", "SJ", "BV", "GL", "FO", "AX",
	},
	// The App Store, iOS and macOS, 21 Nov 2026: the EU, Iceland, Norway,
	// Switzerland and the United Kingdom (no storefront in Liechtenstein; Korea
	// isn't selected), plus the same 15 Wave 1 countries as Google Play.
	"app-store": {
		"AT", "BE", "BG", "HR", "CY", "CZ", "DK", "EE", "FI", "FR",
		"DE", "GR", "HU", "IE", "IT", "LV", "LT", "LU", "MT", "NL",
		"PL", "PT", "RO", "SK", "SI", "ES", "SE",
		"IS", "NO", "GB", "CH",
		"US", "CA", "MX", "PA", "BR", "AR", "CL", "PE", "CO",
		"AU", "NZ", "SG", "MY", "IN", "ZA",
		// Wave 2, Group A, 21 Nov 2026: 30 countries, then 47 territories of
		// listed countries. Haiti and Burundi are held out (sanctions regimes, as
		// the Central African Republic and DR Congo are), pending the owner.
		"BO", "DO", "GT", "HN", "PY", "GQ", "MZ", "GW", "ST", "TL",
		"DM", "GD", "TT", "FJ", "KI", "MH", "FM", "NR", "PW", "PG",
		"WS", "SB", "TO", "TV", "VU", "LR", "NA", "SL", "PK", "SR",
		"PR", "GU", "VI", "AS", "MP", "UM", "GI", "IM", "JE", "GG",
		"BM", "KY", "VG", "AI", "MS", "TC", "FK", "SH", "IO", "GS",
		"PN", "GF", "GP", "MQ", "RE", "YT", "PM", "BL", "MF", "PF",
		"NC", "WF", "TF", "AW", "CW", "SX", "BQ", "CX", "CC", "NF",
		"HM", "TK", "SJ", "BV", "GL", "FO", "AX",
	},
}
