// Package anchor implements the on-chain anchoring proof-of-concept.
//
// The PoC scope is intentionally narrow:
//   - one demo wallet, no per-tenant signers
//   - in-process simulated EVM as the default e2e harness
//   - audit-record stream only; usage and token streams arrive with
//     the production form
//
// This file holds the Merkle tree used to commit a batch of records
// to a single 32-byte root suitable for `KiffAnchor.submitRoot`.
package anchor

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/crypto/sha3"
)

// Hash is a 32-byte keccak-256 digest. Matches Solidity's bytes32.
type Hash [32]byte

// String renders the hash as a 0x-prefixed hex string for logs.
func (h Hash) String() string {
	const hexChars = "0123456789abcdef"
	out := make([]byte, 2+len(h)*2)
	out[0], out[1] = '0', 'x'
	for i, b := range h {
		out[2+i*2] = hexChars[b>>4]
		out[2+i*2+1] = hexChars[b&0x0f]
	}
	return string(out)
}

// MarshalJSON encodes the hash as a 0x-prefixed hex string. Matches
// the convention used everywhere else in Ethereum tooling.
func (h Hash) MarshalJSON() ([]byte, error) {
	return json.Marshal(h.String())
}

// UnmarshalJSON accepts either a 0x-prefixed or unprefixed 32-byte
// hex string.
func (h *Hash) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s = s[2:]
	}
	if len(s) != 64 {
		return fmt.Errorf("anchor: hash must be 32 bytes (got %d hex chars)", len(s))
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return fmt.Errorf("anchor: decode hash: %w", err)
	}
	copy(h[:], raw)
	return nil
}

// keccak256 returns the keccak-256 digest of the concatenated inputs.
// Matches Solidity's keccak256(abi.encodePacked(...)).
func keccak256(parts ...[]byte) Hash {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		_, _ = h.Write(p)
	}
	var out Hash
	copy(out[:], h.Sum(nil))
	return out
}

// LeafHash hashes a leaf payload. The PoC uses a single-byte domain
// separator (0x00 for leaves, 0x01 for internal nodes) to match the
// OpenZeppelin convention and avoid second-preimage collisions.
//
// salt is an optional per-tenant secret prepended to the payload
// before hashing. The on-chain leaf becomes
// keccak256(0x00 || salt || payload). An empty/nil salt is
// byte-identical to the pre-#107 leaf hash, so receipts written
// before salting was wired up keep verifying with the same root.
//
// The salt itself is the cloud's per-tenant
// `cloud_meta.tenants.redaction_salt`. Off-chain verification
// (anchor.VerifyRecord, kiff-verify) needs the salt to recompute
// the leaf — see anchor.Batch.Salt for the on-the-wire shape.
func LeafHash(salt, payload []byte) Hash {
	if len(salt) == 0 {
		return keccak256([]byte{0x00}, payload)
	}
	return keccak256([]byte{0x00}, salt, payload)
}

// nodeHash combines two child hashes into their parent. Children are
// sorted lexicographically before hashing so verification does not
// need a "left/right" path bit alongside each sibling.
func nodeHash(a, b Hash) Hash {
	if bytes.Compare(a[:], b[:]) > 0 {
		a, b = b, a
	}
	return keccak256([]byte{0x01}, a[:], b[:])
}

// Tree is a sorted-pair Merkle tree over a fixed batch of leaves.
// Leaves keep their submission order so the proof returned by
// ProofForIndex points at the expected leaf; ordering inside
// nodeHash is only for the parent-hash computation.
type Tree struct {
	leaves []Hash
	levels [][]Hash // levels[0] == leaves; levels[len-1] == [root]
}

// NewTree builds a tree from the given leaf hashes. The input is
// copied; the caller may safely reuse its slice. An empty input is
// rejected — anchoring an empty batch is meaningless and would push a
// known constant on-chain.
func NewTree(leaves []Hash) (*Tree, error) {
	if len(leaves) == 0 {
		return nil, errors.New("anchor: tree requires at least one leaf")
	}

	cp := make([]Hash, len(leaves))
	copy(cp, leaves)

	levels := [][]Hash{cp}
	current := cp
	for len(current) > 1 {
		next := make([]Hash, 0, (len(current)+1)/2)
		for i := 0; i < len(current); i += 2 {
			if i+1 == len(current) {
				// Odd node: promote unchanged. Avoids the "duplicate
				// last leaf" trick which biases proofs.
				next = append(next, current[i])
				continue
			}
			next = append(next, nodeHash(current[i], current[i+1]))
		}
		levels = append(levels, next)
		current = next
	}

	return &Tree{leaves: cp, levels: levels}, nil
}

// Root returns the Merkle root suitable for KiffAnchor.submitRoot.
func (t *Tree) Root() Hash {
	last := t.levels[len(t.levels)-1]
	return last[0]
}

// LeafCount returns the number of leaves in the tree.
func (t *Tree) LeafCount() int {
	return len(t.leaves)
}

// Proof is the list of sibling hashes from a leaf to the root, in
// bottom-up order. Combined with the leaf itself and the order rule
// (smaller child first when hashing parents), it lets a verifier
// reconstruct the root without needing the rest of the tree.
type Proof []Hash

// ProofForIndex returns the inclusion proof for the leaf at i.
func (t *Tree) ProofForIndex(i int) (Proof, error) {
	if i < 0 || i >= len(t.leaves) {
		return nil, fmt.Errorf("anchor: leaf index %d out of range [0,%d)", i, len(t.leaves))
	}

	var out Proof
	idx := i
	for level := 0; level < len(t.levels)-1; level++ {
		nodes := t.levels[level]
		// Sibling at the same level. Promoted (odd) nodes have no
		// sibling at this step; the parent equals the node itself, so
		// we simply rise to the next level.
		var sibling int
		if idx%2 == 0 {
			sibling = idx + 1
		} else {
			sibling = idx - 1
		}
		if sibling < len(nodes) {
			out = append(out, nodes[sibling])
		}
		idx /= 2
	}
	return out, nil
}

// VerifyProof recomputes the root from a leaf and its proof and
// returns true if the result matches the expected root. The
// algorithm is the verification side of nodeHash.
func VerifyProof(leaf Hash, proof Proof, root Hash) bool {
	current := leaf
	for _, sibling := range proof {
		current = nodeHash(current, sibling)
	}
	return current == root
}
