package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

var googleSections = map[string]templ.Component{
	"en": tafsPrivacyGoogle(), "de": tafsPrivacyGoogleDE(), "es": tafsPrivacyGoogleES(), "fr": tafsPrivacyGoogleFR(),
	"it": tafsPrivacyGoogleIT(), "nl": tafsPrivacyGoogleNL(), "pl": tafsPrivacyGooglePL(), "pt": tafsPrivacyGooglePT(),
	"ko": tafsPrivacyGoogleKO(), "ja": tafsPrivacyGoogleJA(),
	"et": tafsPrivacyGoogleET(),
	"lv": tafsPrivacyGoogleLV(),
	"el": tafsPrivacyGoogleEL(),
	"lt": tafsPrivacyGoogleLT(),
	"cs": tafsPrivacyGoogleCS(),
	"sk": tafsPrivacyGoogleSK(),
	"sl": tafsPrivacyGoogleSL(),
	"hu": tafsPrivacyGoogleHU(),
	"ro": tafsPrivacyGoogleRO(),
	"bg": tafsPrivacyGoogleBG(),
	"hr": tafsPrivacyGoogleHR(),
	"ar": tafsPrivacyGoogleAR(),
	"zh": tafsPrivacyGoogleZH(),
	"th": tafsPrivacyGoogleTH(),
	"vi": tafsPrivacyGoogleVI(),
	"id": tafsPrivacyGoogleID(),
	"tr": tafsPrivacyGoogleTR(),
}

func renderGoogleSection(t *testing.T, lang string) string {
	t.Helper()
	var b bytes.Buffer
	if err := googleSections[lang].Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(b.String(), "&#39;", "'")
}

const limitedUse = "Google API Services User Data Policy</a>, including the Limited Use requirements."

// While no Google app is in-house, §6 keeps the managed text it has always
// had: Gmail as the example, the sign-in partner, and no scopes.
func TestGoogleSectionStaysManagedWhileNoGoogleAppIsInHouse(t *testing.T) {
	saved := InHouseApps
	defer func() { InHouseApps = saved }()
	InHouseApps = []string{"Stripe"}
	for lang := range googleSections {
		s := renderGoogleSection(t, lang)
		if !strings.Contains(s, "Gmail") || strings.Contains(s, "googleapis.com/auth") || strings.Contains(s, "AWS KMS") {
			t.Errorf("%s: §6 changed with no Google app in-house", lang)
		}
	}
}

// Once Google apps are in-house and none is managed, §6 names each one's scopes, says planque
// keeps the token in the EU and that no other company makes the call, keeps
// Limited Use word for word, and no longer speaks of Gmail or a partner.
func TestGoogleSectionDescribesTheInHouseApps(t *testing.T) {
	saved, savedManaged := InHouseApps, ManagedApps
	defer func() { InHouseApps, ManagedApps = saved, savedManaged }()
	ManagedApps = nil
	InHouseApps = []string{"Google Sheets"}
	for lang := range googleSections {
		s := renderGoogleSection(t, lang)
		for _, want := range []string{"Google Sheets", "auth/spreadsheets</code>", "AWS KMS", "planque", limitedUse} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: §6 is missing %q", lang, want)
			}
		}
		// planque asks for every scope in an app's row at once (registration()
		// refuses any other set), so §6 never says a read-only sign-in exists.
		for _, not := range []string{"Gmail", "auth/calendar", "spreadsheets.readonly", "read-only access when", "doesn't connect"} {
			if strings.Contains(s, not) {
				t.Errorf("%s: §6 names %q with only Sheets in-house", lang, not)
			}
		}
	}
	InHouseApps = []string{"Google Sheets", "Google Calendar"}
	for lang := range googleSections {
		s := renderGoogleSection(t, lang)
		if strings.Count(s, "googleapis.com/auth/") != 3 || !strings.Contains(s, "auth/calendar.events.readonly</code>") {
			t.Errorf("%s: §6 doesn't list the three scopes", lang)
		}
	}
}

// Every scope says what it allows in every language.
func TestEveryGoogleScopeSaysWhatItAllowsInEveryLanguage(t *testing.T) {
	for app, scopes := range googleAppScopes {
		for _, s := range scopes {
			for lang := range googleSections {
				if s.What[lang] == "" {
					t.Errorf("%s %s: no words in %s", app, s.ID, lang)
				}
			}
		}
	}
}

