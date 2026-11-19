// Package archive serves dated, unchanging copies of the public technical
// documentation and the security page. The DPA and the Terms refer to a
// dated revision, so what they point at must stay as it was when it was
// captured, whatever the current pages say later.
//
// A revision is a directory under revisions/ written once by
// cmd/archive-docs: the rendered body of each page, the images they show,
// and a manifest listing every file's SHA-256. The manifest's own SHA-256
// is pinned in pinned.go. A revision whose files or manifest no longer
// match is refused, so changing a captured byte fails the tests and cannot
// be served. A new revision is a new directory; an old one is never edited.
package archive

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed all:revisions
var revisionsFS embed.FS

// Kind is what a captured page is.
type Kind string

const (
	KindDocs     Kind = "docs"
	KindSecurity Kind = "security"
)

// SecuritySlug is the security page's slug within a revision.
const SecuritySlug = "security"

// Page is one captured page in a revision's manifest.
type Page struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Kind  Kind   `json:"kind"`
	// File is the captured body, relative to the revision's directory.
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	// Current is the live page this was captured from.
	Current string `json:"current"`
}

// Asset is a captured image a page shows.
type Asset struct {
	File        string `json:"file"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type"`
}

// Manifest describes one revision.
type Manifest struct {
	Revision     string    `json:"revision"`
	CapturedAt   time.Time `json:"captured_at"`
	SourceCommit string    `json:"source_commit"`
	Pages        []Page    `json:"pages"`
	Assets       []Asset   `json:"assets"`
}

// Revision is a verified revision: its manifest, page bodies and assets.
type Revision struct {
	Manifest
	fsys   fs.FS
	bodies map[string]template.HTML
	assets map[string]Asset
}

// RevisionPattern is the form of a revision id: the capture date.
var RevisionPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

var (
	once    sync.Once
	byID    map[string]*Revision
	ordered []*Revision
	loadErr error
)

func load() {
	byID = map[string]*Revision{}
	entries, err := fs.ReadDir(revisionsFS, "revisions")
	if err != nil {
		loadErr = fmt.Errorf("archive: read revisions: %w", err)
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rev, err := verify(revisionsFS, pinned, e.Name())
		if err != nil {
			loadErr = err
			return
		}
		byID[rev.Revision] = rev
		ordered = append(ordered, rev)
	}
	// Newest first.
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Revision > ordered[j].Revision })
	for id := range pinned {
		if byID[id] == nil {
			loadErr = fmt.Errorf("archive: pinned revision %s is missing", id)
			return
		}
	}
}

