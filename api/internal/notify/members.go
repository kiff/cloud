package notify

import (
	"context"
	"html"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/words"
)

// MembershipNotifier sends the membership emails: the invitation
// itself, and the notices that keep the people on an account aware of
// who can approve (a new approver or admin, separation of duties turned
// off). Kept apart from Notifier so existing fakes stay valid; the
// Resend and no-op notifiers implement both.
type MembershipNotifier interface {
	MemberInvited(ctx context.Context, to string, inv MemberInvite) error
	AccountNotice(ctx context.Context, to string, n AccountNotice) error
}

// MemberInvite is what an invitation email says.
type MemberInvite struct {
	Account   string // the account's display name
	Role      string
	InvitedBy string // the inviter as the person invited sees them ("An admin"), never their email
	AcceptURL string // contains the single-use token
	ExpiresAt time.Time
	// Lang is the email's language (internal/words); English when empty.
	Lang words.Lang
}

// AccountNotice is a short factual notice about a change to who can do
// what on an account, with a link to the people page.
type AccountNotice struct {
	Subject string
	Body    string
	URL     string
	// Lang is the language Subject and Body are in, for the link's words;
	// English when empty.
	Lang words.Lang
}

func renderInvite(inv MemberInvite) (subject, text, htmlBody string) {
	w := words.Of(inv.Lang)
	// The account's name is whatever its admins chose, so the email quotes
	// it as theirs and never lets it read as the sender: an account named
	// "Apple Support" is still an account on KIFF. The inviter isn't named
	// by their email: the person invited sees the account, not its people.
	name := strings.TrimSpace(inv.Account)
	account := w.AnAccount
	if name != "" {
		account = w.AccountNamed(name)
	}
	by := strings.TrimSpace(inv.InvitedBy)
	if by == "" {
		by = w.AnAdmin
	}
	what := w.InviteRoleWhat[inv.Role]
	if what == "" {
		what = w.InviteRoleOther
	}
	subject = w.InviteSubject(name)
	intro := w.InviteIntro(by, account, w.Role(inv.Role), what)
	expiry := ""
	if !inv.ExpiresAt.IsZero() {
		expiry = " " + w.InviteExpires(w.DateTime(inv.ExpiresAt))
	}
	text = intro + " " + w.InviteAccept(inv.AcceptURL) + expiry + " " + w.InviteIgnore
	htmlBody = `<!doctype html><html><body style="font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;font-size:15px;line-height:1.5;color:#1a1a1a">` +
		`<p>` + html.EscapeString(intro) + `</p>` +
		`<p><a href="` + html.EscapeString(inv.AcceptURL) + `">` + html.EscapeString(w.InviteButton) + `</a></p>` +
		`<p style="color:#666;font-size:13px">` + html.EscapeString(strings.TrimSpace(expiry+" "+w.InviteIgnore)) + `</p>` +
		`</body></html>`
	return subject, text, htmlBody
}

// MemberInvited implements MembershipNotifier.
func (r *ResendNotifier) MemberInvited(ctx context.Context, to string, inv MemberInvite) error {
	subject, text, body := renderInvite(inv)
	return r.sendHTML(ctx, to, subject, text, body)
}

// AccountNotice implements MembershipNotifier.
func (r *ResendNotifier) AccountNotice(ctx context.Context, to string, n AccountNotice) error {
	text := n.Body
	if n.URL != "" {
		text += " " + n.URL
		return r.sendHTML(ctx, to, n.Subject, text, htmlBodyLink(html.EscapeString(n.Body), n.URL, html.EscapeString(words.Of(n.Lang).PeopleLink)))
	}
	return r.sendHTML(ctx, to, n.Subject, text, htmlBody(html.EscapeString(text)))
}

// MemberInvited implements MembershipNotifier (no-op).
func (NoopNotifier) MemberInvited(context.Context, string, MemberInvite) error { return nil }

// AccountNotice implements MembershipNotifier (no-op).
func (NoopNotifier) AccountNotice(context.Context, string, AccountNotice) error { return nil }

var (
	_ MembershipNotifier = NoopNotifier{}
	_ MembershipNotifier = (*ResendNotifier)(nil)
)
