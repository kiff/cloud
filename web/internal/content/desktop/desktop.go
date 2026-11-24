// Package desktop says, for each computer tafs is built for, whether its app
// can be downloaded today, from data: a published GitHub release, read from
// GitHub's public API. Each download has its own channel (Channels): a
// repository, and for a repository that also releases other apps, the tag its
// releases carry and the file's name. The Mac has two: macOS 13 and later get
// the Catalyst app's notarized DMG from kiff/tafs (tags "mac-v…"), and macOS 12
// gets tafs-macos's DMG from kiff/tafs-macos.
//
// A download is available when its channel's latest release is published (not
// a draft, not a prerelease) and carries its file (.dmg, .exe, .AppImage) with
// GitHub's own SHA-256 digest of it. Then the
// site shows its link, version, size and SHA-256. Putting a build on the page
// is publishing its release; unpublishing takes it off at the next read. No
// code change and no deploy.
//
// A release with no such file, a draft, a prerelease or a repository with no
// release at all is "coming soon". A computer the deploy names in
// KIFF_DESKTOP_NOT_AVAILABLE is "not available". With KIFF_DESKTOP_RELEASES
// off, nothing is read and every computer is coming soon, as before.
package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A computer's app's state.
const (
	StateAvailable    = "available"
	StateComingSoon   = "coming-soon"
	StateNotAvailable = "not-available"
)

// Channel is where one download's release comes from.
type Channel struct {
	// Repo is the repository whose releases carry it.
	Repo string
	// Tag, when set, is the start of the tag its releases carry: the repository
	// also releases other apps, so its latest release may not be this one. The
	// newest published release whose tag starts so is read. Empty, the
	// repository's latest release is read.
	Tag string
	// Prefix and Suffix are the start and end of the file's name, matched
	// without case. Empty Prefix is any start.
	Prefix, Suffix string
}

// Channels is each download's channel, by the name the site gives it. These
// names are what KIFF_DESKTOP_NOT_AVAILABLE and the page's data-os say.
//
// Windows ships an x64 and an ARM64 installer, and the page offers the x64
// one, which most PCs run; an ARM64 installer won't run on an x64 PC. Linux's
// AppImage is x86_64 only.
//
// The Mac's Catalyst app shares kiff/tafs with the phone apps, so its releases
// are told apart by their tag ("mac-v1.0.0") and file ("tafs-mac-1.0.0.dmg").
var Channels = map[string]Channel{
	"macos13": {Repo: "kiff/tafs", Tag: "mac-v", Prefix: "tafs-mac-", Suffix: ".dmg"},
	"macos":   {Repo: "kiff/tafs-macos", Suffix: ".dmg"},
	"windows": {Repo: "kiff/tafs-windows", Suffix: "-x64.exe"},
	"linux":   {Repo: "kiff/tafs-linux", Suffix: "-x86_64.appimage"},
}

// Release is a computer's app as a published release offers it.
type Release struct {
	// Version is the release's tag ("v0.1-2").
	Version string
	// URL is the file's download link, on github.com.
	URL string
	// Size is the file's size in bytes.
	Size int64
	// SHA256 is GitHub's digest of the file, 64 lower-case hex digits.
	SHA256 string
	// Published is when the release was published, as GitHub says. The page
	// doesn't show it; the download check (cmd/downloadcheck) uses it to give a
	// release just published time to reach the page.
	Published time.Time
	// Sums and SumsSignature are the release's SHA256SUMS and its signature
	// (SHA256SUMS.asc), on github.com in the same release, when it carries
	// both: Linux's, signed with its release key. Empty otherwise.
	Sums, SumsSignature string
}

var (
	current      atomic.Pointer[map[string]Release]
	notAvailable atomic.Pointer[map[string]bool]
	generation   atomic.Uint64
)

func init() {
	empty := map[string]Release{}
	current.Store(&empty)
	none := map[string]bool{}
	notAvailable.Store(&none)
}

// Generation changes each time a computer's state does.
func Generation() uint64 { return generation.Load() }

