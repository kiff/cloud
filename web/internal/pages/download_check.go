package pages

import "github.com/a-h/templ"

// What the download check (cmd/downloadcheck) needs from /tafs/download: the
// page's own words and sentence builder, so the check computes what the page
// should say with the page's code, never a copy of it.

// downloadByLang is /tafs/download's words by language, as the site serves
// them: English at /tafs/download, each other at /<lang>/tafs/download.
var downloadByLang = map[string]downloadWords{
	"en": downloadEN,
	"de": downloadDE,
	"es": downloadES,
	"fr": downloadFR,
	"it": downloadIT,
	"nl": downloadNL,
	"pl": downloadPL,
	"pt": downloadPT,
}

// DownloadLangs reports whether lang has a /tafs/download page.
func DownloadLangs(lang string) bool {
	_, ok := downloadByLang[lang]
	return ok
}

// DownloadDescription is /tafs/download's meta, og and twitter description in
// lang, for these computers in these states: what the page renders for them.
// An unknown language is English.
func DownloadDescription(lang string, apps []DesktopApp) string {
	w, ok := downloadByLang[lang]
	if !ok {
		w = downloadEN
	}
	return DownloadView{Desktop: apps, W: w}.description()
}

// DownloadPage is /tafs/download in lang for these computers, as a visitor
// whose device the page can't name sees it. An unknown language is English.
func DownloadPage(lang string, apps []DesktopApp) templ.Component {
	w, ok := downloadByLang[lang]
	if !ok {
		w = downloadEN
	}
	return Download(DownloadView{Desktop: apps, W: w})
}
