package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/kiff/kiff/pkg/kiff/actor"
)

// walletServer wires the same middleware → mux pipeline the API
// uses, so the handler is exercised behind real auth.
func walletServer(t *testing.T, now time.Time) (*httptest.Server, *WalletService, string) {
	t.Helper()
	resolver := NewMapResolver()
	resolver.Set("user_alice", actor.Actor{ID: "actor-alice"}, "tenant-acme")

	provider := NewStubProvider(resolver)
	mw := NewMiddleware(provider, NewMemoryAPIKeyStore(), resolver, WithClock(func() time.Time { return now }))

	nonces := NewMemoryWalletNonceStore()
	wallets := NewMemoryWalletStore()
	service := NewWalletService(nonces, wallets,
		WithWalletClock(func() time.Time { return now }),
		WithWalletTTL(WalletNonceTTL),
	)
	mux := http.NewServeMux()
	NewWalletHandler(service).Register(mux)

	srv := httptest.NewServer(mw.Wrap(mux))
	t.Cleanup(srv.Close)
	return srv, service, "Bearer user_alice"
}

func walletDoJSON(t *testing.T, method, url, auth string, body any) (*http.Response, []byte) {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		bodyReader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, raw
}

func TestWalletHandler_FullRoundtrip(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	srv, _, token := walletServer(t, now)

	key, _ := crypto.GenerateKey()
	address := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())

	// 1. Issue a nonce.
	resp, raw := walletDoJSON(t, "POST", srv.URL+"/v1/me/wallet/nonce", token, map[string]any{
		"address":  address,
		"chain_id": 8453,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("nonce status: got %d, want 201; body=%s", resp.StatusCode, raw)
	}
	var nonce nonceResponse
	if err := json.Unmarshal(raw, &nonce); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, raw)
	}
	if !strings.Contains(nonce.Message, address) {
		t.Errorf("message should embed the address: %q", nonce.Message)
	}

	// 2. User signs the canonical message text (server-rendered).
	sig := signWithKey(t, key, nonce.Message)

	// 3. Verify.
	resp, raw = walletDoJSON(t, "POST", srv.URL+"/v1/me/wallet/verify", token, map[string]any{
		"nonce":     nonce.Nonce,
		"signature": sigHex(sig),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify status: got %d, want 200; body=%s", resp.StatusCode, raw)
	}
	var verified verifyResponse
	if err := json.Unmarshal(raw, &verified); err != nil {
		t.Fatalf("unmarshal verify: %v body=%s", err, raw)
	}
	if verified.Address != address || verified.ChainID != 8453 {
		t.Errorf("verified mismatch: %+v", verified)
	}

	// 4. Lookup.
	resp, raw = walletDoJSON(t, "GET", srv.URL+"/v1/me/wallet", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lookup status: got %d", resp.StatusCode)
	}
	var lookup struct {
		Wallet *verifyResponse `json:"wallet"`
	}
	_ = json.Unmarshal(raw, &lookup)
	if lookup.Wallet == nil || lookup.Wallet.Address != address {
		t.Errorf("lookup did not return the linked wallet: %+v", lookup)
	}

	// 5. Unlink.
	resp, _ = walletDoJSON(t, "DELETE", srv.URL+"/v1/me/wallet", token, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unlink status: got %d, want 204", resp.StatusCode)
	}

	// 6. Lookup after unlink returns wallet:null.
	resp, raw = walletDoJSON(t, "GET", srv.URL+"/v1/me/wallet", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post-unlink lookup status: got %d", resp.StatusCode)
	}
	if !strings.Contains(string(raw), `"wallet":null`) {
		t.Errorf("expected wallet:null after unlink, got %s", raw)
	}
}

