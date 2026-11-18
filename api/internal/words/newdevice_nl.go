package words

// The new-device email in nl.
func init() {
	nl.NewDeviceEmail = NewDeviceEmail{
		NewDeviceSubject:       "Een nieuwe inlog op je KIFF-account",
		NewDeviceSubjectTafs:   "Een nieuwe inlog op tafs by KIFF",
		NewDeviceIntro:         "Er is ingelogd op je KIFF-account op een apparaat dat het nog niet kende.",
		NewDeviceIntroTafs:     "Er is via de tafs-app ingelogd op je account op een apparaat dat het nog niet kende.",
		NewDeviceWhen:          "Wanneer",
		NewDeviceDevice:        "Apparaat",
		NewDeviceBrowser:       "Webbrowser",
		NewDeviceMethod:        "Inlogmethode",
		NewDevicePlace:         "Geschatte locatie",
		NewDeviceOn:            func(browser, device string) string { return browser + " op " + device },
		NewDeviceZone:          func(city string) string { return "tijd in " + city },
		NewDeviceNear:          func(place string) string { return "in de buurt van " + place },
		NewDeviceRow:           func(label, value string) string { return label + ": " + value },
		NewDevicePassword:      "E-mail en wachtwoord",
		NewDevicePlaceNote:     "Een locatie volgens het netwerk zit er vaak een stad of meer naast.",
		NewDeviceWasYou:        "Was jij dit, dan hoef je niets te doen.",
		NewDeviceNotYou:        "Zo niet, log dat apparaat uit en wijzig je wachtwoord:",
		NewDeviceNotYouSignOut: "Zo niet, log dat apparaat uit:",
		NewDeviceSessions:      "Je apparaten",
		NewDeviceWhy:           "KIFF stuurt dit elke keer dat er op je account wordt ingelogd op een apparaat dat het nog niet kende.",
	}
	nl.DeviceNames = DeviceNames{
		TafsOnIPhone:        "tafs op iPhone",
		TafsOnIPad:          "tafs op iPad",
		TafsOnAndroidPhone:  "tafs op Android-telefoon",
		TafsOnAndroidTablet: "tafs op Android-tablet",
	}
}
