package pages

import (
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/content/desktop"
)

// downloadWords are /tafs/download's words in one language. English is
// downloadEN; each other language has its own download_words_<lang>.go, a
// translation held to the English by TestTranslationsKeepTheEnglishPage.
// Product, store and system names stay as they are.
type downloadWords struct {
	Lang string
	// Title and Description are the page's head. Description is the phones'
	// two sentences; the computers' come from the release data (description).
	Title, Description string
	// DescAvailable ("Download tafs for %s."), DescComingOne ("The app for %s
	// is coming soon.") and DescComingMany ("The apps for %s are coming
	// soon.") end the description with the computers, %s being their names
	// joined by list. And is the language's "and" for that list.
	DescAvailable, DescComingOne, DescComingMany, And string
	// Head and Sub open the page.
	Head, Sub string
	// Phones and Computer head the two groups.
	Phones, Computer string
	// NotYet stands for a computer's app that has no file yet: it's coming,
	// with no download.
	NotYet string
	// NotAvailable stands for a computer's app the site says isn't available
	// (KIFF_DESKTOP_NOT_AVAILABLE).
	NotAvailable string
	// AvailableVersion, AvailableSize and AvailableSHA describe a released
	// computer's file: "Version %s", "%s" (its size, worded by sizeText), and
	// the SHA-256's label. CheckHow says how to check the file against it.
	// Empty in a language, the English stands until its translation comes.
	AvailableVersion, AvailableSize, AvailableSHA, CheckHow string
	// DownloadFor is the button for a computer's app: "Download for %s".
	DownloadFor string
	// WindowsSigning stands in Windows' row while its installer isn't signed
	// (WindowsSigned off): it's coming, and there's no download until then.
	// NotarizedMac stands under macOS 12's download: signed and notarized by
	// Apple. Libfido2Linux stands under Linux's: what approving on it needs.
	WindowsSigning, NotarizedMac, Libfido2Linux string
	// SignedBy and TheKey are Linux's signature line, in the file's details:
	// "SHA256SUMS is signed with the release key" (then the fingerprint and
	// the two files) and the key's link.
	SignedBy, TheKey string
	// QRAlt and QRCaption are the QR code a computer sees.
	QRAlt, QRCaption string
	// Cost leads to the prices: Cost, then the link named Pricing.
	Cost, Pricing string
	// ComingAppStore stands for the App Store badge until the listing is live.
	ComingAppStore string
	// IPhoneIPad names Apple's devices before their requirement: "iPhone and iPad, %s".
	IPhoneIPad string
	// Needs is each app's requirement, by OS: "android", "ios", "macos13" (the
	// Catalyst Mac app), "macos" (tafs-macos, for macOS 12), "windows",
	// "linux". The versions are the builds' own (download.templ).
	Needs map[string]string
}

var downloadEN = downloadWords{
	Lang:           "en",
	Title:          "Get tafs — KIFF",
	Description:    "Get tafs on Google Play for Android. iPhone and iPad are coming to the App Store.",
	DescAvailable:  "Download tafs for %s.",
	DescComingOne:  "The app for %s is coming soon.",
	DescComingMany: "The apps for %s are coming soon.",
	And:            "and",
	Head:           "Get tafs",
	Sub:            "AI agents for your business's routine jobs, each inside a Card you set. Anything bigger waits for your approval on your phone.",
	Phones:         "Phone and tablet",
	Computer:       "Computer",
	NotYet:         "Coming soon",
	NotAvailable:   "Not available",
	// The released file's lines.
	AvailableVersion: "Version %s",
	AvailableSize:    "%s",
	AvailableSHA:     "SHA-256:",
	CheckHow:         "Check that the file's SHA-256 matches before you open it.",
	DownloadFor:      "Download for %s",
	WindowsSigning:   "Coming soon, once its installer is signed.",
	NotarizedMac:     "Signed with Apple's Developer ID and notarized by Apple.",
	Libfido2Linux:    "To approve with a security key, Linux needs libfido2. Without it, you view on this computer and approve on your phone.",
	SignedBy:         "SHA256SUMS is signed with the release key",
	TheKey:           "the key",
	QRAlt:            "QR code that opens kiff.dev/tafs/download",
	QRCaption:        "Scan with your phone's camera to open this page there.",
	Cost:             "What it costs:",
	Pricing:          "pricing",
	ComingAppStore:   "Coming to the App Store",
	IPhoneIPad:       "iPhone and iPad, %s",
	Needs: map[string]string{
		"android": downloadAndroidNeeds,
		"ios":     downloadIOSNeeds,
		"macos13": "macOS 13 or later",
		"macos":   "macOS 12 (Monterey) only",
		"windows": "Windows 10 or 11",
		"linux":   "Ubuntu 24.04 or later",
	},
}

// needs is an app's requirement in this language, or the build's own words.
func (w downloadWords) needs(os, fallback string) string {
	if n, ok := w.Needs[os]; ok {
		return n
	}
	return fallback
}

// or is w's word, or the English when this language has none yet.
func or(w, en string) string {
	if w != "" {
		return w
	}
	return en
}

