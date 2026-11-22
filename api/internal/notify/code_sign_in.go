package notify

import (
	"bytes"
	"context"
	htmltemplate "html/template"
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

func askedRows(w *words.Words, lang words.Lang, a CodeAsked) []newDeviceRow {
	rows := []newDeviceRow{{Label: w.CodeAskedWhen, Value: w.DateTimeIn(a.At, time.UTC) + " (UTC)"}}
	if b := strings.TrimSpace(a.Browser); b != "" {
		rows = append(rows, newDeviceRow{Label: w.CodeAskedBrowser, Value: b})
	}
	if c := country(a.Country, lang); c != "" {
		rows = append(rows, newDeviceRow{Label: w.CodeAskedPlace, Value: c})
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

// The HTML is what mail apps render alike, as the new-device email's: tables,
// inline styles, no script; the code large, in a monospace face.
var codeHTML = htmltemplate.Must(htmltemplate.New("code").Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><meta name="supported-color-schemes" content="light dark"></head>
<body style="margin:0;padding:0">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td style="padding:24px 16px;font-family:system-ui,-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;font-size:15px;line-height:1.5;color:#1a1a1a">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="max-width:560px">
<tr><td style="padding:0 0 16px"><strong>KIFF</strong></td></tr>
<tr><td style="padding:0 0 12px">{{.Intro}}</td></tr>{{if .Code}}
<tr><td style="padding:4px 0 12px;font-family:ui-monospace,Menlo,Consolas,monospace;font-size:30px;font-weight:700;letter-spacing:6px">{{.Code}}</td></tr>
<tr><td style="padding:0 0 12px">{{.Valid}}<br><strong>{{.NeverShare}}</strong></td></tr>{{end}}
<tr><td style="padding:0 0 16px"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="border:1px solid #d0d0d0;border-radius:8px;border-collapse:separate">
{{range $i, $r := .Rows}}<tr><td width="38%" style="padding:10px 12px;{{if $i}}border-top:1px solid #d0d0d0;{{end}}color:#666666;font-size:13px;vertical-align:top">{{$r.Label}}</td><td style="padding:10px 12px;{{if $i}}border-top:1px solid #d0d0d0;{{end}}font-weight:600;vertical-align:top">{{$r.Value}}</td></tr>
{{end}}</table></td></tr>
<tr><td style="padding:0 0 16px">{{.NotYou}}</td></tr>
<tr><td style="color:#666666;font-size:13px">{{.Why}}</td></tr>
</table></td></tr></table>
</body></html>`))

func renderCode(v codeView) (string, string, error) {
	var t, b bytes.Buffer
	if err := codeText.Execute(&t, v); err != nil {
		return "", "", err
	}
	if err := codeHTML.Execute(&b, v); err != nil {
		return "", "", err
	}
	return t.String(), b.String(), nil
}

// renderSignInCode is the code email: the code, how long it works, never
// to give it to anyone, and where it was asked for.
func renderSignInCode(code string, lang words.Lang, a CodeAsked) (subject, text, html string, err error) {
	w := words.Of(lang)
	v := codeView{Subject: w.CodeSubject, Intro: w.CodeIntro, Code: code, Valid: w.CodeValid, NeverShare: w.CodeNeverShare,
		NotYou: w.CodeNotYou, Why: w.CodeWhy, Rows: askedRows(w, lang, a)}
	text, html, err = renderCode(v)
	return v.Subject, text, html, err
}

// renderSignInCodeOff is "signing in with a code isn't on", and how to turn
// it on.
func renderSignInCodeOff(lang words.Lang, a CodeAsked) (subject, text, html string, err error) {
	w := words.Of(lang)
	v := codeView{Subject: w.CodeOffSubject, Intro: w.CodeOffIntro + " " + w.CodeOffHow, NotYou: w.CodeOffNotYou, Why: w.CodeWhy,
		Rows: askedRows(w, lang, a)}
	text, html, err = renderCode(v)
	return v.Subject, text, html, err
}

// renderSignInCodeChanged is the notice that the setting was turned on or
// off.
func renderSignInCodeChanged(on bool, lang words.Lang, at time.Time) (subject, text, html string, err error) {
	w := words.Of(lang)
	v := codeView{Subject: w.CodeTurnedOffSubject, Intro: w.CodeTurnedOffIntro, NotYou: w.CodeChangedNotYou}
	if on {
		v.Subject, v.Intro = w.CodeTurnedOnSubject, w.CodeTurnedOnIntro
	}
	v.Rows = askedRows(w, lang, CodeAsked{At: at})
	text, html, err = renderCode(v)
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
