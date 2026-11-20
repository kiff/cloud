package region

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// Documentation ranges (RFC 5737, RFC 3849) stand in for real countries.
const fixture = `0.0.0.0,0.255.255.255,ZZ
192.0.2.0,192.0.2.255,FR
198.51.100.0,198.51.100.255,CN
203.0.113.0,203.0.113.127,RU
203.0.113.128,203.0.113.255,ZZ
2001:db8::,2001:db8:0:ffff:ffff:ffff:ffff:ffff,DE
2001:db8:1::,2001:db8:1:ffff:ffff:ffff:ffff:ffff,HK
`

func table(t *testing.T) *Table {
	t.Helper()
	tb, err := Read(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestTheListHasEveryListedCountry(t *testing.T) {
	// The list's own entries come first in Supported; territories follow.
	if len(Supported) <= ListedCount {
		t.Fatalf("Supported has %d codes, want the %d listed plus territories", len(Supported), ListedCount)
	}
	for _, c := range []string{"FR", "DE", "GB", "CH", "NO", "IS", "LI", "KR", "US", "BR", "IN", "UA", "PR", "RE"} {
		if !IsSupported(c) {
			t.Errorf("%s should be supported", c)
		}
	}
	// Not on the list.
	for _, c := range []string{"CN", "HK", "MO", "RU", "BY", "IR", "KP", "CU", "SY", "VE", "AF", "MM", "YE", "XK", "ZZ", ""} {
		if IsSupported(c) {
			t.Errorf("%s should not be supported", c)
		}
	}
	for c := range Supported {
		if len(c) != 2 || strings.ToUpper(c) != c {
			t.Errorf("%q isn't an upper-case alpha-2 code", c)
		}
	}
}

func TestCountry(t *testing.T) {
	tb := table(t)
	for addr, want := range map[string]string{
		"192.0.2.10":            "FR",
		"198.51.100.1":          "CN",
		"203.0.113.5":           "RU",
		"203.0.113.200":         "", // ZZ
		"0.0.0.1":               "", // ZZ
		"8.8.8.8":               "", // in no range
		"2001:db8::1":           "DE",
		"2001:db8:1::1":         "HK",
		"2001:db8:2::1":         "", // in no range
		"::ffff:192.0.2.10":     "FR",
		"255.255.255.255":       "",
		"ffff:ffff:ffff:ffff::": "",
	} {
		if got := tb.Country(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Country(%s) = %q, want %q", addr, got, want)
		}
	}
}

func TestABrokenLineRefusesTheWholeTable(t *testing.T) {
	for _, bad := range []string{
		"192.0.2.0,192.0.2.255\n",
		"192.0.2.0,nope,FR\n",
		"192.0.2.255,192.0.2.0,FR\n",
		"192.0.2.0,2001:db8::,FR\n",
		"192.0.2.0,192.0.2.255,FRA\n",
	} {
		if _, err := Read(strings.NewReader(fixture + bad)); err == nil {
			t.Errorf("%q read without an error", bad)
		}
	}
}

func req(method, path, remote string, xff ...string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = remote
	for _, x := range xff {
		r.Header.Add("X-Forwarded-For", x)
	}
	return r
}

func TestTheGate(t *testing.T) {
	g := NewGate(table(t), SessionRoutes, nil)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := g.Wrap(ok)
	cases := []struct {
		name string
		r    *http.Request
		want int
	}{
		{"supported, through Caddy", req("POST", "/v1/auth/device/start", "127.0.0.1:5000", "192.0.2.10"), 204},
		{"unsupported, through Caddy", req("POST", "/v1/auth/device/start", "127.0.0.1:5000", "198.51.100.1"), 403},
		{"unsupported IPv6", req("GET", "/oauth/authorize", "[::1]:5000", "2001:db8:1::1"), 403},
		{"supported IPv6", req("POST", "/v1/auth/app/token", "[::1]:5000", "2001:db8::1"), 204},
		{"unknown country fails closed", req("POST", "/v1/pair/claim", "127.0.0.1:5000", "8.8.8.8"), 403},
		{"ZZ fails closed", req("POST", "/v1/pair/claim", "127.0.0.1:5000", "203.0.113.200"), 403},
		{"a client's own X-Forwarded-For is never the address: Caddy's last entry is",
			req("POST", "/oauth/device_authorization", "127.0.0.1:5000", "192.0.2.10, 198.51.100.1"), 403},
		{"the last header line counts", req("POST", "/v1/auth/device/start", "127.0.0.1:5000", "192.0.2.10", "198.51.100.1"), 403},
		{"a direct connection is taken by its own address, whatever it sends",
			req("POST", "/v1/auth/device/start", "198.51.100.1:5000", "192.0.2.10"), 403},
		{"a direct connection from a supported country", req("POST", "/v1/auth/device/start", "192.0.2.10:5000"), 204},
		{"a call from the box itself", req("POST", "/v1/auth/device/start", "127.0.0.1:5000", "127.0.0.1"), 204},
		{"a loopback call with no proxy header", req("POST", "/v1/auth/device/start", "127.0.0.1:5000"), 204},
		{"an unreadable last hop fails closed", req("POST", "/v1/auth/device/start", "127.0.0.1:5000", "not-an-ip"), 403},
		{"another route isn't checked", req("POST", "/v1/auth/device/token", "127.0.0.1:5000", "198.51.100.1"), 204},
		{"another method isn't checked", req("GET", "/v1/auth/app/token", "127.0.0.1:5000", "198.51.100.1"), 204},
		{"an already signed-in call isn't checked", req("GET", "/v1/me", "127.0.0.1:5000", "198.51.100.1"), 204},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, c.r)
		if w.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, w.Code, c.want)
		}
		if w.Code == 403 && !strings.Contains(w.Body.String(), `"error":"region_unsupported"`) {
			t.Errorf("%s: body %s", c.name, w.Body.String())
		}
	}
}