// verify reads one revision from fsys and checks it against its pin and
// its manifest.
func verify(fsys fs.FS, pins map[string]string, id string) (*Revision, error) {
	if !RevisionPattern.MatchString(id) {
		return nil, fmt.Errorf("archive: %q is not a revision id", id)
	}
	dir := path.Join("revisions", id)
	raw, err := fs.ReadFile(fsys, path.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("archive: revision %s: %w", id, err)
	}
	want, ok := pins[id]
	if !ok {
		return nil, fmt.Errorf("archive: revision %s is not pinned in pinned.go", id)
	}
	if got := Digest(raw); got != want {
		return nil, fmt.Errorf("archive: revision %s manifest changed: sha256 %s, pinned %s", id, got, want)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("archive: revision %s manifest: %w", id, err)
	}
	if m.Revision != id {
		return nil, fmt.Errorf("archive: directory %s holds revision %q", id, m.Revision)
	}
	rev := &Revision{Manifest: m, fsys: fsys, bodies: map[string]template.HTML{}, assets: map[string]Asset{}}
	files := map[string]bool{"manifest.json": true}
	check := func(file, sum string) ([]byte, error) {
		if !fs.ValidPath(file) || file == "." {
			return nil, fmt.Errorf("archive: revision %s has invalid file %q", id, file)
		}
		if files[file] {
			return nil, fmt.Errorf("archive: revision %s lists file %s twice", id, file)
		}
		files[file] = true
		b, err := fs.ReadFile(fsys, path.Join(dir, file))
		if err != nil {
			return nil, fmt.Errorf("archive: revision %s: %w", id, err)
		}
		if got := Digest(b); got != sum {
			return nil, fmt.Errorf("archive: revision %s: %s changed: sha256 %s, manifest %s", id, file, got, sum)
		}
		return b, nil
	}
	for _, p := range m.Pages {
		b, err := check(p.File, p.SHA256)
		if err != nil {
			return nil, err
		}
		if _, dup := rev.bodies[p.Slug]; dup {
			return nil, fmt.Errorf("archive: revision %s lists %s twice", id, p.Slug)
		}
		// Captured from our own rendered pages, never from user input.
		rev.bodies[p.Slug] = template.HTML(b)
	}
	for _, a := range m.Assets {
		if _, err := check(a.File, a.SHA256); err != nil {
			return nil, err
		}
		name := path.Base(a.File)
		if _, dup := rev.assets[name]; dup {
			return nil, fmt.Errorf("archive: revision %s lists asset name %s twice", id, name)
		}
		rev.assets[name] = a
	}
	if err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		file := strings.TrimPrefix(p, dir+"/")
		if !d.Type().IsRegular() || !files[file] {
			return fmt.Errorf("archive: revision %s contains unlisted or non-regular file %s", id, file)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return rev, nil
}

// Digest is the hex SHA-256 of b.
func Digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Revisions returns every verified revision, newest first.
func Revisions() ([]*Revision, error) {
	once.Do(load)
	if loadErr != nil {
		return nil, loadErr
	}
	return ordered, nil
}

// Lookup returns a verified revision by id.
func Lookup(id string) (*Revision, bool, error) {
	once.Do(load)
	if loadErr != nil {
		return nil, false, loadErr
	}
	r, ok := byID[id]
	return r, ok, nil
}

// Page returns a captured page and its body.
func (r *Revision) Page(slug string) (Page, template.HTML, bool) {
	for _, p := range r.Pages {
		if p.Slug == slug {
			return p, r.bodies[slug], true
		}
	}
	return Page{}, "", false
}

// DocsPages are the revision's documentation pages, in capture order.
func (r *Revision) DocsPages() []Page {
	var out []Page
	for _, p := range r.Pages {
		if p.Kind == KindDocs {
			out = append(out, p)
		}
	}
	return out
}

// Asset returns a captured image by file name.
func (r *Revision) Asset(name string) (Asset, []byte, bool) {
	a, ok := r.assets[name]
	if !ok {
		return Asset{}, nil, false
	}
	b, err := fs.ReadFile(r.fsys, path.Join("revisions", r.Revision, a.File))
	if err != nil {
		return Asset{}, nil, false
	}
	return a, b, true
}

// Href is where a revision's page is served.
func Href(rev, slug string) string { return "/docs/archive/" + rev + "/" + slug }

// IndexHref is where a revision's index is served.
func IndexHref(rev string) string { return "/docs/archive/" + rev }

// AssetHref is where a revision's image is served.
func AssetHref(rev, name string) string { return "/docs/archive/" + rev + "/assets/" + name }

// PrivatePrefixes are signed-in or operator surfaces. An archived page
// keeps the words of a link to them but not the link, the same set
// /llms.txt never lists.
var PrivatePrefixes = []string{"/ops", "/dashboard", "/sign-in"}

func private(href string) bool {
	for _, p := range PrivatePrefixes {
		if href == p || strings.HasPrefix(href, p+"/") || strings.HasPrefix(href, p+"?") || strings.HasPrefix(href, p+"#") {
			return true
		}
	}
	return false
}

var (
	anchorRe = regexp.MustCompile(`(?s)<a\b([^>]*?)\bhref="([^"]*)"([^>]*)>(.*?)</a>`)
	srcRe    = regexp.MustCompile(`\bsrc="(/static/img/[^"]+)"`)
)

// Rewrite points a captured body's own links into its revision:
//   - /docs and /docs/<slug> of a captured page go to that page here;
//   - /security goes to the captured security page;
//   - a link to a private surface keeps its words and loses the link;
//   - every other link (another public page, an external site) is kept.
//
// Images under /static/img are listed in assets (their path → file name)
// and served from the revision, so a later change to the image does not
// change the archived page.
func Rewrite(body, rev string, captured map[string]bool, assets map[string]string) string {
	body = anchorRe.ReplaceAllStringFunc(body, func(a string) string {
		m := anchorRe.FindStringSubmatch(a)
		before, href, after, text := m[1], m[2], m[3], m[4]
		if private(href) {
			return `<span class="archive-unlinked">` + text + `</span>`
		}
		if to, ok := archivedHref(href, rev, captured); ok {
			return `<a` + before + `href="` + to + `"` + after + `>` + text + `</a>`
		}
		return a
	})
	return srcRe.ReplaceAllStringFunc(body, func(s string) string {
		src := srcRe.FindStringSubmatch(s)[1]
		if name, ok := assets[src]; ok {
			return `src="` + AssetHref(rev, name) + `"`
		}
		return s
	})
}

func archivedHref(href, rev string, captured map[string]bool) (string, bool) {
	p, frag, _ := strings.Cut(href, "#")
	if frag != "" {
		frag = "#" + frag
	}
	switch {
	case p == "/docs" || p == "/docs/":
		return IndexHref(rev) + frag, true
	case p == "/security":
		if captured[SecuritySlug] {
			return Href(rev, SecuritySlug) + frag, true
		}
	case strings.HasPrefix(p, "/docs/"):
		if slug := strings.TrimPrefix(p, "/docs/"); captured[slug] && slug != SecuritySlug {
			return Href(rev, slug) + frag, true
		}
	}
	return "", false
}
