package permit

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) Key {
	t.Helper()
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	k, err := ParseKey("k1:" + base64.RawURLEncoding.EncodeToString(seed))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func decodeToken(t *testing.T, tok string, pub ed25519.PublicKey) (map[string]string, Claims) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a compact JWS: %s", tok)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		t.Fatal("signature does not verify")
	}
	var h map[string]string
	var c Claims
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	pb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(hb, &h)
	_ = json.Unmarshal(pb, &c)
	return h, c
}

func TestMintSignsTheStoredBindingOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	is := &Issuer{Key: testKey(t), Store: NewMemoryStore(), Iss: "https://api.kiff.dev", Now: func() time.Time { return now }}
	ctx := context.Background()
	req := Request{Arguments: json.RawMessage(`{"order_number":"1042","amount_eur":80,"idempotency_key":"inv_1"}`)}
	b, err := req.BindingFor(Target{Action: "refund_order", Tool: "refund_order", Audience: "https://refunds.example.com/mcp", AmountArg: "amount_eur"})
	if err != nil {
		t.Fatal(err)
	}
	stored, created, err := is.Bind(ctx, "t1", "inv_1", b)
	if err != nil || !created || !stored.Same(b) {
		t.Fatalf("bind: %+v %v", stored, err)
	}
	tok, minted, err := is.Mint(ctx, "t1", "agent_1", "inv_1", "card_1", stored)
	if err != nil || !minted {
		t.Fatalf("mint: %v %v", minted, err)
	}
	h, c := decodeToken(t, tok, is.Key.Public().Pub)
	if h["alg"] != "Ed25519" || h["typ"] != "kiff-permit+jwt" || h["kid"] != "k1" {
		t.Fatalf("header: %v", h)
	}
	if c.Aud != b.Audience || c.Sub != "agent_1" || c.Exp-c.Iat != 60 || c.Iss != "https://api.kiff.dev" {
		t.Fatalf("claims: %+v", c)
	}
	d := c.Details[0]
	if len(c.Details) != 1 || d.Type != DetailType || d.Tenant != "t1" || d.Tool != "refund_order" ||
		d.ArgsSHA256 != b.ArgsSHA256 || d.Op != "inv_1" || d.Card != "card_1" {
		t.Fatalf("detail: %+v", c.Details)
	}

	// One permit per operation: a second mint issues nothing.
	if tok2, minted, err := is.Mint(ctx, "t1", "agent_1", "inv_1", "card_1", stored); err != nil || minted || tok2 != "" {
		t.Fatalf("second mint: %q %v %v", tok2, minted, err)
	}
}

func TestBindKeepsTheFirstCall(t *testing.T) {
	is := &Issuer{Key: testKey(t), Store: NewMemoryStore()}
	ctx := context.Background()
	first := Binding{Tool: "refund_order", ArgsSHA256: "aa", Audience: "a"}
	if _, _, err := is.Bind(ctx, "t1", "op", first); err != nil {
		t.Fatal(err)
	}
	got, created, _ := is.Bind(ctx, "t1", "op", Binding{Tool: "refund_order", ArgsSHA256: "bb", Audience: "a"})
	if created {
		t.Fatal("a second bind reported creating the row")
	}
	if !got.Same(first) {
		t.Fatalf("a later call replaced the binding: %+v", got)
	}
	other, _, _ := is.Bind(ctx, "t2", "op", Binding{Tool: "x", ArgsSHA256: "cc", Audience: "a"})
	if other.Tool != "x" {
		t.Fatal("bindings leak across tenants")
	}
}

func TestRequestBindingRefusesNonObjects(t *testing.T) {
	tg := Target{Action: "a", Tool: "t", Audience: "aud"}
	for _, args := range []string{``, `[]`, `"x"`, `{"a":1} trailing`} {
		if _, err := (Request{Arguments: json.RawMessage(args)}).BindingFor(tg); err == nil {
			t.Errorf("accepted arguments %q", args)
		}
	}
}

// The permit carries the configured target, never what a request names.
func TestBindingIsTheTargetNotTheRequest(t *testing.T) {
	tg := Target{Action: "refund_order", Tool: "refund_order", Audience: "https://refunds.example.com/mcp"}
	b, err := (Request{Arguments: json.RawMessage(`{}`)}).BindingFor(tg)
	if err != nil || b.Tool != tg.Tool || b.Audience != tg.Audience {
		t.Fatalf("%+v %v", b, err)
	}
	for _, r := range []Request{
		{Tool: "delete_account", Arguments: json.RawMessage(`{}`)},
		{Audience: "https://evil.example/mcp", Arguments: json.RawMessage(`{}`)},
	} {
		if _, err := r.BindingFor(tg); err != ErrSubstituted {
			t.Errorf("%+v: %v", r, err)
		}
	}
}

