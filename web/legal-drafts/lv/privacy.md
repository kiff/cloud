# Privātuma politika

Pēdējoreiz atjaunināta: 2026. gada 17. novembrī. Spēkā stāšanās datums: 2026. gada 24. maijs.

Šis tulkojums ir publicēts kopā ar tekstu angļu valodā. Ja tie atšķiras, noteicošais ir teksts angļu valodā.

Šī politika izskaidro, kā KIFF Cloud apstrādā personas datus tīmekļa vietnē, vadības panelī, API un lietotnē tafs iPhone, iPad, Android ierīcēm un datoriem. Jautājumi: [privacy@kiff.dev](mailto:privacy@kiff.dev).

## 1. Kas mēs esam

KIFF Cloud darbina Kiff Agents OÜ, Igaunijā reģistrēta sabiedrība ar ierobežotu atbildību. Reģistra informācija:
- Reģistra kods: 17372313 (Igaunijas e-komercreģistrs, e-Business Register)
- PVN numurs: EE102924327
- Juridiskā adrese: Tööstuse tn 75-71, Põhja-Tallinna linnaosa, Tallinn 10416, Igaunija
- Kontakti privātuma jautājumos: [privacy@kiff.dev](mailto:privacy@kiff.dev)
- Oficiāli juridiski paziņojumi: [legal@kiff.dev](mailto:legal@kiff.dev)

Tā kā Kiff Agents OÜ ir reģistrēta Eiropas Savienībā, ES Vispārīgā datu aizsardzības regula (VDAR) uz mūsu apstrādi attiecas tieši. Mums nav nepieciešams ES pārstāvis saskaņā ar 27. pantu.

## 2. Šīs politikas darbības joma

