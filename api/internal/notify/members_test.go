package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/words"
)

// An account's name is its admins' choice, so an invitation quotes it as
// theirs, after the inviter's own address: a name like "Apple Support"
// never reads as the sender.
func TestInviteQuotesTheAccountNameAsTheAdmins(t *testing.T) {
	subject, text, body := renderInvite(MemberInvite{Account: "Apple Support", InvitedBy: "mallory@example.com",
		Role: "viewer", AcceptURL: "https://app.kiff.dev/invite/t"})
	if subject != "You're invited to “Apple Support” on KIFF" {
		t.Errorf("subject %q", subject)
	}
	want := "mallory@example.com invited you to the KIFF account its admins named “Apple Support”, as viewer"
	if !strings.HasPrefix(text, want) || !strings.Contains(body, "the KIFF account its admins named “Apple Support”") {
		t.Errorf("text %q\nbody %q", text, body)
	}
	if subject, text, _ := renderInvite(MemberInvite{Role: "viewer"}); subject != "You're invited to an account on KIFF" ||
		!strings.HasPrefix(text, "An admin invited you to a KIFF account, as viewer") {
		t.Errorf("unnamed: %q %q", subject, text)
	}
}

// An invitation and a notice come in the language they're given: the
// invitation's every sentence and its button, and the notice's link.
func TestMembershipEmailsInALanguage(t *testing.T) {
	subject, text, body := renderInvite(MemberInvite{Account: "Acme", InvitedBy: "Ein Admin", Role: "approver",
		AcceptURL: "https://app.kiff.dev/invite/t", ExpiresAt: time.Date(2027, 3, 2, 15, 4, 0, 0, time.UTC), Lang: words.DE})
	if subject != "Du bist zu „Acme“ auf KIFF eingeladen" ||
		!strings.HasPrefix(text, "Ein Admin hat dich zu dem KIFF-Konto, das seine Admins „Acme“ genannt haben, eingeladen, als Genehmiger") ||
		!strings.Contains(text, "die zurückgehaltenen Aktionen des Kontos beantworten kannst") ||
		!strings.Contains(text, "Die Einladung läuft am 2. März 2027, 15:04 (UTC) ab.") ||
		!strings.Contains(body, ">Einladung ansehen und annehmen</a>") {
		t.Fatalf("de invite:\n%s\n%s\n%s", subject, text, body)
	}

	var sent []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer server.Close()
	n, err := NewResendNotifier(ResendConfig{APIKey: "synthetic", From: "KIFF <test@example.invalid>", Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.AccountNotice(context.Background(), "p@example.invalid", AccountNotice{Subject: "s", Body: "b",
		URL: "https://app.kiff.dev/dashboard/people", Lang: words.NL}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sent), "Bekijk de mensen in het account") {
		t.Fatalf("nl notice: %s", sent)
	}
}
