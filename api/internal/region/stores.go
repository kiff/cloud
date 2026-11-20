package region

// StoreCountries are the countries each app store offers tafs in, copied from
// the store's console. The consoles are the truth; this copy is what CI checks
// against Supported, so a country is never offered where the AI isn't. Adding a
// country in a console means adding it here first, and the test says whether
// it may be added.
var StoreCountries = map[string][]string{
	// Google Play, production track (the Wear track inherits it), 20 Nov 2026:
	// the EU, Iceland, Liechtenstein, Norway, the United Kingdom, Switzerland
	// and South Korea.
	"google-play": {
		"AT", "BE", "BG", "HR", "CY", "CZ", "DK", "EE", "FI", "FR",
		"DE", "GR", "HU", "IE", "IT", "LV", "LT", "LU", "MT", "NL",
		"PL", "PT", "RO", "SK", "SI", "ES", "SE",
		"IS", "LI", "NO", "GB", "CH", "KR",
	},
	// The App Store, iOS and macOS, 20 Nov 2026: the EU, Iceland, Norway,
	// Switzerland and the United Kingdom (no storefront in Liechtenstein; Korea
	// isn't selected).
	"app-store": {
		"AT", "BE", "BG", "HR", "CY", "CZ", "DK", "EE", "FI", "FR",
		"DE", "GR", "HU", "IE", "IT", "LV", "LT", "LU", "MT", "NL",
		"PL", "PT", "RO", "SK", "SI", "ES", "SE",
		"IS", "NO", "GB", "CH",
	},
}
