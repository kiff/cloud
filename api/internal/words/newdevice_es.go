package words

// The new-device email in es.
func init() {
	es.NewDeviceEmail = NewDeviceEmail{
		NewDeviceSubject:       "Un nuevo inicio de sesión en tu cuenta de KIFF",
		NewDeviceSubjectTafs:   "Un nuevo inicio de sesión en tafs by KIFF",
		NewDeviceIntro:         "Se inició sesión en tu cuenta de KIFF en un dispositivo que no conocía.",
		NewDeviceIntroTafs:     "Se inició sesión en tu cuenta desde la app tafs en un dispositivo que no conocía.",
		NewDeviceWhen:          "Cuándo",
		NewDeviceDevice:        "Dispositivo",
		NewDeviceBrowser:       "Navegador",
		NewDeviceMethod:        "Método de inicio de sesión",
		NewDevicePlace:         "Ubicación aproximada",
		NewDeviceOn:            func(browser, device string) string { return browser + " en " + device },
		NewDeviceZone:          func(city string) string { return "hora de " + city },
		NewDeviceNear:          func(place string) string { return "cerca de " + place },
		NewDeviceRow:           func(label, value string) string { return label + ": " + value },
		NewDevicePassword:      "Correo y contraseña",
		NewDevicePlaceNote:     "Una ubicación según la red a menudo falla por una ciudad o más.",
		NewDeviceWasYou:        "Si fuiste tú, no tienes que hacer nada.",
		NewDeviceNotYou:        "Si no fuiste tú, cierra la sesión de ese dispositivo y cambia tu contraseña:",
		NewDeviceNotYouSignOut: "Si no fuiste tú, cierra la sesión de ese dispositivo:",
		NewDeviceSessions:      "Tus dispositivos",
		NewDeviceWhy:           "KIFF envía esto cada vez que se inicia sesión en tu cuenta en un dispositivo que no conocía.",
	}
	es.DeviceNames = DeviceNames{
		TafsOnIPhone:        "tafs en iPhone",
		TafsOnIPad:          "tafs en iPad",
		TafsOnAndroidPhone:  "tafs en teléfono Android",
		TafsOnAndroidTablet: "tafs en tableta Android",
	}
}
