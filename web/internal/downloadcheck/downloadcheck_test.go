package downloadcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/content/desktop"
	"github.com/kiff/cloud/web/internal/pages"
)

var now = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

const (
	shaA = "6838a7f75102e71d408f9ce70c32e395f2f440dbdc9f696450e813092463bc3a"
	shaB = "0000000000000000000000000000000000000000000000000000000000000001"
)

// rel is one computer's published release on the fake GitHub.
type rel struct {
	tag       string
	sha       string
	size      int64
	published time.Time
}

var fileName = map[string]string{
	"macos13": "tafs-mac-%s.dmg",
	"macos":   "tafs-%s.dmg",
	"windows": "tafs-%s-x64.exe",
	"linux":   "tafs-%s-x86_64.AppImage",
}

// fakeGitHub answers releases/latest for each computer in rels, and 404 for
// the others; fail makes every read fail.
func fakeGitHub(t *testing.T, rels map[string]rel, fail bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		for os, ch := range desktop.Channels {
			list := ch.Tag != ""
			path := "/repos/" + ch.Repo + "/releases/latest"
			if list {
				path = "/repos/" + ch.Repo + "/releases"
			}
			if r.URL.Path != path {
				continue
			}
			rl, ok := rels[os]
			if !ok {
				if list {
					// A channel sharing its repository: the other apps' releases only.
					_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "android-v1.37.0", "draft": false, "prerelease": false,
						"assets": []map[string]any{{"name": "tafs.aab", "size": 1, "digest": "sha256:" + shaB,
							"browser_download_url": "https://github.com/" + ch.Repo + "/releases/download/android-v1.37.0/tafs.aab"}}}})
					return
				}
				http.NotFound(w, r)
				return
			}
			name := strings.Replace(fileName[os], "%s", strings.TrimPrefix(rl.tag, ch.Tag), 1)
			one := map[string]any{
				"tag_name":     rl.tag,
				"draft":        false,
				"prerelease":   false,
				"published_at": rl.published.Format(time.RFC3339),
				"assets": []map[string]any{{
					"name":                 name,
					"size":                 rl.size,
					"digest":               "sha256:" + rl.sha,
					"browser_download_url": "https://github.com/" + ch.Repo + "/releases/download/" + rl.tag + "/" + name,
				}},
			}
			if list {
				_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "ios-v2820", "draft": false, "prerelease": false, "assets": []map[string]any{}}, one})
				return
			}
			_ = json.NewEncoder(w).Encode(one)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// rendered is the real /tafs/download in each language for the computers as
// these releases (and this hold) make them: what a site that has read them
// shows.
func rendered(t *testing.T, rels map[string]rel, notAvailable string, langs ...string) map[string]string {
	t.Helper()
	gh := fakeGitHub(t, rels, false)
	desktop.Reset()
	desktop.HoldNotAvailable(notAvailable)
	if err := (&desktop.Source{API: gh.URL}).Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	apps := pages.DesktopApps()
	out := map[string]string{}
	for _, l := range langs {
		var b bytes.Buffer
		if err := pages.DownloadPage(l, apps).Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		out[l] = b.String()
	}
	desktop.Reset()
	return out
}

