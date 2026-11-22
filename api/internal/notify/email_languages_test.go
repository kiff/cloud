package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/words"
)

// A language beyond the eight that has the emails' lines reads them in the emails whose words are all in the table:
// new device, held action, invitation. None says an English line of Words (the sentences below are English's) when
// the language has written its own. Account notices and Card warnings are worded by their callers from other
// Words fields, so they stay in the eight or English (TestAnEmailIsLaidOutInTheLanguageItsWordsAreIn).
func TestEveryEmailReadsALanguageBeyondTheEight(t *testing.T) {
	const tag = "qq"
	lines := words.EmailLines{}
	for k, v := range words.EmailLinesEnglish() {
		// The language's own lines: marked, and nothing English left in them.
		lines[k] = shout(tag, k, v)
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

}

// An email reads a language only as far as its words are written: the page's language, its direction and its
// footer follow the words, so English is never laid out right to left. A person whose language is Arabic gets
// every email in English, left to right, until that email's words are written.
func TestAnEmailIsLaidOutInTheLanguageItsWordsAreIn(t *testing.T) {
	// Arabic has its real lines now; the test needs it as a language with none yet.
	realMore, realAccount, realFooter := words.EmailMore["ar"], words.AccountEmails["ar"], words.EmailFooters["ar"]
	delete(words.EmailMore, "ar")
	delete(words.AccountEmails, "ar")
	delete(words.EmailFooters, "ar")
	defer func() {
		words.EmailMore["ar"], words.AccountEmails["ar"], words.EmailFooters["ar"] = realMore, realAccount, realFooter
	}()
	var sent []resendRequest
	n, srv := recordingResend(t, &sent)
	defer srv.Close()
	ctx := context.Background()
	const ltr = `<html lang="en" dir="ltr">`
	const rtlAr = `<html lang="ar" dir="rtl">`
	last := func() string { return sent[len(sent)-1].HTML }

	// Notices and warnings are worded by their callers from the eight: English for Arabic.
	if err := n.AccountNotice(ctx, "a@example.com", AccountNotice{Subject: "s", Body: "An English notice.", URL: "https://app.kiff.dev/p", Lang: "ar"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(last(), ltr) {
		t.Errorf("an account notice in English for an Arabic reader is left to right: %s", last())
	}
	if err := n.CardBudgetWarning(ctx, "a@example.com", "cw_1", "An English warning.", "https://app.kiff.dev/c", "ar"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(last(), ltr) {
		t.Errorf("a Card warning in English for an Arabic reader is left to right")
	}
	// Suspended, reactivated and support read English until their lines are written, then Arabic.
	for name, send := range map[string]func() error{
		"suspended":   func() error { return n.TenantSuspended(ctx, "a@example.com", "ar") },
		"unsuspended": func() error { return n.TenantUnsuspended(ctx, "a@example.com", "ar") },
		"support":     func() error { return n.SupportTicketUpdated(ctx, "a@example.com", "https://app.kiff.dev/t", "ar") },
	} {
		if err := send(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(last(), ltr) {
			t.Errorf("%s in English for an Arabic reader is left to right", name)
		}
	}
	words.AccountEmails["ar"] = words.AccountEmail{SuspendedSubject: "s", SuspendedBody: "b", UnsuspendedSubject: "u", UnsuspendedBody: "ub", SupportSubject: "ss", SupportBody: "sb", SupportView: "v"}
	defer delete(words.AccountEmails, "ar")
	if err := n.TenantSuspended(ctx, "a@example.com", "ar"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(last(), rtlAr) {
		t.Errorf("with its lines written it is Arabic, right to left: %s", last())
	}

	// New device, held and invitation: English until the language has every line; one line short is none.
	nd := sampleNewDevice(t)
	nd.Lang = "ar"
	_, _, h, _ := renderNewDevice(nd)
	if !strings.Contains(h, ltr) {
		t.Errorf("a new-device email in English for an Arabic reader is left to right")
	}
	held := heldForTest()
	held.Lang = "ar"
	_, _, h, _ = renderHeld(held)
	if !strings.Contains(h, ltr) {
		t.Errorf("a held email in English for an Arabic reader is left to right")
	}
	_, _, h = renderInvite(MemberInvite{Account: "Acme", Role: "admin", AcceptURL: "https://app.kiff.dev/i", Lang: "ar"})
	if !strings.Contains(h, ltr) {
		t.Errorf("an invitation in English for an Arabic reader is left to right")
	}
	half := words.EmailLines{"HeldReject": "رفض"} // one line, not all of them
	words.EmailMore["ar"] = half
	defer delete(words.EmailMore, "ar")
	_, _, h, _ = renderHeld(held)
	if !strings.Contains(h, ltr) || strings.Contains(h, "رفض") {
		t.Errorf("an entry with a line missing is read as none: %s", h)
	}
	full := words.EmailLines{}
	for k, v := range words.EmailLinesEnglish() {
		full[k] = shout("ar", k, v)
	}
	words.EmailMore["ar"] = full
	_, _, h, _ = renderHeld(held)
	if !strings.Contains(h, rtlAr) {
		t.Errorf("with every line it is Arabic, right to left: %s", h)
	}
}

// A translation's own words are text: a "<" in a held-action sentence is escaped, while the agent and the action are
// still bold.
func TestATranslatedHeldSentenceIsEscapedAndItsPartsAreBold(t *testing.T) {
	const tag = "qh"
	lines := words.EmailLines{}
	for k, v := range words.EmailLinesEnglish() {
		lines[k] = v
	}
	lines["HeldWants"] = "<b>{0}</b> <script>x</script> wants"
	lines["HeldIntro"] = "{0} <i>{1}</i> {2}"
	words.EmailMore[tag] = lines
	defer delete(words.EmailMore, tag)
	h := heldForTest()
	h.Lang, h.Agent, h.Action = tag, "Bot & co", "pay <it>"
	for name, withWords := range map[string]bool{"people's words": true, "identifiers": false} {
		h.ActionWords = ""
		if withWords {
			h.ActionWords = "Pay"
		}
		_, _, html, err := renderHeld(h)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"<script>x", "<b>", "<i>", "<it>"} {
			if strings.Contains(html, bad) {
				t.Errorf("%s: %q is markup in the HTML", name, bad)
			}
		}
		if !strings.Contains(html, "<strong>Bot &amp; co</strong>") {
			t.Errorf("%s: the agent is escaped and bold: %s", name, html)
		}
	}
}

// An account notice is laid out in a language only when the language has both groups of lines, the emails' and the
// notices' sentences that handlers word it from: with the first only, an Arabic reader's notice is English left to
// right; with both it is Arabic right to left, and its button is the language's.
func TestAnAccountNoticeFollowsTheNoticesGroup(t *testing.T) {
	const tag = "fa" // a right-to-left language the emails have no lines for
	var sent []resendRequest
	n, srv := recordingResend(t, &sent)
	defer srv.Close()
	ctx := context.Background()
	notice := AccountNotice{Subject: "s", Body: "b", URL: "https://app.kiff.dev/people", Lang: tag}
	all := words.EmailLinesEnglish()
	core, notices := words.EmailLines{}, words.EmailLines{}
	for k, v := range all {
		if words.EmailGroupName(k) == "notices" {
			notices[k] = shout(tag, k, v)
		} else {
			core[k] = shout(tag, k, v)
		}
	}
	defer delete(words.EmailMore, tag)

	words.EmailMore[tag] = core
	words.ForgetEmailWords(tag)
	if err := n.AccountNotice(ctx, "a@example.com", notice); err != nil {
		t.Fatal(err)
	}
	if h := sent[len(sent)-1].HTML; !strings.Contains(h, `<html lang="en" dir="ltr">`) || strings.Contains(h, "[fa]") {
		t.Errorf("with the emails' lines only the notice is English, left to right: %s", h)
	}
	full := words.EmailLines{}
	for k, v := range core {
		full[k] = v
	}
	for k, v := range notices {
		full[k] = v
	}
	words.EmailMore[tag] = full
	words.ForgetEmailWords(tag)
	if err := n.AccountNotice(ctx, "a@example.com", notice); err != nil {
		t.Fatal(err)
	}
	if h := sent[len(sent)-1].HTML; !strings.Contains(h, `<html lang="fa" dir="rtl">`) || !strings.Contains(h, "[fa]") {
		t.Errorf("with both groups the notice is the language's, right to left: %s", h)
	}
}

// shout is a stand-in translation of one line: its words in capitals behind the language's tag, keeping the lines that
// are not words (true or false, a money separator's name) and the plural lines' structure ("one=…|other=…").
func shout(tag, k, v string) string {
	if v == "true" || v == "false" || k == "Money.Thousands" || k == "Money.Gap" {
		return v
	}
	if k == "CallCount" || k == "Ordinal" {
		parts := strings.Split(v, "|")
		for i, p := range parts {
			cat, text, _ := strings.Cut(p, "=")
			parts[i] = cat + "=[" + tag + "] " + strings.ToUpper(text)
		}
		return strings.Join(parts, "|")
	}
	return "[" + tag + "] " + strings.ToUpper(v)
}

// A Card warning is laid out in a language only when its sentence is in it, which is the cards lines' group: with the
// emails' lines only, the whole email is English, left to right; with the cards lines too it is the language's, right to
// left, its subject and button too.
func TestACardWarningFollowsTheCardsGroup(t *testing.T) {
	const tag = "fa"
	var sent []resendRequest
	n, srv := recordingResend(t, &sent)
	defer srv.Close()
	ctx := context.Background()
	core, full := words.EmailLines{}, words.EmailLines{}
	for k, v := range words.EmailLinesEnglish() {
		full[k] = shout(tag, k, v)
		if words.EmailGroupName(k) == "core" {
			core[k] = full[k]
		}
	}
	defer delete(words.EmailMore, tag)

	words.EmailMore[tag] = core
	words.ForgetEmailWords(tag)
	if err := n.CardBudgetWarning(ctx, "a@example.com", "cw_1", "An English warning.", "https://app.kiff.dev/c", tag); err != nil {
		t.Fatal(err)
	}
	if h := sent[len(sent)-1].HTML; !strings.Contains(h, `<html lang="en" dir="ltr">`) || strings.Contains(h, "[FA]") || strings.Contains(h, "[fa]") {
		t.Errorf("with the emails' lines only the warning is English, left to right: %s", h)
	}
	words.EmailMore[tag] = full
	words.ForgetEmailWords(tag)
	if err := n.CardBudgetWarning(ctx, "a@example.com", "cw_2", "[fa] A WARNING.", "https://app.kiff.dev/c", tag); err != nil {
		t.Fatal(err)
	}
	last := sent[len(sent)-1]
	if !strings.Contains(last.HTML, `<html lang="fa" dir="rtl">`) || !strings.Contains(last.Subject, "[fa]") {
		t.Errorf("with the cards lines the warning is the language's, right to left: %s %s", last.Subject, last.HTML)
	}
}
