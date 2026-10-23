package receipt_test

import (
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/receipt"
)

// The causal chain's canonical encoding is a wire format, not an
// implementation detail.
//
// TestBuild_CausalChainHashIsDeterministic proves two receipts built in
// the same binary hash alike. It cannot catch the failure that matters
// here: canonicalJSON is json.Marshal, and Go encodes struct fields in
// declaration order, so reordering ChainStep's fields — or editing a
// json tag, or changing a type's marshalling — changes the hash of every
// receipt issued afterwards while leaving every receipt issued before
// still verifiable against its own signature.
//
// Nothing fails. Old receipts verify, new receipts verify, and the two
// sets disagree about what identical facts hash to. During an
// investigation that is indistinguishable from tampering, which is the
// exact accusation the receipt exists to answer.
//
// So the encoding is pinned to a literal. If this test fails, the wire
// format changed: either revert it, or treat it as a schema version bump
// with a documented migration for receipts already signed.
//
// Invariant: The bytes a receipt's signature commits to are pinned to a
// fixed encoding. Reordering a field would otherwise change every
// future hash while leaving past receipts verifiable, which is
// indistinguishable from tampering.
// Category: evidence
// Attack: Reorder a field in the chain struct, changing what every future
// receipt hashes to
// Refused: The encoding is pinned to fixed bytes, so the build fails before
// a single receipt is signed against the new shape
func TestCausalChainCanonicalEncodingIsPinned(t *testing.T) {
	t.Parallel()

	// Fixed instants in UTC. Any monotonic-clock reading or non-UTC
	// zone would make the encoding host-dependent, which is itself a
	// property worth freezing.
	at := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatalf("parse fixture time %q: %v", s, err)
		}
		return ts.UTC()
	}

	chain := []receipt.ChainStep{
		{
			Sequence:   1,
			Kind:       "proposal_received",
			ActorID:    "agent-1",
			OccurredAt: at("2026-01-02T03:04:05.000000006Z"),
			Message:    "proposal received",
			DataHash:   "1f0a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8",
		},
		{
			// ActorID and Message omitted on purpose: both are
			// `omitempty`, so this step also pins the fact that an
			// absent optional field is absent from the bytes rather
			// than present as an empty string.
			Sequence:   2,
			Kind:       "action_executed",
			OccurredAt: at("2026-01-02T03:04:06Z"),
			DataHash:   "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
		},
	}

	const wantJSON = `[{"sequence":1,"kind":"proposal_received","actor_id":"agent-1","occurred_at":"2026-01-02T03:04:05.000000006Z","message":"proposal received","data_hash":"1f0a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8"},{"sequence":2,"kind":"action_executed","occurred_at":"2026-01-02T03:04:06Z","data_hash":"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"}]`

	// keccak-256 of this string is the digest LocalAttester signs, so
	// this constant is transitively the signing input.
	//
	// The value is not magic and does not require running Go to check.
	// It is sha-256 over the wantJSON literal above, re-derivable with:
	//
	//	printf '%s' '<wantJSON>' | shasum -a 256
	const wantHash = "66ef72a2a55d4e60cf8328a0a8993fc07d5b9d55dc8efc82f61bf3a94058d99b"

	if got := receipt.CanonicalChainJSONForTest(chain); got != wantJSON {
		t.Errorf("canonical chain JSON changed.\n got: %s\nwant: %s\n\n"+
			"The bytes above are what every signature commits to. A field "+
			"reordered, a json tag edited, or an omitempty added or removed "+
			"all land here.", got, wantJSON)
	}

	r := receipt.Receipt{CausalChain: chain}
	got, err := r.CausalChainHash()
	if err != nil {
		t.Fatalf("CausalChainHash: %v", err)
	}
	if got != wantHash {
		t.Errorf("causal-chain hash changed: got %s, want %s\n\n"+
			"Receipts signed before this change and after it now hash the "+
			"same facts differently. Revert, or bump the receipt schema "+
			"version and document the migration.", got, wantHash)
	}
}
