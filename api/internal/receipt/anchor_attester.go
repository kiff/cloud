package receipt

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/kiff/cloud/api/internal/anchor"
)

// receiptStreamID is the canonical stream identifier for the receipt
// anchor stream. Distinct from the audit stream defined by
// anchor.StreamID("audit") so verifiers reading the chain can
// distinguish receipt commitments from raw audit commitments.
//
// The keccak-256 of the literal "kiff.receipts" matches the
// convention anchor.StreamID uses for naming streams.
var receiptStreamID = anchor.StreamID("kiff.receipts")

// ReceiptStreamID returns the stream identifier the anchor attester
// uses on chain. Exposed so verifiers know which stream to query.
func ReceiptStreamID() anchor.Hash { return receiptStreamID }

// SaltResolver returns the per-tenant Merkle leaf salt for the
// given tenant id. The cloud reads this from
// cloud_meta.tenants.redaction_salt; tests pass a fixed map.
//
// An empty/nil salt is interpreted as "this tenant has no salt yet"
// — the AnchorAttester treats that as the unsalted backward-compat
// path so legacy tenants pending salt backfill can still anchor
// receipts. Returning a non-nil error fails the attestation
// outright; the receipt is not anchored.
type SaltResolver interface {
	RedactionSaltFor(ctx context.Context, tenantID string) ([]byte, error)
}

// SaltResolverFunc adapts a plain function into a SaltResolver.
// Lets call sites pass a closure (e.g. a tenant.Store-backed lookup)
// without defining a type.
type SaltResolverFunc func(ctx context.Context, tenantID string) ([]byte, error)

// RedactionSaltFor implements SaltResolver.
func (f SaltResolverFunc) RedactionSaltFor(ctx context.Context, tenantID string) ([]byte, error) {
	return f(ctx, tenantID)
}

// AnchorAttester signs a receipt and submits a one-record batch via
// the anchoring path. The Attestation it produces carries
// both an off-chain signature (recoverable to the wallet's address)
// and the broadcast transaction hash; downstream verification can
// proceed off-chain via VerifyAttestation or on-chain via the
// existing kiff-verify CLI.
//
// v0.1 anchors one receipt per transaction so the receipt's
// AnchorTxHash points unambiguously at "the transaction that
// committed this receipt." A later batching slice can amortize gas
// by anchoring many receipts under one root and persisting the
// (root, leaf-index, proof) triple alongside the receipt; for v0.1
// the simpler shape keeps the attestation contract obvious.
//
// AnchorAttester composes a LocalAttester for signing so the same
// secp256k1 signature shape is produced regardless of whether the
// receipt is anchored. Callers pass the wallet once; the attester
// holds onto it for both the on-chain transaction (via Submitter,
// which already holds the same wallet) and the off-chain signature
// (via the embedded LocalAttester).
type AnchorAttester struct {
	submitter *anchor.Submitter
	signer    *LocalAttester
	salts     SaltResolver
	now       func() time.Time
	batchID   func(r Receipt) uint64
	network   string
}

// NewAnchorAttester wraps a Submitter and the wallet that backs it.
// The wallet must be the same one the Submitter was constructed
// with — the on-chain transaction and the off-chain signature both
// originate from that key.
//
// salts is the per-tenant salt resolver. Pass nil to anchor with
// unsalted leaves (the original form), used by
// existing tests and the in-process anchor demo. Production wires
// a tenant.Store-backed resolver so every batch carries the
// issuing tenant's secret.
//
// now is the clock used for AnchoredAt; pass nil for time.Now.
//
// batchID is the function the attester calls to derive the on-chain
// batch identifier for a receipt. Pass nil to use the default
// (DeterministicBatchID), which is stable per (tenant, trace). A
// custom batchID lets callers source the id from a sidecar table.
func NewAnchorAttester(s *anchor.Submitter, w *anchor.Wallet, salts SaltResolver, now func() time.Time, batchID func(r Receipt) uint64) *AnchorAttester {
	if now == nil {
		now = time.Now
	}
	if batchID == nil {
		batchID = DeterministicBatchID
	}
	return &AnchorAttester{
		submitter: s,
		signer:    NewLocalAttester(w, now),
		salts:     salts,
		now:       now,
		batchID:   batchID,
	}
}

