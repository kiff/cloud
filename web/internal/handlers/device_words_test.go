package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/words"
)

// tafsPhoneAPI is a fake API whose PHON-E123 is the tafs app (it sent a
// signing key), with a recent sign-in when recent, and whose approve
// fails with KIFF's own words.
func tafsPhoneAPI(t *testing.T, recent bool) *apiclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/auth/device/pending" && r.URL.Query().Get("user_code") == "PHON-E123":
			rs := "false"
			if recent {
				rs = "true"
			}
			_, _ = w.Write([]byte(`{"user_code":"PHON-E123","device_name":"Pixel 8","has_key":true,"key_fingerprint":"ab12 cd34","recent_sign_in":` + rs + `}`))
		case r.URL.Path == "/v1/auth/device/approve":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal: the device store is down"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func deviceGet(mux *http.ServeMux, target, accept string, signedIn bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if accept != "" {
		req.Header.Set("Accept-Language", accept)
	}
	if signedIn {
		req = req.WithContext(withSession(req.Context(), deviceTestSession()))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// Device approval speaks the app's language (?lang=, else the browser's),
// wears "tafs by KIFF" for the tafs app and KIFF alone for the CLI, and
// keeps its language through its forms and every sign-in on the way.
func TestDeviceApproveSpeaksTheAppsLanguage(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	NewDeviceApproveHandler(tafsPhoneAPI(t, true), nil, nil, nil).Register(mux)
	de := words.DeviceIn("de")

	rec := deviceGet(mux, "/auth/device?code=PHON-E123&lang=de", "fr-FR,fr;q=0.9", true)
	body := rec.Body.String()
	for _, want := range []string{`<html lang="de"`, "<title>" + de.Title + "</title>", ">" + de.ReviewTitle + "<", ">" + de.ApproveDevice + "<", de.KeyNote,
		words.TafsByKIFF, "tafs-icon-512.png", `action="/auth/device?lang=de"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the tafs phone's page in German has no %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Approve this device") {
		t.Error("English is left on the German page")
	}

	// No ?lang=: the browser's language.
	fr := words.DeviceIn("fr")
	if body := deviceGet(mux, "/auth/device?code=PHON-E123", "fr-FR,fr;q=0.9,en;q=0.5", true).Body.String(); !strings.Contains(body, `<html lang="fr"`) || !strings.Contains(body, fr.ReviewTitle) {
		t.Errorf("the browser's French: %s", body)
	}

	// Approving fails: the page says what to do in German, never KIFF's own words.
	req := httptest.NewRequest(http.MethodPost, "/auth/device?lang=de", strings.NewReader("user_code=PHON-E123&reviewed=1&key_fingerprint=ab12+cd34"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(withSession(req.Context(), deviceTestSession()))
	post := httptest.NewRecorder()
	mux.ServeHTTP(post, req)
	if b := post.Body.String(); !strings.Contains(b, de.ErrApprove) || !strings.Contains(b, de.CouldntApprove) || strings.Contains(b, "device store") {
		t.Errorf("a failed approval in German: %s", b)
	}

	// Signed out: sign-in brings the person back to the page in German.
	if loc := deviceGet(mux, "/auth/device?code=PHON-E123&lang=de", "", false).Header().Get("Location"); !strings.Contains(loc, url.QueryEscape("lang=de")) {
		t.Errorf("sign-in drops the language: %s", loc)
	}
}

// The CLI's device (no key) wears KIFF alone, in English unless asked.
func TestDeviceApproveForTheCLIStaysKIFFs(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	NewDeviceApproveHandler(deviceApproveAPI(t, http.StatusOK, "{}"), nil, nil, nil).Register(mux)
	body := deviceGet(mux, "/auth/device?code=BCDF-GHJK", "", true).Body.String()
	if !strings.Contains(body, `<html lang="en"`) || strings.Contains(body, words.TafsByKIFF) || !strings.Contains(body, "kiff<span>.</span>dev") || !strings.Contains(body, "Approve this device") {
		t.Errorf("the CLI's page: %s", body)
	}
}

// A sign-in that isn't recent enough sends the person to sign in again and
// back to the same code, in the same language.
func TestDeviceReauthKeepsTheLanguage(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	NewDeviceApproveHandler(tafsPhoneAPI(t, false), nil, nil, nil).Register(mux)
	rec := deviceGet(mux, "/auth/device?code=PHON-E123&lang=pl", "", true)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != deviceReauthURL("PHON-E123", "pl") || !strings.Contains(loc, url.QueryEscape("&lang=pl")) {
		t.Errorf("sign in again: %d %s", rec.Code, loc)
	}
}
