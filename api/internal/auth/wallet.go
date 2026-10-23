package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Wallet linking.
//
// Flow:
//
//   1. Authenticated user POSTs /v1/me/wallet/nonce with the chain
//      id and address they intend to link. Server issues a
//      single-use nonce + the canonical message text; both are
//      persisted in cloud_meta.wallet_nonces with a TTL.
//   2. User signs the message with their wallet (MetaMask, Rabby,
//      WalletConnect, anything that produces personal_sign / EIP-191).
//   3. User POSTs /v1/me/wallet/verify with the same nonce and the
//      signature. Server rebuilds the canonical message, recovers
//      the signer's address, asserts it matches the address bound
//      to the nonce, marks the nonce used, persists the link.
//   4. Future requests carry the wallet link onto Identity.Wallet
//      so audit records pick up attribution.
//
// Failure modes the test suite covers:
//
//   - missing nonce        -> ErrWalletNonceNotFound
//   - expired nonce        -> ErrWalletNonceExpired
//   - already-used nonce   -> ErrWalletNonceReplayed
//   - wrong signer         -> ErrWalletSignerMismatch
//   - malformed signature  -> ErrWalletBadSignature
//
// The signature-replay protection comes from two layers: the
// canonical message embeds the nonce so a sig cannot be reused on
// any other message, and MarkUsed is compare-and-swap so two
// concurrent verifies race on the database (only one wins).

// Defaults for the nonce subsystem. Callers override via WalletService
// options when needed.
const (
	// WalletNonceLength is the byte length of the random portion of
	// a nonce. 24 bytes -> 32 base32 characters.
	WalletNonceLength = 24

	// WalletNonceTTL is the lifetime of a fresh nonce. The user
	// has this long between issuing the nonce and submitting the
	// signature. 10 minutes is generous enough for a wallet pop-up
	// + signing UX without leaving stale state for hours.
	WalletNonceTTL = 10 * time.Minute
)

// canonicalLinkMessage is the format the server signs and the user's
// wallet signs. Stable across versions; if it ever changes the change
// goes through an RFC and a graceful migration window.
const canonicalLinkMessage = `KIFF Cloud wallet link

Address: %s
Chain: %d
Nonce: %s
Issued: %s
`

// LinkMessage returns the personal_sign-shaped message text for the
// given (address, chainID, nonce, issuedAt). The user's wallet wraps
// this in the standard `\x19Ethereum Signed Message:\n<len>` envelope
// before hashing; go-ethereum's accounts/crypto helpers do the same
// when we recover the signer.
//
// Exposed because the dashboard surface (separate web issue) renders
// the same message text to the user before asking them to sign, so
// they can see what they are signing. Keeping it as a function lets
// tests assert that the server and the client compute identical bytes.
func LinkMessage(address string, chainID uint64, nonce string, issuedAt time.Time) string {
	return fmt.Sprintf(
		canonicalLinkMessage,
		strings.ToLower(strings.TrimSpace(address)),
		chainID,
		nonce,
		issuedAt.UTC().Format(time.RFC3339),
	)
}

// Errors surfaced by the wallet flow. Distinct sentinels so the
// HTTP handler (and the dashboard) can map each to a different
// caller-facing message.
var (
	ErrWalletNonceNotFound  = errors.New("auth: wallet nonce not found")
	ErrWalletNonceExpired   = errors.New("auth: wallet nonce has expired")
	ErrWalletNonceReplayed  = errors.New("auth: wallet nonce has already been used")
	ErrWalletSignerMismatch = errors.New("auth: recovered signer does not match the bound address")
	ErrWalletBadSignature   = errors.New("auth: signature is malformed or invalid")
	ErrWalletAddressInvalid = errors.New("auth: wallet address is not a 0x-prefixed 20-byte hex value")
	ErrWalletChainInvalid   = errors.New("auth: wallet chain_id must be non-zero")
	ErrWalletNotLinked      = errors.New("auth: no wallet linked for the subject")
)