// HoldNotAvailable names the computers the site says aren't available
// (KIFF_DESKTOP_NOT_AVAILABLE, comma-separated OS names). A computer named here
// is not available whatever its release says.
func HoldNotAvailable(list string) {
	m := map[string]bool{}
	for _, os := range strings.Split(list, ",") {
		if os = strings.ToLower(strings.TrimSpace(os)); os != "" {
			m[os] = true
		}
	}
	notAvailable.Store(&m)
	generation.Add(1)
}

// Of is a computer's state, and its release when it's available.
func Of(os string) (string, Release) {
	if (*notAvailable.Load())[os] {
		return StateNotAvailable, Release{}
	}
	if r, ok := (*current.Load())[os]; ok {
		return StateAvailable, r
	}
	return StateComingSoon, Release{}
}

var digestRE = regexp.MustCompile(`^sha256:([0-9a-f]{64})$`)

// errNoRelease is a repository with no published release: GitHub's 404 for
// releases/latest. The computer is then coming soon.
var errNoRelease = errors.New("no published release")

type wireRelease struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"`
		URL    string `json:"browser_download_url"`
	} `json:"assets"`
}

// decodeRelease reads a releases/latest answer for os: the release it offers,
// or ok false when it offers none the site can vouch for (a draft, a
// prerelease, another channel's tag, no file of the download's kind, a file
// with no digest or no github.com link). An answer that isn't a release at all
// is an error, and the last good state stands.
func decodeRelease(os string, raw []byte) (Release, bool, error) {
	var w wireRelease
	if err := json.Unmarshal(raw, &w); err != nil {
		return Release{}, false, fmt.Errorf("release: %w", err)
	}
	if strings.TrimSpace(w.TagName) == "" {
		return Release{}, false, errors.New("release: no tag")
	}
	r, ok := offered(os, w)
	return r, ok, nil
}

// decodeReleases reads a releases list for os (newest first, as GitHub lists
// them) and takes the newest published release on os's channel: the one
// releases/latest would be if the repository released nothing else. None is
// errNoRelease, as a repository with no release is.
func decodeReleases(os string, raw []byte) (Release, bool, error) {
	var ws []wireRelease
	if err := json.Unmarshal(raw, &ws); err != nil {
		return Release{}, false, fmt.Errorf("releases: %w", err)
	}
	ch := Channels[os]
	for _, w := range ws {
		if w.Draft || w.Prerelease || !strings.HasPrefix(strings.TrimSpace(w.TagName), ch.Tag) {
			continue
		}
		r, ok := offered(os, w)
		return r, ok, nil
	}
	return Release{}, false, errNoRelease
}

// offered is the file a release offers for os, if the site can vouch for it.
func offered(os string, w wireRelease) (Release, bool) {
	ch := Channels[os]
	if w.Draft || w.Prerelease || !strings.HasPrefix(strings.TrimSpace(w.TagName), ch.Tag) {
		return Release{}, false
	}
	for _, a := range w.Assets {
		name := strings.ToLower(a.Name)
		if !strings.HasPrefix(name, strings.ToLower(ch.Prefix)) || !strings.HasSuffix(name, strings.ToLower(ch.Suffix)) || a.Size <= 0 {
			continue
		}
		m := digestRE.FindStringSubmatch(a.Digest)
		u, err := url.Parse(a.URL)
		// The file must be that computer's own release, on its own repository.
		if m == nil || err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil ||
			!strings.HasPrefix(u.Path, "/"+ch.Repo+"/releases/download/") {
			continue
		}
		r := Release{Version: strings.TrimSpace(w.TagName), URL: a.URL, Size: a.Size, SHA256: m[1], Published: w.PublishedAt}
		r.Sums, r.SumsSignature = sums(ch, w)
		return r, true
	}
	return Release{}, false
}

