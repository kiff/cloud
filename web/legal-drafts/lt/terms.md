# Paslaugų teikimo sąlygos

Paskutinį kartą atnaujinta: 2026 m. lapkričio 9 d. Įsigaliojimo data: 2026 m. gegužės 24 d.

Šis vertimas skelbiamas kartu su tekstu anglų kalba. Jei jie skiriasi, taikomas tekstas anglų kalba.

Kas pasikeitė: programėlė tafs Android įrenginiams ir per Google Play įsigytas Solo (1 ir 8 punktai). Anksčiau: mokami planai (3A ir 8 punktai), [paslaugų lygio sutartis](/legal/sla) (14 punktas) ir draudžiamų naudojimo būdų sąrašas, kuris dabar pateikiamas [Priimtino naudojimo politikoje](/acceptable-use) (5 punktas).

Šios sąlygos reglamentuoja KIFF Cloud, įskaitant tafs, naudojimą. Sutartiniai klausimai: [legal@kiff.dev](mailto:legal@kiff.dev).

## 1. Sąvokos
- „Kiff Agents OÜ“, „mes“, „mus“, „mūsų“: Estijos uždaroji akcinė bendrovė, teikianti Paslaugą. Registro kodas 17372313.
- „Klientas“, „jūs“, „jūsų“: fizinis ar juridinis asmuo, sutinkantis su šiomis sąlygomis.
- „Naudotojas“: bet kuris fizinis asmuo, kuriam Klientas leidžia naudotis Paslauga per Kliento paskyrą.
- „Paslauga“: produktas KIFF Cloud, įskaitant api.kiff.dev, app.kiff.dev, kiff.dev, tafs ir ten skelbiamą dokumentaciją, valdymo skydelius ir API.
- „tafs“: programėlė tafs, skirta iPhone, iPad, Android įrenginiams ir kompiuteriams (Mac, Windows ir Linux), ir paslauga, kuria ji grindžiama, įskaitant Setup ir planque (3A punktas), kurioje Klientas patiki darbą dirbtinio intelekto (AI) agentui, dirbančiam KIFF Card ribose. „taf“ yra vienas toks agentas.
- „Tenant“: logiškai atskirta darbo erdvė Paslaugoje, priklausanti vienam Klientui.
- „Audito duomenys“: įvykiai, sprendimai, veiksmai, patvirtinimai ir kvitai (receipts), kuriuos Paslauga per vykdymo aplinką (runtime) išsaugo Kliento vardu.
- „tafs duomenys“: tai, ką tafs saugo Kliento vardu: kiekvieno taf paleidimai ir pokalbiai (įskaitant tai, ką jam grąžina Kliento prijungti įrankiai), jo ataskaitos, atminties pastabos ir jo įspėjimams užregistruoti telefonai.
- „Kvitas“ (Receipt): pasirašytas sprendimo įrašas su versija, kurį Paslauga išduoda dėl kiekvieno valdomo veiksmo.
- „Dokumentacija“: techninė Paslaugos dokumentacija ir saugumo priemonės datuotoje redakcijoje, į kurią nurodo šios Sąlygos ([kiff.dev/docs/archive/2026-10-10](https://kiff.dev/docs/archive/2026-10-10)). Ji neapima strateginės baltosios knygos (whitepaper), rinkodaros medžiagos ir atskirai licencijuojamo atvirojo kodo karkaso dokumentacijos.
- „DPA“: Duomenų tvarkymo sutartis adresu [/legal/dpa](/legal/dpa), reglamentuojanti mūsų, kaip duomenų tvarkytojo, santykius.

## 2. Sutartis

Šios sąlygos reglamentuoja, kaip naudojatės Paslauga. Susikurdami paskyrą, sugeneruodami API raktą ar kitaip naudodamiesi Paslauga, sutinkate su šiomis sąlygomis. Jei sutinkate įmonės ar kito juridinio asmens vardu, patvirtinate, kad turite teisę įpareigoti tą asmenį, o „jūs“ ir „jūsų“ reiškia jį.

Galime skelbti papildomas sąlygas konkrečioms funkcijoms (beta funkcijoms, trečiųjų šalių integracijoms). Jei jos prieštarauja šioms sąlygoms, konkrečiai funkcijai taikomos papildomos sąlygos.

## 3. Paslauga

KIFF Cloud yra talpinama paslauga deleguotiems veiksmams valdyti. Ji vertina užklausas pagal Kliento nustatytus leidimus ir Card, gali reikalauti žmogaus patvirtinimo ir registruoja valdymo sprendimus. Priklausomai nuo nustatytos integracijos, KIFF persiunčia leistą užklausą arba suteikia autorizaciją, kurią patikrina saugoma sistema. Patį veiksmą atlieka prijungta sistema.

Taikoma techninė elgsena, įrodymai, integracijos reikalavimai ir vykdymo apribojimai nustatyti datuotoje Dokumentacijoje, į kurią nurodo šios Sąlygos. Strateginė [baltoji knyga](/whitepaper) paaiškina KIFF principus ir modelį; pagal šias Sąlygas ji nėra įtraukta kaip techninė Dokumentacija.

KIFF Cloud valdymo paslauga nėra agentų karkasas, darbo eigų variklis, modelių šliuzas ar valdoma agentų platforma. Ji veikia kartu su tuo, ką jau naudojate. Aiškų sąrašą rasite pagrindinio puslapio skiltyje [„Kas KIFF nėra“](/#not). tafs yra vienintelė Paslaugos dalis, kuri vykdo agentus už jus, jūsų išduotų Card ribose; ją aprašo 3A punktas.

Atvirojo kodo karkasą [kiff/kiff](https://github.com/kiff/kiff) reglamentuoja jo paties MIT licencija, ir šios sąlygos jam netaikomos. Kai karkasą vykdote savo infrastruktūroje, sutartis su Kiff Agents OÜ nesudaroma.

## 3A. tafs

tafs jūs patikite darbą (pavyzdžiui, pinigų grąžinimus, sąskaitų apmokėjimą ar ginčus dėl kortelių operacijų) taf: AI agentui, kuris dirba pagal tvarkaraštį su jūsų prijungtais įrankiais, jūsų jam išduotų KIFF Card ribose. Kiekvienas taf veikia mūsų agentų vykdymo aplinkoje, o jo naudojamą AI modelį teikia Anthropic. Į jo sulaikytas užklausas atsakote programėlėje tafs.

Įrankių prijungimas. Setup, tafs AI agentas, veikiantis kaip taf, padeda jums prijungti įrankį ir nustatyti taf darbui. Pats jis nieko neprijungia. Jūs prisijungiate prie paties įrankio teikėjo (pavyzdžiui, Stripe) ir savo įrenginyje patvirtinate kiekvieną ryšį, įrankius, kuriuos taf gali naudoti, ir jo Card. To prisijungimo žetoną saugo planque, mūsų ryšių paslauga. Jis užšifruotas, naudojamas tik užklausai, dėl kurios nusprendė KIFF, atlikti ir niekada neperduodamas taf. Atjungus įrankį, žetonas atšaukiamas teikėjo pusėje ir ištrinamas.

Jūsų Card yra jūsų nurodymai. taf pasiekia jūsų įrankius tik per KIFF, kuri prieš išsiųsdama kiekvieną užklausą patikrina ją pagal taf Card. Užklausa jūsų išduotos Card ribose atliekama jūsų nurodymu. Užklausa už Card ribų sulaikoma arba atmetama, kaip nustato Card. Sulaikyta užklausa atliekama tik tada, kai ją patvirtina asmuo, kuriam jūsų paskyra leidžia atsakyti į sulaikytas užklausas. Programėlėje tafs atsakymas pasirašomas jo įrenginio parašu, kurį patikrina KIFF, o kvitas išsaugo šį įrodymą. Jūsų paskyra taip pat gali atsakyti pačioje KIFF (valdymo skydelyje, jos komandinės eilutės įrankyje arba per ten jūsų įgaliotą tvirtintoją); tada kvite užfiksuojama, kas atsakė ir kaip.

Kas lieka jums. Jūs pasirenkate kiekvieno taf darbą ir Card, atsakote į jo sulaikytas užklausas ir užtikrinate, kad turite teisę naudoti prijungtus įrankius ir paskyras. taf ataskaitas, žinutes ir atminties pastabas rašo AI modelis, todėl jos gali būti klaidingos; prieš pasikliaudami, patikrinkite tai, kas svarbu.

taf sustabdymas. Galite sustabdyti vieną taf arba visus, pristabdyti taf, atšaukti bet kurią jo Card arba jį pašalinti. Nuo tos akimirkos, kai sustabdote taf ar atšaukiate Card, KIFF neleidžia jokios naujos užklausos pagal juos. Programėlė tafs rodo taf kaip sustabdytą tik tada, kai sustabdymą patvirtina ir KIFF, ir planque; nuo tada planque nesiunčia jokios jo užklausos, net ir tokios, kurią KIFF leido prieš pat tai. Įrankio teikėjui jau išsiųsta užklausa vis tiek gali būti įvykdyta: mes sustabdome naujas užklausas ir negalime atšaukti to, ką teikėjas jau padarė. Įrankis, kuris pats tikrina KIFF leidimus (permits), be planque, dar gali priimti leidimą, kurį KIFF išdavė prieš pat tai, ne ilgiau kaip 90 sekundžių. Sustabdyto taf veiklai atnaujinti reikia jūsų patvirtinimo jūsų įrenginyje, ir niekas, kas buvo sulaikyta ar laukė eilėje prieš sustabdymą, po jo nevykdoma.

Pašalinus taf, sustabdomas jo tvarkaraštis ir ištrinami jo paleidimai, pokalbiai, atmintis ir jo naudotas raktas. Dar iki tol tai, ką jūsų įrankiai grąžina taf, saugoma tik iki jo paleidimo pabaigos (tai, ką pokalbis tik perskaitė, užšifruotai, kitoms jo žinutėms: naudojama ne ilgiau kaip keturias valandas, tada ištrinama), o jo paleidimai ir pokalbiai – 30 dienų. KIFF saugo kvitus apie tai, ką ji padarė.

ES dirbtinio intelekto aktas. tafs atžvilgiu Kiff Agents OÜ yra AI sistemos tiekėjas, o jūs – jos diegėjas. Mes užtikriname, kad viskas, ką taf rašo kam nors už jūsų įmonės ribų, nurodytų, kad tai parašė AI agentas. Jūs naudojate tafs tik jo siūlomiems darbams, o ne tokiam naudojimui, kurį AI aktas draudžia arba priskiria didelės rizikos kategorijai, pavyzdžiui, sprendimams dėl asmens įdarbinimo, kredito ar prieigos prie esminių paslaugų. Jūs išlaikote žmogaus patvirtinimus, kurių reikalauja jūsų Card, ir užtikrinate, kad žmonės, kurie jūsų vardu naudoja tafs, suprastų, ką jis daro ir kokie jo apribojimai.

Išlaidos. tafs yra jūsų KIFF plano dalis (8 punktas): kiekvienas taf paleidimas naudoja kreditus už jo panaudotą AI. Coach ir tafs nustatymas įskaičiuoti į sąžiningo Coach naudojimo ribas.

## 4. Paskyros ir prieiga

Norėdami naudotis Paslauga, turite susikurti paskyrą. Įsipareigojate pateikti tikslią registracijos informaciją, saugoti savo prisijungimo duomenis paslaptyje ir atsakyti už visą veiklą savo paskyroje.

Jums turi būti ne mažiau kaip 16 metų arba jūsų jurisdikcijos skaitmeninio sutikimo amžius, priklausomai nuo to, kuris yra didesnis. Paslauga nėra skirta vaikams.

Jūs atsakote už kiekvieno Naudotojo, kurį įsileidžiate į savo paskyrą, veiksmus. Nustatę patikimą šių sąlygų pažeidimą, galime, pagrįstai įspėję, sustabdyti paskyrą ar atšaukti prieigą. Priimtino naudojimo pažeidimų atveju (žr. 5 punktą) sustabdymas gali būti nedelsiamas.

## 5. Priimtinas naudojimas

Jūs, jūsų naudotojai ir jūsų agentai turite laikytis [Priimtino naudojimo politikos](/acceptable-use). Ji yra šių sąlygų dalis ir išvardija, kam Paslauga negali būti naudojama.

Esant patikimam Priimtino naudojimo politikos pažeidimui, galime nedelsiant sustabdyti naudojimą. Mes neprivalome stebėti Kliento turinio, bet galime imtis veiksmų gavę patikimų pranešimų.

## 6. Kliento duomenys ir turinys

Jūsų Audito duomenys priklauso jums. Jūs suteikiate Kiff Agents OÜ ribotą, neišimtinę, neatlygintiną, pasaulinę licenciją talpinti, kopijuoti, perduoti, rodyti ir kitaip tvarkyti jūsų Audito duomenis tik tiek, kiek būtina Paslaugai jums teikti, prižiūrėti ir tobulinti.

Jūsų Audito duomenų ir tafs duomenų atžvilgiu veikiame kaip duomenų tvarkytojas pagal BDAR 28 straipsnį; šiuos santykius išsamiai reglamentuoja [DPA](/legal/dpa). Jūsų paskyros metaduomenų atžvilgiu veikiame kaip duomenų valdytojas; šiuos santykius reglamentuoja [privatumo politika](/privacy).

Jūs patvirtinate, kad turite visas būtinas teises pateikti duomenis, kuriuos pateikiate, ir kad jų pateikimas atitinka taikomus teisės aktus (įskaitant duomenų apsaugos teisės aktus ir, kai taikoma, ES dirbtinio intelekto aktą).

## 7. Konfidencialumas

Kiekviena šalis gali gauti iš kitos neviešos informacijos („Konfidenciali informacija“). Gaunančioji šalis naudoja Konfidencialią informaciją tik tiek, kiek būtina savo įsipareigojimams vykdyti ir teisėms pagal šias sąlygas įgyvendinti, ir saugo ją ne mažiau rūpestingai nei savo konfidencialią informaciją.

Konfidenciali informacija neapima informacijos, kuri (a) yra arba tampa viešai prieinama be pažeidimo, (b) gaunančiajai šaliai buvo žinoma prieš atskleidimą, (c) sukurta savarankiškai, nenaudojant Konfidencialios informacijos, arba (d) turi būti atskleista pagal įstatymą (pranešus kitai šaliai, kai tai leidžiama).

## 8. Mokesčiai ir mokėjimas

Planai. Kiekviena paskyra turi vieną planą. Dabartinės kainos pateikiamos [kainų puslapyje](/pricing); šio atnaujinimo metu:
- Solo: vienas asmuo, tafs naudojimui, parduodamas programėlėje tafs per App Store arba Google Play už 12,50 € per mėnesį su PVM, su 500 įskaičiuotų kreditų per mėnesį.
- Company: 10,50 € per mėnesį už kiekvieną vietą, su 500 kreditų per mėnesį už kiekvieną vietą, bendrai, ir jūsų pačių agentų užklausoms per KIFF Cloud – 500 veiksmų per mėnesį už kiekvieną vietą, bendrai.
- Sutartis: mokesčiai ir sąlygos jos užsakymo formoje.

Vietos yra paskyros administratoriai ir redaktoriai. Tvirtintojai ir žiūrovai nemokami visuose planuose. Company plane virš įskaičiuoto kiekio kreditas kainuoja 0,01 €, o veiksmas – 0,02 €. Kreditai virš įskaičiuotų apmokestinami iki paskyros nustatyto mėnesinio išlaidų limito (pradžioje 50 €): pasiekus limitą, KIFF nepradeda jokio naujo paskyros tafs paleidimo, kol neprasideda kitas laikotarpis arba kas nors, kas tvarko atsiskaitymus, jo nepadidina.

Kreditų paketai. Solo plane kreditai virš mėnesio įskaičiuotų gaunami iš kreditų paketų, perkamų programėlėje tafs. Paketas perkamas vieną kartą, nenustoja galioti ir nėra prenumerata; jo kreditai naudojami po mėnesio įskaičiuotų, pradedant nuo seniausio paketo. Paketas, už kurį sumokėti pinigai grąžinti visiškai, atimamas kartu su jame likusiais kreditais. Išnaudojus mėnesio kreditus ir paketus, KIFF nepradeda jokio naujo paskyros tafs paleidimo, kol neprasideda kitas laikotarpis arba nepridedamas paketas. Premijiniai kreditai, kuriuos KIFF prideda paskyrai, taip pat nenustoja galioti, kaip ir paketai.

Kas skaičiuojama. Kreditas apmoka AI, kurį panaudojo taf paleidimas, sveikais kreditais, apvalinant į didesniąją pusę vieną kartą per paleidimą, ir skaičiuojamas vieną kartą, kai paleidimas baigiasi. Paleidimas, kurio užklausą KIFF atmetė, vis tiek panaudojo savo AI. Niekada neskaičiuojama: Coach ir tafs nustatymas, neviršijant sąžiningo Coach naudojimo; naujos versijos bandymas, kuris nieko nesiunčia; ir paleidimas, kuris nepavyko dėl mūsų kaltės. Veiksmas yra jūsų paties agento užklausa per KIFF Cloud, kuri kažką atliko: KIFF ją leido, ji buvo išsiųsta ir įrankis pranešė apie sėkmę. Niekada neskaičiuojama kaip veiksmas: užklausa, kuri buvo atmesta, sulaikyta ir tada atmesta, sustabdyta dėl sustabdymo, negaliojanti, nepavyko įrankyje arba kurios rezultatas nežinomas, taip pat užklausa, kuri tik skaito.

Be bandomojo laikotarpio. Paskyra moka nuo pirmos dienos. Kol planas neprasidėjo, KIFF nepradeda jokio jos tafs paleidimo, išskyrus Coach atliekamą nustatymą; niekas neištrinama.

Mokėjimas ir sąskaitos faktūros. Už planą atsiskaitoma vienoje vietoje. Svetainėje kiff.dev įsigytas planas ten perkamas, keičiamas ir atšaukiamas, taip pat atsiskaitymų portale, į kurį ji nukreipia, o jo mokėjimus apdoroja Stripe. Programėlėje tafs įsigytas Solo parduodamas per App Store iPhone ir iPad įrenginiuose arba per Google Play Android įrenginiuose ir valdomas jūsų Apple arba Google Play prenumeratose pagal tos parduotuvės mokėjimo, atnaujinimo ir pinigų grąžinimo sąlygas. Kreditų paketai perkami programėlėje. Company perkamas svetainėje kiff.dev. Mokesčiai apmokestinami kas mėnesį iš anksto; naudojimas virš įskaičiuoto kiekio įtraukiamas į kitą sąskaitą faktūrą. Žmonių skaičiaus pokytis taikomas nuo kitos sąskaitos faktūros. Jei mokėjimas nepavyksta ir nėra atliktas vėliau, planas baigiasi, o KIFF atmeta paskyros užklausas, kol pasirenkamas planas.

Atšaukimas. Galite atšaukti bet kada atsiskaitymų portale arba, jei Solo įsigytas programėlėje, savo Apple arba Google Play prenumeratose. Planas galioja iki apmokėto laikotarpio pabaigos ir nėra atnaujinamas. Išskyrus atvejus, kai to reikalauja įstatymas, jau sumokėti mokesčiai negrąžinami. Jei esate vartotojas ES, galite atsisakyti pirkimo per 14 dienų nenurodydami priežasties, ir mes grąžinsime tai, ką sumokėjote, atėmę dalį už dienas, kurias planas jau veikė. Už Solo pirkimus pinigai grąžinami per App Store arba Google Play pagal jų sąlygas.

PVM. Company kainos nurodytos be PVM; Solo ir kreditų paketus parduoda App Store arba Google Play su PVM. Kiff Agents OÜ (PVM mokėtojo kodas EE102924327) prideda Estijos PVM, kai jis taikomas. ES verslo sandoriams su Klientu už Estijos ribų, turinčiu galiojantį PVM mokėtojo kodą, sąskaitos faktūros išrašomos taikant ES atvirkštinio apmokestinimo mechanizmą.

Pakeitimai. Apie kainų padidinimą aktyvioms paskyroms raštu pranešame ne vėliau kaip prieš trisdešimt dienų (16 punktas). Kainų sumažinimas taikomas nuo kitos sąskaitos faktūros.

## 9. Galiojimas ir nutraukimas

Šios sąlygos įsigalioja, kai pirmą kartą pasinaudojate Paslauga, ir galioja, kol nutraukiamos.

Galite nutraukti bet kada ištrindami savo paskyrą. Mes galime nutraukti (a) dėl esminio pažeidimo, kuris nepašalintas per trisdešimt dienų nuo rašytinio pranešimo, arba (b) nedelsiant dėl Priimtino naudojimo pažeidimų ar nemokumo atvejų.

Išėjimas ir duomenų pasiėmimas. Galite bet kada eksportuoti savo duomenis struktūrizuotu, kompiuterio skaitomu formatu: KIFF skiltyje Settings → Data gaunate JSON failą su jūsų paskyra, jūsų Card su kiekviena jų versija, sprendimais jūsų Record (naujausiais 10 000; daugiau galite paprašyti mūsų), pasirašytu kvitu dėl kiekvieno, kurį KIFF leido, ir jūsų API raktų informacija (niekada pačiais raktais). Programėlėje tafs kiekvienas asmuo eksportuoja savo pokalbius, failus ir pastabas skiltyje Account → Export my chats; pokalbiai bet kuriuo atveju saugomi trisdešimt dienų, kaip nurodyta privatumo politikoje. Ištrynus paskyrą, Paslauga nedelsiant nutraukiama, todėl pirmiausia eksportuokite duomenis.

Vietoj to galite išeiti pranešę, kad pereitumėte pas kitą teikėją ar į savo sistemas, parašę adresu [support@kiff.dev](mailto:support@kiff.dev). Niekada nereikalaujame ilgesnio nei dviejų mėnesių įspėjimo: jūsų pranešimas įsigalioja jūsų pasirinktą dieną, ne vėliau kaip po dviejų mėnesių nuo jo išsiuntimo. Nuo tos dienos prasideda trisdešimties dienų pereinamasis laikotarpis, kurio metu Paslauga toliau veikia, jūs eksportuojate savo duomenis ir perkeliate savo darbą, o mes padedame vykdydami pagrįstus prašymus. Pereinamąjį laikotarpį galite pratęsti vieną kartą, tiek, kiek jums reikia. Jei techniškai negalime jo užbaigti per trisdešimt dienų, pranešame jums per keturiolika darbo dienų nuo jūsų pranešimo, nurodydami priežastis ir naują ne ilgesnį kaip septynių mėnesių laikotarpį. Pasibaigus pereinamajam laikotarpiui, jūsų duomenys lieka prieinami eksportui dar bent trisdešimt dienų. Plano mokesčiai mokami įprastai, kol baigiasi pereinamasis laikotarpis. App Store arba Google Play įsigytas Solo atšaukiamas parduotuvėje; pereinamasis ir eksporto laikotarpiai taikomi taip pat.

Už išėjimą, perėjimą ar duomenų eksportą mokesčio neimame. Šis skirsnis kiekvienam klientui suteikia ES duomenų akto (Reglamentas (ES) 2023/2854) VI skyriuje numatytas perėjimo teises.

Nutraukus sutartį ir pasibaigus bet kokiam pereinamajam ir eksporto laikotarpiui, ištriname jūsų Audito duomenis, jūsų tafs duomenis ir paskyros metaduomenis pagal [privatumo politikos](/privacy#retention) saugojimo taisykles.

Skirsniai, kurie pagal savo pobūdį turėtų galioti ir nutraukus sutartį, toliau galioja, įskaitant konfidencialumą, intelektinę nuosavybę, žalos atlyginimą, atsakomybės apribojimą, atsisakymus, taikytiną teisę ir bet kokius sukauptus mokesčius.

## 10. Garantijos

Kiekviena šalis pareiškia, kad turi teisę ir įgaliojimus sudaryti ir vykdyti šias sąlygas.

Garantuojame, kad Paslaugą teiksime profesionaliai ir iš esmės laikydamiesi Dokumentacijos. Jūsų vienintelė teisių gynimo priemonė pažeidus šią garantiją yra, mūsų pasirinkimu, pakartotinis įvykdymas arba proporcingas mokesčių, tenkančių paveiktai Paslaugai pažeidimo laikotarpiu, grąžinimas.

Jūs garantuojate, kad jūsų naudojimasis Paslauga atitinka taikomus teisės aktus ir šias sąlygas.

## 11. Atsakomybės atsisakymas

Išskyrus 10 punkte aiškiai nurodytas garantijas, Paslauga teikiama „tokia, kokia yra“ ir „tokia, kokia prieinama“. Kiek tai leidžia įstatymai, atsisakome visų kitų garantijų, aiškiai išreikštų, numanomų ar nustatytų įstatymu, įskaitant tinkamumo parduoti, tinkamumo konkrečiam tikslui, teisių nepažeidimo garantijas ir bet kokias garantijas, kylančias iš verslo praktikos ar prekybos papročių.

Negarantuojame, kad Paslauga veiks be pertrūkių, be klaidų ar be kenksmingų komponentų, nei kad jokie duomenys nebus prarasti ar sugadinti. Jūs atsakote už savo duomenų atsarginių kopijų darymą.

## 12. Atsakomybės apribojimas

Kiek tai leidžia įstatymai, nė viena šalis neatsako kitai už netiesioginę, atsitiktinę, pasekminę, specialiąją, pavyzdinę ar baudinę žalą, įskaitant prarastą pelną, prarastas pajamas, prarastus duomenis ar veiklos sutrikdymą, kylančią iš šių sąlygų ar Paslaugos arba susijusią su jomis, net jei buvo informuota apie tokios žalos galimybę.

Bendra kiekvienos šalies atsakomybė, kylanti iš šių sąlygų ar susijusi su jomis, neviršija didesniosios iš šių sumų: (a) mokesčių, kuriuos Klientas sumokėjo ar turi sumokėti Kiff Agents OÜ per dvylika mėnesių iki įvykio, dėl kurio atsiranda atsakomybė, arba (b) šimto tūkstančių eurų (100 000 €).

Niekas šiose sąlygose neriboja ir nepanaikina atsakomybės, kurios negalima apriboti ar panaikinti pagal taikomus teisės aktus, įskaitant atsakomybę už mirtį, dėl neatsargumo padarytą kūno sužalojimą, sukčiavimą ar tyčinį nusižengimą, taip pat atsakomybę už produktą su trūkumais pagal teisės aktus, kuriais įgyvendinama Direktyva (ES) 2024/2853 dėl atsakomybės už produktus su trūkumais.

## 13. Žalos atlyginimas

Mes ginsime jus nuo trečiųjų šalių reikalavimų, kad Paslauga, kokią mes ją teikiame ir kokia jūs ja naudojatės pagal šias sąlygas, pažeidžia tos trečiosios šalies intelektinės nuosavybės teises, ir sumokėsime galutiniu sprendimu prieš jus priteistą žalą (arba sutartą taikos sutarties sumą) pagal tokius reikalavimus.

Jūs ginsite mus nuo trečiųjų šalių reikalavimų, kylančių iš (a) jūsų, kaip Kliento, duomenų ar turinio; (b) jūsų naudojimosi Paslauga pažeidžiant šias sąlygas (įskaitant Priimtino naudojimo politiką); arba (c) jūsų didelio neatsargumo ar tyčinio nusižengimo, ir sumokėsite galutiniu sprendimu prieš mus priteistą žalą (arba sutartą taikos sutarties sumą) pagal tokius reikalavimus.

Žalos atlyginimo sąlygos: skubus pranešimas apie reikalavimą, išimtinė atlyginančiosios šalies gynybos ir taikos sutarties kontrolė ir pagrįstas atlyginimą gaunančiosios šalies bendradarbiavimas. Atlyginančioji šalis be sutikimo negali sudaryti taikos sutarties, nustatančios įsipareigojimų atlyginimą gaunančiajai šaliai.

## 14. Paslaugų lygio įsipareigojimas

Mokamame plane [paslaugų lygio sutartis](/legal/sla) nustato prieinamumą, kurį įsipareigojame užtikrinti, kaip įskaičiuojama lėta ar nepavykusi užklausa, mūsų pagalbos atsakymo laikus ir paslaugų kreditus, kai įsipareigojimo nevykdome. Ji yra šių sąlygų dalis.

Veiklos praktika, reagavimas į incidentus ir įsipareigojimai pranešti apie pažeidimus aprašyti datuotoje redakcijoje, į kurią nurodo šios Sąlygos ([kiff.dev/docs/archive/2026-10-10](https://kiff.dev/docs/archive/2026-10-10)); puslapyje [/security](/security) pateikiama dabartinė praktika.

## 15. Paslaugos pakeitimai

Galime bet kada pridėti, keisti ar pašalinti funkcijas. Apie esminį funkcionalumo sumažinimą ar API dalies, kuri sugadina dokumentuotą elgseną, nebenaudojimą aktyviems klientams pranešama ne vėliau kaip prieš trisdešimt dienų, paskelbiant nebenaudojimo tvarkaraštį. Saugumo sumetimais pakeitimai gali būti atliekami be pranešimo, jei pats pranešimas sukeltų riziką.

## 16. Šių sąlygų pakeitimai

Retkarčiais galime keisti šias sąlygas. Apie esminius pakeitimus pranešama el. paštu ir gerai matomu pranešimu šiame puslapyje ne vėliau kaip prieš trisdešimt dienų. Neesminiai pakeitimai (paaiškinimai, nuorodos) skelbiami su nauja „paskutinio atnaujinimo“ data. Jei toliau naudojatės Paslauga po atnaujinimo įsigaliojimo datos, laikoma, kad jį priimate.

## 17. Nenugalima jėga

Nė viena šalis neatsako už įsipareigojimų neįvykdymą ar vėlavimą, kiek jį lėmė aplinkybės, kurių ji negali pagrįstai kontroliuoti, įskaitant gamtos jėgas, karą, terorizmą, masinius neramumus, darbo ginčus, gaisrą, stichines nelaimes, valdžios veiksmus dėl pandemijos, interneto ar elektros tiekimo sutrikimus arba telekomunikacijų teikėjų ar trečiųjų šalių talpinimo infrastruktūros veiksmus.

## 18. Taikytina teisė ir jurisdikcija

Šioms sąlygoms taikoma Estijos Respublikos teisė, neatsižvelgiant į jos kolizines normas. Išimtinė ginčų nagrinėjimo vieta yra Harju apskrities teismas (Harju Maakohus) Taline.

Jei esate vartotojas, gyvenantis kitoje ES valstybėje narėje, privalomosios jūsų gyvenamosios šalies vartotojų apsaugos taisyklės toliau taikomos tiek, kiek jos suteikia jums teises, kurių negalite atsisakyti sutartimi; ši nuostata neatima iš jūsų tų taisyklių apsaugos.

## 19. Bendrosios nuostatos

Visa sutartis. Šios sąlygos kartu su privatumo politika, DPA, bet kokiomis užsakymo formomis ir bet kokiomis aiškiai įtrauktomis papildomomis sąlygomis sudaro visą mūsų susitarimą dėl jų dalyko ir pakeičia bet kokį ankstesnį ar tuo pat metu buvusį susitarimą.

Atskiriamumas. Jei kuri nors nuostata pripažįstama neįgyvendinama, kitos lieka galioti.

Perleidimas. Negalite perleisti šių sąlygų be mūsų rašytinio sutikimo. Mes galime jas perleisti be sutikimo susijungimo, įsigijimo ar viso arba iš esmės viso mūsų turto pardavimo atveju, apie tai jums pranešę.

Teisių neatsisakymas. Jei kuri nors nuostata nebuvo įgyvendinta, tai nereiškia, kad atsisakoma teisės ją įgyvendinti vėliau.

Pranešimai. Pranešimai Kiff Agents OÜ siunčiami adresu [legal@kiff.dev](mailto:legal@kiff.dev) arba paštu adresu Tööstuse tn 75-71, Põhja-Tallinna linnaosa, Tallinn 10416, Estija. Pranešimai jums siunčiami su jūsų paskyra susietu el. pašto adresu arba, jei jį nurodėte, jūsų nurodytu teisinių pranešimų adresu.

Nepriklausomos šalys. Niekas šiose sąlygose nesukuria partnerystės, atstovavimo, jungtinės veiklos ar darbo santykių.

Tretieji naudos gavėjai. Jų nėra, išskyrus tiek, kiek aiškiai nurodytos susijusios įmonės, kurioms atlyginama žala.

## 20. Susisiekite su mumis

Komerciniai ir sutartiniai klausimai: [legal@kiff.dev](mailto:legal@kiff.dev).

Bendri klausimai apie produktą: [hello@kiff.dev](mailto:hello@kiff.dev). Privatumo klausimai: [privacy@kiff.dev](mailto:privacy@kiff.dev). Pranešimai apie saugumo spragas: [security@kiff.dev](mailto:security@kiff.dev).

Dokumento versija: v0.1. Paskutinį kartą atnaujinta: 2026 m. lapkričio 9 d.