// WalletNonce is the cloud-side record for an outstanding link
// request. Stored in cloud_meta.wallet_nonces; consumed exactly
// once by VerifyLink.
type WalletNonce struct {
	Nonce     string     // the random portion users echo back
	Subject   string     // the Clerk user the nonce was issued for
	Address   string     // lowercase 0x-prefixed hex; the address the user committed to sign with
	ChainID   uint64     // the chain id the address was committed against
	IssuedAt  time.Time  // wall-clock time the server issued the nonce
	ExpiresAt time.Time  // ExpiresAt is IssuedAt + TTL; verify rejects after
	UsedAt    *time.Time // nil until consumed; non-nil indicates replay attempt
}

// IsExpired reports whether the nonce is past its TTL relative to now.
func (n WalletNonce) IsExpired(now time.Time) bool {
	return !n.ExpiresAt.IsZero() && now.After(n.ExpiresAt)
}

// IsUsed reports whether the nonce has already been consumed.
func (n WalletNonce) IsUsed() bool { return n.UsedAt != nil }

// WalletNonceStore is the persistence boundary for wallet nonces.
// Implementations must serialize MarkUsed against concurrent
// callers — only one verify per nonce should succeed.
type WalletNonceStore interface {
	// Insert persists a fresh nonce.
	Insert(ctx context.Context, n WalletNonce) error

	// Get returns the nonce by its random portion. ErrWalletNonceNotFound
	// when no row exists. Returned nonce may be expired or used; the
	// service layer asserts both conditions explicitly.
	Get(ctx context.Context, nonce string) (WalletNonce, error)

	// MarkUsed records the consumption of a nonce at the given time.
	// Returns ErrWalletNonceReplayed when the nonce is already
	// marked used. Implementations achieve atomicity via an UPDATE
	// ... WHERE used_at IS NULL guard or its in-memory analogue.
	MarkUsed(ctx context.Context, nonce string, at time.Time) error
}

// WalletStore is the persistence boundary for the user-wallet link.
// One row per subject in v0.1; multi-wallet is a v0.2 concern.
type WalletStore interface {
	// Upsert creates or replaces the wallet link for the subject.
	// The previous link, if any, is overwritten (linking a new
	// wallet implicitly unlinks the old one).
	Upsert(ctx context.Context, subject string, link WalletLink) error

	// Get returns the wallet link for the subject. ErrWalletNotLinked
	// when no row exists.
	Get(ctx context.Context, subject string) (WalletLink, error)

	// Delete removes the link for the subject. Idempotent: deleting a
	// non-existent link returns nil.
	Delete(ctx context.Context, subject string) error
}

// WalletService composes the two stores and the EIP-191 verification
// logic into the ergonomic surface the HTTP handler uses. Tests
// exercise the service directly without the HTTP layer.
type WalletService struct {
	nonces  WalletNonceStore
	wallets WalletStore
	now     func() time.Time
	ttl     time.Duration
	rand    func(p []byte) (int, error)
}

// WalletServiceOption configures WalletService.
type WalletServiceOption func(*WalletService)

// WithWalletClock substitutes the time source. Tests use this for
// deterministic IssuedAt / ExpiresAt values.
func WithWalletClock(now func() time.Time) WalletServiceOption {
	return func(s *WalletService) { s.now = now }
}

// WithWalletTTL substitutes the nonce TTL.
func WithWalletTTL(ttl time.Duration) WalletServiceOption {
	return func(s *WalletService) { s.ttl = ttl }
}

// WithWalletRand substitutes the random source. Tests use this to
// pin the nonce bytes; production uses crypto/rand.
func WithWalletRand(rd func(p []byte) (int, error)) WalletServiceOption {
	return func(s *WalletService) { s.rand = rd }
}