// nonGoogleInHouse are the in-house apps that aren't Google's, so §6 rightly
// leaves them out. Add an app here when it goes in-house.
var nonGoogleInHouse = map[string]bool{"Stripe": true}

// unknownInHouse returns each name googleInHouse would skip in silence: not a
// Google app with registered scopes, and not a known non-Google app.
func unknownInHouse(names []string) []string {
	var bad []string
	for _, n := range names {
		if _, google := googleAppScopes[n]; !google && !nonGoogleInHouse[n] {
			bad = append(bad, n)
		}
	}
	return bad
}

// A misspelled name in the shipped inhouse_apps.json ("Sheets", "Google
// sheets") would keep §6 on the managed text with no error, and Google's
// reviewers would read a page that doesn't match the in-house sign-in. So
// every name in the embedded file must be known, and named once.
func TestShippedInHouseAppsAreKnownNames(t *testing.T) {
	names := mustAppNames(inHouseAppsJSON)
	if bad := unknownInHouse(names); len(bad) > 0 {
		t.Errorf("inhouse_apps.json names unknown apps %q: use a key of googleAppScopes or nonGoogleInHouse", bad)
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("inhouse_apps.json names %q twice", n)
		}
		seen[n] = true
	}
}

// The check itself catches the typos it exists for.
func TestUnknownInHouseCatchesTypos(t *testing.T) {
	for _, typo := range []string{"Sheets", "Google sheets", "Google Sheets ", "Google Calender", "stripe"} {
		if bad := unknownInHouse([]string{typo}); len(bad) != 1 {
			t.Errorf("%q: not caught", typo)
		}
	}
	if bad := unknownInHouse([]string{"Google Sheets", "Google Calendar", "Stripe"}); len(bad) != 0 {
		t.Errorf("known names refused: %q", bad)
	}
}

