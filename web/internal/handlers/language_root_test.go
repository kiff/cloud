package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/components"
)

// A reader who types a bare language prefix ("/de/", "/de") lands on
// that language's /tafs, not a 404; "/de/anything-else" is not caught. A
// language that has no /tafs (Korean, Japanese: the legal texts only) lands on the
// home page.
func TestALanguagePrefixLandsOnItsTafs(t *testing.T) {
	targets := LanguageRootTargets()
	mux := http.NewServeMux()
	for from, to := range targets {
		pattern := "GET " + from
		if strings.HasSuffix(from, "/") {
			pattern += "{$}"
		}
		mux.Handle(pattern, LanguageRoot(to))
	}
	for _, lang := range components.PageLanguages {
		if lang == "en" {
			continue
		}
		want := "/" + lang + "/tafs"
		if !components.HasPage(lang, "/tafs") {
			want = "/"
		}
		for _, from := range []string{"/" + lang, "/" + lang + "/"} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, from, nil))
			if rec.Code != http.StatusFound {
				t.Fatalf("%s: status %d, want 302", from, rec.Code)
			}
			if got := rec.Header().Get("Location"); got != want {
				t.Errorf("%s: Location %q, want %q", from, got, want)
			}
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+lang+"/nothing-here", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("/%s/nothing-here: status %d, want 404 (the prefix route caught it)", lang, rec.Code)
		}
	}
	if _, ok := targets["/en"]; ok {
		t.Error("English has no prefix; /en should not be routed")
	}
}