func (w downloadWords) notAvailable() string { return or(w.NotAvailable, downloadEN.NotAvailable) }

func (w downloadWords) version(v string) string {
	return strings.Replace(or(w.AvailableVersion, downloadEN.AvailableVersion), "%s", v, 1)
}

func (w downloadWords) size(n int64) string {
	return strings.Replace(or(w.AvailableSize, downloadEN.AvailableSize), "%s", sizeText(n), 1)
}

func (w downloadWords) sha() string { return or(w.AvailableSHA, downloadEN.AvailableSHA) }

func (w downloadWords) checkHow() string { return or(w.CheckHow, downloadEN.CheckHow) }

// note is the line under a computer's download, if it has one: macOS 12's and
// Linux's.
func (w downloadWords) note(os string) string {
	switch {
	case os == "macos":
		return or(w.NotarizedMac, downloadEN.NotarizedMac)
	case os == "linux":
		return or(w.Libfido2Linux, downloadEN.Libfido2Linux)
	}
	return ""
}

// windowsSigning is Windows' line while its installer isn't signed.
func (w downloadWords) windowsSigning() string {
	return or(w.WindowsSigning, downloadEN.WindowsSigning)
}

func (w downloadWords) signedBy() string { return or(w.SignedBy, downloadEN.SignedBy) }
func (w downloadWords) theKey() string   { return or(w.TheKey, downloadEN.TheKey) }

// description is the page's meta, og and twitter description: the phones'
// sentences, then the computers whose release is out, then those still to
// come, each named from apps (the same release data the rows read). A
// computer that isn't available is in neither, and an empty group says
// nothing.
//
// A computer with more than one download (the Mac) is named once, by its
// Family: out if any of its downloads is, else coming if any is coming.
func (v DownloadView) description() string {
	w := v.words()
	var order []string
	state := map[string]string{}
	for _, a := range v.Desktop {
		name := a.Family
		if name == "" {
			name = a.Name
		}
		cur, seen := state[name]
		if !seen {
			order = append(order, name)
		}
		switch {
		case a.State == desktop.StateAvailable:
			state[name] = desktop.StateAvailable
		case a.State == desktop.StateComingSoon && cur != desktop.StateAvailable:
			state[name] = desktop.StateComingSoon
		case !seen:
			state[name] = a.State
		}
	}
	var avail, coming []string
	for _, name := range order {
		switch state[name] {
		case desktop.StateAvailable:
			avail = append(avail, name)
		case desktop.StateComingSoon:
			coming = append(coming, name)
		}
	}
	out := w.Description
	if len(avail) > 0 {
		out += " " + strings.Replace(or(w.DescAvailable, downloadEN.DescAvailable), "%s", w.list(avail), 1)
	}
	switch {
	case len(coming) == 1:
		out += " " + strings.Replace(or(w.DescComingOne, downloadEN.DescComingOne), "%s", coming[0], 1)
	case len(coming) > 1:
		out += " " + strings.Replace(or(w.DescComingMany, downloadEN.DescComingMany), "%s", w.list(coming), 1)
	}
	return out
}

// list joins names the way the language does: "A", "A and B", "A, B and C".
func (w downloadWords) list(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " " + or(w.And, downloadEN.And) + " " + names[len(names)-1]
}

// sizeText is a file's size as the page says it: "15 MB", "850 KB".
func sizeText(n int64) string {
	switch {
	case n >= 1_000_000:
		return strconv.FormatInt((n+500_000)/1_000_000, 10) + " MB"
	case n >= 1_000:
		return strconv.FormatInt((n+500)/1_000, 10) + " KB"
	}
	return strconv.FormatInt(n, 10) + " B"
}

func (w downloadWords) downloadFor(name string) string {
	return strings.Replace(w.DownloadFor, "%s", name, 1)
}

func (w downloadWords) iPhoneIPad() string {
	return strings.Replace(w.IPhoneIPad, "%s", w.needs("ios", downloadIOSNeeds), 1)
}

// inEnglish marks a link to an English-only page, on a translated page.
func (w downloadWords) inEnglish() string {
	if w.Lang == "en" || w.Lang == "" {
		return ""
	}
	return " " + components.InEnglish(w.Lang)
}

type deviceKey struct{}

// WithDevice carries the device asking ("ios", "android", "macos",
// "windows", "linux" or "") to a page rendered without its request.
func WithDevice(ctx context.Context, device string) context.Context {
	return context.WithValue(ctx, deviceKey{}, device)
}

// DeviceFrom is the device WithDevice carried, or "".
func DeviceFrom(ctx context.Context) string {
	d, _ := ctx.Value(deviceKey{}).(string)
	return d
}

// downloadIn is /tafs/download in a language, for the device the request
// carried (WithDevice).
func downloadIn(w downloadWords, o OfferLinks) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, out io.Writer) error {
		v := DownloadView{PlayURL: o.PlayURL, AppStoreLive: o.AppStoreLive, Device: DeviceFrom(ctx), Desktop: DesktopApps(), W: w}
		return Download(v).Render(ctx, out)
	})
}
