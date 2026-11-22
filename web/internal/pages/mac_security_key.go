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
	"lv": " vai Mac datorā ar drošības atslēgu",
	"el": ", ή, σε Mac, με κλειδί ασφαλείας",
	"lt": ", arba Mac kompiuteryje – saugos raktu",
	"cs": ", nebo na Macu bezpečnostním klíčem",
	"sk": " alebo na Macu bezpečnostným kľúčom",
	"sl": " ali na Macu z varnostnim ključem",
	"hu": ", vagy Macen biztonsági kulccsal",
	"ro": " sau, pe un Mac, cu o cheie de securitate",
	"bg": ", или на Mac – с ключ за сигурност",
	"hr": ", ili, na Macu, sigurnosnim ključem",
	"tr": ", ya da bir Mac üzerinde bir güvenlik anahtarıyla",
	"id": ", atau di Mac, dengan kunci keamanan",
	"vi": ", hoặc trên Mac, bằng khóa bảo mật",
	"th": " หรือบน Mac ด้วยกุญแจความปลอดภัย",
	"zh": "，或在 Mac 上使用安全金鑰",
	"ar": "، أو على Mac بمفتاح أمان",
	"sr": ", ili, na Mac-u, sigurnosnim ključem",
	"sq": ", ose, në një Mac, me një çelës sigurie",
	"ka": ", ან Mac-ზე, უსაფრთხოების გასაღებით",
	"hy": ", կամ Mac-ում՝ անվտանգության բանալիով",
	"az": ", ya da Mac-də təhlükəsizlik açarı ilə",
	"bn": ", অথবা Mac-এ, সিকিউরিটি কী দিয়ে",
	"he": ", או ב-Mac, עם מפתח אבטחה",
	"ca": ", o, en un Mac, amb una clau de seguretat",
}

// macSecurityKey is the clause, or nothing while the switch is off.
func macSecurityKey(lang string) string {
	if !MacSecurityKeyApprovals {
		return ""
	}
	return macSecurityKeyClause[lang]
}
