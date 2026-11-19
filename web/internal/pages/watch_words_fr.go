package pages

// watchFR is /tafs/watch's words in French, at /fr/tafs/watch. The watch's
// own lines are quoted as tafs says them in French.
var watchFR = watchWords{
	Lang:        "fr",
	Title:       "tafs sur votre montre — KIFF",
	Description: "Voyez ce qui vous attend, refusez un appel retenu, bloquez un taf et demandez à Coach depuis votre montre. L'approbation reste sur votre téléphone.",
	Badge:       "tafs · montre",
	Head:        "tafs sur votre montre",
	Sub:         "Voyez ce qui vous attend, refusez un appel retenu, bloquez un taf et demandez à Coach, depuis votre poignet. L'approbation reste sur votre téléphone.",
	Does: watchSection{
		Head: "1. Ce qu'il fait",
		Items: []string{
			"**Vous attend :** combien d'appels retenus vous attendent, et chacun tel que votre téléphone le montre : son taf, ce qu'il demande, le montant et pourquoi.",
			"**Vos tafs :** l'état de chaque taf en une ligne.",
			"**Refuser** un appel retenu, **Bloquer maintenant** un taf, ou **Tout bloquer maintenant**, que la montre vous demande de confirmer une fois de plus.",
			"**Demander à Coach** à la voix.",
			"**Une carte** qui indique combien vous attendent.",
			"**L'approbation ne se fait jamais sur la montre.** Pour un appel retenu, une Card, un paiement ou une connexion, la montre affiche « Approuvez sur votre téléphone » et l'ouvre là-bas.",
		},
	},
	Get: watchSection{
		Head: "2. L'obtenir",
		Items: []string{
			"**Wear OS : Bientôt sur Google Play.** Wear OS 3 ou version ultérieure. L'app pour la montre est fournie avec tafs pour Android : dès qu'elle sera sur Google Play, installez-la depuis Google Play sur votre montre.",
			"**Apple Watch : À venir.** Elle viendra avec tafs pour iPhone et fera les mêmes choses, l'approbation se faisant sur votre iPhone.",
			"**Vous ne vous connectez pas sur la montre.** Elle utilise tafs sur votre téléphone, qui doit être connecté et à proximité.",
		},
	},
	Act: watchSection{
		Head: "3. Refuser et bloquer",
		Items: []string{
			"**Votre montre a besoin d'un verrouillage de l'écran, et doit être déverrouillée.** Sinon, elle affiche : « Pour refuser ou bloquer depuis cette montre, définissez un verrouillage de l'écran et déverrouillez-la. »",
			"**Votre téléphone s'en charge,** avec sa propre clé, et la montre affiche le résultat une fois l'action faite, par exemple « Refusé. Rien n'a été envoyé. »",
			"Si quelqu'un a répondu à l'appel entre-temps, la montre affiche « Déjà répondu, ou terminé. »",
		},
	},
	Coach: watchSection{
		Head: "4. Demander à Coach",
		Items: []string{
			"Touchez **Demander à Coach** et parlez. La saisie vocale de votre montre transforme ce que vous dites en texte, et la montre l'affiche avant que quoi que ce soit ne soit envoyé : **Envoyer** ou **Annuler**.",
			"**Coach reçoit vos mots en texte, jamais votre voix.**",
			"D'abord, autorisez l'utilisation de l'IA dans tafs sur votre téléphone, et lancez Coach là-bas.",
			"Une réponse courte s'affiche sur la montre. Une réponse longue, ou qui attend une réponse de votre part, montre son début et **Ouvrir sur le téléphone**.",
			"Comme pour refuser et bloquer, votre montre a besoin d'un verrouillage de l'écran et doit être déverrouillée.",
		},
	},
	Locked: watchSection{
		Head: "5. Quand votre montre est verrouillée",
		Items: []string{
			"La carte n'affiche que « tafs », et une alerte n'affiche que « Un taf vous attend ». Wear OS verrouille votre montre quand vous la retirez de votre poignet.",
			"Déverrouillée, une alerte montre ce que montre celle de votre téléphone : quel taf et ce qu'il demande, ce qui peut nommer un montant, un commerçant ou une personne.",
			"Sur une Apple Watch, les alertes sont celles de votre iPhone : c'est donc le réglage des aperçus de votre iPhone qui décide de ce qu'elles affichent. Avec les aperçus masqués, le réglage de l'iPhone quand il est verrouillé, sauf si vous le changez, une alerte n'affiche que « Un taf vous attend ».",
		},
	},
	Keeps: watchSection{
		Head: "6. Ce qu'il garde",
		Items: []string{
			"Seulement combien vous attendent, pour la carte. Ce n'est jamais affiché tant que votre montre est verrouillée, et c'est alors effacé, comme quand vous vous déconnectez sur votre téléphone.",
			"Aucun identifiant de connexion, aucune clé. Ce qu'il affiche est oublié quand vous le rangez, et rien de ce que vous dites n'est gardé.",
		},
		Link: watchLink{Before: "Les détails sont dans la ", Text: "politique de confidentialité de tafs", After: "."},
	},
	Help: watchSection{
		Head: "7. Si quelque chose ne va pas",
		Items: []string{
			"« Votre téléphone est hors de portée. Rapprochez-le, puis réessayez. »",
			"« Connectez-vous d'abord à tafs sur votre téléphone. »",
		},
		Link: watchLink{Before: "Pour tout le reste : **Ouvrir sur le téléphone**, ou ", Text: "écrivez-nous", After: "."},
	},
}
