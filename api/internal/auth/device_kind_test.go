package auth_test

import (
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
)

// A kind the device sent wins; without one KIFF knows, the names the apps
// gave in any of their languages say it, and any other name is "other".
func TestDeviceKindOf(t *testing.T) {
	for _, c := range []struct{ sent, name, want string }{
		{"ipad", "tafs on iPhone", "ipad"},
		{"android_tablet", "", "android_tablet"},
		{"other", "tafs on iPad", "other"},
		{"", "tafs on iPhone", "iphone"},
		{"", "tafs en iPad", "ipad"},
		{"", "tafs on Android", "android_phone"},
		{"", "tafs on Android phone", "android_phone"},
		{"", "tafs sur Android appareil", "android_phone"},
		{"", "tafs auf Android-Tablet", "android_tablet"},
		{"", "tafs na tablet z Androidem", "android_tablet"},
		{"foldable", "tafs op Android-telefoon", "android_phone"},
		{"", "Ana's laptop", "other"},
		{"", "", "other"},
	} {
		if got := auth.DeviceKindOf(c.sent, c.name); got != c.want {
			t.Errorf("DeviceKindOf(%q, %q) = %q; want %q", c.sent, c.name, got, c.want)
		}
	}
}
