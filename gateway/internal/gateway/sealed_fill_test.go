package gateway_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
)

// Invariant: a sealed fill is held for its owner, and once allowed it goes to the tool with the phone's release in KIFF-Sealed-Release, exactly as apps/api handed it, once, and never inside the call's arguments.
// Category: authority
// Attack: a taf puts its own release, or a stray header, beside a sealed fill so the Chrome fills a saved password
// Refused: the call's arguments carry no release, the header is the gateway's alone (set from KIFF's decision), and a call KIFF handed no release goes with none
func TestASealedFillGoesWithThePhonesReleaseOnlyOnceAllowed(t *testing.T) {
	r := newRig(t)
	fill := func(op string) map[string]any {
		a := amount(500, op)
		a["steps"] = []any{map[string]any{"fill": "f1", "sealed": "sec_a1:password"}}
		return a
	}
	got := r.call(t, "agent-key", fill("op-sealed"))
	key, _ := got.call["invocation"].(string)
	if key == "" || got.call["state"] != "held" || len(r.tool.calls) != 0 {
		t.Fatalf("a sealed fill wasn't held: %+v, %d sent", got, len(r.tool.calls))
	}
	if p := r.api.params[key]; p[gateway.OwnerAskParam] != gateway.OwnerAskSealedFill {
		t.Fatalf("a sealed fill told KIFF %v", p[gateway.OwnerAskParam])
	}

	// The owner allows it with the phone's release; the run sends it again.
	release := json.RawMessage(`{"chrome_id":"chr_1","op":"op-sealed.1","origin":"https://shop.example","nonce":"bm9uY2Utbm9uY2Utbm9uY2U","exp":1,"sig":"MEUCIQ"}`)
	r.api.mu.Lock()
	r.api.approved[key] = true
	r.api.sealedReleases[key] = release
	r.api.mu.Unlock()
	r.call(t, "agent-key", fill("op-sealed"))
	if len(r.tool.calls) != 1 {
		t.Fatalf("sent %d times", len(r.tool.calls))
	}
	if want := base64.RawURLEncoding.EncodeToString(release); r.tool.releases[0] != want {
		t.Fatalf("the release sent was %q, want %q", r.tool.releases[0], want)
	}
	if strings.Contains(string(r.tool.raw[0]), "MEUCIQ") || strings.Contains(string(r.tool.raw[0]), "release") {
		t.Fatalf("the release rode in the arguments: %s", r.tool.raw[0])
	}

	// A call KIFF handed no release goes with none, even when the taf sends
	// its own header: the gateway's headers to the tool are its own.
	plain := newRig(t)
	plain.headers = map[string]string{gateway.SealedReleaseHeader: base64.RawURLEncoding.EncodeToString(release)}
	plain.call(t, "agent-key", amount(500, "op-plain"))
	if len(plain.tool.releases) != 1 || plain.tool.releases[0] != "" {
		t.Fatalf("a plain call went with a release: %q", plain.tool.releases)
	}
}
