package notify

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/words"
)

// TestWriteEmailSamples writes one HTML file per email and language into the
// directory named by KIFF_EMAIL_SAMPLES, to look at them in a browser or
// screenshot them for docs/email/. It does nothing when the variable is unset.
func TestWriteEmailSamples(t *testing.T) {
	dir := os.Getenv("KIFF_EMAIL_SAMPLES")
	if dir == "" {
		t.Skip("KIFF_EMAIL_SAMPLES is not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, html string, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".html"), []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 11, 10, 20, 34, 0, 0, time.UTC)
	asked := CodeAsked{At: at, Browser: "Chrome", Country: "FR"}
	for _, lang := range []words.Lang{words.EN, words.FR, "ar"} {
		l := string(lang)
		_, _, h, err := renderSignInCode("483 920", lang, asked)
		write(l+"-code", h, err)
		_, _, h, err = renderSignInCodeOff(lang, asked)
		write(l+"-code-off", h, err)
		_, _, h, err = renderSignInCodeChanged(false, lang, at)
		write(l+"-code-changed", h, err)
	}
	for _, lang := range []words.Lang{words.EN, words.FR} {
		l := string(lang)
		nd := sampleNewDevice(t)
		nd.Lang = lang
		_, _, h, err := renderNewDevice(nd)
		write(l+"-new-device", h, err)
		held := heldForTest()
		held.Lang = lang
		_, _, h, err = renderHeld(held)
		write(l+"-held", h, err)
		_, _, h2 := renderInvite(MemberInvite{Account: "Acme", Role: "admin", InvitedBy: "", AcceptURL: "https://app.kiff.dev/invite/abc", ExpiresAt: at.Add(72 * time.Hour), Lang: lang})
		write(l+"-invite", h2, nil)
	}
	e := words.AccountEmailFor(words.EN)
	for name, m := range map[string]mail{
		"en-suspended":   {Lang: words.EN, Title: e.SuspendedSubject, Lead: e.SuspendedBody},
		"en-unsuspended": {Lang: words.EN, Title: e.UnsuspendedSubject, Lead: e.UnsuspendedBody},
		"en-support": {Lang: words.EN, Title: e.SupportSubject, Lead: e.SupportBody,
			Buttons: []mailButton{{Label: e.SupportView, URL: "https://app.kiff.dev/dashboard/support/tkt_1"}}},
	} {
		h, err := renderLayout(m)
		write(name, h, err)
	}
}
