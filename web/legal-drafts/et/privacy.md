# Privaatsuspoliitika

Viimati uuendatud: 17. november 2026. Jõustumise kuupäev: 24. mai 2026.

See tõlge on avaldatud koos ingliskeelse tekstiga. Kui need erinevad, kehtib ingliskeelne tekst.

See poliitika selgitab, kuidas KIFF Cloud töötleb isikuandmeid veebisaidil, töölaual, API-s ja rakenduses tafs iPhone'ile, iPadile, Androidile ja arvutitele. Küsimused: [privacy@kiff.dev](mailto:privacy@kiff.dev).

## 1. Kes me oleme

KIFF Cloudi haldab Kiff Agents OÜ, Eestis asutatud osaühing. Registriandmed:

- Registrikood: 17372313 (Eesti e-äriregister)
- Käibemaksukohustuslase number: EE102924327
- Registrijärgne aadress: Tööstuse tn 75-71, Põhja-Tallinna linnaosa, Tallinn 10416, Eesti
- Privaatsusküsimused: [privacy@kiff.dev](mailto:privacy@kiff.dev)
- Ametlikud õiguslikud teated: [legal@kiff.dev](mailto:legal@kiff.dev)

Kuna Kiff Agents OÜ on asutatud Euroopa Liidus, kohaldatakse meie andmetöötlusele otse ELi isikuandmete kaitse üldmäärust (GDPR). Me ei vaja artikli 27 kohast ELi esindajat.

## 2. Selle poliitika ulatus

See poliitika hõlmab isikuandmeid, mida töötleme, kui suhtlete järgmisega:

