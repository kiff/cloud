package pages

// revokeNoteWords ends /tafs/privacy's retention line for a connection's token
// or key, per language. That line says a sign-in no other connection uses is
// revoked when it's disconnected; planque asks the vendor to revoke it through
// the vendor's OAuth revocation endpoint, and a vendor that publishes none
// can't be asked. Qonto publishes none (its authorization server metadata
// lists only an issuer and a token endpoint), so planque deletes the token on
// its side and Qonto keeps treating it as valid until the owner removes tafs's
// access in Qonto. When every vendor in the catalog revokes, or Qonto starts
// to, this note changes in the same change.
func revokeNoteWords(lang string) string {
	if w, ok := revokeNoteText[lang]; ok {
		return w
	}
	return revokeNoteText["en"]
}

var revokeNoteText = map[string]string{
	"en":    "Revoking depends on the vendor: where it offers no way to revoke a sign-in, as with Qonto, we delete the sign-in here, and that vendor may still treat it as valid until you remove tafs's access there.",
	"de":    "Der Widerruf hängt vom Anbieter ab: Bietet er keine Möglichkeit, eine Anmeldung zu widerrufen, wie bei Qonto, löschen wir die Anmeldung bei uns, und dieser Anbieter kann sie weiter als gültig behandeln, bis Sie den Zugriff von tafs dort entfernen.",
	"es":    "La revocación depende del proveedor: si no ofrece ninguna forma de revocar un inicio de sesión, como ocurre con Qonto, eliminamos el inicio de sesión aquí, y ese proveedor puede seguir considerándolo válido hasta que retire allí el acceso de tafs.",
	"fr":    "La révocation dépend du fournisseur : s'il n'offre aucun moyen de révoquer une connexion, comme Qonto, nous supprimons la connexion de notre côté, et ce fournisseur peut encore la considérer comme valide jusqu'à ce que vous y retiriez l'accès de tafs.",
	"it":    "La revoca dipende dal fornitore: se non offre alcun modo per revocare un accesso, come nel caso di Qonto, eliminiamo l'accesso da parte nostra, e quel fornitore può continuare a considerarlo valido finché non rimuovi lì l'accesso di tafs.",
	"nl":    "Intrekken hangt af van de leverancier: als die geen manier biedt om een aanmelding in te trekken, zoals bij Qonto, verwijderen we de aanmelding hier, en kan die leverancier haar als geldig blijven behandelen tot u de toegang van tafs daar verwijdert.",
	"pl":    "Unieważnienie zależy od dostawcy: jeśli nie oferuje on sposobu na unieważnienie logowania, jak w przypadku Qonto, usuwamy logowanie u nas, a ten dostawca może nadal traktować je jako ważne, dopóki nie usuniesz tam dostępu tafs.",
	"pt":    "A revogação depende do fornecedor: quando este não oferece forma de revogar um início de sessão, como acontece com o Qonto, eliminamos o início de sessão do nosso lado, e esse fornecedor pode continuar a considerá-lo válido até que remova lá o acesso do tafs.",
	"pt-br": "A revogação depende do fornecedor: quando ele não oferece um jeito de revogar um login, como acontece com o Qonto, excluímos o login do nosso lado, e esse fornecedor pode continuar a considerá-lo válido até você remover lá o acesso do tafs.",
	"ko":    "취소는 제공업체에 따라 다릅니다. Qonto처럼 로그인을 취소할 방법을 제공하지 않는 제공업체의 경우, 저희는 이곳에서 로그인을 삭제하며, 해당 제공업체에서 tafs의 접근 권한을 제거하기 전까지는 그 제공업체가 이를 계속 유효한 것으로 취급할 수 있습니다.",
	"ja":    "取り消しができるかはベンダーによります。Qonto のようにサインインを取り消す手段を提供していないベンダーの場合、当社側ではサインインを削除しますが、そのベンダーで tafs のアクセスを削除するまで、ベンダー側では有効なものとして扱われることがあります。",
	"et":    "Tühistamine sõltub teenusepakkujast: kui see ei paku võimalust sisselogimist tühistada, nagu Qonto puhul, kustutame sisselogimise meie poolel ja see teenusepakkuja võib seda kehtivaks pidada seni, kuni eemaldate seal tafsi juurdepääsu.",
	"ar":    "يعتمد السحب على المزوّد: فإذا لم يتح أي طريقة لسحب تسجيل الدخول، كما في Qonto، نحذف تسجيل الدخول لدينا، وقد يظل ذلك المزوّد يعدّه صالحًا إلى أن تزيل وصول tafs لديه.",
	"az":    "Ləğvetmə təchizatçıdan asılıdır: Qonto-da olduğu kimi, girişi ləğv etmək üçün heç bir yol təklif etmirsə, girişi bizim tərəfdə silirik, həmin təchizatçı isə siz orada tafs-ın girişini silənədək onu etibarlı saya bilər.",
	"bg":    "Отменянето зависи от доставчика: когато той не предлага начин да се отмени вход, както при Qonto, изтриваме входа при нас, а този доставчик може да продължи да го смята за валиден, докато не премахнете достъпа на tafs там.",
	"bn":    "প্রত্যাহার নির্ভর করে বিক্রেতার উপর: Qonto-র মতো যেখানে কোনো সাইন-ইন প্রত্যাহারের উপায় নেই, সেখানে আমরা আমাদের দিকে সাইন-ইনটি মুছে ফেলি, এবং আপনি সেখানে tafs-এর অ্যাক্সেস সরিয়ে না দেওয়া পর্যন্ত সেই বিক্রেতা এটিকে বৈধ হিসেবে গণ্য করতে পারে।",
	"ca":    "La revocació depèn del proveïdor: si no ofereix cap manera de revocar un inici de sessió, com passa amb Qonto, eliminem l'inici de sessió aquí, i aquest proveïdor el pot continuar considerant vàlid fins que hi retiris l'accés de tafs.",
	"cs":    "Odvolání závisí na dodavateli: pokud nenabízí žádný způsob, jak přihlášení odvolat, jako je tomu u Qonto, přihlášení u nás smažeme a tento dodavatel ho může dál považovat za platné, dokud tam přístup tafs neodeberete.",
	"dv":    "އުވާލެވޭނީ ވެންޑަރަށް ބަލައިގެން: Qonto ފަދައިން ސައިން-އިން އެއް އުވާލެވޭނެ ގޮތެއް ނުދޭނަމަ، އަހަރެމެން އެ ސައިން-އިން މިތަނުން ފޮހެލަމެވެ. އަދި ތިޔަބޭފުޅާ އެތަނުން tafs ގެ އެކްސެސް ނައްތާލައްވަންދެން، އެ ވެންޑަރު އެއީ ޞައްޙަ އެއްޗެއްގެ ގޮތުގައި ބަލާދާނެއެވެ.",
	"dz":    "ཆ་མེད་གཏང་ནི་འདི་ བཙོང་མཁན་ལུ་རག་ལསཔ་ཨིན། Qonto བཟུམ་ ནང་བསྐྱོད་ཅིག་ཆ་མེད་གཏང་ནིའི་ཐབས་ལམ་མ་བྱིན་པ་ཅིན་ ང་བཅས་ཀྱིས་ ནང་བསྐྱོད་འདི་ ང་བཅས་ཀྱི་ཕྱོགས་ལས་བཏོན་གཏངམ་ཨིན་ དེ་ལས་ ཁྱོད་ཀྱིས་ཕར་ tafs གི་འཛུལ་སྤྱོད་བཏོན་མ་གཏང་ཚུན་ བཙོང་མཁན་དེ་གིས་ ནུས་ཅན་སྦེ་བརྩི་འཐདཔ་ཨིན།",
	"el":    "Η ανάκληση εξαρτάται από τον πάροχο: όταν δεν προσφέρει τρόπο ανάκλησης μιας σύνδεσης, όπως η Qonto, διαγράφουμε τη σύνδεση από την πλευρά μας, και ο πάροχος αυτός μπορεί να συνεχίσει να τη θεωρεί έγκυρη μέχρι να αφαιρέσετε εκεί την πρόσβαση του tafs.",
	"he":    "הביטול תלוי בספק: כשהוא אינו מציע דרך לבטל התחברות, כמו Qonto, אנו מוחקים את ההתחברות אצלנו, והספק עשוי עדיין להתייחס אליה כתקפה עד שתסירו שם את הגישה של tafs.",
	"hi":    "रद्द करना विक्रेता पर निर्भर करता है: जहाँ वह किसी साइन-इन को रद्द करने का कोई तरीका नहीं देता, जैसे Qonto, वहाँ हम अपनी ओर से साइन-इन हटा देते हैं, और जब तक आप वहाँ tafs की पहुँच नहीं हटाते, वह विक्रेता उसे मान्य मान सकता है।",
	"hr":    "Opoziv ovisi o dobavljaču: ako ne nudi način za opoziv prijave, kao Qonto, prijavu brišemo kod nas, a taj je dobavljač može i dalje smatrati valjanom dok ondje ne uklonite pristup tafsa.",
	"hu":    "A visszavonás a szolgáltatón múlik: ha nem kínál módot egy bejelentkezés visszavonására, mint a Qonto, a bejelentkezést nálunk töröljük, a szolgáltató pedig továbbra is érvényesnek tekintheti, amíg ott el nem távolítja a tafs hozzáférését.",
	"hy":    "Չեղարկումը կախված է մատակարարից. եթե այն մուտքը չեղարկելու որևէ եղանակ չի առաջարկում, ինչպես Qonto-ն, մենք մուտքը ջնջում ենք մեր կողմում, իսկ այդ մատակարարը կարող է այն դեռ վավեր համարել, մինչև դուք այնտեղ չհեռացնեք tafs-ի մուտքը։",
	"id":    "Pencabutan bergantung pada vendor: jika vendor tidak menyediakan cara untuk mencabut proses masuk, seperti Qonto, kami menghapus proses masuk itu di pihak kami, dan vendor tersebut mungkin masih menganggapnya berlaku sampai Anda menghapus akses tafs di sana.",
	"ka":    "გაუქმება მომწოდებელზეა დამოკიდებული: თუ ის შესვლის გაუქმების საშუალებას არ იძლევა, როგორც Qonto, შესვლას ჩვენს მხარეს ვშლით, ის მომწოდებელი კი შეიძლება მას კვლავ მოქმედად თვლიდეს, სანამ იქ tafs-ის წვდომას არ მოხსნით.",
	"km":    "ការដកសិទ្ធិអាស្រ័យលើអ្នកផ្គត់ផ្គង់៖ បើគេមិនផ្តល់វិធីដកសិទ្ធិការចូលគណនីទេ ដូចជា Qonto យើងលុបការចូលគណនីនៅខាងយើង ហើយអ្នកផ្គត់ផ្គង់នោះអាចនៅតែចាត់ទុកវាថាមានសុពលភាព រហូតដល់អ្នកដកសិទ្ធិចូលរបស់ tafs នៅទីនោះ។",
	"lo":    "ການຖອນສິດຂຶ້ນກັບຜູ້ໃຫ້ບໍລິການ: ຖ້າເຂົາບໍ່ມີວິທີຖອນສິດການເຂົ້າສູ່ລະບົບ ເຊັ່ນ Qonto, ພວກເຮົາຈະລຶບການເຂົ້າສູ່ລະບົບຢູ່ຝັ່ງພວກເຮົາ ແລະ ຜູ້ໃຫ້ບໍລິການນັ້ນອາດຍັງຖືວ່າມັນໃຊ້ໄດ້ ຈົນກວ່າທ່ານຈະລຶບສິດເຂົ້າເຖິງຂອງ tafs ຢູ່ທີ່ນັ້ນ.",
	"lt":    "Atšaukimas priklauso nuo tiekėjo: jei jis nesiūlo būdo atšaukti prisijungimą, kaip Qonto, prisijungimą ištriname savo pusėje, o tas tiekėjas gali jį laikyti galiojančiu, kol ten nepašalinsite tafs prieigos.",
	"lv":    "Atsaukšana ir atkarīga no piegādātāja: ja tas nepiedāvā veidu, kā atsaukt pierakstīšanos, kā Qonto, mēs pierakstīšanos dzēšam savā pusē, un šis piegādātājs to var turpināt uzskatīt par derīgu, līdz jūs tur noņemat tafs piekļuvi.",
	"mk":    "Отповикувањето зависи од добавувачот: ако тој не нуди начин да се отповика најава, како Qonto, ја бришеме најавата кај нас, а тој добавувач може и понатаму да ја смета за важечка додека таму не го отстраните пристапот на tafs.",
	"mn":    "Цуцлах эсэх нь нийлүүлэгчээс хамаарна: Qonto шиг нэвтрэлтийг цуцлах арга санал болгодоггүй бол бид нэвтрэлтийг өөрийн талд устгадаг бөгөөд та тэнд tafs-ийн хандалтыг устгах хүртэл тэр нийлүүлэгч үүнийг хүчинтэй гэж үзэж болно.",
	"ne":    "रद्द गर्ने कुरा विक्रेतामा भर पर्छ: Qonto जस्तै, साइन-इन रद्द गर्ने कुनै उपाय नदिने ठाउँमा हामी हाम्रो तर्फबाट साइन-इन मेटाउँछौं, र तपाईंले त्यहाँ tafs को पहुँच नहटाएसम्म त्यो विक्रेताले यसलाई मान्य मान्न सक्छ।",
	"ro":    "Revocarea depinde de furnizor: dacă nu oferă nicio modalitate de a revoca o conectare, ca în cazul Qonto, ștergem conectarea la noi, iar acel furnizor o poate considera în continuare validă până când eliminați acolo accesul tafs.",
	"si":    "අවලංගු කිරීම සැපයුම්කරු මත රඳා පවතී: Qonto වැනි, පිවිසුමක් අවලංගු කිරීමට ක්‍රමයක් නොදෙන තැන, අපි අපේ පැත්තෙන් පිවිසුම මකා දමන අතර, ඔබ එහි tafs හි ප්‍රවේශය ඉවත් කරන තුරු එම සැපයුම්කරු එය වලංගු ලෙස සැලකිය හැකිය.",
	"sk":    "Odvolanie závisí od dodávateľa: ak neponúka spôsob, ako prihlásenie odvolať, ako je to pri Qonto, prihlásenie u nás vymažeme a tento dodávateľ ho môže naďalej považovať za platné, kým tam prístup tafs neodstránite.",
	"sl":    "Preklic je odvisen od ponudnika: če ne ponuja načina za preklic prijave, kot pri Qonto, prijavo izbrišemo pri nas, ta ponudnik pa jo lahko še naprej šteje za veljavno, dokler tam ne odstranite dostopa tafs.",
	"sq":    "Revokimi varet nga ofruesi: kur ai nuk ofron asnjë mënyrë për të revokuar një hyrje, si në rastin e Qonto, ne e fshijmë hyrjen nga ana jonë, dhe ai ofrues mund ta konsiderojë ende të vlefshme derisa të hiqni atje qasjen e tafs.",
	"sr":    "Opoziv zavisi od dobavljača: ako ne nudi način da se prijava opozove, kao kod Qonto, prijavu brišemo kod nas, a taj dobavljač može i dalje da je smatra važećom dok tamo ne uklonite pristup tafs-a.",
	"ta":    "திரும்பப் பெறுவது விற்பனையாளரைப் பொறுத்தது: Qonto போல, உள்நுழைவைத் திரும்பப் பெற வழி இல்லாத இடத்தில், நாங்கள் எங்கள் பக்கத்தில் உள்நுழைவை நீக்குகிறோம், நீங்கள் அங்கு tafs இன் அணுகலை அகற்றும் வரை அந்த விற்பனையாளர் அதைச் செல்லுபடியானதாகக் கருதலாம்.",
	"tg":    "Бекор кардан аз таъминкунанда вобаста аст: агар ӯ ягон роҳи бекор кардани воридшавӣ пешниҳод накунад, мисли Qonto, мо воридшавиро дар тарафи худ нест мекунем, ва он таъминкунанда метавонад онро то он даме ки шумо дар он ҷо дастрасии tafs-ро нест накунед, эътибор дошта ҳисоб кунад.",
	"th":    "การเพิกถอนขึ้นอยู่กับผู้ให้บริการ: หากผู้ให้บริการไม่มีวิธีเพิกถอนการลงชื่อเข้าใช้ เช่น Qonto เราจะลบการลงชื่อเข้าใช้ฝั่งเรา และผู้ให้บริการนั้นอาจยังถือว่าใช้ได้อยู่ จนกว่าคุณจะนำสิทธิ์เข้าถึงของ tafs ออกที่นั่น",
	"tk":    "Ýatyrmak üpjün edijä bagly: Qonto ýaly, girişi ýatyrmagyň ýoluny hödürlemeýän bolsa, girişi öz tarapymyzda pozýarys, siz ol ýerde tafs-yň elýeterliligini aýyrýançaňyz, şol üpjün ediji ony entek hereketdäki hasaplap biler.",
	"tr":    "İptal, sağlayıcıya bağlıdır: Qonto'da olduğu gibi bir oturum açmayı iptal etmenin yolunu sunmuyorsa, oturum açmayı kendi tarafımızda sileriz ve siz orada tafs'ın erişimini kaldırana kadar o sağlayıcı onu geçerli saymaya devam edebilir.",
	"ur":    "منسوخی فراہم کنندہ پر منحصر ہے: جہاں وہ سائن اِن منسوخ کرنے کا کوئی طریقہ نہیں دیتا، جیسے Qonto، وہاں ہم اپنی طرف سے سائن اِن حذف کر دیتے ہیں، اور جب تک آپ وہاں tafs کی رسائی نہیں ہٹاتے، وہ فراہم کنندہ اسے درست سمجھ سکتا ہے۔",
	"vi":    "Việc thu hồi phụ thuộc vào nhà cung cấp: nếu họ không có cách nào để thu hồi một lần đăng nhập, như Qonto, chúng tôi xóa lần đăng nhập đó ở phía chúng tôi, và nhà cung cấp đó vẫn có thể coi nó là hợp lệ cho đến khi bạn gỡ quyền truy cập của tafs ở đó.",
	"zh":    "能否撤銷取決於供應商：如果供應商不提供撤銷登入的方式（例如 Qonto），我們會在我們這邊刪除該登入，而在您於該供應商處移除 tafs 的存取權之前，該供應商可能仍視其為有效。",
}
