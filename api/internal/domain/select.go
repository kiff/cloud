package domain

import (
	"errors"
	"fmt"
)

// select.go — domain resolution.
//
// Once a tenant can hold more than one domain, every request that
// reaches the runtime must resolve to exactly one (tenant_id,
// domain_id). This file fixes how, and makes a key's domain
// binding an isolation boundary rather than a hint:
//
//   - A domain-scoped key resolves to its bound domain. An explicit
//     `domain` selector on the request that names a *different*
//     domain is rejected — the binding is a wall, not a default the
//     caller can step over.
//   - A tenant-wide key uses the explicit `domain` selector to pick
//     among the tenant's domains; with no selector it falls back to
//     the tenant's default domain for
//     back-compat with single-key / single-domain integrations.
//
// `entity_type` never participates — it stays a validation /
// attribution field, never a router.
//
// ResolveRequestDomain applies the rule on the decide path.

// ErrDomainSelectorMismatch is returned when a domain-scoped key is
// presented with an explicit domain selector that names a different
// domain than the key is bound to. This is the isolation boundary: the
// request is refused (403), never silently redirected to the bound
// domain.
var ErrDomainSelectorMismatch = errors.New("domain: request domain does not match the key's bound domain")

// ResolveRequestDomain resolves the domain selector a decide request
// routes with, for the hot path that defers existence + default
// resolution to the storage layer (the runtime resolver already loads
// by immutable id or current public slug, and LoadByDomain("") yields
// the tenant's default). It returns a value using the "" = default
// convention the resolver understands.
//
// It applies the isolation boundary: a domain-scoped key
// (keyBound != "") refuses a conflicting explicit selector rather than
// being redirected. A tenant-wide key passes the selector through
// ("" → default); whether a named domain actually exists is the
// storage layer's call (LoadByDomain returns ErrDomainNotFound).
func ResolveRequestDomain(keyBound, requested string) (string, error) {
	if keyBound != "" {
		if requested != "" && requested != keyBound {
			return "", fmt.Errorf("%w: requested %q, key bound to %q", ErrDomainSelectorMismatch, requested, keyBound)
		}
		return keyBound, nil
	}
	return requested, nil
}
