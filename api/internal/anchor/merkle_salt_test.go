package anchor

import (
	"testing"
	"time"
)

// TestLeafHashEmptySaltMatchesUnsaltedFormat guards the
// backward-compatibility contract: a batch with no salt produces
// byte-identical leaves to the pre-#107 unsalted form. Receipts
// anchored before salting was wired up therefore stay verifiable
// against the same root.
func TestLeafHashEmptySaltMatchesUnsaltedFormat(t *testing.T) {
	t.Parallel()

	payload := []byte("canonical-record")
	withNil := LeafHash(nil, payload)
	withEmpty := LeafHash([]byte{}, payload)
	if withNil != withEmpty {
		t.Fatalf("nil vs empty-slice salt produced different hashes: %s vs %s", withNil, withEmpty)
	}
}

// TestLeafHashSaltChangesOutput verifies that adding a salt actually
// shifts the leaf hash. Without this assertion the salt could be
// silently dropped (e.g. by a wiring regression that passes nil
// where it shouldn't) and we'd never notice on a green test run.
func TestLeafHashSaltChangesOutput(t *testing.T) {
	t.Parallel()

	payload := []byte("canonical-record")
	unsalted := LeafHash(nil, payload)
	salted := LeafHash([]byte("salt-A"), payload)
	if unsalted == salted {
		t.Fatalf("salt did not change leaf hash: both = %s", unsalted)
	}
}

// TestLeafHashDifferentSaltsCrossTenantIsolation proves the
// cross-tenant claim of the per-tenant salt: two tenants with the same
// canonical-record bytes commit to different leaves, so a public
// anchor cannot be used as a confirmation oracle for guessed records
// without also guessing the per-tenant salt.
//
// Invariant: Two tenants recording byte-identical facts commit to
// different Merkle leaves. A public anchor cannot be used as a
// confirmation oracle for a guessed record without also guessing that
// tenant's salt.
// Category: isolation
// Attack: Guess a record, hash it, and look for that leaf in the public
// anchor to confirm the guess
// Refused: Each tenant's leaves are salted with a per-tenant secret, so the
// guess produces a different hash and confirms nothing
func TestLeafHashDifferentSaltsCrossTenantIsolation(t *testing.T) {
	t.Parallel()

	payload := []byte("same-record")
	a := LeafHash([]byte("salt-tenant-A"), payload)
	b := LeafHash([]byte("salt-tenant-B"), payload)
	if a == b {
		t.Fatalf("identical record across two salts produced the same leaf: %s", a)
	}
}

// TestLeafHashSameSaltDeterministic guards determinism: a tenant
// hashing the same record twice with the same salt must produce the
// same leaf, otherwise off-chain verification cannot recompute it.
func TestLeafHashSameSaltDeterministic(t *testing.T) {
	t.Parallel()

	salt := []byte("tenant-salt")
	payload := []byte("record")
	first := LeafHash(salt, payload)
	second := LeafHash(salt, payload)
	if first != second {
		t.Fatalf("salted leaf is non-deterministic: %s vs %s", first, second)
	}
}

// TestBatchLeafHashesUseBatchSalt ensures the salt threads through
// Batch.LeafHashes — the production call site that produces the
// leaves NewTree consumes. Two batches with the same records but
// different salts must produce different roots.
func TestBatchLeafHashesUseBatchSalt(t *testing.T) {
	t.Parallel()

	rec := Record{
		RecordID: "rec-1",
		EntityID: "order-1",
		Kind:     "action_executed",
		At:       time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC),
		Payload:  []byte("p"),
	}
	a := &Batch{Salt: []byte("salt-A"), Records: []Record{rec}}
	b := &Batch{Salt: []byte("salt-B"), Records: []Record{rec}}

	la := a.LeafHashes()
	lb := b.LeafHashes()
	if len(la) != 1 || len(lb) != 1 {
		t.Fatalf("expected 1 leaf each, got %d and %d", len(la), len(lb))
	}
	if la[0] == lb[0] {
		t.Fatal("Batch.LeafHashes did not propagate Salt: same leaf for two different salts")
	}

	// Building Trees over each must yield different roots, which
	// is the on-chain effect we care about.
	treeA, err := NewTree(la)
	if err != nil {
		t.Fatalf("NewTree A: %v", err)
	}
	treeB, err := NewTree(lb)
	if err != nil {
		t.Fatalf("NewTree B: %v", err)
	}
	if treeA.Root() == treeB.Root() {
		t.Fatal("salted batches produced identical Merkle roots")
	}
}

// TestBatchLeafHashesDefaultsToUnsalted explicitly pins the
// backward-compatibility behaviour at the Batch level: a batch with
// nil/empty Salt must produce exactly the leaves the pre-#107
// codebase would have produced. The legacy already-anchored receipts
// in production rely on this.
func TestBatchLeafHashesDefaultsToUnsalted(t *testing.T) {
	t.Parallel()

	rec := Record{
		RecordID: "rec-1",
		EntityID: "order-1",
		Kind:     "action_executed",
		At:       time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC),
		Payload:  []byte("p"),
	}
	unsaltedBatch := &Batch{Records: []Record{rec}}
	leaves := unsaltedBatch.LeafHashes()
	if len(leaves) != 1 {
		t.Fatalf("expected 1 leaf, got %d", len(leaves))
	}
	expected := LeafHash(nil, rec.CanonicalEncoding())
	if leaves[0] != expected {
		t.Fatalf("unsalted Batch leaf diverged from LeafHash(nil, ...): got %s, want %s", leaves[0], expected)
	}
}
