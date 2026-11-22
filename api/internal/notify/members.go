package notify

import (
	"context"
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
	htmlBody, _ = renderLayout(mail{Lang: inv.Lang, Title: subject, Lead: intro,
		Buttons: []mailButton{{Label: w.InviteButton, URL: inv.AcceptURL}},
		Small:   []string{strings.TrimSpace(expiry + " " + w.InviteIgnore)}})
	return subject, text, htmlBody
}

// MemberInvited implements MembershipNotifier.
func (r *ResendNotifier) MemberInvited(ctx context.Context, to string, inv MemberInvite) error {
	subject, text, body := renderInvite(inv)
	return r.sendHTML(ctx, to, subject, text, body)
}

// AccountNotice implements MembershipNotifier.
func (r *ResendNotifier) AccountNotice(ctx context.Context, to string, n AccountNotice) error {
	text, m := n.Body, mail{Lang: n.Lang, Lead: n.Body}
	if n.URL != "" {
		text += " " + n.URL
		m.Buttons = []mailButton{{Label: words.Of(n.Lang).PeopleLink, URL: n.URL}}
	}
	return r.sendMail(ctx, to, n.Subject, text, "", m)
}

// MemberInvited implements MembershipNotifier (no-op).
func (NoopNotifier) MemberInvited(context.Context, string, MemberInvite) error { return nil }

// AccountNotice implements MembershipNotifier (no-op).
func (NoopNotifier) AccountNotice(context.Context, string, AccountNotice) error { return nil }

var (
	_ MembershipNotifier = NoopNotifier{}
	_ MembershipNotifier = (*ResendNotifier)(nil)
)
