package receipt

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kiff/cloud/api/internal/anchor"
)

// AttestationAlgorithm is the v0.1 signature suite. The cloud signs
// over the receipt's causal-chain hash with secp256k1 + keccak-256,
// matching the algorithm used by the anchoring path.
//
// If we ever change the suite, every previously attested receipt
// must remain verifiable under its declared algorithm; the field
// exists on Attestation precisely so consumers can branch on it.
const AttestationAlgorithm = "secp256k1+keccak256"

// Errors surfaced by the attester path. Distinct from the build-side
// errors so callers can branch on intent.
var (
	ErrAttesterMissing       = errors.New("receipt: attester is nil")
	ErrAttestationMissing    = errors.New("receipt: attestation missing")
	ErrAttestationAlgorithm  = errors.New("receipt: attestation algorithm not supported")
	ErrAttestationSignature  = errors.New("receipt: attestation signature invalid")
	ErrAttestationWalletAddr = errors.New("receipt: attestation wallet address does not match recovered signer")
)

// Attester produces an Attestation for a receipt. Implementations:
//
//   - LocalAttester: signs with an in-memory wallet, does not anchor.
//     Suitable for tests and for the offline demo. AnchorTxHash on
//     the produced Attestation is empty.
//
//   - AnchorAttester: signs and submits a one-record batch via the
//     anchoring path. Suitable for any environment that has
//     a configured chain backend (the simulated backend in tests, or
//     Base Sepolia in dev / production).
//
// The interface is small on purpose. The attester does not touch
// the receipt's body, the persistence layer, or the HTTP surface;
// callers compose those.
type Attester interface {
	// Attest produces an Attestation for the receipt's causal
	// chain. Implementations must populate Algorithm, Signature
	// (hex, no 0x prefix), WalletAddress (0x-lowercase), and
	// AnchoredAt. AnchorTxHash may be empty when the implementation
	// does not anchor on chain (LocalAttester); AnchorAttester
	// populates it with the broadcast transaction's hash.
	Attest(ctx context.Context, r Receipt) (*Attestation, error)

	// Address returns the wallet address the attester signs under.
	// Exposed so callers can record the address in tenant
	// bookkeeping at provisioning time.
	Address() common.Address
}

// hashToSign returns the 32-byte digest the attester signs over.
// It is the keccak-256 of the canonical bytes of the receipt's
// causal-chain hash (which itself is sha-256 hex). Two layers:
//
//   - The receipt's CausalChainHash() (sha-256 over canonical JSON
//     of the chain) is the cloud's tamper-evidence layer.
//   - The keccak-256 over those bytes is the EVM-shaped signing
//     digest the cloud anchors against. This matches the algorithm
//     the on-chain contract uses when verification ever moves
//     fully on-chain.
//
// The returned digest is what crypto.Sign / crypto.SigToPub take.
func hashToSign(r Receipt) ([]byte, error) {
	chainHash, err := r.CausalChainHash()
	if err != nil {
		return nil, fmt.Errorf("hash chain: %w", err)
	}
	digest := crypto.Keccak256([]byte(chainHash))
	return digest, nil
}

// LocalAttester signs with a wallet held in memory and does not
// anchor on chain. Use for tests and the offline demo.
//
// Wallet ownership: the attester does not take custody of the
// wallet. Callers retain the wallet for as long as the attester
// lives.
type LocalAttester struct {
	wallet *anchor.Wallet
	now    func() time.Time
}

// NewLocalAttester wraps a wallet. now is the clock used for
// AnchoredAt; pass nil for time.Now (the production path) or a
// fixed clock for deterministic tests.
func NewLocalAttester(w *anchor.Wallet, now func() time.Time) *LocalAttester {
	if now == nil {
		now = time.Now
	}
	return &LocalAttester{wallet: w, now: now}
}

// Address implements Attester.
func (a *LocalAttester) Address() common.Address { return a.wallet.Address() }

// Attest implements Attester. The signed digest is the keccak-256 of
// the receipt's causal-chain hash; the resulting 65-byte signature
// is hex-encoded (no 0x prefix) and stored in
// Attestation.Signature.
func (a *LocalAttester) Attest(ctx context.Context, r Receipt) (*Attestation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	digest, err := hashToSign(r)
	if err != nil {
		return nil, err
	}
	sig, err := crypto.Sign(digest, a.wallet.PrivateKey())
	if err != nil {
		return nil, fmt.Errorf("sign receipt: %w", err)
	}
	return &Attestation{
		Algorithm:     AttestationAlgorithm,
		Signature:     hex.EncodeToString(sig),
		WalletAddress: strings.ToLower(a.wallet.Address().Hex()),
		AnchorTxHash:  "",
		AnchoredAt:    a.now().UTC(),
	}, nil
}

// VerifyAttestation recovers the signer from att.Signature against
// the receipt's causal-chain hash and confirms it matches
// att.WalletAddress. Returns nil on success, ErrAttestation*
// otherwise.
//
// Note: this is the off-chain verification path. On-chain
// verification (Merkle inclusion against the public root anchored
// to Base) lands with the production form.
func VerifyAttestation(r Receipt, att *Attestation) error {
	if att == nil {
		return ErrAttestationMissing
	}
	if att.Algorithm != AttestationAlgorithm {
		return fmt.Errorf("%w: got %q, want %q", ErrAttestationAlgorithm, att.Algorithm, AttestationAlgorithm)
	}
	digest, err := hashToSign(r)
	if err != nil {
		return err
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(att.Signature, "0x"))
	if err != nil {
		return fmt.Errorf("%w: decode hex: %v", ErrAttestationSignature, err)
	}
	if len(sig) != 65 {
		return fmt.Errorf("%w: signature must be 65 bytes (got %d)", ErrAttestationSignature, len(sig))
	}
	pub, err := crypto.SigToPub(digest, sig)
	if err != nil {
		return fmt.Errorf("%w: recover pubkey: %v", ErrAttestationSignature, err)
	}
	recovered := strings.ToLower(crypto.PubkeyToAddress(*pub).Hex())
	declared := strings.ToLower(strings.TrimSpace(att.WalletAddress))
	if recovered != declared {
		return fmt.Errorf("%w: recovered %q, declared %q", ErrAttestationWalletAddr, recovered, declared)
	}
	return nil
}
