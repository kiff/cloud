# Privatumo politika

Paskutinį kartą atnaujinta: 2026 m. lapkričio 17 d. Įsigaliojimo data: 2026 m. gegužės 24 d.

Šis vertimas skelbiamas kartu su tekstu anglų kalba. Jei jie skiriasi, taikomas tekstas anglų kalba.

Ši politika paaiškina, kaip KIFF Cloud tvarko asmens duomenis svetainėje, valdymo skydelyje, API ir programėlėje tafs, skirtoje iPhone, iPad, Android įrenginiams ir kompiuteriams. Klausimai: [privacy@kiff.dev](mailto:privacy@kiff.dev).

## 1. Kas mes esame

KIFF Cloud valdo Kiff Agents OÜ, Estijoje įsteigta uždaroji akcinė bendrovė. Registro informacija:
- Registro kodas: 17372313 (Estijos elektroninis verslo registras, e-Business Register)
- PVM mokėtojo kodas: EE102924327
- Registruota buveinė: Tööstuse tn 75-71, Põhja-Tallinna linnaosa, Tallinn 10416, Estija
- Kontaktai privatumo klausimais: [privacy@kiff.dev](mailto:privacy@kiff.dev)
- Oficialūs teisiniai pranešimai: [legal@kiff.dev](mailto:legal@kiff.dev)

Kadangi Kiff Agents OÜ yra įsisteigusi Europos Sąjungoje, ES Bendrasis duomenų apsaugos reglamentas (BDAR) mūsų duomenų tvarkymui taikomas tiesiogiai. Mums nereikia ES atstovo pagal 27 straipsnį.

## 2. Šios politikos taikymo sritis