- [kiff.dev](https://kiff.dev), meie turundussait, see poliitikaleht ja avalik kviitungite kuvamise pind.
- [app.kiff.dev](https://app.kiff.dev), meie autentimist nõudev töölaud.
- [api.kiff.dev](https://api.kiff.dev), meie HTTP API.
- tafs: meie rakendus iPhone'ile, iPadile, Androidile ja arvutitele (Mac, Windows ja Linux) ning selle taga olev teenus (sealhulgas Setup, mis ühendab ettevõtte tööriistad, ja planque, mis neid ühendusi hoiab), milles ettevõte annab töö AI-agendile, mis töötab KIFF Cardide piires.

See poliitika ei hõlma avatud lähtekoodiga raamistikku kiff/kiff, mida võite ise käitada. Kui käitate raamistikku oma taristul, olete selle töödeldavate andmete vastutav töötleja ja Kiff Agents OÜ selles töötlemises ei osale.

## 3. Meie rollid GDPR-i alusel

Tegutseme olenevalt andmetest kahes rollis:

- Vastutav töötleja konto- ja sisselogimise metaandmete, arveldusandmete (kui arveldus on aktiivne), teie tellitud turundusteadaannete ja meie oma teenuse töötelemeetria puhul.
- Volitatud töötleja auditiandmete, toimingukirjete, otsusekviitungite ja muu sisu puhul, mis liigub käituskeskkonna kaudu meie klientide nimel, ning selle puhul, mida tafs nende jaoks hoiab (iga tafi käivitused, vestlused, aruanded, mälumärkmed ja failid, mida need väljastavad). Nende andmete vastutavad töötlejad on kliendid (rentnikud); nad annavad meile juhiseid oma konto seadete ja domeeni seadistuse kaudu. Seda suhet reguleerib andmetöötlusleping; avaldatud tingimused on [andmetöötluslepingu mallis](/legal/dpa).

See poliitika käsitleb peamiselt meie töötlemist vastutava töötlejana. Kui mainime volitatud töötleja poole andmeid, on täpsem dokument andmetöötlusleping.

## 4. Töödeldavate isikuandmete liigid

Allpool toodud liigid on kooskõlas tehnilise loendiga lehel [/security#data-we-process](/security#data-we-process). Kui need erinevad, on aluseks tehniline leht ja me ajakohastame seda jaotist vastavalt.

- Kontotunnused: teie autentimissubjekt (mille väljastab meie identiteedipakkuja) ja e-posti aadress, millega sisse logite. Praegu salvestatakse need muutmata kujul; räsitud salvestamine on kavas.
- Rentniku metaandmed: rentniku tunnus, lühinimi, kuvatav nimi, loomise ajatempel ja keel, mida iga inimese rakendus viimati kasutas, et KIFFi saadetavad e-kirjad tuleksid selles keeles.
- API-mandaadid: läbipaistmatud API-võtmete kirjed (salvestame räsid, mitte kunagi lihtteksti).
- Seansiküpsise andmed: pärast sisselogimist määratud HMAC-allkirjaga seansiküpsis. Vt allpool küpsiste jaotist.
- HTTP-päringu metaandmed: IP-aadress, kasutajaagent, päringu tee, vastuse olek, latentsus. Hoitakse meie pöördpuhverserveri juurdepääsulogides piiratud säilitusaja jooksul turvalisuse ja kuritarvituste ennetamise eesmärgil.
- Rentniku kontrollitav sisu: parameetrid, tegija ja olemi andmed, mida käituskeskkonnale saadate. Hoiame neid kliendi nimel volitatud töötlejana; klient vastutab selle eest, mis võrku jõuab. Domeenipõhine varjamise seadistus on kavas.
- Suhtlus: e-kirjad, mida saadate meie tugi-, müügi-, turva- või privaatsuspostkastidesse.
- Rakendus tafs: tafsi logite sisse oma KIFFi kontoga. Telefonis või tahvelarvutis hoiab rakendus teie seanssi seadme võtmehoidjas (Androidis selle krüpteeritud salvestuses). Arvutis hoiab see seda teie kasutajakonto jaoks suletuna: Windowsi arvutis Windowsi enda andmekaitsega, Linuxis teie võtmehoidjas, Macis selle võtmehoidjas (Keychain). Arvuti seanss kestab kuni 90 päeva. Rakendus loob allkirjavõtme, mis ei lahku kunagi seadmest: iPhone'i, iPadi või Maci Secure Enclave'is, Androidi seadme Keystore'is või Windowsi arvuti TPM-is. Linuxis ja Secure Enclave'ita Macis on võti tarkvaraline võti teie võtmehoidjas või Keychainis ning see arvuti saab näidata, mis teid ootab, kuid mitte seda kinnitada; kinnitate oma telefonis. Windowsi arvuti ilma TPM-ita ei saa tafsi sisse logida. KIFF hoiab võtme avalikku poolt, seadme nime ja platvormi ning seda, millal see lisati ja viimati kasutati, et saaks kontrollida teie vastuseid kinni peetud päringutele ja ühenduste kinnitusi. Kui kinnitate arvutis pääsuvõtmega, näiteks Windows Hello või turvavõtmega, jääb teie nägu, sõrmejälg või PIN-kood arvuti enda süsteemi; KIFF hoiab pääsuvõtme avalikku võtit, selle tunnust, loendurit ja nime, mitte kunagi midagi salajast, ning keeldub pääsuvõtmest, mis sünkroonitakse teistesse seadmetesse. Kui lülitate teavitused sisse telefonis, tahvelarvutis või iPadi rakendust kasutavas Macis, hoiame seadme tõukemärki koos teie KIFFi isiku tunnuse ja rakenduses kuvatud keelega, et selle teavitused tuleksid selles keeles. tafsi teenus hoiab ka konto keelt, selle järgi, kes viimati Coachiga vestles, kuni kolm kuud, et tafi aruanded tuleksid selles keeles. Telefoni teavitus läheb läbi Apple'i või Google'i ainult sellega, milline taf ja mida see soovib, ning ukse teavituses selle inimese nime või e-posti aadressiga, kes kirjutas: mitte kunagi summat ega päringu üksikasju. Rakendus lisab need seadmes endas KIFFist, nii et lukust lahti telefoni teavitus võib näidata summat, kaupmeest või inimest. Lukus Android-telefonis näitab see ainult „A taf needs you" ja lukus iPhone'is samuti, kui te pole selle eelvaateid seadnud alati kuvama. Teie kell: kell ei hoia ühtegi parooli ega võtit, suhtleb ainult teie seotud telefoniga ega kinnita kunagi midagi. See saadab Coachile või teie valitud tafile seda, mida ütlete, ainult tekstina, telefoni kaudu, pärast seda, kui olete AI lubanud. Kella enda kõnetuvastusteenus, Apple'i või Google'i oma, muudab teie sõnad selleks tekstiks. Kell saab tagasi lükata, peatada või Coachile või tafile saata ainult siis, kui sellel on ekraanilukk või pääsukood ja see on lukust lahti. See, mida tafile saadate, läheb selle tafi enda vestlusesse, nagu oleksite selle telefonis kirjutanud. Tafi vestlus, summad kaasa arvatud, kuvatakse kellas ainult siis, kui see on avatud, ja loetakse iga kord telefoni kaudu; kell ei hoia sellest midagi. Apple Watch: see näitab teie tafe ja nende lehti neid hoidmata. See hoiab ainult teie postkasti (Inbox) failis, mida see ei saa lukus olles lugeda: iga kinni peetud päring selle tafi nime ja küsimusena, mitte kunagi summana, ning Coachi vastused sellele, mille kellast saatsite, tekstina. Kui logite kellas välja, hoiab see ka teadet, et tegite seda, kuni logite kellas uuesti sisse. Peale selle ei hoia see midagi, kui kellal pole pääsukoodi. Postkast kustub, kui logite kellas välja või teie iPhone logib välja, või järgmisel korral, kui rakendus avaneb või sulgub ilma pääsukoodita. See ei tee salvestisi ja ainsad teavitused, mida see näitab, on teie iPhone'i omad. Wear OS: see näitab teie konto e-posti aadressi, tänast kalendrit ja teie tafide lehti neid hoidmata. See hoiab teie postkasti, kus iga rida on tafi nimi ja see, mida see ütleb, mitte kunagi summa, ning ühte hilisemaks hoitud sõnumit, tekstina. Väljalogimine, kellas või telefonis, kustutab selle kõik. Oma paani jaoks hoiab see ka teid ootavate asjade arvu, mis kuvatakse ainult siis, kui kell on lukust lahti, ja kustutatakse väljalogimisel või järgmisel korral, kui paan kuvatakse kella lukus olles või ekraanilukuta. Pärast kellas väljalogimist hoiab see teadet, et tegite seda, kuni logite uuesti sisse. Postkast kustub ka järgmisel korral, kui rakendus avaneb või sulgub kella lukus olles või ekraanilukuta. Hilisemaks hoitud sõnum jääb alles, kuni selle saadate või kustutate, kuna palusite selle saata. Kell ei tee salvestisi. Tafi tulemus võib jääda kella teavitustesse, kuni selle eemaldate või kellas välja logite. Windowsi arvutis, Linuxi arvutis või macOS 12 rakendusega Macis loob teavitused rakendus ise töö ajal, ilma tõukemärgita ja ühegi teise ettevõtte kaudu. Windowsis ja Linuxis näitavad need, milline taf, mida see teha tahab, summat, põhjust ja tähtaega; macOS 12 rakenduses näitavad need KIFFi sõnastust päringu kohta, kui need sisse lülitate, ning lukus ekraan näitab seda, mida Maci eelvaate seade lubab. Windowsis on need välja lülitatud, kuni need sisse lülitate, ja näitavad ekraani lukus olles ainult „A taf needs you". Enne ekraani lukustumist kuvatud teavitus säilitab oma üksikasjad Windowsi teavituste loendis. Linuxis on need sees, kuni need välja lülitate, ja see, mida lukus ekraan näitab, sõltub teie töölauast. Tööriista ühendamisel logite sisse tööriista enda pakkuja juures. Rakenduse puhul, kuhu planque ise sisse logib, näiteks Stripe, või teie antud võtme puhul hoiab planque seda märki või võtit teie ettevõtte jaoks krüpteeritult; hallatud ühenduse puhul hoiab sisselogimist Composio, ühendusteenus, millega planque töötab, ja planque hoiab ainult ühenduse kirjet ja teie kinnitatud tööriistu (kõike volitatud töötlejana, vt allpool). KIFF kasutab hallatud ühendusi ainult rakenduste jaoks, mis on nimetatud lehel [/subprocessors](/subprocessors#composio). Rakenduses pole analüütika- ega reklaamijälgijaid.
- App Store'i ostud: kui ostate tafsis Solo või krediidipaketi, müüb selle teile Apple: Apple on müüja, võtab makse vastu ja hoiab teie makseandmeid. Apple teatab meile, mida osteti, millise KIFFi konto jaoks (juhuslik tunnus, mille rakendus ostule lisab), millal, Apple'i tehingu tunnuse ning Solo puhul, millal periood lõpeb või uueneb. Kasutame seda krediitide või paketi lisamiseks teie kontole. Apple ei anna meile teie Apple ID-d, nime, e-posti aadressi ega kaarti.
- Google Play ostud: Androidis käivad samad ostud läbi Google Play arvelduse: Google võtab makse vastu ja hoiab teie makseandmeid. Google teatab meile, mida osteti, millise KIFFi konto jaoks (sama juhuslik tunnus), millal, ostu märgi ja tellimuse tunnuse ning Solo puhul, millal periood lõpeb või uueneb, ja me kasutame seda samamoodi. Google ei anna meile teie Google'i kontot, nime, e-posti aadressi ega kaarti.

## 5. Töötlemise õiguslikud alused

GDPR-i artikli 6 alusel tugineme järgmistele õiguslikele alustele:

- Lepingu täitmine (artikli 6 lõike 1 punkt b) konto loomiseks, autentimiseks, käituskeskkonna käitamiseks teie nimel, arvelduseks (kui see on aktiivne) ja teenuse põhipindade jaoks.
- Õigustatud huvid (artikli 6 lõike 1 punkt f) turvalisuse, pettuste ennetamise, kuritarvituste avastamise, töötelemeetria ja teenuse kaitsmise jaoks. Kaalume oma huve teie õiguste suhtes ja ütleme teile taotluse korral, milline oli selle kaalumise tulemus.
- Nõusolek (artikli 6 lõike 1 punkt a) AI jaoks rakenduses tafs: see, mida saadetakse Coachi ja teie tafe käitavale AI-mudelile, nagu kirjeldatud [allpool](#ai-in-tafs). Midagi ei saadeta enne, kui te selle lubate, ja nõusoleku saate tagasi võtta menüüs Account. Nõusolekut küsiksime ka enne mis tahes analüütikaküpsiseid, valikulisi turundusteadaandeid või uut töötlemist, mis laiendab oluliselt teie andmete kasutamist. Me ei tee analüütikat, reklaami ega mittevajalikku jälgimist.
- Juriidiline kohustus (artikli 6 lõike 1 punkt c) maksuarvestuse (raamatupidamise seaduse § 12, 7 aastat), pädevate asutuste seaduslikele päringutele vastamise ja rahapesu tõkestamise reeglitega nõutud säilitamise jaoks, kui see on kohaldatav.

### AI rakenduses tafs

Rakendus tafs küsib teilt enne, kui midagi AI-le saadetakse, nende sõnadega:

- tafs kasutab Coachi ja teie tafide jaoks Anthropicu AI-d Claude.
- Coach ja teie tafid on AI-agendid: see, mida nad kirjutavad, on AI loodud ja võib olla vale. Sõnum, mille taf saadab väljapoole teie ettevõtet, ütleb, et selle kirjutas AI-agent.
- Mida sinna saadetakse: see, mida kirjutate Coachile ja oma tafidele, teie lisatud failid, see, mida teie ühendatud rakendused tafile töö ajal tagastavad, ja see, mis on teie ekraanil, kui Coach teid rakenduses aitab.
- Mida ei saadeta kunagi: paroole ja võtmeid, mida teie ühendatud rakendused kasutavad, ega teie sõrmejälge.
- Anthropic kasutab seda vastamiseks ja hoiab seda kuni 30 päeva.
- Midagi ei saadeta enne, kui te selle lubate. Nõusoleku saate tagasi võtta menüüs Account.

Taf, mis töötab ilma, et keegi parasjagu kohal oleks (oma ajakava järgi või kui midagi saabub), tugineb selle inimese nõusolekule, kes selle tegi või üle võttis, või muidu esimese konto tafide haldaja nõusolekule, kes selle lubab.

Kui nõusoleku tagasi võtate, lõpetavad Coach ja teie tafid teile vastamise. Taf, mille käivitused põhinevad teie nõusolekul, peatub: selle ajakavad peatuvad ja saabuv ootab. See jätkab, kui nõusolek antakse uuesti või kui keegi konto tafide haldajatest selle oma nõusolekuga üle võtab. Tagasivõtmine ei muuda varem saadetut.

Anthropic on nimetatud lehel [/subprocessors](/subprocessors), koos sellega, kus see andmeid töötleb ja millal võib neid kauem hoida.

## 6. Volitatud alamtöötlejad

Me ei müü teie isikuandmeid ega jaga neid reklaami eesmärgil.

Teenuse osutamiseks kasutame väikest hulka tarnijaid. Ametlik ja ajakohane nimekiri on lehel [/subprocessors](/subprocessors); seda lehte ajakohastatakse 30 päeva jooksul pärast iga muudatust. Nõuame igalt alamtöötlejalt kirjalike lepingute alusel meie omaga samaväärset kaitset, sealhulgas ELi lepingu tüüptingimusi, kui isikuandmed lahkuvad EList.

Alamtöötlejate olulistest lisandumistest või asendustest, mis mõjutavad isikuandmeid, teatatakse aktiivsetele klientidele ette, koos õigusega esitada vastuväide.

## 7. Rahvusvahelised edastused

Mõned meie alamtöötlejad asuvad väljaspool Euroopa Majanduspiirkonda, peamiselt Ameerika Ühendriikides. Iga sellise edastuse puhul tugineme vähemalt ühele järgmistest kaitsemeetmetest:

- ELi-USA andmekaitseraamistik: USA alamtöötlejate puhul, kes on raamistiku alusel sertifitseeritud. Piisavuse otsus kehtib alates 2023. aasta keskpaigast; jälgime selle staatuse muutusi.
- Lepingu tüüptingimused: moodul 2 (vastutavalt töötlejalt volitatud töötlejale) meie otseste alamtöötlejate puhul ja moodul 3 (volitatud töötlejalt alamtöötlejale), kui kohaldub töötlejate ahel. Kasutame komisjoni poolt 2021. aastal heaks kiidetud tüüptingimusi.
- Täiendavad meetmed: kui riskihinnang näitab, et lisakaitse on põhjendatud (krüpteerimine edastamisel ja salvestamisel, juurdepääsu logimine, andmete minimeerimine).

Konkreetse alamtöötleja suhtes kohaldatava edastusinstrumendi saate küsida, kirjutades aadressil [privacy@kiff.dev](mailto:privacy@kiff.dev).

**Teie riik.** Mõne riigi seadused nõuavad rohkem, kui selles poliitikas on öeldud. Mida need lisavad, inglise keeles ja riigi keeles, on lehel [Teie riik](/privacy/countries).

## 8. Säilitamine

Säilitame isikuandmeid kõige lühema aja jooksul, mis on kooskõlas nende töötlemise eesmärkide ja mis tahes kohaldatava juriidilise kohustusega.

- Konto- ja sisselogimise metaandmed: kuni konto kustutamiseni. Rakenduses tafs kustutab omanik selle menüüs Account → Delete account. Veebis loodud konto kustutatakse e-kirjaga aadressile [privacy@kiff.dev](mailto:privacy@kiff.dev), GDPR-i artikli 12 lõike 3 ühekuulise tähtaja jooksul. Mõlemal juhul lakkavad konto võtmed kohe toimimast. Rakenduses tafs kustutatuna ei saa seda tagasi võtta: selle tafsi vestlused, märkmed ja ühendused kustutatakse kohe ning see, mida KIFF hoiab, kustutatakse 30 päeva jooksul. Veebis kustutatuna hoiame selle andmeid 30-päevase ooteaja jooksul juhuks, kui omanik soovib need tagasi, ning seejärel kustutame need.
- API-võtmete kirjed: kuni konto kustutamiseni, sealhulgas pärast võtme tühistamist. Agendi sisselogimisühendusele kuuluv võti kustutatakse koos selle ühendusega 30 päeva pärast ühenduse lõppu.
- Seansiküpsised: kuni aegumiseni (vaikimisi 24 tundi) või kuni välja logite.
- Pöördpuhverserveri juurdepääsulogid: kuni 90 päeva, seejärel koondatakse või kustutatakse.
- Teenuse logid: see, mida KIFFi teenused ja planque töö ajal salvestavad: vead ja päringud, mis võivad nimetada kontot, inimest või agenti. 30 päeva, seejärel kustutatakse.
- Varukoopiad: KIFFi andmebaasist tehakse iga päev krüpteeritud varukoopia ja iga varukoopiat hoitakse 7 päeva, nii et kustutatud kirje kaob neist 7 päeva jooksul. tafsi ja planque'i salvestused hoiavad krüpteeritud ajahetke koopiaid 35 päeva ja neid kasutatakse ainult salvestuse taastamiseks pärast tõrget, nii et kustutatud kirje kaob neist 35 päeva jooksul.
- Rentniku auditiandmed: hoitakse volitatud töötlejana vastavalt kliendi rentniku seadistusele. Vaikimisi: säilitatakse rentniku eluea jooksul; kustutatakse rentniku kustutamisel. Vt [/security#deletion](/security#deletion).
- Päringud KIFFi lüüsi kaudu: päringu argumendid kuni selle saatmise, tagasilükkamise või ebaõnnestumiseni, seejärel kustutatakse; tööriista tulemus 24 tundi, seejärel kustutatakse. Otsuse kirjet ja argumentide räsi hoitakse rentniku auditiandmetena. Vt [/security#data-kept](/security#data-kept).
- Kasutus: iga lüüsi kaudu tehtud päringu kohta pärast selle lõppu: milline agent selle tegi, tööriist ning kas see arvestati toiminguna või oli tasuta (ja miks). Iga tafi või Coachi käivituse kohta pärast selle lõppu: taf, aeg, selle AI kasutatud krediidid ning kas need arvestati või miks mitte. Kuni konto kustutamiseni; see, mis teatatakse meie makseteenuse pakkujale arveldamiseks, hoitakse koos arveldusdokumentidega.
- Agentide sisselogimisühendused: milline agent, rakenduse nimi, kes selle lubas, millal ja selle viimane kasutus: ühenduse kehtivuse ajal, seejärel 30 päeva pärast selle lõppu (see on selle aja jooksul töölaual loetletud), seejärel kustutatakse koos selle märkide ja API-võtmega. Kõik see kustutatakse koos kontoga.
- tafs: seadme tõukemärk, kuni logite rakendusest välja, eemaldate seadme või Apple või Google teatab, et märki enam pole. Seadme avalik võti, nimi, platvorm ja kuupäevad, kuni seda kasutate; kui selle välja logite, märgitakse see eemaldatuks ja seda hoitakse kuni konto kustutamiseni, et KIFF saaks keelduda selle hilisest vastusest ja näidata, milline seade allkirjastas varasema. Pääsuvõtme avalikku võtit, tunnust ja nime hoitakse nagu selle seadme omi. Arvuti hoiab oma seanssi kuni 90 päeva või kuni välja logite. Seda, mida tafs teie ettevõtte jaoks hoiab, hoitakse volitatud töötlejana: tafi käivitusi ja vestlusi koos vestluse kokkuvõttega (ja Setupi omaga) 30 päeva, seda, mida teie tööriistad tagastavad, ainult käivituse lõpuni (vestluses hoitakse ainult loetut krüpteeritult järgmiste sõnumite jaoks, kasutatakse kuni vestlus on olnud tund aega jõude ja kõige rohkem neli tundi pärast lugemist, seejärel kustutatakse automaatselt mõne päeva jooksul), ning selle mälumärkmeid kuni tafi eemaldamiseni; faile, mida see väljastab, 30 päeva alates käivitusest, mis need lõi, seejärel kustutatakse automaatselt päeva jooksul või varem, kui keegi teie kontol faili kustutab; ühenduse märki või võtit planque'is või hallatud ühenduse sisselogimist Composios kuni ühenduse katkestamiseni ning selle kinnituste kirjet kuni konto lõpuni; vt [andmetöötluslepingu lisa 4](/legal/dpa#annex-4).
- Arveldusdokumendid: 7 aastat majandusaasta lõpust (raamatupidamise seaduse § 12).
- App Store'i ja Google Play ostud: see, mida Apple või Google meile iga ostu kohta teatas, jääb teie kontole kuni konto kustutamiseni. Nende müügi- ja väljamakseraportid, mida hoiame arveldusdokumentidena 7 aastat, teid ei nimeta.
- Tugikirjavahetus: 3 aastat viimasest suhtlusest, seejärel kustutatakse.

## 9. Teie õigused

GDPR-i alusel on teil oma isikuandmete suhtes järgmised õigused:

- Juurdepääs (artikkel 15): kinnitus selle kohta, kas töötleme teie andmeid, ja nende koopia.
- Parandamine (artikkel 16): ebaõigete või puudulike andmete parandamine.
- Kustutamine (artikkel 17): teie andmete kustutamine, arvestades seadusjärgse säilitamise erandeid.
- Piiramine (artikkel 18): töötlemise peatamine vaidluse lahendamise ajaks.
- Ülekantavus (artikkel 20): teie esitatud andmete masinloetav eksport.
- Vastuväide (artikkel 21): õigustatud huvidel põhinevale töötlemisele; lõpetame, kui me ei suuda näidata kaalukamaid põhjuseid.
- Nõusoleku tagasivõtmine (artikli 7 lõige 3): mis tahes nõusolekul põhineva töötlemise puhul, näiteks AI rakenduses tafs (menüüs Account, nagu [eespool](#ai-in-tafs)). Tagasivõtmine ei mõjuta varasemat seaduslikku töötlemist.

Mis tahes nende õiguste kasutamiseks kirjutage aadressil [privacy@kiff.dev](mailto:privacy@kiff.dev). Vastame artikli 12 lõike 3 kohaselt ühe kuu jooksul; keeruliste taotluste puhul võime tähtaega pikendada kuni kahe kuu võrra ja teatame teile sellest.

### Teie enda tafsi vestlused

Rakenduses tafs laadib Account → Export my chats alla teie enda vestlused iga tafiga, sealhulgas Coachiga, teie lisatud failide nimed ja teie enda märkmed, loetava failina ja JSON-ina. Neid saate eksportida ainult teie: administraator ega konto omanik ei saa seda teha ning KIFFi konto eksport neid ei sisalda.

### tafsi konto kustutamine

tafs on KIFFi rakendus. tafsi konto ja selle andmete kustutamiseks:

- Avage rakendus tafs ja logige sisse.
- Minge menüüsse Account ja puudutage Delete account.
- Kinnitage. Konto kustutamist ei saa tagasi võtta.

Delete account kuvatakse sellele, kes tohib ettevõtte konto kustutada, tavaliselt selle omanikule. Kui te seda ei näe või teil pole enam rakendust, kirjutage aadressil [privacy@kiff.dev](mailto:privacy@kiff.dev) sellelt aadressilt, millega sisse logite, ja me kustutame teie andmed ühe kuu jooksul.

Mida kustutatakse: konto võtmed lakkavad kohe toimimast. Selle tafid, vestlused, märkmed, lisatud failid ja ühendused kustutatakse kohe ning nende ajahetke koopiatest 35 päeva jooksul. See, mida KIFF konto jaoks hoiab, kustutatakse 30 päeva jooksul ja varukoopiatest 7 päeva jooksul pärast seda. Kontot nimetavad teenuse logid kaovad 30 päeva jooksul. Rentniku auditiandmed kustutatakse koos rentnikuga. Iga andmeliigi kohta vt [Säilitamine](#retention).

Osa andmete kustutamiseks konto alles jättes: kustutage rakenduses vestlus ja selle sõnumid ning lisatud failid kaovad koos sellega; eemaldage taf ja selle vestlused, märkmed ja ühendused kaovad koos sellega. Muu puhul kirjutage aadressil [privacy@kiff.dev](mailto:privacy@kiff.dev) ja öelge, mida kustutada. Teeme seda ühe kuu jooksul.

Teil on õigus esitada kaebus järelevalveasutusele. Meie juhtiv järelevalveasutus on [Andmekaitse Inspektsioon](https://www.aki.ee). Võite esitada kaebuse ka oma elukohajärgse liikmesriigi järelevalveasutusele.

**Teie riik.** Mõne riigi seadused nõuavad rohkem, kui selles poliitikas on öeldud. Mida need lisavad, inglise keeles ja riigi keeles, on lehel [Teie riik](/privacy/countries).

## 10. Küpsised

Määrame kolm küpsist, mis kõik on rangelt vajalikud, esimese osapoole küpsised, saadetakse ainult HTTPS-i kaudu ja lehe skriptid ei saa neid lugeda. ELi e-privaatsuse reeglite kohaselt ei nõua need nõusolekut; loetleme need, et teaksite nende olemasolust. Üksikasjad on [küpsiste poliitikas](/cookies).

- kiff_session: hoiab teid sisselogituna. Allkirjastatud seansiküpsis, mis sisaldab teie sisselogimise tunnust, e-posti aadressi ja nime ning mandaati, mida töölaud teie jaoks kasutab. Määratud domeenile .kiff.dev. Kehtib 24 tundi või kuni välja logite.
- kiff_flash: kannab järgmisele lehele ühekordset sõnumit, näiteks „teie võti on loodud". Allkirjastatud; uut võtit selles kunagi ei ole. Määratud domeenile .kiff.dev. Kustutatakse, kui järgmine leht selle loeb, ja kõige hiljem 5 minuti pärast.
- kiff_cb_attempts: loeb, kui sageli sisselogimisleht on uuesti laaditud, et ringlev sisselogimine peatada. Saadetakse ainult sellele lehele. Kehtib 5 minutit ja kustutatakse, kui sisselogimine algab või õnnestub.

Me ei kasuta analüütikaküpsiseid, reklaamiküpsiseid ega kolmandate osapoolte jälgijaid. Kui me kunagi neid lisame, kuvame enne küpsise määramist nõusolekuriba, kooskõlas e-privaatsuse reeglitega ja Euroopa Andmekaitsenõukogu 2023. aasta juhistega jälgimistehnoloogiate kohta.

Kui logite sisse Clerki kaudu (meie autentimise alamtöötleja), määrab Clerk küpsiseid ka oma esiotsa domeenile (clerk.kiff.dev). Need küpsised on vajalikud, et Clerk tunneks teid oma sisselogimise voos ära, ning neid reguleerivad nii Clerki kui ka meie privaatsuspoliitika. Üksikasjad on alamtöötlejate nimekirjas.

## 11. Turvalisus

Meie tehnilised ja korralduslikud turvameetmed on dokumenteeritud lehel [/security](/security). Need hõlmavad rentnike eraldamist andmebaasi tasandil, TLS 1.2+ edastamisel, krüpteerimist salvestamisel hallatud pakkuja vaikeseadetega ning minimaalsete õiguste põhimõttel põhinevat juurdepääsukontrolli.

Teatame mõjutatud klientidele isikuandmetega seotud rikkumisest põhjendamatu viivituseta ja 72 tunni jooksul pärast selle kinnitamist, kooskõlas GDPR-i artikliga 33. Teade hõlmab rikkumise olemust, asjaomaste andmesubjektide ja kirjete liike ja ligikaudset arvu, tõenäolisi tagajärgi ning võetud või kavandatud meetmeid.

## 12. Lapsed

KIFF Cloud on arendajatele mõeldud taristutoode, mis ei ole suunatud lastele. Me ei kogu teadlikult alla 16-aastaste laste isikuandmeid. Kui arvate, et laps on meile isikuandmeid andnud, võtke ühendust aadressil [privacy@kiff.dev](mailto:privacy@kiff.dev) ja me kustutame need.

## 13. Automatiseeritud otsuste tegemine

KIFF Cloudi käituskeskkond teeb meie klientide saadetud andmete põhjal deterministlikke põhimõtteotsuseid. Need ei ole teie kohta tehtud otsused, millel oleks GDPR-i artikli 22 tähenduses õiguslik või samaväärselt oluline mõju; need on otsused kliendi toimingute kohta (tagasimaksed, tellimused, sõnumid jne), mida täidetakse kliendi enda põhimõtete alusel.

Kliendid, kes kasutavad KIFF Cloudi oma lõppkasutajaid mõjutavate otsuste tegemiseks, vastutavad ise artikli 22 järgimise eest. Meie reguleerimiskviitungid on loodud andma neile klientidele auditijälje, mida nad vajavad inimjärelevalve tõendamiseks, kuid juriidiline kohustus seda järelevalvet tagada lasub kliendil.

## 14. Selle poliitika muudatused

Võime seda poliitikat aeg-ajalt ajakohastada. Olulistest muudatustest teatatakse aktiivsetele klientidele e-posti teel ja vähemalt 30 päeva jooksul selgelt nähtava teatega sellel lehel. Ebaolulised muudatused avaldatakse uue „viimati uuendatud" kuupäevaga.

## 15. Kontakt

Privaatsusküsimused ja õiguste kasutamise taotlused: [privacy@kiff.dev](mailto:privacy@kiff.dev).

Ametlikud õiguslikud teated Kiff Agents OÜ-le: [legal@kiff.dev](mailto:legal@kiff.dev) või posti teel aadressil Tööstuse tn 75-71, Põhja-Tallinna linnaosa, Tallinn 10416, Eesti.

Juhtiv järelevalveasutus kaebuste esitamiseks: [Andmekaitse Inspektsioon](https://www.aki.ee).

Dokumendi versioon: v0.1. Viimati uuendatud: 17. november 2026.
