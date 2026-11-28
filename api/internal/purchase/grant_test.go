package purchase

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// The execution grant is signed with KIFF's key under its own typ, carries
// the case as the relay executes it, and is minted only for an executing
// case within its time.
func TestTheExecutionGrantCarriesTheCaseOnlyWhileItExecutes(t *testing.T) {
	c, digest := contractCase(t)
	k := receiptKey(t)
	now := time.Date(2026, 11, 28, 9, 15, 11, 0, time.UTC)
	r := Row{Case: c, Digest: digest, State: StateExecuting, LeaseHolder: "exe_1", ValidUntil: now.Add(15 * time.Minute)}
	tok, jti, err := MintGrant(k, "https://api.kiff.dev", "https://relay.example.com", r, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	pb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if !ed25519.Verify(k.Public().Pub, []byte(parts[0]+"."+parts[1]), sig) {
		t.Fatal("signature")
	}
	var h map[string]string
	var g GrantClaims
	_ = json.Unmarshal(hb, &h)
	_ = json.Unmarshal(pb, &g)
	if h["typ"] != GrantType || h["typ"] == "kiff-permit+jwt" {
		t.Fatalf("typ %q", h["typ"])
	}
	p := g.Purchase
	if g.Jti != jti || p.CaseID != c.CaseID || p.Digest != digest || p.Cap != c.Price.Max || p.Seller != c.Merchant.Site || p.Holder != "exe_1" ||
		g.Exp != r.ValidUntil.Unix() || p.Delivery != c.Delivery {
		t.Fatalf("claims %+v", g)
	}
	for name, bad := range map[string]Row{
		"approved, not executing": {Case: c, Digest: digest, State: StateApproved, LeaseHolder: "exe_1", ValidUntil: r.ValidUntil},
		"no holder":               {Case: c, Digest: digest, State: StateExecuting, ValidUntil: r.ValidUntil},
		"past its time":           {Case: c, Digest: digest, State: StateExecuting, LeaseHolder: "exe_1", ValidUntil: now},
	} {
		if _, _, err := MintGrant(k, "i", "a", bad, now); !errors.Is(err, ErrNotHeld) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
