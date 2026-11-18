package words

// The new-device email in en.
func init() {
	en.NewDeviceEmail = NewDeviceEmail{
		NewDeviceSubject:       "A new sign-in to your KIFF account",
		NewDeviceSubjectTafs:   "A new sign-in to tafs by KIFF",
		NewDeviceIntro:         "Your KIFF account was signed in on a device it hasn't seen before.",
		NewDeviceIntroTafs:     "Your account was signed in from the tafs app on a device it hasn't seen before.",
		NewDeviceWhen:          "When",
		NewDeviceDevice:        "Device",
		NewDeviceBrowser:       "Browser",
		NewDeviceMethod:        "Sign-in method",
		NewDevicePlace:         "Approximate location",
		NewDeviceOn:            func(browser, device string) string { return browser + " on " + device },
		NewDeviceZone:          func(city string) string { return city + " time" },
		NewDeviceNear:          func(place string) string { return "near " + place },
		NewDeviceRow:           func(label, value string) string { return label + ": " + value },
		NewDevicePassword:      "Email and password",
		NewDevicePlaceNote:     "A location from the network is often off by a city or more.",
		NewDeviceWasYou:        "If this was you, there's nothing to do.",
		NewDeviceNotYou:        "If it wasn't you, sign that device out and change your password:",
		NewDeviceNotYouSignOut: "If it wasn't you, sign that device out:",
		NewDeviceSessions:      "Your devices",
		NewDeviceWhy:           "KIFF sends this each time your account signs in on a device it hasn't seen.",
	}
	en.DeviceNames = DeviceNames{
		TafsOnIPhone:        "tafs on iPhone",
		TafsOnIPad:          "tafs on iPad",
		TafsOnAndroidPhone:  "tafs on Android phone",
		TafsOnAndroidTablet: "tafs on Android tablet",
	}
}