// The Card must judge exactly the governed argument the permit signs.
func TestSameGoverned(t *testing.T) {
	tg := Target{Action: "refund_order", Tool: "refund_order", Audience: "a", AmountArg: "amount_eur"}
	r := Request{Arguments: json.RawMessage(`{"order_number":"1042","amount_eur":500}`)}
	cases := []struct {
		params map[string]any
		want   bool
	}{
		{map[string]any{"amount_eur": json.Number("500")}, true},
		{map[string]any{"amount_eur": 500.0}, true},
		{map[string]any{"amount_eur": json.Number("1")}, false},   // the Card would judge €1
		{map[string]any{}, false},                                 // the Card would judge nothing
		{map[string]any{"amount_eur": 500, "other": 1}, false},    // extra parameters
		{map[string]any{"AMOUNT_EUR": json.Number("500")}, false}, // a differently named one
	}
	for _, c := range cases {
		if got := r.SameGoverned(tg, c.params); got != c.want {
			t.Errorf("%v: got %v", c.params, got)
		}
	}
	noAmount := Target{Action: "lookup", Tool: "lookup", Audience: "a"}
	if !r.SameGoverned(noAmount, map[string]any{}) || r.SameGoverned(noAmount, map[string]any{"amount_eur": 500}) {
		t.Error("an action with no governed argument")
	}
	if (Request{Arguments: json.RawMessage(`{"order_number":"1"}`)}).SameGoverned(tg, map[string]any{"amount_eur": 0}) {
		t.Error("a governed argument missing from the signed arguments")
	}
}

func TestLifetimeIsCapped(t *testing.T) {
	is := &Issuer{Key: testKey(t), Store: NewMemoryStore(), Lifetime: time.Hour}
	ctx := context.Background()
	b := Binding{Tool: "t", ArgsSHA256: "h", Audience: "a"}
	_, _, _ = is.Bind(ctx, "t1", "op", b)
	tok, _, _ := is.Mint(ctx, "t1", "s", "op", "", b)
	_, c := decodeToken(t, tok, is.Key.Public().Pub)
	if c.Exp-c.Iat != int64(MaxLifetime/time.Second) {
		t.Fatalf("lifetime %d", c.Exp-c.Iat)
	}
}

func TestJWKSPublishesOKPKeys(t *testing.T) {
	k := testKey(t)
	next, _ := ParsePublicKeys("k2:" + base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	is := &Issuer{Key: k, Others: next}
	rec := httptest.NewRecorder()
	is.JWKSHandler().ServeHTTP(rec, httptest.NewRequest("GET", JWKSPath, nil))
	var set struct{ Keys []JWK }
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		t.Fatal(err)
	}
	if len(set.Keys) != 2 || set.Keys[0].Kid != "k1" || set.Keys[0].Kty != "OKP" || set.Keys[0].Crv != "Ed25519" ||
		set.Keys[0].Alg != "Ed25519" || set.Keys[1].Kid != "k2" {
		t.Fatalf("jwks: %+v", set.Keys)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "max-age=300") {
		t.Fatal("no cache bound")
	}
}

func TestParseKeyRefusesBadInput(t *testing.T) {
	for _, s := range []string{"", "nokid", "k1:short", "bad kid:" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))} {
		if _, err := ParseKey(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}

// A target's resource argument is governed like its amount: the
// resource the Card read is the one in the signed arguments.
func TestSameGovernedWithAResource(t *testing.T) {
	tg := Target{Action: "refund_order", Tool: "refund_order", Audience: "a", AmountArg: "amount_eur", ResourceArg: "order_number"}
	r := Request{Arguments: json.RawMessage(`{"order_number":"1042","amount_eur":500}`)}
	for _, c := range []struct {
		params map[string]any
		want   bool
	}{
		{map[string]any{"amount_eur": 500, "order_number": "1042"}, true},
		{map[string]any{"amount_eur": 500, "order_number": "1043"}, false}, // the Card would judge another order
		{map[string]any{"amount_eur": 500}, false},                         // the Card would judge no order
	} {
		if got := r.SameGoverned(tg, c.params); got != c.want {
			t.Errorf("%v: got %v", c.params, got)
		}
	}
	if (Request{Arguments: json.RawMessage(`{"amount_eur":500}`)}).SameGoverned(tg, map[string]any{"amount_eur": 500, "order_number": "1042"}) {
		t.Error("a resource missing from the signed arguments")
	}
	for _, bad := range []Target{
		{Domain: "gateway", Action: "a", Tool: "t", Audience: "x", ResourceArg: "not valid"},
		{Domain: "gateway", Action: "a", Tool: "t", Audience: "x", AmountArg: "n", ResourceArg: "n"},
	} {
		if bad.Validate() == nil {
			t.Errorf("target %+v accepted", bad)
		}
	}
}
