package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/words"
)

// Every part of a mail is plain text and is escaped; only LeadHTML is markup,
// and the caller escapes its parts.
func TestTheLayoutEscapesEveryPart(t *testing.T) {
	const evil = `<script>alert(1)</script>"><img src=x>`
	page, err := renderLayout(mail{Lang: words.EN, Title: evil, Brand: evil, Lead: evil, Code: evil, Paras: []string{evil}, Warn: evil,
		List: []string{evil}, Rows: []newDeviceRow{{Label: evil, Value: evil}}, Note: evil,
		Buttons: []mailButton{{Label: evil, URL: `https://kiff.dev/x?a="><script>`}}, Small: []string{evil}, Logos: []string{`https://kiff.dev/"><i>`}})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"<script>", "<img src=x>", `"><i>`} {
		if strings.Contains(page, bad) {
			t.Errorf("the layout let %q through: %s", bad, page)
		}
	}
}

// The page's language and direction are the person's: right to left for the
// languages that read that way, with the code still left to right.
func TestTheLayoutFollowsTheLanguageAndItsDirection(t *testing.T) {
	for _, c := range []struct {
		lang words.Lang
		dir  string
	}{{words.EN, "ltr"}, {words.FR, "ltr"}, {"ar", "rtl"}, {"he", "rtl"}, {"dv", "rtl"}, {"", "ltr"}} {
		page, err := renderLayout(mail{Lang: c.lang, Lead: "x", Code: "123456"})
		if err != nil {
			t.Fatal(err)
		}
		lang := string(c.lang)
		if lang == "" {
			lang = "en"
		}
		if want := `<html lang="` + lang + `" dir="` + c.dir + `">`; !strings.Contains(page, want) {
			t.Errorf("%q: want %s in the page", c.lang, want)
		}
		if !strings.Contains(page, `<td dir="ltr"`) {
			t.Errorf("%q: the code must stay left to right", c.lang)
		}
		if c.dir == "rtl" && !strings.Contains(page, "text-align:right") {
			t.Errorf("%q: right to left text aligns right", c.lang)
		}
	}
}

// The first button is the one to press; the rest are outlines. The brand and
// the footer are KIFF's unless the mail names another (tafs by KIFF).
func TestTheLayoutHasOnePrimaryButtonAndOneFooter(t *testing.T) {
	page, err := renderLayout(mail{Lang: words.EN, Lead: "x", Buttons: []mailButton{{"One", "https://kiff.dev/1"}, {"Two", "https://kiff.dev/2"}}})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(page, "background:#1a1a1a;color:#ffffff"); n != 1 {
		t.Errorf("%d primary buttons, want 1", n)
	}
	if !strings.Contains(page, "KIFF · kiff.dev") || strings.Contains(page, "KIFF Operations") {
		t.Errorf("the footer is KIFF · kiff.dev: %s", page)
	}
	tafs, _ := renderLayout(mail{Lang: words.EN, Brand: TafsByKIFF, Lead: "x"})
	if !strings.Contains(tafs, TafsByKIFF+" · kiff.dev") {
		t.Errorf("the tafs brand is the footer's too")
	}
}