// fakeSite serves those pages at their paths.
func fakeSite(t *testing.T, byLang map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for l, body := range byLang {
			if r.URL.Path == pagePath(l) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(body))
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func check(t *testing.T, site, gh string, notAvailable string) Report {
	t.Helper()
	t.Cleanup(desktop.Reset)
	rep, err := Run(context.Background(), Config{
		Site: site, Langs: []string{"en", "fr"}, Releases: true, NotAvailable: notAvailable,
		GitHubAPI: gh, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

var linuxOut = map[string]rel{"linux": {tag: "v1.0.0", sha: shaA, size: 43_063_800, published: now.Add(-3 * time.Hour)}}

func TestALivePageThatMatchesPasses(t *testing.T) {
	site := fakeSite(t, rendered(t, linuxOut, "", "en", "fr"))
	gh := fakeGitHub(t, linuxOut, false)
	rep := check(t, site.URL, gh.URL, "")
	if rep.Mismatch() || len(rep.Findings) != 0 {
		t.Fatalf("a matching page: %s", rep.Text())
	}
	if !strings.Contains(rep.Text(), "Linux: available, v1.0.0, 43063800 bytes, SHA-256 "+shaA) {
		t.Fatalf("the report doesn't say what the releases say:\n%s", rep.Text())
	}
}

func TestAWrongSHAIsAMismatch(t *testing.T) {
	site := fakeSite(t, rendered(t, linuxOut, "", "en", "fr"))
	other := map[string]rel{"linux": {tag: "v1.0.0", sha: shaB, size: 43_063_800, published: now.Add(-3 * time.Hour)}}
	gh := fakeGitHub(t, other, false)
	rep := check(t, site.URL, gh.URL, "")
	if !rep.Mismatch() {
		t.Fatalf("a wrong SHA-256 passed:\n%s", rep.Text())
	}
	assertFinding(t, rep, "en", "linux row", false)
	assertFinding(t, rep, "fr", "linux row", false)
}

func TestAStaleComingSoonAfterAReleaseIsAMismatch(t *testing.T) {
	site := fakeSite(t, rendered(t, nil, "", "en", "fr"))
	gh := fakeGitHub(t, linuxOut, false)
	rep := check(t, site.URL, gh.URL, "")
	if !rep.Mismatch() {
		t.Fatalf("a page still saying coming soon passed:\n%s", rep.Text())
	}
	assertFinding(t, rep, "en", "linux row", false)
	assertFinding(t, rep, "en", "description", false)
}

func TestAReleaseJustPublishedIsPendingThenAMismatch(t *testing.T) {
	site := fakeSite(t, rendered(t, nil, "", "en", "fr"))
	t.Run("within the grace", func(t *testing.T) {
		gh := fakeGitHub(t, map[string]rel{"linux": {tag: "v1.0.0", sha: shaA, size: 43_063_800, published: now.Add(-5 * time.Minute)}}, false)
		rep := check(t, site.URL, gh.URL, "")
		if rep.Mismatch() {
			t.Fatalf("a release published 5 minutes ago failed:\n%s", rep.Text())
		}
		assertFinding(t, rep, "en", "linux row", true)
		assertFinding(t, rep, "en", "description", true)
		if !strings.Contains(rep.Text(), "pending") {
			t.Fatalf("the report doesn't say pending:\n%s", rep.Text())
		}
	})
	t.Run("within the grace, a description the rows don't give is still a mismatch", func(t *testing.T) {
		stale := rendered(t, nil, "", "en", "fr")
		stale["en"] = strings.ReplaceAll(stale["en"], "The apps for macOS, Windows and Linux are coming soon.", "Something else entirely.")
		odd := fakeSite(t, stale)
		gh := fakeGitHub(t, map[string]rel{"linux": {tag: "v1.0.0", sha: shaA, size: 43_063_800, published: now.Add(-5 * time.Minute)}}, false)
		rep := check(t, odd.URL, gh.URL, "")
		if !rep.Mismatch() {
			t.Fatalf("a description neither the releases nor the rows give passed:\n%s", rep.Text())
		}
		assertFinding(t, rep, "en", "description", false)
	})
	t.Run("past the grace", func(t *testing.T) {
		gh := fakeGitHub(t, map[string]rel{"linux": {tag: "v1.0.0", sha: shaA, size: 43_063_800, published: now.Add(-20 * time.Minute)}}, false)
		rep := check(t, site.URL, gh.URL, "")
		if !rep.Mismatch() {
			t.Fatalf("a release published 20 minutes ago and not shown passed:\n%s", rep.Text())
		}
	})
}

func TestAWrongDescriptionIsAMismatch(t *testing.T) {
	pagesByLang := rendered(t, linuxOut, "", "en", "fr")
	en := pagesByLang["en"]
	want := "Download tafs for Linux. The apps for macOS and Windows are coming soon."
	if strings.Count(en, want) != 3 {
		t.Fatalf("the rendered page doesn't carry the description three times")
	}
	pagesByLang["en"] = strings.ReplaceAll(en, want, "The apps for macOS, Windows and Linux are coming soon.")
	site := fakeSite(t, pagesByLang)
	gh := fakeGitHub(t, linuxOut, false)
	rep := check(t, site.URL, gh.URL, "")
	if !rep.Mismatch() {
		t.Fatalf("a stale description passed:\n%s", rep.Text())
	}
	assertFinding(t, rep, "en", "description", false)
	for _, f := range rep.Findings {
		if f.Lang == "fr" {
			t.Fatalf("the French page matched but was reported: %+v", f)
		}
	}
}

func TestTagsThatDisagreeAreAMismatch(t *testing.T) {
	pagesByLang := rendered(t, linuxOut, "", "en", "fr")
	en := pagesByLang["en"]
	i := strings.Index(en, `property="og:description"`)
	if i < 0 {
		t.Fatal("no og:description in the rendered page")
	}
	j := i + strings.Index(en[i:], "Download tafs for Linux.")
	pagesByLang["en"] = en[:j] + "Something else." + en[j+len("Download tafs for Linux."):]
	site := fakeSite(t, pagesByLang)
	gh := fakeGitHub(t, linuxOut, false)
	rep := check(t, site.URL, gh.URL, "")
	if !rep.Mismatch() {
		t.Fatalf("og:description disagreeing passed:\n%s", rep.Text())
	}
	assertFinding(t, rep, "en", "og:description", false)
}

func TestANotAvailableComputerIsChecked(t *testing.T) {
	both := map[string]rel{
		"linux":   linuxOut["linux"],
		"windows": {tag: "v1.0.0", sha: shaB, size: 50_000_000, published: now.Add(-3 * time.Hour)},
	}
	t.Run("held back on both sides matches", func(t *testing.T) {
		site := fakeSite(t, rendered(t, both, "windows", "en", "fr"))
		gh := fakeGitHub(t, both, false)
		rep := check(t, site.URL, gh.URL, "windows")
		if rep.Mismatch() {
			t.Fatalf("a held-back Windows on both sides failed:\n%s", rep.Text())
		}
		for _, a := range rep.Expected {
			if a.OS == "windows" && a.State != desktop.StateNotAvailable {
				t.Fatalf("windows expected %s, want not available", a.State)
			}
		}
	})
	t.Run("the page offering what the deploy holds back is a mismatch", func(t *testing.T) {
		site := fakeSite(t, rendered(t, both, "", "en", "fr"))
		gh := fakeGitHub(t, both, false)
		rep := check(t, site.URL, gh.URL, "windows")
		if !rep.Mismatch() {
			t.Fatalf("a page offering a held-back Windows passed:\n%s", rep.Text())
		}
		assertFinding(t, rep, "en", "windows row", false)
	})
}

func TestAPageThatCantBeFetchedFails(t *testing.T) {
	gh := fakeGitHub(t, linuxOut, false)
	t.Run("an error answer", func(t *testing.T) {
		site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "oops", http.StatusInternalServerError)
		}))
		defer site.Close()
		rep := check(t, site.URL, gh.URL, "")
		if !rep.Mismatch() {
			t.Fatalf("a 500 passed:\n%s", rep.Text())
		}
		assertFinding(t, rep, "en", "page", false)
	})
	t.Run("nothing listening", func(t *testing.T) {
		site := httptest.NewServer(http.NotFoundHandler())
		url := site.URL
		site.Close()
		rep := check(t, url, gh.URL, "")
		if !rep.Mismatch() {
			t.Fatalf("an unreachable site passed:\n%s", rep.Text())
		}
	})
	t.Run("a page without the rows' markers", func(t *testing.T) {
		site := fakeSite(t, map[string]string{"en": "<html><head></head><body>hello</body></html>", "fr": "<html></html>"})
		rep := check(t, site.URL, gh.URL, "")
		if !rep.Mismatch() {
			t.Fatalf("a page with no rows passed:\n%s", rep.Text())
		}
	})
}

