package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// A computer's own passkey (the desktop client contract, §2.6): a Windows
// computer holds no P-256 approval key, since Windows Hello's key store is
// RSA only, so it approves with Windows Hello as a platform passkey, ES256 in
// the TPM, with Windows asking on every assertion. The phone's pairing
// registers it for that one device (§5.1), and removing the device removes
// it. It never counts as a second, independent device (§5.7).

// StoredPasskey is a WebAuthn credential KIFF keeps: the Passkey the
// assertion checks use, the person it's for, and, for a computer's own, the
// device it was registered with.
type StoredPasskey struct {
	Passkey
	Subject string
	// DeviceID is the device a computer's own passkey belongs to: it counts
	// only for that device's session, and goes with it. "" for one of the
	// person's passkeys registered for the account (POST /v1/me/passkeys),
	// which approves for any of their passkey-only sessions.
	DeviceID string
	// KeyID is KIFF's id for it, "pk_" and 26 base32 characters, and Name the
	// person's for it ("" for a computer's own).
	KeyID string
	Name  string
	// Synced is the BS flag its last assertion carried: nil until one has.
	Synced    *bool
	CreatedAt time.Time
	RevokedAt *time.Time
}

// NewPasskeyID makes a passkey's KeyID: "pk_" and 26 base32 characters.
func NewPasskeyID() string {
	return "pk_" + NewPairingID()[len("pair_"):]
}

// IsRevoked reports whether the passkey was removed.
func (p StoredPasskey) IsRevoked() bool { return p.RevokedAt != nil }

var (
	ErrPasskeyNotFound  = errors.New("auth: no such passkey")
	ErrPasskeyDuplicate = errors.New("auth: this credential is already registered")
	// ErrPasskeySynced is a computer's own passkey whose assertion says it
	// can leave the device (BE or BS): it isn't this computer.
	ErrPasskeySynced = errors.New("auth: a computer's own passkey must not be synced")
	// ErrPasskeyDevice is a computer's own passkey used by another device's
	// session, or by a session of a removed device.
	ErrPasskeyDevice = errors.New("auth: this passkey is another device's")
)

// PasskeyStore keeps the passkeys.
type PasskeyStore interface {
	// Insert keeps a passkey. ErrPasskeyDuplicate when its credential id is
	// already registered on the account.
	Insert(ctx context.Context, p StoredPasskey) error
	// Find returns the account's passkey with this credential id, removed or
	// not. ErrPasskeyNotFound when there's none.
	Find(ctx context.Context, tenant string, credentialID []byte) (StoredPasskey, error)
	// SetSignCount stores the counter an assertion carried, only forward,
	// and whether it said the passkey is synced.
	SetSignCount(ctx context.Context, tenant string, credentialID []byte, count uint32, synced bool) error
	// ListForPerson returns a person's passkeys on the account that aren't
	// removed, oldest first.
	ListForPerson(ctx context.Context, tenant, subject string) ([]StoredPasskey, error)
	// ListActiveInTenant returns every person's passkeys on the account that
	// aren't removed: what the proofs' boot check reads.
	ListActiveInTenant(ctx context.Context, tenant string) ([]StoredPasskey, error)
	// Revoke removes one passkey, by its KeyID, only if it is the person's.
	// ErrPasskeyNotFound otherwise.
	Revoke(ctx context.Context, tenant, subject, keyID string, at time.Time) error
	// RevokeForDevice removes the passkeys registered with a device.
	RevokeForDevice(ctx context.Context, deviceID string, at time.Time) error
}

// MemoryPasskeyStore is the in-memory PasskeyStore for tests and dev.
type MemoryPasskeyStore struct {
	mu   sync.Mutex
	keys []StoredPasskey
}

// NewMemoryPasskeyStore returns an empty store.
func NewMemoryPasskeyStore() *MemoryPasskeyStore { return &MemoryPasskeyStore{} }

func (s *MemoryPasskeyStore) Insert(_ context.Context, p StoredPasskey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.keys {
		if o.Tenant == p.Tenant && bytes.Equal(o.ID, p.ID) {
			return ErrPasskeyDuplicate
		}
	}
	s.keys = append(s.keys, p)
	return nil
}