// Every country a store offers tafs in is on the pinned list.
func TestEveryStoreCountryIsSupported(t *testing.T) {
	if len(StoreCountries["google-play"]) != 33 {
		t.Errorf("Google Play lists %d countries, want the 33 in its console", len(StoreCountries["google-play"]))
	}
	for store, codes := range StoreCountries {
		seen := map[string]bool{}
		for _, c := range codes {
			if seen[c] {
				t.Errorf("%s lists %s twice", store, c)
			}
			seen[c] = true
			if !IsSupported(c) {
				t.Errorf("%s offers tafs in %s, which isn't on the list effective %s (%s)", store, c, ListEffective, ListSource)
			}
		}
	}
}

func TestTheRefusalSpeaksTheRequestsLanguage(t *testing.T) {
	if len(Here) != 8 {
		t.Fatalf("%d languages, want 8", len(Here))
	}
	for lang, line := range Here {
		if strings.TrimSpace(line) == "" || !strings.Contains(line, "KIFF") {
			t.Errorf("%s: %q", lang, line)
		}
	}
	h := NewGate(table(t), SessionRoutes, nil).Wrap(http.NotFoundHandler())
	for accept, want := range map[string]string{
		"":                        "en",
		"de-DE,de;q=0.9,en;q=0.8": "de",
		"ja,pt-BR;q=0.5":          "pt",
		"ko":                      "en",
		"fr;q=0,it;q=0.2":         "it",
		"nl;q=nope,pl;q=0.1":      "pl",
	} {
		r := req("POST", "/v1/auth/device/start", "127.0.0.1:5000", "198.51.100.1")
		r.Header.Set("Accept-Language", accept)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Header().Get("Content-Language") != want || body["message"] != Here[want] || body["code"] != Code || body["error"] != Code || body["error_description"] != Here[want] {
			t.Errorf("Accept-Language %q: %s %v", accept, w.Header().Get("Content-Language"), body)
		}
	}
}
