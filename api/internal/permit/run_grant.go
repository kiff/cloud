package permit

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// RunGrantType is the typ of a run grant: KIFF's word that a person may
// use an agent, carried by the run they asked for and checked by KIFF on
// each of its calls. A permit verifier refuses it, its typ not being Type.
const RunGrantType = "kiff-run-grant+jwt"

// RunGrantLifetime is how long a run grant is good for: enough for a run
// paused on a hold. Each use checks again, live, that the person may use
// the agent, so a removed grant takes effect at once.
const RunGrantLifetime = 7 * 24 * time.Hour

// RunGrant is the person a run is for: the account, the agent and the
// person's sign-in subject.
type RunGrant struct {
	Iss    string `json:"iss"`
	Iat    int64  `json:"iat"`
	Exp    int64  `json:"exp"`
	Jti    string `json:"jti"`
	Tenant string `json:"tenant"`
	Agent  string `json:"agent"`
	Sub    string `json:"sub"`
}

// SignRunGrant returns a run grant, from iss at now, for sub to use agent
// of tenant.
func (k Key) SignRunGrant(iss, tenant, agent, sub string, now time.Time) (string, RunGrant, error) {
	g := RunGrant{Iss: iss, Iat: now.Unix(), Exp: now.Add(RunGrantLifetime).Unix(), Jti: NewJTI(), Tenant: tenant, Agent: agent, Sub: sub}
	tok, err := k.sign(RunGrantType, g)
	return tok, g, err
}

// ErrInvalidToken is a token that isn't KIFF's, isn't of the type asked
// for, or has expired.
var ErrInvalidToken = errors.New("permit: not a valid token of KIFF's")

// VerifyRunGrant checks a run grant against KIFF's keys (its signing key
// and those it still publishes) and its expiry, and returns what it says.
func VerifyRunGrant(token string, keys []PublicKey, now time.Time) (RunGrant, error) {
	var g RunGrant
	if err := verify(token, RunGrantType, keys, &g); err != nil {
		return RunGrant{}, err
	}
	if g.Tenant == "" || g.Agent == "" || g.Sub == "" || now.Unix() >= g.Exp {
		return RunGrant{}, ErrInvalidToken
	}
	return g, nil
}

// VerifyPermit checks a permit KIFF issued against its keys and expiry,
// and returns its claims. A permit it issued for a person's run names
// them in Act.
func VerifyPermit(token string, keys []PublicKey, now time.Time) (Claims, error) {
	var c Claims
	if err := verify(token, Type, keys, &c); err != nil {
		return Claims{}, err
	}
	if now.Unix() >= c.Exp || len(c.Details) != 1 || c.Details[0].Tenant == "" {
		return Claims{}, ErrInvalidToken
	}
	return c, nil
}

// verify checks a compact JWS of type typ, signed with one of keys, and
// reads its payload into out.
func verify(token, typ string, keys []PublicKey, out any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ErrInvalidToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ErrInvalidToken
	}
	var h struct{ Alg, Typ, Kid string }
	if json.Unmarshal(raw, &h) != nil || h.Alg != Alg || h.Typ != typ {
		return ErrInvalidToken
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil {
		return ErrInvalidToken
	}
	for _, k := range keys {
		if k.ID != h.Kid || !ed25519.Verify(k.Pub, []byte(parts[0]+"."+parts[1]), sig) {
			continue
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || json.Unmarshal(payload, out) != nil {
			return ErrInvalidToken
		}
		return nil
	}
	return ErrInvalidToken
}