Šī politika attiecas uz personas datiem, ko mēs apstrādājam, kad jūs izmantojat:
- [kiff.dev](https://kiff.dev), mūsu mārketinga vietni, šo politikas lapu un publisko kvīšu attēlošanas lapu.
- [app.kiff.dev](https://app.kiff.dev), mūsu vadības paneli, kam nepieciešama pierakstīšanās.
- [api.kiff.dev](https://api.kiff.dev), mūsu HTTP API.
- tafs: mūsu lietotni iPhone, iPad, Android ierīcēm un datoriem (Mac, Windows un Linux) un pakalpojumu, uz kura tā darbojas (tostarp Setup, kas pievieno uzņēmuma rīkus, un planque, kas glabā šos savienojumus), kurā uzņēmums uztic darbu AI aģentam, kas strādā KIFF Card ietvaros.

Šī politika neattiecas uz atvērtā koda ietvaru kiff/kiff, ko varat darbināt pats. Ja darbināt ietvaru savā infrastruktūrā, jūs esat pārzinis visiem datiem, ko tas apstrādā, un Kiff Agents OÜ šajā apstrādē nepiedalās.

## 3. Mūsu lomas saskaņā ar VDAR

Atkarībā no datiem mēs darbojamies divās lomās:
- Pārzinis attiecībā uz konta un pierakstīšanās metadatiem, norēķinu datiem (kad norēķini ir aktīvi), mārketinga paziņojumiem, kuriem jūs piesakāties, un mūsu pašu pakalpojuma darbības telemetriju.
- Apstrādātājs attiecībā uz audita datiem, darbību ierakstiem, lēmumu kvītīm un citu saturu, kas plūst caur izpildlaiku (runtime) mūsu klientu vārdā, un attiecībā uz to, ko tafs viņiem glabā (katra taf palaišanas, tērzēšanas sarunas, ziņojumi, atmiņas piezīmes un faili, ko tie piegādā). Klienti (tenants) ir šo datu pārziņi; viņi sniedz mums norādījumus ar sava konta iestatījumiem un domēna konfigurāciju. Šīs attiecības regulē Datu apstrādes līgums (DPA); publicētie noteikumi ir [DPA veidnē](/legal/dpa).

Šī politika galvenokārt attiecas uz mūsu kā pārziņa veikto apstrādi. Ja minam datus, ko apstrādājam kā apstrādātājs, konkrētākais dokuments ir DPA.

## 4. Mūsu apstrādāto personas datu kategorijas

Turpmāk norādītās kategorijas atbilst tehniskajam sarakstam lapā [/security#data-we-process](/security#data-we-process). Ja tie atšķiras, noteicošā ir tehniskā lapa, un mēs šo sadaļu atjauninām atbilstoši.
- Konta identifikatori: jūsu autentifikācijas subjekts (ko izdod mūsu identitātes nodrošinātājs) un e-pasta adrese, ar kuru pierakstāties. Pašlaik tiek glabāti nemainītā veidā; jaucējvērtību (hash) glabāšana ir plānota.
- Tenant metadati: tenant identifikators, slug, attēlojamais nosaukums, izveides laikspiedols un valoda, ko katras personas lietotne izmantoja pēdējo reizi, lai e-pasta vēstules, ko KIFF viņai sūta, būtu šajā valodā.
- API pierakstīšanās dati: necaurredzami API atslēgu ieraksti (mēs glabājam jaucējvērtības, nekad atklāto tekstu).
- Sesijas sīkdatnes dati: ar HMAC parakstīta sesijas sīkdatne, ko iestata pēc pierakstīšanās. Skatīt sadaļu par sīkdatnēm turpmāk.
- HTTP pieprasījumu metadati: IP adrese, lietotāja aģents (user agent), pieprasījuma ceļš, atbildes statuss, latentums. Tiek glabāti mūsu reversā starpniekservera (reverse proxy) piekļuves žurnālos ierobežotu laiku drošības un ļaunprātīgas izmantošanas novēršanas nolūkā.
- Tenant kontrolēts saturs: parametri, darbības veicējs un entītiju dati, ko sūtāt izpildlaikam. Mēs tos glabājam kā apstrādātājs klienta vārdā; klients atbild par to, kas tiek nosūtīts. Rediģēšanas (redaction) konfigurācija katram domēnam ir plānota.
- Saziņa: e-pasta vēstules, ko sūtāt uz mūsu atbalsta, pārdošanas, drošības vai privātuma pastkastēm.
- Lietotne tafs: tafs pierakstāties ar savu KIFF kontu. Tālrunī vai planšetdatorā lietotne glabā jūsu sesiju ierīces Keychain (Android ierīcē — tās šifrētajā krātuvē). Datorā tā to glabā aizzīmogotu jūsu lietotāja kontam: Windows datorā ar pašas Windows datu aizsardzību, Linux — jūsu keyring, Mac — tā Keychain. Datora sesija ilgst līdz 90 dienām. Lietotne izveido parakstīšanas atslēgu, kas nekad neatstāj ierīci: iPhone, iPad vai Mac ierīces Secure Enclave, Android ierīces Keystore vai Windows datora TPM. Linux un Mac bez Secure Enclave atslēga ir programmatūras atslēga, kas glabājas jūsu keyring vai Keychain, un šis dators var parādīt, kas jūs gaida, bet nevar to apstiprināt; jūs apstiprināt savā tālrunī. Windows dators bez TPM nevar pierakstīties tafs. KIFF glabā atslēgas publisko daļu, ierīces nosaukumu un platformu, kā arī to, kad tā pievienota un pēdējoreiz izmantota, lai varētu pārbaudīt jūsu atbildes uz aizturētiem izsaukumiem un jūsu savienojumu apstiprinājumus. Ja apstiprināt ar piekļuves atslēgu (passkey) savā datorā, piemēram, Windows Hello vai drošības atslēgu, jūsu seja, pirksta nospiedums vai PIN paliek datora paša sistēmā; KIFF glabā piekļuves atslēgas publisko atslēgu, tās identifikatoru, skaitītāju un nosaukumu, nekad neko slepenu, un noraida piekļuves atslēgu, kas sinhronizējas ar citām ierīcēm. Ja ieslēdzat brīdinājumus tālrunī, planšetdatorā vai Mac, kurā darbojas iPad lietotne, mēs glabājam ierīces push žetonu kopā ar jūsu KIFF personas identifikatoru un valodu, ko rādīja lietotne, lai tās brīdinājumi būtu šajā valodā. Pakalpojums tafs glabā arī konta valodu, no tā, kurš pēdējais tērzēja ar Coach, līdz trim mēnešiem, lai taf ziņojumi būtu šajā valodā. Tālruņa brīdinājums iet caur Apple vai Google tikai ar to, kurš taf un ko tas vēlas, un durvju (door) brīdinājumā ar tā cilvēka vārdu vai e-pasta adresi, kurš rakstīja: nekad ar summu vai pieprasījuma informāciju. Lietotne tās pievieno pašā ierīcē, no KIFF, lai atbloķēta tālruņa brīdinājums varētu rādīt summu, tirgotāju vai personu. Bloķētā Android tālrunī tas rāda tikai "A taf needs you", un bloķētā iPhone tāpat, ja vien neesat iestatījis, lai tā priekšskatījumi tiktu rādīti vienmēr. Jūsu pulkstenis: pulkstenis neglabā paroli vai atslēgu, sazinās tikai ar jūsu savienoto tālruni un nekad neapstiprina. Tas sūta to, ko sakāt Coach vai jūsu izvēlētajam taf, tikai kā tekstu, caur tālruni, kad esat atļāvis AI. Pulksteņa paša runas pakalpojums, Apple vai Google, pārvērš jūsu vārdus šajā tekstā. Tas var noraidīt, apturēt vai sūtīt Coach vai taf tikai tad, ja tam ir ekrāna bloķēšana vai piekļuves kods un tas ir atbloķēts. Tas, ko sūtāt taf, nonāk šī taf paša tērzēšanas sarunā, it kā jūs to būtu rakstījis tālrunī. taf tērzēšanas saruna, ieskaitot summas, pulkstenī tiek rādīta tikai tad, kad tā ir atvērta, katru reizi nolasot to caur tālruni; pulkstenis neko no tās neglabā. Apple Watch: tas rāda jūsu taf un to lapas, tās neglabājot. Tas glabā tikai jūsu Inbox failā, ko tas nevar nolasīt, kamēr ir bloķēts: katru aizturēto izsaukumu kā tā taf nosaukumu un to, ko tas prasa, nekad summu, un Coach atbildes uz to, ko sūtījāt no pulksteņa, kā tekstu. Ja izrakstāties pulkstenī, tas glabā arī to, ka to izdarījāt, līdz atkal pierakstāties pulkstenī. Izņemot to, tas neko neglabā, ja vien pulkstenim nav piekļuves koda. Inbox tiek dzēsts, kad izrakstāties pulkstenī vai jūsu iPhone izrakstās, vai nākamreiz, kad lietotne tiek atvērta vai aizvērta bez piekļuves koda. Tas neveic ierakstus, un vienīgie paziņojumi, ko tas rāda, ir jūsu iPhone paziņojumi. Wear OS: tas rāda jūsu konta e-pastu, šodienas kalendāru un jūsu taf lapas, tās neglabājot. Tas glabā jūsu Inbox, kur katra rinda ir taf nosaukums un tas, ko tas saka, nekad summa, un vienu vēlākam laikam saglabātu ziņu, kā tekstu. Izrakstīšanās pulkstenī vai tālrunī to visu dzēš. Savai flīzei (tile) tas glabā arī to, cik daudz jūs gaida, ko rāda tikai tad, kad pulkstenis ir atbloķēts, un dzēš, kad izrakstāties, vai nākamreiz, kad flīze tiek rādīta, kamēr pulkstenis ir bloķēts vai tam nav ekrāna bloķēšanas. Pēc izrakstīšanās pulkstenī tas glabā piezīmi, ka to izdarījāt, līdz atkal pierakstāties. Inbox tiek dzēsts arī nākamreiz, kad lietotne tiek atvērta vai aizvērta, kamēr pulkstenis ir bloķēts vai tam nav ekrāna bloķēšanas. Vēlākam laikam saglabāta ziņa paliek, līdz to nosūtāt vai dzēšat, jo jūs lūdzāt to nosūtīt. Pulkstenis neglabā ierakstus. taf rezultāts var palikt pulksteņa paziņojumos, līdz to aizverat vai izrakstāties pulkstenī. Windows datorā, Linux datorā vai Mac ar macOS 12 lietotni brīdinājumus veido pati lietotne, kamēr tā darbojas, bez push žetona un bez jebkāda cita uzņēmuma. Windows un Linux tie rāda, kurš taf, ko tas vēlas darīt, summu, kāpēc un līdz kuram laikam; macOS 12 lietotnē tie rāda KIFF formulējumu par izsaukumu, kad tos ieslēdzat, un bloķēts ekrāns rāda to, ko atļauj Mac priekšskatījuma iestatījums. Windows tie ir izslēgti, līdz tos ieslēdzat, un, kamēr ekrāns ir bloķēts, rāda tikai "A taf needs you". Paziņojums, kas parādīts pirms ekrāna bloķēšanas, saglabā savu informāciju Windows paziņojumu sarakstā. Linux tie ir ieslēgti, līdz tos izslēdzat, un tas, ko rāda bloķēts ekrāns, ir atkarīgs no jūsu darbvirsmas vides. Kad pievienojat rīku, jūs pierakstāties pie paša rīka nodrošinātāja. Lietotnei, kurā planque pierakstās pats, piemēram, Stripe, vai jūsu sniegtai atslēgai planque glabā šo žetonu vai atslēgu jūsu uzņēmumam, šifrētu; pārvaldītam savienojumam pierakstīšanos glabā Composio, savienojumu pakalpojums, ar kuru strādā planque, un planque glabā tikai savienojuma ierakstu un jūsu apstiprinātos rīkus (visu kā apstrādātājs, skatīt turpmāk). KIFF izmanto pārvaldītus savienojumus tikai lietotnēm, kas norādītas lapā [/subprocessors](/subprocessors#composio). Lietotnē nav analītikas vai reklāmas izsekotāju.
- App Store pirkumi: kad tafs pērkat Solo vai kredītu paketi, to jums pārdod Apple: Apple ir pārdevējs, iekasē maksājumu un glabā jūsu maksājumu informāciju. Apple mums paziņo, kas tika nopirkts, kuram KIFF kontam (nejaušs identifikators, ko lietotne pievieno pirkumam), kad, Apple darījuma identifikatoru un, Solo gadījumā, kad periods beidzas vai tiek atjaunots. Mēs to izmantojam, lai pievienotu kredītus vai plānu jūsu kontam. Apple mums nesniedz jūsu Apple ID, vārdu, e-pasta adresi vai karti.
- Google Play pirkumi: Android ierīcēs tie paši pirkumi notiek ar Google Play norēķinu sistēmu: Google iekasē maksājumu un glabā jūsu maksājumu informāciju. Google mums paziņo, kas tika nopirkts, kuram KIFF kontam (tas pats nejaušais identifikators), kad, pirkuma žetonu un pasūtījuma identifikatoru un, Solo gadījumā, kad periods beidzas vai tiek atjaunots, un mēs to izmantojam tāpat. Google mums nesniedz jūsu Google kontu, vārdu, e-pasta adresi vai karti.

## 5. Apstrādes tiesiskais pamats

Saskaņā ar VDAR 6. pantu mēs balstāmies uz šādiem tiesiskajiem pamatiem:
- Līguma izpilde (6. panta 1. punkta b) apakšpunkts) konta izveidei, autentifikācijai, izpildlaika darbināšanai jūsu vārdā, norēķiniem (kad tie ir aktīvi) un pakalpojuma pamatfunkcijām.
- Leģitīmās intereses (6. panta 1. punkta f) apakšpunkts) drošībai, krāpšanas novēršanai, ļaunprātīgas izmantošanas atklāšanai, darbības telemetrijai un pakalpojuma aizsardzībai. Mēs izsveram savas intereses pret jūsu tiesībām un pēc pieprasījuma pastāstīsim, kāds bija šīs izsvēršanas rezultāts.
- Piekrišana (6. panta 1. punkta a) apakšpunkts) AI izmantošanai lietotnē tafs: tam, kas tiek sūtīts AI modelim, kas darbina Coach un jūsu taf, kā aprakstīts [turpmāk](#ai-in-tafs). Nekas netiek sūtīts, līdz jūs to atļaujat, un piekrišanu varat atsaukt sadaļā Account. Piekrišanu mēs lūgtu arī pirms jebkādām analītikas sīkdatnēm, neobligātiem mārketinga paziņojumiem vai jaunas apstrādes, kas būtiski paplašina jūsu datu izmantošanu. Mēs neveicam analītiku, reklāmu un nebūtisku izsekošanu.
- Juridisks pienākums (6. panta 1. punkta c) apakšpunkts) nodokļu uzskaites ierakstiem (Igaunijas Grāmatvedības likuma 12. pants, 7 gadi), atbildēm uz likumīgiem iestāžu pieprasījumiem un glabāšanai, ko pieprasa noziedzīgi iegūtu līdzekļu legalizācijas novēršanas noteikumi, ja tie piemērojami.

### AI lietotnē tafs

Lietotne tafs jums jautā, pirms kaut kas tiek nosūtīts AI, ar šiem vārdiem:
- tafs izmanto Anthropic AI Claude, lai darbinātu Coach un jūsu taf.
- Coach un jūsu taf ir AI aģenti: to rakstīto ģenerē AI, un tas var būt kļūdains. Ziņā, ko taf sūta ārpus jūsu uzņēmuma, ir norādīts, ka to ir rakstījis AI aģents.
- Kas tam tiek sūtīts: tas, ko rakstāt Coach un saviem taf, jūsu pievienotie faili, tas, ko jūsu pievienotās lietotnes atgriež taf, kamēr tas strādā, un tas, kas ir jūsu ekrānā, kamēr Coach jums palīdz lietotnē.
- Kas nekad netiek sūtīts: paroles un atslēgas, ko izmanto jūsu pievienotās lietotnes, vai jūsu pirksta nospiedums.
- Anthropic to izmanto, lai atbildētu, un glabā to līdz 30 dienām.
- Nekas netiek sūtīts, līdz jūs to atļaujat. Piekrišanu varat atsaukt sadaļā Account.

taf, kas darbojas, kad neviens tobrīd nav klāt (pēc sava grafika vai kad kaut kas pienāk), balstās uz tās personas piekrišanu, kura to izveidoja vai pārņēma, vai citādi uz pirmās personas piekrišanu, kas pārvalda konta taf un to atļauj.

Kad atsaucat piekrišanu, Coach un jūsu taf pārstāj jums atbildēt. taf, kura palaišanas balstās uz jūsu piekrišanu, tiek iepauzēts: tā grafiki apstājas, un tas, kas pienāk, gaida. Tas turpina, kad piekrišana tiek sniegta atkārtoti vai kāds, kas pārvalda konta taf, to pārņem ar savu piekrišanu. Atsaukšana nemaina to, kas tika nosūtīts iepriekš.

Anthropic ir norādīts lapā [/subprocessors](/subprocessors) kopā ar to, kur tas apstrādā datus un kad tas var tos glabāt ilgāk.

## 6. Apakšapstrādātāji

Mēs nepārdodam jūsu personas datus un nekopīgojam tos reklāmas nolūkos.

Pakalpojuma sniegšanai mēs piesaistām nelielu piegādātāju loku. Oficiālais, aktuālais saraksts ir lapā [/subprocessors](/subprocessors); šī lapa tiek atjaunināta 30 dienu laikā pēc jebkuras izmaiņas. Mēs pieprasām, lai katrs apakšapstrādātājs saskaņā ar rakstiskiem līgumiem nodrošina mūsējai līdzvērtīgu aizsardzību, tostarp ES līguma standartklauzulas, ja personas dati tiek nosūtīti ārpus ES.

Par būtiskiem apakšapstrādātāju papildinājumiem vai aizstāšanu, kas skar personas datus, aktīvajiem klientiem paziņo iepriekš, ar tiesībām iebilst.

## 7. Starptautiska datu nosūtīšana

Daži no mūsu apakšapstrādātājiem ir reģistrēti ārpus Eiropas Ekonomikas zonas, galvenokārt Amerikas Savienotajās Valstīs. Katrai šādai nosūtīšanai mēs balstāmies uz vismaz vienu no šīm garantijām:
- ES un ASV datu privātuma regulējums (EU–US Data Privacy Framework): ASV apakšapstrādātājiem, kas ir sertificēti saskaņā ar šo regulējumu. Lēmums par aizsardzības līmeņa pietiekamību ir spēkā kopš 2023. gada vidus; mēs sekojam jebkurām tā statusa izmaiņām.
- Līguma standartklauzulas: 2. modulis (no pārziņa apstrādātājam) mūsu tiešajiem apakšapstrādātājiem un 3. modulis (no apstrādātāja apakšapstrādātājam), ja ir apstrādātāju ķēde. Mēs izmantojam Komisijas apstiprinātās 2021. gada līguma standartklauzulas.
- Papildu pasākumi: ja riska novērtējums norāda, ka nepieciešamas papildu garantijas (šifrēšana pārsūtīšanas laikā un glabāšanā, piekļuves reģistrēšana, datu minimizēšana).

Jūs varat pieprasīt konkrēto nosūtīšanas instrumentu, kas piemērojams konkrētam apakšapstrādātājam, rakstot uz [privacy@kiff.dev](mailto:privacy@kiff.dev).

**Jūsu valsts.** Dažu valstu tiesību akti prasa vairāk, nekā teikts šajā politikā. Ko tie papildina, angļu valodā un attiecīgās valsts valodā, ir lapā [Jūsu valsts](/privacy/countries).

## 8. Glabāšana

Mēs glabājam personas datus īsāko laiku, kas atbilst to apstrādes nolūkiem un jebkuram piemērojamam juridiskam pienākumam.
- Konta un pierakstīšanās metadati: līdz konta dzēšanai. Lietotnē tafs īpašnieks to dzēš sadaļā Account → Delete account. Tīmeklī izveidots konts tiek dzēsts, rakstot uz [privacy@kiff.dev](mailto:privacy@kiff.dev), VDAR 12. panta 3. punktā noteiktajā viena mēneša termiņā. Abos gadījumos konta atslēgas nekavējoties pārstāj darboties. Ja konts dzēsts lietotnē tafs, to nevar atsaukt: tā tafs tērzēšanas sarunas, piezīmes un savienojumi tiek nekavējoties dzēsti, un tas, ko glabā KIFF, tiek dzēsts 30 dienu laikā. Ja konts dzēsts tīmeklī, mēs glabājam tā datus 30 dienu labvēlības periodu, ja īpašnieks vēlas to atgūt, un pēc tam tos dzēšam.
- API atslēgu ieraksti: līdz konta dzēšanai, arī pēc atslēgas atsaukšanas. Atslēga, kas pieder aģenta pierakstīšanās savienojumam, tiek dzēsta kopā ar šo savienojumu 30 dienas pēc tā beigām.
- Sesijas sīkdatnes: līdz to derīguma beigām (pēc noklusējuma 24 stundas) vai līdz izrakstāties.
- Reversā starpniekservera piekļuves žurnāli: līdz 90 dienām, pēc tam apkopoti vai dzēsti.
- Pakalpojumu žurnāli: tas, ko KIFF pakalpojumi un planque reģistrē darbības laikā: kļūdas un pieprasījumi, kuros var būt minēts konts, persona vai aģents. 30 dienas, pēc tam dzēsti.
- Rezerves kopijas: KIFF datubāzei katru dienu tiek veidota šifrēta rezerves kopija, un katra kopija tiek glabāta 7 dienas, tāpēc dzēsts ieraksts no tām pazūd 7 dienu laikā. tafs un planque krātuves glabā šifrētas kopijas noteiktā laika punktā 35 dienas, ko izmanto tikai krātuves atjaunošanai pēc kļūmes, tāpēc dzēsts ieraksts no tām pazūd 35 dienu laikā.
- Tenant audita dati: tiek glabāti kā apstrādātājam saskaņā ar klienta tenant konfigurāciju. Pēc noklusējuma: tiek glabāti visu tenant pastāvēšanas laiku; tiek dzēsti, dzēšot tenant. Skatīt [/security#deletion](/security#deletion).
- Izsaukumi caur KIFF vārteju: izsaukuma argumenti līdz tā nosūtīšanai, noraidīšanai vai neizdošanās brīdim, pēc tam dzēsti; rīka rezultāts 24 stundas, pēc tam dzēsts. Lēmuma ieraksts un argumentu jaucējvērtība tiek glabāti kā tenant audita dati. Skatīt [/security#data-kept](/security#data-kept).
- Lietojums: par katru izsaukumu caur vārteju, kad tas beidzas: kurš aģents to veica, rīks un vai tas tika ieskaitīts kā darbība vai bija bez maksas (un kāpēc). Par katru taf vai Coach palaišanu, kad tā beidzas: taf, kad, kredīti, ko izmantoja tā AI, un vai tie tika ieskaitīti vai kāpēc ne. Līdz konta dzēšanai; tas, kas tiek paziņots mūsu maksājumu apstrādātājam norēķinu vajadzībām, tiek glabāts kopā ar norēķinu ierakstiem.
- Aģentu pierakstīšanās savienojumi: kurš aģents, lietotnes nosaukums, kurš to atļāva, kad un tā pēdējā izmantošana: kamēr savienojums ir aktīvs, pēc tam 30 dienas pēc tā beigām (šo laiku tas paliek redzams vadības panelī), pēc tam dzēsti kopā ar tā žetoniem un API atslēgu. Viss tiek dzēsts kopā ar kontu.
- tafs: ierīces push žetons, līdz izrakstāties no lietotnes, noņemat ierīci vai Apple vai Google paziņo, ka žetona vairs nav. Ierīces publiskā atslēga, nosaukums, platforma un datumi, kamēr to izmantojat; kad to izrakstāt, tā tiek atzīmēta kā noņemta un glabāta līdz konta dzēšanai, lai KIFF varētu noraidīt no tās novēlotu atbildi un parādīt, kura ierīce parakstīja agrāko. Piekļuves atslēgas publiskā atslēga, identifikators un nosaukums tiek glabāti tāpat kā tās ierīces dati. Dators glabā savu sesiju līdz 90 dienām vai līdz izrakstāties. Tas, ko tafs glabā jūsu uzņēmumam, tiek glabāts kā apstrādātājam: taf palaišanas un tērzēšanas sarunas kopā ar sarunas kopsavilkumu (un Setup kopsavilkumu) 30 dienas, tas, ko atgriež jūsu rīki, tikai līdz palaišanas beigām (tērzēšanas sarunā tas, ko tā tikai nolasīja, tiek glabāts šifrēti tās nākamajām ziņām, izmantots, līdz saruna ir bijusi neaktīva stundu un ne ilgāk kā četras stundas pēc nolasīšanas, pēc tam automātiski dzēsts dažu dienu laikā), un tā atmiņas piezīmes līdz taf noņemšanai; faili, ko tas piegādā, 30 dienas no palaišanas, kas tos izveidoja, pēc tam automātiski dzēsti dienas laikā vai ātrāk, ja kāds jūsu kontā kādu no tiem dzēš; savienojuma žetons vai atslēga planque vai pārvaldīta savienojuma pierakstīšanās Composio, līdz to atvienojat, un tā apstiprinājumu ieraksts līdz konta beigām; skatīt [DPA 4. pielikumu](/legal/dpa#annex-4).
- Norēķinu ieraksti: 7 gadi no finanšu gada beigām (Igaunijas Grāmatvedības likuma 12. pants).
- App Store un Google Play pirkumi: tas, ko Apple vai Google mums paziņoja par katru pirkumu, paliek jūsu kontā līdz konta dzēšanai. To pārdošanas un izmaksu pārskati, ko glabājam kā norēķinu ierakstus 7 gadus, jūs nenosauc.
- Atbalsta sarakste: 3 gadi no pēdējās saziņas, pēc tam dzēsta.

## 9. Jūsu tiesības

Saskaņā ar VDAR jums ir šādas tiesības attiecībā uz jūsu personas datiem:
- Piekļuve (15. pants): apstiprinājums, vai mēs apstrādājam jūsu datus, un to kopija.
- Labošana (16. pants): neprecīzu vai nepilnīgu datu labošana.
- Dzēšana (17. pants): jūsu datu dzēšana, ievērojot juridiskās glabāšanas izņēmumus.
- Ierobežošana (18. pants): apstrādes apturēšana, kamēr tiek risināts strīds.
- Pārnesamība (20. pants): jūsu sniegto datu eksports mašīnlasāmā formātā.
- Iebildumi (21. pants): pret apstrādi, kas balstīta uz leģitīmajām interesēm; mēs to pārtraucam, ja vien nevaram pierādīt pārliecinošus pamatus, kas ir svarīgāki.
- Piekrišanas atsaukšana (7. panta 3. punkts): jebkurai apstrādei, kas balstīta uz piekrišanu, piemēram, AI lietotnē tafs (sadaļā Account, kā [iepriekš](#ai-in-tafs)). Atsaukšana neietekmē iepriekšējo likumīgo apstrādi.

Lai izmantotu jebkuras no šīm tiesībām, rakstiet uz [privacy@kiff.dev](mailto:privacy@kiff.dev). Mēs atbildam viena mēneša laikā saskaņā ar 12. panta 3. punktu; sarežģītu pieprasījumu gadījumā mēs varam šo termiņu pagarināt vēl par diviem mēnešiem un jums par to paziņosim.

### Jūsu pašu tafs tērzēšanas sarunas

Lietotnē tafs sadaļa Account → Export my chats lejupielādē jūsu pašu tērzēšanas sarunas ar katru taf, ieskaitot Coach, jūsu pievienoto failu nosaukumus un jūsu pašu piezīmes, kā lasāmu failu un kā JSON. Tās varat eksportēt tikai jūs: administrators vai konta īpašnieks to nevar, un KIFF konta eksportā tās nav iekļautas.

### tafs konta dzēšana

tafs ir KIFF lietotne. Lai dzēstu savu tafs kontu un tā datus:
- Atveriet lietotni tafs un pierakstieties.
- Atveriet Account un pieskarieties Delete account.
- Apstipriniet. Konta dzēšanu nevar atsaukt.

Delete account tiek rādīts tam, kurš drīkst dzēst uzņēmuma kontu, parasti tā īpašniekam. Ja to neredzat vai jums vairs nav lietotnes, rakstiet uz [privacy@kiff.dev](mailto:privacy@kiff.dev) no adreses, ar kuru pierakstāties, un mēs dzēsīsim jūsu datus viena mēneša laikā.

Kas tiek dzēsts: konta atslēgas nekavējoties pārstāj darboties. Tā taf, tērzēšanas sarunas, piezīmes, pievienotie faili un savienojumi tiek nekavējoties dzēsti, un no to kopijām noteiktā laika punktā 35 dienu laikā. Tas, ko KIFF glabā par kontu, tiek dzēsts 30 dienu laikā, un no rezerves kopijām 7 dienu laikā pēc tam. Pakalpojumu žurnāli, kuros minēts konts, pazūd 30 dienu laikā. Tenant audita dati tiek dzēsti kopā ar tenant. Katram datu veidam skatiet sadaļu [Glabāšana](#retention).

Lai dzēstu daļu savu datu un saglabātu kontu: lietotnē dzēsiet tērzēšanas sarunu, un tās ziņas un pievienotie faili tiek dzēsti kopā ar to; noņemiet taf, un tā tērzēšanas sarunas, piezīmes un savienojumi tiek dzēsti kopā ar to. Visam citam rakstiet uz [privacy@kiff.dev](mailto:privacy@kiff.dev) un norādiet, ko dzēst. Mēs to izdarām viena mēneša laikā.

Jums ir tiesības iesniegt sūdzību uzraudzības iestādei. Mūsu vadošā uzraudzības iestāde ir [Igaunijas Datu aizsardzības inspekcija (Andmekaitse Inspektsioon)](https://www.aki.ee/en). Jūs varat iesniegt sūdzību arī savas dzīvesvietas dalībvalsts uzraudzības iestādei.

**Jūsu valsts.** Dažu valstu tiesību akti prasa vairāk, nekā teikts šajā politikā. Ko tie papildina, angļu valodā un attiecīgās valsts valodā, ir lapā [Jūsu valsts](/privacy/countries).

## 10. Sīkdatnes

Mēs iestatām trīs sīkdatnes, visas noteikti nepieciešamas, pirmās puses, sūtītas tikai pa HTTPS un nenolasāmas lapas skriptiem. Saskaņā ar ES e-privātuma noteikumiem tām nav nepieciešama piekrišana; mēs tās uzskaitām, lai jūs zinātu, ka tās pastāv. Sīkāka informācija ir [sīkdatņu politikā](/cookies).
- kiff_session: uztur jūs pierakstītu. Parakstīta sesijas sīkdatne ar jūsu pierakstīšanās identifikatoru, e-pastu un vārdu, kā arī pierakstīšanās datiem, ko vadības panelis izmanto jūsu vārdā. Iestatīta .kiff.dev. Ilgst 24 stundas vai līdz izrakstāties.
- kiff_flash: nodod vienreizēju ziņojumu nākamajai lapai, piemēram, "jūsu atslēga ir izveidota". Parakstīta; jauna atslēga tajā nekad nav. Iestatīta .kiff.dev. Tiek dzēsta, kad nākamā lapa to nolasa, un ilgst ne vairāk kā 5 minūtes.
- kiff_cb_attempts: skaita, cik reižu pierakstīšanās lapa ir pārlādēta, lai apturētu pierakstīšanos, kas iestrēgusi ciklā. Tiek sūtīta tikai uz šo lapu. Ilgst 5 minūtes un tiek dzēsta, kad pierakstīšanās sākas vai izdodas.

Mēs neizmantojam analītikas sīkdatnes, reklāmas sīkdatnes vai jebkādus trešo pušu izsekotājus. Ja kādreiz tādus pievienosim, pirms sīkdatnes iestatīšanas parādīsim piekrišanas joslu saskaņā ar e-privātuma noteikumiem un EDAK 2023. gada vadlīnijām par izsekošanas tehnoloģijām.

Ja pierakstāties ar Clerk (mūsu autentifikācijas apakšapstrādātāja) starpniecību, Clerk iestata sīkdatnes arī savā domēnā (clerk.kiff.dev). Šīs sīkdatnes ir nepieciešamas, lai Clerk jūs atpazītu visā pierakstīšanās procesā, un uz tām attiecas Clerk privātuma politika, kā arī mūsējā. Sīkāka informācija ir apakšapstrādātāju sarakstā.

## 11. Drošība

Mūsu tehniskie un organizatoriskie drošības pasākumi ir aprakstīti lapā [/security](/security). Tie ietver tenant nodalīšanu datubāzes līmenī, TLS 1.2+ pārsūtīšanas laikā, šifrēšanu glabāšanā ar pārvaldītā nodrošinātāja noklusējuma iestatījumiem un piekļuves kontroli, kas balstīta uz mazāko privilēģiju principu.

Par personas datu aizsardzības pārkāpumu mēs ietekmētajiem klientiem paziņojam bez nepamatotas kavēšanās un 72 stundu laikā pēc tā apstiprināšanas saskaņā ar VDAR 33. pantu. Paziņojumā ir norādīts pārkāpuma raksturs, attiecīgo datu subjektu un ierakstu kategorijas un aptuvenais skaits, iespējamās sekas un veiktie vai ierosinātie pasākumi.

## 12. Bērni

KIFF Cloud ir infrastruktūras produkts izstrādātājiem, kas nav paredzēts bērniem. Mēs apzināti nevācam personas datus no bērniem, kas jaunāki par 16 gadiem. Ja uzskatāt, ka bērns mums ir sniedzis personas datus, sazinieties ar [privacy@kiff.dev](mailto:privacy@kiff.dev), un mēs tos dzēsīsim.

## 13. Automatizēta lēmumu pieņemšana

KIFF Cloud izpildlaiks pieņem deterministiskus politikas lēmumus par datiem, ko sūta mūsu klienti. Tie nav lēmumi par jums, kas rada tiesiskas vai līdzīgi būtiskas sekas VDAR 22. panta izpratnē; tie ir lēmumi par klienta darbībām (naudas atmaksām, pasūtījumiem, ziņām utt.), ko izpilda saskaņā ar paša klienta politiku.

Klienti, kas izmanto KIFF Cloud, lai pieņemtu lēmumus, kas ietekmē viņu pašu galalietotājus, paši atbild par atbilstību 22. pantam. Mūsu pārvaldības kvītis ir veidotas tā, lai sniegtu šiem klientiem audita liecības, kas tiem nepieciešamas cilvēka uzraudzības pierādīšanai, bet juridiskais pienākums nodrošināt šo uzraudzību ir klientam.

## 14. Šīs politikas izmaiņas

Mēs laiku pa laikam varam atjaunināt šo politiku. Par būtiskām izmaiņām aktīvajiem klientiem paziņo pa e-pastu un ar labi redzamu paziņojumu šajā lapā vismaz 30 dienas. Nebūtiskas izmaiņas publicē ar jaunu "pēdējoreiz atjaunināta" datumu.

## 15. Sazinieties ar mums

Privātuma jautājumi un tiesību pieprasījumi: [privacy@kiff.dev](mailto:privacy@kiff.dev).

Oficiāli juridiski paziņojumi Kiff Agents OÜ: [legal@kiff.dev](mailto:legal@kiff.dev) vai pa pastu uz adresi Tööstuse tn 75-71, Põhja-Tallinna linnaosa, Tallinn 10416, Igaunija.

Vadošā uzraudzības iestāde sūdzībām: [Igaunijas Datu aizsardzības inspekcija (Andmekaitse Inspektsioon)](https://www.aki.ee/en).

Dokumenta versija: v0.1. Pēdējoreiz atjaunināta: 2026. gada 17. novembrī.
