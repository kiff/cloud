package domain

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// slug.go — domain identity helpers for the multi-domain foundation.
//
// Today a tenant has exactly one domain, keyed by tenant_id. Multi-domain
// makes the domain a first-class (tenant_id, domain_id) object. This
// file introduces the two identity values every layer will key on,
// derived from the domain's authored name:
//
//   - Slug: a stable, URL-safe, lowercased token for the {domain}
//     path segment (`/dashboard/domains/{domain}`,
//     `/v1/me/domains/{domain}`). Human-facing and
//     mutable on rename.
//   - DomainID: the stable per-tenant identity a domain keeps for
//     life. Decision 6 it is *derived from the current
//     slug at migration* (so DomainID == Slug today) and only
//     diverges once explicit rename lands in a later slice.
//
// Introducing these as derived values first — read off the domain
// YAML at load time, no schema or interface change — lets the
// resolver, metering, and record-stamping work key on a real
// domain_id before the storage layer moves to a composite key.
// Nothing keys on these fields yet; this is the foundation the rest
// of multi-domain builds on.

// Slugify converts an authored domain name into a stable, URL-safe
// token: lowercased, with runs of spaces / underscores / slashes /
// hyphens collapsed to a single hyphen, and any other character
// dropped. Leading and trailing hyphens are trimmed.
//
//	"Refund Flow"   -> "refund-flow"
//	"Vendor_Payments" -> "vendor-payments"
//	"refunds"       -> "refunds"
//
// The result is empty only when the input has no alphanumeric
// content. Slugify is deterministic so the same name always derives
// the same identity.
func Slugify(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	b.Grow(len(lower))
	lastHyphen := false
	for _, r := range lower {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastHyphen = false
		case r == ' ' || r == '_' || r == '-' || r == '/' || r == '.':
			// Collapse separators to a single hyphen, never leading.
			if b.Len() > 0 && !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		default:
			// Drop anything else (punctuation, symbols, non-ASCII).
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// DomainNameFromYAML extracts the authored `domain:` name from a
// domain YAML document without running the full registry-backed
// ParseAndBuild. It is deliberately lenient: a parse error or a
// missing/blank field yields "" rather than an error, mirroring the
// read paths that already tolerate stored YAML that no longer parses
// against the current registry. Callers treat "" as "no derivable
// identity".
func DomainNameFromYAML(y []byte) string {
	var doc struct {
		Domain string `yaml:"domain"`
	}
	if err := yaml.Unmarshal(y, &doc); err != nil {
		return ""
	}
	return strings.TrimSpace(doc.Domain)
}

// SlugFromYAML derives the URL-safe slug for a domain YAML document,
// or "" when no domain name can be read. Equivalent to
// Slugify(DomainNameFromYAML(y)).
func SlugFromYAML(y []byte) string {
	return Slugify(DomainNameFromYAML(y))
}

// DefaultDomainID is the storage fallback used when a domain YAML
// carries no readable name. The storage key (tenant_id, domain_id)
// must never be empty, so a nameless domain is keyed "default"
// rather than "" (a review flagged the empty case to settle at the
// composite-key migration).
const DefaultDomainID = "default"

// DeriveStorageIdentity returns the (domain_id, slug) a domain YAML
// is stored under. Unlike SlugFromYAML — which is a read-time
// projection that may be "" — this never returns empty: a nameless
// domain falls back to DefaultDomainID so the composite storage key
// is always present. domain_id == slug at first write;
// they diverge only once rename lands, which keeps domain_id stable
// while slug follows the name.
func DeriveStorageIdentity(y []byte) (domainID, slug string) {
	s := SlugFromYAML(y)
	if s == "" {
		s = DefaultDomainID
	}
	return s, s
}
