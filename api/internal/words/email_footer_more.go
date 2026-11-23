package words

// emailFooterMore holds the footer every email ends with (the two link names and the
// never-ask sentence) in every shipped language besides English. First drafts for each
// language's owner to correct; the key is the primary language tag ("zh" is Traditional
// Chinese).
func init() {
	for code, f := range emailFooterMore {
		EmailFooters[code] = f
	}
}

var emailFooterMore = map[string]EmailFooter{
	"ar": {Privacy: "الخصوصية", Help: "المساعدة", NeverAsk: "لن يطلب منك KIFF كلمة المرور أو الرمز أبدًا."},
	"az": {Privacy: "Məxfilik", Help: "Kömək", NeverAsk: "KIFF sizdən heç vaxt parolunuzu və ya kodunuzu istəməyəcək."},
	"bg": {Privacy: "Поверителност", Help: "Помощ", NeverAsk: "KIFF никога няма да поиска вашата парола или код."},
	"bn": {Privacy: "গোপনীয়তা", Help: "সহায়তা", NeverAsk: "KIFF কখনো আপনার পাসওয়ার্ড বা কোড চাইবে না।"},
	"ca": {Privacy: "Privadesa", Help: "Ajuda", NeverAsk: "KIFF mai no et demanarà la contrasenya ni el codi."},
	"cs": {Privacy: "Soukromí", Help: "Nápověda", NeverAsk: "KIFF vás nikdy nepožádá o heslo ani kód."},
	"de": {Privacy: "Datenschutz", Help: "Hilfe", NeverAsk: "KIFF fragt Sie nie nach Ihrem Passwort oder Code."},
	"dv": {Privacy: "ޕްރައިވެސީ", Help: "އެހީތެރިކަން", NeverAsk: "KIFF އިން ތިޔަބައިމީހާގެ ޕާސްވޯޑް ނުވަތަ ކޯޑު ދުވަހަކުވެސް ނުއެދޭނެ."},
	"dz": {Privacy: "སྒེར་དོན", Help: "གྲོགས་རམ", NeverAsk: "KIFF གིས་ ཁྱེད་ཀྱི་ གསང་ཚིག་ ཡང་ན་ ཨང་རྟགས་ ནམ་ཡང་ མི་འདྲི།"},
	"el": {Privacy: "Απόρρητο", Help: "Βοήθεια", NeverAsk: "Το KIFF δεν θα σας ζητήσει ποτέ τον κωδικό πρόσβασης ή τον κωδικό σας."},
	"es": {Privacy: "Privacidad", Help: "Ayuda", NeverAsk: "KIFF nunca te pedirá tu contraseña ni tu código."},
	"et": {Privacy: "Privaatsus", Help: "Abi", NeverAsk: "KIFF ei küsi sinult kunagi sinu parooli ega koodi."},
	"fr": {Privacy: "Confidentialité", Help: "Aide", NeverAsk: "KIFF ne vous demandera jamais votre mot de passe ni votre code."},
	"he": {Privacy: "פרטיות", Help: "עזרה", NeverAsk: "KIFF לעולם לא יבקש ממך את הסיסמה או הקוד שלך."},
	"hi": {Privacy: "गोपनीयता", Help: "सहायता", NeverAsk: "KIFF आपसे कभी आपका पासवर्ड या कोड नहीं मांगेगा।"},
	"hr": {Privacy: "Privatnost", Help: "Pomoć", NeverAsk: "KIFF vas nikada neće tražiti zaporku ni kod."},
	"hu": {Privacy: "Adatvédelem", Help: "Súgó", NeverAsk: "A KIFF soha nem kéri a jelszavadat vagy a kódodat."},
	"hy": {Privacy: "Գաղտնիություն", Help: "Օգնություն", NeverAsk: "KIFF-ը երբեք չի խնդրի ձեր գաղտնաբառը կամ կոդը։"},
	"id": {Privacy: "Privasi", Help: "Bantuan", NeverAsk: "KIFF tidak akan pernah meminta kata sandi atau kode Anda."},
	"it": {Privacy: "Privacy", Help: "Aiuto", NeverAsk: "KIFF non ti chiederà mai la password o il codice."},
	"ja": {Privacy: "プライバシー", Help: "ヘルプ", NeverAsk: "KIFFがパスワードやコードをお尋ねすることはありません。"},
	"ka": {Privacy: "კონფიდენციალურობა", Help: "დახმარება", NeverAsk: "KIFF არასოდეს მოგთხოვთ პაროლს ან კოდს."},
	"km": {Privacy: "ឯកជនភាព", Help: "ជំនួយ", NeverAsk: "KIFF នឹងមិនសុំពាក្យសម្ងាត់ ឬលេខកូដរបស់អ្នកទេ។"},
	"ko": {Privacy: "개인정보 처리방침", Help: "도움말", NeverAsk: "KIFF는 비밀번호나 코드를 절대 묻지 않습니다."},
	"lo": {Privacy: "ຄວາມເປັນສ່ວນຕົວ", Help: "ຊ່ວຍເຫຼືອ", NeverAsk: "KIFF ຈະບໍ່ຖາມລະຫັດຜ່ານ ຫຼື ລະຫັດຂອງທ່ານຈັກເທື່ອ."},
	"lt": {Privacy: "Privatumas", Help: "Pagalba", NeverAsk: "KIFF niekada neprašys jūsų slaptažodžio ar kodo."},
	"lv": {Privacy: "Privātums", Help: "Palīdzība", NeverAsk: "KIFF nekad neprasīs jūsu paroli vai kodu."},
	"mk": {Privacy: "Приватност", Help: "Помош", NeverAsk: "KIFF никогаш нема да ви побара лозинка или код."},
	"mn": {Privacy: "Нууцлал", Help: "Тусламж", NeverAsk: "KIFF таны нууц үг эсвэл кодыг хэзээ ч асуухгүй."},
	"ne": {Privacy: "गोपनीयता", Help: "सहायता", NeverAsk: "KIFF ले तपाईंको पासवर्ड वा कोड कहिल्यै माग्दैन।"},
	"nl": {Privacy: "Privacy", Help: "Help", NeverAsk: "KIFF vraagt je nooit om je wachtwoord of code."},
	"pl": {Privacy: "Prywatność", Help: "Pomoc", NeverAsk: "KIFF nigdy nie poprosi Cię o hasło ani kod."},
	"pt": {Privacy: "Privacidade", Help: "Ajuda", NeverAsk: "O KIFF nunca lhe pedirá a palavra-passe nem o código."},
	"ro": {Privacy: "Confidențialitate", Help: "Ajutor", NeverAsk: "KIFF nu îți va cere niciodată parola sau codul."},
	"si": {Privacy: "පෞද්ගලිකත්වය", Help: "උදව්", NeverAsk: "KIFF ඔබගේ මුරපදය හෝ කේතය කිසිවිටෙක ඉල්ලන්නේ නැත."},
	"sk": {Privacy: "Súkromie", Help: "Pomoc", NeverAsk: "KIFF vás nikdy nepožiada o heslo ani kód."},
	"sl": {Privacy: "Zasebnost", Help: "Pomoč", NeverAsk: "KIFF vas nikoli ne bo prosil za geslo ali kodo."},
	"sq": {Privacy: "Privatësia", Help: "Ndihma", NeverAsk: "KIFF nuk do t'ju kërkojë kurrë fjalëkalimin ose kodin."},
	"sr": {Privacy: "Privatnost", Help: "Pomoć", NeverAsk: "KIFF vas nikada neće tražiti lozinku ni kod."},
	"ta": {Privacy: "தனியுரிமை", Help: "உதவி", NeverAsk: "KIFF உங்கள் கடவுச்சொல்லையோ குறியீட்டையோ ஒருபோதும் கேட்காது."},
	"tg": {Privacy: "Махфият", Help: "Кӯмак", NeverAsk: "KIFF ҳеҷ гоҳ рамз ё коди шуморо намепурсад."},
	"th": {Privacy: "ความเป็นส่วนตัว", Help: "ความช่วยเหลือ", NeverAsk: "KIFF จะไม่ขอรหัสผ่านหรือรหัสของคุณเด็ดขาด"},
	"tk": {Privacy: "Gizlinlik", Help: "Kömek", NeverAsk: "KIFF sizden hiç haçan parolyňyzy ýa-da koduňyzy soramaz."},
	"tr": {Privacy: "Gizlilik", Help: "Yardım", NeverAsk: "KIFF sizden asla parolanızı veya kodunuzu istemez."},
	"ur": {Privacy: "رازداری", Help: "مدد", NeverAsk: "KIFF آپ سے کبھی آپ کا پاس ورڈ یا کوڈ نہیں مانگے گا۔"},
	"vi": {Privacy: "Quyền riêng tư", Help: "Trợ giúp", NeverAsk: "KIFF sẽ không bao giờ hỏi mật khẩu hoặc mã của bạn."},
	"zh": {Privacy: "隱私權", Help: "說明", NeverAsk: "KIFF 絕不會向您索取密碼或代碼。"},
}
