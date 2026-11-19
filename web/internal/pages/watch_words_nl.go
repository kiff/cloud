package pages

// watchNL is /tafs/watch's words in Dutch, at /nl/tafs/watch. The watch's
// own lines are quoted as tafs says them in Dutch.
var watchNL = watchWords{
	Lang:        "nl",
	Title:       "tafs op je horloge — KIFF",
	Description: "Zie wat op je wacht, weiger een vastgehouden aanroep, zet een taf stil en vraag Coach vanaf je horloge. Goedkeuren blijft op je telefoon.",
	Badge:       "tafs · horloge",
	Head:        "tafs op je horloge",
	Sub:         "Zie wat op je wacht, weiger een vastgehouden aanroep, zet een taf stil en vraag Coach, vanaf je pols. Goedkeuren blijft op je telefoon.",
	Does: watchSection{
		Head: "1. Wat het doet",
		Items: []string{
			"**Wacht op jou:** hoeveel vastgehouden aanroepen op je wachten, en elke zoals je telefoon hem toont: de taf, wat hij wil, het bedrag en waarom.",
			"**Je tafs:** de status van elke taf op één regel.",
			"**Weigeren** van een vastgehouden aanroep, **Nu stilzetten** voor één taf, of **Alles nu stilzetten**, wat het horloge je nog een keer laat bevestigen.",
			"**Coach vragen** met je stem.",
			"**Een tegel** met hoeveel er op je wachten.",
			"**Goedkeuren gebeurt nooit op het horloge.** Bij een vastgehouden aanroep, een Card, een betaling of een koppeling zegt het horloge “Keur goed op je telefoon” en opent het daar.",
		},
	},
	Get: watchSection{
		Head: "2. Zo krijg je het",
		Items: []string{
			"**Wear OS: Binnenkort in Google Play.** Wear OS 3 of later. De horloge-app komt mee met tafs voor Android: zodra die in Google Play staat, installeer je hem vanuit Google Play op je horloge.",
			"**Apple Watch: Komt eraan.** Hij komt mee met tafs voor iPhone en doet hetzelfde, met goedkeuren op je iPhone.",
			"**Geen inloggen op het horloge.** Het gebruikt tafs op je telefoon, die ingelogd en in de buurt moet zijn.",
		},
	},
	Act: watchSection{
		Head: "3. Weigeren en stilzetten",
		Items: []string{
			"**Je horloge heeft een schermvergrendeling nodig, en moet ontgrendeld zijn.** Anders zegt het: “Stel een schermvergrendeling in op dit horloge en ontgrendel het om vanaf hier te weigeren of stil te zetten.”",
			"**Je telefoon doet het,** met zijn eigen sleutel, en het horloge toont het resultaat zodra het klaar is, zoals “Geweigerd. Er is niets verstuurd.”",
			"Heeft iemand de aanroep intussen beantwoord, dan zegt het horloge “Al beantwoord, of beëindigd.”",
		},
	},
	Coach: watchSection{
		Head: "4. Coach vragen",
		Items: []string{
			"Tik op **Coach vragen** en spreek. De spraakinvoer van je horloge maakt tekst van wat je zegt, en het horloge toont die voordat er iets wordt verzonden: **Verzenden** of **Annuleren**.",
			"**Coach krijgt je woorden als tekst, nooit je stem.**",
			"Sta eerst AI-gebruik toe in tafs op je telefoon, en start Coach daar.",
			"Een kort antwoord staat op het horloge. Een lang antwoord, of een waarop je iets moet antwoorden, toont het begin en **Openen op telefoon**.",
			"Net als voor weigeren en stilzetten heeft je horloge een schermvergrendeling nodig en moet het ontgrendeld zijn.",
		},
	},
	Locked: watchSection{
		Head: "5. Als je horloge vergrendeld is",
		Items: []string{
			"De tegel toont alleen “tafs”, en een melding alleen “Een taf wacht op jou”. Wear OS vergrendelt je horloge als je het van je pols haalt.",
			"Ontgrendeld toont een melding wat die van je telefoon toont: welke taf en wat hij wil, wat een bedrag, een verkoper of een persoon kan noemen.",
			"Op een Apple Watch zijn de meldingen die van je iPhone, dus bepaalt de voorvertoningsinstelling van je iPhone wat ze tonen. Met verborgen voorvertoningen, de instelling van de iPhone als hij vergrendeld is tenzij je die wijzigt, toont een melding alleen “Een taf wacht op jou”.",
		},
	},
	Keeps: watchSection{
		Head: "6. Wat het bewaart",
		Items: []string{
			"Alleen hoeveel er op je wachten, voor de tegel. Dat wordt nooit getoond zolang je horloge vergrendeld is, en dan gewist, net als wanneer je uitlogt op je telefoon.",
			"Geen inlog, geen sleutels. Wat het toont, wordt vergeten als je het wegzet, en niets wat je zegt wordt bewaard.",
		},
		Link: watchLink{Before: "De details staan in het ", Text: "privacybeleid van tafs", After: "."},
	},
	Help: watchSection{
		Head: "7. Als er iets niet klopt",
		Items: []string{
			"“Je telefoon is buiten bereik. Breng hem dichterbij en probeer het opnieuw.”",
			"“Log eerst in bij tafs op je telefoon.”",
		},
		Link: watchLink{Before: "Al het andere: **Openen op telefoon**, of ", Text: "schrijf ons", After: "."},
	},
}
