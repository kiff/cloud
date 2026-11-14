package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

// A pairing is a computer a person's phone approved with its approval key
// (the desktop client contract, §5): the computer showed its keys in a QR, the
// phone sent them to KIFF, and the computer claims them once, proving it
// holds the device key, for a session bound to both keys. KIFF keeps the
// nonce's hash, never the nonce, and no token: the session is minted at
// the claim.
type Pairing struct {
	// ID is "pair_" and 26 base32 characters.
	ID      string
	Tenant  string
	Subject string
	// ApprovedBy is the device whose approval key approved it.
	ApprovedBy string
	// PublicKey and ApprovalKey are the computer's device and approval keys
	// (SPKI DER), as the phone approved them.
	PublicKey   []byte
	ApprovalKey []byte
	Name        string
	Platform    string
	// Kind is the QR's: hello or enclave.
	Kind      string
	NonceHash []byte
	Expires   time.Time
	CreatedAt time.Time
	ClaimedAt *time.Time
}

// PairingMessage is the QR's v.
const PairingMessage = "kiff.pair.v1"

// MaxPairingAhead is how far after the phone's signed_at a pairing may
// expire.
const MaxPairingAhead = 5 * time.Minute

var (
	ErrPairingNotFound = errors.New("auth: no such pairing")
	ErrPairingUsed     = errors.New("auth: this pairing's nonce was already approved")
	ErrPairingClaimed  = errors.New("auth: this pairing was already claimed")
	ErrPairingProof    = errors.New("auth: the claim's proof does not verify")
)

// PairingStore keeps the pairings.
type PairingStore interface {
	// Insert stores an approved pairing. ErrPairingUsed when its nonce
	// hash is already kept.
	Insert(ctx context.Context, p Pairing) error
	// ByNonce returns the pairing with this nonce hash, claimed or not.
	// ErrPairingNotFound when there's none.
	ByNonce(ctx context.Context, nonceHash []byte) (Pairing, error)
	// Claim marks it claimed, only if it isn't yet. ErrPairingClaimed
	// otherwise.
	Claim(ctx context.Context, id string, at time.Time) error
	// Unclaim gives a claim back when its device or session couldn't be
	// kept, so the computer can claim it again.
	Unclaim(ctx context.Context, id string) error
	// DropUnclaimedBy deletes the pairings a device approved that no
	// computer has claimed yet: removing the device takes them back.
	DropUnclaimedBy(ctx context.Context, deviceID string) error
}

// NewPairingID makes a pairing id: "pair_" and 26 base32 characters.
func NewPairingID() string {
	b := make([]byte, 17)
	if _, err := rand.Read(b); err != nil {
		panic("auth: no randomness for a pairing id: " + err.Error())
	}
	return "pair_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))[:26]
}

// pairingB64 is the QR's encoding: base64url without padding, strict, so
// each value has one spelling.
var pairingB64 = base64.RawURLEncoding.Strict()

// ParsePairingKey decodes a QR key (base64url SPKI DER) and accepts only
// P-256. It returns the DER bytes.
func ParsePairingKey(s string) ([]byte, error) {
	der, err := pairingB64.DecodeString(s)
	if err != nil || len(der) == 0 || len(der) > 512 {
		return nil, ErrDeviceKey
	}
	if _, err := deviceKey(der); err != nil {
		return nil, err
	}
	return der, nil
}

// PairingNonceHash decodes a QR nonce, 32 bytes in base64url, and returns
// its SHA-256, which is all KIFF keeps of it.
func PairingNonceHash(nonce string) ([]byte, bool) {
	b, err := pairingB64.DecodeString(nonce)
	if err != nil || len(b) != 32 {
		return nil, false
	}
	sum := sha256.Sum256(b)
	return sum[:], true
}

// PairingCode is the 6 characters both screens show:
// base32(sha256(pub ‖ approval_pub)), RFC 4648 alphabet, its first 6.
func PairingCode(pub, approvalPub []byte) string {
	sum := sha256.Sum256(append(append([]byte(nil), pub...), approvalPub...))
	return base32.StdEncoding.EncodeToString(sum[:])[:6]
}

// PairClaimMessage is what a computer's device key signs to claim a
// pairing, one field per line:
//
//	kiff.pair.claim.v1
//	<nonce, as in the QR>
//	<sha256(pub SPKI DER) hex>
func PairClaimMessage(nonce string, pub []byte) []byte {
	sum := sha256.Sum256(pub)
	return []byte(strings.Join([]string{"kiff.pair.claim.v1", nonce, hex.EncodeToString(sum[:])}, "\n"))
}

// VerifyPairClaim checks a claim's proof, a base64 DER ECDSA signature
// over PairClaimMessage, with pub.
func VerifyPairClaim(pub []byte, nonce, proofB64 string) error {
	k, err := deviceKey(pub)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(proofB64))
	if err != nil || len(sig) == 0 {
		return ErrPairingProof
	}
	digest := sha256.Sum256(PairClaimMessage(nonce, pub))
	if !ecdsa.VerifyASN1(k, digest[:], sig) {
		return ErrPairingProof
	}
	return nil
}

// MemoryPairingStore is the in-memory PairingStore for tests and dev.
type MemoryPairingStore struct {
	mu   sync.Mutex
	byID map[string]Pairing
}

// NewMemoryPairingStore returns an empty store.
func NewMemoryPairingStore() *MemoryPairingStore {
	return &MemoryPairingStore{byID: map[string]Pairing{}}
}

func (s *MemoryPairingStore) Insert(_ context.Context, p Pairing) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.byID {
		if string(o.NonceHash) == string(p.NonceHash) {
			return ErrPairingUsed
		}
	}
	s.byID[p.ID] = p
	return nil
}

func (s *MemoryPairingStore) ByNonce(_ context.Context, nonceHash []byte) (Pairing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.byID {
		if string(p.NonceHash) == string(nonceHash) {
			return p, nil
		}
	}
	return Pairing{}, ErrPairingNotFound
}

func (s *MemoryPairingStore) Claim(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	if !ok {
		return ErrPairingNotFound
	}
	if p.ClaimedAt != nil {
		return ErrPairingClaimed
	}
	at = at.UTC()
	p.ClaimedAt = &at
	s.byID[id] = p
	return nil
}

func (s *MemoryPairingStore) DropUnclaimedBy(_ context.Context, deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.byID {
		if p.ApprovedBy == deviceID && p.ClaimedAt == nil {
			delete(s.byID, id)
		}
	}
	return nil
}

func (s *MemoryPairingStore) Unclaim(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.byID[id]; ok {
		p.ClaimedAt = nil
		s.byID[id] = p
	}
	return nil
}
