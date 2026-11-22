package notify

import (
	"bytes"
	"context"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/kiff/cloud/api/internal/words"
)

// CodeAsked is where a code was asked for, as the web read it: the browser
// and the network's guess of the country. Either may be empty, and then has
// no row.
type CodeAsked struct {
	At      time.Time
	Browser string
	Country string
}

type codeView struct {
	Subject, Intro, Code, Valid, NeverShare, NotYou, Why string
	Rows                                                 []newDeviceRow
}

func askedRows(w *words.Words, e words.CodeSignInEmail, lang words.Lang, a CodeAsked) []newDeviceRow {
	rows := []newDeviceRow{{Label: e.CodeAskedWhen, Value: w.DateTimeIn(a.At, time.UTC) + " (UTC)"}}
	if b := strings.TrimSpace(a.Browser); b != "" {
		rows = append(rows, newDeviceRow{Label: e.CodeAskedBrowser, Value: b})
	}
	if c := country(a.Country, lang); c != "" {
		rows = append(rows, newDeviceRow{Label: e.CodeAskedPlace, Value: c})
	}
	for i := range rows {
		rows[i].Line = w.NewDeviceRow(rows[i].Label, rows[i].Value)
	}
	return rows
}

var codeText = texttemplate.Must(texttemplate.New("code").Parse(`{{.Intro}}{{if .Code}}

    {{.Code}}

{{.Valid}}
{{.NeverShare}}{{end}}
{{range .Rows}}
{{.Line}}{{end}}

{{.NotYou}}

{{.Why}}

— KIFF
`))

// codeMail is the email on the shared layout (layout.go): the code large,
// in a monospace face.
func codeMail(v codeView, lang words.Lang) mail {
	m := mail{Lang: lang, Title: v.Subject, Lead: v.Intro, Code: v.Code, Rows: v.Rows}
	if v.Code != "" {
		m.Paras = append(m.Paras, v.Valid)
		m.Warn = v.NeverShare
	}
	m.Paras = append(m.Paras, v.NotYou)
	m.Small = []string{v.Why}
	return m
}

func renderCode(v codeView, lang words.Lang) (string, string, error) {
	var t bytes.Buffer
	if err := codeText.Execute(&t, v); err != nil {
		return "", "", err
	}
	page, err := renderLayout(codeMail(v, lang))
	if err != nil {
		return "", "", err
	}
	return t.String(), page, nil
}

// renderSignInCode is the code email: the code, how long it works, never
// to give it to anyone, and where it was asked for.
func renderSignInCode(code string, lang words.Lang, a CodeAsked) (subject, text, html string, err error) {
	w, e := words.Of(lang), words.CodeSignInEmailFor(lang)
	v := codeView{Subject: e.CodeSubject, Intro: e.CodeIntro, Code: code, Valid: e.CodeValid, NeverShare: e.CodeNeverShare,
		NotYou: e.CodeNotYou, Why: e.CodeWhy, Rows: askedRows(w, e, lang, a)}
	text, html, err = renderCode(v, lang)
	return v.Subject, text, html, err
}

// renderSignInCodeOff is "signing in with a code isn't on", and how to turn
// it on.
func renderSignInCodeOff(lang words.Lang, a CodeAsked) (subject, text, html string, err error) {
	w, e := words.Of(lang), words.CodeSignInEmailFor(lang)
	v := codeView{Subject: e.CodeOffSubject, Intro: e.CodeOffIntro + " " + e.CodeOffHow, NotYou: e.CodeOffNotYou, Why: e.CodeWhy,
		Rows: askedRows(w, e, lang, a)}
	text, html, err = renderCode(v, lang)
	return v.Subject, text, html, err
}

// renderSignInCodeChanged is the notice that the setting was turned on or
// off.
func renderSignInCodeChanged(on bool, lang words.Lang, at time.Time) (subject, text, html string, err error) {
	w, e := words.Of(lang), words.CodeSignInEmailFor(lang)
	v := codeView{Subject: e.CodeTurnedOffSubject, Intro: e.CodeTurnedOffIntro, NotYou: e.CodeChangedNotYou}
	if on {
		v.Subject, v.Intro = e.CodeTurnedOnSubject, e.CodeTurnedOnIntro
	}
	v.Rows = askedRows(w, e, lang, CodeAsked{At: at})
	text, html, err = renderCode(v, lang)
	return v.Subject, text, html, err
}

// SignInCode sends a code.
func (r *ResendNotifier) SignInCode(ctx context.Context, to, code string, lang words.Lang, a CodeAsked) error {
	subject, text, html, err := renderSignInCode(code, lang, a)
	if err != nil {
		return err
	}
	return r.sendHTML(ctx, to, subject, text, html)
}

// SignInCodeOff sends "signing in with a code isn't on".
func (r *ResendNotifier) SignInCodeOff(ctx context.Context, to string, lang words.Lang, a CodeAsked) error {
	subject, text, html, err := renderSignInCodeOff(lang, a)
	if err != nil {
		return err
	}
	return r.sendHTML(ctx, to, subject, text, html)
}

// SignInCodeChanged sends the notice that the setting changed.
func (r *ResendNotifier) SignInCodeChanged(ctx context.Context, to string, on bool, lang words.Lang, at time.Time) error {
	subject, text, html, err := renderSignInCodeChanged(on, lang, at)
	if err != nil {
		return err
	}
	return r.sendHTML(ctx, to, subject, text, html)
}

// SignInCode sends nothing (no-op).
func (NoopNotifier) SignInCode(context.Context, string, string, words.Lang, CodeAsked) error {
	return nil
}

// SignInCodeOff sends nothing (no-op).
func (NoopNotifier) SignInCodeOff(context.Context, string, words.Lang, CodeAsked) error { return nil }

// SignInCodeChanged sends nothing (no-op).
func (NoopNotifier) SignInCodeChanged(context.Context, string, bool, words.Lang, time.Time) error {
	return nil
}
