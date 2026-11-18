package words

// The new-device email in pl.
func init() {
	pl.NewDeviceEmail = NewDeviceEmail{
		NewDeviceSubject:       "Nowe logowanie na Twoje konto KIFF",
		NewDeviceSubjectTafs:   "Nowe logowanie do tafs by KIFF",
		NewDeviceIntro:         "Na Twoje konto KIFF zalogowano się na urządzeniu, którego wcześniej nie znało.",
		NewDeviceIntroTafs:     "Na Twoje konto zalogowano się z aplikacji tafs na urządzeniu, którego wcześniej nie znało.",
		NewDeviceWhen:          "Kiedy",
		NewDeviceDevice:        "Urządzenie",
		NewDeviceBrowser:       "Przeglądarka",
		NewDeviceMethod:        "Metoda logowania",
		NewDevicePlace:         "Przybliżona lokalizacja",
		NewDeviceOn:            func(browser, device string) string { return browser + " na " + device },
		NewDeviceZone:          func(city string) string { return "czas lokalny: " + city },
		NewDeviceNear:          func(place string) string { return "w pobliżu: " + place },
		NewDeviceRow:           func(label, value string) string { return label + ": " + value },
		NewDevicePassword:      "E-mail i hasło",
		NewDevicePlaceNote:     "Lokalizacja według sieci często myli się o jedno miasto lub więcej.",
		NewDeviceWasYou:        "Jeśli to Ty, nie musisz nic robić.",
		NewDeviceNotYou:        "Jeśli nie, wyloguj to urządzenie i zmień hasło:",
		NewDeviceNotYouSignOut: "Jeśli nie, wyloguj to urządzenie:",
		NewDeviceSessions:      "Twoje urządzenia",
		NewDeviceWhy:           "KIFF wysyła tę wiadomość za każdym razem, gdy Twoje konto loguje się na urządzeniu, którego wcześniej nie znało.",
	}
	pl.DeviceNames = DeviceNames{
		TafsOnIPhone:        "tafs na iPhonie",
		TafsOnIPad:          "tafs na iPadzie",
		TafsOnAndroidPhone:  "tafs na telefonie z Androidem",
		TafsOnAndroidTablet: "tafs na tablecie z Androidem",
	}
}
