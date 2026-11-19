package pages

// watchDE is /tafs/watch's words in German, at /de/tafs/watch. The watch's
// own lines are quoted as tafs says them in German.
var watchDE = watchWords{
	Lang:        "de",
	Title:       "tafs auf deiner Uhr — KIFF",
	Description: "Sieh, was dich braucht, lehne einen zurückgehaltenen Aufruf ab, halte einen taf an und frag Coach von deiner Uhr aus. Genehmigen bleibt auf deinem Handy.",
	Badge:       "tafs · Uhr",
	Head:        "tafs auf deiner Uhr",
	Sub:         "Sieh, was dich braucht, lehne einen zurückgehaltenen Aufruf ab, halte einen taf an und frag Coach, direkt am Handgelenk. Genehmigen bleibt auf deinem Handy.",
	Does: watchSection{
		Head: "1. Was tafs auf der Uhr kann",
		Items: []string{
			"**Braucht dich:** wie viele zurückgehaltene Aufrufe dich brauchen, und jeder so, wie dein Handy ihn zeigt: sein taf, was er will, der Betrag und warum.",
			"**Deine tafs:** der Zustand jedes tafs in einer Zeile.",
			"**Ablehnen** eines zurückgehaltenen Aufrufs, **Jetzt anhalten** für einen taf oder **Alle jetzt anhalten**, was die Uhr dich noch einmal bestätigen lässt.",
			"**Coach fragen** per Sprache.",
			"**Eine Ansicht** mit der Zahl, wie viele dich brauchen.",
			"**Genehmigt wird nie auf der Uhr.** Bei einem zurückgehaltenen Aufruf, einer Card, einer Zahlung oder einer Verbindung sagt die Uhr „Genehmige auf deinem Handy“ und öffnet es dort.",
		},
	},
	Get: watchSection{
		Head: "2. Hol dir tafs auf die Uhr",
		Items: []string{
			"**Wear OS: Bald bei Google Play.** Wear OS 3 oder neuer. Die Uhr-App kommt mit tafs für Android: Sobald sie bei Google Play ist, installiere sie aus Google Play auf deiner Uhr.",
			"**Apple Watch: Kommt noch.** Sie kommt mit tafs für iPhone und kann dasselbe, wobei du auf deinem iPhone genehmigst.",
			"**Keine Anmeldung auf der Uhr.** Sie nutzt tafs auf deinem Handy, das angemeldet und in der Nähe sein muss.",
		},
	},
	Act: watchSection{
		Head: "3. Ablehnen und Anhalten",
		Items: []string{
			"**Deine Uhr braucht eine Displaysperre und muss entsperrt sein.** Sonst sagt sie: „Um mit dieser Uhr abzulehnen oder anzuhalten, richte eine Displaysperre ein und entsperre sie.“",
			"**Dein Handy erledigt es,** mit seinem eigenen Schlüssel, und die Uhr zeigt das Ergebnis, sobald es fertig ist, etwa „Abgelehnt. Nichts wurde gesendet.“",
			"Hat in der Zwischenzeit jemand den Aufruf beantwortet, sagt die Uhr „Schon beantwortet oder beendet.“",
		},
	},
	Coach: watchSection{
		Head: "4. Coach fragen",
		Items: []string{
			"Tippe auf **Coach fragen** und sprich. Die Spracheingabe deiner Uhr macht aus dem, was du sagst, Text, und die Uhr zeigt ihn, bevor etwas gesendet wird: **Senden** oder **Abbrechen**.",
			"**Coach bekommt deine Worte als Text, nie deine Stimme.**",
			"Erlaube zuerst die KI-Nutzung in tafs auf deinem Handy und starte Coach dort.",
			"Eine kurze Antwort wird auf der Uhr gezeigt. Eine lange, oder eine, auf die du etwas antworten sollst, zeigt ihren Anfang und **Auf dem Handy öffnen**.",
			"Wie zum Ablehnen und Anhalten braucht deine Uhr eine Displaysperre und muss entsperrt sein.",
		},
	},
	Locked: watchSection{
		Head: "5. Wenn deine Uhr gesperrt ist",
		Items: []string{
			"Die Ansicht zeigt nur „tafs“, und eine Benachrichtigung nur „Ein taf braucht dich“. Wear OS sperrt deine Uhr, wenn du sie vom Handgelenk nimmst.",
			"Entsperrt zeigt eine Benachrichtigung, was die auf deinem Handy zeigt: welcher taf und was er will, was einen Betrag, einen Händler oder eine Person nennen kann.",
			"Auf einer Apple Watch sind die Benachrichtigungen die deines iPhones, also entscheidet die Vorschau-Einstellung deines iPhones, was sie zeigen. Mit ausgeblendeten Vorschauen, der Einstellung des iPhones, wenn es gesperrt ist, sofern du sie nicht änderst, zeigt eine Benachrichtigung nur „Ein taf braucht dich“.",
		},
	},
	Keeps: watchSection{
		Head: "6. Was die Uhr behält",
		Items: []string{
			"Nur, wie viele dich brauchen, für die Ansicht. Das wird nie gezeigt, solange deine Uhr gesperrt ist, und dann gelöscht, ebenso wenn du dich auf deinem Handy abmeldest.",
			"Keine Anmeldung, keine Schlüssel. Was sie zeigt, ist vergessen, sobald du sie weglegst, und nichts, was du sagst, wird behalten.",
		},
		Link: watchLink{Before: "Die Einzelheiten stehen in der ", Text: "Datenschutzerklärung von tafs", After: "."},
	},
	Help: watchSection{
		Head: "7. Wenn etwas nicht stimmt",
		Items: []string{
			"„Dein Handy ist nicht in Reichweite. Bring es näher und versuch es dann erneut.“",
			"„Melde dich zuerst auf deinem Handy bei tafs an.“",
		},
		Link: watchLink{Before: "Alles andere: **Auf dem Handy öffnen**, oder ", Text: "schreib uns", After: "."},
	},
}