func TestReleasesThatCantBeReadStopTheCheck(t *testing.T) {
	t.Cleanup(desktop.Reset)
	site := fakeSite(t, rendered(t, linuxOut, "", "en"))
	gh := fakeGitHub(t, nil, true)
	_, err := Run(context.Background(), Config{Site: site.URL, Langs: []string{"en"}, Releases: true, GitHubAPI: gh.URL, Now: func() time.Time { return now }})
	if err == nil {
		t.Fatal("a failed release read was compared anyway")
	}
}

func TestReleasesOffExpectsEverythingComingSoon(t *testing.T) {
	t.Cleanup(desktop.Reset)
	site := fakeSite(t, rendered(t, nil, "", "en", "fr"))
	rep, err := Run(context.Background(), Config{Site: site.URL, Langs: []string{"en", "fr"}, Releases: false, Now: func() time.Time { return now }})
	if err != nil || rep.Mismatch() {
		t.Fatalf("releases off, all coming soon: err %v\n%s", err, rep.Text())
	}
	for _, a := range rep.Expected {
		if a.State != desktop.StateComingSoon {
			t.Fatalf("%s expected %s with releases off", a.OS, a.State)
		}
	}
}

func TestTheRowsCarryTheirReleaseAsDataAttributes(t *testing.T) {
	en := rendered(t, linuxOut, "", "en")["en"]
	for _, want := range []string{
		`data-os="linux" data-state="available" data-version="v1.0.0" data-size="43063800" data-sha256="` + shaA + `"`,
		`data-os="macos" data-state="coming-soon" data-version="" data-size="0" data-sha256=""`,
	} {
		if !strings.Contains(en, want) {
			t.Fatalf("the page lacks %s", want)
		}
	}
	p := parse(en)
	if p.rows["linux"] != (row{state: "available", version: "v1.0.0", sha256: shaA, size: 43_063_800}) {
		t.Fatalf("parsed linux %+v", p.rows["linux"])
	}
	if len(p.meta) != 3 {
		t.Fatalf("parsed meta %v", p.meta)
	}
}

