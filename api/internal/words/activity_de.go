package words

// German Activity lines (du).
func init() {
	why := func(w string) string {
		if w == "" {
			return ""
		}
		return ": " + w
	}
	de.Activity = Activity{
		ByYou: "von dir", BySomeone: "von einer Person im Konto",
		ByRole:      map[string]string{"admin": "von einem Admin", "editor": "von einem Bearbeiter", "approver": "von einem Genehmiger", "owner": "vom Inhaber"},
		ByOtherRole: func(string) string { return "von einer Person im Konto" },

		WaitingForYou:  "Wartet auf dich",
		NotSent:        "Nicht gesendet",
		NotSentBecause: func(reason string) string { return "Nicht gesendet · " + reason },
		ApprovedOnceBy: func(by string) string { return "Einmal genehmigt " + by },
		ExtraRoomBy:    func(by string) string { return "Zusätzlicher Spielraum " + by },
		RefusedBy:      func(by string) string { return "Abgelehnt " + by },

		AllowedByCard:    "Von seiner Card erlaubt",
		ApprovedBy:       func(by string) string { return "Genehmigt " + by },
		ApprovedThenDone: func(by string) string { return "Genehmigt " + by + ", dann erledigt" },
		DoneWithinCard:   "Erledigt, im Rahmen seiner Card",
		ConnectionRefused: func(w string) string {
			return "Nicht gesendet: Die Verbindung hat es vor dem Senden abgelehnt" + why(w) + ". Es zählt weiter zu den heutigen Limits, bis KIFF bestätigen kann, dass nichts getan wurde."
		},
		AppRefusedCounts: func(w string) string {
			return "Die App hat es abgelehnt, und es zählt weiter zu den heutigen Limits" + why(w)
		},
		AppRefusedMoney: "Die App hat es abgelehnt. Es zählt zur heutigen Summe, bis KIFF bestätigen kann, dass kein Geld geflossen ist.",
		AppRefusedOther: "Die App hat es abgelehnt. Es zählt zur heutigen Summe, bis KIFF bestätigen kann, dass nichts getan wurde.",
		NotSentToTool: func(allowed, w string) string {
			return allowed + ", aber es konnte nicht gesendet werden: Nichts hat das Tool erreicht" + why(w)
		},
		Sending: func(allowed string) string { return allowed + ", und gesendet: wartet auf die Antwort des Tools" },
		AnswerLost: func(allowed string) string {
			return allowed + ", und gesendet, aber die Antwort des Tools ging verloren: Prüfe die eigenen Aufzeichnungen des Tools"
		},
		FoundAtVendor: func(allowed string) string {
			return allowed + ", und gesendet. KIFF konnte nicht sagen, wie es ausging, hat in den eigenen Aufzeichnungen des Tools nachgesehen und es dort gefunden: Es ist durchgegangen"
		},
		NotRecorded: func(allowed string) string {
			return allowed + ", aber ob das Tool es getan hat, wurde nicht festgehalten: Prüfe die eigenen Aufzeichnungen des Tools"
		},
		ConnectionWhy: map[string]string{
			"halt_unknown":           "sie konnte nicht prüfen, ob der Agent angehalten war",
			"halted":                 "der Agent oder das Konto ist angehalten",
			"halted_before":          "der Aufruf wurde vor dem letzten Anhalten genehmigt",
			"not_granted":            "die Verbindung umfasst diese Aktion nicht",
			"permit_invalid":         "die Freigabe von KIFF passte nicht zum Aufruf",
			"permit_expired":         "die Freigabe von KIFF ist abgelaufen, bevor der Aufruf losging",
			"bad_arguments":          "die Argumente des Aufrufs konnten nicht gelesen werden",
			"record_unavailable":     "sie konnte keinen Eintrag über den Aufruf speichern",
			"profile_not_applied":    "KIFF hat die genehmigten Einstellungen noch nicht übernommen",
			"definition_invalid":     "die Aktion nennt ein Feld, das die App nicht annimmt",
			"definition_unread":      "sie konnte die Aktion nicht mit der App abgleichen",
			"operation_changed":      "die App hat diese Aktion seit der Genehmigung geändert; die Verbindung sendet nichts, bis sie erneut verbunden ist",
			"signed_out":             "die App hat die Anmeldung beendet; die Verbindung sendet nichts, bis du dich erneut anmeldest",
			"app_closed":             "diese App wird nicht mehr angeboten, daher ist ihre Verbindung geschlossen",
			"connection_unavailable": "die Verbindung oder ihr Konto bei der App ließ sich gerade nicht nutzen",
			"tool_not_served":        "diese Aktion wird für diese App nie ausgeführt",
			"waits_for_key":          "diese Aktion braucht den Schlüssel der App, und er wurde noch nicht hinzugefügt",
			"waits_for_approval":     "diese Aktion wurde zur Verbindung hinzugefügt und wartet noch auf deine Genehmigung",
		},
	}
}
