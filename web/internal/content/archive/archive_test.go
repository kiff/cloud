package archive

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kiff/cloud/web/internal/content/docs"
)

// Every pinned revision loads and verifies; the embedded one has every
// docs page that existed and the security page.
func TestPinnedRevisionsVerify(t *testing.T) {
	revs, err := Revisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != len(pinned) {
		t.Fatalf("%d revisions, %d pinned", len(revs), len(pinned))
	}
	r, ok, err := Lookup("2026-10-09")
	if err != nil || !ok {
		t.Fatalf("2026-10-09: %v %v", ok, err)
	}
	if len(r.SourceCommit) != 40 {
		t.Errorf("source commit %q", r.SourceCommit)
	}
	if _, body, ok := r.Page(SecuritySlug); !ok || !strings.Contains(string(body), `class="security"`) {
		t.Error("the security page was not captured")
	}
	for _, p := range r.DocsPages() {
		if _, body, ok := r.Page(p.Slug); !ok || len(body) == 0 {
			t.Errorf("docs page %s is empty", p.Slug)
		}
	}
	// It was captured from the pages that exist now; a page added later
	// belongs to a later revision, so this only checks the first one.
	if got, want := len(r.DocsPages()), len(docs.Pages()); got > want {
		t.Errorf("revision has %d docs pages, the site %d", got, want)
	}
}

// copyRevision copies the embedded revision into a map filesystem that a
// test can change.
func copyRevision(t *testing.T, id string) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	err := fs.WalkDir(revisionsFS, "revisions/"+id, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(revisionsFS, p)
		out[p] = &fstest.MapFile{Data: b}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A captured byte that changes, a manifest that changes, or a revision no
// one pinned is refused: an archived page cannot change after the DPA or
// the Terms point at it.
func TestAChangedRevisionIsRefused(t *testing.T) {
	const id = "2026-10-09"
	if _, err := verify(copyRevision(t, id), pinned, id); err != nil {
		t.Fatalf("an unchanged copy is refused: %v", err)
	}

	page := copyRevision(t, id)
	f := page["revisions/"+id+"/pages/kiff-cards.html"]
	f.Data = append(append([]byte{}, f.Data...), ' ')
	if _, err := verify(page, pinned, id); err == nil || !strings.Contains(err.Error(), "kiff-cards.html changed") {
		t.Errorf("a changed page: %v", err)
	}

	img := copyRevision(t, id)
	g := img["revisions/"+id+"/assets/card-issued.jpg"]
	g.Data = append([]byte{0}, g.Data...)
	if _, err := verify(img, pinned, id); err == nil || !strings.Contains(err.Error(), "card-issued.jpg changed") {
		t.Errorf("a changed image: %v", err)
	}

	manifest := copyRevision(t, id)
	m := manifest["revisions/"+id+"/manifest.json"]
	m.Data = []byte(strings.Replace(string(m.Data), `"kind": "security"`, `"kind": "docs"`, 1))
	if _, err := verify(manifest, pinned, id); err == nil || !strings.Contains(err.Error(), "manifest changed") {
		t.Errorf("a changed manifest: %v", err)
	}

	if _, err := verify(copyRevision(t, id), map[string]string{}, id); err == nil || !strings.Contains(err.Error(), "not pinned") {
		t.Errorf("an unpinned revision: %v", err)
	}
}

func TestRewrite(t *testing.T) {
	captured := map[string]bool{"kiff-cards": true, SecuritySlug: true}
	assets := map[string]string{"/static/img/docs/card.jpg": "card.jpg"}
	in := `<a href="/docs/kiff-cards#limits">Cards</a> <a href="/docs">Docs</a> <a href="/docs/gone">Gone</a> ` +
		`<a href="/security#subprocessors">Sub-processors</a> <a href="/dashboard/cards">your <em>Cards</em></a> ` +
		`<a href="/ops">ops</a> <a href="/pricing">Pricing</a> <a href="https://example.com/x" target="_blank">x</a> ` +
		`<img src="/static/img/docs/card.jpg" alt=""> <img src="/static/img/other.png" alt="">`
	want := `<a href="/docs/archive/2026-10-09/kiff-cards#limits">Cards</a> <a href="/docs/archive/2026-10-09">Docs</a> <a href="/docs/gone">Gone</a> ` +
		`<a href="/docs/archive/2026-10-09/security#subprocessors">Sub-processors</a> <span class="archive-unlinked">your <em>Cards</em></span> ` +
		`<span class="archive-unlinked">ops</span> <a href="/pricing">Pricing</a> <a href="https://example.com/x" target="_blank">x</a> ` +
		`<img src="/docs/archive/2026-10-09/assets/card.jpg" alt=""> <img src="/static/img/other.png" alt="">`
	if got := Rewrite(in, "2026-10-09", captured, assets); got != want {
		t.Errorf("Rewrite:\n got %s\nwant %s", got, want)
	}
	// /dashboards is not /dashboard.
	if got := Rewrite(`<a href="/dashboards">d</a>`, "r", nil, nil); got != `<a href="/dashboards">d</a>` {
		t.Errorf("a public path sharing a prefix was unlinked: %s", got)
	}
}
