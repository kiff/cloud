package auth

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

// signWithKey produces an EIP-191 personal_sign-shaped signature
// over the given message text. Mirrors what MetaMask / Rabby /
// WalletConnect produce when a user accepts a personal_sign request.
func signWithKey(t *testing.T, key *ecdsa.PrivateKey, message string) []byte {
	t.Helper()
	sig, err := crypto.Sign(textHash([]byte(message)), key)
	if err != nil {
		t.Fatalf("crypto.Sign: %v", err)
	}
	// crypto.Sign produces v in {0, 1}; wallets canonically send
	// {27, 28}. Bump v to match real wallet output so the test
	// exercises the recovery code's normalization branch.
	sig[64] += 27
	return sig
}

func sigHex(sig []byte) string {
	return "0x" + hex.EncodeToString(sig)
}

// newTestService is the deterministic test rig: fixed clock, fixed
// random source for nonces, in-memory stores. Tests inspect the
// stores directly to assert state changes.
func newTestService(t *testing.T, now time.Time) (*WalletService, *MemoryWalletNonceStore, *MemoryWalletStore) {
	t.Helper()
	nonceStore := NewMemoryWalletNonceStore()
	walletStore := NewMemoryWalletStore()
	service := NewWalletService(
		nonceStore, walletStore,
		WithWalletClock(func() time.Time { return now }),
		WithWalletTTL(WalletNonceTTL),
	)
	return service, nonceStore, walletStore
}

