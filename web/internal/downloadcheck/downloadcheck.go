// Package downloadcheck checks that the live /tafs/download shows what the
// computers' published releases say it should: each computer's row (available
// with its version, size and SHA-256, coming soon, or not available) and the
// page's meta, og and twitter description.
//
// It computes what the page should show with the page's own code: package
// desktop reads the same releases the site reads, under the same rules, and
// package pages builds the rows (DesktopApps) and the description
// (DownloadDescription). So there is no second data source and no list of
// computers of its own.
package downloadcheck

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/content/desktop"
	"github.com/kiff/cloud/web/internal/pages"
)

// DefaultGrace is how long a release just published may be missing from the
// page before it counts as a mismatch: the site reads the releases again
// every 10 minutes.
const DefaultGrace = 15 * time.Minute

// Config is one check.
type Config struct {
	// Site is the site's root, e.g. "https://kiff.dev".
	Site string
	// Langs are the page's languages to check; "en" is /tafs/download, any
	// other /<lang>/tafs/download.
	Langs []string
	// Releases is the deploy's KIFF_DESKTOP_RELEASES == "on". Off, the site
	// reads nothing and every computer is coming soon, so that is expected.
	Releases bool
	// NotAvailable is the deploy's KIFF_DESKTOP_NOT_AVAILABLE.
	NotAvailable string
	// GitHubAPI is GitHub's API root; empty is api.github.com.
	GitHubAPI string
	// Token, when set, is sent to GitHub for its higher rate limit.
	Token string
	// Client fetches the page and the releases; nil is a 15-second client.
	Client *http.Client
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Grace is DefaultGrace when zero.
	Grace time.Duration
}

// Finding is one thing the live page shows differently from the releases.
type Finding struct {
	// Lang is the page's language, "en" for /tafs/download.
	Lang string
	// What names it: "linux row", "description", "page".
	What string
	// Want and Got are what the releases say and what the page shows.
	Want, Got string
	// Pending is a release published within the grace: not yet a mismatch.
	Pending bool
}

// Report is a check's result.
type Report struct {
	// Expected is each computer as the releases say the page should show it.
	Expected []pages.DesktopApp
	// Langs are the languages checked.
	Langs    []string
	Findings []Finding
}

// Mismatch is any finding outside the grace.
func (r Report) Mismatch() bool {
	for _, f := range r.Findings {
		if !f.Pending {
			return true
		}
	}
	return false
}

// Text is the report in plain words, for the run's log and the comment.
func (r Report) Text() string {
	var b strings.Builder
	switch {
	case r.Mismatch():
		b.WriteString("The live /tafs/download doesn't match the computers' published releases.\n")
	case len(r.Findings) > 0:
		b.WriteString("The live /tafs/download matches, except for releases published in the last minutes, which the page reads within 10 minutes.\n")
	default:
		b.WriteString("The live /tafs/download matches the computers' published releases.\n")
	}
	b.WriteString("\nWhat the releases say:\n")
	for _, a := range r.Expected {
		b.WriteString("- " + a.Name + ": " + rowText(a.State, a.Version, a.Size, a.SHA256) + "\n")
	}
	if len(r.Findings) > 0 {
		b.WriteString("\nWhat differs:\n")
		for _, f := range r.Findings {
			tag := ""
			if f.Pending {
				tag = " (pending: just published)"
			}
			fmt.Fprintf(&b, "- %s, %s%s:\n  - wanted: %s\n  - live: %s\n", pagePath(f.Lang), f.What, tag, f.Want, f.Got)
		}
	}
	b.WriteString("\nChecked: " + strings.Join(pathsOf(r.Langs), ", ") + ".\n")
	return b.String()
}

func pathsOf(langs []string) []string {
	out := make([]string, len(langs))
	for i, l := range langs {
		out[i] = pagePath(l)
	}
	return out
}

func pagePath(lang string) string {
	if lang == "" || lang == "en" {
		return "/tafs/download"
	}
	return "/" + lang + "/tafs/download"
}

func rowText(state, version string, size int64, sha string) string {
	switch state {
	case desktop.StateAvailable:
		return fmt.Sprintf("available, %s, %d bytes, SHA-256 %s", version, size, sha)
	case desktop.StateNotAvailable:
		return "not available"
	case desktop.StateComingSoon:
		return "coming soon"
	case "":
		return "no row"
	}
	return state
}

// ErrReleases is a release read that failed: what the page should show can't
// be known, so nothing is compared.
var ErrReleases = errors.New("the computers' releases couldn't be read")

