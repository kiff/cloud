package notify

import (
	"bytes"
	"context"
	"fmt"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/kiff/cloud/api/internal/words"
)

// HeldAction is work an agent asked for outside its Card, held for the
// Card's owner. Everything in it comes
// from the owner's own account; it is still escaped, because the agent
// chose the action name and its arguments.
type HeldAction struct {
	// Agent is the Card holder that asked.
	Agent string
	// Action is what it asked to do, and Amount what it carries ("4700
	// amount_eur"), empty when the action has no amount.
	Action string
	Amount string
	// Outside is one line per Card the action is outside of, such as
	// "card-refunds allows up to 500 amount_eur per action".
	Outside []string
	// ExpiresAt is when the hold stops waiting and is refused; zero when
	// it does not expire.
	ExpiresAt time.Time
	// ReviewURL is the absolute Needs you link for this hold. It is the
	// only place the owner answers, after signing in.
	ReviewURL string
	// NextSteps are the answers the Card offers besides approve and
	// reject, such as "return_first"). Each is a refusal that tells
	// the agent what to do instead.
	NextSteps []string
	// Lang is the recipient's language (personlang); English when empty.
	Lang words.Lang
}

// The answers offered in the email, with the Card's next steps between
// "Change the Card" and "Reject", as on Needs you. Every button opens the
// hold on Needs you; nothing is answered until the owner, signed in,
// chooses there. A link that answered by itself would let a mail
// scanner's prefetch, or anyone the email is forwarded to, approve.
type heldView struct {
	HeldAction
	Answers []heldAnswerLink
	// The email's sentences in the recipient's language (internal/words):
	// Intro and Intro HTML say who wants what; the rest are as worded.
	Intro       string
	IntroHTML   htmltemplate.HTML
	NothingDone string
	Open        string
	Options     string
	Steps       string
	Expires     string
	NoApprove   string
	ButtonsNote string
}

type heldAnswerLink struct{ Label, URL string }

func newHeldView(h HeldAction) heldView {
	w := words.Of(h.Lang)
	v := heldView{HeldAction: h, NothingDone: w.HeldNothingDone, Open: w.HeldOpen, Options: w.HeldOptions,
		NoApprove: w.HeldNoApprove, ButtonsNote: w.HeldButtonsNote}
	amount := ""
	if h.Amount != "" {
		amount = " " + h.Amount
	}
	v.Intro = w.HeldIntro(h.Agent, h.Action, amount)
	esc := htmltemplate.HTMLEscapeString
	v.IntroHTML = htmltemplate.HTML(w.HeldIntro("<strong>"+esc(h.Agent)+"</strong>", "<strong>"+esc(h.Action)+"</strong>", esc(amount))) //nolint:gosec // every part the agent chose is escaped
	if !h.ExpiresAt.IsZero() {
		v.Expires = w.HeldExpires(w.DateTime(h.ExpiresAt))
	}
	var steps []string
	for _, s := range h.NextSteps {
		if l := nextStepLabel(s); l != "" {
			steps = append(steps, l)
		}
	}
	if len(steps) > 0 {
		v.Steps = w.HeldSteps(strings.Join(steps, ", "))
	}
	labels := append(append([]string{w.HeldApproveOnce, w.HeldChangeCard}, steps...), w.HeldReject)
	for _, label := range labels {
		v.Answers = append(v.Answers, heldAnswerLink{Label: label, URL: h.ReviewURL})
	}
	return v
}

// nextStepLabel is a next step in words, as Needs you shows it:
// return_first reads "Return first".
func nextStepLabel(step string) string {
	s := strings.TrimSpace(strings.ReplaceAll(step, "_", " "))
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

var heldText = texttemplate.Must(texttemplate.New("held").Parse(`{{.Intro}}
{{range .Outside}}
- {{.}}{{end}}

{{.NothingDone}} {{.Open}}
{{.ReviewURL}}

{{.Options}}{{if .Steps}} {{.Steps}}{{end}}{{if .Expires}} {{.Expires}}{{end}}

{{.NoApprove}}

— KIFF
`))

// heldMail is the email on the shared layout (layout.go): who wants what,
// what it would do, then a button per answer that opens the hold.
func heldMail(v heldView) mail {
	paras := v.NothingDone
	if v.Expires != "" {
		paras += " " + v.Expires
	}
	m := mail{Lang: v.Lang, LeadHTML: v.IntroHTML, List: v.Outside, Paras: []string{paras},
		Small: []string{v.ButtonsNote + " " + v.NoApprove}}
	for _, a := range v.Answers {
		m.Buttons = append(m.Buttons, mailButton{Label: a.Label, URL: a.URL})
	}
	return m
}

// renderHeld returns the subject, text and HTML of a held-action email.
func renderHeld(h HeldAction) (subject, text, html string, err error) {
	if strings.TrimSpace(h.ReviewURL) == "" {
		return "", "", "", fmt.Errorf("notify: a held action needs its review link")
	}
	v := newHeldView(h)
	var t bytes.Buffer
	if err := heldText.Execute(&t, v); err != nil {
		return "", "", "", err
	}
	subject = words.Of(h.Lang).HeldSubject
	m := heldMail(v)
	m.Title = subject
	page, err := renderLayout(m)
	if err != nil {
		return "", "", "", err
	}
	return subject, t.String(), page, nil
}

// ActionHeld implements Notifier.
func (r *ResendNotifier) ActionHeld(ctx context.Context, to string, h HeldAction) error {
	subject, text, html, err := renderHeld(h)
	if err != nil {
		return err
	}
	return r.sendHTML(ctx, to, subject, text, html)
}

// ActionHeld implements Notifier (no-op).
func (NoopNotifier) ActionHeld(context.Context, string, HeldAction) error { return nil }
