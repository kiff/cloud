package tenant_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/tenant"
)

// TestNewRedactionSaltLengthAndEntropy guards the salt generator's
// two contracts: every salt is exactly RedactionSaltSize bytes, and
// two consecutive salts are not the same (a degraded RNG returning
// zeros would defeat the whole salt design). The probability of a
// collision on a 256-bit random sample is astronomically low, so
// equality is a deterministic test for the failure mode.
func TestNewRedactionSaltLengthAndEntropy(t *testing.T) {
	t.Parallel()

	a, err := tenant.NewRedactionSalt()
	if err != nil {
		t.Fatalf("first salt: %v", err)
	}
	if len(a) != tenant.RedactionSaltSize {
		t.Fatalf("first salt: got %d bytes, want %d", len(a), tenant.RedactionSaltSize)
	}
	b, err := tenant.NewRedactionSalt()
	if err != nil {
		t.Fatalf("second salt: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two consecutive salts are identical (RNG appears stuck)")
	}
}

// TestMemoryStoreCreateGeneratesSalt covers the in-memory path so
// downstream tests that use MemoryStore (the bulk of the API
// suite) automatically get tenants with HasRedactionSalt() == true.
func TestMemoryStoreCreateGeneratesSalt(t *testing.T) {
	t.Parallel()

	s := tenant.NewMemoryStore()
	tt := sampleTenant("salt-mem")
	if err := s.Create(context.Background(), tt); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.HasRedactionSalt() {
		t.Fatalf("HasRedactionSalt: got false, want true (salt was %d bytes)", len(got.RedactionSalt))
	}
}

// TestMemoryStorePreservesProvidedSalt covers the operator-led
// migration path: a Tenant{} literal that already carries a salt
// keeps it through Create. Without this, an admin tool seeding
// salts from a backup would have its salt replaced on insert.
func TestMemoryStorePreservesProvidedSalt(t *testing.T) {
	t.Parallel()

	s := tenant.NewMemoryStore()
	tt := sampleTenant("salt-mem-keep")
	want := bytes.Repeat([]byte{0x42}, tenant.RedactionSaltSize)
	tt.RedactionSalt = want
	if err := s.Create(context.Background(), tt); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got.RedactionSalt, want) {
		t.Fatalf("salt: got %x, want %x", got.RedactionSalt, want)
	}
}

