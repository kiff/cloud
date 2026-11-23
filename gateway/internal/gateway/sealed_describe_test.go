package gateway_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// planque's two browser tools on one connection, as KIFF's catalog lists
// them: each call verified by planque with KIFF's permit.
var (
	browseOpenTool = gateway.Tool{Name: "browse_open", Description: "Open a page", ReadOnly: true,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`),
		UpstreamURL: "http://planque.test/relay/t1/grt_1", UpstreamTool: "browse_open", IdempotencyArg: "op", Mode: gateway.ModeVerify, Audience: "planque"}
	browseActTool = gateway.Tool{Name: "browse_act", Description: "Fill in and click on a page",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"},"read":{"type":"string"},"steps":{"type":"array"}},"required":["url","read","steps"]}`),
		UpstreamURL: "http://planque.test/relay/t1/grt_1", UpstreamTool: "browse_act", IdempotencyArg: "op", Mode: gateway.ModeVerify, Audience: "planque"}
)

// describedBlock is planque's answer to a describe: its signed block.
var describedBlock = json.RawMessage(`{"block":{"v":"planque/describe/v1","op":"x.2"},"sig":"c2lnbmVkLWJ5LXBsYW5xdWU"}`)

func sealedRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	r.gw.Catalog = catalog{browseOpenTool, browseActTool}
	answer, _ := json.Marshal(map[string]any{"content": []map[string]any{{"type": "text", "text": "Described for the owner's card."}},
		"structuredContent": map[string]any{"browser_block": describedBlock}})
	r.tool.described = answer
	return r
}

func sealedFill(op string) map[string]any {
	return map[string]any{"url": "https://shop.example/", "read": "rd-9f2", gateway.OperationArg: op,
		"steps": []any{map[string]any{"fill": "f1", "sealed": "sec_a1:password"}}}
}

// callNamed calls one of the rig's tools by name, as callTool does.
func (r *rig) callNamed(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	status, out := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": name, "arguments": args})
	if status != http.StatusOK || out["result"] == nil {
		t.Fatalf("tools/call: %d %v", status, out)
	}
	res := out["result"].(map[string]any)
	meta, _ := res["_meta"].(map[string]any)
	call, _ := meta[gateway.MetaKey].(map[string]any)
	return call
}

// Invariant: a sealed fill is held only with planque's block for the page its owner will be shown, read by an open KIFF allowed on its own, on the same connection, as the sealed call's own run
// Category: authority
// Attack: a taf asks for a sealed fill so the gateway opens a page in the owner's Chrome that the taf's Card wouldn't let it open, or so the hold's card shows a page other than the one the fill acts on
// Refused: the open is decided by KIFF as "<call>:open" with its own permit for exactly {url, op}; planque gets the describe header naming the sealed call, its read, field and handle; the hold carries planque's block as planque answered it; nothing else is sent
func TestASealedFillIsDescribedByItsOwnAllowedOpenBeforeItsHeld(t *testing.T) {
	r := sealedRig(t)
	call := r.callNamed(t, "browse_act", sealedFill("op-fill"))
	key, _ := call["invocation"].(string)
	if call["state"] != "held" || key == "" {
		t.Fatalf("not held: %v", call)
	}
	open := key + ":open"
	// KIFF decided the open on its own, with a permit for exactly it.
	if r.api.ingested[open] != 1 {
		t.Fatalf("the open wasn't recorded with KIFF: %v", r.api.ingested)
	}
	pr, ok := r.api.permits[open]
	if !ok || pr.Tool != "browse_open" || pr.Audience != "planque" {
		t.Fatalf("the open's permit request: %+v", pr)
	}
	var openArgs map[string]any
	_ = json.Unmarshal(pr.Arguments, &openArgs)
	if len(openArgs) != 2 || openArgs["url"] != "https://shop.example/" || openArgs["op"] != open {
		t.Fatalf("the open's arguments: %v", openArgs)
	}
	// planque was asked once, with browse_open, the open's permit and the
	// describe naming the sealed call.
	if !slices.Equal(r.tool.tools, []string{"browse_open"}) || r.tool.permits[0] != "permit-for-"+open {
		t.Fatalf("sent: %v %v", r.tool.tools, r.tool.permits)
	}
	raw, err := base64.RawURLEncoding.DecodeString(r.tool.describes[0])
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]string
	_ = json.Unmarshal(raw, &d)
	if d["op"] != key || d["read"] != "rd-9f2" || d["fill"] != "f1" || d["sealed"] != "sec_a1:password" {
		t.Fatalf("the describe: %v", d)
	}
	// The hold carries planque's block, byte for byte.
	if string(r.api.blocks[key]) != string(describedBlock) {
		t.Fatalf("the hold's block: %s", r.api.blocks[key])
	}
	// Asked again while held: no second describe.
	if again := r.callNamed(t, "browse_act", sealedFill("op-fill")); again["state"] != "held" || len(r.tool.tools) != 1 {
		t.Fatalf("a retry: %v, sent %v", again, r.tool.tools)
	}
}

