# Kasutustingimused

Viimati uuendatud: 9. november 2026. Jõustumise kuupäev: 24. mai 2026.

See tõlge on avaldatud koos ingliskeelse tekstiga. Kui need erinevad, kehtib ingliskeelne tekst.

Mis muutus: rakendus tafs Androidis ja Google Play kaudu ostetud Solo (punktid 1 ja 8). Varem: tasulised paketid (punktid 3A ja 8), [teenustaseme leping](/legal/sla) (punkt 14) ja keelatud kasutuste loetelu, mis on nüüd [lubatud kasutuse põhimõtetes](/acceptable-use) (punkt 5).

Need tingimused reguleerivad KIFF Cloudi kasutamist, sealhulgas tafsi. Lepingulised küsimused: [legal@kiff.dev](mailto:legal@kiff.dev).

## 1. Mõisted

- „Kiff Agents OÜ", „meie", „meid", „meie oma": Teenust pakkuv Eesti osaühing. Registrikood 17372313.
- „Klient", „teie", „teie oma": füüsiline või juriidiline isik, kes nõustub nende tingimustega.
- „Kasutaja": iga isik, keda Klient volitab kasutama Teenust Kliendi konto kaudu.
- „Teenus": toode KIFF Cloud, sealhulgas api.kiff.dev, app.kiff.dev, kiff.dev, tafs ning seal avaldatud dokumentatsioon, töölauad ja API-d.
- „tafs": rakendus tafs iPhone'ile, iPadile, Androidile ja arvutitele (Mac, Windows ja Linux) ning selle taga olev teenus, sealhulgas Setup ja planque (punkt 3A), milles Klient annab töö AI-agendile, kes töötab KIFF Cardide piires. „Taf" on üks selline agent.
- „Rentnik" (Tenant): Teenuse sees loogiliselt eraldatud tööruum, mis kuulub ühele Kliendile.
- „Auditiandmed": sündmused, otsused, toimingud, kinnitused ja kviitungid, mida Teenus Kliendi nimel käituskeskkonna kaudu säilitab.
- „tafsi andmed": see, mida tafs Kliendi nimel hoiab: iga tafi käivitused ja vestlused (sealhulgas see, mida Kliendi ühendatud tööriistad sellele tagastavad), selle aruanded, mälumärkmed ning telefonid, mis on registreeritud selle teavituste jaoks.
- „Kviitung": allkirjastatud, versioonitud otsuse kirje, mille Teenus väljastab iga reguleeritud toimingu kohta.
- „Dokumentatsioon": Teenuse tehniline dokumentatsioon ja turvameetmed nendest tingimustest viidatud dateeritud versioonis ([kiff.dev/docs/archive/2026-10-10](https://kiff.dev/docs/archive/2026-10-10)). See ei hõlma strateegilist valget raamatut, turundusmaterjale ega eraldi litsentsitud avatud lähtekoodiga raamistiku dokumentatsiooni.
- „Andmetöötlusleping": andmetöötlusleping aadressil [/legal/dpa](/legal/dpa), mis reguleerib meie suhet volitatud töötlejana.

## 2. Leping

Need tingimused reguleerivad teie Teenuse kasutamist. Kui loote konto, genereerite API-võtme või kasutate Teenust muul viisil, nõustute nende tingimustega. Kui nõustute ettevõtte või muu juriidilise isiku nimel, kinnitate, et teil on õigus seda isikut siduda, ning „teie" ja „teie oma" tähendavad seda isikut.

Võime avaldada konkreetsete funktsioonide jaoks lisatingimusi (beetafunktsioonid, kolmandate osapoolte integratsioonid). Kui need on vastuolus nende tingimustega, kehtivad selle funktsiooni puhul lisatingimused.

## 3. Teenus

