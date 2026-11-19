package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/words"
)

// The sign-in callback speaks the language the page it returns to asked
// for (a tafs app's device sign-in passes lang along), else the browser's,
// else English, which reads word for word as before.
func TestSignInCallbackSpeaksTheAppsLanguage(t *testing.T) {
	cases := []struct {
		target, accept, want string
	}{
		{"/sign-in/callback?next=" + "%2Fauth%2Fdevice%3Fcode%3DABCD-EFGH%26lang%3Dfr", "de-DE,de;q=0.9", "fr"},
		{"/sign-in/callback?next=%2Fdashboard", "pl-PL,pl;q=0.9,en;q=0.5", "pl"},
		{"/sign-in/callback", "", "en"},
		{"/sign-in/callback?next=%2Fauth%2Fdevice%3Flang%3Dxx", "", "en"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, c.target, nil)
		if c.accept != "" {
			r.Header.Set("Accept-Language", c.accept)
		}
		if got := callbackLanguage(r); got != c.want {
			t.Errorf("%s (%s): %q, want %q", c.target, c.accept, got, c.want)
			continue
		}
		w := words.SignInIn(c.want)

		rec := httptest.NewRecorder()
		if err := renderCallbackPage(rec, callbackPageData{FrontendAPI: "clerk.example", PublishableKey: "pk_test", Next: "/dashboard", Lang: c.want}); err != nil {
			t.Fatal(err)
		}
		body := rec.Body.String()
		for _, s := range []string{`<html lang="` + c.want + `">`, "<title>" + w.Title + "</title>", w.Signing, `href="/sign-in">` + w.TryAgain + "<"} {
			if !strings.Contains(body, s) {
				t.Errorf("%s: the hand-off page is missing %q", c.want, s)
			}
		}

		rec = httptest.NewRecorder()
		if err := renderLoopBreakPage(rec, c.want); err != nil {
			t.Fatal(err)
		}
		body = strings.ReplaceAll(rec.Body.String(), "&rsquo;", "’")
		for _, s := range []string{`<html lang="` + c.want + `">`, w.LoopHead, w.LoopButton, "<code>clerk.kiff.dev</code>"} {
			if !strings.Contains(body, s) {
				t.Errorf("%s: the loop-breaker page is missing %q", c.want, s)
			}
		}
	}
	// English is what the pages said before they were translated, and the
	// script's errors name no vendor, in any language.
	en := words.SignInIn("en")
	if en.Signing != "Signing you in…" || en.LoopHead != "We couldn’t complete sign-in" || en.NoSDK != "The sign-in service didn’t load. Ad blocker?" {
		t.Errorf("the English lines changed: %+v", en)
	}
	for _, lang := range []string{"en", "de", "fr", "es", "it", "pt", "nl", "pl"} {
		w := words.SignInIn(lang)
		for name, line := range map[string]string{"NoSDK": w.NoSDK, "NoSession": w.NoSession, "NoToken": w.NoToken} {
			if strings.Contains(line, "Clerk") {
				t.Errorf("%s: %s names the vendor: %q", lang, name, line)
			}
		}
	}
}

// Every language has every line.
func TestSignInWordsAreComplete(t *testing.T) {
	for _, lang := range []string{"en", "de", "fr", "es", "it", "pt", "nl", "pl"} {
		w := words.SignInIn(lang)
		for name, v := range map[string]string{
			"Title": w.Title, "Signing": w.Signing, "Failed": w.Failed, "TryAgain": w.TryAgain,
			"NoSDK": w.NoSDK, "NoSession": w.NoSession, "NoToken": w.NoToken, "LoopTitle": w.LoopTitle,
			"LoopHead": w.LoopHead, "LoopBody": w.LoopBody, "LoopButton": w.LoopButton,
			"LoopHintBefore": w.LoopHintBefore, "LoopHintAfter": w.LoopHintAfter,
		} {
			if v == "" {
				t.Errorf("%s has no %s", lang, name)
			}
		}
		if lang != "en" && w.Signing == words.SignInIn("en").Signing {
			t.Errorf("%s isn't translated", lang)
		}
	}
}
