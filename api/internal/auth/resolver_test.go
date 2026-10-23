package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/kiff/pkg/kiff/actor"
)

const (
	testWalletAddress = "0x" + "1234567890abcdef1234567890abcdef12345678"
	testChainBase     = uint64(8453)
)

// ─── Identity ────────────────────────────────────────────────────

func TestIdentity_Validate_HappyPath(t *testing.T) {
	t.Parallel()

	cases := []Identity{
		{Subject: "user_123", Method: MethodSession},
		{Subject: "key_abc", Method: MethodAPIKey, APIKey: true},
		{Subject: "user_123", Method: MethodSession, Wallet: &WalletLink{
			Address: testWalletAddress, ChainID: testChainBase, LinkedAt: time.Now().UTC(),
		}},
	}
	for _, id := range cases {
		if err := id.Validate(); err != nil {
			t.Errorf("unexpected validate error for %+v: %v", id, err)
		}
	}
}

func TestIdentity_Validate_RejectsBadShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		id   Identity
		want string // substring of expected error
	}{
		{"empty subject", Identity{Method: MethodSession}, "empty subject"},
		{"empty method", Identity{Subject: "x"}, "empty method"},
		{"unknown method", Identity{Subject: "x", Method: AuthMethod("oauth")}, "unknown method"},
		{"api key flag missing", Identity{Subject: "x", Method: MethodAPIKey}, "APIKey=true"},
		{"session with api key flag", Identity{Subject: "x", Method: MethodSession, APIKey: true}, "must not set APIKey"},
		{"bad wallet propagates", Identity{
			Subject: "x", Method: MethodSession,
			Wallet: &WalletLink{Address: "not-hex", ChainID: 1, LinkedAt: time.Now().UTC()},
		}, "0x-prefixed hex"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := c.id.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), c.want)
			}
		})
	}
}

func TestIdentity_HasRole(t *testing.T) {
	t.Parallel()
	id := Identity{Roles: []string{"admin", "operator"}}
	if !id.HasRole("admin") {
		t.Errorf("expected HasRole(admin) = true")
	}
	if id.HasRole("Admin") {
		t.Errorf("HasRole is case-sensitive; 'Admin' should not match 'admin'")
	}
	if id.HasRole("missing") {
		t.Errorf("HasRole(missing) should be false")
	}
}

// ─── WalletLink ───────────────────────────────────────────────────

func TestWalletLink_Validate(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	good := WalletLink{Address: testWalletAddress, ChainID: testChainBase, LinkedAt: now}
	if err := good.Validate(); err != nil {
		t.Fatalf("happy-path WalletLink should validate: %v", err)
	}

	cases := []struct {
		name string
		w    WalletLink
		want string
	}{
		{"empty address", WalletLink{ChainID: 1, LinkedAt: now}, "empty address"},
		{"missing 0x", WalletLink{Address: "1234567890abcdef1234567890abcdef12345678abcd", ChainID: 1, LinkedAt: now}, "0x-prefixed"},
		{"wrong length", WalletLink{Address: "0xdead", ChainID: 1, LinkedAt: now}, "0x-prefixed"},
		{"upper-case", WalletLink{Address: strings.ToUpper(testWalletAddress), ChainID: 1, LinkedAt: now}, "lowercase"},
		{"zero chain", WalletLink{Address: testWalletAddress, LinkedAt: now}, "chain_id"},
		{"zero linked_at", WalletLink{Address: testWalletAddress, ChainID: 1}, "linked_at"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := c.w.Validate()
			if err == nil {
				t.Fatalf("expected validation error containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), c.want)
			}
		})
	}
}

// ─── MapResolver ──────────────────────────────────────────────────