// Run reads the releases, works out what the page should show, fetches each
// language's live page and compares. An error means the check couldn't be
// made (a release read failed); a page that can't be fetched is a finding.
func Run(ctx context.Context, cfg Config) (Report, error) {
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	grace := cfg.Grace
	if grace <= 0 {
		grace = DefaultGrace
	}
	langs := cfg.Langs
	if len(langs) == 0 {
		langs = []string{"en"}
	}

	desktop.Reset()
	if cfg.Releases {
		desktop.HoldNotAvailable(cfg.NotAvailable)
		src := &desktop.Source{API: cfg.GitHubAPI, Token: cfg.Token, Client: client}
		if err := src.Read(ctx); err != nil {
			return Report{Langs: langs}, fmt.Errorf("%w: %v", ErrReleases, err)
		}
	}
	expected := pages.DesktopApps()
	published := map[string]time.Time{}
	for _, a := range expected {
		if _, r := desktop.Of(a.OS); !r.Published.IsZero() {
			published[a.OS] = r.Published
		}
	}
	rep := Report{Expected: expected, Langs: langs}

	for _, lang := range langs {
		if !pages.DownloadLangs(lang) {
			rep.Findings = append(rep.Findings, Finding{Lang: lang, What: "page", Want: "a language the site serves", Got: "unknown language " + strconv.Quote(lang)})
			continue
		}
		body, err := fetch(ctx, client, strings.TrimRight(cfg.Site, "/")+pagePath(lang))
		if err != nil {
			rep.Findings = append(rep.Findings, Finding{Lang: lang, What: "page", Want: "the page, 200", Got: err.Error()})
			continue
		}
		live := parse(body)

		// The rows.
		pending := false
		liveApps := make([]pages.DesktopApp, len(expected))
		for i, want := range expected {
			got, ok := live.rows[want.OS]
			liveApps[i] = want
			liveApps[i].State = got.state
			if ok && got == rowOf(want) {
				continue
			}
			f := Finding{Lang: lang, What: want.OS + " row", Want: rowText(want.State, want.Version, want.Size, want.SHA256)}
			if ok {
				f.Got = rowText(got.state, got.version, got.size, got.sha256)
			} else {
				f.Got = "no row for " + want.OS
			}
			if p, ok := published[want.OS]; ok && want.State == desktop.StateAvailable && now().Sub(p) < grace {
				f.Pending = true
				pending = true
			}
			rep.Findings = append(rep.Findings, f)
		}

		// The description: three tags, one text, the one the rows give.
		want := pages.DownloadDescription(lang, expected)
		tags := []string{"description", "og:description", "twitter:description"}
		var got string
		equal := true
		for i, t := range tags {
			v, ok := live.meta[t]
			if !ok {
				rep.Findings = append(rep.Findings, Finding{Lang: lang, What: t, Want: want, Got: "no " + t + " tag"})
				equal = false
				continue
			}
			if i == 0 {
				got = v
			} else if v != got {
				rep.Findings = append(rep.Findings, Finding{Lang: lang, What: t, Want: "the same text as description: " + got, Got: v})
				equal = false
			}
		}
		if equal && got != want {
			f := Finding{Lang: lang, What: "description", Want: want, Got: got}
			// While a release just published hasn't reached the rows, the
			// description follows the rows the page shows.
			if pending && got == pages.DownloadDescription(lang, liveApps) {
				f.Pending = true
			}
			rep.Findings = append(rep.Findings, f)
		}
	}
	return rep, nil
}

func fetch(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "kiff.dev download-check")
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %d", url, res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// row is a computer's row as the page renders it, from its data attributes.
type row struct {
	state, version, sha256 string
	size                   int64
}

func rowOf(a pages.DesktopApp) row {
	return row{state: a.State, version: a.Version, sha256: a.SHA256, size: a.Size}
}

type page struct {
	rows map[string]row
	meta map[string]string
}

var (
	tagRE  = regexp.MustCompile(`<(div|meta)\s[^>]*>`)
	attrRE = regexp.MustCompile(`([a-zA-Z_:][-a-zA-Z0-9_:.]*)="([^"]*)"`)
)

// parse reads the rows (a div with data-os and data-state) and the meta tags.
// templ writes every attribute double-quoted and escaped.
func parse(body string) page {
	p := page{rows: map[string]row{}, meta: map[string]string{}}
	for _, m := range tagRE.FindAllStringSubmatch(body, -1) {
		attrs := map[string]string{}
		for _, a := range attrRE.FindAllStringSubmatch(m[0], -1) {
			attrs[strings.ToLower(a[1])] = html.UnescapeString(a[2])
		}
		switch m[1] {
		case "div":
			os, ok := attrs["data-os"]
			if !ok {
				continue
			}
			if _, ok := attrs["data-state"]; !ok {
				continue
			}
			size, _ := strconv.ParseInt(attrs["data-size"], 10, 64)
			p.rows[os] = row{state: attrs["data-state"], version: attrs["data-version"], sha256: attrs["data-sha256"], size: size}
		case "meta":
			key := attrs["name"]
			if key == "" {
				key = attrs["property"]
			}
			if key == "description" || key == "og:description" || key == "twitter:description" {
				if _, seen := p.meta[key]; !seen {
					p.meta[key] = attrs["content"]
				}
			}
		}
	}
	return p
}
