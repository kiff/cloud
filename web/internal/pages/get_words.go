package pages

import "strings"

// getWords are the words "Get it" on /tafs and /tafs/download share: the
// computers' fold, the older-Mac link, the watches' state lines, the
// trademark notice and the Google Play badge, in each of the pages' 8
// languages.
type getWords struct {
	// Big and Small are "Get it"'s heading and the line under it, once a
	// computer can download tafs. Small's count follows Play Console's
	// country list: change it when that list changes.
	Big, Small string
	// Fold is the control that opens a computer's version, size and SHA-256.
	Fold string
	// OlderMac is the quiet link under "Get it"'s buttons to the macOS 12 app.
	OlderMac string
	// WearOS and AppleWatch say how each watch will arrive, until it ships:
	// Wear OS on Google Play, Apple Watch inside the iPhone app.
	WearOS, AppleWatch string
	// Notice is the one trademark notice both pages carry: every mark either
	// page names, each said once.
	Notice string
	// PlayBadge is Google's official badge in the page's language, unmodified.
	PlayBadge string
}

// WatchesSub is what Wear OS runs on, under its name: product names, never
// translated.
const WatchesSub = "Galaxy Watch, Pixel Watch"

var getEN = getWords{
	Big:        "On Android and your computer now.",
	Small:      "Android: on Google Play in 33 countries and regions. macOS, Windows and Linux: everywhere.",
	Fold:       "Version, size and SHA-256",
	OlderMac:   "On an older Mac with macOS 12? →",
	WearOS:     "Soon on Google Play",
	AppleWatch: "Comes with the iPhone app",
	Notice: "Google Play, the Google Play logo, Wear OS and Pixel Watch are trademarks of Google LLC. " +
		"Apple, Apple Watch, iPhone, iPad, iPadOS, Mac and macOS are trademarks of Apple Inc., registered in the U.S. and other countries and regions. App Store is a service mark of Apple Inc. " +
		"Galaxy Watch is a trademark of Samsung Electronics Co., Ltd. " +
		"Windows is a trademark of the Microsoft group of companies. " +
		"Linux® is the registered trademark of Linus Torvalds in the U.S. and other countries. Tux by Larry Ewing, made with The GIMP.",
	PlayBadge: "img/download/google-play-badge.png",
}