func TestLinkMessage_Stable(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	got := LinkMessage("0x1111111111111111111111111111111111111111", 8453, "noncexyz", now)
	want := strings.Join([]string{
		"KIFF Cloud wallet link",
		"",
		"Address: 0x1111111111111111111111111111111111111111",
		"Chain: 8453",
		"Nonce: noncexyz",
		"Issued: 2026-05-23T12:00:00Z",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("LinkMessage mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestNormalizeAddress(t *testing.T) {
	t.Parallel()
	good := []string{
		"0x1234567890abcdef1234567890abcdef12345678",
		"0X1234567890ABCDEF1234567890ABCDEF12345678",
		"  0x1234567890abcdef1234567890abcdef12345678  ",
	}
	for _, raw := range good {
		got, err := normalizeAddress(raw)
		if err != nil {
			t.Errorf("good %q: %v", raw, err)
		}
		if !strings.HasPrefix(got, "0x") || got != strings.ToLower(got) {
			t.Errorf("not normalized: %q", got)
		}
	}
	bad := []string{
		"",
		"1234567890abcdef1234567890abcdef12345678", // missing prefix
		"0xshort", // wrong length
		"0xZ234567890abcdef1234567890abcdef12345678", // not hex
	}
	for _, raw := range bad {
		_, err := normalizeAddress(raw)
		if !errors.Is(err, ErrWalletAddressInvalid) {
			t.Errorf("bad %q: expected ErrWalletAddressInvalid, got %v", raw, err)
		}
	}
}

// ─── IssueNonce ──────────────────────────────────────────────────

func TestIssueNonce_HappyPath(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	svc, nonces, _ := newTestService(t, now)

	res, err := svc.IssueNonce(context.Background(), "user_alice", "0x1111111111111111111111111111111111111111", 8453)
	if err != nil {
		t.Fatalf("IssueNonce: %v", err)
	}
	if res.Nonce == "" {
		t.Fatal("nonce should be non-empty")
	}
	if !strings.Contains(res.Message, res.Nonce) {
		t.Errorf("canonical message should embed the nonce: %q", res.Message)
	}
	if !res.IssuedAt.Equal(now) {
		t.Errorf("IssuedAt: got %v, want %v", res.IssuedAt, now)
	}
	if !res.ExpiresAt.Equal(now.Add(WalletNonceTTL)) {
		t.Errorf("ExpiresAt: got %v, want %v", res.ExpiresAt, now.Add(WalletNonceTTL))
	}
	stored, err := nonces.Get(context.Background(), res.Nonce)
	if err != nil {
		t.Fatalf("nonce should be persisted: %v", err)
	}
	if stored.Subject != "user_alice" {
		t.Errorf("Subject: got %q, want user_alice", stored.Subject)
	}
}

func TestIssueNonce_RejectsBadAddress(t *testing.T) {
	t.Parallel()
	svc, _, _ := newTestService(t, time.Now().UTC())
	_, err := svc.IssueNonce(context.Background(), "user_alice", "not-an-address", 8453)
	if !errors.Is(err, ErrWalletAddressInvalid) {
		t.Fatalf("expected ErrWalletAddressInvalid, got %v", err)
	}
}

func TestIssueNonce_RejectsZeroChain(t *testing.T) {
	t.Parallel()
	svc, _, _ := newTestService(t, time.Now().UTC())
	_, err := svc.IssueNonce(context.Background(), "user_alice", "0x1111111111111111111111111111111111111111", 0)
	if !errors.Is(err, ErrWalletChainInvalid) {
		t.Fatalf("expected ErrWalletChainInvalid, got %v", err)
	}
}

// ─── VerifyLink — happy path + the four failure modes the issue lists ───

func TestVerifyLink_HappyPath(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	svc, _, walletStore := newTestService(t, now)

	key, _ := crypto.GenerateKey()
	address := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())

	res, err := svc.IssueNonce(context.Background(), "user_alice", address, 8453)
	if err != nil {
		t.Fatalf("IssueNonce: %v", err)
	}
	sig := signWithKey(t, key, res.Message)
	link, err := svc.VerifyLink(context.Background(), "user_alice", res.Nonce, sigHex(sig))
	if err != nil {
		t.Fatalf("VerifyLink: %v", err)
	}
	if link.Address != address || link.ChainID != 8453 {
		t.Errorf("link mismatch: got %+v, want address=%q chain=8453", link, address)
	}
	if !link.LinkedAt.Equal(now) {
		t.Errorf("LinkedAt: got %v, want %v", link.LinkedAt, now)
	}
	stored, err := walletStore.Get(context.Background(), "user_alice")
	if err != nil {
		t.Fatalf("walletStore.Get: %v", err)
	}
	if stored.Address != address {
		t.Errorf("persisted wallet mismatch: %+v", stored)
	}
}

func TestVerifyLink_ExpiredNonce(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	nonceStore := NewMemoryWalletNonceStore()
	walletStore := NewMemoryWalletStore()

	clk := now
	svc := NewWalletService(nonceStore, walletStore,
		WithWalletClock(func() time.Time { return clk }),
		WithWalletTTL(WalletNonceTTL),
	)
	key, _ := crypto.GenerateKey()
	address := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())

	res, err := svc.IssueNonce(context.Background(), "user_alice", address, 8453)
	if err != nil {
		t.Fatalf("IssueNonce: %v", err)
	}
	// Advance clock past the TTL.
	clk = now.Add(WalletNonceTTL).Add(time.Second)

	sig := signWithKey(t, key, res.Message)
	_, err = svc.VerifyLink(context.Background(), "user_alice", res.Nonce, sigHex(sig))
	if !errors.Is(err, ErrWalletNonceExpired) {
		t.Fatalf("expected ErrWalletNonceExpired, got %v", err)
	}
}

func TestVerifyLink_WrongSigner(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	svc, _, _ := newTestService(t, now)

	bound, _ := crypto.GenerateKey()
	addrBound := strings.ToLower(crypto.PubkeyToAddress(bound.PublicKey).Hex())

	res, err := svc.IssueNonce(context.Background(), "user_alice", addrBound, 8453)
	if err != nil {
		t.Fatalf("IssueNonce: %v", err)
	}
	// Sign with a different key. The recovered address must not
	// match the address bound to the nonce.
	other, _ := crypto.GenerateKey()
	sig := signWithKey(t, other, res.Message)

	_, err = svc.VerifyLink(context.Background(), "user_alice", res.Nonce, sigHex(sig))
	if !errors.Is(err, ErrWalletSignerMismatch) {
		t.Fatalf("expected ErrWalletSignerMismatch, got %v", err)
	}
}

func TestVerifyLink_Replay_SameNonceTwice(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	svc, _, _ := newTestService(t, now)

	key, _ := crypto.GenerateKey()
	address := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())

	res, err := svc.IssueNonce(context.Background(), "user_alice", address, 8453)
	if err != nil {
		t.Fatalf("IssueNonce: %v", err)
	}
	sig := signWithKey(t, key, res.Message)

	if _, err := svc.VerifyLink(context.Background(), "user_alice", res.Nonce, sigHex(sig)); err != nil {
		t.Fatalf("first VerifyLink: %v", err)
	}
	_, err = svc.VerifyLink(context.Background(), "user_alice", res.Nonce, sigHex(sig))
	if !errors.Is(err, ErrWalletNonceReplayed) {
		t.Fatalf("expected ErrWalletNonceReplayed on second attempt, got %v", err)
	}
}