KIFF Cloud on majutatud teenus delegeeritud toimingute reguleerimiseks. See hindab päringuid Kliendi seadistatud õiguste ja Cardide alusel, võib nõuda inimese kinnitust ning salvestab reguleerimisotsused. Olenevalt seadistatud integratsioonist edastab KIFF lubatud päringu või annab loa, mida kaitstud süsteem kontrollib. Aluseks oleva toimingu teeb ühendatud süsteem.

Kohaldatav tehniline käitumine, tõendid, integratsiooninõuded ja täitmise piirangud on määratud nendest tingimustest viidatud dateeritud Dokumentatsioonis. Strateegiline [valge raamat](/whitepaper) selgitab KIFFi põhimõtteid ja mudelit; see ei ole nende tingimuste alusel osa tehnilisest Dokumentatsioonist.

KIFF Cloudi reguleerimisteenus ei ole agendiraamistik, töövoomootor, mudelivärav ega hallatud agendiplatvorm. See sobib kokku kõigega, mida te juba kasutate. Täpne loetelu on avalehe jaotises [„Mis KIFF ei ole"](/#not). tafs on Teenuse ainus osa, mis käitab agente teie eest, teie väljastatud Cardide piires; seda kirjeldab punkt 3A.

Avatud lähtekoodiga raamistikku [kiff/kiff](https://github.com/kiff/kiff) reguleerib selle enda MIT-litsents ja see jääb nendest tingimustest välja. Kui käitate raamistikku oma taristul, ei teki lepingut Kiff Agents OÜ-ga.

## 3A. tafs

tafsis annate töö (näiteks tagasimaksed, arvete tasumine või kaardivaidlused) tafile: AI-agendile, mis töötab ajakava järgi teie ühendatud tööriistadega, nende KIFF Cardide piires, mille te talle väljastate. Iga taf töötab meie agendi käituskeskkonnas ja selle kasutatava AI-mudeli pakub Anthropic. Selle kinni peetud päringutele vastate rakenduses tafs.

Tööriistade ühendamine. Setup, tafsi AI-agent, mis töötab nagu taf, aitab teil tööriista ühendada ja tafi töö jaoks seadistada. See ei ühenda ise midagi. Logite sisse tööriista enda pakkuja juures (näiteks Stripe) ning kinnitate oma seadmes iga ühenduse, tööriistad, mida taf tohib kasutada, ja selle Cardid. Sellest sisselogimisest saadud märki hoiab planque, meie ühendusteenus. See on krüpteeritud, seda kasutatakse ainult KIFFi otsustatud päringu tegemiseks ja seda ei anta kunagi tafile. Tööriista ühenduse katkestamine tühistab märgi pakkuja juures ja kustutab selle.

Teie Cardid on teie juhised. Taf jõuab teie tööriistadeni ainult KIFFi kaudu, mis kontrollib iga päringut tafi Cardide alusel enne selle saatmist. Teie väljastatud Cardi piires tehtud päring tehakse teie juhisel. Cardist väljapoole jääv päring peetakse kinni või lükatakse tagasi, nagu Card ütleb. Kinni peetud päring tehakse alles siis, kui selle kinnitab keegi, kellel teie konto lubab kinni peetud päringutele vastata. Rakenduses tafs kannab vastus nende seadme allkirja, mida KIFF kontrollib, ja kviitung säilitab selle tõendi. Teie konto saab vastata ka KIFFis endas (töölaual, selle käsureatööriistas või seal volitatud kinnitaja kaudu); kviitung salvestab siis, kes vastas ja kuidas.

Mis jääb teile. Teie valite iga tafi töö ja Cardid, vastate selle kinni peetud päringutele ning tagate, et teil on õigus kasutada ühendatud tööriistu ja kontosid. Tafi aruanded, sõnumid ja mälumärkmed kirjutab AI-mudel ja need võivad olla valed; kontrollige olulist enne, kui sellele tuginete.

Tafi peatamine. Võite peatada ühe tafi või kõik, panna tafi pausile, tühistada mis tahes selle Cardi või selle eemaldada. Hetkest, mil tafi peatate või Cardi tühistate, ei luba KIFF selle alusel ühtegi uut päringut. Rakendus tafs näitab tafi peatatuna alles siis, kui nii KIFF kui ka planque on peatamise kinnitanud; sellest hetkest ei saada planque ühtegi selle päringut, sealhulgas ka seda, mille KIFF lubas vahetult enne. Tööriista pakkujale juba saadetud päring võib siiski lõpule jõuda: me peatame uued päringud, kuid ei saa tagasi võtta seda, mida pakkuja on teinud. Tööriist, mis kontrollib KIFFi lube ise, ilma planque'ita, võib vahetult enne väljastatud KIFFi loa siiski vastu võtta, kõige rohkem 90 sekundi jooksul. Peatatud tafi jätkamine nõuab teie kinnitust teie seadmes ning midagi, mis oli enne peatamist kinni peetud või järjekorras, ei käivitu pärast seda.

Tafi eemaldamine peatab selle ajakava ning kustutab selle käivitused, vestlused, mälu ja kasutatud võtme. Juba enne seda hoitakse seda, mida teie tööriistad tafile tagastavad, ainult selle käivituse lõpuni (seda, mida vestlus ainult luges, krüpteeritult järgmiste sõnumite jaoks: kasutatakse kõige rohkem neli tundi, seejärel kustutatakse) ning selle käivitusi ja vestlusi 30 päeva. KIFF hoiab kviitungeid selle kohta, mida see tegi.

ELi tehisintellekti määrus. tafsi puhul on Kiff Agents OÜ AI-süsteemi pakkuja ja teie selle juurutaja. Tagame, et kõik, mida taf kirjutab kellelegi väljaspool teie ettevõtet, ütleb, et selle kirjutas AI-agent. Kasutate tafsi ainult tööde jaoks, mida see pakub, ja mitte ühelgi viisil, mille tehisintellekti määrus keelab või liigitab suure riskiga kasutuseks, näiteks otsused inimese töösuhte, krediidi või oluliste teenuste kättesaadavuse kohta. Säilitate inimkinnitused, mida teie Cardid nõuavad, ja tagate, et inimesed, kes tafsi teie eest kasutavad, mõistavad, mida see teeb ja millised on selle piirid.

Kulud. tafs on osa teie KIFFi paketist (punkt 8): iga tafi käivitus kasutab krediite selle AI eest, mida see kasutas. Coach ja tafide seadistamine sisalduvad Coachi mõistliku kasutuse piires.

## 4. Kontod ja juurdepääs

Teenuse kasutamiseks peate looma konto. Nõustute andma õiget registreerimisteavet, hoidma oma sisselogimisandmed salajas ja vastutama kogu oma konto all toimuva tegevuse eest.

Peate olema vähemalt 16-aastane või oma jurisdiktsiooni digitaalse nõusoleku vanuses, olenevalt sellest, kumb on kõrgem. Teenus ei ole suunatud lastele.

Vastutate iga Kasutaja tegevuse eest, keda oma kontol volitate. Võime konto peatada või juurdepääsu tühistada mõistliku etteteatamisega, kui avastame nende tingimuste usaldusväärse rikkumise. Lubatud kasutuse rikkumiste korral (vt punkt 5) võib peatamine olla kohene.

## 5. Lubatud kasutus

Teie, teie kasutajad ja teie agendid peavad järgima [lubatud kasutuse põhimõtteid](/acceptable-use). Need on osa nendest tingimustest ja loetlevad, milleks Teenust kasutada ei tohi.

Lubatud kasutuse põhimõtete usaldusväärse rikkumise korral võime kasutuse kohe peatada. Meil ei ole kohustust Kliendi sisu jälgida, kuid võime usaldusväärsete teadete põhjal tegutseda.

## 6. Kliendi andmed ja sisu

Teile kuuluvad teie Auditiandmed. Annate Kiff Agents OÜ-le piiratud, mitteainuõigusliku, tasuta, ülemaailmse litsentsi teie Auditiandmete majutamiseks, kopeerimiseks, edastamiseks, kuvamiseks ja muul viisil töötlemiseks ainult niivõrd, kuivõrd see on vajalik Teenuse käitamiseks, hooldamiseks ja täiustamiseks teie jaoks.

Tegutseme teie Auditiandmete ja teie tafsi andmete volitatud töötlejana GDPR-i artikli 28 alusel; seda suhet reguleerib üksikasjalikult [andmetöötlusleping](/legal/dpa). Teie konto metaandmete puhul tegutseme vastutava töötlejana; seda suhet reguleerib [privaatsuspoliitika](/privacy).

Kinnitate, et teil on kõik õigused esitatavate andmete esitamiseks ja et esitamine vastab kohaldatavatele õigusaktidele (sealhulgas andmekaitseõigusele ja, kui see on kohaldatav, ELi tehisintellekti määrusele).

## 7. Konfidentsiaalsus

Kumbki pool võib saada teiselt avalikustamata teavet („Konfidentsiaalne teave"). Vastuvõttev pool kasutab Konfidentsiaalset teavet ainult niivõrd, kuivõrd see on vajalik oma kohustuste täitmiseks ja oma õiguste kasutamiseks nende tingimuste alusel, ning kaitseb seda vähemalt sama hoolega, mida ta kasutab oma konfidentsiaalse teabe puhul.

Konfidentsiaalne teave ei hõlma teavet, mis (a) on või muutub avalikult kättesaadavaks ilma rikkumiseta, (b) oli vastuvõtvale poolele teada enne avalikustamist, (c) on välja töötatud iseseisvalt ilma Konfidentsiaalset teavet kasutamata või (d) tuleb seaduse alusel avalikustada (teisele poolele teatades, kui see on lubatud).

## 8. Tasud ja maksmine

Paketid. Igal kontol on üks pakett. Kehtivad hinnad on [hinnalehel](/pricing); selle uuenduse ajal:

- Solo: üks inimene, tafsi jaoks, müüakse rakenduses tafs App Store'i või Google Play kaudu hinnaga 12,50 € kuus, käibemaks sisaldub, sealhulgas 500 krediiti kuus.
- Company: 10,50 € kuus iga istekoha eest, 500 krediiti kuus iga istekoha kohta, ühiselt kasutatavana, ning teie enda agentide päringute jaoks KIFF Cloudi kaudu 500 toimingut kuus iga istekoha kohta, ühiselt kasutatavana.
- Leping: tasud ja tingimused selle tellimusvormis.

Istekohad on konto administraatorid ja toimetajad. Kinnitajad ja vaatajad on kõigis pakettides tasuta. Company paketis maksab paketti kuuluvast üle jääv krediit 0,01 € ja toiming 0,02 €. Paketti kuuluvatest üle jäävad krediidid arveldatakse kuni konto määratud igakuise kululimiidini (alguses 50 €): limiidi korral ei käivita KIFF konto tafide uusi käivitusi enne järgmise perioodi algust või seni, kuni arveldust haldav isik limiiti tõstab.

Krediidipaketid. Solo puhul tulevad kuu sisalduvatest krediitidest üle jäävad krediidid krediidipakettidest, mis ostetakse rakenduses tafs. Pakett ostetakse üks kord, see ei aegu ega ole tellimus; selle krediite kasutatakse pärast kuu sisalduvaid krediite, vanim pakett esimesena. Pakett, mille makse tagastatakse täielikult, võetakse tagasi koos selle järelejäänud krediitidega. Kui kuu krediidid ja paketid on kasutatud, ei käivita KIFF konto tafide uusi käivitusi enne järgmise perioodi algust või paketi lisamist. Boonuskrediidid, mille KIFF kontole lisab, ei aegu samuti, nagu paketid.

Mida arvestatakse. Krediit maksab selle AI eest, mida tafi käivitus kasutas, täiskrediitides, ümardatuna üles üks kord käivituse kohta ja arvestatuna üks kord, kui käivitus lõpeb. Käivitus, mille päringu KIFF tagasi lükkas, kasutas siiski oma AI-d. Kunagi ei arvestata: Coachi ja tafide seadistamist Coachi mõistliku kasutuse piires; uue versiooni proovi, mis ei saada midagi; ja käivitust, mis ebaõnnestus meie poolel. Toiming on teie enda agendi päring KIFF Cloudi kaudu, millega midagi tehti: KIFF lubas selle, see saadeti ja tööriist teatas õnnestumisest. Kunagi ei arvestata toiminguna: päringut, mis lükati tagasi, peeti kinni ja seejärel lükati tagasi, peatati peatamisega, oli kehtetu, ebaõnnestus tööriista juures või mille tulemus on teadmata, ega päringut, mis ainult loeb.

Prooviperioodi ei ole. Konto maksab oma esimesest päevast. Enne paketi algust ei käivita KIFF ühtegi selle tafide käivitust, välja arvatud Coachi seadistamine; midagi ei kustutata.

Makse ja arved. Paketti arveldatakse ühes kohas. kiff.dev-is ostetud paketti ostetakse, muudetakse ja tühistatakse seal ning arveldusportaalis, millele see viitab, ning selle makseid töötleb Stripe. Rakenduses tafs ostetud Solot müüakse App Store'i kaudu iPhone'is ja iPadis või Google Play kaudu Androidis ning seda hallatakse teie Apple'i või Google Play tellimustes, selle poe makse-, uuendamis- ja tagasimaksetingimuste alusel. Krediidipaketid ostetakse rakenduses. Company ostetakse kiff.dev-is. Tasud arveldatakse igakuiselt ette; paketti kuuluvast üle jääv kasutus arveldatakse järgmise arvega. Inimeste arvu muutus kehtib järgmisest arvest. Kui makse ebaõnnestub ja seda ei tasuta, pakett lõpeb ja KIFF lükkab konto päringud tagasi, kuni pakett on valitud.

Tühistamine. Võite igal ajal tühistada arveldusportaalis või, rakenduses ostetud Solo puhul, oma Apple'i või Google Play tellimustes. Pakett kehtib makstud perioodi lõpuni ja seda ei uuendata. Välja arvatud seadusega nõutud juhtudel, juba makstud tasusid ei tagastata. Kui olete ELi tarbija, võite ostust taganeda 14 päeva jooksul põhjust esitamata ning me tagastame makstud summa, millest on maha arvatud osa nende päevade eest, mil pakett juba kehtis. Solo ostud tagastatakse App Store'i või Google Play kaudu nende tingimuste alusel.

Käibemaks. Company hinnad ei sisalda käibemaksu; Solot ja krediidipakette müüvad App Store või Google Play käibemaksuga. Kiff Agents OÜ (käibemaksukohustuslase number EE102924327) lisab Eesti käibemaksu, kui see on kohaldatav. ELi-sisesed ettevõttelt ettevõttele müügid Kliendile väljaspool Eestit, kellel on kehtiv käibemaksukohustuslase number, arveldatakse ELi pöördmaksustamise korra alusel.

Muudatused. Teatame aktiivsetele kontodele hinnatõusust kirjalikult vähemalt kolmkümmend päeva ette (punkt 16). Hinnalangus kehtib järgmisest arvest.

## 9. Kehtivus ja lõpetamine

Need tingimused jõustuvad, kui kasutate Teenust esimest korda, ja kehtivad kuni lõpetamiseni.

Võite igal ajal lõpetada, kustutades oma konto. Meie võime lõpetada (a) olulise rikkumise korral, mida ei ole kõrvaldatud kolmekümne päeva jooksul pärast kirjalikku teatamist, või (b) kohe lubatud kasutuse rikkumise või maksejõuetusega seotud sündmuste korral.

Lahkumine ja oma andmete kaasavõtmine. Võite oma andmed igal ajal eksportida struktureeritud, masinloetavas vormingus: KIFFis annab Settings → Data JSON-faili teie konto, teie Cardide koos iga versiooniga, teie Record'i otsustega (viimased 10 000; rohkema saamiseks küsige meilt), iga KIFFi lubatud otsuse allkirjastatud kviitungiga ning teie API-võtmete andmetega (mitte kunagi võtmete endiga). Rakenduses tafs ekspordib iga inimene oma vestlused, failid ja märkmed menüüst Account → Export my chats; vestlusi hoitakse igal juhul kolmkümmend päeva, nagu ütleb privaatsuspoliitika. Konto kustutamine lõpetab Teenuse kohe, seega eksportige enne.

Võite selle asemel lahkuda etteteatamisega, et minna üle teisele pakkujale või oma süsteemidele, kirjutades aadressil [support@kiff.dev](mailto:support@kiff.dev). Me ei nõua kunagi rohkem kui kahe kuu pikkust etteteatamist: teie teade jõustub teie valitud kuupäeval, mitte hiljem kui kaks kuud pärast selle saatmist. Sellest kuupäevast järgneb kolmekümnepäevane üleminekuperiood, mille jooksul Teenus töötab edasi, te ekspordite oma andmed ja viite oma töö üle ning me aitame mõistlike taotlustega. Võite üleminekuperioodi üks kord pikendada, nii kauaks, kui vajate. Kui me ei saa seda tehniliselt kolmekümne päeva jooksul lõpule viia, teatame teile neljateistkümne tööpäeva jooksul pärast teie teadet, koos põhjuste ja uue, kuni seitsmekuulise perioodiga. Pärast üleminekuperioodi jäävad teie andmed eksportimiseks kättesaadavaks veel vähemalt kolmkümmend päeva. Paketi tasud jätkuvad tavapäraselt kuni üleminekuperioodi lõpuni. App Store'is või Google Plays ostetud Solo tühistatakse poes; ülemineku- ja ekspordiperioodid kehtivad samamoodi.

Me ei võta tasu lahkumise, üleminemise ega teie andmete eksportimise eest. See punkt annab kõigile klientidele ELi andmemääruse (määrus (EL) 2023/2854) VI peatüki kohased üleminekuõigused.

Lõpetamisel ning pärast võimalikku ülemineku- ja ekspordiperioodi kustutame teie Auditiandmed, teie tafsi andmed ja konto metaandmed kooskõlas [privaatsuspoliitika](/privacy#retention) säilitamisreeglitega.

Punktid, mis oma olemuselt peaksid lõpetamise järel kehtima jääma, jäävad kehtima, sealhulgas konfidentsiaalsus, intellektuaalomand, kahju hüvitamine, vastutuse piiramine, lahtiütlemised, kohaldatav õigus ja kõik tekkinud tasud.

## 10. Garantiid

Kumbki pool kinnitab, et tal on õigus ja volitus nende tingimustega nõustuda ja neid täita.

Garanteerime, et osutame Teenust professionaalselt ja Dokumentatsiooniga sisuliselt kooskõlas. Teie ainus õiguskaitsevahend selle garantii rikkumise korral on meie valikul teenuse uuesti osutamine või mõjutatud Teenusega seotud tasude proportsionaalne tagastamine rikkumise perioodi eest.

Garanteerite, et teie Teenuse kasutamine vastab kohaldatavale õigusele ja nendele tingimustele.

## 11. Lahtiütlemine

Välja arvatud punktis 10 sõnaselgelt toodud garantiid, pakutakse Teenust „nagu on" ja „nagu saadaval". Seadusega lubatud maksimaalses ulatuses ütleme lahti kõigist muudest garantiidest, olgu sõnaselgetest, kaudsetest või seadusjärgsetest, sealhulgas turustatavuse, konkreetseks otstarbeks sobivuse ja õiguste mitterikkumise garantiidest ning mis tahes garantiist, mis tuleneb tehingutavast või kaubandustavast.

Me ei garanteeri, et Teenus on katkestusteta, vigadeta või kahjulike komponentideta või et ühtegi andmeid ei kaotata ega rikuta. Teie vastutate oma andmete varundamise eest.

## 12. Vastutuse piiramine

Seadusega lubatud maksimaalses ulatuses ei vastuta kumbki pool teise ees mis tahes kaudse, juhusliku, järgneva, erilise, eeskujuliku või karistusliku kahju eest, sealhulgas saamata jäänud kasum, saamata jäänud tulu, kaotatud andmed või äritegevuse katkemine, mis tuleneb nendest tingimustest või Teenusest või on nendega seotud, isegi kui teda on sellise kahju võimalikkusest teavitatud.

Kummagi poole kogu vastutus, mis tuleneb nendest tingimustest või on nendega seotud, ei ületa suuremat järgmistest: (a) tasud, mille Klient maksis või peab maksma Kiff Agents OÜ-le vastutuse aluseks olevale sündmusele eelnenud kaheteistkümne kuu jooksul, või (b) ükssada tuhat eurot (100 000 €).

Miski nendes tingimustes ei piira ega välista vastutust, mida kohaldatava õiguse alusel ei saa piirata ega välistada, sealhulgas vastutust surma, hooletusest põhjustatud kehavigastuse, pettuse või tahtliku üleastumise eest ning vastutust puudusega toote eest õigusaktide alusel, millega rakendatakse direktiivi (EL) 2024/2853 puudusega toodete eest vastutuse kohta.

## 13. Kahju hüvitamine

Kaitseme teid kolmandate isikute nõuete eest, mille kohaselt meie pakutud ja teie poolt nende tingimuste kohaselt kasutatud Teenus rikub selle kolmanda isiku intellektuaalomandi õigusi, ning maksame teie vastu sellistes nõuetes lõplikult välja mõistetud kahjuhüvitise (või kokkulepitud kompromissisumma).

Teie kaitsete meid kolmandate isikute nõuete eest, mis tulenevad (a) teie Kliendi andmetest või sisust; (b) teie Teenuse kasutamisest nende tingimuste (sealhulgas lubatud kasutuse põhimõtete) rikkumisega; või (c) teie raskest hooletusest või tahtlikust üleastumisest, ning maksate meie vastu sellistes nõuetes lõplikult välja mõistetud kahjuhüvitise (või kokkulepitud kompromissisumma).

Kahju hüvitamise eeldused on nõudest viivitamatu teatamine, hüvitaja ainukontroll kaitse ja kompromissi üle ning hüvitatava mõistlik koostöö. Hüvitaja ei tohi ilma nõusolekuta sõlmida kompromissi, mis paneb hüvitatavale kohustusi.

## 14. Teenustaseme kohustus

Tasulise paketi puhul määrab [teenustaseme leping](/legal/sla) kättesaadavuse, mida lubame, kuidas aeglast või ebaõnnestunud päringut arvestatakse, meie toe reageerimisajad ning teenuskrediidid, kui me lubadusest maha jääme. See on osa nendest tingimustest.

Tööpraktikad, intsidentidele reageerimine ja rikkumistest teatamise kohustused on dokumenteeritud nendest tingimustest viidatud dateeritud versioonis ([kiff.dev/docs/archive/2026-10-10](https://kiff.dev/docs/archive/2026-10-10)); leht [/security](/security) näitab kehtivaid praktikaid.

## 15. Teenuse muudatused

Võime igal ajal funktsioone lisada, muuta või eemaldada. Funktsionaalsuse olulisest vähendamisest või API-pinna kasutuselt kõrvaldamisest, mis rikub dokumenteeritud käitumist, teatatakse aktiivsetele klientidele vähemalt kolmkümmend päeva ette koos avaldatud kõrvaldamise ajakavaga. Turvalisusest tingitud muudatusi võib teha ilma etteteatamiseta, kui etteteatamine ise tekitaks riski.

## 16. Nende tingimuste muudatused

Võime neid tingimusi aeg-ajalt muuta. Olulistest muudatustest teatatakse e-posti teel ja selgelt nähtava teatega sellel lehel vähemalt kolmkümmend päeva ette. Ebaolulised muudatused (täpsustused, viited) avaldatakse uue „viimati uuendatud" kuupäevaga. Teenuse jätkuv kasutamine pärast uuenduse jõustumiskuupäeva tähendab nõustumist.

## 17. Vääramatu jõud

Kumbki pool ei vastuta kohustuste täitmata jätmise või viivituse eest ulatuses, milles selle põhjustasid tema mõistliku kontrolli alt väljas olevad asjaolud, sealhulgas loodusõnnetused, sõda, terrorism, rahutused, töötülid, tulekahju, looduskatastroof, pandeemiaga seotud valitsuse meetmed, interneti- või elektrikatkestused või telekommunikatsioonipakkujate või kolmandate osapoolte majutustaristu tegevus.

## 18. Kohaldatav õigus ja kohtualluvus

Nendele tingimustele kohaldatakse Eesti Vabariigi õigust, arvestamata selle kollisiooninorme. Vaidluste ainupädev kohus on Harju Maakohus Tallinnas.

Kui olete tarbija, kelle elukoht on mõnes teises ELi liikmesriigis, kehtivad endiselt teie koduriigi kohustuslikud tarbijakaitse-eeskirjad ulatuses, milles need annavad teile õigusi, millest te ei saa lepinguga loobuda; see säte ei võta teilt nende eeskirjade kaitset.

## 19. Üldsätted

Kogu leping. Need tingimused koos privaatsuspoliitika, andmetöötluslepingu, võimalike tellimusvormide ja sõnaselgelt lisatud lisatingimustega moodustavad meievahelise kogu lepingu nende reguleerimisesemes ja asendavad kõik varasemad või samaaegsed kokkulepped.

Sätete eraldatavus. Kui mõni säte tunnistatakse jõustamatuks, jäävad ülejäänud kehtima.

Loovutamine. Te ei tohi neid tingimusi ilma meie kirjaliku nõusolekuta loovutada. Meie võime loovutada ilma nõusolekuta seoses ühinemise, omandamise või kogu või sisuliselt kogu meie vara müügiga, teile teatades.

Loobumise puudumine. Sätte jõustamata jätmine ei tähenda loobumist õigusest seda hiljem jõustada.

Teated. Teated Kiff Agents OÜ-le saadetakse aadressil [legal@kiff.dev](mailto:legal@kiff.dev) või posti teel aadressil Tööstuse tn 75-71, Põhja-Tallinna linnaosa, Tallinn 10416, Eesti. Teated teile saadetakse teie kontoga seotud e-posti aadressile või, kui olete selle esitanud, teie määratud õiguslike teadete aadressile.

Sõltumatud lepingupooled. Miski nendes tingimustes ei loo seltsingut, esindust, ühisettevõtet ega töösuhet.

Kolmandatest isikutest soodustatud isikud. Neid ei ole, välja arvatud ulatuses, milles hüvitatavad sidusettevõtjad on sõnaselgelt nimetatud.

## 20. Kontakt

Ärilised ja lepingulised küsimused: [legal@kiff.dev](mailto:legal@kiff.dev).

Üldised tootega seotud küsimused: [hello@kiff.dev](mailto:hello@kiff.dev). Privaatsusküsimused: [privacy@kiff.dev](mailto:privacy@kiff.dev). Turvateated: [security@kiff.dev](mailto:security@kiff.dev).

Dokumendi versioon: v0.1. Viimati uuendatud: 9. november 2026.
