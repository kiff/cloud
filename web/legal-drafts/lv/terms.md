# Pakalpojumu noteikumi

Pēdējoreiz atjaunināti: 2026. gada 9. novembrī. Spēkā stāšanās datums: 2026. gada 24. maijs.

Šis tulkojums ir publicēts kopā ar tekstu angļu valodā. Ja tie atšķiras, noteicošais ir teksts angļu valodā.

Kas mainījies: lietotne tafs Android ierīcēm un Solo, kas pirkts Google Play (1. un 8. punkts). Agrāk: maksas plāni (3.A un 8. punkts), [pakalpojumu līmeņa līgums](/legal/sla) (14. punkts) un aizliegto lietojumu saraksts, kas tagad ir [Pieņemamas lietošanas politikā](/acceptable-use) (5. punkts).

Šie noteikumi regulē KIFF Cloud lietošanu, tostarp tafs. Līgumiski jautājumi: [legal@kiff.dev](mailto:legal@kiff.dev).

## 1. Definīcijas
- "Kiff Agents OÜ", "mēs", "mūs", "mūsu": Igaunijas sabiedrība ar ierobežotu atbildību, kas nodrošina Pakalpojumu. Reģistra kods 17372313.
- "Klients", "jūs", "jūsu": fiziskā vai juridiskā persona, kas piekrīt šiem noteikumiem.
- "Lietotājs": jebkura fiziskā persona, kurai Klients atļauj piekļūt Pakalpojumam ar Klienta kontu.
- "Pakalpojums": produkts KIFF Cloud, tostarp api.kiff.dev, app.kiff.dev, kiff.dev, tafs un tur publicētā dokumentācija, vadības paneļi un API.
- "tafs": lietotne tafs iPhone, iPad, Android ierīcēm un datoriem (Mac, Windows un Linux) un pakalpojums, uz kura tā darbojas, tostarp Setup un planque (3.A punkts), kurā Klients uztic darbu mākslīgā intelekta (AI) aģentam, kas strādā KIFF Card ietvaros. "taf" ir viens šāds aģents.
- "Tenant": loģiski nodalīta darba telpa Pakalpojumā, kas pieder vienam Klientam.
- "Audita dati": notikumi, lēmumi, darbības, apstiprinājumi un kvītis (receipts), ko Pakalpojums ar izpildlaika (runtime) starpniecību saglabā Klienta vārdā.
- "tafs dati": tas, ko tafs glabā Klienta vārdā: katra taf palaišanas un tērzēšanas sarunas (tostarp to, ko tam atgriež Klienta pievienotie rīki), tā ziņojumi, atmiņas piezīmes un tā brīdinājumiem reģistrētie tālruņi.
- "Kvīts" (Receipt): parakstīts lēmuma ieraksts ar versiju, ko Pakalpojums izdod par katru pārvaldītu darbību.
- "Dokumentācija": Pakalpojuma tehniskā dokumentācija un drošības pasākumi datētajā redakcijā, uz kuru atsaucas šie Noteikumi ([kiff.dev/docs/archive/2026-10-10](https://kiff.dev/docs/archive/2026-10-10)). Tajā neietilpst stratēģiskā baltā grāmata (whitepaper), mārketinga materiāli un atsevišķi licencētā atvērtā koda ietvara dokumentācija.
- "DPA": Datu apstrādes līgums vietnē [/legal/dpa](/legal/dpa), kas regulē mūsu kā apstrādātāja attiecības.

## 2. Līgums

Šie noteikumi regulē to, kā jūs lietojat Pakalpojumu. Izveidojot kontu, ģenerējot API atslēgu vai citādi lietojot Pakalpojumu, jūs piekrītat šiem noteikumiem. Ja piekrītat uzņēmuma vai citas juridiskas personas vārdā, jūs apliecināt, ka esat tiesīgs šo personu saistīt, un "jūs" un "jūsu" attiecas uz to.

Mēs varam publicēt papildu noteikumus konkrētām funkcijām (beta funkcijām, trešo pušu integrācijām). Ja tie ir pretrunā ar šiem noteikumiem, attiecībā uz konkrēto funkciju noteicošie ir papildu noteikumi.

## 3. Pakalpojums

KIFF Cloud ir mitināts pakalpojums deleģētu darbību pārvaldībai. Tas izvērtē pieprasījumus pret Klienta iestatītajām atļaujām un Card, var pieprasīt cilvēka apstiprinājumu un reģistrē pārvaldības lēmumus. Atkarībā no iestatītās integrācijas KIFF pārsūta atļautu pieprasījumu vai sniedz autorizāciju, ko pārbauda aizsargātā sistēma. Pašu darbību veic pievienotā sistēma.

Piemērojamā tehniskā darbība, pierādījumi, integrācijas prasības un izpildes ierobežojumi ir noteikti datētajā Dokumentācijā, uz kuru atsaucas šie Noteikumi. Stratēģiskā [baltā grāmata](/whitepaper) izskaidro KIFF principus un modeli; tā nav iekļauta šajos Noteikumos kā tehniskā Dokumentācija.

KIFF Cloud pārvaldības pakalpojums nav aģentu ietvars, darbplūsmu dzinējs, modeļu vārteja vai pārvaldīta aģentu platforma. Tas darbojas kopā ar to, ko jūs jau izmantojat. Skaidru sarakstu skatiet sākumlapas sadaļā ["Kas KIFF nav"](/#not). tafs ir vienīgā Pakalpojuma daļa, kas darbina aģentus jūsu vietā, jūsu izsniegto Card ietvaros; to apraksta 3.A punkts.

Atvērtā koda ietvaru [kiff/kiff](https://github.com/kiff/kiff) regulē tā paša MIT licence, un uz to šie noteikumi neattiecas. Ja darbināt ietvaru savā infrastruktūrā, līgums ar Kiff Agents OÜ netiek noslēgts.

## 3A. tafs

tafs jūs uzticat darbu (piemēram, naudas atmaksas, rēķinu apmaksu vai karšu darījumu apstrīdēšanu) taf: AI aģentam, kas strādā pēc grafika ar jūsu pievienotajiem rīkiem, to KIFF Card ietvaros, ko jūs tam izsniedzat. Katrs taf darbojas mūsu aģentu izpildlaikā, un tā izmantoto AI modeli nodrošina Anthropic. Uz tā aizturētajiem izsaukumiem jūs atbildat lietotnē tafs.

Rīku pievienošana. Setup, tafs AI aģents, kas darbojas kā taf, palīdz jums pievienot rīku un iestatīt taf kādam darbam. Pats tas neko nepievieno. Jūs pierakstāties pie paša rīka nodrošinātāja (piemēram, Stripe) un savā ierīcē apstiprināt katru savienojumu, rīkus, ko taf drīkst izmantot, un tā Card. Šīs pierakstīšanās tokenu glabā planque, mūsu savienojumu pakalpojums. Tas ir šifrēts, tiek izmantots tikai, lai veiktu izsaukumu, par kuru KIFF ir lēmusi, un nekad netiek nodots taf. Atvienojot rīku, tokens tiek atsaukts pie nodrošinātāja un dzēsts.

Jūsu Card ir jūsu norādījumi. taf piekļūst jūsu rīkiem tikai caur KIFF, kas pirms nosūtīšanas pārbauda katru izsaukumu pret taf Card. Izsaukums jūsu izsniegtas Card ietvaros tiek veikts pēc jūsu norādījuma. Izsaukums ārpus Card tiek aizturēts vai noraidīts, kā nosaka Card. Aizturēts izsaukums tiek veikts tikai pēc tam, kad to apstiprina kāds, kam jūsu konts ļauj atbildēt uz aizturētiem izsaukumiem. Lietotnē tafs atbildei ir paraksts no viņa ierīces, ko KIFF pārbauda, un kvīts saglabā šo pierādījumu. Jūsu konts var atbildēt arī pašā KIFF (vadības panelī, tās komandrindas rīkā vai ar apstiprinātāju, ko tur pilnvarojat); tad kvītī tiek reģistrēts, kas atbildēja un kā.

Kas paliek jūsu ziņā. Jūs izvēlaties katra taf darbu un Card, atbildat uz tā aizturētajiem izsaukumiem un nodrošināt, ka drīkstat izmantot pievienotos rīkus un kontus. taf ziņojumus, ziņas un atmiņas piezīmes raksta AI modelis, un tie var būt kļūdaini; pārbaudiet svarīgo, pirms uz to paļaujaties.

taf apturēšana. Jūs varat apturēt vienu taf vai visus, iepauzēt taf, atsaukt jebkuru tā Card vai to noņemt. No brīža, kad apturat taf vai atsaucat Card, KIFF neatļauj nevienu jaunu izsaukumu saskaņā ar tiem. Lietotne tafs rāda taf kā apturētu tikai tad, kad apturēšanu ir apstiprinājušas gan KIFF, gan planque; no tā brīža planque nesūta nevienu tā izsaukumu, arī tādu, ko KIFF atļāva tieši pirms tam. Rīka nodrošinātājam jau nosūtīts pieprasījums tomēr var tikt izpildīts: mēs apturam jaunus izsaukumus un nevaram atsaukt to, ko nodrošinātājs jau ir izdarījis. Rīks, kas pats pārbauda KIFF atļaujas (permits) bez planque, vēl var pieņemt atļauju, ko KIFF izdeva tieši pirms tam, ne ilgāk kā 90 sekundes. Apturēta taf atsākšanai nepieciešams jūsu apstiprinājums jūsu ierīcē, un nekas, kas bija aizturēts vai rindā pirms apturēšanas, pēc tās netiek izpildīts.

taf noņemšana aptur tā grafiku un dzēš tā palaišanas, tērzēšanas sarunas, atmiņu un tā izmantoto atslēgu. Jau pirms tam tas, ko jūsu rīki atgriež taf, tiek glabāts tikai līdz tā palaišanas beigām (tas, ko tērzēšanas saruna tikai nolasīja, šifrēti, tās nākamajām ziņām: tiek izmantots ne ilgāk kā četras stundas, pēc tam dzēsts), un tā palaišanas un tērzēšanas sarunas 30 dienas. KIFF saglabā kvītis par to, ko tā darīja.

ES Mākslīgā intelekta akts. Attiecībā uz tafs Kiff Agents OÜ ir AI sistēmas nodrošinātājs, un jūs esat tās uzturētājs. Mēs nodrošinām, ka viss, ko taf raksta kādam ārpus jūsu uzņēmuma, norāda, ka to ir rakstījis AI aģents. Jūs izmantojat tafs tikai tā piedāvātajiem darbiem un ne lietojumam, ko AI akts aizliedz vai klasificē kā augsta riska, piemēram, lēmumiem par personas nodarbinātību, kredītu vai piekļuvi būtiskiem pakalpojumiem. Jūs saglabājat cilvēka apstiprinājumus, ko pieprasa jūsu Card, un nodrošināt, ka cilvēki, kas jūsu vārdā izmanto tafs, saprot, ko tas dara un kādi ir tā ierobežojumi.

Izmaksas. tafs ir daļa no jūsu KIFF plāna (8. punkts): katra taf palaišana izmanto kredītus par tā izmantoto AI. Coach un taf iestatīšana ir iekļauta Coach godīgas lietošanas ietvaros.

## 4. Konti un piekļuve

Lai lietotu Pakalpojumu, jums jāizveido konts. Jūs apņematies sniegt precīzu reģistrācijas informāciju, glabāt savus pierakstīšanās datus slepenībā un atbildēt par visām darbībām savā kontā.

Jums jābūt vismaz 16 gadus vecam vai savas jurisdikcijas digitālās piekrišanas vecumā, atkarībā no tā, kurš ir augstāks. Pakalpojums nav paredzēts bērniem.

Jūs atbildat par katra Lietotāja darbībām, kuram piešķirat piekļuvi savam kontam. Ja konstatējam ticamu šo noteikumu pārkāpumu, mēs varam ar saprātīgu paziņojumu apturēt kontu vai atsaukt piekļuvi. Pieņemamas lietošanas pārkāpumu gadījumā (skatīt 5. punktu) apturēšana var būt tūlītēja.

## 5. Pieņemama lietošana

Jums, jūsu lietotājiem un jūsu aģentiem jāievēro [Pieņemamas lietošanas politika](/acceptable-use). Tā ir daļa no šiem noteikumiem un uzskaita, kam Pakalpojumu nedrīkst izmantot.

Ticama Pieņemamas lietošanas politikas pārkāpuma gadījumā mēs varam nekavējoties apturēt lietošanu. Mums nav pienākuma uzraudzīt Klienta saturu, bet mēs varam rīkoties, pamatojoties uz ticamiem ziņojumiem.

## 6. Klienta dati un saturs

Jūsu Audita dati pieder jums. Jūs piešķirat Kiff Agents OÜ ierobežotu, neekskluzīvu, bezatlīdzības, pasaules mēroga licenci mitināt, kopēt, pārsūtīt, attēlot un citādi apstrādāt jūsu Audita datus tikai tiktāl, cik tas nepieciešams, lai darbinātu, uzturētu un uzlabotu Pakalpojumu jūsu labā.

Attiecībā uz jūsu Audita datiem un tafs datiem mēs darbojamies kā apstrādātājs saskaņā ar VDAR 28. pantu; šīs attiecības sīki regulē [DPA](/legal/dpa). Attiecībā uz jūsu konta metadatiem mēs darbojamies kā pārzinis; šīs attiecības regulē [privātuma politika](/privacy).

Jūs apliecināt, ka jums ir visas tiesības iesniegt datus, ko iesniedzat, un ka to iesniegšana atbilst piemērojamajiem tiesību aktiem (tostarp datu aizsardzības tiesību aktiem un, ja piemērojams, ES Mākslīgā intelekta aktam).

## 7. Konfidencialitāte

Katra puse var saņemt no otras nepublisku informāciju ("Konfidenciāla informācija"). Saņēmēja puse izmanto Konfidenciālo informāciju tikai tiktāl, cik tas nepieciešams, lai pildītu savas saistības un īstenotu savas tiesības saskaņā ar šiem noteikumiem, un aizsargā to ar vismaz tādu pašu rūpību, kādu tā izmanto savai konfidenciālajai informācijai.

Konfidenciāla informācija neietver informāciju, kas (a) ir vai kļūst publiski pieejama bez pārkāpuma, (b) saņēmējai pusei bija zināma pirms izpaušanas, (c) ir izstrādāta neatkarīgi, neizmantojot Konfidenciālo informāciju, vai (d) ir jāizpauž saskaņā ar likumu (ar paziņojumu otrai pusei, ja tas ir atļauts).

## 8. Maksas un maksājumi

Plāni. Katram kontam ir viens plāns. Pašreizējās cenas ir [cenu lapā](/pricing); šī atjauninājuma brīdī:
- Solo: viena persona, tafs lietošanai, pārdod lietotnē tafs caur App Store vai Google Play par 12,50 € mēnesī, ieskaitot PVN, ar 500 iekļautiem kredītiem mēnesī.
- Company: 10,50 € mēnesī par katru vietu, ar 500 kredītiem mēnesī par katru vietu, kopīgi, un jūsu pašu aģentu izsaukumiem caur KIFF Cloud 500 darbībām mēnesī par katru vietu, kopīgi.
- Līgums: maksas un noteikumi tā pasūtījuma veidlapā.

Vietas ir konta administratori un redaktori. Apstiprinātāji un skatītāji ir bez maksas visos plānos. Company plānā pāri iekļautajam kredīts maksā 0,01 € un darbība 0,02 €. Kredītus pāri iekļautajiem iekasē līdz ikmēneša tēriņu limitam, ko iestata konts (sākumā 50 €): sasniedzot limitu, KIFF nesāk nevienu jaunu konta taf palaišanu, līdz sākas nākamais periods vai kāds, kas pārvalda norēķinus, limitu paaugstina.

Kredītu paketes. Solo plānā kredīti pāri mēneša iekļautajiem nāk no kredītu paketēm, ko pērk lietotnē tafs. Paketi pērk vienreiz, tai nav derīguma termiņa, un tā nav abonements; tās kredītus izmanto pēc mēneša iekļautajiem, sākot ar vecāko paketi. Pakete, kuras maksājums ir pilnībā atmaksāts, tiek atņemta kopā ar tajā atlikušajiem kredītiem. Kad mēneša kredīti un paketes ir izlietoti, KIFF nesāk nevienu jaunu konta taf palaišanu, līdz sākas nākamais periods vai tiek pievienota pakete. Arī bonusa kredītiem, ko KIFF pievieno kontam, tāpat kā paketēm nav derīguma termiņa.

Kas tiek skaitīts. Kredīts apmaksā AI, ko izmantoja taf palaišana, veselos kredītos, noapaļojot uz augšu vienu reizi palaišanā, un tiek skaitīts vienreiz, kad palaišana beidzas. Palaišana, kuras izsaukumu KIFF noraidīja, tomēr izmantoja savu AI. Nekad netiek skaitīts: Coach un taf iestatīšana Coach godīgas lietošanas ietvaros; jaunas versijas izmēģinājums, kas neko nesūta; un palaišana, kas neizdevās mūsu pusē. Darbība ir jūsu pašu aģenta izsaukums caur KIFF Cloud, kas kaut ko paveica: KIFF to atļāva, tas tika nosūtīts, un rīks ziņoja par panākumiem. Nekad netiek skaitīts kā darbība: izsaukums, kas tika noraidīts, aizturēts un pēc tam noraidīts, apturēts apturēšanas dēļ, nederīgs, neizdevās rīkā vai kura iznākums nav zināms, kā arī izsaukums, kas tikai nolasa.

Bez izmēģinājuma perioda. Konts maksā no pirmās dienas. Pirms plāns sākas, KIFF nesāk nevienu tā taf palaišanu, izņemot Coach veikto iestatīšanu; nekas netiek dzēsts.

Maksājumi un rēķini. Par plānu norēķinās vienā vietā. Vietnē kiff.dev pirktu plānu tur arī pērk, maina un atceļ, kā arī norēķinu portālā, uz kuru tā saite ved, un tā maksājumus apstrādā Stripe. Lietotnē tafs pirktais Solo tiek pārdots caur App Store iPhone un iPad ierīcēs vai Google Play Android ierīcēs, un to pārvalda jūsu Apple vai Google Play abonementos saskaņā ar attiecīgā veikala maksājumu, atjaunošanas un atmaksas noteikumiem. Kredītu paketes pērk lietotnē. Company pērk vietnē kiff.dev. Maksas iekasē reizi mēnesī avansā; lietojumu pāri iekļautajam iekļauj nākamajā rēķinā. Cilvēku skaita izmaiņas tiek piemērotas no nākamā rēķina. Ja maksājums neizdodas un netiek nokārtots, plāns beidzas, un KIFF noraida konta izsaukumus, līdz tiek izvēlēts plāns.

Atcelšana. Jūs varat atcelt jebkurā laikā norēķinu portālā vai, ja Solo pirkts lietotnē, savos Apple vai Google Play abonementos. Plāns darbojas līdz apmaksātā perioda beigām un netiek atjaunots. Izņemot gadījumus, kad to pieprasa likums, jau samaksātās maksas netiek atmaksātas. Ja esat patērētājs ES, jūs varat atkāpties no pirkuma 14 dienu laikā, nenorādot iemeslu, un mēs atmaksājam samaksāto, atskaitot daļu par dienām, kurās plāns jau darbojās. Solo pirkumus atmaksā caur App Store vai Google Play saskaņā ar to noteikumiem.

PVN. Company cenās nav iekļauts PVN; Solo un kredītu paketes pārdod App Store vai Google Play ar iekļautu PVN. Kiff Agents OÜ (PVN numurs EE102924327) pievieno Igaunijas PVN, ja tas piemērojams. ES uzņēmumu savstarpējiem darījumiem ar Klientu ārpus Igaunijas, kuram ir derīgs PVN identifikācijas numurs, rēķinu izraksta saskaņā ar ES apgrieztās maksāšanas mehānismu.

Izmaiņas. Par cenu paaugstināšanu mēs aktīvajiem kontiem rakstiski paziņojam vismaz trīsdesmit dienas iepriekš (16. punkts). Cenu samazinājums tiek piemērots no nākamā rēķina.

## 9. Termiņš un izbeigšana

Šie noteikumi stājas spēkā, kad jūs pirmo reizi lietojat Pakalpojumu, un ir spēkā līdz to izbeigšanai.

Jūs varat izbeigt līgumu jebkurā laikā, dzēšot savu kontu. Mēs varam to izbeigt (a) būtiska pārkāpuma dēļ, kas nav novērsts trīsdesmit dienu laikā pēc rakstiska paziņojuma, vai (b) nekavējoties Pieņemamas lietošanas pārkāpumu vai maksātnespējas gadījumu dēļ.

Aiziešana un datu paņemšana līdzi. Jūs varat jebkurā laikā eksportēt savus datus strukturētā, mašīnlasāmā formātā: KIFF sadaļa Settings → Data sniedz JSON failu ar jūsu kontu, jūsu Card ar katru to versiju, lēmumiem jūsu Record (jaunākajiem 10 000; vairāk varat lūgt mums), parakstītu kvīti par katru, ko KIFF atļāva, un jūsu API atslēgu informāciju (nekad pašas atslēgas). Lietotnē tafs katra persona eksportē savas tērzēšanas sarunas, failus un piezīmes sadaļā Account → Export my chats; tērzēšanas sarunas jebkurā gadījumā tiek glabātas trīsdesmit dienas, kā norādīts privātuma politikā. Konta dzēšana nekavējoties izbeidz Pakalpojumu, tāpēc vispirms eksportējiet datus.

Tā vietā jūs varat aiziet ar paziņojumu, lai pārietu pie cita nodrošinātāja vai uz savām sistēmām, rakstot uz [support@kiff.dev](mailto:support@kiff.dev). Mēs nekad nepieprasām paziņojumu, kas garāks par diviem mēnešiem: jūsu paziņojums stājas spēkā jūsu izvēlētajā datumā, ne vēlāk kā divus mēnešus pēc tā nosūtīšanas. No šī datuma seko trīsdesmit dienu pārejas periods, kurā Pakalpojums turpina darboties, jūs eksportējat savus datus un pārceļat savu darbu, un mēs palīdzam ar saprātīgiem lūgumiem šajā nolūkā. Jūs varat pārejas periodu vienreiz pagarināt, cik ilgi jums nepieciešams. Ja tehniski nevaram to pabeigt trīsdesmit dienās, mēs to paziņojam četrpadsmit darba dienu laikā pēc jūsu paziņojuma, norādot iemeslus un jaunu periodu, kas nav garāks par septiņiem mēnešiem. Pēc pārejas perioda jūsu dati paliek pieejami eksportam vēl vismaz trīsdesmit dienas. Plāna maksas turpinās kā parasti līdz pārejas perioda beigām. Solo, kas pirkts App Store vai Google Play, atceļ veikalā; pārejas un eksporta periodi tiek piemēroti tāpat.

Mēs neiekasējam maksu par aiziešanu, pāreju vai datu eksportu. Šī sadaļa piešķir ikvienam klientam ES Datu akta (Regula (ES) 2023/2854) VI nodaļā paredzētās pārejas tiesības.

Pēc izbeigšanas un pēc jebkura pārejas un eksporta perioda mēs dzēšam jūsu Audita datus, jūsu tafs datus un konta metadatus saskaņā ar [privātuma politikas](/privacy#retention) glabāšanas noteikumiem.

Sadaļas, kurām pēc to būtības jāpaliek spēkā pēc izbeigšanas, paliek spēkā, tostarp par konfidencialitāti, intelektuālo īpašumu, zaudējumu atlīdzināšanu, atbildības ierobežošanu, atrunām, piemērojamajiem tiesību aktiem un jebkurām uzkrātajām maksām.

## 10. Garantijas

Katra puse apliecina, ka tai ir tiesības un pilnvaras noslēgt un izpildīt šos noteikumus.

Mēs garantējam, ka nodrošināsim Pakalpojumu profesionāli un būtiskā atbilstībā Dokumentācijai. Jūsu vienīgais tiesiskās aizsardzības līdzeklis šīs garantijas pārkāpuma gadījumā ir, pēc mūsu izvēles, atkārtota izpilde vai proporcionāla to maksu atmaksa, kas attiecas uz ietekmēto Pakalpojumu pārkāpuma periodā.

Jūs garantējat, ka jūsu Pakalpojuma lietošana atbilst piemērojamajiem tiesību aktiem un šiem noteikumiem.

## 11. Atruna

Izņemot 10. punktā skaidri paredzētās garantijas, Pakalpojums tiek nodrošināts "tāds, kāds tas ir" un "kā pieejams". Maksimāli likumā atļautajā apjomā mēs atsakāmies no visām citām garantijām, gan skaidri izteiktām, gan netiešām, gan likumā noteiktām, tostarp no piemērotības pārdošanai, piemērotības konkrētam mērķim, tiesību neaizskaršanas garantijām un jebkurām garantijām, kas izriet no darījumu prakses vai tirdzniecības paražām.

Mēs negarantējam, ka Pakalpojums darbosies bez pārtraukumiem, bez kļūdām vai bez kaitīgiem komponentiem, ne arī to, ka dati netiks zaudēti vai bojāti. Jūs atbildat par savu datu rezerves kopiju veidošanu.

## 12. Atbildības ierobežojums

Maksimāli likumā atļautajā apjomā neviena puse neatbild otrai par netiešiem, nejaušiem, izrietošiem, īpašiem, sodošiem vai priekšzīmīgiem zaudējumiem, tostarp par negūto peļņu, zaudētiem ieņēmumiem, zaudētiem datiem vai darbības pārtraukumu, kas izriet no šiem noteikumiem vai Pakalpojuma vai ir ar tiem saistīti, pat ja tā bija informēta par šādu zaudējumu iespējamību.

Katras puses kopējā atbildība, kas izriet no šiem noteikumiem vai ir ar tiem saistīta, nepārsniedz lielāko no šīm summām: (a) maksas, ko Klients samaksāja vai kas tam jāmaksā Kiff Agents OÜ divpadsmit mēnešos pirms notikuma, kas rada atbildību, vai (b) simt tūkstoši eiro (100 000 €).

Nekas šajos noteikumos neierobežo un neizslēdz atbildību, ko nevar ierobežot vai izslēgt saskaņā ar piemērojamajiem tiesību aktiem, tostarp atbildību par nāvi, nolaidības izraisītu miesas bojājumu, krāpšanu vai tīšu pārkāpumu, kā arī atbildību par produktu ar trūkumiem saskaņā ar tiesību aktiem, ar kuriem transponēta Direktīva (ES) 2024/2853 par atbildību par produktiem ar trūkumiem.

## 13. Zaudējumu atlīdzināšana

Mēs jūs aizstāvēsim pret trešo personu prasībām, ka Pakalpojums, kādu mēs to nodrošinām un kādu jūs to lietojat saskaņā ar šiem noteikumiem, aizskar šīs trešās personas intelektuālā īpašuma tiesības, un samaksāsim zaudējumus, kas pret jums piespriesti ar galīgu nolēmumu (vai saskaņoto izlīguma summu) par šādām prasībām.

Jūs mūs aizstāvēsiet pret trešo personu prasībām, kas izriet no (a) jūsu kā Klienta datiem vai satura; (b) jūsu Pakalpojuma lietošanas, pārkāpjot šos noteikumus (tostarp Pieņemamas lietošanas politiku); vai (c) jūsu rupjas nolaidības vai tīša pārkāpuma, un samaksāsiet zaudējumus, kas pret mums piespriesti ar galīgu nolēmumu (vai saskaņoto izlīguma summu) par šādām prasībām.

Zaudējumu atlīdzināšanas priekšnoteikums ir tūlītējs paziņojums par prasību, atlīdzinātājas puses ekskluzīva kontrole pār aizstāvību un izlīgumu un atlīdzības saņēmējas puses saprātīga sadarbība. Atlīdzinātāja puse nedrīkst bez piekrišanas noslēgt izlīgumu, kas uzliek pienākumus atlīdzības saņēmējai pusei.

## 14. Pakalpojumu līmeņa apņemšanās

Maksas plānā [pakalpojumu līmeņa līgums](/legal/sla) nosaka pieejamību, ko mēs apņemamies nodrošināt, kā tiek ieskaitīts lēns vai neizdevies pieprasījums, mūsu atbalsta reaģēšanas laikus un pakalpojumu kredītus, ja mēs neizpildām apņemšanos. Tas ir daļa no šiem noteikumiem.

Darbības prakse, reaģēšana uz incidentiem un apņemšanās paziņot par pārkāpumiem ir aprakstītas datētajā redakcijā, uz kuru atsaucas šie Noteikumi ([kiff.dev/docs/archive/2026-10-10](https://kiff.dev/docs/archive/2026-10-10)); lapā [/security](/security) redzama pašreizējā prakse.

## 15. Pakalpojuma izmaiņas

Mēs varam jebkurā laikā pievienot, mainīt vai noņemt funkcijas. Par būtisku funkcionalitātes samazināšanu vai tādas API daļas novecošanu, kas izjauc dokumentētu darbību, aktīvajiem klientiem paziņo vismaz trīsdesmit dienas iepriekš, publicējot novecošanas grafiku. Drošības nolūkā izmaiņas var veikt bez paziņojuma, ja pats paziņojums radītu risku.

## 16. Šo noteikumu izmaiņas

Mēs laiku pa laikam varam grozīt šos noteikumus. Par būtiskām izmaiņām paziņo pa e-pastu un ar labi redzamu paziņojumu šajā lapā vismaz trīsdesmit dienas iepriekš. Nebūtiskas izmaiņas (precizējumi, atsauces) publicē ar jaunu "pēdējoreiz atjaunināts" datumu. Ja jūs turpināt lietot Pakalpojumu pēc atjauninājuma spēkā stāšanās datuma, tas nozīmē, ka jūs to pieņemat.

## 17. Nepārvarama vara

Neviena puse neatbild par saistību neizpildi vai kavējumu tiktāl, ciktāl to izraisījuši apstākļi ārpus tās saprātīgas kontroles, tostarp dabas stihijas, karš, terorisms, masu nekārtības, darba strīdi, ugunsgrēks, dabas katastrofas, valdības rīcība saistībā ar pandēmiju, interneta vai elektroapgādes pārtraukumi vai telekomunikāciju nodrošinātāju vai trešo pušu mitināšanas infrastruktūras darbības.

## 18. Piemērojamie tiesību akti un jurisdikcija

Šiem noteikumiem piemēro Igaunijas Republikas tiesību aktus, neņemot vērā tās kolīziju normas. Ekskluzīvā strīdu izskatīšanas vieta ir Harju apriņķa tiesa (Harju Maakohus) Tallinā.

Ja esat patērētājs, kura dzīvesvieta ir citā ES dalībvalstī, jūsu mītnes valsts obligātie patērētāju aizsardzības noteikumi joprojām ir spēkā tiktāl, ciktāl tie piešķir jums tiesības, no kurām nevarat atteikties ar līgumu; šis noteikums neatņem jums šo noteikumu aizsardzību.

## 19. Vispārīgi noteikumi

Viss līgums. Šie noteikumi kopā ar privātuma politiku, DPA, jebkurām pasūtījuma veidlapām un jebkuriem skaidri iekļautiem papildu noteikumiem ir viss mūsu līgums par to priekšmetu un aizstāj jebkuru iepriekšēju vai vienlaicīgu vienošanos.

Atdalāmība. Ja kāds noteikums tiek atzīts par nepiemērojamu, pārējie paliek spēkā.

Cesija. Jūs nedrīkstat nodot šos noteikumus bez mūsu rakstiskas piekrišanas. Mēs varam tos nodot bez piekrišanas saistībā ar apvienošanu, iegādi vai visu vai būtībā visu mūsu aktīvu pārdošanu, par to jums paziņojot.

Neatteikšanās. Kāda noteikuma neizpildes pieprasīšanas neizmantošana nenozīmē atteikšanos no tiesībām to pieprasīt vēlāk.

Paziņojumi. Paziņojumus Kiff Agents OÜ sūta uz [legal@kiff.dev](mailto:legal@kiff.dev) vai pa pastu uz adresi Tööstuse tn 75-71, Põhja-Tallinna linnaosa, Tallinn 10416, Igaunija. Paziņojumus jums sūta uz e-pasta adresi, kas saistīta ar jūsu kontu, vai, ja esat to norādījis, uz jūsu norādīto juridisko paziņojumu adresi.

Neatkarīgi līgumslēdzēji. Nekas šajos noteikumos nerada partnerības, pārstāvības, kopuzņēmuma vai darba attiecības.

Trešās personas labuma guvēji. Tādu nav, izņemot tiktāl, ciktāl skaidri norādīti saistītie uzņēmumi, kuriem tiek atlīdzināti zaudējumi.

## 20. Sazinieties ar mums

Komerciāli un līgumiski jautājumi: [legal@kiff.dev](mailto:legal@kiff.dev).

Vispārīgi jautājumi par produktu: [hello@kiff.dev](mailto:hello@kiff.dev). Privātuma jautājumi: [privacy@kiff.dev](mailto:privacy@kiff.dev). Ziņojumi par drošības problēmām: [security@kiff.dev](mailto:security@kiff.dev).

Dokumenta versija: v0.1. Pēdējoreiz atjaunināti: 2026. gada 9. novembrī.
