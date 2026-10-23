package permit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// Request is what a decide request carries when the call's connection
// is in verify or relay mode: the exact arguments the gateway
// will forward, from which apps/api computes args_sha256 itself. Tool and
// Audience are optional and only checked: the permit carries the action's
// Target, which the account configured, never what the request names.
type Request struct {
	Audience  string          `json:"audience,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Arguments json.RawMessage `json:"arguments"`
}

// ErrInvalid: the permit request is malformed. ErrSubstituted: it names a
// tool or audience other than the action's target.
var (
	ErrInvalid     = errors.New("permit: invalid request")
	ErrSubstituted = errors.New("permit: the request names another tool or audience than the action's target")
)

// BindingFor computes the binding for the action's target t.
func (r Request) BindingFor(t Target) (Binding, error) {
	if (r.Tool != "" && r.Tool != t.Tool) || (r.Audience != "" && r.Audience != t.Audience) {
		return Binding{}, ErrSubstituted
	}
	if len(r.Arguments) == 0 || r.Arguments[0] != '{' {
		return Binding{}, ErrInvalid
	}
	h, err := ArgsSHA256(r.Arguments)
	if err != nil {
		return Binding{}, ErrInvalid
	}
	return Binding{Tool: t.Tool, ArgsSHA256: h, Audience: t.Audience}, nil
}

// SameGoverned reports whether params, the parameters the Card evaluates,
// are exactly the target's governed arguments as they appear in the signed
// arguments: the amount argument and the resource argument where the
// target names them, and nothing else. Numbers compare as JSON numbers
// (80 and 80.0 are equal).
func (r Request) SameGoverned(t Target, params map[string]any) bool {
	var args map[string]json.RawMessage
	if json.Unmarshal(r.Arguments, &args) != nil {
		return false
	}
	want := map[string]json.RawMessage{}
	if t.AmountArg != "" {
		v, ok := args[t.AmountArg]
		if !ok {
			return false
		}
		want[t.AmountArg] = v
	}
	if t.ResourceArg != "" {
		v, ok := args[t.ResourceArg]
		if !ok {
			return false
		}
		want[t.ResourceArg] = v
	}
	if len(params) != len(want) {
		return false
	}
	for k, v := range params {
		w, ok := want[k]
		if !ok {
			return false
		}
		got, err := json.Marshal(v)
		if err != nil {
			return false
		}
		a, err1 := Canonicalize(got)
		b, err2 := Canonicalize(w)
		if err1 != nil || err2 != nil || string(a) != string(b) {
			return false
		}
	}
	return true
}

// Issuer mints permits.
type Issuer struct {
	Key Key
	// Targets says where each action's permit goes.
	Targets Targets
	// Others are published alongside Key (the next key, or a retired one
	// whose permits may not have expired yet).
	Others   []PublicKey
	Store    Store
	Iss      string
	Lifetime time.Duration
	Now      func() time.Time
}

func (i *Issuer) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}

// Bind records the request's binding for (tenant, op) at the operation's
// first decision and returns what is recorded, and whether this call
// created it.
func (i *Issuer) Bind(ctx context.Context, tenantID, op string, b Binding) (Binding, bool, error) {
	return i.Store.Bind(ctx, tenantID, op, b, i.now())
}

// Mint signs the operation's one permit from its stored binding. minted
// is false, with no token, when the operation already has its permit.
func (i *Issuer) Mint(ctx context.Context, tenantID, sub, op, card string, b Binding) (token string, minted bool, err error) {
	life := i.Lifetime
	if life <= 0 {
		life = DefaultLifetime
	}
	if life > MaxLifetime {
		life = MaxLifetime
	}
	now := i.now()
	c := Claims{
		Iss: i.Iss, Aud: b.Audience, Sub: sub, Iat: now.Unix(), Exp: now.Add(life).Unix(), Jti: NewJTI(),
		Details: []Detail{{Type: DetailType, Tenant: tenantID, Tool: b.Tool, ArgsSHA256: b.ArgsSHA256,
			Op: op, Card: card, Decision: op}},
	}
	// Sign before marking: a mark without a token would leave the
	// operation unable to run, a token without a mark is never sent.
	tok, err := i.Key.Sign(c)
	if err != nil {
		return "", false, err
	}
	ok, err := i.Store.MarkMinted(ctx, tenantID, op, c.Jti, now)
	if err != nil || !ok {
		return "", false, err
	}
	return tok, true, nil
}

// JWKSHandler serves the published keys at
// /.well-known/kiff-permit-keys.json. Verifiers refetch at least every
// five minutes, so caches may keep it that long.
func (i *Issuer) JWKSHandler() http.Handler {
	body, _ := json.Marshal(JWKS(i.Key.Public(), i.Others))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/jwk-set+json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = w.Write(body)
	})
}

// JWKSPath is where the keys are published.
const JWKSPath = "/.well-known/kiff-permit-keys.json"
