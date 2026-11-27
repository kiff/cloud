package pages

import _ "embed"

// PendingInHouseApps names the Google apps whose own tafs sign-in is in
// Google's verification and not yet offered to customers. Customers connect
// them through the sign-in partner until then, so each one must also be on
// ManagedApps. /tafs/privacy §6 keeps disclosing tafs's own sign-in for them,
// with the access it will ask for, because Google's review of that sign-in
// reads §6. An app moves from here to inhouse_apps.json when it goes live
// for customers.
var PendingInHouseApps = mustAppNames(pendingAppsJSON)

//go:embed inhouse_pending.json
var pendingAppsJSON []byte

// googlePending is the pending Google apps §6 names, each with the scopes its
// own sign-in will ask for, in lang. An app already live in-house isn't
// pending, and one with no known scopes is skipped (a test keeps the file to
// known names).
func googlePending(lang string) []shownGoogleApp {
	live := map[string]bool{}
	for _, a := range InHouseApps {
		live[a] = true
	}
	var out []shownGoogleApp
	for _, name := range PendingInHouseApps {
		scopes, ok := googleAppScopes[name]
		if !ok || live[name] {
			continue
		}
		app := shownGoogleApp{Name: name}
		for _, s := range scopes {
			what := s.What[lang]
			if what == "" {
				what = s.What["en"]
			}
			app.Scopes = append(app.Scopes, shownScope{ID: s.ID, What: what})
		}
		out = append(out, app)
	}
	return out
}

// pendingWords is what §6 says of a pending app, per language: Line follows
// "<app>: " in the list of how each Google app connects, right under the
// sign-in partner's item ("listed above"); AsksFor introduces the access the
// app's own sign-in will ask for.
type pendingWords struct{ Line, AsksFor string }

// googlePendingSep joins the app's name to its Line: French puts a space
// before the colon.
func googlePendingSep(lang string) string {
	if lang == "fr" {
		return " : "
	}
	return ": "
}

func googlePendingWords(lang string) pendingWords {
	if w, ok := googlePendingText[lang]; ok {
		return w
	}
	return googlePendingText["en"]
}

