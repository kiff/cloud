package region

// StoreCountries are the countries where each app store sells tafs: what its
// console offers and has selected, read from the console. A store can only sell
// where it has a storefront, so a country we permit but a store doesn't offer
// isn't listed here (Supported, where the service may be used, is wider). Play
// serves some territories under a parent country, as its console lists them;
// those are listed too. CI checks this list against Supported, so a country is
// never offered where the AI isn't. Selecting a country in a console means
// adding it here first, and the test says whether it may be added.
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
		// Wave 2, Group A, 21 Nov 2026 (Haiti and Burundi held out, pending
		// the owner): the 28 Play offers on their own, then the territories it
		// serves under a parent country (United States: AS, GU, MH, MP, PW, PR,
		// VI; France: GF, PF, GP, MQ, YT, NC, RE, BL, MF, PM, WF; Denmark: FO,
		// GL; Finland: AX; Norway: SJ). Play doesn't offer the group's other 27.
		"BO", "DO", "GT", "HN", "PY", "MZ", "GW", "DM", "GD", "TT",
		"FJ", "FM", "PG", "WS", "SB", "TO", "VU", "LR", "NA", "SL",
		"PK", "SR", "GI", "BM", "KY", "VG", "TC", "AW",
		"AS", "GU", "MH", "MP", "PW", "PR", "VI",
		"GF", "PF", "GP", "MQ", "YT", "NC", "RE", "BL", "MF", "PM", "WF",
		"FO", "GL", "AX", "SJ",
		// Wave 2, Group C (owner's yes pending): the 12 of the group Play offers.
		// Play doesn't offer Guyana, St Vincent, Madagascar, Lesotho or Eswatini;
		// Barbados waits on its scope question.
		"BZ", "AG", "BS", "KN", "LC", "GM", "GN", "CG", "KM", "DJ",
		"SC", "MU",
		// Wave 2, the owner's five (22 Nov 2026): the 3 Play offers. Play
		// doesn't offer Barbados or Lesotho.
		"SV", "CM", "SM",
		// Wave 3 (owner's yes, 22 Nov 2026): the Gulf, Jordan, Iraq, Taiwan,
		// Bosnia and Nicaragua, then batch 5's Mongolia, Nepal, Cambodia and
		// Laos. Play doesn't offer Brunei.
		"AE", "QA", "KW", "BH", "OM", "JO", "IQ", "TW", "BA", "NI",
		"MN", "NP", "KH", "LA",
		// Batch 6 (owner's yes, 22 Nov 2026): Sri Lanka, Tajikistan and the
		// Maldives. Play doesn't offer Bhutan.
		"LK", "TJ", "MV",
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
		// Wave 2, Group A, 21 Nov 2026 (Haiti and Burundi held out, pending
		// the owner): the 29 with a storefront. The group's other 48 have none.
		"BO", "DO", "GT", "HN", "PY", "MZ", "GW", "ST", "DM", "GD",
		"TT", "FJ", "FM", "PW", "PG", "SB", "TO", "VU", "LR", "NA",
		"SL", "PK", "SR", "BM", "KY", "VG", "AI", "MS", "TC",
		// Wave 2, Group C (owner's yes pending): the 13 of the group with a
		// storefront. None in Guinea, Comoros, Djibouti or Lesotho; Barbados
		// waits on its scope question.
		"BZ", "GY", "AG", "BS", "KN", "LC", "VC", "GM", "CG", "MG",
		"SC", "MU", "SZ",
		// Wave 2, the owner's five (22 Nov 2026): the 3 with a storefront.
		// None in San Marino or Lesotho.
		"SV", "CM", "BB",
		// Wave 3 (owner's yes, 22 Nov 2026): the same 14 as Google Play, plus
		// Brunei (App Store only).
		"AE", "QA", "KW", "BH", "OM", "JO", "IQ", "TW", "BA", "NI",
		"MN", "NP", "KH", "LA", "BN",
		// Batch 6 (owner's yes, 22 Nov 2026): the same 3 as Google Play, plus
		// Bhutan (App Store only).
		"LK", "TJ", "MV", "BT",
	},
}
