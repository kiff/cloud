package auth

import "strings"

// The kinds of device KIFF names in a person's list (kiff/tafs's Account →
// Devices): what the device is, not what it's called, so its name is drawn
// in the reader's language when the list is read, never fixed in the
// language the app had at sign-in. DeviceOther is any other, or one KIFF
// can't tell: it shows by the name it gave.
const (
	DeviceIPhone        = "iphone"
	DeviceIPad          = "ipad"
	DeviceAndroidPhone  = "android_phone"
	DeviceAndroidTablet = "android_tablet"
	DeviceOther         = "other"
)

var deviceKinds = map[string]bool{DeviceIPhone: true, DeviceIPad: true, DeviceAndroidPhone: true, DeviceAndroidTablet: true, DeviceOther: true}

// ValidDeviceKind reports whether k is a kind KIFF knows.
func ValidDeviceKind(k string) bool { return deviceKinds[k] }

// tabletWords are "tablet" in the eight languages' names the apps gave
// ("Android tablet", "tablette Android", "tableta", "tablecie").
var tabletWords = []string{"tablet", "tablecie"}

// DeviceKindOf is a device's kind as it sent it, or, when it sent none
// KIFF knows, the kind its name says: the names the apps gave before they
// sent a kind, in any of their languages ("tafs on iPhone", "tafs en iPad",
// "tafs sur Android appareil", "tafs auf Android-Tablet"). A name that says
// none is DeviceOther, and shows as it is.
func DeviceKindOf(sent, name string) string {
	if ValidDeviceKind(sent) {
		return sent
	}
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "ipad"):
		return DeviceIPad
	case strings.Contains(lower, "iphone"):
		return DeviceIPhone
	case strings.Contains(lower, "android"):
		for _, w := range tabletWords {
			if strings.Contains(lower, w) {
				return DeviceAndroidTablet
			}
		}
		return DeviceAndroidPhone
	}
	return DeviceOther
}