Ši politika apima asmens duomenis, kuriuos tvarkome, kai naudojatės:
- [kiff.dev](https://kiff.dev), mūsų rinkodaros svetaine, šios politikos puslapiu ir viešu kvitų rodymo puslapiu.
- [app.kiff.dev](https://app.kiff.dev), mūsų valdymo skydeliu, kuriam reikia prisijungti.
- [api.kiff.dev](https://api.kiff.dev), mūsų HTTP API.
- tafs: mūsų programėle, skirta iPhone, iPad, Android įrenginiams ir kompiuteriams (Mac, Windows ir Linux), ir paslauga, kuria ji grindžiama (įskaitant Setup, kuris prijungia įmonės įrankius, ir planque, kuris saugo tuos ryšius), kurioje įmonė patiki darbą AI agentui, dirbančiam KIFF Card ribose.

Ši politika netaikoma atvirojo kodo karkasui kiff/kiff, kurį galite vykdyti patys. Kai karkasą vykdote savo infrastruktūroje, esate visų jo tvarkomų duomenų valdytojas, o Kiff Agents OÜ šiame tvarkyme nedalyvauja.

## 3. Mūsų vaidmenys pagal BDAR

Priklausomai nuo duomenų, veikiame dviem vaidmenimis:
- Duomenų valdytojas paskyros ir prisijungimo metaduomenų, atsiskaitymo duomenų (kai atsiskaitymas aktyvus), rinkodaros pranešimų, kuriuos užsisakote, ir mūsų pačių paslaugos veikimo telemetrijos atžvilgiu.
- Duomenų tvarkytojas audito duomenų, veiksmų įrašų, sprendimų kvitų ir kito turinio, kuris mūsų klientų vardu keliauja per vykdymo aplinką (runtime), atžvilgiu, taip pat to, ką tafs jiems saugo (kiekvieno taf paleidimų, pokalbių, ataskaitų, atminties pastabų ir jų pateikiamų failų), atžvilgiu. Klientai (tenants) yra šių duomenų valdytojai; jie duoda mums nurodymus per savo paskyros nustatymus ir domeno konfigūraciją. Šiuos santykius reglamentuoja Duomenų tvarkymo sutartis (DPA); paskelbtos sąlygos pateikiamos [DPA šablone](/legal/dpa).

Ši politika daugiausia skirta duomenų tvarkymui, kurį atliekame kaip duomenų valdytojas. Kai minime duomenis, kuriuos tvarkome kaip duomenų tvarkytojas, konkretesnis dokumentas yra DPA.

## 4. Mūsų tvarkomų asmens duomenų kategorijos

Toliau nurodytos kategorijos atitinka techninį sąrašą puslapyje [/security#data-we-process](/security#data-we-process). Jei jie skiriasi, lemiamas yra techninis puslapis, ir mes šį skirsnį atnaujiname, kad jie sutaptų.
- Paskyros identifikatoriai: jūsų autentifikavimo subjektas (kurį išduoda mūsų tapatybės teikėjas) ir el. pašto adresas, kuriuo prisijungiate. Šiuo metu saugomi nepakeisti; maišos (hash) reikšmių saugojimas planuojamas.
- Tenant metaduomenys: tenant identifikatorius, slug, rodomas pavadinimas, sukūrimo laiko žyma ir kalba, kurią kiekvieno asmens programėlė naudojo paskutinį kartą, kad KIFF jam siunčiami el. laiškai būtų ta kalba.
- API prisijungimo duomenys: nepermatomi API raktų įrašai (saugome maišos reikšmes, niekada atvirojo teksto).
- Sesijos slapuko duomenys: HMAC pasirašytas sesijos slapukas, nustatomas prisijungus. Žr. skirsnį apie slapukus toliau.
- HTTP užklausų metaduomenys: IP adresas, naudotojo agentas (user agent), užklausos kelias, atsakymo būsena, delsa. Saugomi mūsų atvirkštinio tarpinio serverio (reverse proxy) prieigos žurnaluose ribotą laiką saugumo ir piktnaudžiavimo prevencijos tikslais.
- Tenant kontroliuojamas turinys: parametrai, veikėjas ir objektų duomenys, kuriuos siunčiate vykdymo aplinkai. Juos saugome kaip duomenų tvarkytojas kliento vardu; klientas atsako už tai, kas išsiunčiama. Duomenų slėpimo (redaction) konfigūracija kiekvienam domenui planuojama.
- Susirašinėjimas: el. laiškai, kuriuos siunčiate į mūsų pagalbos, pardavimų, saugumo ar privatumo pašto dėžutes.
- Programėlė tafs: prie tafs prisijungiate naudodami savo KIFF paskyrą. Telefone ar planšetėje programėlė saugo jūsų sesiją įrenginio Keychain (Android įrenginyje – jo šifruotoje saugykloje). Kompiuteryje ji saugo ją užantspauduotą jūsų naudotojo paskyrai: Windows kompiuteryje – naudodama pačios Windows duomenų apsaugą, Linux – jūsų keyring, Mac – jo Keychain. Kompiuterio sesija trunka iki 90 dienų. Programėlė sukuria pasirašymo raktą, kuris niekada nepalieka įrenginio: iPhone, iPad ar Mac Secure Enclave, Android įrenginio Keystore arba Windows kompiuterio TPM. Linux ir Mac be Secure Enclave raktas yra programinis raktas, saugomas jūsų keyring ar Keychain, ir toks kompiuteris gali rodyti, kas jūsų laukia, bet negali to patvirtinti; patvirtinate savo telefone. Windows kompiuteris be TPM negali prisijungti prie tafs. KIFF saugo viešąją rakto dalį, įrenginio pavadinimą ir platformą, taip pat kada jis buvo pridėtas ir paskutinį kartą naudotas, kad galėtų patikrinti jūsų atsakymus į sulaikytas užklausas ir jūsų ryšių patvirtinimus. Jei patvirtinate prieigos raktu (passkey) savo kompiuteryje, pavyzdžiui, Windows Hello ar saugumo raktu, jūsų veidas, piršto atspaudas ar PIN lieka paties kompiuterio sistemoje; KIFF saugo prieigos rakto viešąjį raktą, jo identifikatorių, skaitiklį ir pavadinimą, niekada nieko slapto, ir atmeta prieigos raktą, kuris sinchronizuojamas su kitais įrenginiais. Jei įjungiate įspėjimus telefone, planšetėje ar Mac, kuriame veikia iPad programėlė, saugome įrenginio push žetoną kartu su jūsų KIFF asmens identifikatoriumi ir kalba, kurią rodė programėlė, kad jos įspėjimai būtų ta kalba. Paslauga tafs taip pat saugo paskyros kalbą, pagal tai, kas paskutinis kalbėjosi su Coach, iki trijų mėnesių, kad taf ataskaitos būtų ta kalba. Telefono įspėjimas keliauja per Apple ar Google tik su informacija, kuris taf ir ko jis nori, o durų (door) įspėjime – su to, kas parašė, vardu ar el. pašto adresu: niekada su suma ar užklausos informacija. Programėlė juos prideda pačiame įrenginyje, iš KIFF, kad atrakinto telefono įspėjimas galėtų rodyti sumą, prekybininką ar asmenį. Užrakintame Android telefone jis rodo tik „A taf needs you“, o užrakintame iPhone – taip pat, nebent nustatėte, kad jo peržiūros būtų rodomos visada. Jūsų laikrodis: laikrodis nesaugo slaptažodžio ar rakto, bendrauja tik su jūsų susietu telefonu ir niekada netvirtina. Jis siunčia tai, ką sakote Coach ar jūsų pasirinktam taf, tik kaip tekstą, per telefoną, kai esate leidę AI. Paties laikrodžio kalbos paslauga, Apple ar Google, paverčia jūsų žodžius tuo tekstu. Jis gali atmesti, sustabdyti ar siųsti Coach ar taf tik tada, kai turi ekrano užraktą ar kodą ir yra atrakintas. Tai, ką siunčiate taf, patenka į to taf pokalbį, tarsi būtumėte tai parašę telefone. taf pokalbis, įskaitant sumas, laikrodyje rodomas tik tada, kai jis atidarytas, kiekvieną kartą nuskaitomas per telefoną; laikrodis nieko iš jo nesaugo. Apple Watch: jis rodo jūsų tafs ir jų puslapius jų nesaugodamas. Jis saugo tik jūsų Inbox faile, kurio negali perskaityti, kol yra užrakintas: kiekvieną sulaikytą užklausą kaip jos taf pavadinimą ir tai, ko ji prašo, niekada sumos, ir Coach atsakymus į tai, ką siuntėte iš laikrodžio, kaip tekstą. Jei atsijungiate laikrodyje, jis taip pat saugo, kad tai padarėte, kol vėl prisijungiate laikrodyje. Be to, jis nieko nesaugo, nebent laikrodis turi kodą. Inbox ištrinamas, kai atsijungiate laikrodyje arba atsijungia jūsų iPhone, arba kitą kartą, kai programėlė atidaroma ar uždaroma be kodo. Jis nieko neįrašo, o vieninteliai jo rodomi pranešimai yra jūsų iPhone pranešimai. Wear OS: jis rodo jūsų paskyros el. paštą, šiandienos kalendorių ir jūsų tafs puslapius jų nesaugodamas. Jis saugo jūsų Inbox, kur kiekviena eilutė yra taf pavadinimas ir tai, ką jis sako, niekada suma, ir vieną vėlesniam laikui išsaugotą žinutę, kaip tekstą. Atsijungus laikrodyje ar telefone, visa tai ištrinama. Savo plytelei (tile) jis taip pat saugo, kiek jūsų laukia, rodo tai tik tada, kai laikrodis atrakintas, ir ištrina, kai atsijungiate arba kitą kartą, kai plytelė rodoma, kol laikrodis užrakintas arba neturi ekrano užrakto. Atsijungus laikrodyje, jis saugo pastabą, kad tai padarėte, kol vėl prisijungiate. Inbox taip pat ištrinamas kitą kartą, kai programėlė atidaroma ar uždaroma, kol laikrodis užrakintas arba neturi ekrano užrakto. Vėlesniam laikui išsaugota žinutė lieka, kol ją išsiunčiate arba ištrinate, nes prašėte ją išsiųsti. Laikrodis nesaugo jokių įrašų. taf rezultatas gali likti laikrodžio pranešimuose, kol jį uždarote arba atsijungiate laikrodyje. Windows kompiuteryje, Linux kompiuteryje ar Mac su macOS 12 programėle įspėjimus kuria pati programėlė, kol veikia, be push žetono ir be jokios kitos įmonės. Windows ir Linux jie rodo, kuris taf, ką jis nori daryti, sumą, kodėl ir iki kada; macOS 12 programėlėje jie rodo KIFF formuluotę apie užklausą, kai juos įjungiate, o užrakintas ekranas rodo tai, ką leidžia Mac peržiūros nustatymas. Windows jie išjungti, kol juos įjungiate, ir, kol ekranas užrakintas, rodo tik „A taf needs you“. Pranešimas, parodytas prieš užrakinant ekraną, išlaiko savo informaciją Windows pranešimų sąraše. Linux jie įjungti, kol juos išjungiate, o tai, ką rodo užrakintas ekranas, priklauso nuo jūsų darbalaukio aplinkos. Kai prijungiate įrankį, prisijungiate prie paties įrankio teikėjo. Programėlei, prie kurios planque prisijungia pats, pavyzdžiui, Stripe, ar jūsų pateiktam raktui planque saugo tą žetoną ar raktą jūsų įmonei, užšifruotą; valdomam ryšiui prisijungimą saugo Composio, ryšių paslauga, su kuria dirba planque, o planque saugo tik ryšio įrašą ir jūsų patvirtintus įrankius (visa tai kaip duomenų tvarkytojas, žr. toliau). KIFF naudoja valdomus ryšius tik puslapyje [/subprocessors](/subprocessors#composio) nurodytoms programėlėms. Programėlėje nėra analitikos ar reklamos sekiklių.
- App Store pirkimai: kai tafs perkate Solo ar kreditų paketą, jį jums parduoda Apple: Apple yra pardavėjas, gauna mokėjimą ir saugo jūsų mokėjimo informaciją. Apple mums praneša, kas nupirkta, kuriai KIFF paskyrai (atsitiktinis identifikatorius, kurį programėlė prideda prie pirkimo), kada, Apple operacijos identifikatorių ir, Solo atveju, kada laikotarpis baigiasi ar atnaujinamas. Tai naudojame, kad pridėtume kreditus ar planą prie jūsų paskyros. Apple mums neperduoda jūsų Apple ID, vardo, el. pašto adreso ar kortelės.
- Google Play pirkimai: Android įrenginiuose tie patys pirkimai vyksta per Google Play atsiskaitymų sistemą: Google gauna mokėjimą ir saugo jūsų mokėjimo informaciją. Google mums praneša, kas nupirkta, kuriai KIFF paskyrai (tas pats atsitiktinis identifikatorius), kada, pirkimo žetoną ir užsakymo identifikatorių ir, Solo atveju, kada laikotarpis baigiasi ar atnaujinamas, ir tai naudojame taip pat. Google mums neperduoda jūsų Google paskyros, vardo, el. pašto adreso ar kortelės.

## 5. Teisinis duomenų tvarkymo pagrindas

Pagal BDAR 6 straipsnį remiamės šiais teisiniais pagrindais:
- Sutarties vykdymas (6 straipsnio 1 dalies b punktas) paskyrai sukurti, autentifikuoti, vykdymo aplinkai jūsų vardu valdyti, atsiskaityti (kai tai aktyvu) ir pagrindinėms paslaugos funkcijoms.
- Teisėti interesai (6 straipsnio 1 dalies f punktas) saugumui, sukčiavimo prevencijai, piktnaudžiavimo aptikimui, veikimo telemetrijai ir paslaugos apsaugai. Mes pasveriame savo interesus ir jūsų teises ir paprašius pasakysime, koks buvo to vertinimo rezultatas.
- Sutikimas (6 straipsnio 1 dalies a punktas) AI programėlėje tafs: tam, kas siunčiama AI modeliui, kuris valdo Coach ir jūsų tafs, kaip aprašyta [toliau](#ai-in-tafs). Niekas nesiunčiama, kol to neleidžiate, o sutikimą galite atšaukti skiltyje Account. Sutikimo taip pat prašytume prieš bet kokius analitikos slapukus, neprivalomus rinkodaros pranešimus ar naują duomenų tvarkymą, kuris iš esmės išplečia jūsų duomenų naudojimą. Mes nevykdome analitikos, reklamos ir nebūtino sekimo.
- Teisinė prievolė (6 straipsnio 1 dalies c punktas) mokesčių apskaitos įrašams (Estijos buhalterinės apskaitos įstatymo 12 straipsnis, 7 metai), atsakymams į teisėtus institucijų prašymus ir saugojimui, kurio reikalauja pinigų plovimo prevencijos taisyklės, kai jos taikomos.

### AI programėlėje tafs

Programėlė tafs paklausia jūsų prieš bet ką siųsdama AI, šiais žodžiais:
- tafs naudoja Anthropic AI Claude, kad veiktų Coach ir jūsų tafs.
- Coach ir jūsų tafs yra AI agentai: tai, ką jie rašo, sukuria AI, todėl tai gali būti klaidinga. Žinutėje, kurią taf siunčia už jūsų įmonės ribų, nurodoma, kad ją parašė AI agentas.
- Kas jam siunčiama: tai, ką rašote Coach ir savo tafs, jūsų pridėti failai, tai, ką jūsų prijungtos programėlės grąžina taf, kol jis dirba, ir tai, kas yra jūsų ekrane, kol Coach jums padeda programėlėje.
- Kas niekada nesiunčiama: slaptažodžiai ir raktai, kuriuos naudoja jūsų prijungtos programėlės, ar jūsų piršto atspaudas.
- Anthropic tai naudoja atsakyti ir saugo iki 30 dienų.
- Niekas nesiunčiama, kol to neleidžiate. Sutikimą galite atšaukti skiltyje Account.

taf, kuris veikia, kai tuo metu niekas nedalyvauja (pagal savo tvarkaraštį arba kai kas nors gaunama), remiasi asmens, kuris jį sukūrė ar perėmė, sutikimu, o kitu atveju – pirmojo asmens, kuris tvarko paskyros tafs ir jį leidžia, sutikimu.

Kai atšaukiate sutikimą, Coach ir jūsų tafs nustoja jums atsakinėti. taf, kurio paleidimai grindžiami jūsų sutikimu, pristabdomas: jo tvarkaraščiai sustoja, o tai, kas gaunama, laukia. Jis tęsia, kai sutikimas duodamas iš naujo arba kas nors, kas tvarko paskyros tafs, jį perima su savo sutikimu. Atšaukimas nekeičia to, kas buvo išsiųsta anksčiau.

Anthropic nurodyta puslapyje [/subprocessors](/subprocessors) kartu su tuo, kur ji tvarko duomenis ir kada gali juos saugoti ilgiau.

## 6. Duomenų subtvarkytojai

Mes neparduodame jūsų asmens duomenų ir nesidalijame jais reklamos tikslais.

Paslaugai teikti pasitelkiame nedidelį tiekėjų ratą. Oficialus, dabartinis sąrašas pateikiamas puslapyje [/subprocessors](/subprocessors); šis puslapis atnaujinamas per 30 dienų po bet kokio pakeitimo. Reikalaujame, kad kiekvienas subtvarkytojas pagal rašytines sutartis užtikrintų mūsiškei lygiavertę apsaugą, įskaitant ES standartines sutarčių sąlygas, kai asmens duomenys perduodami už ES ribų.

Apie esminius subtvarkytojų papildymus ar pakeitimus, susijusius su asmens duomenimis, aktyviems klientams pranešama iš anksto, suteikiant teisę prieštarauti.

## 7. Tarptautinis duomenų perdavimas

Kai kurie mūsų subtvarkytojai yra įsisteigę už Europos ekonominės erdvės ribų, daugiausia Jungtinėse Amerikos Valstijose. Kiekvienam tokiam perdavimui remiamės bent viena iš šių apsaugos priemonių:
- ES ir JAV duomenų privatumo sistema (EU–US Data Privacy Framework): JAV subtvarkytojams, sertifikuotiems pagal šią sistemą. Sprendimas dėl tinkamumo galioja nuo 2023 m. vidurio; stebime bet kokius jo statuso pokyčius.
- Standartinės sutarčių sąlygos: 2 modulis (iš duomenų valdytojo tvarkytojui) mūsų tiesioginiams subtvarkytojams ir 3 modulis (iš tvarkytojo subtvarkytojui), kai yra tvarkytojų grandinė. Naudojame Komisijos patvirtintas 2021 m. standartines sutarčių sąlygas.
- Papildomos priemonės: kai rizikos vertinimas rodo, kad reikia papildomų apsaugos priemonių (šifravimas perduodant ir saugant, prieigos registravimas, duomenų kiekio mažinimas).

Galite paprašyti konkrečios perdavimo priemonės, taikomos konkrečiam subtvarkytojui, parašę adresu [privacy@kiff.dev](mailto:privacy@kiff.dev).

**Jūsų šalis.** Kai kurių šalių įstatymai reikalauja daugiau, nei nurodyta šioje politikoje. Ką jie papildomai nustato, anglų kalba ir tos šalies kalba, rasite puslapyje [Jūsų šalis](/privacy/countries).

## 8. Saugojimas

Asmens duomenis saugome trumpiausią laikotarpį, atitinkantį jų tvarkymo tikslus ir bet kokią taikomą teisinę prievolę.
- Paskyros ir prisijungimo metaduomenys: kol paskyra ištrinama. Programėlėje tafs savininkas ją ištrina skiltyje Account → Delete account. Žiniatinklyje sukurta paskyra ištrinama parašius adresu [privacy@kiff.dev](mailto:privacy@kiff.dev), per BDAR 12 straipsnio 3 dalyje nustatytą vieno mėnesio terminą. Bet kuriuo atveju paskyros raktai nedelsiant nustoja veikti. Jei paskyra ištrinama programėlėje tafs, to atšaukti negalima: jos tafs pokalbiai, pastabos ir ryšiai ištrinami nedelsiant, o tai, ką saugo KIFF, ištrinama per 30 dienų. Jei paskyra ištrinama žiniatinklyje, jos duomenis saugome 30 dienų atidėjimo laikotarpį, jei savininkas norėtų ją susigrąžinti, o tada ištriname.
- API raktų įrašai: kol paskyra ištrinama, taip pat ir atšaukus raktą. Raktas, priklausantis agento prisijungimo ryšiui, ištrinamas kartu su tuo ryšiu praėjus 30 dienų po jo pabaigos.
- Sesijos slapukai: kol baigia galioti (pagal numatytuosius nustatymus 24 valandos) arba kol atsijungiate.
- Atvirkštinio tarpinio serverio prieigos žurnalai: iki 90 dienų, tada apibendrinami arba ištrinami.
- Paslaugų žurnalai: tai, ką KIFF paslaugos ir planque registruoja veikdamos: klaidos ir užklausos, kuriose gali būti nurodyta paskyra, asmuo ar agentas. 30 dienų, tada ištrinami.
- Atsarginės kopijos: KIFF duomenų bazės atsarginė kopija daroma kasdien, užšifruota, ir kiekviena kopija saugoma 7 dienas, todėl ištrintas įrašas iš jų dingsta per 7 dienas. tafs ir planque saugyklos saugo užšifruotas tam tikro laiko momento kopijas 35 dienas, naudojamas tik saugyklai atkurti po gedimo, todėl ištrintas įrašas iš jų dingsta per 35 dienas.
- Tenant audito duomenys: saugomi kaip duomenų tvarkytojo pagal kliento tenant konfigūraciją. Pagal numatytuosius nustatymus: saugomi visą tenant gyvavimo laiką; ištrinami ištrynus tenant. Žr. [/security#deletion](/security#deletion).
- Užklausos per KIFF šliuzą: užklausos argumentai, kol ji išsiunčiama, atmetama ar nepavyksta, tada ištrinami; įrankio rezultatas 24 valandas, tada ištrinamas. Sprendimo įrašas ir argumentų maišos reikšmė saugomi kaip tenant audito duomenys. Žr. [/security#data-kept](/security#data-kept).
- Naudojimas: dėl kiekvienos užklausos per šliuzą, kai ji baigiasi: kuris agentas ją pateikė, įrankis ir ar ji buvo įskaičiuota kaip veiksmas, ar buvo nemokama (ir kodėl). Dėl kiekvieno taf ar Coach paleidimo, kai jis baigiasi: taf, kada, kreditai, kuriuos panaudojo jo AI, ir ar jie buvo įskaičiuoti, ar kodėl ne. Kol paskyra ištrinama; tai, kas atsiskaitymo tikslais pranešama mūsų mokėjimų tvarkytojui, saugoma kartu su atsiskaitymo įrašais.
- Agentų prisijungimo ryšiai: kuris agentas, programėlės pavadinimas, kas jį leido, kada ir jo paskutinis naudojimas: kol ryšys aktyvus, tada 30 dienų po jo pabaigos (tą laiką jis lieka matomas valdymo skydelyje), tada ištrinami kartu su jo žetonais ir API raktu. Visa tai ištrinama kartu su paskyra.
- tafs: įrenginio push žetonas, kol atsijungiate nuo programėlės, pašalinate įrenginį arba Apple ar Google praneša, kad žetono nebėra. Įrenginio viešasis raktas, pavadinimas, platforma ir datos, kol jį naudojate; kai jį atjungiate, jis pažymimas kaip pašalintas ir saugomas, kol ištrinama paskyra, kad KIFF galėtų atmesti pavėluotą jo atsakymą ir parodyti, kuris įrenginys pasirašė ankstesnį. Prieigos rakto viešasis raktas, identifikatorius ir pavadinimas saugomi kaip jo įrenginio duomenys. Kompiuteris saugo savo sesiją iki 90 dienų arba kol atsijungiate. Tai, ką tafs saugo jūsų įmonei, saugoma kaip duomenų tvarkytojo: taf paleidimai ir pokalbiai kartu su pokalbio santrauka (ir Setup santrauka) 30 dienų, tai, ką grąžina jūsų įrankiai, tik iki paleidimo pabaigos (pokalbyje tai, ką jis tik perskaitė, saugoma užšifruota kitoms jo žinutėms, naudojama, kol pokalbis valandą būna neaktyvus, ir ne ilgiau kaip keturias valandas po perskaitymo, tada automatiškai ištrinama per kelias dienas), o jo atminties pastabos – kol taf pašalinamas; jo pateikiami failai – 30 dienų nuo juos sukūrusio paleidimo, tada automatiškai ištrinami per dieną arba anksčiau, jei kas nors jūsų paskyroje kurį nors ištrina; ryšio žetonas ar raktas planque arba valdomo ryšio prisijungimas Composio – kol jį atjungiate, o jo patvirtinimų įrašas – iki paskyros pabaigos; žr. [DPA 4 priedą](/legal/dpa#annex-4).
- Atsiskaitymo įrašai: 7 metai nuo finansinių metų pabaigos (Estijos buhalterinės apskaitos įstatymo 12 straipsnis).
- App Store ir Google Play pirkimai: tai, ką Apple ar Google mums pranešė apie kiekvieną pirkimą, lieka jūsų paskyroje, kol paskyra ištrinama. Jų pardavimų ir išmokų ataskaitos, kurias saugome kaip atsiskaitymo įrašus 7 metus, jūsų nenurodo.
- Susirašinėjimas su pagalbos tarnyba: 3 metai nuo paskutinio bendravimo, tada ištrinamas.

## 9. Jūsų teisės

Pagal BDAR turite šias teises, susijusias su jūsų asmens duomenimis:
- Prieiga (15 straipsnis): patvirtinimas, ar tvarkome jūsų duomenis, ir jų kopija.
- Ištaisymas (16 straipsnis): netikslių ar neišsamių duomenų ištaisymas.
- Ištrynimas (17 straipsnis): jūsų duomenų ištrynimas, atsižvelgiant į teisinio saugojimo išimtis.
- Apribojimas (18 straipsnis): tvarkymo sustabdymas, kol sprendžiamas ginčas.
- Perkeliamumas (20 straipsnis): jūsų pateiktų duomenų eksportas kompiuterio skaitomu formatu.
- Prieštaravimas (21 straipsnis): tvarkymui, grindžiamam teisėtais interesais; mes jį nutraukiame, nebent galime įrodyti viršesnius įtikinamus pagrindus.
- Sutikimo atšaukimas (7 straipsnio 3 dalis): bet kokiam sutikimu grindžiamam tvarkymui, pavyzdžiui, AI programėlėje tafs (skiltyje Account, kaip [aprašyta anksčiau](#ai-in-tafs)). Atšaukimas neturi įtakos ankstesniam teisėtam tvarkymui.

Norėdami pasinaudoti bet kuria iš šių teisių, rašykite adresu [privacy@kiff.dev](mailto:privacy@kiff.dev). Atsakome per vieną mėnesį pagal 12 straipsnio 3 dalį; sudėtingų prašymų atveju galime šį terminą pratęsti dar iki dviejų mėnesių ir jums apie tai pranešime.

### Jūsų pačių tafs pokalbiai

Programėlėje tafs skiltis Account → Export my chats atsisiunčia jūsų pačių pokalbius su kiekvienu taf, įskaitant Coach, jūsų pridėtų failų pavadinimus ir jūsų pačių pastabas, kaip skaitomą failą ir kaip JSON. Juos eksportuoti galite tik jūs: administratorius ar paskyros savininkas to padaryti negali, o KIFF paskyros eksportas jų neapima.

### tafs paskyros ištrynimas

tafs yra KIFF programėlė. Norėdami ištrinti savo tafs paskyrą ir jos duomenis:
- Atidarykite programėlę tafs ir prisijunkite.
- Eikite į Account ir bakstelėkite Delete account.
- Patvirtinkite. Paskyros ištrynimo atšaukti negalima.

Delete account rodomas tam, kas gali ištrinti įmonės paskyrą, paprastai jos savininkui. Jei jo nematote arba nebeturite programėlės, parašykite adresu [privacy@kiff.dev](mailto:privacy@kiff.dev) iš adreso, kuriuo prisijungiate, ir jūsų duomenis ištrinsime per vieną mėnesį.

Kas ištrinama: paskyros raktai nedelsiant nustoja veikti. Jos tafs, pokalbiai, pastabos, pridėti failai ir ryšiai ištrinami nedelsiant, o iš jų tam tikro laiko momento kopijų – per 35 dienas. Tai, ką KIFF saugo apie paskyrą, ištrinama per 30 dienų, o iš atsarginių kopijų – per 7 dienas po to. Paslaugų žurnalai, kuriuose nurodyta paskyra, dingsta per 30 dienų. Tenant audito duomenys ištrinami kartu su tenant. Kiekvienai duomenų rūšiai žr. [Saugojimas](#retention).

Norėdami ištrinti dalį savo duomenų ir išsaugoti paskyrą: programėlėje ištrinkite pokalbį, ir kartu su juo ištrinamos jo žinutės ir pridėti failai; pašalinkite taf, ir kartu su juo ištrinami jo pokalbiai, pastabos ir ryšiai. Dėl visko kito rašykite adresu [privacy@kiff.dev](mailto:privacy@kiff.dev) ir nurodykite, ką ištrinti. Tai padarome per vieną mėnesį.

Turite teisę pateikti skundą priežiūros institucijai. Mūsų vadovaujanti priežiūros institucija yra [Estijos duomenų apsaugos inspekcija (Andmekaitse Inspektsioon)](https://www.aki.ee/en). Taip pat galite pateikti skundą savo gyvenamosios valstybės narės priežiūros institucijai.

**Jūsų šalis.** Kai kurių šalių įstatymai reikalauja daugiau, nei nurodyta šioje politikoje. Ką jie papildomai nustato, anglų kalba ir tos šalies kalba, rasite puslapyje [Jūsų šalis](/privacy/countries).

## 10. Slapukai

Nustatome tris slapukus, visi jie būtinai reikalingi, pirmosios šalies, siunčiami tik per HTTPS ir neperskaitomi puslapio scenarijų. Pagal ES e. privatumo taisykles jiems nereikia sutikimo; juos išvardijame, kad žinotumėte, jog jie yra. Išsamiau – [slapukų politikoje](/cookies).
- kiff_session: palaiko jūsų prisijungimą. Pasirašytas sesijos slapukas su jūsų prisijungimo identifikatoriumi, el. paštu ir vardu, taip pat prisijungimo duomenimis, kuriuos valdymo skydelis naudoja jūsų vardu. Nustatomas .kiff.dev. Galioja 24 valandas arba kol atsijungiate.
- kiff_flash: perduoda vienkartinį pranešimą kitam puslapiui, pavyzdžiui, „jūsų raktas sukurtas“. Pasirašytas; naujo rakto jame niekada nėra. Nustatomas .kiff.dev. Ištrinamas, kai kitas puslapis jį perskaito, ir galioja ne ilgiau kaip 5 minutes.
- kiff_cb_attempts: skaičiuoja, kiek kartų prisijungimo puslapis buvo įkeltas iš naujo, kad būtų sustabdytas užsiciklinęs prisijungimas. Siunčiamas tik į tą puslapį. Galioja 5 minutes ir išvalomas, kai prisijungimas prasideda arba pavyksta.

Nenaudojame analitikos slapukų, reklamos slapukų ar jokių trečiųjų šalių sekiklių. Jei kada nors tokių pridėsime, prieš nustatydami slapuką parodysime sutikimo juostą pagal e. privatumo taisykles ir EDAV 2023 m. gaires dėl sekimo technologijų.

Jei prisijungiate per Clerk (mūsų autentifikavimo subtvarkytoją), Clerk taip pat nustato slapukus savo domene (clerk.kiff.dev). Šie slapukai būtini, kad Clerk jus atpažintų viso prisijungimo proceso metu, ir jiems taikoma Clerk privatumo politika, taip pat mūsų. Išsamiau – subtvarkytojų sąraše.

## 11. Saugumas

Mūsų techninės ir organizacinės saugumo priemonės aprašytos puslapyje [/security](/security). Jos apima tenant atskyrimą duomenų bazės lygmeniu, TLS 1.2+ perduodant, šifravimą saugant pagal valdomo teikėjo numatytuosius nustatymus ir prieigos kontrolę, grindžiamą mažiausių privilegijų principu.

Apie asmens duomenų saugumo pažeidimą paveiktiems klientams pranešame nepagrįstai nedelsdami ir per 72 valandas nuo jo patvirtinimo, laikydamiesi BDAR 33 straipsnio. Pranešime nurodomas pažeidimo pobūdis, susijusių duomenų subjektų ir įrašų kategorijos ir apytikslis skaičius, tikėtinos pasekmės ir taikytos ar siūlomos priemonės.

## 12. Vaikai

KIFF Cloud yra infrastruktūros produktas kūrėjams, neskirtas vaikams. Sąmoningai nerenkame asmens duomenų iš jaunesnių nei 16 metų vaikų. Jei manote, kad vaikas mums pateikė asmens duomenų, susisiekite adresu [privacy@kiff.dev](mailto:privacy@kiff.dev), ir mes juos ištrinsime.

## 13. Automatizuotas sprendimų priėmimas

KIFF Cloud vykdymo aplinka priima deterministinius politikos sprendimus dėl mūsų klientų siunčiamų duomenų. Tai nėra sprendimai dėl jūsų, sukeliantys teisines ar panašaus reikšmingumo pasekmes, kaip apibrėžta BDAR 22 straipsnyje; tai sprendimai dėl kliento veiksmų (pinigų grąžinimų, užsakymų, žinučių ir pan.), vykdomi pagal paties kliento politiką.

Klientai, naudojantys KIFF Cloud sprendimams, darantiems poveikį jų pačių galutiniams naudotojams, priimti, patys atsako už atitiktį 22 straipsniui. Mūsų valdymo kvitai sukurti taip, kad šiems klientams suteiktų audito seką, kurios jiems reikia žmogaus priežiūrai įrodyti, tačiau teisinė prievolė užtikrinti tą priežiūrą tenka klientui.

## 14. Šios politikos pakeitimai

Retkarčiais galime atnaujinti šią politiką. Apie esminius pakeitimus aktyviems klientams pranešama el. paštu ir gerai matomu pranešimu šiame puslapyje bent 30 dienų. Neesminiai pakeitimai skelbiami su nauja „paskutinio atnaujinimo“ data.

## 15. Susisiekite su mumis

Privatumo klausimai ir prašymai dėl teisių: [privacy@kiff.dev](mailto:privacy@kiff.dev).

Oficialūs teisiniai pranešimai Kiff Agents OÜ: [legal@kiff.dev](mailto:legal@kiff.dev) arba paštu adresu Tööstuse tn 75-71, Põhja-Tallinna linnaosa, Tallinn 10416, Estija.

Vadovaujanti priežiūros institucija skundams: [Estijos duomenų apsaugos inspekcija (Andmekaitse Inspektsioon)](https://www.aki.ee/en).

Dokumento versija: v0.1. Paskutinį kartą atnaujinta: 2026 m. lapkričio 17 d.
