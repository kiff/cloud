package handlers

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/content/archive"
)

func archiveMux() *http.ServeMux {
	mux := http.NewServeMux()
	h := DocsArchive(nil)
	mux.Handle("GET /docs/archive", h)
	mux.Handle("GET /docs/archive/{rev}", h)
	mux.Handle("GET /docs/archive/{rev}/{slug}", h)
	mux.Handle("GET /docs/archive/{rev}/assets/{name}", h)
	return mux
}

func get(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

// The index lists the revision, the revision lists its pages, and each
// archived page says it is an archived copy and links the current page.
func TestDocsArchiveServesARevision(t *testing.T) {
	mux := archiveMux()
	if rr := get(t, mux, "/docs/archive"); rr.Code != 200 || !strings.Contains(rr.Body.String(), `href="/docs/archive/2026-10-09"`) {
		t.Fatalf("index: %d", rr.Code)
	}
	rr := get(t, mux, "/docs/archive/2026-10-09")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `href="/docs/archive/2026-10-09/security"`) ||
		!strings.Contains(rr.Body.String(), `href="/docs/archive/2026-10-09/kiff-cards"`) {
		t.Fatalf("revision: %d", rr.Code)
	}
	rr = get(t, mux, "/docs/archive/2026-10-09/kiff-cards")
	body := rr.Body.String()
	for _, want := range []string{"Archived revision of 9 October 2026.", "This copy does not change.", `href="/docs/kiff-cards">Current version`} {
		if !strings.Contains(body, want) {
			t.Errorf("archived page missing %q", want)
		}
	}
	if rr := get(t, mux, "/docs/archive/2026-10-09/security"); rr.Code != 200 || !strings.Contains(rr.Body.String(), `href="/security">Current version`) {
		t.Errorf("archived security: %d", rr.Code)
	}
	rr = get(t, mux, "/docs/archive/2026-10-09/assets/card-issued.jpg")
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "image/jpeg" || !strings.Contains(rr.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %q %q", rr.Code, rr.Header().Get("Content-Type"), rr.Header().Get("Cache-Control"))
	}
	for _, p := range []string{"/docs/archive/1999-01-01", "/docs/archive/2026-10-09/nope", "/docs/archive/2026-10-09/assets/nope.jpg", "/docs/archive/latest"} {
		if rr := get(t, mux, p); rr.Code != 404 {
			t.Errorf("%s = %d, want 404", p, rr.Code)
		}
	}
}

var hrefRe = regexp.MustCompile(`\b(?:href|src)="([^"]*)"`)

// Every link on an archived page works and stays public: a link into the
// archive is served, nothing links a private surface or the live docs
// (which may change), and nothing is relative to the archive's path.
func TestArchivedLinksResolveAndStayPublic(t *testing.T) {
	mux := archiveMux()
	revs, err := archive.Revisions()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range revs {
		paths := []string{archive.IndexHref(r.Revision)}
		for _, p := range r.Pages {
			paths = append(paths, archive.Href(r.Revision, p.Slug))
		}
		for _, page := range paths {
			rr := get(t, mux, page)
			if rr.Code != 200 {
				t.Fatalf("%s = %d", page, rr.Code)
			}
			// Only the page's own content: the site layout's links are
			// the live site's, the same on every page.
			body := rr.Body.String()
			if i := strings.Index(body, `class="docs archive"`); i >= 0 {
				body = body[i:]
			}
			if i := strings.Index(body, "</main>"); i >= 0 {
				body = body[:i]
			}
			for _, m := range hrefRe.FindAllStringSubmatch(body, -1) {
				href := m[1]
				switch {
				case strings.HasPrefix(href, "#"), strings.HasPrefix(href, "mailto:"),
					strings.HasPrefix(href, "https://"), strings.HasPrefix(href, "http://"):
				case strings.HasPrefix(href, "/docs/archive"):
					target, _, _ := strings.Cut(href, "#")
					if rr := get(t, mux, target); rr.Code != 200 {
						t.Errorf("%s links %s, which is %d", page, href, rr.Code)
					}
				case strings.HasPrefix(href, "/docs"):
					// The current-version link in the banner is the one
					// live docs link an archived page carries.
					if !strings.Contains(body, `href="`+href+`">Current version`) {
						t.Errorf("%s links the live docs page %s", page, href)
					}
				case strings.HasPrefix(href, "/"):
					for _, p := range archive.PrivatePrefixes {
						if href == p || strings.HasPrefix(href, p+"/") {
							t.Errorf("%s links the private surface %s", page, href)
						}
					}
				default:
					t.Errorf("%s has a relative link %q", page, href)
				}
			}
		}
	}
}
