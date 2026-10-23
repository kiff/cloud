package anchor

import (
	"context"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/core/types"
)

// AnchorResult is what the batcher returns after a successful submit.
// It is everything a verifier needs to recreate the proof later.
type AnchorResult struct {
	Batch *Batch
	Root  Hash
	Tx    *types.Transaction
}

// Anchor builds a Merkle tree from the batch and submits the root
// via the Submitter. Validates the batch first; returns the tree-root
// and the broadcast transaction. Does not wait for inclusion — that
// is the caller's choice (the simulated backend wants Commit() before
// the receipt is observable; production wants TransactionReceipt with
// confirmations).
func Anchor(ctx context.Context, sub *Submitter, batch *Batch) (AnchorResult, error) {
	if sub == nil {
		return AnchorResult{}, errors.New("anchor: nil submitter")
	}
	if batch == nil {
		return AnchorResult{}, errors.New("anchor: nil batch")
	}
	if err := batch.Validate(); err != nil {
		return AnchorResult{}, err
	}
	tree, err := NewTree(batch.LeafHashes())
	if err != nil {
		return AnchorResult{}, fmt.Errorf("anchor: tree: %w", err)
	}
	root := tree.Root()
	tx, err := sub.SubmitRoot(ctx, root, batch.BatchID, batch.StreamID)
	if err != nil {
		return AnchorResult{}, err
	}
	return AnchorResult{Batch: batch, Root: root, Tx: tx}, nil
}

// VerifyResult describes the outcome of a verification attempt.
type VerifyResult struct {
	OK        bool
	Reason    string
	Anchored  *AnchoredEvent
	LeafIndex int
	Root      Hash
}

// VerifyRecord checks that a record is committed to an on-chain
// anchor. The caller supplies the batch the record was anchored in
// (off-chain context the cloud holds); the verifier rebuilds the
// Merkle proof, fetches the on-chain root for the (stream, batch),
// and returns OK iff the proof verifies against that root.
//
// In production this collapses into a single call from a record id —
// the cloud looks up the batch from a sidecar table. The PoC asks
// callers to pass the batch directly.
func VerifyRecord(ctx context.Context, sub *Submitter, batch *Batch, recordID string, fromBlock uint64) (VerifyResult, error) {
	if err := batch.Validate(); err != nil {
		return VerifyResult{}, err
	}
	idx := batch.IndexOf(recordID)
	if idx < 0 {
		return VerifyResult{OK: false, Reason: "record id not in batch"}, nil
	}

	leaves := batch.LeafHashes()
	tree, err := NewTree(leaves)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("verify: tree: %w", err)
	}
	proof, err := tree.ProofForIndex(idx)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("verify: proof: %w", err)
	}

	events, err := sub.FilterAnchored(ctx, fromBlock, batch.StreamID, batch.BatchID)
	if err != nil {
		return VerifyResult{}, err
	}
	if len(events) == 0 {
		return VerifyResult{OK: false, Reason: "no Anchored event for stream/batch"}, nil
	}
	// First match wins. The contract has no replay protection, so a
	// determined caller could submit the same (stream, batch) twice;
	// we accept the first matching root and let downstream policy
	// decide whether duplicates are an alarm.
	ev := events[0]
	if !VerifyProof(leaves[idx], proof, ev.Root) {
		return VerifyResult{
			OK:       false,
			Reason:   "merkle proof does not match on-chain root",
			Anchored: &ev,
			Root:     ev.Root,
		}, nil
	}
	return VerifyResult{
		OK:        true,
		Anchored:  &ev,
		LeafIndex: idx,
		Root:      ev.Root,
	}, nil
}
