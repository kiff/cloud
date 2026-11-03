package permit_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/permit"
)

func newKey(t *testing.T, id string) permit.Key {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return permit.Key{ID: id, Priv: priv}
}

// A run grant is KIFF's signed word for one person and one agent of one
// account. It verifies with the key that signed it, or one still
// published, until it expires; nothing else passes for it, not even a
// permit signed with the same key.
func TestRunGrant(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	key, retired, other := newKey(t, "k2"), newKey(t, "k1"), newKey(t, "kx")
	published := []permit.PublicKey{key.Public(), retired.Public()}

	tok, g, err := key.SignRunGrant("https://api.kiff.dev", "t1", "refunds", "ana", now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := permit.VerifyRunGrant(tok, published, now.Add(6*24*time.Hour))
	if err != nil || got != g || got.Sub != "ana" || got.Agent != "refunds" || got.Tenant != "t1" {
		t.Fatalf("verify: %+v %v", got, err)
	}
	old, _, _ := retired.SignRunGrant("https://api.kiff.dev", "t1", "refunds", "ana", now)
	if _, err := permit.VerifyRunGrant(old, published, now); err != nil {
		t.Errorf("a grant from a key still published: %v", err)
	}
	for name, c := range map[string]struct {
		tok string
		at  time.Time
	}{
		"expired":     {tok, now.Add(permit.RunGrantLifetime)},
		"another key": {func() string { s, _, _ := other.SignRunGrant("x", "t1", "refunds", "ana", now); return s }(), now},
		"tampered":    {tampered(tok), now},
		"not a token": {"krg_nope", now},
	} {
		if _, err := permit.VerifyRunGrant(c.tok, published, c.at); !errors.Is(err, permit.ErrInvalidToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A permit is not a run grant, nor the other way, though one key signs both.
	p, err := key.Sign(permit.Claims{Iss: "x", Aud: "a", Sub: "refunds", Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(), Jti: "j",
		Details: []permit.Detail{{Type: permit.DetailType, Tenant: "t1"}}, Act: &permit.Actor{Sub: "ana"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := permit.VerifyRunGrant(p, published, now); !errors.Is(err, permit.ErrInvalidToken) {
		t.Errorf("a permit passed as a run grant: %v", err)
	}
	if _, err := permit.VerifyPermit(tok, published, now); !errors.Is(err, permit.ErrInvalidToken) {
		t.Errorf("a run grant passed as a permit: %v", err)
	}
	c, err := permit.VerifyPermit(p, published, now)
	if err != nil || c.Act == nil || c.Act.Sub != "ana" {
		t.Errorf("a permit for a person's run: %+v %v", c, err)
	}
}

// tampered is tok with the first character of its signature changed, so
// one of the signature's bytes differs.
func tampered(tok string) string {
	i := strings.LastIndex(tok, ".") + 1
	c := "A"
	if tok[i] == 'A' {
		c = "B"
	}
	return tok[:i] + c + tok[i+1:]
}
