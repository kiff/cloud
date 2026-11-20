package region

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const fixture = `192.0.2.0,192.0.2.255,FR
198.51.100.0,198.51.100.255,CN
`

func TestTheWebGate(t *testing.T) {
	tb, err := Read(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	var gotCountry string
	refused := func(w http.ResponseWriter, r *http.Request, country string) {
		gotCountry = country
		Refused(w, r, country)
	}
	h := NewGate(tb, SessionRoutes, refused, nil).Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	cases := []struct {
		method, path, remote, xff string
		want                      int
	}{
		{"GET", "/sign-in", "127.0.0.1:1", "192.0.2.1", 204},
		{"GET", "/sign-in", "127.0.0.1:1", "198.51.100.1", 403},
		{"POST", "/sign-in/callback", "127.0.0.1:1", "198.51.100.1", 403},
		{"GET", "/sign-in/app", "127.0.0.1:1", "8.8.8.8", 403},
		{"GET", "/auth/device", "[::1]:1", "198.51.100.1", 403},
		{"GET", "/auth/device", "127.0.0.1:1", "127.0.0.1", 204},
		{"GET", "/sign-in", "10.0.0.7:1", "192.0.2.1", 403},
		{"GET", "/sign-in", "127.0.0.1:1", "172.16.0.9", 403},
		{"POST", "/sign-in/app", "127.0.0.1:1", "198.51.100.1", 403},
		{"POST", "/auth/device", "127.0.0.1:1", "198.51.100.1", 403},
		{"POST", "/sign-in/app", "127.0.0.1:1", "192.0.2.1", 204},
		{"GET", "/oauth/consent", "127.0.0.1:1", "198.51.100.1", 403},
		{"POST", "/oauth/device", "127.0.0.1:1", "198.51.100.1", 403},
		{"GET", "/oauth/consent", "127.0.0.1:1", "192.0.2.1", 204},
		{"GET", "/dashboard", "127.0.0.1:1", "198.51.100.1", 204},
		{"GET", "/", "127.0.0.1:1", "198.51.100.1", 204},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		r.RemoteAddr = c.remote
		r.Header.Set("X-Forwarded-For", c.xff)
		r.Header.Set("Accept-Language", "fr-FR,fr;q=0.9")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s %s from %s: %d, want %d", c.method, c.path, c.xff, w.Code, c.want)
		}
		if w.Code == 403 && (!strings.Contains(w.Body.String(), "KIFF n&#39;est pas disponible") || w.Header().Get("Cache-Control") != "no-store") {
			t.Errorf("%s %s: refusal page %q", c.method, c.path, w.Body.String())
		}
	}
	if gotCountry != "CN" {
		t.Errorf("the refuser got %q, want the last refusal's country CN", gotCountry)
	}
}

// Each page's approval is its POST, and the API can't stand in for it (the
// web calls the API from the same box), so a gated path is gated for both
// GET and POST. POST /auth/device and POST /sign-in/app were once missed.
func TestEveryGatedPageIsGatedForItsPostToo(t *testing.T) {
	gated := map[string]bool{}
	for _, r := range SessionRoutes {
		gated[r] = true
	}
	for _, r := range SessionRoutes {
		method, path, _ := strings.Cut(r, " ")
		other := "POST"
		if method == "POST" {
			other = "GET"
		}
		if !gated[other+" "+path] {
			t.Errorf("%s is gated but %s %s isn't", r, other, path)
		}
	}
}