// The route each Google app connects by, in each language: the label of its
// item, and the sentence only that route makes true. While a Google app is
// managed, "no other company makes the call" is said only under tafs's own
// sign-in, never of Google data as a whole.
var googleRouteWords = map[string]struct{ Managed, InHouse, NoOtherCompany string }{
	"en": {"Through our sign-in partner:", "Through tafs's own Google sign-in:", "so no other company makes the call or holds your sign-in."},
	"de": {"Über unseren Anmeldepartner:", "Über die eigene Google-Anmeldung von tafs:", "also führt kein anderes Unternehmen den Aufruf aus oder bewahrt deine Anmeldung auf."},
	"es": {"A través de nuestro socio de inicio de sesión:", "A través del propio inicio de sesión de Google de tafs:", "así que ninguna otra empresa hace la llamada ni guarda tu inicio de sesión."},
	"fr": {"Par notre partenaire d'authentification :", "Par l'authentification Google propre à tafs :", "aucune autre société n'effectue l'appel ni ne conserve votre authentification."},
	"it": {"Tramite il nostro partner per gli accessi:", "Tramite l'accesso Google di tafs stesso:", "quindi nessun'altra società fa la chiamata o custodisce il tuo accesso."},
	"nl": {"Via onze inlogpartner:", "Via de eigen Google-login van tafs:", "dus geen ander bedrijf doet de aanroep of bewaart je login."},
	"pl": {"Przez naszego partnera do logowania:", "Przez własne logowanie Google tafs:", "więc żadna inna firma nie wykonuje wywołania ani nie przechowuje Twojego logowania."},
	"pt": {"Através do nosso parceiro de início de sessão:", "Através do próprio início de sessão Google do tafs:", "por isso nenhuma outra empresa faz a chamada nem guarda o seu início de sessão."},
	"ko": {"로그인 파트너를 통하는 경우:", "tafs 자체 Google 로그인을 통하는 경우:", "다른 어떤 회사도 호출을 하거나 로그인 정보를 보관하지 않습니다."},
	"ja": {"サインインパートナーを通じて：", "tafs自身のGoogleサインインを通じて：", "他のどの会社も呼び出しを行わず、あなたのサインインを保持することもありません。"},
	"et": {"Meie sisselogimispartneri kaudu:", "tafsi enda Google'i sisselogimise kaudu:", "nii et ükski teine ettevõte ei tee päringut ega hoia teie sisselogimist."},
	"lv": {"Caur mūsu pierakstīšanās partneri:", "Caur paša tafs Google pierakstīšanos:", "tāpēc neviens cits uzņēmums neveic izsaukumu un neglabā jūsu pierakstīšanos."},
	"el": {"Μέσω του συνεργάτη σύνδεσής μας:", "Μέσω της δικής του σύνδεσης Google του tafs:", "οπότε καμία άλλη εταιρεία δεν κάνει την κλήση ούτε κρατά τη σύνδεσή σας."},
	"lt": {"Per mūsų prisijungimo partnerį:", "Per paties tafs Google prisijungimą:", "todėl jokia kita įmonė neatlieka užklausos ir nesaugo jūsų prisijungimo."},
	"cs": {"Prostřednictvím našeho partnera pro přihlašování:", "Prostřednictvím vlastního přihlášení Google aplikace tafs:", "takže volání neprovádí ani vaše přihlášení neuchovává žádná jiná společnost."},
	"sk": {"Prostredníctvom nášho partnera pre prihlasovanie:", "Prostredníctvom vlastného prihlásenia Google v tafs:", "takže žiadna iná spoločnosť nevykonáva volanie ani neuchováva vaše prihlásenie."},
	"sl": {"Prek našega partnerja za prijavo:", "Prek lastne prijave Google aplikacije tafs:", "zato klica ne izvede in vaše prijave ne hrani nobeno drugo podjetje."},
	"hu": {"Bejelentkezési partnerünkön keresztül:", "A tafs saját Google-bejelentkezésén keresztül:", "így más vállalat nem indítja a hívást, és nem őrzi az Ön bejelentkezését."},
	"ro": {"Prin partenerul nostru de autentificare:", "Prin autentificarea Google proprie a tafs:", "astfel încât nicio altă companie nu efectuează apelul și nu păstrează autentificarea dumneavoastră."},
	"bg": {"Чрез нашия партньор за вход:", "Чрез собствения вход с Google на tafs:", "така че никоя друга компания не извършва заявката и не съхранява вашия вход."},
	"hr": {"Putem našeg partnera za prijavu:", "Putem tafsove vlastite Google prijave:", "pa nijedno drugo poduzeće ne upućuje poziv niti čuva vašu prijavu."},
	"ar": {"عبر شريكنا في تسجيل الدخول:", "عبر تسجيل دخول Google الخاص بـ tafs نفسه:", "فلا تجري أي شركة أخرى الاستدعاء أو تحتفظ بتسجيل دخولك."},
	"zh": {"透過我們的登入夥伴：", "透過 tafs 自己的 Google 登入：", "因此沒有其他公司進行該呼叫或保管您的登入。"},
	"th": {"ผ่านพาร์ตเนอร์การลงชื่อเข้าใช้ของเรา:", "ผ่านการลงชื่อเข้าใช้ Google ของ tafs เอง:", "ดังนั้นไม่มีบริษัทอื่นทำการเรียกหรือเก็บการลงชื่อเข้าใช้ของคุณ"},
	"vi": {"Qua đối tác đăng nhập của chúng tôi:", "Qua chính phương thức đăng nhập Google của tafs:", "nên không công ty nào khác thực hiện lệnh gọi hay giữ thông tin đăng nhập của bạn."},
	"id": {"Melalui mitra login kami:", "Melalui login Google milik tafs sendiri:", "sehingga tidak ada perusahaan lain yang membuat panggilan atau menyimpan login Anda."},
	"tr": {"Giriş ortağımız üzerinden:", "tafs'ın kendi Google girişi üzerinden:", "böylece çağrıyı başka hiçbir şirket yapmaz ve girişinizi tutmaz."},
}

// itemAt returns the <li> that holds s[i]: from the nearest "<li>" before it.
func itemAt(s string, i int) string {
	start := strings.LastIndex(s[:i], "<li>")
	if start < 0 {
		return ""
	}
	return s[start:i]
}