func assertFinding(t *testing.T, rep Report, lang, what string, pending bool) {
	t.Helper()
	for _, f := range rep.Findings {
		if f.Lang == lang && f.What == what {
			if f.Pending != pending {
				t.Fatalf("%s %s pending %v, want %v\n%s", lang, what, f.Pending, pending, rep.Text())
			}
			return
		}
	}
	t.Fatalf("no finding for %s %s:\n%s", lang, what, rep.Text())
}

// The two Mac downloads are checked one by one, in every combination: each
// row against its own channel's release (the Catalyst app's among kiff/tafs's
// other releases), and the description naming the Mac once.
func TestBothMacDownloadsAreChecked(t *testing.T) {
	catalyst := rel{tag: "mac-v1.0.0", sha: shaA, size: 41_000_000, published: now.Add(-3 * time.Hour)}
	monterey := rel{tag: "v0.1-2", sha: shaB, size: 15_394_495, published: now.Add(-3 * time.Hour)}
	for name, rels := range map[string]map[string]rel{
		"neither":                 {},
		"macOS 13 and later only": {"macos13": catalyst},
		"macOS 12 only":           {"macos": monterey},
		"both":                    {"macos13": catalyst, "macos": monterey},
	} {
		t.Run(name, func(t *testing.T) {
			site := fakeSite(t, rendered(t, rels, "", "en", "fr"))
			gh := fakeGitHub(t, rels, false)
			rep := check(t, site.URL, gh.URL, "")
			if rep.Mismatch() || len(rep.Findings) != 0 {
				t.Fatalf("a matching page: %s", rep.Text())
			}
			for _, want := range []string{"- macOS: ", "- macOS 12: "} {
				if !strings.Contains(rep.Text(), want) {
					t.Fatalf("the report doesn't name %q:\n%s", want, rep.Text())
				}
			}
		})
	}
	// Each Mac's row is compared with its own release: the page offering one
	// Mac while the releases say the other is a mismatch on both rows.
	t.Run("the rows swapped", func(t *testing.T) {
		site := fakeSite(t, rendered(t, map[string]rel{"macos13": catalyst}, "", "en"))
		gh := fakeGitHub(t, map[string]rel{"macos": monterey}, false)
		rep := check(t, site.URL, gh.URL, "")
		if !rep.Mismatch() {
			t.Fatalf("the swapped Macs passed:\n%s", rep.Text())
		}
		assertFinding(t, rep, "en", "macos13 row", false)
		assertFinding(t, rep, "en", "macos row", false)
	})
	// A page from before the Catalyst row (no data-os="macos13") is a
	// finding, not a pass.
	t.Run("a page without the macOS 13 row", func(t *testing.T) {
		byLang := rendered(t, nil, "", "en")
		byLang["en"] = strings.Replace(byLang["en"], `data-os="macos13"`, `data-os="gone"`, 1)
		site := fakeSite(t, byLang)
		gh := fakeGitHub(t, nil, false)
		rep := check(t, site.URL, gh.URL, "")
		if !rep.Mismatch() {
			t.Fatalf("a page with no macOS 13 row passed:\n%s", rep.Text())
		}
		assertFinding(t, rep, "en", "macos13 row", false)
	})
}
