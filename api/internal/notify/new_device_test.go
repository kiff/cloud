package notify

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/words"
)

func paris(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func sampleNewDevice(t *testing.T) NewDevice {
	return NewDevice{Device: "Android", Browser: "Chrome", Method: "password", Password: true, City: "Haguenau", Country: "FR",
		At: time.Date(2026, 11, 10, 20, 34, 0, 0, time.UTC), Zone: paris(t),
		SessionsURL: "https://app.kiff.dev/dashboard/settings#sessions", SiteURL: "https://kiff.dev"}
}

// tags drops the HTML's tags and collapses its spaces, to read it as text.
func tags(html string) string {
	return strings.Join(strings.Fields(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(html, " ")), " ")
}

// One line, then the details: the time in the account's zone and named,
// the browser on the device, the method, and the place as a guess. Both
// parts say the same, and the HTML is one a mail app renders: tables and
// inline styles, no script, no stylesheet.
func TestNewDeviceEmailDetails(t *testing.T) {
	subject, text, html, err := renderNewDevice(sampleNewDevice(t))
	if err != nil {
		t.Fatal(err)
	}
	en := words.Of(words.EN)
	rows := [][2]string{
		{"When", "10 Nov 2026, 21:34 (Paris time)"},
		{"Device", "Chrome on Android"},
		{"Sign-in method", "Email and password"},
		{"Approximate location", "near Haguenau, France"},
	}
	for _, r := range rows {
		if !strings.Contains(text, r[0]+": "+r[1]) || !strings.Contains(tags(html), r[0]+" "+r[1]) {
			t.Errorf("no %s: %s\n%s\n%s", r[0], r[1], text, html)
		}
	}
	for _, want := range []string{en.NewDeviceIntro, en.NewDevicePlaceNote, en.NewDeviceNotYou, en.NewDeviceWasYou, "https://app.kiff.dev/dashboard/settings#sessions"} {
		if !strings.Contains(text, want) || !strings.Contains(html, strings.ReplaceAll(want, "'", "&#39;")) {
			t.Errorf("no %q in both parts", want)
		}
	}
	if subject != en.NewDeviceSubject || strings.Contains(text, "UTC") || strings.Contains(html, "tafs") || !strings.Contains(html, "kiff-logo-transparent-512.png") {
		t.Errorf("subject %q\n%s", subject, text)
	}
	for _, never := range []string{"<script", "<style", "<link", "class=", "unknown", "Unknown"} {
		if strings.Contains(html, never) || strings.Contains(text, never) {
			t.Errorf("the email has %q", never)
		}
	}
	if !strings.Contains(html, `role="presentation"`) {
		t.Error("the HTML isn't laid out in tables")
	}
}

// The time is the account's wall clock on either side of a change of
// clocks, and UTC is named as UTC.
func TestNewDeviceEmailTimeInTheAccountsZone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		at   time.Time
		zone *time.Location
		want string
	}{
		// Paris left summer time on 25 Oct 2026: 01:30 UTC is 03:30 before
		// it, and 02:30 after.
		{time.Date(2026, 10, 24, 1, 30, 0, 0, time.UTC), paris(t), "24 Oct 2026, 03:30 (Paris time)"},
		{time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC), paris(t), "25 Oct 2026, 02:30 (Paris time)"},
		// New York, a day behind UTC here, and a city of two words.
		{time.Date(2026, 3, 9, 3, 15, 0, 0, time.UTC), ny, "8 Mar 2026, 23:15 (New York time)"},
		{time.Date(2026, 11, 10, 20, 34, 0, 0, time.UTC), nil, "10 Nov 2026, 20:34 (UTC)"},
		{time.Date(2026, 11, 10, 20, 34, 0, 0, time.UTC), time.UTC, "10 Nov 2026, 20:34 (UTC)"},
	} {
		d := sampleNewDevice(t)
		d.At, d.Zone = c.at, c.zone
		_, text, html, err := renderNewDevice(d)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text, "When: "+c.want) || !strings.Contains(tags(html), "When "+c.want) {
			t.Errorf("want %q\n%s", c.want, text)
		}
	}
	// In German, the zone and the date are German.
	d := sampleNewDevice(t)
	d.Lang = words.DE
	_, text, _, _ := renderNewDevice(d)
	if want := "Wann: " + words.Of(words.DE).DateTimeIn(d.At, d.Zone) + " (Ortszeit Paris)"; !strings.Contains(text, want) || !strings.Contains(want, "21:34") {
		t.Errorf("German time:\n%s", text)
	}
}

