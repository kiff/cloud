package permit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestWriteInteropFixture writes a permit minted by this package for the
// Python verifier's interop test, when KIFF_PERMIT_FIXTURE names a file.
func TestWriteInteropFixture(t *testing.T) {
	out := os.Getenv("KIFF_PERMIT_FIXTURE")
	if out == "" {
		t.Skip("KIFF_PERMIT_FIXTURE not set")
	}
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(7 * i)
	}
	k, _ := ParseKey("fixture-1:" + base64.RawURLEncoding.EncodeToString(seed))
	now := time.Unix(1_800_000_000, 0)
	is := &Issuer{Key: k, Store: NewMemoryStore(), Iss: "https://api.kiff.dev", Now: func() time.Time { return now }}
	args := json.RawMessage(`{"order_number":"1042","amount_eur":80,"reason":"damaged","idempotency_key":"op-fixture"}`)
	b, err := Request{Arguments: args}.BindingFor(Target{Action: "refund_order", Tool: "refund_order", Audience: "https://refunds.example.com/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = is.Bind(context.Background(), "tenant-fixture", "op-fixture", b)
	tok, _, err := is.Mint(context.Background(), "tenant-fixture", "agent-fixture", "op-fixture", "card-fixture", b)
	if err != nil {
		t.Fatal(err)
	}
	fx, _ := json.MarshalIndent(map[string]any{
		"token": tok, "jwks": JWKS(k.Public(), nil), "arguments": json.RawMessage(args), "now": now.Unix(),
		"issuer": "https://api.kiff.dev", "audience": "https://refunds.example.com/mcp", "tenant": "tenant-fixture",
		"tool": "refund_order", "args_sha256": b.ArgsSHA256,
	}, "", " ")
	if err := os.WriteFile(out, fx, 0o644); err != nil {
		t.Fatal(err)
	}
}
