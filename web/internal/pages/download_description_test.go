package pages

import (
	"regexp"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/content/desktop"
)

// allDownloadWords are /tafs/download's words in every language it has.
var allDownloadWords = []downloadWords{downloadEN, downloadDE, downloadES, downloadFR, downloadIT, downloadNL, downloadPL, downloadPT}

// computers is the page's three computers, each available or coming soon as
// mask says (bit 0 macOS, bit 1 Windows, bit 2 Linux).
func computers(mask int) []DesktopApp {
	apps := []DesktopApp{{OS: "macos", Name: "macOS"}, {OS: "windows", Name: "Windows"}, {OS: "linux", Name: "Linux"}}
	for i := range apps {
		apps[i].State = desktop.StateComingSoon
		if mask&(1<<i) != 0 {
			apps[i].State = desktop.StateAvailable
		}
	}
	return apps
}

// The description names each computer once, in the group its release puts it
// in, in all 8 cases of which are out and in every language: never an empty
// list, never an "and" at a list's edge, and "one app" only for one.
func TestTheDownloadDescriptionNamesOnlyWhatIsStillToCome(t *testing.T) {
	names := []string{"macOS", "Windows", "Linux"}
	for _, w := range allDownloadWords {
		if w.DescAvailable == "" || w.DescComingOne == "" || w.DescComingMany == "" || w.And == "" {
			t.Fatalf("%s: a description word is missing", w.Lang)
		}
		and := regexp.QuoteMeta(w.And)
		edge := regexp.MustCompile(`(^|[ ,.])` + and + ` *[.,]|\b` + and + ` +` + and + `\b|, *` + and + `\b|%s|  `)
		for mask := 0; mask < 8; mask++ {
			d := DownloadView{Desktop: computers(mask), W: w}.description()
			if !strings.HasPrefix(d, w.Description+" ") {
				t.Errorf("%s %03b: %q doesn't start with the phones' sentences", w.Lang, mask, d)
			}
			rest := strings.TrimPrefix(d, w.Description)
			for _, n := range names {
				if c := strings.Count(rest, n); c != 1 {
					t.Errorf("%s %03b: %s named %d times in %q", w.Lang, mask, n, c, rest)
				}
			}
			if edge.MatchString(rest) {
				t.Errorf("%s %03b: a list's edge is wrong in %q", w.Lang, mask, rest)
			}
			var avail, coming []string
			for i, n := range names {
				if mask&(1<<i) != 0 {
					avail = append(avail, n)
				} else {
					coming = append(coming, n)
				}
			}
			want := w.Description
			if len(avail) > 0 {
				want += " " + strings.Replace(w.DescAvailable, "%s", w.list(avail), 1)
			}
			switch len(coming) {
			case 0:
			case 1:
				want += " " + strings.Replace(w.DescComingOne, "%s", coming[0], 1)
			default:
				want += " " + strings.Replace(w.DescComingMany, "%s", w.list(coming), 1)
			}
			if d != want {
				t.Errorf("%s %03b:\n got %q\nwant %q", w.Lang, mask, d, want)
			}
		}
	}
}

// The lists read as each language writes them.
func TestTheDownloadDescriptionsListsReadNaturally(t *testing.T) {
	for _, c := range []struct {
		w     downloadWords
		names []string
		want  string
	}{
		{downloadEN, []string{"Linux"}, "Linux"},
		{downloadEN, []string{"macOS", "Windows"}, "macOS and Windows"},
		{downloadEN, []string{"macOS", "Windows", "Linux"}, "macOS, Windows and Linux"},
		{downloadDE, []string{"macOS", "Windows", "Linux"}, "macOS, Windows und Linux"},
		{downloadPL, []string{"macOS", "Windows"}, "macOS i Windows"},
	} {
		if got := c.w.list(c.names); got != c.want {
			t.Errorf("%s %v: got %q, want %q", c.w.Lang, c.names, got, c.want)
		}
	}
}

// With nothing released, the description is word for word what the page said
// before it was built from the release data. With today's data (Linux out),
// English reads as agreed with the translators.
func TestTheDownloadDescriptionForTodayAndForNothingOut(t *testing.T) {
	before := map[string]string{
		"en": "Get tafs on Google Play for Android. iPhone and iPad are coming to the App Store. The apps for macOS, Windows and Linux are coming soon.",
		"de": "Hol dir tafs bei Google Play für Android. iPhone und iPad kommen bald in den App Store. Die Apps für macOS, Windows und Linux sind bald verfügbar.",
		"pl": "Pobierz tafs z Google Play na Androida. Wersje na iPhone i iPad pojawią się w App Store. Aplikacje na macOS, Windows i Linux będą wkrótce dostępne.",
	}
	for _, w := range allDownloadWords {
		if b, ok := before[w.Lang]; ok {
			if got := (DownloadView{Desktop: computers(0), W: w}).description(); got != b {
				t.Errorf("%s, nothing out:\n got %q\nwant %q", w.Lang, got, b)
			}
		}
	}
	today := DownloadView{Desktop: computers(4)}.description()
	if want := "Get tafs on Google Play for Android. iPhone and iPad are coming to the App Store. Download tafs for Linux. The apps for macOS and Windows are coming soon."; today != want {
		t.Errorf("today:\n got %q\nwant %q", today, want)
	}
}

// A computer held back as not available is in neither group.
func TestANotAvailableComputerIsNeitherOutNorComing(t *testing.T) {
	apps := computers(4)
	apps[1].State = desktop.StateNotAvailable
	d := DownloadView{Desktop: apps}.description()
	if strings.Contains(d, "Windows") {
		t.Errorf("Windows is not available, yet named: %q", d)
	}
	if !strings.HasSuffix(d, "Download tafs for Linux. The app for macOS is coming soon.") {
		t.Errorf("got %q", d)
	}
}

var metaDescRE = regexp.MustCompile(`<meta (?:name|property)="(description|og:description|twitter:description)" content="([^"]*)"`)

// The page's three descriptions are the same text, built from what the rows
// show: with the Mac out, the Mac is offered and only Windows and Linux are
// coming, in English and in each translation.
func TestTheDownloadPagesThreeDescriptionsFollowTheRows(t *testing.T) {
	releasedMac(t, "")
	for _, w := range allDownloadWords {
		html := renderDownload(t, w)
		got := map[string]string{}
		for _, m := range metaDescRE.FindAllStringSubmatch(html, -1) {
			got[m[1]] = m[2]
		}
		want := htmlEscape(DownloadView{Desktop: DesktopApps(), W: w}.description())
		for _, k := range []string{"description", "og:description", "twitter:description"} {
			if got[k] != want {
				t.Errorf("%s %s:\n got %q\nwant %q", w.Lang, k, got[k], want)
			}
		}
		if !strings.Contains(want, strings.Replace(w.DescAvailable, "%s", "macOS", 1)) {
			t.Errorf("%s: the Mac's release isn't offered in %q", w.Lang, want)
		}
	}
}

// htmlEscape is how templ writes an attribute's text.
func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "'", "&#39;", "\"", "&#34;", "<", "&lt;", ">", "&gt;").Replace(s)
}