func TestVerifyLink_NotFound(t *testing.T) {
	t.Parallel()
	svc, _, _ := newTestService(t, time.Now().UTC())
	_, err := svc.VerifyLink(context.Background(), "user_alice", "no-such-nonce", "0x"+strings.Repeat("00", 65))
	if !errors.Is(err, ErrWalletNonceNotFound) {
		t.Fatalf("expected ErrWalletNonceNotFound, got %v", err)
	}
}

func TestVerifyLink_DifferentSubject_LooksLikeNotFound(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	svc, _, _ := newTestService(t, now)
	key, _ := crypto.GenerateKey()
	address := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())

	res, _ := svc.IssueNonce(context.Background(), "user_alice", address, 8453)
	sig := signWithKey(t, key, res.Message)

	// user_bob tries to redeem alice's nonce — should look like NotFound.
	_, err := svc.VerifyLink(context.Background(), "user_bob", res.Nonce, sigHex(sig))
	if !errors.Is(err, ErrWalletNonceNotFound) {
		t.Fatalf("expected cross-subject redemption to look like NotFound, got %v", err)
	}
}

func TestVerifyLink_BadSignatureLength(t *testing.T) {
	t.Parallel()
	svc, nonceStore, _ := newTestService(t, time.Now().UTC())
	rec := WalletNonce{
		Nonce:     "n1",
		Subject:   "user_alice",
		Address:   "0x1111111111111111111111111111111111111111",
		ChainID:   8453,
		IssuedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := nonceStore.Insert(context.Background(), rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := svc.VerifyLink(context.Background(), "user_alice", "n1", "0xdeadbeef")
	if !errors.Is(err, ErrWalletBadSignature) {
		t.Fatalf("expected ErrWalletBadSignature, got %v", err)
	}
}

func TestVerifyLink_AcceptsBothSignatureFormats(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	svc, _, _ := newTestService(t, now)
	key, _ := crypto.GenerateKey()
	address := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())

	res, _ := svc.IssueNonce(context.Background(), "user_alice", address, 8453)
	sig := signWithKey(t, key, res.Message)
	rawHex := hex.EncodeToString(sig) // no 0x prefix

	if _, err := svc.VerifyLink(context.Background(), "user_alice", res.Nonce, rawHex); err != nil {
		t.Fatalf("hex without 0x prefix: %v", err)
	}
}

// ─── Lookup / Unlink ─────────────────────────────────────────────

func TestLookupAndUnlink(t *testing.T) {
	t.Parallel()
	svc, _, _ := newTestService(t, time.Now().UTC())

	if _, err := svc.Lookup(context.Background(), "missing"); !errors.Is(err, ErrWalletNotLinked) {
		t.Fatalf("expected ErrWalletNotLinked, got %v", err)
	}
	// Unlink on missing is a no-op (idempotent).
	if err := svc.Unlink(context.Background(), "missing"); err != nil {
		t.Fatalf("Unlink on missing: %v", err)
	}
}
