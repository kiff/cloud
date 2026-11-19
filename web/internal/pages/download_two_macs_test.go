package pages

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/content/desktop"
)

// macs reads a fake GitHub where the Catalyst Mac app (kiff/tafs, among the
// phones' releases) and the macOS 12 app (kiff/tafs-macos) are published as
// asked, and nothing else is.
func macs(t *testing.T, catalyst, monterey bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/kiff/tafs/releases":
			mac := ""
			if catalyst {
				mac = `{"tag_name": "mac-v1.0.0", "draft": false, "prerelease": false, "assets": [{"name": "tafs-mac-1.0.0.dmg",
				  "size": 41000000, "digest": "sha256:` + releaseSHA + `",
				  "browser_download_url": "https://github.com/kiff/tafs/releases/download/mac-v1.0.0/tafs-mac-1.0.0.dmg"}]},`
			}
			_, _ = w.Write([]byte(`[{"tag_name": "ios-v2820", "draft": false, "prerelease": false, "assets": []},` + mac +
				`{"tag_name": "android-v1.37.0", "draft": false, "prerelease": false, "assets": []}]`))
		case "/repos/kiff/tafs-macos/releases/latest":
			if !monterey {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"tag_name": "v0.1-2", "draft": false, "prerelease": false, "assets": [{"name": "tafs-0.1-2.dmg",
			  "size": 15597295, "digest": "sha256:` + releaseSHA + `",
			  "browser_download_url": "https://github.com/kiff/tafs-macos/releases/download/v0.1-2/tafs-0.1-2.dmg"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(desktop.ResetForTest)
	desktop.ResetForTest()
	if err := (&desktop.Source{API: srv.URL}).Read(context.Background()); err != nil {
		t.Fatal(err)
	}
}

const (
	catalystDMG = `href="https://github.com/kiff/tafs/releases/download/mac-v1.0.0/tafs-mac-1.0.0.dmg"`
	montereyDMG = `href="https://github.com/kiff/tafs-macos/releases/download/v0.1-2/tafs-0.1-2.dmg"`
)

// Each Mac download stands on its own: macOS 13 and later get the Catalyst
// app, macOS 12 gets tafs-macos, each labelled by its macOS, each linked only
// when its own release is out.
func TestEachMacDownloadStandsOnItsOwn(t *testing.T) {
	for name, tc := range map[string]struct {
		catalyst, monterey bool
		comingSoon         int
	}{
		"both":                    {true, true, 2},
		"macOS 13 and later only": {true, false, 3},
		"macOS 12 only":           {false, true, 3},
		"neither, as today":       {false, false, 4},
	} {
		t.Run(name, func(t *testing.T) {
			macs(t, tc.catalyst, tc.monterey)
			body := renderDownload(t, downloadWords{})
			if got := strings.Contains(body, catalystDMG); got != tc.catalyst {
				t.Errorf("the Catalyst DMG linked = %v, want %v", got, tc.catalyst)
			}
			if got := strings.Contains(body, montereyDMG); got != tc.monterey {
				t.Errorf("the macOS 12 DMG linked = %v, want %v", got, tc.monterey)
			}
			if got := strings.Contains(body, ">Download for macOS<"); got != tc.catalyst {
				t.Errorf("Download for macOS = %v, want %v", got, tc.catalyst)
			}
			if got := strings.Contains(body, ">Download for macOS 12<"); got != tc.monterey {
				t.Errorf("Download for macOS 12 = %v, want %v", got, tc.monterey)
			}
			if n := strings.Count(body, " dl-pending"); n != tc.comingSoon || strings.Contains(body, ">Coming soon<") {
				t.Errorf("%d downloads wait for their file, want %d, none saying it's coming soon", n, tc.comingSoon)
			}
			for _, needs := range []string{"macOS 13 or later", "macOS 12 (Monterey) only"} {
				if !strings.Contains(body, needs) {
					t.Errorf("a Mac row doesn't say %q", needs)
				}
			}
		})
	}
}

// The description names the Mac once: out when either DMG is, coming while
// neither is but one is coming.
func TestTheDescriptionNamesTheMacOnce(t *testing.T) {
	const phones = "Get tafs on Google Play for Android. iPhone and iPad are coming to the App Store. "
	for name, tc := range map[string]struct {
		catalyst, monterey string
		want               string
	}{
		"neither out": {desktop.StateComingSoon, desktop.StateComingSoon,
			"The apps for macOS, Windows and Linux are coming soon."},
		"macOS 13 and later out": {desktop.StateAvailable, desktop.StateComingSoon,
			"Download tafs for macOS. The apps for Windows and Linux are coming soon."},
		"macOS 12 out": {desktop.StateComingSoon, desktop.StateAvailable,
			"Download tafs for macOS. The apps for Windows and Linux are coming soon."},
		"both out": {desktop.StateAvailable, desktop.StateAvailable,
			"Download tafs for macOS. The apps for Windows and Linux are coming soon."},
		"one held back, the other coming": {desktop.StateNotAvailable, desktop.StateComingSoon,
			"The apps for macOS, Windows and Linux are coming soon."},
		"both held back": {desktop.StateNotAvailable, desktop.StateNotAvailable,
			"The apps for Windows and Linux are coming soon."},
	} {
		t.Run(name, func(t *testing.T) {
			apps := []DesktopApp{
				{OS: "macos13", Device: "macos", Name: "macOS", Family: "macOS", State: tc.catalyst},
				{OS: "macos", Device: "macos", Name: "macOS 12", Family: "macOS", State: tc.monterey},
				{OS: "windows", Device: "windows", Name: "Windows", Family: "Windows", State: desktop.StateComingSoon},
				{OS: "linux", Device: "linux", Name: "Linux", Family: "Linux", State: desktop.StateComingSoon},
			}
			if got := (DownloadView{Desktop: apps}).description(); got != phones+tc.want {
				t.Errorf("got %q\nwant %q", got, phones+tc.want)
			}
		})
	}
}

// macOS 13 and later is the big computer for every visitor; the macOS 12 app
// is never big.
func TestMacOS13IsTheBigComputerForEveryVisitor(t *testing.T) {
	for _, device := range []string{"macos", "windows", "linux", "", "ios", "android"} {
		v := DownloadView{Device: device, Desktop: DesktopApps()}
		var big []string
		for _, a := range v.desktopFirst() {
			if v.big(a) {
				big = append(big, a.OS)
			}
		}
		if strings.Join(big, " ") != "macos13" {
			t.Errorf("%q: big %v, want macos13 only", device, big)
		}
	}
}

// "Get it" offers each computer whose file is out, macOS 13 and later the
// big one, by /tafs/download's words in the page's language, and the macOS 12
// app only as the quiet older-Mac link. With no computer out, it keeps the
// Android heading: it never says "your computer" with nothing to download.
func TestGetItOffersTheComputersWhoseFileIsOut(t *testing.T) {
	render := func(w tafsWords) string {
		var buf bytes.Buffer
		if err := tafsPage(w, OfferLinks{PlayURL: "https://play.example/tafs"}).Render(context.Background(), &buf); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	macs(t, true, true)
	body := render(tafsEN)
	for _, want := range []string{
		"On Android and your computer now.", "macOS, Windows and Linux: everywhere.",
		`class="pf pf-macos13 hero"`, "<small>Download for</small>",
		"On an older Mac with macOS\u00a012?\u00a0→", "google-play-badge.png", "Soon on Google Play", "Comes with the iPhone app",
		"Tux by Larry Ewing, made with The GIMP.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, "pf-macos\"") || strings.Contains(body, `<b class="wm">macOS 12</b>`) {
		t.Error("the macOS 12 app is a button on Get it")
	}
	de := render(tafsDE)
	for _, want := range []string{"Jetzt auf Android und deinem Computer.", "<small>Für</small>", "<small>herunterladen</small>", "Älterer Mac mit macOS\u00a012?\u00a0→", "google-play-badge-de.png"} {
		if !strings.Contains(de, want) {
			t.Errorf("German: missing %q", want)
		}
	}

	macs(t, false, false)
	body = render(tafsEN)
	if strings.Contains(body, "On Android and your computer now.") || strings.Contains(body, `class="pf `) || strings.Contains(body, "older Mac") {
		t.Error("with no computer out, Get it still offers one")
	}
	if !strings.Contains(body, tafsEN.Get.Big) {
		t.Error("with no computer out, Get it lost its Android heading")
	}
}

// "Get it" is two parts: the buttons (computers, the Play badge, the App
// Store line), then everything else in one .dl-side block (the watches, the
// older-Mac or computer link, GitHub, the trademark notice). Where /tafs runs
// sideways, each panel is one screen, clipped, and the block is the second
// column: tafs.css lays it out by this structure, so the notice can't fall
// back under the buttons and off the screen.
func TestGetItKeepsTheRestBesideTheButtons(t *testing.T) {
	for _, out := range []bool{true, false} {
		macs(t, out, out)
		var buf bytes.Buffer
		if err := tafsPage(tafsEN, OfferLinks{PlayURL: "https://play.example/tafs"}).Render(context.Background(), &buf); err != nil {
			t.Fatal(err)
		}
		body := buf.String()
		get := body[strings.Index(body, `id="download"`):]
		btns, side := strings.Index(get, `class="btns"`), strings.Index(get, `class="dl-side"`)
		if btns < 0 || side < btns {
			t.Fatalf("files out %v: no .dl-side after the buttons", out)
		}
		buttons, rest := get[btns:side], get[side:]
		for _, want := range []string{`class="watches"`, "github.com/kiff/tafs", `class="tm"`} {
			if strings.Contains(buttons, want) || !strings.Contains(rest, want) {
				t.Errorf("files out %v: %s isn't in .dl-side", out, want)
			}
		}
		if out && !strings.Contains(rest, `class="older"`) {
			t.Error("the older-Mac link isn't in .dl-side")
		}
		if strings.Contains(rest, `class="pf `) || strings.Contains(rest, `class="gpb"`) {
			t.Error("a button or the Play badge is in .dl-side")
		}
	}
}

// The older-Mac link's arrow stays with its last word in every language, and
// French's "?" with its word: those spaces are non-breaking, and nothing else
// in the words changes.
func TestTheOlderMacArrowNeverWrapsAlone(t *testing.T) {
	for lang, w := range getByLang {
		got := w.olderMac()
		if !strings.HasSuffix(got, "\u00a0→") || strings.HasSuffix(got, " →") {
			t.Errorf("%s: %q", lang, got)
		}
		if strings.Contains(got, "macOS 12") {
			t.Errorf("%s: \"macOS 12\" can break: %q", lang, got)
		}
		if strings.Contains(got, " ?") {
			t.Errorf("%s: a breakable space before \"?\": %q", lang, got)
		}
		if strings.ReplaceAll(got, "\u00a0", " ") != w.OlderMac {
			t.Errorf("%s: the words changed: %q", lang, got)
		}
	}
}