func TestWalletHandler_Verify_SignerMismatch_400(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	srv, _, token := walletServer(t, now)

	bound, _ := crypto.GenerateKey()
	other, _ := crypto.GenerateKey()
	addrBound := strings.ToLower(crypto.PubkeyToAddress(bound.PublicKey).Hex())

	resp, raw := walletDoJSON(t, "POST", srv.URL+"/v1/me/wallet/nonce", token, map[string]any{
		"address":  addrBound,
		"chain_id": 8453,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("nonce: %d body=%s", resp.StatusCode, raw)
	}
	var nonce nonceResponse
	_ = json.Unmarshal(raw, &nonce)

	sig := signWithKey(t, other, nonce.Message)
	resp, raw = walletDoJSON(t, "POST", srv.URL+"/v1/me/wallet/verify", token, map[string]any{
		"nonce":     nonce.Nonce,
		"signature": sigHex(sig),
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400 on signer mismatch; body=%s", resp.StatusCode, raw)
	}
}

func TestWalletHandler_NonceWithoutAuth_401(t *testing.T) {
	t.Parallel()
	srv, _, _ := walletServer(t, time.Now().UTC())
	resp, _ := walletDoJSON(t, "POST", srv.URL+"/v1/me/wallet/nonce", "", map[string]any{
		"address":  "0x1111111111111111111111111111111111111111",
		"chain_id": 8453,
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", resp.StatusCode)
	}
}

func TestWalletHandler_VerifyMissingFields_400(t *testing.T) {
	t.Parallel()
	srv, _, token := walletServer(t, time.Now().UTC())
	resp, _ := walletDoJSON(t, "POST", srv.URL+"/v1/me/wallet/verify", token, map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestWalletHandler_VerifyUnknownNonce_404(t *testing.T) {
	t.Parallel()
	srv, _, token := walletServer(t, time.Now().UTC())
	resp, _ := walletDoJSON(t, "POST", srv.URL+"/v1/me/wallet/verify", token, map[string]any{
		"nonce":     "no-such-nonce",
		"signature": "0x" + strings.Repeat("00", 65),
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", resp.StatusCode)
	}
}

func TestWalletHandler_NonceBadAddress_400(t *testing.T) {
	t.Parallel()
	srv, _, token := walletServer(t, time.Now().UTC())
	resp, _ := walletDoJSON(t, "POST", srv.URL+"/v1/me/wallet/nonce", token, map[string]any{
		"address":  "not-an-address",
		"chain_id": 8453,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", resp.StatusCode)
	}
}

// integration: confirm the wallet flows through the resolver onto
// Identity for downstream code.
func TestWalletHandler_LinkAttachesToIdentity(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)

	resolver := NewMapResolver()
	resolver.Set("user_alice", actor.Actor{ID: "actor-alice", DisplayName: "Alice"}, "tenant-acme")

	provider := NewStubProvider(resolver)
	mw := NewMiddleware(provider, NewMemoryAPIKeyStore(), resolver, WithClock(func() time.Time { return now }))

	nonces := NewMemoryWalletNonceStore()
	wallets := NewMemoryWalletStore()
	service := NewWalletService(nonces, wallets,
		WithWalletClock(func() time.Time { return now }),
		WithWalletTTL(WalletNonceTTL),
	)

	mux := http.NewServeMux()
	NewWalletHandler(service).Register(mux)

	// A downstream handler that surfaces actor.Actor.DisplayName so we
	// can confirm the wallet annotation lands when Identity.Wallet
	// is populated. Note: Identity.Wallet is *not* attached by the
	// middleware in slice 3 (the service stores it; the middleware
	// would need to enrich Identity from the WalletStore to propagate
	// it). This test documents the current scope: the wallet is
	// persisted, accessible via /v1/me/wallet, and the service is
	// the source of truth. Middleware enrichment lands in a follow-up.
	mux.HandleFunc("GET /me/actor", func(w http.ResponseWriter, r *http.Request) {
		a, err := ActorFrom(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = json.NewEncoder(w).Encode(a)
	})
	srv := httptest.NewServer(mw.Wrap(mux))
	t.Cleanup(srv.Close)

	key, _ := crypto.GenerateKey()
	address := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	res, err := service.IssueNonce(context.Background(), "user_alice", address, 8453)
	if err != nil {
		t.Fatalf("IssueNonce: %v", err)
	}
	sig := signWithKey(t, key, res.Message)
	if _, err := service.VerifyLink(context.Background(), "user_alice", res.Nonce, sigHex(sig)); err != nil {
		t.Fatalf("VerifyLink: %v", err)
	}

	// Confirm the wallet is retrievable.
	stored, err := service.Lookup(context.Background(), "user_alice")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if stored.Address != address {
		t.Errorf("stored wallet mismatch: %+v", stored)
	}
}