// NewWalletService builds a service with crypto/rand and the default
// TTL. Both stores are required.
func NewWalletService(nonces WalletNonceStore, wallets WalletStore, opts ...WalletServiceOption) *WalletService {
	s := &WalletService{
		nonces:  nonces,
		wallets: wallets,
		now:     func() time.Time { return time.Now().UTC() },
		ttl:     WalletNonceTTL,
		rand:    rand.Read,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// IssueNonceResult is what IssueNonce returns: the nonce string the
// caller echoes back, the canonical message text the user signs,
// and the issuance + expiry timestamps for the dashboard.
type IssueNonceResult struct {
	Nonce     string
	Message   string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// IssueNonce records a nonce for the (subject, address, chainID)
// triple and returns the canonical message text the user signs.
//
// Address validation: lower-cased, 0x-prefixed hex of length 42.
// ChainID validation: non-zero.
func (s *WalletService) IssueNonce(ctx context.Context, subject, address string, chainID uint64) (IssueNonceResult, error) {
	if subject == "" {
		return IssueNonceResult{}, errors.New("auth: empty subject")
	}
	addr, err := normalizeAddress(address)
	if err != nil {
		return IssueNonceResult{}, err
	}
	if chainID == 0 {
		return IssueNonceResult{}, ErrWalletChainInvalid
	}
	nonce, err := s.generateNonce()
	if err != nil {
		return IssueNonceResult{}, err
	}
	now := s.now().UTC()
	rec := WalletNonce{
		Nonce:     nonce,
		Subject:   subject,
		Address:   addr,
		ChainID:   chainID,
		IssuedAt:  now,
		ExpiresAt: now.Add(s.ttl),
	}
	if err := s.nonces.Insert(ctx, rec); err != nil {
		return IssueNonceResult{}, fmt.Errorf("insert nonce: %w", err)
	}
	return IssueNonceResult{
		Nonce:     nonce,
		Message:   LinkMessage(addr, chainID, nonce, now),
		IssuedAt:  now,
		ExpiresAt: rec.ExpiresAt,
	}, nil
}

// VerifyLink verifies the user's signature over the canonical message
// for the named nonce, asserts the recovered signer matches the
// address the nonce was bound to, marks the nonce used, and persists
// the wallet link for the subject.
//
// Argument shape: subject (from auth context), nonce (echoed back by
// the user), signature (the personal_sign hex string the wallet
// produced — accepts 0x-prefixed or unprefixed). Returns the persisted
// WalletLink on success.
//
// The subject argument is the source of truth for "who is linking";
// the nonce's recorded subject must match it. A nonce issued for
// user A cannot be redeemed by user B.
func (s *WalletService) VerifyLink(ctx context.Context, subject, nonce, signatureHex string) (WalletLink, error) {
	rec, err := s.nonces.Get(ctx, nonce)
	if err != nil {
		return WalletLink{}, err
	}
	if rec.Subject != subject {
		// Defensive: the nonce was issued to a different user. Treat
		// this as a not-found to avoid leaking who has outstanding
		// nonces.
		return WalletLink{}, ErrWalletNonceNotFound
	}
	if rec.IsUsed() {
		return WalletLink{}, ErrWalletNonceReplayed
	}
	if rec.IsExpired(s.now()) {
		return WalletLink{}, ErrWalletNonceExpired
	}

	sig, err := decodeSignature(signatureHex)
	if err != nil {
		return WalletLink{}, err
	}

	message := LinkMessage(rec.Address, rec.ChainID, rec.Nonce, rec.IssuedAt)
	recovered, err := recoverSigner(message, sig)
	if err != nil {
		return WalletLink{}, fmt.Errorf("%w: %w", ErrWalletBadSignature, err)
	}
	if !strings.EqualFold(recovered, rec.Address) {
		return WalletLink{}, ErrWalletSignerMismatch
	}

	now := s.now().UTC()
	if err := s.nonces.MarkUsed(ctx, rec.Nonce, now); err != nil {
		return WalletLink{}, err
	}
	link := WalletLink{
		Address:  rec.Address,
		ChainID:  rec.ChainID,
		LinkedAt: now,
	}
	if err := s.wallets.Upsert(ctx, subject, link); err != nil {
		return WalletLink{}, fmt.Errorf("persist wallet: %w", err)
	}
	return link, nil
}

// Lookup returns the wallet link for the subject, or
// ErrWalletNotLinked when none exists.
func (s *WalletService) Lookup(ctx context.Context, subject string) (WalletLink, error) {
	return s.wallets.Get(ctx, subject)
}

// Unlink removes the wallet link for the subject. Idempotent.
func (s *WalletService) Unlink(ctx context.Context, subject string) error {
	return s.wallets.Delete(ctx, subject)
}

// generateNonce reads s.rand for randomness and base32-encodes it.
// Length is WalletNonceLength bytes -> 32-39 base32 characters
// depending on padding; we pin it at 32 by trimming the encoded
// output of 24 bytes (which is exactly 32 in unpadded base32).
func (s *WalletService) generateNonce() (string, error) {
	buf := make([]byte, WalletNonceLength)
	if _, err := s.rand(buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return strings.ToLower(crockfordEncoder.EncodeToString(buf)), nil
}

// normalizeAddress accepts a 0x-prefixed hex string of length 42 and
// returns the lowercase canonical form. Rejects anything else.
func normalizeAddress(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) != 42 || !strings.EqualFold(raw[:2], "0x") {
		return "", fmt.Errorf("%w: %q", ErrWalletAddressInvalid, raw)
	}
	rest := raw[2:]
	if _, err := hex.DecodeString(rest); err != nil {
		return "", fmt.Errorf("%w: %s", ErrWalletAddressInvalid, err)
	}
	return strings.ToLower(raw), nil
}

// decodeSignature accepts an EIP-191 signature in hex (with or
// without 0x prefix). Length must be 65 bytes (r || s || v).
//
// MetaMask and Rabby produce v in {27, 28}; some libraries normalize
// to {0, 1}. We accept both shapes by re-coercing the v byte to
// {27, 28} before passing to crypto.SigToPub, which is what go-
// ethereum expects. crypto.SigToPub then re-coerces it again to
// {0, 1} internally; the hop is intentional so callers don't have
// to know.
func decodeSignature(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		s = s[2:]
	}
	if len(s) != 130 { // 65 bytes * 2 hex chars
		return nil, fmt.Errorf("%w: signature length must be 65 bytes (got %d hex chars)", ErrWalletBadSignature, len(s))
	}
	sig, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrWalletBadSignature, err)
	}
	return sig, nil
}

