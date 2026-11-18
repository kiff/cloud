package words

// The new-device email in it.
func init() {
	it.NewDeviceEmail = NewDeviceEmail{
		NewDeviceSubject:       "Un nuovo accesso al tuo account KIFF",
		NewDeviceSubjectTafs:   "Un nuovo accesso a tafs by KIFF",
		NewDeviceIntro:         "È stato effettuato l'accesso al tuo account KIFF su un dispositivo che non conosceva.",
		NewDeviceIntroTafs:     "È stato effettuato l'accesso al tuo account dall'app tafs su un dispositivo che non conosceva.",
		NewDeviceWhen:          "Quando",
		NewDeviceDevice:        "Dispositivo",
		NewDeviceBrowser:       "Browser web",
		NewDeviceMethod:        "Metodo di accesso",
		NewDevicePlace:         "Posizione approssimativa",
		NewDeviceOn:            func(browser, device string) string { return browser + " su " + device },
		NewDeviceZone:          func(city string) string { return "ora di " + city },
		NewDeviceNear:          func(place string) string { return "vicino a " + place },
		NewDeviceRow:           func(label, value string) string { return label + ": " + value },
		NewDevicePassword:      "Email e password",
		NewDevicePlaceNote:     "Una posizione secondo la rete spesso sbaglia di una città o più.",
		NewDeviceWasYou:        "Se sei stato tu, non devi fare nulla.",
		NewDeviceNotYou:        "Altrimenti, disconnetti quel dispositivo e cambia la password:",
		NewDeviceNotYouSignOut: "Altrimenti, disconnetti quel dispositivo:",
		NewDeviceSessions:      "I tuoi dispositivi",
		NewDeviceWhy:           "KIFF invia questo messaggio ogni volta che si accede al tuo account da un dispositivo che non conosceva.",
	}
	it.DeviceNames = DeviceNames{
		TafsOnIPhone:        "tafs su iPhone",
		TafsOnIPad:          "tafs su iPad",
		TafsOnAndroidPhone:  "tafs su telefono Android",
		TafsOnAndroidTablet: "tafs su tablet Android",
	}
}