// sums is a release's SHA256SUMS and SHA256SUMS.asc, when it carries both on
// github.com in its own repository's release; else both empty, so a page never
// links one without the other.
func sums(ch Channel, w wireRelease) (string, string) {
	var file, sig string
	for _, a := range w.Assets {
		u, err := url.Parse(a.URL)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil ||
			!strings.HasPrefix(u.Path, "/"+ch.Repo+"/releases/download/") || a.Size <= 0 {
			continue
		}
		switch a.Name {
		case "SHA256SUMS":
			file = a.URL
		case "SHA256SUMS.asc":
			sig = a.URL
		}
	}
	if file == "" || sig == "" {
		return "", ""
	}
	return file, sig
}

// Source reads the computers' releases and keeps their state current.
type Source struct {
	// API is GitHub's API root; empty is https://api.github.com.
	API string
	// Client reads it; nil is a client with a 5-second timeout.
	Client *http.Client
	// Every is how often the releases are read again; 0 is 10 minutes.
	Every time.Duration
	// Logf reports a read that failed; nil is silent.
	Logf func(format string, args ...any)
	// Token, when set, is sent as a bearer token, for GitHub's higher rate
	// limit. The site reads the public API without one; the download check
	// passes its workflow's token. The repositories are public either way.
	Token string
	once  sync.Once
}

// read is os's channel's latest release: the repository's releases/latest, or
// for a channel with a Tag, the newest published release on its list carrying
// that tag.
func (s *Source) read(ctx context.Context, os string) (Release, bool, error) {
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	api := strings.TrimRight(s.API, "/")
	if api == "" {
		api = "https://api.github.com"
	}
	ch := Channels[os]
	path := "/releases/latest"
	if ch.Tag != "" {
		path = "/releases?per_page=30"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/repos/"+ch.Repo+path, nil)
	if err != nil {
		return Release{}, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "kiff.dev")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	res, err := client.Do(req)
	if err != nil {
		return Release{}, false, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return Release{}, false, errNoRelease
	}
	if res.StatusCode != http.StatusOK {
		return Release{}, false, fmt.Errorf("release: GitHub answered %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return Release{}, false, err
	}
	if ch.Tag != "" {
		return decodeReleases(os, raw)
	}
	return decodeRelease(os, raw)
}

// Read reads every computer's release once and applies what it learnt. A
// computer whose read failed keeps its last state; one with no release, or no
// release the site can vouch for, is coming soon.
func (s *Source) Read(ctx context.Context) error {
	next := map[string]Release{}
	for os, r := range *current.Load() {
		next[os] = r
	}
	var errs []error
	for os := range Channels {
		r, ok, err := s.read(ctx, os)
		switch {
		case errors.Is(err, errNoRelease):
			delete(next, os)
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", os, err))
		case ok:
			next[os] = r
		default:
			delete(next, os)
		}
	}
	apply(next)
	return errors.Join(errs...)
}

// apply makes these releases the state, if they change it.
func apply(next map[string]Release) {
	cur := *current.Load()
	if len(cur) == len(next) {
		same := true
		for os, r := range next {
			if cur[os] != r {
				same = false
				break
			}
		}
		if same {
			return
		}
	}
	current.Store(&next)
	generation.Add(1)
}

// Start reads the releases now, then every Every until ctx ends. It returns
// after the first read, whatever it answered.
func (s *Source) Start(ctx context.Context) {
	s.once.Do(func() {
		every := s.Every
		if every <= 0 {
			every = 10 * time.Minute
		}
		s.readLogged(ctx)
		go func() {
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					s.readLogged(ctx)
				}
			}
		}()
	})
}

func (s *Source) readLogged(ctx context.Context) {
	if err := s.Read(ctx); err != nil && s.Logf != nil {
		s.Logf("desktop: a release wasn't read, its computer stays as it was: %v", err)
	}
}

// ResetForTest is for tests: no release, nothing held back.
func ResetForTest() { Reset() }

// Reset forgets every release and every computer held back, as at start. The
// download check uses it before its one read.
func Reset() {
	empty := map[string]Release{}
	current.Store(&empty)
	none := map[string]bool{}
	notAvailable.Store(&none)
	generation.Add(1)
}