// recoverSigner returns the lowercase 0x-prefixed address of the
// signer of `message` for the given EIP-191 signature. The message
// is wrapped in the standard `\x19Ethereum Signed Message:\n<len>`
// envelope before hashing; this matches MetaMask's personal_sign
// behavior.
func recoverSigner(message string, sig []byte) (string, error) {
	if len(sig) != 65 {
		return "", fmt.Errorf("signature length: got %d, want 65", len(sig))
	}
	// Normalize the v byte. EIP-191 signatures from wallets land here
	// with v in {27, 28}; some clients send {0, 1}. Both decode the
	// same key after subtraction, so we coerce to the {0, 1} form
	// crypto.SigToPub expects.
	sigCopy := make([]byte, 65)
	copy(sigCopy, sig)
	if sigCopy[64] >= 27 {
		sigCopy[64] -= 27
	}
	hash := textHash([]byte(message))
	pub, err := crypto.SigToPub(hash, sigCopy)
	if err != nil {
		return "", err
	}
	addr := pubKeyToAddress(pub)
	return strings.ToLower(addr.Hex()), nil
}

// textHash wraps the given message in the EIP-191 personal_sign
// envelope and returns the keccak-256 hash. Stand-alone so tests
// can compute the same hash without the recoverSigner round-trip.
func textHash(data []byte) []byte {
	prefix := []byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d", len(data)))
	return crypto.Keccak256(append(prefix, data...))
}

// pubKeyToAddress derives the Ethereum address for a recovered
// public key. Same as crypto.PubkeyToAddress but typed against the
// common.Address shape.
func pubKeyToAddress(pub *ecdsa.PublicKey) common.Address {
	return crypto.PubkeyToAddress(*pub)
}
