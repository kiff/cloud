package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/pages"
)

const testPlay = "https://play.google.com/store/apps/details?id=dev.kiff.tafs"

func getDownload(t *testing.T, appStoreLive bool, ua string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/tafs/download", nil)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	rec := httptest.NewRecorder()
	Download(OfferLinks{PlayURL: testPlay}, appStoreLive).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Vary"), "User-Agent") {
		t.Error("the page varies by device, but doesn't say so to caches")
	}
	return rec.Body.String()
}

const (
	uaIPhone   = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
	uaAndroid  = "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Mobile Safari/537.36"
	uaMac      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"
	uaWindows  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"
	uaLinux    = "Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0"
	uaIPadAsk  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15"
	uaChromeOS = "Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"
)

// One screen: what tafs is, Google Play (live), the App Store only once it's
// live, each app's real requirements, and the price on /pricing. The three
// computers are named, each with its requirement.
func TestDownloadSaysWhatAndWhere(t *testing.T) {
	body := getDownload(t, false, uaMac)
	for _, want := range []string{
		"Get tafs", `href="` + testPlay + `"`, `alt="Google Play"`, "google-play-badge.png",
		"Android 11 or later", "iOS and iPadOS 16 or later", `href="/pricing"`,
		"macOS 12 (Monterey) only", "Windows 10 or 11", "Ubuntu 24.04 or later",
		"Google Play, the Google Play logo, Chrome, Wear OS and Pixel Watch are trademarks of Google LLC.",
		// The Chrome extension: Google's badge, unaltered, linking to the listing, with the listing's own line.
		`href="https://chromewebstore.google.com/detail/tafs/lgdfeikagklmfjobgednngcjjhgknfdl"`,
		`alt="Available in the Chrome Web Store"`, "chrome-web-store-badge.png",
		"Let your tafs work in your own Chrome, only on the sites you allow, with every risky step approved on your phone.",
		"Tux by Larry Ewing, made with The GIMP.", "App Store is a service mark of Apple Inc.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// Until Apple approves the app, the iOS side says it's coming and links
// nowhere; switched on, the badge links the listing.
func TestDownloadLinksTheAppStoreOnlyWhenLive(t *testing.T) {
	off := getDownload(t, false, uaIPhone)
	if strings.Contains(off, "apps.apple.com") || strings.Contains(off, "app-store-badge") || !strings.Contains(off, "Coming to the App Store") {
		t.Error("with the App Store off, the page links it or doesn't say it's coming")
	}
	on := getDownload(t, true, uaIPhone)
	if !strings.Contains(on, `href="https://apps.apple.com/app/id6822760338"`) || !strings.Contains(on, `alt="Download on the App Store"`) || strings.Contains(on, "Coming to the App Store") {
		t.Error("with the App Store on, the badge doesn't link the listing")
	}
}

// firstStore names what the page puts first and big: a store, or a computer's
// data-os.
func firstStore(body string) string {
	i := strings.Index(body, `class="dl-store dl-first`)
	if i < 0 {
		return ""
	}
	rest := body[i:]
	rest = rest[:strings.Index(rest, "</div>")]
	switch {
	case strings.Contains(rest, `data-os="`):
		os := rest[strings.Index(rest, `data-os="`)+len(`data-os="`):]
		return os[:strings.Index(os, `"`)]
	case strings.Contains(rest, "google-play-badge"):
		return "android"
	}
	return "ios"
}

// A phone sees its own store first and bigger. The computers keep one order
// for every visitor, macOS 13 and later first and big, so a computer, or a device the page can't name, sees it first.
func TestDownloadPutsTheVisitorsDeviceFirst(t *testing.T) {
	for _, c := range []struct {
		name, ua, want string
		bigs           int
	}{
		{"Android", uaAndroid, "android", 2},
		{"iPhone", uaIPhone, "ios", 2},
		{"Mac", uaMac, "macos13", 1},
		{"Windows", uaWindows, "macos13", 1},
		{"Linux", uaLinux, "macos13", 1},
		{"an iPad asking for the desktop site", uaIPadAsk, "macos13", 1},
		{"a Chromebook", uaChromeOS, "macos13", 1},
		{"no User-Agent", "", "macos13", 1},
	} {
		body := getDownload(t, false, c.ua)
		if got := firstStore(body); got != c.want {
			t.Errorf("%s: first is %q, want %q", c.name, got, c.want)
		}
		// One big store on a phone, and macOS 13 and later among the computers.
		if n := strings.Count(body, "dl-first"); n != c.bigs {
			t.Errorf("%s: %d apps are big, want %d", c.name, n, c.bigs)
		}
	}
}

// A phone sees the stores before the computers and no QR code. A computer
// sees the computers before the stores, and the QR code that opens the page
// on a phone.
func TestDownloadOrdersTheGroupsByDevice(t *testing.T) {
	const computers, phones = ">Computer</h2>", ">Phone and tablet</h2>"
	for _, ua := range []string{uaAndroid, uaIPhone} {
		body := getDownload(t, false, ua)
		if c, p := strings.Index(body, computers), strings.Index(body, phones); p < 0 || c < p {
			t.Errorf("%s: the stores should come before the computers", ua)
		}
		if strings.Contains(body, "qr-download.svg") {
			t.Error("a phone doesn't need the QR code")
		}
	}
	for _, ua := range []string{uaMac, uaWindows, uaLinux, ""} {
		body := getDownload(t, false, ua)
		if c, p := strings.Index(body, computers), strings.Index(body, phones); c < 0 || p < c {
			t.Errorf("%q: the computers should come before the stores", ua)
		}
		if !strings.Contains(body, "qr-download.svg") {
			t.Errorf("%q: a computer should see the QR code", ua)
		}
	}
}

// No computer's app is released, so each one says so and the page links no
// file: never a button that downloads nothing.
func TestDownloadSaysAComputersAppIsNotAvailableYet(t *testing.T) {
	for _, ua := range []string{uaMac, uaWindows, uaLinux, uaAndroid} {
		body := getDownload(t, false, ua)
		if n := strings.Count(body, " dl-pending"); n != 4 || strings.Contains(body, ">Coming soon<") {
			t.Errorf("%q: %d apps say they aren't available yet, want 4", ua, n)
		}
		for _, os := range []string{"macos13", "macos", "windows", "linux"} {
			if !strings.Contains(body, `data-os="`+os+`"`) {
				t.Errorf("%q: %s is missing", ua, os)
			}
		}
		if strings.Contains(body, "Download for ") {
			t.Errorf("%q: the page offers a download that doesn't exist", ua)
		}
	}
}

// /<lang>/tafs/download is the same page in each language: the visitor's
// device still comes first, the App Store follows the same switch, and a
// cache keeps each device apart. /support is served in each language too.
// comingSoon is how each language says a computer's app is coming.
var comingSoon = map[string]string{
	"de": "Bald verfügbar", "es": "Muy pronto", "fr": "Bientôt disponible", "it": "In arrivo",
	"nl": "Binnenkort beschikbaar", "pl": "Wkrótce dostępne", "pt": "Em breve",
}

func TestDownloadAndSupportInEveryLanguage(t *testing.T) {
	for _, lang := range []string{"de", "es", "fr", "it", "nl", "pl", "pt"} {
		for _, live := range []bool{false, true} {
			req := httptest.NewRequest(http.MethodGet, "/"+lang+"/tafs/download", nil)
			req.Header.Set("User-Agent", uaIPhone)
			rec := httptest.NewRecorder()
			CanonicalPath(Translated(lang, "/tafs/download", OfferLinks{PlayURL: testPlay, AppStoreLive: live})).ServeHTTP(rec, req)
			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Fatalf("/%s/tafs/download: status %d", lang, rec.Code)
			}
			if rec.Header().Get("Vary") != "User-Agent" {
				t.Errorf("/%s/tafs/download: Vary %q", lang, rec.Header().Get("Vary"))
			}
			// A phone sees the stores first, and no QR code.
			if strings.Contains(body, "qr-download.svg") || strings.Index(body, `data-os="macos"`) < strings.Index(body, "dl-badge dl-play") {
				t.Errorf("/%s/tafs/download: an iPhone doesn't see the stores first", lang)
			}
			if got := strings.Contains(body, `href="`+pages.AppStoreURL+`"`); got != live {
				t.Errorf("/%s/tafs/download: App Store linked = %v with the listing live = %v", lang, got, live)
			}
			// No computer's app is released: each of the four is shown, dimmer,
			// with no file offered and no "coming soon" (the owner's one-page rule).
			if n := strings.Count(body, " dl-pending"); n != 4 || strings.Contains(body, `<p class="dl-soon">`+comingSoon[lang]+`</p>`) {
				t.Errorf("/%s/tafs/download: %d computers wait for their file, want 4, none saying %q", lang, n, comingSoon[lang])
			}
			if strings.Contains(body, ".exe") || strings.Contains(body, ".dmg") || strings.Contains(body, ".AppImage") {
				t.Errorf("/%s/tafs/download: a computer's file is offered", lang)
			}
			if !strings.Contains(body, `<html lang="`+lang+`"`) || strings.Contains(body, `class="translation-notice"`) {
				t.Errorf("/%s/tafs/download: not marked %s, or carries the legal notice", lang, lang)
			}
		}
		rec := getPage(t, Translated(lang, "/support", OfferLinks{}), "/"+lang+"/support")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hello@kiff.dev") {
			t.Errorf("/%s/support: status %d, or no hello@kiff.dev", lang, rec.Code)
		}
	}
}
