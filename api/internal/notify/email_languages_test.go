package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/words"
)

// A language beyond the eight that has the emails' lines reads them in every email that takes the person's
// language: new device, held action, invitation, account notice, Card warning. None says an English
// line of Words (the sentences below are English's) when the language has written its own.
func TestEveryEmailReadsALanguageBeyondTheEight(t *testing.T) {
	const tag = "qq"
	lines := words.EmailLines{}
	for k, v := range words.EmailLinesEnglish() {
		// The language's own lines: marked, and nothing English left in them.
		lines[k] = "[" + tag + "] " + strings.ToUpper(v)
	}
	words.EmailMore[tag] = lines
	defer delete(words.EmailMore, tag)
	lang := words.Lang(tag)
	english := words.Of(words.EN)

	nd := sampleNewDevice(t)
	nd.Lang = lang
	subject, text, html, err := renderNewDevice(nd)
	if err != nil {
		t.Fatal(err)
	}
	check := func(name, got string, englishLines ...string) {
		t.Helper()
		if !strings.Contains(got, "["+tag+"]") {
			t.Errorf("%s has none of the language's lines:\n%s", name, got)
		}
		for _, e := range englishLines {
			if e != "" && strings.Contains(got, e) {
				t.Errorf("%s still says English %q:\n%s", name, e, got)
			}
		}
	}
	check("new device subject", subject, english.NewDeviceSubject)
	check("new device text", text, english.NewDeviceIntro, english.NewDeviceWasYou, english.NewDeviceWhy)
	check("new device html", html, english.NewDeviceSessions)

	held := heldForTest()
	held.Lang, held.ActionWords, held.AmountText, held.Reason = lang, "Pay", "5", "why"
	subject, text, html, err = renderHeld(held)
	if err != nil {
		t.Fatal(err)
	}
	check("held subject", subject, english.HeldSubject)
	check("held text", text, english.HeldNothingDone, english.HeldNoApprove, english.HeldRowAction)
	check("held html", html, english.HeldApproveOnce, english.HeldReject)

	subject, text, html = renderInvite(MemberInvite{Account: "Acme", Role: "admin", AcceptURL: "https://app.kiff.dev/i/1", ExpiresAt: time.Date(2026, 3, 2, 15, 4, 0, 0, time.UTC), Lang: lang})
	check("invite subject", subject)
	check("invite text", text, english.InviteIgnore)
	check("invite html", html, english.InviteButton)

	var sent []resendRequest
	n, srv := recordingResend(t, &sent)
	defer srv.Close()
	if err := n.AccountNotice(context.Background(), "a@example.com", AccountNotice{Subject: "s", Body: "b", URL: "https://app.kiff.dev/people", Lang: lang}); err != nil {
		t.Fatal(err)
	}
	check("notice html", sent[0].HTML, english.PeopleLink)
	if err := n.CardBudgetWarning(context.Background(), "a@example.com", "cw_1", "Warning.", "https://app.kiff.dev/cards/1", lang); err != nil {
		t.Fatal(err)
	}
	check("card warning subject", sent[1].Subject, english.WarnSubject)
	check("card warning html", sent[1].HTML, english.WarnReview)
}