func (s *MemoryPasskeyStore) Find(_ context.Context, tenant string, credentialID []byte) (StoredPasskey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.keys {
		if p.Tenant == tenant && bytes.Equal(p.ID, credentialID) {
			return p, nil
		}
	}
	return StoredPasskey{}, ErrPasskeyNotFound
}

func (s *MemoryPasskeyStore) SetSignCount(_ context.Context, tenant string, credentialID []byte, count uint32, synced bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.keys {
		if p.Tenant == tenant && bytes.Equal(p.ID, credentialID) {
			if count > p.SignCount {
				s.keys[i].SignCount = count
			}
			s.keys[i].Synced = &synced
		}
	}
	return nil
}

func (s *MemoryPasskeyStore) ListForPerson(_ context.Context, tenant, subject string) ([]StoredPasskey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []StoredPasskey
	for _, p := range s.keys {
		if p.Tenant == tenant && p.Subject == subject && p.RevokedAt == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *MemoryPasskeyStore) ListActiveInTenant(_ context.Context, tenant string) ([]StoredPasskey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []StoredPasskey
	for _, p := range s.keys {
		if p.Tenant == tenant && p.RevokedAt == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *MemoryPasskeyStore) Revoke(_ context.Context, tenant, subject, keyID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	at = at.UTC()
	for i, p := range s.keys {
		if keyID != "" && p.KeyID == keyID && p.Tenant == tenant && p.Subject == subject && p.RevokedAt == nil {
			s.keys[i].RevokedAt = &at
			return nil
		}
	}
	return ErrPasskeyNotFound
}

func (s *MemoryPasskeyStore) RevokeForDevice(_ context.Context, deviceID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	at = at.UTC()
	for i, p := range s.keys {
		if deviceID != "" && p.DeviceID == deviceID && p.RevokedAt == nil {
			s.keys[i].RevokedAt = &at
		}
	}
	return nil
}

// PasskeyAssertionHeader carries a passkey's approval of a request, in place
// of DeviceSignatureHeader, on a route that needs the approval key:
//
//	t=<signed_at>,a=<base64url of JSON {"credential_id","client_data_json","authenticator_data","signature"}>
//
// or, approving two requests at once as a v3 does,
// "t=…,a=…,with=sha256:<the other request message's hex>". The WebAuthn
// challenge is base64url(sha256(m)), where m is the message the approval key
// would sign: the request's kiff.request.v1 (or the kiff.requests.v1 over
// both) with the same signed_at.
const PasskeyAssertionHeader = "KIFF-Passkey-Assertion"

// ErrPasskeyHeader is a PasskeyAssertionHeader that doesn't parse.
var ErrPasskeyHeader = errors.New("auth: the passkey assertion header is malformed")

// passkeyHeader is a PasskeyAssertionHeader taken apart.
type passkeyHeader struct {
	At        string
	Assertion Assertion
	With      string
}

func parsePasskeyHeader(header string) (passkeyHeader, error) {
	var h passkeyHeader
	parts := strings.Split(strings.TrimSpace(header), ",")
	if len(parts) < 2 || len(parts) > 3 || !strings.HasPrefix(parts[0], "t=") {
		return h, ErrPasskeyHeader
	}
	h.At = strings.TrimPrefix(parts[0], "t=")
	sawA := false
	for _, part := range parts[1:] {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return h, ErrPasskeyHeader
		}
		switch k {
		case "a":
			if sawA {
				return h, ErrPasskeyHeader
			}
			raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(v, "="))
			if err != nil || len(raw) > 8<<10 || json.Unmarshal(raw, &h.Assertion) != nil {
				return h, ErrPasskeyHeader
			}
			sawA = true
		case "with":
			hexSum, ok := strings.CutPrefix(strings.TrimSpace(v), "sha256:")
			if !ok || !lowerHex64(hexSum) {
				return h, ErrPasskeyHeader
			}
			h.With = hexSum
		default:
			return h, ErrPasskeyHeader
		}
	}
	if !sawA {
		return h, ErrPasskeyHeader
	}
	return h, nil
}

func lowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// passkeyHeaderAssertion is a PasskeyAssertionHeader's a= value, as sent,
// for a receipt's proof.
func passkeyHeaderAssertion(header string) string {
	for _, part := range strings.Split(header, ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), "a="); ok {
			return v
		}
	}
	return ""
}

// PasskeyApproval is a passkey assertion that verified for a session: the
// credential it was, what the checks said, and the replay identity to record
// as used.
type PasskeyApproval struct {
	Key      StoredPasskey
	Approved Approved
}

