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

const releaseSHA = "ff7c873e4e9945d024b769549bbd170727d9f36bb31c14c88ea83df5e74826f6"

// releasedMac reads a fake GitHub where only the Mac has a published release
// with its digest, and holds back what notAvailable names.
func releasedMac(t *testing.T, notAvailable string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/kiff/tafs-macos/releases/latest" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name": "v0.1-2", "draft": false, "prerelease": false, "assets": [{"name": "tafs-0.1-2.dmg",
		  "size": 15597295, "digest": "sha256:` + releaseSHA + `",
		  "browser_download_url": "https://github.com/kiff/tafs-macos/releases/download/v0.1-2/tafs-0.1-2.dmg"}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(desktop.ResetForTest)
	if err := (&desktop.Source{API: srv.URL}).Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	desktop.HoldNotAvailable(notAvailable)
}

func renderDownload(t *testing.T, w downloadWords) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Download(DownloadView{Device: "macos", Desktop: DesktopApps(), W: w}).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// The three states, from the releases: the Mac's published release is
// linked with its version, size and SHA-256; Windows, with none, is coming
// soon; Linux, held back, is not available.
func TestDownloadShowsEachComputersStateFromItsRelease(t *testing.T) {
	releasedMac(t, "linux")
	body := renderDownload(t, downloadWords{})
	for _, want := range []string{
		`href="https://github.com/kiff/tafs-macos/releases/download/v0.1-2/tafs-0.1-2.dmg"`,
		"Download for macOS 12",
		"Version v0.1-2 · 16 MB",
		"SHA-256: <code>" + releaseSHA + "</code>",
		"Check that the file&#39;s SHA-256 matches before you open it.",
		" dl-pending",
		">Not available<",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page doesn't say %q", want)
		}
	}
	if strings.Contains(body, "Download for Windows") || strings.Contains(body, "Download for Linux") {
		t.Error("a computer with no release is offered")
	}
	if n := strings.Count(body, " dl-pending"); n != 2 {
		t.Errorf("%d computers wait for their file, want 2 (macOS 13 and later, Windows)", n)
	}
	// The owner's one-page rule: a computer never says "coming soon".
	if strings.Contains(body, ">Coming soon<") {
		t.Error("a computer says it's coming soon")
	}
}

// With no release read (KIFF_DESKTOP_RELEASES off), every computer is coming
// soon and nothing is linked: the page is today's.
func TestDownloadWithNoReleaseIsTodays(t *testing.T) {
	desktop.ResetForTest()
	body := renderDownload(t, downloadWords{})
	if n := strings.Count(body, " dl-pending"); n != 4 || strings.Contains(body, ">Coming soon<") {
		t.Errorf("%d computers wait for their file, want 4, none saying it's coming soon", n)
	}
	if strings.Contains(body, "Download for") || strings.Contains(body, "SHA-256") {
		t.Error("a computer is offered with no release")
	}
}

// A language with no words yet for the released file says them in English
// until its translation comes, never an empty line.
func TestDownloadReleasedFileInEveryLanguage(t *testing.T) {
	releasedMac(t, "")
	for _, w := range []downloadWords{downloadDE, downloadFR, downloadES, downloadIT, downloadPT, downloadNL, downloadPL} {
		body := renderDownload(t, w)
		if !strings.Contains(body, "v0.1-2") || !strings.Contains(body, releaseSHA) || !strings.Contains(body, "16 MB") {
			t.Errorf("%s: the released file isn't described", w.Lang)
		}
	}
}

func TestSizeText(t *testing.T) {
	for n, want := range map[int64]string{15597295: "16 MB", 1_000_000: "1 MB", 850_400: "850 KB", 999: "999 B"} {
		if got := sizeText(n); got != want {
			t.Errorf("%d: %q, want %q", n, got, want)
		}
	}
}

// /support §6 follows the same releases: on with the first published one.
func TestSupportComputerFollowsTheReleases(t *testing.T) {
	desktop.ResetForTest()
	if computerAppReleased() {
		t.Fatal("§6 is on with no release")
	}
	releasedMac(t, "")
	if !computerAppReleased() {
		t.Fatal("§6 is off with the Mac's release published")
	}
	desktop.HoldNotAvailable("macos")
	if computerAppReleased() {
		t.Fatal("§6 is on with the only release held back")
	}
}

// releasedAll reads a fake GitHub where Windows, Linux (with its SHA256SUMS and
// signature) and macOS 12 each have a published release.
func releasedAll(t *testing.T) {
	t.Helper()
	asset := func(repo, tag, name string) string {
		return `{"name": "` + name + `", "size": 15597295, "digest": "sha256:` + releaseSHA + `",
		  "browser_download_url": "https://github.com/` + repo + `/releases/download/` + tag + `/` + name + `"}`
	}
	answers := map[string]string{
		"/repos/kiff/tafs-windows/releases/latest": `{"tag_name": "v1.1.0", "assets": [` + asset("kiff/tafs-windows", "v1.1.0", "tafs-setup-1.1.0-x64.exe") + `]}`,
		"/repos/kiff/tafs-linux/releases/latest": `{"tag_name": "v1.1.0", "assets": [` + asset("kiff/tafs-linux", "v1.1.0", "tafs-v1.1.0-x86_64.AppImage") + `,` +
			asset("kiff/tafs-linux", "v1.1.0", "SHA256SUMS") + `,` + asset("kiff/tafs-linux", "v1.1.0", "SHA256SUMS.asc") + `]}`,
		"/repos/kiff/tafs-macos/releases/latest": `{"tag_name": "v0.2", "assets": [` + asset("kiff/tafs-macos", "v0.2", "tafs-0.2.dmg") + `]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := answers[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(desktop.ResetForTest)
	if err := (&desktop.Source{API: srv.URL}).Read(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Each computer's download says what a person should know about its file:
// Windows' unsigned beta until the installer is signed, macOS 12's
// notarization, and Linux's signature with the release key and its files.
func TestEachDownloadSaysHowItsFileIsSigned(t *testing.T) {
	releasedAll(t)
	t.Cleanup(func() { WindowsSigned = false })
	html := renderDownload(t, downloadEN)
	for _, want := range []string{
		"This beta isn&#39;t signed yet. Windows may say it&#39;s from an unknown publisher",
		"Signed with Apple&#39;s Developer ID and notarized by Apple.",
		"SHA256SUMS is signed with the release key <code>" + LinuxReleaseKey + "</code>",
		`href="https://github.com/kiff/tafs-linux/releases/download/v1.1.0/SHA256SUMS"`,
		`href="https://github.com/kiff/tafs-linux/releases/download/v1.1.0/SHA256SUMS.asc"`,
		`href="` + LinuxReleaseKeyURL + `"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q", want)
		}
	}
	// Once Windows' installer is signed, its warning goes; nothing else changes.
	WindowsSigned = true
	if html := renderDownload(t, downloadEN); strings.Contains(html, "signed yet") {
		t.Error("a signed Windows installer still says it isn't")
	}
}

// A computer with no release says nothing about signing: no note under a
// download that isn't there.
func TestNoNoteWithoutADownload(t *testing.T) {
	releasedMac(t, "")
	html := renderDownload(t, downloadEN)
	if strings.Contains(html, "signed yet") || strings.Contains(html, "SHA256SUMS") {
		t.Error("a note shows for a computer with no download")
	}
}
