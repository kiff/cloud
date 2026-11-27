package pages

// chatEndsWords follows the revoke note in /tafs/privacy's line on a connection's
// token or key, per language: deleting a chat with Coach also ends the connections
// that chat asked for that no taf uses. tafs cloud's DELETE of a chat asks planque
// to end each agent its own successful requests were answered under, never a live
// taf's (its API docs in kiff/tafs). It ends them as a disconnect does, so the revoke note
// before it applies to them too. A change to what a chat's delete ends changes
// this text in the same change.
func chatEndsWords(lang string) string {
	if w, ok := chatEndsText[lang]; ok {
		return w
	}
	return chatEndsText["en"]
}

var chatEndsText = map[string]string{
	"en":    "Deleting a chat with Coach also ends the connections it asked for that no taf uses, as disconnecting them would.",
	"de":    "Wenn du einen Chat mit Coach löschst, enden auch die Verbindungen, die er angefragt hat und die kein taf nutzt, so als würdest du sie trennen.",
	"fr":    "Supprimer un chat avec Coach met aussi fin aux connexions qu'il a demandées et qu'aucun taf n'utilise, comme si vous les déconnectiez.",
	"es":    "Eliminar un chat con Coach también termina las conexiones que pidió y que ningún taf usa, como si las desconectaras.",
	"it":    "Eliminare una chat con Coach chiude anche i collegamenti che ha richiesto e che nessun taf usa, come se li scollegassi.",
	"nl":    "Als je een chat met Coach verwijdert, eindigen ook de koppelingen die die chat aanvroeg en die geen taf gebruikt, alsof je ze ontkoppelt.",
	"pl":    "Usunięcie czatu z Coachem kończy też połączenia, o które poprosił, a których nie używa żaden taf, tak jakbyś je rozłączył.",
	"pt":    "Eliminar um chat com o Coach também termina as ligações que ele pediu e que nenhum taf usa, como se as desligasse.",
	"pt-br": "Excluir um chat com o Coach também encerra as conexões que ele pediu e que nenhum taf usa, como se você as desconectasse.",
	"ca":    "Suprimir un xat amb Coach també posa fi a les connexions que ha demanat i que cap taf no fa servir, com si les desconnectéssiu.",
	"cs":    "Smazání chatu s Coachem ukončí i připojení, o která požádal a která žádný taf nepoužívá, jako byste je odpojili.",
	"sk":    "Vymazanie chatu s Coachom ukončí aj pripojenia, o ktoré požiadal a ktoré nepoužíva žiadny taf, ako keby ste ich odpojili.",
	"sl":    "Izbris klepeta s Coachem konča tudi povezave, ki jih je zahteval in jih ne uporablja noben taf, kot da bi jih prekinili.",
	"hr":    "Brisanje razgovora s Coachem prekida i veze koje je zatražio, a koje ne koristi nijedan taf, kao da ste ih sami prekinuli.",
	"sr":    "Brisanje razgovora sa Coach-om prekida i veze koje je zatražio, a koje ne koristi nijedan taf, kao da ste ih sami prekinuli.",
	"bg":    "Изтриването на чат с Coach прекратява и връзките, които е поискал и които не използва нито един taf, все едно сте ги прекъснали.",
	"mk":    "Бришењето разговор со Coach ги прекинува и врските што ги побарал, а што не ги користи ниеден taf, како да сте ги исклучиле.",
	"ro":    "Ștergerea unei conversații cu Coach încheie și conexiunile pe care le-a cerut și pe care nu le folosește niciun taf, ca și cum le-ați deconecta.",
	"el":    "Η διαγραφή μιας συνομιλίας με τον Coach τερματίζει και τις συνδέσεις που ζήτησε και δεν χρησιμοποιεί κανένα taf, σαν να τις αποσυνδέσατε.",
	"hu":    "Egy Coach-csevegés törlése azokat a kapcsolatokat is megszünteti, amelyeket kért, és amelyeket egyetlen taf sem használ, mintha Ön szüntetné meg őket.",
	"et":    "Vestluse kustutamine Coachiga lõpetab ka ühendused, mida see küsis ja mida ükski taf ei kasuta, nagu katkestaksite need ise.",
	"lt":    "Ištrynus pokalbį su Coach, baigiami ir jo paprašyti ryšiai, kurių nenaudoja joks taf, tarsi juos atjungtumėte.",
	"lv":    "Dzēšot sarunu ar Coach, beidzas arī tās pieprasītie savienojumi, ko neizmanto neviens taf, it kā jūs tos atvienotu.",
	"tr":    "Coach ile bir sohbeti silmek, istediği ve hiçbir taf'ın kullanmadığı bağlantıları da, siz kesmişsiniz gibi sona erdirir.",
	"az":    "Coach ilə söhbəti silmək onun istədiyi və heç bir taf-ın istifadə etmədiyi əlaqələri də, siz onları ayırmış kimi bitirir.",
	"tk":    "Coach bilen söhbetdeşligi pozmak onuň soran we hiç bir taf-yň ulanmaýan birikmelerini hem, siz olary aýyran ýaly, tamamlaýar.",
	"hy":    "Coach-ի հետ զրույցը ջնջելը դադարեցնում է նաև այն կապերը, որոնք նա խնդրել է, և որոնք ոչ մի taf չի օգտագործում, կարծես դուք դրանք անջատեիք։",
	"ka":    "Coach-თან საუბრის წაშლა ასრულებს იმ კავშირებსაც, რომლებიც მან მოითხოვა და რომლებსაც არცერთი taf არ იყენებს, თითქოს თქვენ გათიშეთ ისინი.",
	"sq":    "Fshirja e një bisede me Coach mbyll edhe lidhjet që ai kërkoi dhe që nuk i përdor asnjë taf, sikur t'i kishit shkëputur ju.",
	"he":    "מחיקת שיחה עם Coach מסיימת גם את החיבורים שהיא ביקשה ושאף taf לא משתמש בהם, כאילו ניתקתם אותם.",
	"ar":    "يؤدي حذف محادثة مع Coach أيضًا إلى إنهاء الاتصالات التي طلبتها ولا يستخدمها أي taf، كما لو أنك فصلتها.",
	"hi":    "Coach के साथ चैट हटाने से वे कनेक्शन भी खत्म हो जाते हैं जो उसने माँगे थे और जिन्हें कोई taf उपयोग नहीं करता, मानो आपने उन्हें डिस्कनेक्ट किया हो।",
	"ne":    "Coach सँगको च्याट मेटाउँदा यसले माग गरेका र कुनै taf ले प्रयोग नगर्ने जडानहरू पनि, तपाईंले डिस्कनेक्ट गरे जस्तै, समाप्त हुन्छन्।",
	"ur":    "Coach کے ساتھ چیٹ حذف کرنے سے وہ کنکشن بھی ختم ہو جاتے ہیں جو اس نے مانگے تھے اور جنہیں کوئی taf استعمال نہیں کرتا، گویا آپ نے انہیں ڈسکنیکٹ کیا ہو۔",
	"bn":    "Coach-এর সঙ্গে একটি চ্যাট মুছলে সেটি যে সংযোগগুলো চেয়েছিল এবং যেগুলো কোনো taf ব্যবহার করে না, সেগুলোও শেষ হয়, যেন আপনি সেগুলো বিচ্ছিন্ন করেছেন।",
	"si":    "Coach සමඟ සංවාදයක් මැකීමෙන්, එය ඉල්ලූ සහ කිසිදු taf එකක් භාවිත නොකරන සම්බන්ධතා ද, ඔබ ඒවා විසන්ධි කළාක් මෙන්, අවසන් වේ.",
	"ta":    "Coach உடனான உரையாடலை நீக்கினால், அது கேட்ட, எந்த taf உம் பயன்படுத்தாத இணைப்புகளும், நீங்கள் அவற்றைத் துண்டித்தது போலவே முடிவடையும்.",
	"mn":    "Coach-тай хийсэн чатыг устгавал түүний хүссэн, ямар ч taf ашигладаггүй холболтууд мөн та салгасан мэт дуусна.",
	"tg":    "Нест кардани сӯҳбат бо Coach пайвастҳоеро, ки он дархост кардааст ва ягон taf истифода намебарад, низ қатъ мекунад, гӯё шумо онҳоро қатъ карда бошед.",
	"id":    "Menghapus percakapan dengan Coach juga mengakhiri koneksi yang dimintanya dan tidak dipakai taf mana pun, seolah Anda memutusnya.",
	"vi":    "Xóa một cuộc trò chuyện với Coach cũng kết thúc các kết nối mà nó đã yêu cầu và không taf nào dùng, như thể bạn đã ngắt chúng.",
	"th":    "การลบแชตกับ Coach จะสิ้นสุดการเชื่อมต่อที่แชตนั้นขอไว้และไม่มี taf ใดใช้ด้วย เหมือนกับที่คุณตัดการเชื่อมต่อเอง",
	"km":    "ការលុបការជជែកជាមួយ Coach ក៏បញ្ចប់ការតភ្ជាប់ដែលវាបានស្នើ ហើយគ្មាន taf ណាប្រើ ដូចជាអ្នកផ្តាច់វាដែរ។",
	"lo":    "ການລຶບການສົນທະນາກັບ Coach ກໍສິ້ນສຸດການເຊື່ອມຕໍ່ທີ່ມັນຂໍ ແລະ ບໍ່ມີ taf ໃດໃຊ້ ເຊັ່ນດຽວກັບທີ່ທ່ານຕັດການເຊື່ອມຕໍ່ເອງ.",
	"ko":    "Coach와의 채팅을 삭제하면, 그 채팅이 요청했고 어떤 taf도 사용하지 않는 연결도 연결을 해제한 것처럼 함께 끝납니다.",
	"ja":    "Coachとのチャットを削除すると、そのチャットが求め、どのtafも使っていない接続も、接続を解除したときと同じように終了します。",
	"zh":    "刪除與 Coach 的對話，也會結束該對話所要求、且沒有任何 taf 使用的連線，如同您中斷它們一樣。",
	"dz":    "Coach དང་གྲོས་ཚོགས་བཏོན་གཏང་པ་ཅིན་ དེ་གིས་ཞུ་བ་ཕུལ་མི་དང་ taf གཅིག་གིས་ཡང་ལག་ལེན་མ་འཐབ་མི་མཐུད་མཚམས་ཚུ་ཡང་ ཁྱོད་ཀྱིས་བཅད་དོ་བཟུམ་སྦེ་ མཇུག་བསྡུཝ་ཨིན།",
	"dv":    "Coach އާއެކު ބަހުސެއް ފޮހެލުމުން، އެ ބަހުސުން އެދުނު އަދި އެއްވެސް taf އެއް ބޭނުން ނުކުރާ ގުޅުންތައްވެސް، ތިބާ އެ ކަނޑާލި ފަދައިން ނިމިގެންދެއެވެ.",
}