func TestMapResolver_ResolvesRegisteredSubject(t *testing.T) {
	t.Parallel()

	r := NewMapResolver()
	a := actor.Actor{ID: "u-123", Type: actor.TypeHuman, DisplayName: "Alice", Roles: []string{"admin"}}
	if _, existed := r.Set("user_123", a, "tenant-acme"); existed {
		t.Fatalf("first Set should not report a previous pair")
	}

	got, tenantID, err := r.Resolve(context.Background(), Identity{
		Subject: "user_123", Method: MethodSession, Email: "alice@acme.test",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if tenantID != "tenant-acme" {
		t.Errorf("tenant: got %q, want tenant-acme", tenantID)
	}
	if got.ID != "u-123" || got.DisplayName != "Alice" {
		t.Errorf("actor mismatch: got %+v", got)
	}
	// Mutating the returned actor must not affect future Resolves.
	got.Roles[0] = "tampered"
	got, _, err = r.Resolve(context.Background(), Identity{Subject: "user_123", Method: MethodSession})
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if got.Roles[0] != "admin" {
		t.Errorf("Resolve must return a copy of Roles; got %v", got.Roles)
	}
}

func TestMapResolver_UnknownSubject(t *testing.T) {
	t.Parallel()
	r := NewMapResolver()
	_, _, err := r.Resolve(context.Background(), Identity{Subject: "nope", Method: MethodSession})
	if !errors.Is(err, ErrUnknownIdentity) {
		t.Fatalf("expected ErrUnknownIdentity, got %v", err)
	}
}

func TestMapResolver_RejectsInvalidIdentity(t *testing.T) {
	t.Parallel()
	r := NewMapResolver()
	r.Set("user_123", actor.Actor{ID: "u-123"}, "tenant-acme")
	// Subject populated but method missing — Identity.Validate fails
	// before the resolver looks the subject up.
	_, _, err := r.Resolve(context.Background(), Identity{Subject: "user_123"})
	if err == nil {
		t.Fatalf("expected validation error, got nil")
	}
	if errors.Is(err, ErrUnknownIdentity) {
		t.Errorf("invalid identity should not surface as ErrUnknownIdentity; got %v", err)
	}
}

func TestMapResolver_DeleteAndReplace(t *testing.T) {
	t.Parallel()
	r := NewMapResolver()
	a := actor.Actor{ID: "u-123", DisplayName: "Alice"}
	r.Set("user_123", a, "tenant-acme")

	prev, existed := r.Set("user_123", actor.Actor{ID: "u-123", DisplayName: "Alice II"}, "tenant-acme")
	if !existed || prev.DisplayName != "Alice" {
		t.Fatalf("Set should report previous pair: existed=%v prev=%+v", existed, prev)
	}
	if !r.Delete("user_123") {
		t.Errorf("Delete of registered subject should return true")
	}
	if r.Delete("user_123") {
		t.Errorf("second Delete should return false")
	}
	_, _, err := r.Resolve(context.Background(), Identity{Subject: "user_123", Method: MethodSession})
	if !errors.Is(err, ErrUnknownIdentity) {
		t.Fatalf("after Delete, Resolve should return ErrUnknownIdentity; got %v", err)
	}
}

func TestMapResolver_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	r := NewMapResolver()
	r.Set("user_a", actor.Actor{ID: "a"}, "t1")
	r.Set("user_b", actor.Actor{ID: "b"}, "t2")

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			subject := "user_a"
			if i%2 == 0 {
				subject = "user_b"
			}
			if _, _, err := r.Resolve(context.Background(), Identity{Subject: subject, Method: MethodSession}); err != nil {
				t.Errorf("Resolve(%s): %v", subject, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestMapResolver_AnnotatesDisplayNameWithWallet(t *testing.T) {
	t.Parallel()
	r := NewMapResolver()
	r.Set("user_123", actor.Actor{ID: "u-123", DisplayName: "Alice"}, "tenant-acme")

	id := Identity{
		Subject: "user_123", Method: MethodSession,
		Wallet: &WalletLink{Address: testWalletAddress, ChainID: testChainBase, LinkedAt: time.Now().UTC()},
	}
	got, _, err := r.Resolve(context.Background(), id)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasPrefix(got.DisplayName, "Alice (") || !strings.Contains(got.DisplayName, "0x1234") {
		t.Fatalf("display name should be annotated with wallet short-form, got %q", got.DisplayName)
	}
	if got.ID != "u-123" {
		t.Errorf("Resolve must not change actor.ID: got %q", got.ID)
	}
}

func TestAnnotateDisplay_PureFunction(t *testing.T) {
	t.Parallel()
	cases := []struct {
		display, address, want string
	}{
		{"Alice", testWalletAddress, "Alice (0x1234…5678)"},
		{"", testWalletAddress, "0x1234…5678"},
		{"Alice", "0xshort", "Alice (0xshort)"}, // shorter than 10 chars: pass through
		{"Alice", "", "Alice"},                  // empty address: no annotation
	}
	for _, c := range cases {
		got := annotateDisplay(c.display, c.address)
		if got != c.want {
			t.Errorf("annotateDisplay(%q, %q) = %q, want %q", c.display, c.address, got, c.want)
		}
	}
}