// WithNetwork sets the chain label stamped on every
// Attestation this attester produces — receipt.NetworkBaseSepolia or
// receipt.NetworkBaseMainnet. Fluent so callers can set it inline at
// construction. Empty (the default) leaves Attestation.Network blank.
func (a *AnchorAttester) WithNetwork(network string) *AnchorAttester {
	a.network = network
	return a
}

// Address implements Attester. Returns the wallet address (the
// signer), not the contract address — verifiers compare the
// recovered signer against this value during VerifyAttestation.
func (a *AnchorAttester) Address() common.Address { return a.signer.Address() }

// ContractAddress returns the deployed KiffAnchor address. Exposed
// for ops/dashboard surfaces that want to render a link to the
// contract.
func (a *AnchorAttester) ContractAddress() common.Address { return a.submitter.Address() }

// Attest implements Attester. Anchors a one-record batch on chain
// and stamps the broadcast transaction hash on the Attestation,
// alongside the off-chain signature.
//
// On-chain confirmation (waiting for a receipt, counting blocks) is
// the caller's choice. Production wiring waits for a defined number
// of confirmations before flipping the receipt's anchored_at column;
// the attester returns as soon as the transaction is broadcast.
func (a *AnchorAttester) Attest(ctx context.Context, r Receipt) (*Attestation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Sign first. If signing fails we have not yet broadcast a
	// transaction, so the attempt is cheap to retry.
	att, err := a.signer.Attest(ctx, r)
	if err != nil {
		return nil, err
	}

	// One-record batch keyed on the receipt. The record's payload
	// is the receipt's causal-chain hash bytes (raw 32-byte digest)
	// so the leaf hash matches what an off-chain verifier would
	// re-derive from the receipt alone.
	chainHash, err := r.CausalChainHash()
	if err != nil {
		return nil, fmt.Errorf("hash chain: %w", err)
	}
	chainBytes, err := hex.DecodeString(chainHash)
	if err != nil {
		return nil, fmt.Errorf("decode chain hash: %w", err)
	}

	// Per-tenant salt for the leaf hash. Resolution is
	// best-effort: a nil resolver or a tenant pending salt
	// backfill anchors with the unsalted leaf form so the path
	// stays available during the rollout window. After backfill
	// every active tenant has a salt and the unsalted branch
	// becomes a no-op.
	var salt []byte
	if a.salts != nil {
		s, err := a.salts.RedactionSaltFor(ctx, r.TenantID)
		if err != nil {
			return nil, fmt.Errorf("resolve redaction salt for tenant %s: %w", r.TenantID, err)
		}
		salt = s
	}

	batch := &anchor.Batch{
		BatchID:  a.batchID(r),
		StreamID: receiptStreamID,
		Salt:     salt,
		Records: []anchor.Record{
			{
				RecordID: anchorRecordID(r),
				TraceID:  r.TraceID,
				EntityID: r.EntityID,
				Kind:     "receipt_attested",
				At:       a.now().UTC(),
				Payload:  chainBytes,
			},
		},
	}

	result, err := anchor.Anchor(ctx, a.submitter, batch)
	if err != nil {
		return nil, fmt.Errorf("anchor receipt: %w", err)
	}

	att.AnchorTxHash = "0x" + hex.EncodeToString(result.Tx.Hash().Bytes())
	att.Network = a.network
	return att, nil
}

// anchorRecordID returns the record id used in the on-chain batch
// for a receipt. Stable per (tenant, trace) so re-anchoring the
// same receipt lands under the same record id.
func anchorRecordID(r Receipt) string {
	return r.TenantID + "/" + r.TraceID
}

// DeterministicBatchID derives a uint64 batch id from the receipt's
// (tenant, trace). Stable across runs given the same receipt; the
// kiff-verify CLI uses the same derivation to find the on-chain
// batch.
//
// The id is the first 8 bytes of sha-256 over "tenant\x00trace"
// interpreted as big-endian. Sha-256 is fine here — the batch id is
// not a security primitive, just a stable identifier.
func DeterministicBatchID(r Receipt) uint64 {
	h := sha256.Sum256([]byte(r.TenantID + "\x00" + r.TraceID))
	return binary.BigEndian.Uint64(h[:8])
}

// Compile-time interface assertions.
var _ Attester = (*AnchorAttester)(nil)
var _ Attester = (*LocalAttester)(nil)