// TestPostgresStoreCreateGeneratesSalt is the Postgres-backed
// equivalent. Skipped when KIFF_TEST_POSTGRES_URL is unset (same
// gate as TestPostgresStoreCRUD).
func TestPostgresStoreCreateGeneratesSalt(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	defer truncateTenants(t, pool)

	store := tenant.NewPostgresStore(pool)
	tt := sampleTenant("salt-pg")
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.Get(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.HasRedactionSalt() {
		t.Fatalf("HasRedactionSalt: got false, want true (salt was %d bytes)", len(got.RedactionSalt))
	}

	// Two tenants in the same store must have different salts.
	other := sampleTenant("salt-pg-other")
	if err := store.Create(context.Background(), other); err != nil {
		t.Fatalf("Create other: %v", err)
	}
	gotOther, err := store.Get(context.Background(), other.ID)
	if err != nil {
		t.Fatalf("Get other: %v", err)
	}
	if bytes.Equal(got.RedactionSalt, gotOther.RedactionSalt) {
		t.Fatal("two tenants in the same store share the same redaction salt")
	}
}

// TestPostgresStoreRejectsWrongSizeSalt pins the validation: a
// caller that hands the store a non-32-byte salt gets a clean
// ErrInvalidTenant rather than a corrupt anchor down the line.
func TestPostgresStoreRejectsWrongSizeSalt(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	defer truncateTenants(t, pool)

	store := tenant.NewPostgresStore(pool)
	tt := sampleTenant("salt-pg-wrong-size")
	tt.RedactionSalt = []byte{0x01, 0x02, 0x03} // not RedactionSaltSize
	if err := store.Create(context.Background(), tt); err == nil {
		t.Fatal("Create accepted a wrong-size salt")
	}
}

// TestBackfillRedactionSaltsPopulatesLegacyRows covers the rollout
// hook: rows that exist with redaction_salt = NULL pick up a salt
// the next time BackfillRedactionSalts runs.
//
// We simulate the legacy state by clearing the salt directly via
// SQL after a normal Create (which would otherwise have stamped
// one), then call BackfillRedactionSalts.
func TestBackfillRedactionSaltsPopulatesLegacyRows(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	defer truncateTenants(t, pool)

	store := tenant.NewPostgresStore(pool)
	tt := sampleTenant("salt-pg-backfill")
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Force the row into the legacy state.
	if _, err := pool.Exec(context.Background(),
		"UPDATE cloud_meta.tenants SET redaction_salt = NULL WHERE id = $1", tt.ID); err != nil {
		t.Fatalf("force null salt: %v", err)
	}

	got, err := store.Get(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("pre-backfill Get: %v", err)
	}
	if got.HasRedactionSalt() {
		t.Fatal("test setup failed: salt was not nulled")
	}

	updated, err := tenant.BackfillRedactionSalts(context.Background(), pool)
	if err != nil {
		t.Fatalf("BackfillRedactionSalts: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated: got %d, want 1", updated)
	}

	got, err = store.Get(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("post-backfill Get: %v", err)
	}
	if !got.HasRedactionSalt() {
		t.Fatalf("HasRedactionSalt: got false (len %d), want true", len(got.RedactionSalt))
	}

	// Idempotent: a second run is a no-op.
	updatedAgain, err := tenant.BackfillRedactionSalts(context.Background(), pool)
	if err != nil {
		t.Fatalf("BackfillRedactionSalts (second run): %v", err)
	}
	if updatedAgain != 0 {
		t.Fatalf("idempotency: second run updated %d rows, want 0", updatedAgain)
	}
}

// TestTenantJSONOmitsRedactionSalt is the guardrail against the
// leak path a review caught: kiff-tenant's cmdCreate / cmdList
// marshal Tenant to stderr/stdout, and a salt that JSON-serializes
// shows up in operator shell history, bootstrap-script captures,
// and deploy-side log groups. The salt is supposed to "never leave
// the cloud's database" (per the /security page); this test fails
// loudly the moment any future change re-tags the field.
//
// Both raw json.Marshal and a typical CLI path
// (json.NewEncoder(stderr).SetIndent + Encode) are exercised so a
// regression on either path surfaces.
func TestTenantJSONOmitsRedactionSalt(t *testing.T) {
	t.Parallel()

	tt := tenant.Tenant{
		ID:            "01jjabcdefghjkmnpqrstvwxyz",
		Slug:          "salt-leak-guard",
		DisplayName:   "Salt Leak Guard",
		Status:        tenant.StatusActive,
		RedactionSalt: bytes.Repeat([]byte{0xab}, tenant.RedactionSaltSize),
	}

	// json.Marshal must not leak the salt under any encoding.
	raw, err := json.Marshal(tt)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	body := string(raw)
	for _, banned := range []string{
		"redactionSalt",
		"RedactionSalt",
		"redaction_salt",
		// 0xab byte repeated 32 times encoded as base64 (Go's
		// default for []byte). If the field ever JSON-encodes,
		// the salt content lands here.
		"q6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6s=",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("json.Marshal(Tenant) leaked %q in: %s", banned, body)
		}
	}

	// CLI path: json.Encoder + SetIndent (kiff-tenant's
	// cmdCreate writes via this shape into stderr).
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(tt); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if strings.Contains(buf.String(), "redactionSalt") || strings.Contains(buf.String(), "redaction_salt") {
		t.Errorf("json.Encoder leaked salt in: %s", buf.String())
	}
}