// With Google apps on both routes, §6 names each app under its route, keeps
// both routes' words, and says "no other company" only inside tafs's own
// sign-in's item.
func TestGoogleSectionNamesEachRouteWhileBothAreUsed(t *testing.T) {
	saved, savedManaged := InHouseApps, ManagedApps
	defer func() { InHouseApps, ManagedApps = saved, savedManaged }()
	ManagedApps = []string{"Gmail", "Google Calendar", "Google Sheets", "Slack"}
	InHouseApps = []string{"Google Sheets"}
	for lang := range googleSections {
		s := renderGoogleSection(t, lang)
		w := googleRouteWords[lang]
		for _, want := range []string{": Gmail, Google Calendar, Google Sheets.</li>", ": Google Sheets.</li>", "auth/spreadsheets</code>", "AWS KMS", w.Managed, w.InHouse, limitedUse} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: §6 is missing %q", lang, want)
			}
		}
		if strings.Contains(s, "Slack") {
			t.Errorf("%s: §6 names a managed app that isn't Google's", lang)
		}
		n := strings.Count(s, w.NoOtherCompany)
		if n == 0 {
			t.Errorf("%s: §6 lost tafs's own sign-in's \"no other company\"", lang)
		}
		for i, from := 0, 0; i < n; i++ {
			at := from + strings.Index(s[from:], w.NoOtherCompany)
			if item := itemAt(s, at); !strings.HasPrefix(item, "<li><strong>"+w.InHouse+"</strong>") {
				t.Errorf("%s: \"no other company\" is said outside tafs's own sign-in's item: %q", lang, item)
			}
			from = at + 1
		}
	}
}

// Today's state, managed Google apps and none in-house: §6 names them under
// the sign-in partner, covers Gmail, Calendar and Sheets, and never says "no
// other company".
func TestGoogleSectionNamesTheManagedGoogleApps(t *testing.T) {
	saved, savedManaged := InHouseApps, ManagedApps
	defer func() { InHouseApps, ManagedApps = saved, savedManaged }()
	ManagedApps = []string{"Gmail", "Google Calendar", "Google Sheets"}
	InHouseApps = nil
	for lang := range googleSections {
		s := renderGoogleSection(t, lang)
		w := googleRouteWords[lang]
		if !strings.Contains(s, ": Gmail, Google Calendar, Google Sheets.</li>") || strings.Contains(s, w.NoOtherCompany) || strings.Contains(s, w.InHouse) || strings.Contains(s, "googleapis.com/auth") {
			t.Errorf("%s: §6 doesn't describe the managed Google apps alone", lang)
		}
		if strings.Count(s, "Google Calendar") < 2 || strings.Count(s, "Google Sheets") < 2 {
			t.Errorf("%s: §6 doesn't cover Calendar and Sheets beside the list", lang)
		}
	}
}

// §6's "What it reads and writes" describes these Google apps and no other.
// A Google app added to managed_apps.json or inhouse_apps.json fails here
// until §6 says what tafs does with it, in every language.
var googleAppsSection6Describes = map[string]bool{"Gmail": true, "Google Calendar": true, "Google Sheets": true, "Google Tasks": true}

func TestEveryListedGoogleAppIsDescribedInSection6(t *testing.T) {
	for _, list := range [][]string{ManagedApps, InHouseApps} {
		for _, a := range list {
			if isGoogleApp(a) && !googleAppsSection6Describes[a] {
				t.Errorf("%s is listed but /tafs/privacy §6 doesn't describe it: add its words first", a)
			}
		}
	}
}

// Google Tasks' sentence shows only while Tasks is listed.
func TestGoogleTasksIsDescribedOnlyWhileListed(t *testing.T) {
	saved, savedManaged := InHouseApps, ManagedApps
	defer func() { InHouseApps, ManagedApps = saved, savedManaged }()
	InHouseApps = nil
	for _, listed := range []bool{false, true} {
		ManagedApps = []string{"Gmail", "Google Calendar", "Google Sheets"}
		if listed {
			ManagedApps = append(ManagedApps, "Google Tasks")
		}
		for lang := range googleSections {
			s := renderGoogleSection(t, lang)
			if got := strings.Contains(s, "Google Tasks"); got != listed {
				t.Errorf("%s: Google Tasks named = %v, listed = %v", lang, got, listed)
			}
		}
	}
}
