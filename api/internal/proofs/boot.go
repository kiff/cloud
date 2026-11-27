package proofs

import (
	"context"
	"log/slog"
	"sort"
	"strings"
)

// Why a configured test tenant is refused.
const (
	RefusedMissing    = "doesn't exist"
	RefusedNotOwn     = "isn't one of KIFF's own (plan internal)"
	RefusedDevices    = "has devices that aren't proof test keys"
	RefusedUnreadable = "couldn't be checked: a store was unreadable"
	RefusedPruned     = "is also pruned (KIFF_PRUNE_TENANTS): a test account's data is never pruned"
)

// TestTenantCheck says why a tenant can't be a proof test tenant (one of the
// Refused* reasons), or "" when it can. It returns an error only when a
// store can't be read.
type TestTenantCheck func(ctx context.Context, id string) (refusal string, err error)

// RefusedTenant is a configured test tenant that isn't used, and why.
type RefusedTenant struct{ ID, Why string }

// VetTestTenants splits the configured test tenants into those that may be
// test tenants and those refused. A test tenant must be KIFF's own account
// holding only proof test keys: the proof test phone may approve there, and
// runs may name it. On a store error nothing is vetted: every id is refused
// (fail closed).
func VetTestTenants(ctx context.Context, ids []string, check TestTenantCheck) (ok []string, refused []RefusedTenant, err error) {
	for _, id := range ids {
		why, e := check(ctx, id)
		if e != nil {
			all := make([]RefusedTenant, 0, len(ids))
			for _, each := range ids {
				all = append(all, RefusedTenant{ID: each, Why: RefusedUnreadable})
			}
			return nil, all, e
		}
		if why == "" {
			ok = append(ok, id)
		} else {
			refused = append(refused, RefusedTenant{ID: id, Why: why})
		}
	}
	return ok, refused, nil
}

// OnlyTestKeys reports whether every key given (SPKI DER) is a proof test
// key. No keys at all is true; with no test key listed, any key is false.
func (k TestKeys) OnlyTestKeys(keys [][]byte) bool {
	for _, der := range keys {
		if !k.Has(der) {
			return false
		}
	}
	return true
}

// Fingerprints returns each configured test key's 16-character fingerprint,
// sorted: the first 16 hex characters of its SHA-256, the short form the
// list accepts. A fingerprint names a public key; it's never a secret.
func (k TestKeys) Fingerprints() []string {
	seen := map[string]bool{}
	for f := range k.full {
		seen[f[:16]] = true
	}
	for f := range k.short {
		seen[f] = true
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// BootConfig is what the API runs the proofs with, as logged once at boot.
// It holds counts, ids and fingerprints only: never a token's value.
type BootConfig struct {
	TestTenants    []string        // the vetted test tenants
	Refused        []RefusedTenant // configured test tenants not used, and why
	Vetted         bool            // false when there was no tenant store to ask
	TestKeys       TestKeys
	ReleaseAnyway  bool
	StateTokens    int // how many planque read tokens are set
	RunnerVersions int // how many runner versions are listed
	IngestMounted  bool
}

// LogBoot writes the proofs' effective settings once, so a change in them
// shows in the deploy's log, and names each refused test tenant and why.
func LogBoot(logger *slog.Logger, c BootConfig) {
	logger.Info("proofs: settings",
		slog.Int("test_tenants", len(c.TestTenants)),
		slog.String("test_tenant_ids", strings.Join(c.TestTenants, ",")),
		slog.Int("test_keys", len(c.TestKeys.Fingerprints())),
		slog.String("test_key_fingerprints", strings.Join(c.TestKeys.Fingerprints(), ",")),
		slog.Bool("release_anyway", c.ReleaseAnyway),
		slog.Int("state_tokens", c.StateTokens),
		slog.Int("runner_versions", c.RunnerVersions),
		slog.Bool("ingest_mounted", c.IngestMounted),
	)
	if !c.Vetted {
		logger.Warn("proofs: couldn't tell whether the test tenants are KIFF's own; check KIFF_PROOF_TEST_TENANTS by hand",
			slog.String("test_tenant_ids", strings.Join(c.TestTenants, ",")))
	}
	for _, r := range c.Refused {
		logger.Error("proofs: test tenant refused: the ingest isn't mounted and the test keys are refused there",
			slog.String("tenant", r.ID), slog.String("why", r.Why))
	}
}