var googlePendingText = map[string]pendingWords{
	"en":    {"through tafs's own Google sign-in once Google has verified it; until then, through our sign-in partner listed above.", "Once Google has verified it, that sign-in asks for:"},
	"de":    {"über die eigene Google-Anmeldung von tafs, sobald Google sie überprüft hat; bis dahin über unseren oben genannten Anmeldepartner.", "Sobald Google sie überprüft hat, fragt diese Anmeldung nach:"},
	"es":    {"a través del propio inicio de sesión de Google de tafs, una vez que Google lo haya verificado; hasta entonces, a través de nuestro socio de inicio de sesión indicado arriba.", "Una vez que Google lo haya verificado, ese inicio de sesión pide:"},
	"fr":    {"par l'authentification Google propre à tafs, une fois que Google l'aura validée ; d'ici là, par notre partenaire d'authentification indiqué ci-dessus.", "Une fois validée par Google, cette authentification demande :"},
	"it":    {"tramite l'accesso Google di tafs stesso, una volta che Google lo avrà verificato; fino ad allora, tramite il nostro partner per gli accessi indicato sopra.", "Una volta verificato da Google, quell'accesso chiede:"},
	"nl":    {"via de eigen Google-login van tafs, zodra Google die heeft geverifieerd; tot dan via onze inlogpartner hierboven.", "Zodra Google die heeft geverifieerd, vraagt die login om:"},
	"pl":    {"przez własne logowanie Google tafs, gdy Google je zweryfikuje; do tego czasu przez naszego partnera do logowania wymienionego wyżej.", "Gdy Google je zweryfikuje, to logowanie prosi o:"},
	"pt":    {"através do próprio início de sessão Google do tafs, depois de a Google o verificar; até lá, através do nosso parceiro de início de sessão indicado acima.", "Depois de a Google o verificar, esse início de sessão pede:"},
	"pt-br": {"pelo próprio login do Google do tafs, depois que o Google o verificar; até lá, pelo nosso parceiro de login indicado acima.", "Depois que o Google o verificar, esse login pede:"},
	"ko":    {"Google의 확인이 끝나면 tafs 자체 Google 로그인을 통해, 그때까지는 위의 로그인 파트너를 통해 연결합니다.", "Google의 확인이 끝나면 이 로그인이 요청하는 권한:"},
	"ja":    {"Googleによる確認が完了するとtafs自身のGoogleサインインを通じて、それまでは上記のサインインパートナーを通じて接続します。", "Googleによる確認後、このサインインが求めるアクセス："},
	"et":    {"tafsi enda Google'i sisselogimise kaudu, kui Google on selle kinnitanud; seni ülal nimetatud sisselogimispartneri kaudu.", "Kui Google on selle kinnitanud, küsib see sisselogimine:"},
	"lv":    {"caur paša tafs Google pierakstīšanos, kad Google to būs verificējis; līdz tam caur mūsu iepriekš minēto pierakstīšanās partneri.", "Kad Google to būs verificējis, šī pierakstīšanās prasa:"},
	"el":    {"μέσω της δικής του σύνδεσης Google του tafs, μόλις την επαληθεύσει η Google· μέχρι τότε, μέσω του συνεργάτη σύνδεσής μας που αναφέρεται παραπάνω.", "Μόλις την επαληθεύσει η Google, αυτή η σύνδεση ζητά:"},
	"lt":    {"per paties tafs Google prisijungimą, kai Google jį patvirtins; iki tol per mūsų aukščiau nurodytą prisijungimo partnerį.", "Kai Google jį patvirtins, šis prisijungimas prašo:"},
	"cs":    {"prostřednictvím vlastního přihlášení Google aplikace tafs, jakmile ho Google ověří; do té doby prostřednictvím našeho partnera pro přihlašování uvedeného výše.", "Jakmile ho Google ověří, toto přihlášení žádá o:"},
	"sk":    {"prostredníctvom vlastného prihlásenia Google v tafs, keď ho Google overí; dovtedy prostredníctvom nášho partnera pre prihlasovanie uvedeného vyššie.", "Keď ho Google overí, toto prihlásenie žiada o:"},
	"sl":    {"prek lastne prijave Google aplikacije tafs, ko jo Google preveri; do takrat prek našega partnerja za prijavo, navedenega zgoraj.", "Ko jo Google preveri, ta prijava zahteva:"},
	"hu":    {"a tafs saját Google-bejelentkezésén keresztül, miután a Google ellenőrizte; addig a fent említett bejelentkezési partnerünkön keresztül.", "Miután a Google ellenőrizte, ez a bejelentkezés ezt kéri:"},
	"ro":    {"prin autentificarea Google proprie a tafs, după ce Google o verifică; până atunci, prin partenerul nostru de autentificare menționat mai sus.", "După ce Google o verifică, această autentificare solicită:"},
	"bg":    {"чрез собствения вход с Google на tafs, след като Google го потвърди; дотогава чрез нашия партньор за вход, посочен по-горе.", "След като Google го потвърди, този вход иска:"},
	"hr":    {"putem tafsove vlastite Google prijave, nakon što je Google potvrdi; do tada putem našeg partnera za prijavu navedenog iznad.", "Nakon što je Google potvrdi, ta prijava traži:"},
	"dz":    {"Google གིས་བདེན་དཔྱད་འབད་ཚར་བའི་ཤུལ་ལས་ tafs རང་གི་ Google ནང་འཛུལ་བརྒྱུད་དེ། དེ་མ་ཚུན་ གོང་གི་ང་བཅས་ཀྱི་ནང་འཛུལ་ཕྱོགས་མཐུན་མི་བརྒྱུད་དེ།", "Google གིས་བདེན་དཔྱད་འབད་ཚར་བའི་ཤུལ་ལས་ ནང་འཛུལ་འདི་གིས་ཞུ་མི:"},
	"dv":    {"Google އިން ޔަގީންކޮށްފިނަމަ tafs ގެ އަމިއްލަ Google ލޮގިން މެދުވެރިކޮށް؛ އޭގެ ކުރިން، މަތީގައިވާ އަޅުގަނޑުމެންގެ ލޮގިން ޕާޓްނަރ މެދުވެރިކޮށް.", "Google އިން ޔަގީންކޮށްފިނަމަ، މި ލޮގިން އެދޭނީ:"},
	"tg":    {"тавассути воридшавии Google-и худи tafs, вақте ки Google онро тасдиқ кунад; то он вақт тавассути шарики воридшавии мо, ки дар боло зикр шудааст.", "Вақте ки Google онро тасдиқ кунад, ин воридшавӣ мепурсад:"},
	"tk":    {"Google tassyklandan soň tafs-yň öz Google girişi arkaly; şoňa çenli ýokarda görkezilen giriş hyzmatdaşymyz arkaly.", "Google tassyklandan soň bu giriş şulary soraýar:"},
	"ur":    {"Google کی تصدیق کے بعد tafs کے اپنے Google سائن اِن کے ذریعے؛ تب تک اوپر بیان کردہ ہمارے سائن اِن ساتھی کے ذریعے۔", "Google کی تصدیق کے بعد یہ سائن اِن یہ رسائی مانگتا ہے:"},
	"hi":    {"Google द्वारा सत्यापित होने के बाद tafs के अपने Google साइन-इन के ज़रिए; तब तक ऊपर बताए गए हमारे साइन-इन सहयोगी के ज़रिए।", "Google द्वारा सत्यापित होने के बाद यह साइन-इन यह पहुँच माँगता है:"},
	"ta":    {"Google சரிபார்த்த பிறகு tafs இன் சொந்த Google உள்நுழைவு வழியாக; அதுவரை மேலே குறிப்பிட்ட எங்கள் உள்நுழைவுக் கூட்டாளி வழியாக.", "Google சரிபார்த்த பிறகு, இந்த உள்நுழைவு கேட்பது:"},
	"si":    {"Google විසින් තහවුරු කළ පසු tafs හි ම Google පිවිසුම හරහා; එතෙක් ඉහත සඳහන් අපගේ පිවිසුම් හවුල්කරු හරහා.", "Google විසින් තහවුරු කළ පසු, මෙම පිවිසුම ඉල්ලන්නේ:"},
	"lo":    {"ຜ່ານການເຂົ້າສູ່ລະບົບ Google ຂອງ tafs ເອງ ເມື່ອ Google ຢືນຢັນແລ້ວ; ຈົນກວ່າຈະເຖິງຕອນນັ້ນ ຜ່ານຄູ່ຮ່ວມການເຂົ້າສູ່ລະບົບຂອງພວກເຮົາທີ່ກ່າວໄວ້ຂ້າງເທິງ.", "ເມື່ອ Google ຢືນຢັນແລ້ວ, ການເຂົ້າສູ່ລະບົບນີ້ຂໍ:"},
	"km":    {"តាមរយៈការចូល Google ផ្ទាល់របស់ tafs នៅពេល Google បានផ្ទៀងផ្ទាត់រួច; រហូតដល់ពេលនោះ តាមរយៈដៃគូចូលរបស់យើងដែលបានរៀបរាប់ខាងលើ។", "នៅពេល Google បានផ្ទៀងផ្ទាត់រួច ការចូលនេះស្នើសុំ៖"},
	"ne":    {"Google ले प्रमाणित गरेपछि tafs को आफ्नै Google साइन इन मार्फत; त्यतिन्जेल माथि उल्लेखित हाम्रो साइन इन साझेदार मार्फत।", "Google ले प्रमाणित गरेपछि यो साइन इनले माग्छ:"},
	"mn":    {"Google баталгаажуулсны дараа tafs-ийн өөрийн Google нэвтрэлтээр; тэр болтол дээр дурдсан манай нэвтрэлтийн түншээр дамжуулан.", "Google баталгаажуулсны дараа энэ нэвтрэлт дараахыг хүснэ:"},
	"mk":    {"преку сопствената Google најава на tafs, откако Google ќе ја потврди; дотогаш преку нашиот партнер за најава наведен погоре.", "Откако Google ќе ја потврди, оваа најава бара:"},
	"ca":    {"a través de l'inici de sessió de Google del mateix tafs, un cop Google l'hagi verificat; fins aleshores, a través del nostre soci d'inici de sessió indicat a dalt.", "Un cop Google l'hagi verificat, aquest inici de sessió demana:"},
	"he":    {"דרך ההתחברות העצמית של tafs ל-Google, לאחר ש-Google תאמת אותה; עד אז, דרך שותף ההתחברות שלנו שצוין למעלה.", "לאחר ש-Google תאמת אותה, ההתחברות הזו מבקשת:"},
	"bn":    {"Google যাচাই করার পর tafs-এর নিজস্ব Google সাইন-ইনের মাধ্যমে; ততদিন উপরে উল্লেখিত আমাদের সাইন-ইন অংশীদারের মাধ্যমে।", "Google যাচাই করার পর এই সাইন-ইন চায়:"},
	"az":    {"Google təsdiqlədikdən sonra tafs-ın öz Google girişi vasitəsilə; o vaxta qədər yuxarıda göstərilən giriş tərəfdaşımız vasitəsilə.", "Google təsdiqlədikdən sonra bu giriş bunları istəyir:"},
	"hy":    {"tafs-ի սեփական Google մուտքի միջոցով, երբ Google-ը այն հաստատի. մինչ այդ՝ վերևում նշված մեր մուտքի գործընկերոջ միջոցով։", "Երբ Google-ը այն հաստատի, այս մուտքը խնդրում է՝"},
	"ka":    {"tafs-ის საკუთარი Google შესვლის გავლით, როცა Google მას დაადასტურებს; მანამდე ზემოთ ნახსენები ჩვენი შესვლის პარტნიორის გავლით.", "როცა Google მას დაადასტურებს, ეს შესვლა ითხოვს:"},
	"sq":    {"përmes hyrjes së vet Google të tafs, pasi Google ta ketë verifikuar; deri atëherë, përmes partnerit tonë të hyrjes të përmendur më lart.", "Pasi Google ta ketë verifikuar, kjo hyrje kërkon:"},
	"sr":    {"preko sopstvene Google prijave tafs-a, kada je Google potvrdi; do tada preko našeg partnera za prijavu navedenog iznad.", "Kada je Google potvrdi, ta prijava traži:"},
	"ar":    {"عبر تسجيل دخول Google الخاص بـ tafs نفسه بعد أن تتحقق منه Google؛ وحتى ذلك الحين، عبر شريكنا في تسجيل الدخول المذكور أعلاه.", "بعد أن تتحقق منه Google، يطلب تسجيل الدخول هذا:"},
	"zh":    {"Google 完成驗證後，透過 tafs 自己的 Google 登入；在此之前，透過上述的登入夥伴。", "Google 完成驗證後，此登入要求的存取權限："},
	"th":    {"ผ่านการลงชื่อเข้าใช้ Google ของ tafs เอง เมื่อ Google ยืนยันแล้ว ระหว่างนี้ผ่านพาร์ตเนอร์การลงชื่อเข้าใช้ของเราที่ระบุไว้ข้างต้น", "เมื่อ Google ยืนยันแล้ว การลงชื่อเข้าใช้นี้จะขอ:"},
	"vi":    {"qua chính phương thức đăng nhập Google của tafs, sau khi Google xác minh; trước đó, qua đối tác đăng nhập của chúng tôi nêu ở trên.", "Sau khi Google xác minh, phương thức đăng nhập này yêu cầu:"},
	"id":    {"melalui login Google milik tafs sendiri, setelah Google memverifikasinya; sampai saat itu, melalui mitra login kami yang disebut di atas.", "Setelah Google memverifikasinya, login ini meminta:"},
	"tr":    {"Google doğruladıktan sonra tafs'ın kendi Google girişi üzerinden; o zamana kadar yukarıda belirtilen giriş ortağımız üzerinden.", "Google doğruladıktan sonra bu giriş şunları ister:"},
}
