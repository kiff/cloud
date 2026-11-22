package notify

import (
	"bytes"
	"context"
	"fmt"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/kiff/cloud/api/internal/words"
)

// NewDevice is a sign-in on a device the person's account hasn't seen,
// as the sign-in provider described it when the email went. Device,
// browser and place come from the provider (its guess for the place, from
// the network), so the email escapes them and calls the place approximate.
// A detail the provider didn't give has no row: never "unknown".
type NewDevice struct {
	// Device is the device ("iPhone", "Android"), Browser its browser
	// ("Chrome"); either may be empty.
	Device, Browser string
	// Method is how the person signed in, when it's certain: "password",
	// or a provider's name ("Google"). Empty when it isn't.
	Method string
	// City and Country are the network's guess; either may be empty. A
	// two-letter country is a code, named in the person's language.
	City, Country string
	// At is when the account signed in, shown in Zone, the account's time
	// zone (tenant.TimeZoneFor); UTC when nil.
	At   time.Time
	Zone *time.Location
	// Password: the person signs in with a password and this sign-in isn't
	// known to be another way, so "if it wasn't you" also asks for a new
	// password.
	Password bool
	// Tafs: the sign-in came through the tafs app, so the email says "tafs
	// by KIFF" with both logos.
	Tafs bool
	// SessionsURL is where the person signs a device out; SiteURL is
	// kiff.dev, for the logos.
	SessionsURL, SiteURL string
	// Lang is the person's language (personlang); English when empty.
	Lang words.Lang
	// Key makes a resend of the same email a no-op at the provider
	// ("new-device/<session id>"); none when empty.
	Key string
}

// TafsByKIFF is the name a sign-in through the tafs app wears.
const TafsByKIFF = "tafs by KIFF"

// newDeviceRow is one detail of the sign-in, the same in both parts; Line
// is how the text part writes it.
type newDeviceRow struct{ Label, Value, Line string }

type newDeviceView struct {
	SessionsURL                                    string
	Subject, Intro, PlaceNote, WasYou, NotYou, Why string
	Sessions, Brand, KIFFLogo, TafsLogo            string
	Rows                                           []newDeviceRow
}

// zoneName names a time zone for the person: "Paris time" for
// Europe/Paris, its own name for one without a city ("UTC").
func zoneName(w *words.Words, loc *time.Location) string {
	if loc == nil {
		return "UTC"
	}
	name := loc.String()
	i := strings.LastIndex(name, "/")
	if i < 0 || strings.HasPrefix(name, "Etc/") {
		if name == "" || name == "Local" {
			return "UTC"
		}
		return strings.TrimPrefix(name, "Etc/")
	}
	return w.NewDeviceZone(strings.ReplaceAll(name[i+1:], "_", " "))
}

// country is the provider's country: a code named in the language, any
// other word as given.
func country(c string, lang words.Lang) string {
	c = strings.TrimSpace(c)
	if len(c) != 2 {
		return c
	}
	r, err := language.ParseRegion(c)
	if err != nil {
		return c
	}
	tag, err := language.Parse(string(lang))
	if err != nil {
		tag = language.English
	}
	// display.Regions has no names for a language CLDR doesn't cover (the Dhivehi
	// or Dzongkha code emails): a nil namer, so it reads in English then.
	namer := display.Regions(tag)
	if namer == nil {
		namer = display.Regions(language.English)
	}
	if n := namer.Name(r); n != "" {
		return n
	}
	return c
}

