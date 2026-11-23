package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// Once a computer's app has a file, the page links it and stops saying it
// isn't available; the others still say so.
func TestDownloadLinksAComputersAppOnceReleased(t *testing.T) {
	apps := DesktopApps()
	for i := range apps {
		if apps[i].URL != "" {
			t.Fatalf("%s has a file, so this test's start is wrong", apps[i].Name)
		}
		if apps[i].OS == "windows" {
			apps[i].URL = "https://kiff.dev/download/tafs-windows.msix"
		}
	}
	var buf bytes.Buffer
	if err := Download(DownloadView{Device: "windows", Desktop: apps}).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	if !strings.Contains(body, `href="https://kiff.dev/download/tafs-windows.msix"`) || !strings.Contains(body, "Download for Windows") {
		t.Error("the released app isn't linked")
	}
	if n := strings.Count(body, " dl-pending"); n != 3 || strings.Contains(body, ">Coming soon<") {
		t.Errorf("%d apps say they aren't available yet, want 3", n)
	}
	if strings.Contains(body, "Download for macOS") || strings.Contains(body, "Download for Linux") {
		t.Error("an app with no file is offered")
	}
}

// The computers keep one order for every visitor: macOS 13 and later, then
// Windows, then Linux, and the macOS 12 app last.
func TestDesktopFirst(t *testing.T) {
	names := func(v DownloadView) string {
		var out []string
		for _, a := range v.desktopFirst() {
			out = append(out, a.OS)
		}
		return strings.Join(out, " ")
	}
	for device, want := range map[string]string{
		"linux":   "macos13 windows linux macos",
		"windows": "macos13 windows linux macos",
		"macos":   "macos13 windows linux macos",
		"ios":     "macos13 windows linux macos",
		"":        "macos13 windows linux macos",
	} {
		if got := names(DownloadView{Device: device, Desktop: DesktopApps()}); got != want {
			t.Errorf("%q: %s, want %s", device, got, want)
		}
	}
}

// The watches show before they ship, each with how it will arrive, linking
// nowhere; Wear OS links its Play listing once the watch is live
// (KIFF_WATCH_LIVE), Apple Watch with the iPhone app (KIFF_APP_STORE_LIVE).
func TestTheWatchesShowHowEachWillArrive(t *testing.T) {
	render := func(v DownloadView) string {
		var buf bytes.Buffer
		if err := Download(v).Render(context.Background(), &buf); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	defer func(was bool) { WatchLive = was }(WatchLive)
	WatchLive = false
	off := render(DownloadView{PlayURL: "https://play.example/tafs", Desktop: DesktopApps()})
	for _, want := range []string{"Wear OS", WatchesSub, "Soon on Google Play", "Apple Watch", "Comes with the iPhone app", `aria-disabled="true"`} {
		if !strings.Contains(off, want) {
			t.Errorf("before they ship: missing %q", want)
		}
	}
	if strings.Contains(off, "dl-watch-live") {
		t.Error("a watch links somewhere before it ships")
	}
	WatchLive = true
	on := render(DownloadView{PlayURL: "https://play.example/tafs", AppStoreLive: true, Desktop: DesktopApps()})
	if strings.Count(on, "dl-watch-live") != 2 || strings.Contains(on, "Soon on Google Play") || strings.Contains(on, "Comes with the iPhone app") {
		t.Error("live watches don't link their stores, or still say they're coming")
	}
	if !strings.Contains(on, `class="dl-watch dl-watch-live dl-os-wearos" href="https://play.example/tafs"`) {
		t.Error("Wear OS doesn't link the Play listing")
	}
	// In another language, the state lines are that language's.
	WatchLive = false
	de := render(DownloadView{Desktop: DesktopApps(), W: downloadDE})
	for _, want := range []string{"Bald bei Google Play", "Kommt mit der iPhone-App", "google-play-badge-de.png", "Linux® ist eine eingetragene Marke"} {
		if !strings.Contains(de, want) {
			t.Errorf("German page: missing %q", want)
		}
	}
}

// A released computer's row shows its version without the macOS 13+ tag's
// "mac-" prefix, its size, and its SHA-256 folded under the page language's
// control; the macOS 12 row comes last.
func TestARowShowsItsVersionAndFoldsItsSHA(t *testing.T) {
	v := DownloadView{Device: "windows", Desktop: []DesktopApp{
		{OS: "macos13", Device: "macos", Name: "macOS", Needs: "macOS 13 or later", State: "available", URL: "https://kiff.dev/download/tafs-mac-1.0.dmg", Version: "mac-v1.0", Size: 21_000_000, SHA256: strings.Repeat("a", 64)},
		{OS: "windows", Device: "windows", Name: "Windows", Needs: "Windows 10 or 11", State: "available", URL: "https://kiff.dev/download/tafs.msix", Version: "v1.0.0", Size: 57_000_000, SHA256: strings.Repeat("b", 64)},
		{OS: "macos", Device: "macos", Name: "macOS 12", Needs: "macOS 12 (Monterey) only", State: "available", URL: "https://kiff.dev/download/tafs-macos.dmg", Version: "v0.1-4", Size: 18_000_000, SHA256: strings.Repeat("c", 64)},
	}, W: downloadFR}
	var buf bytes.Buffer
	if err := Download(v).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	if strings.Contains(body, ">mac-v1.0") || !strings.Contains(body, "v1.0 · 21 MB") {
		t.Error("the macOS 13+ version shows its tag's prefix, or not its size")
	}
	if !strings.Contains(body, `data-version="mac-v1.0"`) {
		t.Error("the download check lost the raw tag")
	}
	if strings.Count(body, "<summary>Version, taille et SHA-256</summary>") != 3 {
		t.Error("each released row doesn't fold its SHA-256 under the page's words")
	}
	if w, m := strings.Index(body, `data-os="windows"`), strings.Index(body, `data-os="macos"`); w < 0 || m < w {
		t.Error("macOS 12 isn't last")
	}
}