var getByLang = map[string]getWords{
	"en": getEN,
	"de": {
		Big:        "Jetzt auf Android und deinem Computer.",
		Small:      "Android: bei Google Play in 33 Ländern und Regionen. macOS, Windows und Linux: überall.",
		Fold:       "Version, Größe und SHA-256",
		OlderMac:   "Älterer Mac mit macOS 12? →",
		WearOS:     "Bald bei Google Play",
		AppleWatch: "Kommt mit der iPhone-App",
		Notice: "Google Play, das Google Play-Logo, Wear OS und Pixel Watch sind Marken von Google LLC. " +
			"Apple, Apple Watch, iPhone, iPad, iPadOS, Mac und macOS sind Marken von Apple Inc., die in den USA und weiteren Ländern und Regionen eingetragen sind. App Store ist eine Dienstleistungsmarke von Apple Inc. " +
			"Galaxy Watch ist eine Marke von Samsung Electronics Co., Ltd. " +
			"Windows ist eine Marke der Microsoft-Unternehmensgruppe. " +
			"Linux® ist eine eingetragene Marke von Linus Torvalds in den USA und anderen Ländern. Tux von Larry Ewing, erstellt mit The GIMP.",
		PlayBadge: "img/download/google-play-badge-de.png",
	},
	"es": {
		Big:        "Ya en Android y en tu ordenador.",
		Small:      "Android: en Google Play en 33 países y regiones. macOS, Windows y Linux: en todas partes.",
		Fold:       "Versión, tamaño y SHA-256",
		OlderMac:   "¿Tienes un Mac antiguo con macOS 12? →",
		WearOS:     "Pronto en Google Play",
		AppleWatch: "Llega con la app para iPhone",
		Notice: "Google Play, el logotipo de Google Play, Wear OS y Pixel Watch son marcas comerciales de Google LLC. " +
			"Apple, Apple Watch, iPhone, iPad, iPadOS, Mac y macOS son marcas comerciales de Apple Inc., registradas en EE. UU. y en otros países y regiones. App Store es una marca de servicio de Apple Inc. " +
			"Galaxy Watch es una marca comercial de Samsung Electronics Co., Ltd. " +
			"Windows es una marca comercial del grupo de empresas Microsoft. " +
			"Linux® es una marca registrada de Linus Torvalds en EE. UU. y otros países. Tux, de Larry Ewing, creado con The GIMP.",
		PlayBadge: "img/download/google-play-badge-es.png",
	},
	"fr": {
		Big:        "Disponible sur Android et sur votre ordinateur.",
		Small:      "Android : sur Google Play dans 33 pays et régions. macOS, Windows et Linux : partout.",
		Fold:       "Version, taille et SHA-256",
		OlderMac:   "Un Mac plus ancien sous macOS 12 ? →",
		WearOS:     "Bientôt sur Google Play",
		AppleWatch: "Arrive avec l'app iPhone",
		Notice: "Google Play, le logo Google Play, Wear OS et Pixel Watch sont des marques de Google LLC. " +
			"Apple, Apple Watch, iPhone, iPad, iPadOS, Mac et macOS sont des marques d'Apple Inc., déposées aux États-Unis et dans d'autres pays et régions. App Store est une marque de service d'Apple Inc. " +
			"Galaxy Watch est une marque de Samsung Electronics Co., Ltd. " +
			"Windows est une marque du groupe de sociétés Microsoft. " +
			"Linux® est une marque déposée de Linus Torvalds aux États-Unis et dans d'autres pays. Tux par Larry Ewing, réalisé avec The GIMP.",
		PlayBadge: "img/download/google-play-badge-fr.png",
	},
	"it": {
		Big:        "Già su Android e sul tuo computer.",
		Small:      "Android: su Google Play in 33 paesi e regioni. macOS, Windows e Linux: ovunque.",
		Fold:       "Versione, dimensione e SHA-256",
		OlderMac:   "Hai un Mac meno recente con macOS 12? →",
		WearOS:     "Presto su Google Play",
		AppleWatch: "Arriva con l'app per iPhone",
		Notice: "Google Play, il logo Google Play, Wear OS e Pixel Watch sono marchi di Google LLC. " +
			"Apple, Apple Watch, iPhone, iPad, iPadOS, Mac e macOS sono marchi di Apple Inc., registrati negli Stati Uniti e in altri paesi e regioni. App Store è un marchio di servizio di Apple Inc. " +
			"Galaxy Watch è un marchio di Samsung Electronics Co., Ltd. " +
			"Windows è un marchio del gruppo di società Microsoft. " +
			"Linux® è un marchio registrato di Linus Torvalds negli Stati Uniti e in altri paesi. Tux di Larry Ewing, realizzato con The GIMP.",
		PlayBadge: "img/download/google-play-badge-it.png",
	},
	"nl": {
		Big:        "Nu op Android en op je computer.",
		Small:      "Android: op Google Play in 33 landen en regio's. macOS, Windows en Linux: overal.",
		Fold:       "Versie, grootte en SHA-256",
		OlderMac:   "Oudere Mac met macOS 12? →",
		WearOS:     "Binnenkort op Google Play",
		AppleWatch: "Komt met de iPhone-app",
		Notice: "Google Play, het Google Play-logo, Wear OS en Pixel Watch zijn handelsmerken van Google LLC. " +
			"Apple, Apple Watch, iPhone, iPad, iPadOS, Mac en macOS zijn handelsmerken van Apple Inc., gedeponeerd in de VS en andere landen en regio's. App Store is een dienstmerk van Apple Inc. " +
			"Galaxy Watch is een handelsmerk van Samsung Electronics Co., Ltd. " +
			"Windows is een handelsmerk van de Microsoft-groep. " +
			"Linux® is het gedeponeerde handelsmerk van Linus Torvalds in de VS en andere landen. Tux door Larry Ewing, gemaakt met The GIMP.",
		PlayBadge: "img/download/google-play-badge-nl.png",
	},
	"pl": {
		Big:        "Już na Androidzie i na Twoim komputerze.",
		Small:      "Android: w Google Play w 33 krajach i regionach. macOS, Windows i Linux: wszędzie.",
		Fold:       "Wersja, rozmiar i SHA-256",
		OlderMac:   "Starszy Mac z macOS 12? →",
		WearOS:     "Wkrótce w Google Play",
		AppleWatch: "Pojawi się z aplikacją na iPhone'a",
		Notice: "Google Play, logo Google Play, Wear OS i Pixel Watch są znakami towarowymi Google LLC. " +
			"Apple, Apple Watch, iPhone, iPad, iPadOS, Mac i macOS są znakami towarowymi Apple Inc., zarejestrowanymi w USA oraz w innych krajach i regionach. App Store jest znakiem usługowym Apple Inc. " +
			"Galaxy Watch jest znakiem towarowym Samsung Electronics Co., Ltd. " +
			"Windows jest znakiem towarowym grupy firm Microsoft. " +
			"Linux® jest zastrzeżonym znakiem towarowym Linusa Torvaldsa w USA i innych krajach. Tux autorstwa Larry'ego Ewinga, wykonany w programie The GIMP.",
		PlayBadge: "img/download/google-play-badge-pl.png",
	},
	"pt": {
		Big:        "Já no Android e no seu computador.",
		Small:      "Android: no Google Play em 33 países e regiões. macOS, Windows e Linux: em todo o lado.",
		Fold:       "Versão, tamanho e SHA-256",
		OlderMac:   "Tem um Mac mais antigo com macOS 12? →",
		WearOS:     "Em breve no Google Play",
		AppleWatch: "Chega com a app para iPhone",
		Notice: "Google Play, o logótipo Google Play, Wear OS e Pixel Watch são marcas comerciais da Google LLC. " +
			"Apple, Apple Watch, iPhone, iPad, iPadOS, Mac e macOS são marcas comerciais da Apple Inc., registadas nos EUA e noutros países e regiões. App Store é uma marca de serviço da Apple Inc. " +
			"Galaxy Watch é uma marca comercial da Samsung Electronics Co., Ltd. " +
			"Windows é uma marca comercial do grupo de empresas Microsoft. " +
			"Linux® é a marca registada de Linus Torvalds nos EUA e noutros países. Tux de Larry Ewing, criado com o The GIMP.",
		PlayBadge: "img/download/google-play-badge-pt.png",
	},
}

