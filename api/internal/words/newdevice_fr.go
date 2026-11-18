package words

// The new-device email in fr.
func init() {
	fr.NewDeviceEmail = NewDeviceEmail{
		NewDeviceSubject:       "Une nouvelle connexion à votre compte KIFF",
		NewDeviceSubjectTafs:   "Une nouvelle connexion à tafs by KIFF",
		NewDeviceIntro:         "Votre compte KIFF a été connecté sur un appareil qu'il ne connaissait pas.",
		NewDeviceIntroTafs:     "Votre compte a été connecté depuis l'app tafs sur un appareil qu'il ne connaissait pas.",
		NewDeviceWhen:          "Quand",
		NewDeviceDevice:        "Appareil",
		NewDeviceBrowser:       "Navigateur",
		NewDeviceMethod:        "Méthode de connexion",
		NewDevicePlace:         "Emplacement approximatif",
		NewDeviceOn:            func(browser, device string) string { return browser + " sur " + device },
		NewDeviceZone:          func(city string) string { return "heure de " + city },
		NewDeviceNear:          func(place string) string { return "près de " + place },
		NewDeviceRow:           func(label, value string) string { return label + " : " + value },
		NewDevicePassword:      "E-mail et mot de passe",
		NewDevicePlaceNote:     "Un emplacement d'après le réseau se trompe souvent d'une ville ou plus.",
		NewDeviceWasYou:        "Si c'était vous, il n'y a rien à faire.",
		NewDeviceNotYou:        "Sinon, déconnectez cet appareil et changez votre mot de passe :",
		NewDeviceNotYouSignOut: "Sinon, déconnectez cet appareil :",
		NewDeviceSessions:      "Vos appareils",
		NewDeviceWhy:           "KIFF envoie ceci chaque fois que votre compte se connecte sur un appareil qu'il ne connaissait pas.",
	}
	fr.DeviceNames = DeviceNames{
		TafsOnIPhone:        "tafs sur iPhone",
		TafsOnIPad:          "tafs sur iPad",
		TafsOnAndroidPhone:  "tafs sur téléphone Android",
		TafsOnAndroidTablet: "tafs sur tablette Android",
	}
}