func (d NewDevice) place() string {
	var parts []string
	for _, p := range []string{strings.TrimSpace(d.City), country(d.Country, d.Lang)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

func newNewDeviceView(d NewDevice) newDeviceView {
	w := words.Of(d.Lang)
	site := strings.TrimRight(d.SiteURL, "/")
	v := newDeviceView{SessionsURL: d.SessionsURL, Subject: w.NewDeviceSubject, Intro: w.NewDeviceIntro,
		WasYou: w.NewDeviceWasYou, NotYou: w.NewDeviceNotYouSignOut, Sessions: w.NewDeviceSessions, Why: w.NewDeviceWhy,
		Brand: "KIFF", KIFFLogo: site + "/static/img/logo/kiff-logo-transparent-512.png"}
	if d.Tafs {
		v.Subject, v.Intro, v.Brand = w.NewDeviceSubjectTafs, w.NewDeviceIntroTafs, TafsByKIFF
		v.TafsLogo = site + "/static/img/tafs/tafs-icon-512.png"
	}
	if d.Password {
		v.NotYou = w.NewDeviceNotYou
	}
	v.Rows = append(v.Rows, newDeviceRow{Label: w.NewDeviceWhen, Value: w.DateTimeIn(d.At, d.Zone) + " (" + zoneName(w, d.Zone) + ")"})
	device, browser := strings.TrimSpace(d.Device), strings.TrimSpace(d.Browser)
	switch {
	case device != "" && browser != "":
		v.Rows = append(v.Rows, newDeviceRow{Label: w.NewDeviceDevice, Value: w.NewDeviceOn(browser, device)})
	case device != "":
		v.Rows = append(v.Rows, newDeviceRow{Label: w.NewDeviceDevice, Value: device})
	case browser != "":
		v.Rows = append(v.Rows, newDeviceRow{Label: w.NewDeviceBrowser, Value: browser})
	}
	switch m := strings.TrimSpace(d.Method); m {
	case "":
	case "password":
		v.Rows = append(v.Rows, newDeviceRow{Label: w.NewDeviceMethod, Value: w.NewDevicePassword})
	default:
		v.Rows = append(v.Rows, newDeviceRow{Label: w.NewDeviceMethod, Value: m})
	}
	// "Near" a city; a country alone is said as it is ("France", not
	// "near France").
	if p := d.place(); p != "" {
		if strings.TrimSpace(d.City) != "" {
			p = w.NewDeviceNear(p)
		}
		v.Rows = append(v.Rows, newDeviceRow{Label: w.NewDevicePlace, Value: p})
		v.PlaceNote = w.NewDevicePlaceNote
	}
	for i := range v.Rows {
		v.Rows[i].Line = w.NewDeviceRow(v.Rows[i].Label, v.Rows[i].Value)
	}
	return v
}

var newDeviceText = texttemplate.Must(texttemplate.New("new-device").Parse(`{{.Intro}}
{{range .Rows}}
{{.Line}}{{end}}{{if .PlaceNote}}
{{.PlaceNote}}{{end}}

{{.WasYou}}
{{.NotYou}}
{{.SessionsURL}}

{{.Why}}

— {{.Brand}}
`))

// The HTML is what mail apps render alike: tables, inline styles, no
// script and no web font; one column that fits a phone.
var newDeviceHTML = htmltemplate.Must(htmltemplate.New("new-device").Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><meta name="supported-color-schemes" content="light dark"></head>
<body style="margin:0;padding:0">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td style="padding:24px 16px;font-family:system-ui,-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;font-size:15px;line-height:1.5;color:#1a1a1a">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="max-width:560px">
<tr><td style="padding:0 0 16px">{{if .TafsLogo}}<img src="{{.TafsLogo}}" alt="" width="24" height="24" style="vertical-align:middle;margin-right:6px">{{end}}<img src="{{.KIFFLogo}}" alt="" width="24" height="24" style="vertical-align:middle;margin-right:6px"><strong style="vertical-align:middle">{{.Brand}}</strong></td></tr>
<tr><td style="padding:0 0 12px">{{.Intro}}</td></tr>
<tr><td style="padding:0 0 16px"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="border:1px solid #d0d0d0;border-radius:8px;border-collapse:separate">
{{range $i, $r := .Rows}}<tr><td width="38%" style="padding:10px 12px;{{if $i}}border-top:1px solid #d0d0d0;{{end}}color:#666666;font-size:13px;vertical-align:top">{{$r.Label}}</td><td style="padding:10px 12px;{{if $i}}border-top:1px solid #d0d0d0;{{end}}font-weight:600;vertical-align:top">{{$r.Value}}</td></tr>
{{end}}</table>{{if .PlaceNote}}
<p style="margin:8px 0 0;color:#666666;font-size:13px">{{.PlaceNote}}</p>{{end}}</td></tr>
<tr><td style="padding:0 0 16px">{{.WasYou}}<br>{{.NotYou}}</td></tr>
<tr><td style="padding:0 0 16px"><a href="{{.SessionsURL}}" style="display:inline-block;padding:8px 14px;border:1px solid #1a1a1a;border-radius:6px;color:#1a1a1a;text-decoration:none">{{.Sessions}}</a></td></tr>
<tr><td style="color:#666666;font-size:13px">{{.Why}}</td></tr>
</table></td></tr></table>
</body></html>`))

// renderNewDevice returns the subject, text and HTML of a new-device email.
func renderNewDevice(d NewDevice) (subject, text, html string, err error) {
	if strings.TrimSpace(d.SessionsURL) == "" || strings.TrimSpace(d.SiteURL) == "" {
		return "", "", "", fmt.Errorf("notify: a new-device email needs its sessions link and the site")
	}
	v := newNewDeviceView(d)
	var t, b bytes.Buffer
	if err := newDeviceText.Execute(&t, v); err != nil {
		return "", "", "", err
	}
	if err := newDeviceHTML.Execute(&b, v); err != nil {
		return "", "", "", err
	}
	return v.Subject, t.String(), b.String(), nil
}

// NewDeviceSignedIn sends the new-device email.
func (r *ResendNotifier) NewDeviceSignedIn(ctx context.Context, to string, d NewDevice) error {
	subject, text, html, err := renderNewDevice(d)
	if err != nil {
		return err
	}
	return r.sendHTMLWithKey(ctx, to, subject, text, html, d.Key)
}

// NewDeviceSignedIn sends nothing (no-op).
func (NoopNotifier) NewDeviceSignedIn(context.Context, string, NewDevice) error { return nil }