// getFor is the shared words in lang, English for a language the pages
// don't have.
func getFor(lang string) getWords {
	if w, ok := getByLang[lang]; ok {
		return w
	}
	return getEN
}

// getComputers are the computers "Get it" offers, each only once its file is
// out: macOS 13 and later, Windows and Linux. The macOS 12 app isn't a
// button there: it has the quiet older-Mac link (getOlderMac).
func getComputers() []DesktopApp {
	var out []DesktopApp
	for _, a := range DesktopApps() {
		if a.OS != "macos" && a.URL != "" {
			out = append(out, a)
		}
	}
	return out
}

// getOlderMac says the macOS 12 app's file is out, so "Get it" links it,
// quietly, for the Macs that stay on macOS 12.
func getOlderMac() bool {
	for _, a := range DesktopApps() {
		if a.OS == "macos" && a.URL != "" {
			return true
		}
	}
	return false
}

// olderMac is the older-Mac link's words with the space before its "→",
// French's before its "?", and the one in "macOS 12" made non-breaking, so
// none of them wraps onto a line of its own. The words themselves are
// unchanged.
func (g getWords) olderMac() string {
	s := strings.ReplaceAll(g.OlderMac, "macOS 12", "macOS\u00a012")
	s = strings.ReplaceAll(s, " ?", "\u00a0?")
	if strings.HasSuffix(s, " →") {
		s = strings.TrimSuffix(s, " →") + "\u00a0→"
	}
	return s
}

// shared is the words "Get it" shares with /tafs/download, in the page's
// language.
func (w tafsWords) shared() getWords { return getFor(w.lang()) }

// downloadFor is /tafs/download's "Download for %s" in the page's language,
// cut at the name: part 0 is what comes before it, part 1 what comes after,
// so the button can set the name as a word mark between them.
func (w tafsWords) downloadFor(part int) string {
	d, ok := downloadByLang[w.lang()]
	if !ok || d.DownloadFor == "" {
		d = downloadEN
	}
	pre, post, _ := strings.Cut(d.DownloadFor, "%s")
	return strings.TrimSpace([]string{pre, post}[part])
}