// Once allowed, the sealed call goes with the phone's release, and the
// describe isn't asked again.
func TestAnAllowedSealedFillGoesWithItsReleaseAndNoSecondDescribe(t *testing.T) {
	r := sealedRig(t)
	call := r.callNamed(t, "browse_act", sealedFill("op-fill"))
	key := call["invocation"].(string)
	release := json.RawMessage(`{"chrome_id":"chr_1","op":"x.2","origin":"https://shop.example","nonce":"bm9uY2Utbm9uY2Utbm9uY2U","exp":1,"sig":"MEUCIQ"}`)
	r.api.mu.Lock()
	r.api.approved[key] = true
	r.api.sealedReleases[key] = release
	r.api.mu.Unlock()
	if got := r.callNamed(t, "browse_act", sealedFill("op-fill")); got["state"] == "held" {
		t.Fatalf("still held: %v", got)
	}
	if !slices.Equal(r.tool.tools, []string{"browse_open", "browse_act"}) {
		t.Fatalf("sent: %v", r.tool.tools)
	}
	if r.tool.releases[1] != base64.RawURLEncoding.EncodeToString(release) || r.tool.describes[1] != "" {
		t.Fatalf("the act went with %q / describe %q", r.tool.releases[1], r.tool.describes[1])
	}
}

// Invariant: a sealed fill whose page can't be shown to its owner is refused, never held
// Category: authority
// Attack: a taf's sealed fill whose open KIFF would hold or refuse, or that planque won't describe, still opens a hold the owner might allow
// Refused: the call is refused with sealed_fill_not_described, KIFF never decides the sealed call, and nothing more reaches planque
func TestASealedFillThatCantBeDescribedIsRefusedNotHeld(t *testing.T) {
	for name, set := range map[string]func(r *rig){
		"the open is held":    func(r *rig) { r.api.openOutcome = "owner_required" },
		"the open is refused": func(r *rig) { r.api.openOutcome = "refused" },
		"planque refuses it": func(r *rig) {
			r.tool.described = mcp.TextResult("Refused by planque: sealed_busy", true)
		},
		"no block":  func(r *rig) { r.tool.described = mcp.TextResult("opened", false) },
		"two steps": func(r *rig) {},
	} {
		t.Run(name, func(t *testing.T) {
			r := sealedRig(t)
			set(r)
			args := sealedFill("op-fill")
			if name == "two steps" {
				args["steps"] = []any{map[string]any{"fill": "f2", "value": "x"}, map[string]any{"fill": "f1", "sealed": "sec_a1:password"}}
			}
			call := r.callNamed(t, "browse_act", args)
			key, _ := call["invocation"].(string)
			reasons, _ := json.Marshal(call["reasons"])
			if call["state"] != "refused" || !strings.Contains(string(reasons), gateway.ReasonSealedNotDescribed) {
				t.Fatalf("%v", call)
			}
			if _, decided := r.api.params[key]; decided {
				t.Fatal("KIFF decided the sealed call")
			}
			if slices.Contains(r.tool.tools, "browse_act") {
				t.Fatalf("the act was sent: %v", r.tool.tools)
			}
		})
	}
}

// Invariant: the describe header and planque's block are the gateway's alone: a taf can't send its own describe, and never reads the block
// Category: authority
// Attack: a taf sets KIFF-Sealed-Describe on its own call to have planque open and keep a page for a fill KIFF never decided, or reads the signed block to replay it
// Refused: no call reaches planque with the taf's header (only the gateway's own describe, on its own open), and the taf's answers carry no browser_block
func TestATafsOwnDescribeNeverReachesPlanqueAndItNeverReadsTheBlock(t *testing.T) {
	r := sealedRig(t)
	forged := base64.RawURLEncoding.EncodeToString([]byte(`{"op":"forged","read":"rd-9f2","fill":"f1","sealed":"sec_a1:password"}`))
	r.headers = map[string]string{gateway.SealedDescribeHeader: forged}
	// A plain open, with the taf's own header.
	status, out := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": "browse_open", "arguments": map[string]any{"url": "https://shop.example/", gateway.OperationArg: "op-open"}})
	if status != http.StatusOK || out["result"] == nil {
		t.Fatalf("open: %d %v", status, out)
	}
	// A sealed fill, with it too: the gateway's describe goes, never the taf's.
	status, held := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": "browse_act", "arguments": sealedFill("op-fill")})
	if status != http.StatusOK {
		t.Fatalf("fill: %d %v", status, held)
	}
	for i, d := range r.tool.describes {
		if d == forged {
			t.Fatalf("call %d (%s) reached planque with the taf's describe", i, r.tool.tools[i])
		}
	}
	if len(r.tool.describes) != 2 || r.tool.describes[0] != "" || r.tool.describes[1] == "" {
		t.Fatalf("describes sent: %q for %v", r.tool.describes, r.tool.tools)
	}
	// Neither answer the taf read carries planque's block.
	for _, ans := range []map[string]any{out, held} {
		b, _ := json.Marshal(ans)
		if strings.Contains(string(b), "browser_block") || strings.Contains(string(b), "c2lnbmVkLWJ5LXBsYW5xdWU") {
			t.Fatalf("the taf's answer carries the block: %s", b)
		}
	}
}
