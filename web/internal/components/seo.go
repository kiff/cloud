package components

import (
	"context"
	"strings"
)

// SiteBaseURL is the canonical public origin. Canonical + og:url are
// built from this plus the request path so every public page advertises
// its own URL (previously every page hardcoded the root, which hurt
// retrieval and canonicalization).
const SiteBaseURL = "https://kiff.dev"

// OGImagePath is the default 1200x630 share image public pages advertise
// (og:image and twitter:image). The shell already asked for a large
// summary card, so without an image a shared link rendered blank.
const OGImagePath = "/static/img/og/kiff-og.png"

// OGImageAlt describes the share image for screen readers on the
// platforms that read og:image:alt.
const OGImageAlt = "KIFF: a Card for every AI agent. Which actions it may take, how much, and what happens outside it."

type ctxKey int

const (
	canonicalPathKey ctxKey = iota
	shareImageKey
)

type shareImage struct{ URL, Alt string }

// WithShareImage overrides the default image with a trusted public asset URL.
func WithShareImage(ctx context.Context, imageURL, alt string) context.Context {
	return context.WithValue(ctx, shareImageKey, shareImage{imageURL, alt})
}

func HasShareImage(ctx context.Context) bool {
	image, ok := ctx.Value(shareImageKey).(shareImage)
	return ok && image.URL != ""
}

func ShareImageURL(ctx context.Context) string {
	if image, ok := ctx.Value(shareImageKey).(shareImage); ok && image.URL != "" {
		return image.URL
	}
	return SiteBaseURL + OGImagePath
}

func ShareImageAlt(ctx context.Context) string {
	if image, ok := ctx.Value(shareImageKey).(shareImage); ok && image.URL != "" {
		return image.Alt
	}
	return OGImageAlt
}

// WithCanonicalPath stores the request path so the shared shell can build
// a per-page canonical URL. A middleware sets this once for public pages;
// no per-page wiring needed.
func WithCanonicalPath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, canonicalPathKey, path)
}

// CanonicalPath is the request path a middleware stored for the page
// being rendered, or "" outside a request.
func CanonicalPath(ctx context.Context) string {
	p, _ := ctx.Value(canonicalPathKey).(string)
	return p
}

// CanonicalURL returns the absolute canonical URL for the current page:
// SiteBaseURL + the stored path (query stripped, trailing slash trimmed).
// Falls back to SiteBaseURL when no path is set (e.g. render outside a
// request, or the root).
func CanonicalURL(ctx context.Context) string {
	p, _ := ctx.Value(canonicalPathKey).(string)
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return SiteBaseURL
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return SiteBaseURL + p
}

// siteJSONLD is the site-wide structured data (SoftwareApplication +
// Organization) advertised in the head of every public page, so
// retrieval/discovery agents get a single machine-readable description
// of what KIFF is and where its interfaces live. Honest and
// property-led: no fabricated ratings, pricing, or compliance.
//
// Kept as a raw string (rendered via templ.Raw) because templ parses
// "{" in element text as a Go expression; JSON-LD must pass through
// verbatim. Contains no "</script>" sequence.
const siteJSONLD = `{
  "@context": "https://schema.org",
  "@graph": [
    {
      "@type": "Organization",
      "@id": "https://kiff.dev/#org",
      "name": "KIFF",
      "url": "https://kiff.dev",
      "logo": "https://kiff.dev/static/img/logo/kiff-logo-transparent-512.png",
      "email": "gabriel@kiff.dev",
      "sameAs": ["https://github.com/kiff/kiff", "https://github.com/kiff/kiff-guard"]
    },
    {
      "@type": "SoftwareApplication",
      "name": "KIFF",
      "applicationCategory": "DeveloperApplication",
      "operatingSystem": "Any",
      "url": "https://kiff.dev",
      "description": "KIFF gives each AI agent a Card: which actions it may take, how much per action and over a window, and what happens outside it. KIFF checks every call before it runs, against the business's own state; a call outside the Card waits for the owner or is refused, and every decision has a signed, tamper-evident receipt.",
      "image": "https://kiff.dev/static/img/og/kiff-og.png",
      "license": "https://opensource.org/licenses/MIT",
      "offers": {"@type": "Offer", "price": "0", "priceCurrency": "USD", "description": "MIT framework, self-hostable; KIFF Cloud is the hosted runtime."},
      "softwareHelp": "https://kiff.dev/docs"
    }
  ]
}`

// SiteJSONLDScript returns the full <script type="application/ld+json">
// element for the shell head. Emitted via templ.Raw as a normal child
// (templ treats <script> as a raw-text element, so an expression placed
// INSIDE the tag would not execute — the whole element must be raw).
func SiteJSONLDScript() string {
	return `<script type="application/ld+json">` + siteJSONLD + `</script>`
}
