package permit_test

import (
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/permit"
)

// A read grant is KIFF's, short-lived and its own type: neither a run
// grant nor a permit passes for one, and none passes for those.
func TestReadGrant(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	key, other := newKey(t, "k1"), newKey(t, "kx")
	published := []permit.PublicKey{key.Public()}
	want := permit.ReadGrant{Jti: "rd_1", Tenant: "t1", Agent: "refunds", Reader: "ada", Owner: "ana", Session: "s1", Purpose: "fix"}
	tok, g, err := key.SignReadGrant("https://api.kiff.dev", want, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := permit.VerifyReadGrant(tok, published, now.Add(9*time.Minute))
	if err != nil || got != g || got.Jti != "rd_1" || got.Reader != "ada" || got.Owner != "ana" || got.Purpose != "fix" {
		t.Fatalf("verify: %+v %v", got, err)
	}
	run, _, _ := key.SignRunGrant("https://api.kiff.dev", "t1", "refunds", "ana", now)
	forged, _, _ := other.SignReadGrant("https://api.kiff.dev", want, now)
	for name, c := range map[string]struct {
		tok string
		at  time.Time
	}{
		"expired":     {tok, now.Add(permit.ReadGrantLifetime)},
		"another key": {forged, now},
		"tampered":    {tampered(tok), now},
		"a run grant": {run, now},
		"not a token": {"nope", now},
		"empty":       {"", now},
	} {
		if _, err := permit.VerifyReadGrant(c.tok, published, c.at); !errors.Is(err, permit.ErrInvalidToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := permit.VerifyRunGrant(tok, published, now); !errors.Is(err, permit.ErrInvalidToken) {
		t.Errorf("a read grant passed as a run grant: %v", err)
	}
	if _, err := permit.VerifyPermit(tok, published, now); !errors.Is(err, permit.ErrInvalidToken) {
		t.Errorf("a read grant passed as a permit: %v", err)
	}
}
