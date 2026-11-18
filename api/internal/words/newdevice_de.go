package words

// The new-device email in de.
func init() {
	de.NewDeviceEmail = NewDeviceEmail{
		NewDeviceSubject:       "Eine neue Anmeldung bei deinem KIFF-Konto",
		NewDeviceSubjectTafs:   "Eine neue Anmeldung bei tafs by KIFF",
		NewDeviceIntro:         "Dein KIFF-Konto wurde auf einem Gerät angemeldet, das es noch nicht kannte.",
		NewDeviceIntroTafs:     "Dein Konto wurde über die tafs-App auf einem Gerät angemeldet, das es noch nicht kannte.",
		NewDeviceWhen:          "Wann",
		NewDeviceDevice:        "Gerät",
		NewDeviceBrowser:       "Webbrowser",
		NewDeviceMethod:        "Anmeldemethode",
		NewDevicePlace:         "Ungefährer Ort",
		NewDeviceOn:            func(browser, device string) string { return browser + " auf " + device },
		NewDeviceZone:          func(city string) string { return "Ortszeit " + city },
		NewDeviceNear:          func(place string) string { return "in der Nähe von " + place },
		NewDeviceRow:           func(label, value string) string { return label + ": " + value },
		NewDevicePassword:      "E-Mail und Passwort",
		NewDevicePlaceNote:     "Ein Ort laut Netzwerk liegt oft eine Stadt oder mehr daneben.",
		NewDeviceWasYou:        "Wenn du das warst, ist nichts zu tun.",
		NewDeviceNotYou:        "Wenn nicht, melde dieses Gerät ab und ändere dein Passwort:",
		NewDeviceNotYouSignOut: "Wenn nicht, melde dieses Gerät ab:",
		NewDeviceSessions:      "Deine Geräte",
		NewDeviceWhy:           "KIFF sendet das jedes Mal, wenn dein Konto auf einem Gerät angemeldet wird, das es noch nicht kannte.",
	}
	de.DeviceNames = DeviceNames{
		TafsOnIPhone:        "tafs auf dem iPhone",
		TafsOnIPad:          "tafs auf dem iPad",
		TafsOnAndroidPhone:  "tafs auf dem Android-Telefon",
		TafsOnAndroidTablet: "tafs auf dem Android-Tablet",
	}
}
