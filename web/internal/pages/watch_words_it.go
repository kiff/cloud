package pages

// watchIT is /tafs/watch's words in Italian, at /it/tafs/watch. The watch's
// own lines are quoted as tafs says them in Italian.
var watchIT = watchWords{
	Lang:        "it",
	Title:       "tafs sul tuo orologio — KIFF",
	Description: "Vedi cosa ti aspetta, rifiuta una chiamata trattenuta, blocca un taf e chiedi a Coach dal tuo orologio. L'approvazione resta sul telefono.",
	Badge:       "tafs · orologio",
	Head:        "tafs sul tuo orologio",
	Sub:         "Vedi cosa ti aspetta, rifiuta una chiamata trattenuta, blocca un taf e chiedi a Coach, dal polso. L'approvazione resta sul telefono.",
	Does: watchSection{
		Head: "1. Cosa fa",
		Items: []string{
			"**Ti aspetta:** quante chiamate trattenute ti aspettano, e ognuna come la mostra il telefono: il suo taf, cosa chiede, l'importo e perché.",
			"**I tuoi tafs:** lo stato di ogni taf in una riga.",
			"**Rifiuta** una chiamata trattenuta, **Blocca ora** un taf, o **Blocca tutto ora**, che l'orologio ti chiede di confermare ancora una volta.",
			"**Chiedi a Coach** a voce.",
			"**Una scheda** con quanti ti aspettano.",
			"**Non si approva mai sull'orologio.** Per una chiamata trattenuta, una Card, un pagamento o un collegamento, l'orologio dice «Approva sul telefono» e lo apre lì.",
		},
	},
	Get: watchSection{
		Head: "2. Come averlo",
		Items: []string{
			"**Wear OS: In arrivo su Google Play.** Wear OS 3 o successivo. L'app per l'orologio arriva con tafs per Android: quando sarà su Google Play, installala da Google Play sull'orologio.",
			"**Apple Watch: In arrivo.** Arriverà con tafs per iPhone e farà le stesse cose, con l'approvazione sul tuo iPhone.",
			"**Nessun accesso sull'orologio.** Usa tafs sul telefono, che deve aver effettuato l'accesso ed essere vicino.",
		},
	},
	Act: watchSection{
		Head: "3. Rifiutare e bloccare",
		Items: []string{
			"**L'orologio ha bisogno di un blocco schermo, e deve essere sbloccato.** Altrimenti dice: «Per rifiutare o bloccare da questo orologio, imposta un blocco schermo e sbloccalo.»",
			"**Lo fa il telefono,** con la sua chiave, e l'orologio mostra il risultato quando ha finito, come «Rifiutata. Non è stato inviato nulla.»",
			"Se nel frattempo qualcuno ha risposto alla chiamata, l'orologio dice «Ha già una risposta, o è terminata.»",
		},
	},
	Coach: watchSection{
		Head: "4. Chiedi a Coach",
		Items: []string{
			"Tocca **Chiedi a Coach** e parla. La dettatura vocale dell'orologio trasforma quello che dici in testo, e l'orologio lo mostra prima di inviare qualsiasi cosa: **Invia** o **Annulla**.",
			"**Coach riceve le tue parole come testo, mai la tua voce.**",
			"Prima, consenti l'uso dell'IA in tafs sul telefono, e avvia Coach lì.",
			"Una risposta breve è mostrata sull'orologio. Una lunga, o una che ti chiede di rispondere, mostra l'inizio e **Apri sul telefono**.",
			"Come per rifiutare e bloccare, l'orologio ha bisogno di un blocco schermo e deve essere sbloccato.",
		},
	},
	Locked: watchSection{
		Head: "5. Quando l'orologio è bloccato",
		Items: []string{
			"La scheda mostra solo «tafs», e un avviso mostra solo «Un taf ti aspetta». Wear OS blocca l'orologio quando lo togli dal polso.",
			"Sbloccato, un avviso mostra ciò che mostra quello del telefono: quale taf e cosa chiede, il che può indicare un importo, un esercente o una persona.",
			"Su un Apple Watch, gli avvisi sono quelli del tuo iPhone, quindi è l'impostazione delle anteprime dell'iPhone a decidere cosa mostrano. Con le anteprime nascoste, che è l'impostazione dell'iPhone quando è bloccato se non la cambi, un avviso mostra solo «Un taf ti aspetta».",
		},
	},
	Keeps: watchSection{
		Head: "6. Cosa conserva",
		Items: []string{
			"Solo quanti ti aspettano, per la scheda. Non viene mai mostrato mentre l'orologio è bloccato, e allora viene cancellato, come quando esci dall'account sul telefono.",
			"Nessun accesso, nessuna chiave. Ciò che mostra viene dimenticato quando lo metti via, e nulla di ciò che dici viene conservato.",
		},
		Link: watchLink{Before: "I dettagli sono nell'", Text: "informativa sulla privacy di tafs", After: "."},
	},
	Help: watchSection{
		Head: "7. Se qualcosa non va",
		Items: []string{
			"«Il telefono non è raggiungibile. Avvicinalo, poi riprova.»",
			"«Prima accedi a tafs sul telefono.»",
		},
		Link: watchLink{Before: "Per tutto il resto: **Apri sul telefono**, oppure ", Text: "scrivici", After: "."},
	},
}