// A sign-in with Google shows Google and never asks for a new password; a
// detail the provider didn't give has no row in either part.
func TestNewDeviceEmailLeavesOutWhatIsntKnown(t *testing.T) {
	en := words.Of(words.EN)
	d := sampleNewDevice(t)
	d.Method, d.Password = "Google", false
	_, text, html, err := renderNewDevice(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Sign-in method: Google") || strings.Contains(text, "password") || strings.Contains(html, "password") ||
		!strings.Contains(text, en.NewDeviceNotYouSignOut) {
		t.Errorf("Google:\n%s", text)
	}

	d = sampleNewDevice(t)
	d.Device, d.Method, d.City, d.Country = "", "", "", ""
	_, text, html, _ = renderNewDevice(d)
	if !strings.Contains(text, "Browser: Chrome") || strings.Contains(text, "Device:") || strings.Contains(text, "Sign-in method") ||
		strings.Contains(text, "location") || strings.Contains(text, en.NewDevicePlaceNote) {
		t.Errorf("no device, method or place:\n%s", text)
	}
	if strings.Count(html, "<tr><td width=") != 2 {
		t.Errorf("rows: HTML %d, text %q", strings.Count(html, "<tr><td width="), text)
	}

	d = sampleNewDevice(t)
	d.Browser, d.Country = "", ""
	_, text, _, _ = renderNewDevice(d)
	if !strings.Contains(text, "Device: Android") || !strings.Contains(text, "Approximate location: near Haguenau\n") {
		t.Errorf("a device without a browser, a city without a country:\n%s", text)
	}
}

// Through the tafs app it says "tafs by KIFF", with both logos.
func TestNewDeviceEmailThroughTafs(t *testing.T) {
	d := sampleNewDevice(t)
	d.Tafs = true
	subject, _, html, err := renderNewDevice(d)
	if err != nil {
		t.Fatal(err)
	}
	if subject != words.Of(words.EN).NewDeviceSubjectTafs || !strings.Contains(html, TafsByKIFF) || !strings.Contains(html, "tafs-icon-512.png") ||
		!strings.Contains(html, "kiff-logo-transparent-512.png") {
		t.Errorf("through tafs: %q\n%s", subject, html)
	}
}

// In the person's language, the country named in it, and the provider's
// words are escaped.
func TestNewDeviceEmailInTheLanguageAndEscaped(t *testing.T) {
	d := sampleNewDevice(t)
	d.Lang, d.Device = words.DE, `<img src=x onerror=alert(1)>`
	subject, text, html, err := renderNewDevice(d)
	if err != nil {
		t.Fatal(err)
	}
	de := words.Of(words.DE)
	if subject != de.NewDeviceSubject || !strings.Contains(text, de.NewDeviceNear("Haguenau, Frankreich")) || strings.Contains(html, "<img src=x") {
		t.Errorf("German, escaped: %q\n%s\n%s", subject, text, html)
	}
	if _, _, _, err := renderNewDevice(NewDevice{}); err == nil {
		t.Error("an email with no sessions link was rendered")
	}
}

// A country alone is said as it is, never "near" it; French writes its
// colons with a space before them.
func TestNewDeviceEmailCountryAloneInFrench(t *testing.T) {
	d := sampleNewDevice(t)
	d.Lang, d.City, d.Method, d.Password = words.FR, "", "Google", false
	_, text, html, err := renderNewDevice(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Emplacement approximatif : France\n") || strings.Contains(text, "près de") || strings.Contains(html, "près de") ||
		!strings.Contains(text, "Quand : ") || !strings.Contains(text, "Méthode de connexion : Google") {
		t.Errorf("French, country alone:\n%s", text)
	}
	d.Lang = words.EN
	_, text, _, _ = renderNewDevice(d)
	if !strings.Contains(text, "Approximate location: France\n") || strings.Contains(text, "near") {
		t.Errorf("English, country alone:\n%s", text)
	}
}
