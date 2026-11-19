package handlers

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// Evidence window presets. The API already accepts an absolute
// ?from=&?to= window (parseProofWindow, default 30 days); the web
// exposes a small set of relative presets and computes the absolute
// bounds server-side at request time, so the selector stays no-JS
// and the links never carry stale timestamps.
type windowPreset struct {
	key   string        // ?window= value
	label string        // button label
	span  time.Duration // lookback span
}

// evidenceWindowPresets spans from a 5-minute live view (the "requests
// over time right now" case the Activity chart wants) to 90 days (the
// long evidence window). The short presets make the requests-over-time
// series legible at a fine resolution; the long ones keep the evidence
// review use case. The default stays 30 days so an absent selector and
// an explicit "30d" render identically (matching the API default).
var evidenceWindowPresets = []windowPreset{
	{key: "5m", label: "5 min", span: 5 * time.Minute},
	{key: "15m", label: "15 min", span: 15 * time.Minute},
	{key: "1h", label: "1 hour", span: time.Hour},
	{key: "6h", label: "6 hours", span: 6 * time.Hour},
	{key: "24h", label: "24 hours", span: 24 * time.Hour},
	{key: "7d", label: "7 days", span: 7 * 24 * time.Hour},
	{key: "30d", label: "30 days", span: 30 * 24 * time.Hour},
	{key: "90d", label: "90 days", span: 90 * 24 * time.Hour},
}

const defaultWindowPreset = "30d"
const defaultWindowSpan = 30 * 24 * time.Hour

// windowBoundsFor resolves a preset key to absolute [from,to] bounds
// plus the resolved key. An unknown/empty key falls back to the 30-day
// default — matching the API's own default so an absent selector and an
// explicit "30d" render identically.
func windowBoundsFor(preset string, now time.Time) (from, to time.Time, resolved string) {
	for _, p := range evidenceWindowPresets {
		if p.key == preset {
			return now.Add(-p.span).UTC(), now.UTC(), p.key
		}
	}
	return now.Add(-defaultWindowSpan).UTC(), now.UTC(), defaultWindowPreset
}

// effectiveWindow resolves the window preset for a Govern/Activity
// render: an explicit ?window= wins; otherwise the remembered LastWindow
// is restored; otherwise the 30-day default. It returns the bounds, the
// resolved preset, and the raw preset to persist (non-empty only when
// ?window= was explicit, so navigation restores it but doesn't churn the
// session on every default render). Persisting is the caller's job (via
// rememberScope) so the session is issued at most once per request.
func effectiveWindow(r *http.Request, s session.Session, now time.Time) (from, to time.Time, preset, persist string) {
	param := strings.TrimSpace(r.URL.Query().Get("window"))
	effective := param
	if effective == "" {
		effective = strings.TrimSpace(s.LastWindow)
	}
	from, to, preset = windowBoundsFor(effective, now)
	if param != "" {
		persist = preset
	}
	return from, to, preset, persist
}

// evidenceWindowOptions builds the selector model for the current
// request: one option per preset, each an href that keeps every other
// query param and only swaps window=. Active marks the resolved preset.
func evidenceWindowOptions(r *http.Request, active string) []pages.WindowOption {
	opts := make([]pages.WindowOption, 0, len(evidenceWindowPresets))
	for _, p := range evidenceWindowPresets {
		q := cloneQuery(r.URL.Query())
		q.Set("window", p.key)
		opts = append(opts, pages.WindowOption{
			Label:  p.label,
			Href:   r.URL.Path + "?" + q.Encode(),
			Active: p.key == active,
		})
	}
	return opts
}

func cloneQuery(in url.Values) url.Values {
	out := make(url.Values, len(in))
	for k, v := range in {
		vv := make([]string, len(v))
		copy(vv, v)
		out[k] = vv
	}
	return out
}
