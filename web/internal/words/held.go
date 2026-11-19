package words

import "fmt"

// Held is the lines of a held call's screen, as the app opens it
// (/needs-you/{id}?lang=). What the call is, its amount, why it's held and
// what it sends are KIFF's own lines (apps/api, in the language asked for);
// these are the screen's around them.
type Held struct {
	Heading string // the glossary's "Needs you"
	// Status is a held call's state, by apps/api's status.
	Status map[string]string

	Why, Card, Sends, ArgsUnavailable, NoArgs string

	AnswerByF                 string // the deadline
	PastTotal, ManagerCanAdd  string
	ApproveOnce, Refuse       string // the glossary's
	ReviseCard, ExtraRoom     string
	RoleCantAnswer            string
	HowTitle, HowOnce, HowURL string

	NoRoom, Unavailable string

	AnsweredByF, CardChangedByF string // who
	FullDetails, LoadError      string

	// After an answer: what happened, or why nothing did.
	DoneApproveOnce, DoneChangeCard, DoneRefuse, DoneRefuseStep string
	Conflict, StepNotAccepted, NotSaved                         string
}

func (h Held) AnswerBy(when string) string     { return fmt.Sprintf(h.AnswerByF, when) }
func (h Held) AnsweredBy(who string) string    { return fmt.Sprintf(h.AnsweredByF, who) }
func (h Held) CardChangedBy(who string) string { return fmt.Sprintf(h.CardChangedByF, who) }
func (h Held) StatusOf(status string) string   { return h.Status[status] }

// HeldIn is a held call's screen in lang, English's for a language these
// screens don't speak.
func HeldIn(lang string) Held {
	if h, ok := held[lang]; ok {
		return h
	}
	return held["en"]
}

