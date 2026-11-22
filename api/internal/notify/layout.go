package notify

import (
	"bytes"
	htmltemplate "html/template"

	"github.com/kiff/cloud/api/internal/words"
)

// Every email KIFF sends is one `mail` drawn by `layout` (docs/email.md): the
// same header, the same order of parts, the same buttons and footer, so a
// person knows a KIFF email at a glance and a new email is a list of parts,
// not a new page of HTML. The words are the caller's, in the person's
// language; the layout has none of its own.
//
// Everything is escaped: Lead, Paras, Code, Rows, Note, List, Small and the
// buttons' labels and links are plain text, and only LeadHTML is markup, which
// the caller escapes part by part (the held email's agent and action).

// mailButton is a link drawn as a button. The first is the one to press.
type mailButton struct{ Label, URL string }

// mail is what an email says, in the order it reads.
type mail struct {
	// Lang is the person's language, for the page's `lang` and its direction.
	Lang words.Lang
	// Title is the subject, as the page's <title>.
	Title string
	// Brand is the wordmark: "KIFF", or "tafs by KIFF"; Logos are drawn
	// before it (absolute URLs), none when empty.
	Brand string
	Logos []string
	// Lead is the sentence the email opens with; LeadHTML replaces it when
	// the caller built it with markup and escaped every part.
	Lead     string
	LeadHTML htmltemplate.HTML
	// Code is a sign-in code, large, in a monospace face; Paras follow it.
	Code string
	// Paras are the body's paragraphs, in order; Warn is one in bold that
	// comes after them (the code email's "never give this code to anyone").
	Paras []string
	Warn  string
	// List are bullets; Rows a table of details; Note a small line under
	// the table.
	List []string
	Rows []newDeviceRow
	Note string
	// Buttons are links drawn as buttons; Small are small grey paragraphs
	// that close the email (why it was sent, what to do if it wasn't you).
	Buttons []mailButton
	Small   []string
}

// rtl is true for the languages that read right to left.
func rtl(l words.Lang) bool {
	switch string(l) {
	case "ar", "he", "dv", "fa", "ur":
		return true
	}
	return false
}

type layoutView struct {
	mail
	Dir, LangTag string
	Align        string
}

// layout draws m as the HTML part of an email: tables and inline styles, no
// script, no web font, one column that fits a phone.
var layout = htmltemplate.Must(htmltemplate.New("layout").Parse(`<!doctype html><html lang="{{.LangTag}}" dir="{{.Dir}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><meta name="supported-color-schemes" content="light dark"><title>{{.Title}}</title></head>
<body style="margin:0;padding:0">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td style="padding:24px 16px;font-family:system-ui,-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;font-size:15px;line-height:1.5;color:#1a1a1a;text-align:{{.Align}}">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="max-width:560px">
<tr><td style="padding:0 0 16px;text-align:{{.Align}}">{{range .Logos}}<img src="{{.}}" alt="" width="24" height="24" style="vertical-align:middle;margin:0 6px">{{end}}<strong style="vertical-align:middle">{{.Brand}}</strong></td></tr>
<tr><td style="padding:0 0 12px;text-align:{{.Align}}">{{if .LeadHTML}}{{.LeadHTML}}{{else}}{{.Lead}}{{end}}</td></tr>{{if .Code}}
<tr><td dir="ltr" style="padding:4px 0 12px;font-family:ui-monospace,Menlo,Consolas,monospace;font-size:30px;font-weight:700;letter-spacing:6px;text-align:{{.Align}}">{{.Code}}</td></tr>{{end}}{{range .Paras}}
<tr><td style="padding:0 0 12px;text-align:{{$.Align}}">{{.}}</td></tr>{{end}}{{if .Warn}}
<tr><td style="padding:0 0 12px;text-align:{{.Align}}"><strong>{{.Warn}}</strong></td></tr>{{end}}{{if .List}}
<tr><td style="padding:0 0 12px;text-align:{{.Align}}"><ul style="margin:0;padding-inline-start:20px">{{range .List}}<li>{{.}}</li>{{end}}</ul></td></tr>{{end}}{{if .Rows}}
<tr><td style="padding:0 0 16px"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="border:1px solid #d0d0d0;border-radius:8px;border-collapse:separate">
{{range $i, $r := .Rows}}<tr><td width="38%" style="padding:10px 12px;{{if $i}}border-top:1px solid #d0d0d0;{{end}}color:#666666;font-size:13px;vertical-align:top;text-align:{{$.Align}}">{{$r.Label}}</td><td style="padding:10px 12px;{{if $i}}border-top:1px solid #d0d0d0;{{end}}font-weight:600;vertical-align:top;text-align:{{$.Align}}"><bdi>{{$r.Value}}</bdi></td></tr>
{{end}}</table>{{if .Note}}
<p style="margin:8px 0 0;color:#666666;font-size:13px;text-align:{{.Align}}">{{.Note}}</p>{{end}}</td></tr>{{end}}{{if .Buttons}}
<tr><td style="padding:0 0 16px;text-align:{{.Align}}">{{range $i, $b := .Buttons}}<a href="{{$b.URL}}" style="display:inline-block;margin:0 8px 8px 0;padding:9px 16px;border:1px solid #1a1a1a;border-radius:6px;{{if $i}}color:#1a1a1a;{{else}}background:#1a1a1a;color:#ffffff;{{end}}text-decoration:none">{{$b.Label}}</a>{{end}}</td></tr>{{end}}{{range .Small}}
<tr><td style="padding:0 0 12px;color:#666666;font-size:13px;text-align:{{$.Align}}">{{.}}</td></tr>{{end}}
<tr><td style="padding:12px 0 0;border-top:1px solid #e0e0e0;color:#888888;font-size:12px;text-align:{{.Align}}">{{.Brand}} · kiff.dev</td></tr>
</table></td></tr></table>
</body></html>`))

// renderLayout is m as an email's HTML.
func renderLayout(m mail) (string, error) {
	v := layoutView{mail: m, Dir: "ltr", LangTag: string(m.Lang), Align: "left"}
	if v.LangTag == "" {
		v.LangTag = string(words.EN)
	}
	if v.Brand == "" {
		v.Brand = "KIFF"
	}
	if rtl(m.Lang) {
		v.Dir, v.Align = "rtl", "right"
	}
	var b bytes.Buffer
	if err := layout.Execute(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}
