package pages

// MacSecurityKeyApprovals says whether a Mac without a Secure Enclave can
// approve with a USB security key. The macOS app has the feature built but
// off (Features.macPasskeyApprovals); cmd/web turns this on from
// KIFF_MAC_SECURITY_KEY=on the day the app's switch is on, so the privacy
// pages and /support say it in every language at once. Set it before serving.
var MacSecurityKeyApprovals bool

// macSecurityKeyClause ends "you approve on your phone" with the Mac's
// security key, in lang, while MacSecurityKeyApprovals is on. Each language
// uses the word its privacy pages already use for a security key.
var macSecurityKeyClause = map[string]string{
	"en": ", or, on a Mac, with a security key",
	"de": ", oder auf einem Mac mit einem Sicherheitsschlüssel",
	"es": ", o, en un Mac, con una llave de seguridad",
	"fr": ", ou, sur un Mac, avec une clé de sécurité",
	"it": ", oppure, su un Mac, con una chiave di sicurezza",
	"nl": ", of op een Mac met een beveiligingssleutel",
	"pl": ", a na Macu także kluczem bezpieczeństwa",
	"pt": ", ou, num Mac, com uma chave de segurança",
	"ko": ", 또는 Mac에서는 보안 키로",
	"ja": "、またはMacではセキュリティキーで",
	"et": ", või Macis turvavõtmega",
}

// macSecurityKey is the clause, or nothing while the switch is off.
func macSecurityKey(lang string) string {
	if !MacSecurityKeyApprovals {
		return ""
	}
	return macSecurityKeyClause[lang]
}