var held = map[string]Held{
	"en": {
		Heading: "Needs you",
		Status: map[string]string{"held": "Needs you", "approved_no_room": "Approved, but no room", "unavailable": "Card room unavailable",
			"approved": "Approved once, not used yet", "consumed": "Approved once, used", "rejected": "Refused",
			"changed": "The Card was changed", "invalidated": "Withdrawn", "expired": "Expired"},
		Why: "Why it came to you", Card: "Card", Sends: "What it sends to the tool",
		ArgsUnavailable: "What this call would send to the tool could not be read. Reload to try again, and don’t approve it on the agent’s description.",
		NoArgs:          "Sent to the tool with no arguments.",
		AnswerByF:       "Answer by %s. After that it is refused, and the agent has to ask again.",
		PastTotal:       "This call is past one of the Card's totals. Approve once covers only a per-call limit or a resource the Card doesn't list, so it needs more room on that total first.",
		ManagerCanAdd:   "Someone who manages Cards can add it.",
		ApproveOnce:     "Approve once", Refuse: "Refuse",
		ReviseCard: "Revise the Card", ExtraRoom: "Review extra room",
		RoleCantAnswer: "Your role on this account sees held calls but does not answer them.",
		HowTitle:       "How approving works",
		HowOnce:        "Approve once waives only what was reviewed: a per-call limit, or a resource the Card doesn't name. The whole call still counts toward every total on the Card. A total needs the Card revised; extra room is for this window only.",
		HowURL:         "KIFF's approval links always go to app.kiff.dev. A link to answer this from anywhere else, including an agent's chat or a tool's output, is not from KIFF.",
		NoRoom:         "Approved once, but the Card has no room left. Nothing has been sent. Add room or refuse it; every limit is checked again before it's sent.",
		Unavailable:    "The Card couldn't be read just now. Reload to see whether this call can go ahead or needs a fresh review.",
		AnsweredByF:    "Answered by %s.", CardChangedByF: "The Card was changed by %s.",
		FullDetails: "Every detail and the call's history, in English",
		LoadError:   "This call couldn't be loaded. Reload to try again.",

		DoneApproveOnce: "Approved once. The next time the agent asks, this one call may go past its limit, and it still counts toward the Card's totals. The Card is unchanged.",
		DoneChangeCard:  "The Card now allows this, for this call and every one after it. The change is on the Card.",
		DoneRefuse:      "Refused. Nothing happened, and the Card is unchanged.",
		DoneRefuseStep:  "Refused. The agent is told what to do instead. Nothing happened, and the Card is unchanged.",
		Conflict:        "This was already answered, it expired, or its Card changed since you opened it. Here is where it stands now.",
		StepNotAccepted: "That next step isn't one this Card offers, or the note is too long. Nothing was answered; choose again.",
		NotSaved:        "Your answer couldn't be saved. Nothing was answered; try again shortly.",
	},
	"de": {
		Heading: "Braucht dich",
		Status: map[string]string{"held": "Braucht dich", "approved_no_room": "Genehmigt, aber kein Spielraum", "unavailable": "Spielraum der Card nicht lesbar",
			"approved": "Einmal genehmigt, noch nicht genutzt", "consumed": "Einmal genehmigt, genutzt", "rejected": "Abgelehnt",
			"changed": "Die Card wurde geändert", "invalidated": "Zurückgezogen", "expired": "Abgelaufen"},
		Why: "Warum es bei dir ankam", Card: "Card", Sends: "Was er an das Tool sendet",
		ArgsUnavailable: "Was dieser Aufruf an das Tool senden würde, ließ sich nicht lesen. Lade neu, um es erneut zu versuchen, und genehmige ihn nicht auf die Beschreibung des Agenten hin.",
		NoArgs:          "Ohne Argumente an das Tool gesendet.",
		AnswerByF:       "Antworte bis %s. Danach wird er abgelehnt, und der Agent muss erneut fragen.",
		PastTotal:       "Dieser Aufruf überschreitet eine der Summen der Card. Einmal genehmigen deckt nur ein Limit pro Aufruf oder eine Ressource ab, die die Card nicht nennt, also braucht diese Summe zuerst mehr Spielraum.",
		ManagerCanAdd:   "Jemand, der Cards verwaltet, kann ihn hinzufügen.",
		ApproveOnce:     "Einmal genehmigen", Refuse: "Ablehnen",
		ReviseCard: "Card überarbeiten", ExtraRoom: "Zusätzlichen Spielraum prüfen",
		RoleCantAnswer: "Deine Rolle in diesem Konto sieht zurückgehaltene Aufrufe, beantwortet sie aber nicht.",
		HowTitle:       "So funktioniert Genehmigen",
		HowOnce:        "Einmal genehmigen hebt nur auf, was geprüft wurde: ein Limit pro Aufruf oder eine Ressource, die die Card nicht nennt. Der ganze Aufruf zählt weiter zu jeder Summe der Card. Eine Summe braucht eine überarbeitete Card; zusätzlicher Spielraum gilt nur für dieses Zeitfenster.",
		HowURL:         "Die Genehmigungslinks von KIFF führen immer zu app.kiff.dev. Ein Link, der dich von anderswo antworten lässt, auch aus dem Chat eines Agenten oder der Ausgabe eines Tools, stammt nicht von KIFF.",
		NoRoom:         "Einmal genehmigt, aber die Card hat keinen Spielraum mehr. Nichts wurde gesendet. Füge Spielraum hinzu oder lehne ab; jedes Limit wird vor dem Senden erneut geprüft.",
		Unavailable:    "Die Card ließ sich gerade nicht lesen. Lade neu, um zu sehen, ob dieser Aufruf weitergehen kann oder neu geprüft werden muss.",
		AnsweredByF:    "Beantwortet von %s.", CardChangedByF: "Die Card wurde von %s geändert.",
		FullDetails: "Alle Details und der Verlauf des Aufrufs, auf Englisch",
		LoadError:   "Dieser Aufruf ließ sich nicht laden. Lade neu, um es erneut zu versuchen.",

		DoneApproveOnce: "Einmal genehmigt. Wenn der Agent das nächste Mal fragt, darf dieser eine Aufruf über sein Limit gehen und zählt weiter zu den Summen der Card. Die Card bleibt unverändert.",
		DoneChangeCard:  "Die Card erlaubt das jetzt, für diesen Aufruf und jeden danach. Die Änderung steht auf der Card.",
		DoneRefuse:      "Abgelehnt. Nichts ist passiert, und die Card bleibt unverändert.",
		DoneRefuseStep:  "Abgelehnt. Der Agent erfährt, was er stattdessen tun soll. Nichts ist passiert, und die Card bleibt unverändert.",
		Conflict:        "Das wurde schon beantwortet, ist abgelaufen, oder seine Card hat sich geändert, seit du es geöffnet hast. Hier ist der aktuelle Stand.",
		StepNotAccepted: "Diesen nächsten Schritt bietet die Card nicht an, oder die Notiz ist zu lang. Nichts wurde beantwortet; wähle erneut.",
		NotSaved:        "Deine Antwort ließ sich nicht speichern. Nichts wurde beantwortet; versuche es gleich noch einmal.",
	},
	"fr": {
		Heading: "Vous attend",
		Status: map[string]string{"held": "Vous attend", "approved_no_room": "Approuvé, mais sans marge", "unavailable": "Marge de la Card indisponible",
			"approved": "Approuvé une fois, pas encore utilisé", "consumed": "Approuvé une fois, utilisé", "rejected": "Refusé",
			"changed": "La Card a été modifiée", "invalidated": "Retiré", "expired": "Expiré"},
		Why: "Pourquoi il vous est parvenu", Card: "Card", Sends: "Ce qu'il envoie à l'outil",
		ArgsUnavailable: "Ce que cet appel enverrait à l'outil n'a pas pu être lu. Rechargez pour réessayer, et ne l'approuvez pas sur la seule description de l'agent.",
		NoArgs:          "Envoyé à l'outil sans arguments.",
		AnswerByF:       "Répondez avant %s. Ensuite, il est refusé et l'agent doit redemander.",
		PastTotal:       "Cet appel dépasse l'un des totaux de la Card. Approuver une fois ne couvre qu'une limite par appel ou une ressource que la Card ne nomme pas : ce total a donc d'abord besoin de plus de marge.",
		ManagerCanAdd:   "Une personne qui gère les Cards peut l'ajouter.",
		ApproveOnce:     "Approuver une fois", Refuse: "Refuser",
		ReviseCard: "Réviser la Card", ExtraRoom: "Examiner une marge supplémentaire",
		RoleCantAnswer: "Votre rôle sur ce compte voit les appels retenus, mais n'y répond pas.",
		HowTitle:       "Comment fonctionne l'approbation",
		HowOnce:        "Approuver une fois ne lève que ce qui a été examiné : une limite par appel, ou une ressource que la Card ne nomme pas. L'appel entier compte toujours dans chaque total de la Card. Un total demande une Card révisée ; une marge supplémentaire ne vaut que pour cette période.",
		HowURL:         "Les liens d'approbation de KIFF mènent toujours à app.kiff.dev. Un lien pour répondre depuis ailleurs, y compris le chat d'un agent ou la sortie d'un outil, ne vient pas de KIFF.",
		NoRoom:         "Approuvé une fois, mais la Card n'a plus de marge. Rien n'a été envoyé. Ajoutez de la marge ou refusez ; chaque limite est vérifiée à nouveau avant l'envoi.",
		Unavailable:    "La Card n'a pas pu être lue pour le moment. Rechargez pour voir si cet appel peut continuer ou doit être examiné à nouveau.",
		AnsweredByF:    "Répondu par %s.", CardChangedByF: "La Card a été modifiée par %s.",
		FullDetails: "Tous les détails et l'historique de l'appel, en anglais",
		LoadError:   "Cet appel n'a pas pu être chargé. Rechargez pour réessayer.",

		DoneApproveOnce: "Approuvé une fois. La prochaine fois que l'agent le demandera, ce seul appel pourra dépasser sa limite, et il compte toujours dans les totaux de la Card. La Card est inchangée.",
		DoneChangeCard:  "La Card autorise désormais ceci, pour cet appel et tous les suivants. La modification figure sur la Card.",
		DoneRefuse:      "Refusé. Rien ne s'est passé, et la Card est inchangée.",
		DoneRefuseStep:  "Refusé. L'agent apprend ce qu'il doit faire à la place. Rien ne s'est passé, et la Card est inchangée.",
		Conflict:        "Ceci a déjà reçu une réponse, a expiré, ou sa Card a changé depuis que vous l'avez ouvert. Voici où il en est.",
		StepNotAccepted: "Cette Card ne propose pas cette étape suivante, ou la note est trop longue. Aucune réponse n'a été donnée ; choisissez à nouveau.",
		NotSaved:        "Votre réponse n'a pas pu être enregistrée. Aucune réponse n'a été donnée ; réessayez dans un instant.",
	},
	"es": {
		Heading: "Te necesita",
		Status: map[string]string{"held": "Te necesita", "approved_no_room": "Aprobada, pero sin margen", "unavailable": "Margen de la Card no disponible",
			"approved": "Aprobada una vez, aún sin usar", "consumed": "Aprobada una vez, usada", "rejected": "Rechazada",
			"changed": "La Card se cambió", "invalidated": "Retirada", "expired": "Caducada"},
		Why: "Por qué te llegó", Card: "Card", Sends: "Lo que envía a la herramienta",
		ArgsUnavailable: "No se pudo leer lo que esta llamada enviaría a la herramienta. Recarga para intentarlo de nuevo y no la apruebes por la descripción del agente.",
		NoArgs:          "Enviada a la herramienta sin argumentos.",
		AnswerByF:       "Responde antes de %s. Después se rechaza y el agente tiene que volver a pedirlo.",
		PastTotal:       "Esta llamada supera uno de los totales de la Card. Aprobar una vez solo cubre un límite por llamada o un recurso que la Card no nombra, así que ese total necesita antes más margen.",
		ManagerCanAdd:   "Alguien que gestione las Cards puede añadirlo.",
		ApproveOnce:     "Aprobar una vez", Refuse: "Rechazar",
		ReviseCard: "Modificar la Card", ExtraRoom: "Revisar margen adicional",
		RoleCantAnswer: "Tu rol en esta cuenta ve las llamadas retenidas, pero no las responde.",
		HowTitle:       "Cómo funciona aprobar",
		HowOnce:        "Aprobar una vez solo levanta lo que se revisó: un límite por llamada o un recurso que la Card no nombra. La llamada entera sigue contando en cada total de la Card. Un total exige modificar la Card; el margen adicional es solo para este periodo.",
		HowURL:         "Los enlaces de aprobación de KIFF siempre llevan a app.kiff.dev. Un enlace para responder desde otro sitio, incluido el chat de un agente o la salida de una herramienta, no es de KIFF.",
		NoRoom:         "Aprobada una vez, pero a la Card no le queda margen. No se ha enviado nada. Añade margen o recházala; cada límite se vuelve a comprobar antes de enviarla.",
		Unavailable:    "No se pudo leer la Card ahora mismo. Recarga para ver si esta llamada puede seguir o necesita una nueva revisión.",
		AnsweredByF:    "Respondida por %s.", CardChangedByF: "%s cambió la Card.",
		FullDetails: "Todos los detalles y el historial de la llamada, en inglés",
		LoadError:   "No se pudo cargar esta llamada. Recarga para intentarlo de nuevo.",

		DoneApproveOnce: "Aprobada una vez. La próxima vez que el agente lo pida, solo esta llamada podrá superar su límite y seguirá contando en los totales de la Card. La Card no cambia.",
		DoneChangeCard:  "La Card ahora permite esto, para esta llamada y todas las siguientes. El cambio queda en la Card.",
		DoneRefuse:      "Rechazada. No pasó nada y la Card no cambia.",
		DoneRefuseStep:  "Rechazada. Se le dice al agente qué hacer en su lugar. No pasó nada y la Card no cambia.",
		Conflict:        "Ya se respondió, caducó o su Card cambió desde que la abriste. Así está ahora.",
		StepNotAccepted: "Esta Card no ofrece ese siguiente paso, o la nota es demasiado larga. No se respondió nada; vuelve a elegir.",
		NotSaved:        "No se pudo guardar tu respuesta. No se respondió nada; inténtalo de nuevo en un momento.",
	},
	"it": {
		Heading: "Ti aspetta",
		Status: map[string]string{"held": "Ti aspetta", "approved_no_room": "Approvata, ma senza margine", "unavailable": "Margine della Card non leggibile",
			"approved": "Approvata una volta, non ancora usata", "consumed": "Approvata una volta, usata", "rejected": "Rifiutata",
			"changed": "La Card è stata modificata", "invalidated": "Ritirata", "expired": "Scaduta"},
		Why: "Perché è arrivata a te", Card: "Card", Sends: "Cosa invia allo strumento",
		ArgsUnavailable: "Non è stato possibile leggere cosa questa chiamata invierebbe allo strumento. Ricarica per riprovare e non approvarla sulla descrizione dell'agente.",
		NoArgs:          "Inviata allo strumento senza argomenti.",
		AnswerByF:       "Rispondi entro %s. Dopo viene rifiutata e l'agente deve chiedere di nuovo.",
		PastTotal:       "Questa chiamata supera uno dei totali della Card. Approva una volta copre solo un limite per chiamata o una risorsa che la Card non nomina, quindi quel totale ha prima bisogno di più margine.",
		ManagerCanAdd:   "Chi gestisce le Card può aggiungerlo.",
		ApproveOnce:     "Approva una volta", Refuse: "Rifiuta",
		ReviseCard: "Modifica la Card", ExtraRoom: "Valuta un margine extra",
		RoleCantAnswer: "Il tuo ruolo in questo account vede le chiamate trattenute, ma non vi risponde.",
		HowTitle:       "Come funziona l'approvazione",
		HowOnce:        "Approva una volta sospende solo ciò che è stato esaminato: un limite per chiamata, o una risorsa che la Card non nomina. L'intera chiamata conta comunque in ogni totale della Card. Un totale richiede di modificare la Card; il margine extra vale solo per questo periodo.",
		HowURL:         "I link di approvazione di KIFF portano sempre a app.kiff.dev. Un link per rispondere da altrove, anche dalla chat di un agente o dall'output di uno strumento, non è di KIFF.",
		NoRoom:         "Approvata una volta, ma la Card non ha più margine. Non è stato inviato nulla. Aggiungi margine o rifiutala; ogni limite viene ricontrollato prima dell'invio.",
		Unavailable:    "Al momento non è stato possibile leggere la Card. Ricarica per vedere se questa chiamata può proseguire o va riesaminata.",
		AnsweredByF:    "Risposta di %s.", CardChangedByF: "La Card è stata modificata da %s.",
		FullDetails: "Tutti i dettagli e la cronologia della chiamata, in inglese",
		LoadError:   "Non è stato possibile caricare questa chiamata. Ricarica per riprovare.",

		DoneApproveOnce: "Approvata una volta. La prossima volta che l'agente lo chiede, questa sola chiamata può superare il suo limite, e conta comunque nei totali della Card. La Card resta invariata.",
		DoneChangeCard:  "Ora la Card lo consente, per questa chiamata e per tutte le successive. La modifica è sulla Card.",
		DoneRefuse:      "Rifiutata. Non è successo nulla e la Card resta invariata.",
		DoneRefuseStep:  "Rifiutata. All'agente viene detto cosa fare invece. Non è successo nulla e la Card resta invariata.",
		Conflict:        "Ha già ricevuto risposta, è scaduta, oppure la sua Card è cambiata da quando l'hai aperta. Ecco com'è ora.",
		StepNotAccepted: "Questa Card non offre quel passo successivo, oppure la nota è troppo lunga. Non è stata data risposta; scegli di nuovo.",
		NotSaved:        "Non è stato possibile salvare la tua risposta. Non è stata data risposta; riprova tra poco.",
	},
	"pt": {
		Heading: "Precisa de si",
		Status: map[string]string{"held": "Precisa de si", "approved_no_room": "Aprovada, mas sem margem", "unavailable": "Margem do Card indisponível",
			"approved": "Aprovada uma vez, ainda não usada", "consumed": "Aprovada uma vez, usada", "rejected": "Recusada",
			"changed": "O Card foi alterado", "invalidated": "Retirada", "expired": "Expirada"},
		Why: "Porque lhe chegou", Card: "Card", Sends: "O que envia para a ferramenta",
		ArgsUnavailable: "Não foi possível ler o que esta chamada enviaria para a ferramenta. Recarregue para tentar novamente e não a aprove com base na descrição do agente.",
		NoArgs:          "Enviada para a ferramenta sem argumentos.",
		AnswerByF:       "Responda até %s. Depois disso é recusada e o agente tem de pedir novamente.",
		PastTotal:       "Esta chamada ultrapassa um dos totais do Card. Aprovar uma vez só cobre um limite por chamada ou um recurso que o Card não indica, por isso esse total precisa primeiro de mais margem.",
		ManagerCanAdd:   "Quem gere os Cards pode adicioná-la.",
		ApproveOnce:     "Aprovar uma vez", Refuse: "Recusar",
		ReviseCard: "Alterar o Card", ExtraRoom: "Analisar margem adicional",
		RoleCantAnswer: "A sua função nesta conta vê as chamadas retidas, mas não lhes responde.",
		HowTitle:       "Como funciona aprovar",
		HowOnce:        "Aprovar uma vez só levanta o que foi analisado: um limite por chamada, ou um recurso que o Card não indica. A chamada inteira continua a contar para cada total do Card. Um total exige alterar o Card; a margem adicional vale só para este período.",
		HowURL:         "Os links de aprovação da KIFF levam sempre a app.kiff.dev. Um link para responder a partir de outro sítio, incluindo o chat de um agente ou a saída de uma ferramenta, não é da KIFF.",
		NoRoom:         "Aprovada uma vez, mas o Card já não tem margem. Nada foi enviado. Adicione margem ou recuse; cada limite é verificado novamente antes do envio.",
		Unavailable:    "Não foi possível ler o Card neste momento. Recarregue para ver se esta chamada pode avançar ou precisa de nova análise.",
		AnsweredByF:    "Respondida por %s.", CardChangedByF: "O Card foi alterado por %s.",
		FullDetails: "Todos os detalhes e o histórico da chamada, em inglês",
		LoadError:   "Não foi possível carregar esta chamada. Recarregue para tentar novamente.",

		DoneApproveOnce: "Aprovada uma vez. Da próxima vez que o agente pedir, só esta chamada pode ultrapassar o seu limite e continua a contar para os totais do Card. O Card não muda.",
		DoneChangeCard:  "O Card passa a permitir isto, para esta chamada e todas as seguintes. A alteração fica no Card.",
		DoneRefuse:      "Recusada. Nada aconteceu e o Card não muda.",
		DoneRefuseStep:  "Recusada. O agente fica a saber o que fazer em vez disso. Nada aconteceu e o Card não muda.",
		Conflict:        "Já foi respondida, expirou, ou o seu Card mudou desde que a abriu. É assim que está agora.",
		StepNotAccepted: "Este Card não oferece esse passo seguinte, ou a nota é demasiado longa. Nada foi respondido; escolha novamente.",
		NotSaved:        "Não foi possível guardar a sua resposta. Nada foi respondido; tente novamente dentro de momentos.",
	},
	"nl": {
		Heading: "Wacht op jou",
		Status: map[string]string{"held": "Wacht op jou", "approved_no_room": "Goedgekeurd, maar geen ruimte", "unavailable": "Ruimte van de Card niet beschikbaar",
			"approved": "Eenmalig goedgekeurd, nog niet gebruikt", "consumed": "Eenmalig goedgekeurd, gebruikt", "rejected": "Geweigerd",
			"changed": "De Card is gewijzigd", "invalidated": "Ingetrokken", "expired": "Verlopen"},
		Why: "Waarom het bij jou kwam", Card: "Card", Sends: "Wat hij naar de tool stuurt",
		ArgsUnavailable: "Wat deze aanroep naar de tool zou sturen, kon niet worden gelezen. Laad opnieuw om het nog eens te proberen, en keur hem niet goed op basis van de beschrijving van de agent.",
		NoArgs:          "Zonder argumenten naar de tool gestuurd.",
		AnswerByF:       "Antwoord vóór %s. Daarna wordt hij geweigerd en moet de agent het opnieuw vragen.",
		PastTotal:       "Deze aanroep gaat over een van de totalen van de Card. Eenmalig goedkeuren dekt alleen een limiet per aanroep of een resource die de Card niet noemt, dus dat totaal heeft eerst meer ruimte nodig.",
		ManagerCanAdd:   "Iemand die Cards beheert, kan die toevoegen.",
		ApproveOnce:     "Eenmalig goedkeuren", Refuse: "Weigeren",
		ReviseCard: "De Card herzien", ExtraRoom: "Extra ruimte bekijken",
		RoleCantAnswer: "Je rol in dit account ziet vastgehouden aanroepen, maar beantwoordt ze niet.",
		HowTitle:       "Zo werkt goedkeuren",
		HowOnce:        "Eenmalig goedkeuren heft alleen op wat is bekeken: een limiet per aanroep, of een resource die de Card niet noemt. De hele aanroep telt nog steeds mee voor elk totaal van de Card. Een totaal vraagt een herziene Card; extra ruimte geldt alleen voor deze periode.",
		HowURL:         "De goedkeuringslinks van KIFF gaan altijd naar app.kiff.dev. Een link om hier ergens anders op te antwoorden, ook in de chat van een agent of de uitvoer van een tool, komt niet van KIFF.",
		NoRoom:         "Eenmalig goedgekeurd, maar de Card heeft geen ruimte meer. Er is niets verstuurd. Voeg ruimte toe of weiger; elke limiet wordt voor het versturen opnieuw gecontroleerd.",
		Unavailable:    "De Card kon nu niet worden gelezen. Laad opnieuw om te zien of deze aanroep door kan of opnieuw bekeken moet worden.",
		AnsweredByF:    "Beantwoord door %s.", CardChangedByF: "De Card is gewijzigd door %s.",
		FullDetails: "Alle details en de geschiedenis van de aanroep, in het Engels",
		LoadError:   "Deze aanroep kon niet worden geladen. Laad opnieuw om het nog eens te proberen.",

		DoneApproveOnce: "Eenmalig goedgekeurd. De volgende keer dat de agent het vraagt, mag deze ene aanroep over zijn limiet gaan, en hij telt nog steeds mee voor de totalen van de Card. De Card blijft ongewijzigd.",
		DoneChangeCard:  "De Card staat dit nu toe, voor deze aanroep en elke volgende. De wijziging staat op de Card.",
		DoneRefuse:      "Geweigerd. Er is niets gebeurd en de Card blijft ongewijzigd.",
		DoneRefuseStep:  "Geweigerd. De agent hoort wat hij in plaats daarvan moet doen. Er is niets gebeurd en de Card blijft ongewijzigd.",
		Conflict:        "Dit is al beantwoord, is verlopen, of de Card is veranderd sinds je het opende. Zo staat het er nu voor.",
		StepNotAccepted: "Deze Card biedt die volgende stap niet aan, of de notitie is te lang. Er is niets beantwoord; kies opnieuw.",
		NotSaved:        "Je antwoord kon niet worden opgeslagen. Er is niets beantwoord; probeer het zo opnieuw.",
	},
	"pl": {
		Heading: "Czeka na Ciebie",
		Status: map[string]string{"held": "Czeka na Ciebie", "approved_no_room": "Zatwierdzone, ale bez zapasu", "unavailable": "Zapas na Card niedostępny",
			"approved": "Zatwierdzone raz, jeszcze nieużyte", "consumed": "Zatwierdzone raz, użyte", "rejected": "Odrzucone",
			"changed": "Card została zmieniona", "invalidated": "Wycofane", "expired": "Wygasło"},
		Why: "Dlaczego trafiło do Ciebie", Card: "Card", Sends: "Co wysyła do narzędzia",
		ArgsUnavailable: "Nie udało się odczytać, co to wywołanie wysłałoby do narzędzia. Odśwież, aby spróbować ponownie, i nie zatwierdzaj go na podstawie opisu agenta.",
		NoArgs:          "Wysłane do narzędzia bez argumentów.",
		AnswerByF:       "Odpowiedz do %s. Potem zostanie odrzucone, a agent będzie musiał zapytać ponownie.",
		PastTotal:       "To wywołanie przekracza jedną z sum na Card. Zatwierdź raz obejmuje tylko limit na wywołanie albo zasób, którego Card nie wymienia, więc ta suma potrzebuje najpierw większego zapasu.",
		ManagerCanAdd:   "Może go dodać osoba zarządzająca Card.",
		ApproveOnce:     "Zatwierdź raz", Refuse: "Odrzuć",
		ReviseCard: "Zmień Card", ExtraRoom: "Sprawdź dodatkowy zapas",
		RoleCantAnswer: "Twoja rola na tym koncie widzi oczekujące wywołania, ale na nie nie odpowiada.",
		HowTitle:       "Jak działa zatwierdzanie",
		HowOnce:        "Zatwierdź raz znosi tylko to, co zostało sprawdzone: limit na wywołanie albo zasób, którego Card nie wymienia. Całe wywołanie nadal liczy się do każdej sumy na Card. Suma wymaga zmiany Card; dodatkowy zapas obowiązuje tylko w tym okresie.",
		HowURL:         "Linki KIFF do zatwierdzania zawsze prowadzą do app.kiff.dev. Link do odpowiedzi z innego miejsca, także z czatu agenta lub wyniku narzędzia, nie pochodzi od KIFF.",
		NoRoom:         "Zatwierdzone raz, ale na Card nie ma już zapasu. Nic nie zostało wysłane. Dodaj zapas albo odrzuć; każdy limit jest sprawdzany ponownie przed wysłaniem.",
		Unavailable:    "Nie udało się teraz odczytać Card. Odśwież, aby zobaczyć, czy to wywołanie może przejść, czy wymaga ponownego sprawdzenia.",
		AnsweredByF:    "Odpowiedź od %s.", CardChangedByF: "Card została zmieniona przez %s.",
		FullDetails: "Wszystkie szczegóły i historia wywołania, po angielsku",
		LoadError:   "Nie udało się wczytać tego wywołania. Odśwież, aby spróbować ponownie.",

		DoneApproveOnce: "Zatwierdzone raz. Gdy agent zapyta następnym razem, to jedno wywołanie może przekroczyć swój limit i nadal liczy się do sum na Card. Card się nie zmienia.",
		DoneChangeCard:  "Card pozwala teraz na to, dla tego wywołania i każdego następnego. Zmiana jest zapisana na Card.",
		DoneRefuse:      "Odrzucone. Nic się nie stało, a Card się nie zmienia.",
		DoneRefuseStep:  "Odrzucone. Agent dowie się, co zrobić zamiast tego. Nic się nie stało, a Card się nie zmienia.",
		Conflict:        "Na to już odpowiedziano, wygasło albo jego Card zmieniła się od chwili otwarcia. Tak to wygląda teraz.",
		StepNotAccepted: "Ta Card nie oferuje takiego następnego kroku albo notatka jest za długa. Nie udzielono żadnej odpowiedzi; wybierz ponownie.",
		NotSaved:        "Nie udało się zapisać Twojej odpowiedzi. Nie udzielono żadnej odpowiedzi; spróbuj ponownie za chwilę.",
	},
}