// Every email KIFF sends is drawn by the layout (the same footer, the same
// language and direction) and none says "workspace" or "KIFF Cloud".
func TestEveryEmailIsOnTheLayoutAndSaysAccount(t *testing.T) {
	var sent []resendRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got resendRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		sent = append(sent, got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n, err := NewResendNotifier(ResendConfig{APIKey: testKey, From: "KIFF <hello@kiff.dev>", ReplyTo: "support@kiff.dev", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, send := range map[string]func() error{
		"suspended":   func() error { return n.TenantSuspended(ctx, "a@example.com", words.EN) },
		"unsuspended": func() error { return n.TenantUnsuspended(ctx, "a@example.com", words.EN) },
		"support": func() error {
			return n.SupportTicketUpdated(ctx, "a@example.com", "https://app.kiff.dev/t/1", words.EN)
		},
		"held": func() error { return n.ActionHeld(ctx, "a@example.com", heldForTest()) },
		"invite": func() error {
			return n.MemberInvited(ctx, "a@example.com", MemberInvite{Account: "Acme", Role: "admin", AcceptURL: "https://app.kiff.dev/i/1", ExpiresAt: time.Now().Add(time.Hour)})
		},
		"notice": func() error {
			return n.AccountNotice(ctx, "a@example.com", AccountNotice{Subject: "s", Body: "b", URL: "https://app.kiff.dev/people"})
		},
		"card warning": func() error {
			return n.CardBudgetWarning(ctx, "a@example.com", "cw_1", "Warning.", "https://app.kiff.dev/cards/1", words.EN)
		},
		"code":         func() error { return n.SignInCode(ctx, "a@example.com", "123456", words.EN, CodeAsked{At: time.Now()}) },
		"code off":     func() error { return n.SignInCodeOff(ctx, "a@example.com", words.EN, CodeAsked{At: time.Now()}) },
		"code changed": func() error { return n.SignInCodeChanged(ctx, "a@example.com", true, words.EN, time.Now()) },
		"new device":   func() error { return n.NewDeviceSignedIn(ctx, "a@example.com", sampleNewDevice(t)) },
	} {
		sent = nil
		if err := send(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(sent) != 1 {
			t.Fatalf("%s: %d emails sent", name, len(sent))
		}
		e := sent[0]
		if e.From != "KIFF <hello@kiff.dev>" || e.ReplyTo != "support@kiff.dev" {
			t.Errorf("%s: from %q reply-to %q", name, e.From, e.ReplyTo)
		}
		if !strings.Contains(e.HTML, ` · kiff.dev</td>`) || !strings.Contains(e.HTML, `<html lang="en" dir="ltr">`) {
			t.Errorf("%s: not drawn by the layout: %s", name, e.HTML)
		}
		if !strings.Contains(e.HTML, "<title>"+htmlEscape(e.Subject)+"</title>") {
			t.Errorf("%s: the page's title is not the subject %q", name, e.Subject)
		}
		for _, old := range []string{"workspace", "Workspace", "KIFF Cloud", "KIFF Operations"} {
			if strings.Contains(e.Subject+e.Text+e.HTML, old) {
				t.Errorf("%s says %q", name, old)
			}
		}
	}
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;").Replace(s)
}

// The allowance and usage-budget emails are gone: nothing sent them.
func TestTheAllowanceAndBudgetEmailsAreGone(t *testing.T) {
	var _ Notifier = NoopNotifier{}
	if _, ok := any(NoopNotifier{}).(interface {
		AllowanceThresholdReached(context.Context, string, int, int, int) error
	}); ok {
		t.Error("the allowance email is back")
	}
	if _, ok := any(NoopNotifier{}).(interface {
		BudgetReached(context.Context, string, int, int) error
	}); ok {
		t.Error("the usage-budget email is back")
	}
}

// A language with no account lines yet reads English; one with lines reads
// them. A language is never half written.
func TestTheAccountEmailsReadEnglishUntilALanguageIsWritten(t *testing.T) {
	en := words.AccountEmailFor(words.EN)
	if got := words.AccountEmailFor(words.FR); got != en {
		t.Errorf("French has no lines yet and reads English: %+v", got)
	}
	words.AccountEmails["xx"] = words.AccountEmail{SuspendedSubject: "s"}
	defer delete(words.AccountEmails, "xx")
	if got := words.AccountEmailFor("xx"); got.SuspendedSubject != "s" {
		t.Errorf("a written language reads its own lines: %+v", got)
	}
	for _, line := range []string{en.SuspendedSubject, en.SuspendedBody, en.UnsuspendedSubject, en.UnsuspendedBody, en.SupportSubject, en.SupportBody, en.SupportView} {
		if strings.TrimSpace(line) == "" || strings.Contains(line, "orkspace") || strings.Contains(line, "KIFF Cloud") {
			t.Errorf("an English account line is empty or says workspace: %q", line)
		}
	}
}

// A row's value is its own direction, so an English date or a Latin browser name inside
// an Arabic email isn't scrambled by the right-to-left line around it.
func TestTheLayoutIsolatesEachRowsValue(t *testing.T) {
	page, err := renderLayout(mail{Lang: "ar", Lead: "x", Rows: []newDeviceRow{{Label: "متى", Value: "10 Nov 2026, 20:34 (UTC)"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page, "<bdi>10 Nov 2026, 20:34 (UTC)</bdi>") {
		t.Errorf("the value is not isolated: %s", page)
	}
}
