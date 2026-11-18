package words

// The new-device email in pt.
func init() {
	pt.NewDeviceEmail = NewDeviceEmail{
		NewDeviceSubject:       "Um novo início de sessão na sua conta KIFF",
		NewDeviceSubjectTafs:   "Um novo início de sessão no tafs by KIFF",
		NewDeviceIntro:         "Foi iniciada sessão na sua conta KIFF num dispositivo que ainda não conhecia.",
		NewDeviceIntroTafs:     "Foi iniciada sessão na sua conta a partir da app tafs num dispositivo que ainda não conhecia.",
		NewDeviceWhen:          "Quando",
		NewDeviceDevice:        "Dispositivo",
		NewDeviceBrowser:       "Navegador",
		NewDeviceMethod:        "Método de início de sessão",
		NewDevicePlace:         "Localização aproximada",
		NewDeviceOn:            func(browser, device string) string { return browser + " em " + device },
		NewDeviceZone:          func(city string) string { return "hora de " + city },
		NewDeviceNear:          func(place string) string { return "perto de " + place },
		NewDeviceRow:           func(label, value string) string { return label + ": " + value },
		NewDevicePassword:      "E-mail e palavra-passe",
		NewDevicePlaceNote:     "Uma localização segundo a rede falha muitas vezes por uma cidade ou mais.",
		NewDeviceWasYou:        "Se foi o próprio, não precisa de fazer nada.",
		NewDeviceNotYou:        "Caso contrário, termine a sessão nesse dispositivo e altere a sua palavra-passe:",
		NewDeviceNotYouSignOut: "Caso contrário, termine a sessão nesse dispositivo:",
		NewDeviceSessions:      "Os seus dispositivos",
		NewDeviceWhy:           "A KIFF envia isto sempre que a sua conta inicia sessão num dispositivo que ainda não conhecia.",
	}
	pt.DeviceNames = DeviceNames{
		TafsOnIPhone:        "tafs no iPhone",
		TafsOnIPad:          "tafs no iPad",
		TafsOnAndroidPhone:  "tafs no telemóvel Android",
		TafsOnAndroidTablet: "tafs no tablet Android",
	}
}