// VerifyOwnPasskey checks that one of the person's passkeys approved exactly
// message for a session that approves with passkeys (the desktop client
// contract, §2.6), on top of VerifyAssertion's checks:
//
//  1. the credential is the account's, kept, not removed, and the person's;
//  2. a computer's own passkey was registered with this session's device, and
//     only counts for it; one registered for the account (no device) counts
//     for any of the person's passkey-only sessions;
//  3. its assertion verifies for message (UV, rpIdHash, challenge, signature,
//     counter: VerifyAssertion);
//  4. a computer's own passkey is not synced: BE and BS are both 0. An
//     account passkey may be synced, and its record says so (never the
//     second device).
//
// The replay identity (Approved.Used) is for the caller's used set, which
// makes each challenge single-use: the counter isn't relied on, since a
// platform authenticator may keep it at 0. The counter is stored forward.
func VerifyOwnPasskey(ctx context.Context, store PasskeyStore, rp RelyingParty, id Identity, a Assertion, message []byte) (PasskeyApproval, error) {
	if store == nil || id.DeviceID == "" {
		return PasskeyApproval{}, ErrPasskeyDevice
	}
	credential, err := b64url(a.CredentialID)
	if err != nil || len(credential) == 0 {
		return PasskeyApproval{}, ErrAssertionShape
	}
	key, err := store.Find(ctx, id.Tenant, credential)
	if errors.Is(err, ErrPasskeyNotFound) {
		return PasskeyApproval{}, ErrPasskeyAccount
	}
	if err != nil {
		return PasskeyApproval{}, err
	}
	if key.IsRevoked() || key.Subject != id.Subject {
		return PasskeyApproval{}, ErrPasskeyAccount
	}
	own := key.DeviceID != ""
	if own && key.DeviceID != id.DeviceID {
		return PasskeyApproval{}, ErrPasskeyDevice
	}
	ok, _, err := VerifyAssertion(rp, key.Passkey, id.Tenant, a, message)
	if err != nil {
		return PasskeyApproval{}, err
	}
	if own && (ok.Synced || ok.BackupEligible) {
		return PasskeyApproval{}, ErrPasskeySynced
	}
	if err := store.SetSignCount(ctx, id.Tenant, key.ID, ok.SignCount, ok.Synced); err != nil {
		return PasskeyApproval{}, err
	}
	return PasskeyApproval{Key: key, Approved: ok}, nil
}

// VerifyRequestPasskey is VerifyOwnPasskey for a request: the header's
// signed_at within MaxAnswerSkew of now, and the message the approval key
// would have signed for it (kiff.request.v1, or kiff.requests.v1 with the
// other request's hash). It returns the replay identity the request is
// recorded under.
func VerifyRequestPasskey(ctx context.Context, store PasskeyStore, rp RelyingParty, id Identity, method, uri, bodySHA256, header string, now time.Time) (PasskeyApproval, []byte, error) {
	h, err := parsePasskeyHeader(header)
	if err != nil {
		return PasskeyApproval{}, nil, err
	}
	at, err := time.Parse(time.RFC3339, h.At)
	if err != nil {
		return PasskeyApproval{}, nil, ErrAnswerSignedAt
	}
	if d := now.Sub(at); d > MaxAnswerSkew || d < -MaxAnswerSkew {
		return PasskeyApproval{}, nil, ErrAnswerSignedAt
	}
	message := requestMessage(id.Tenant, method, uri, bodySHA256, h.At)
	own := ""
	if h.With != "" {
		sum := sha256.Sum256(message)
		own = hex.EncodeToString(sum[:])
		if own == h.With {
			return PasskeyApproval{}, nil, ErrPasskeyHeader
		}
		message = RequestsMessage(id.Tenant, h.At, own, h.With)
	}
	ok, err := VerifyOwnPasskey(ctx, store, rp, id, h.Assertion, message)
	if err != nil {
		return PasskeyApproval{}, nil, err
	}
	// Approving two requests, this one's use is its own: the other service's
	// use of the same assertion is never this one's (as for a v3).
	used := ok.Approved.Used
	if own != "" {
		sum := sha256.Sum256(append(append(append([]byte(nil), used...), 0), own...))
		used = sum[:]
	}
	return ok, used, nil
}
