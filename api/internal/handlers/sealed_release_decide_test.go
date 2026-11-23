package handlers_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
)

// lazyReleases is the rig's own store, set once the rig has made it.
type lazyReleases struct{ s mandate.SealedReleases }

func (l *lazyReleases) KeepSealedRelease(ctx context.Context, tenantID, id string, release json.RawMessage) error {
	return l.s.KeepSealedRelease(ctx, tenantID, id, release)
}

func (l *lazyReleases) TakeSealedRelease(ctx context.Context, tenantID, proposalID string, now time.Time) (json.RawMessage, error) {
	return l.s.TakeSealedRelease(ctx, tenantID, proposalID, now)
}

// Invariant: the phone's release for a sealed fill the owner allowed goes out once, on the decision that sends that call, and on nothing else: not a second decision of it, not one past its exp, not another call, and never into KIFF's decision records.
// Category: authority
// Attack: a taf decides its held sealed fill again after it was sent, or decides another call, to collect the owner's release a second time
// Refused: the second decision of the call carries no release, a release past its exp is never handed over, another call carries none, and no audit record of any decision holds it
func TestASealedFillsReleaseGoesOutOnceOnTheDecisionThatSendsIt(t *testing.T) {
	ctx := context.Background()
	releases := &lazyReleases{}
	r := newGrantRig(t, handlers.WithProposalsSealedReleases(releases))
	releases.s = r.store
	resolved, err := r.rt.For(ctx, r.tnt)
	if err != nil {
		t.Fatal(err)
	}
	type answer struct {
		Outcome       string          `json:"outcome"`
		Reasons       []string        `json:"reasons"`
		SealedRelease json.RawMessage `json:"sealed_release"`
		Exception     *struct {
			ID string `json:"id"`
		} `json:"exception"`
	}
	ingested := map[string]bool{}
	decide := func(call string, amount int, sealed bool) answer {
		t.Helper()
		if !ingested[call] {
			ingested[call] = true
			_, _ = resolved.IngestRaw(ctx, kiffadapter.RawInput{ID: "evt-" + call, Adapter: "cloud", Type: "TOOL_CALL_REQUESTED",
				Source: "kiff-gateway", ReceivedAt: time.Now().UTC(), EntityID: call, EntityType: "ToolCall", ActorID: "refund-agent", Payload: map[string]any{}})
		}
		params := map[string]any{"amount": amount}
		if sealed {
			params[handlers.GatewayOwnerAskParam] = "sealed_fill"
		}
		rec := drivePost(t, r.serve(agentKey), "/v1/proposals/decide", map[string]any{"id": call, "entity_id": call, "entity_type": "ToolCall",
			"action_name": "refund_payment", "actor_id": "refund-agent", "parameters": params})
		var a answer
		_ = json.Unmarshal(rec.Body.Bytes(), &a)
		return a
	}
	release := func(exp time.Time) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"chrome_id":"chr_1","op":"call-fill.2","origin":"https://shop.example","nonce":%q,"exp":%d,"sig":"SIGNED-BY-THE-PHONE"}`,
			base64.RawURLEncoding.EncodeToString([]byte("sixteen-byte-nonce")), exp.UnixMilli()))
	}
	allow := func(call string, a answer, rel json.RawMessage) {
		t.Helper()
		if a.Outcome != "owner_required" || a.Exception == nil {
			t.Fatalf("%s wasn't held: %+v", call, a)
		}
		if _, err := r.store.ApproveOnce(ctx, r.tnt, a.Exception.ID, "ada", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := r.store.KeepSealedRelease(ctx, r.tnt, a.Exception.ID, rel); err != nil {
			t.Fatal(err)
		}
	}

	// Well inside the Card, a sealed fill is still held: the gateway's ask.
	allow("call-fill", decide("call-fill", 100, true), release(time.Now().Add(time.Minute)))
	sent := decide("call-fill", 100, true)
	if sent.Outcome != "allowed" || !strings.Contains(string(sent.SealedRelease), "SIGNED-BY-THE-PHONE") {
		t.Fatalf("the decision that sends it: %+v %s", sent, sent.SealedRelease)
	}
	if again := decide("call-fill", 100, true); len(again.SealedRelease) != 0 {
		t.Fatalf("a second decision carried the release: %s", again.SealedRelease)
	}
	// The same id with other arguments carries none.
	if other := decide("call-fill", 200, true); len(other.SealedRelease) != 0 {
		t.Fatalf("other arguments carried the release: %+v", other)
	}

	// Kept past its exp: the decision goes, with no release.
	allow("call-late", decide("call-late", 100, true), release(time.Now().Add(-time.Second)))
	if late := decide("call-late", 100, true); late.Outcome != "allowed" || len(late.SealedRelease) != 0 {
		t.Fatalf("a late release went out: %+v %s", late, late.SealedRelease)
	}

	// A call not marked sealed_fill never reads the store.
	if plain := decide("call-plain", 100, false); plain.Outcome != "allowed" || len(plain.SealedRelease) != 0 {
		t.Fatalf("a plain call: %+v", plain)
	}

	// No decision's record holds a release.
	records, _ := resolved.Audit.Query(ctx, kiffaudit.Filter{})
	for _, rec := range records {
		b, _ := json.Marshal(rec.Data)
		if strings.Contains(string(b), "SIGNED-BY-THE-PHONE") {
			t.Fatalf("record %s keeps the release: %s", rec.ID, b)
		}
	}
}

// lazyBlocks is the rig's own store, set once the rig has made it.
type lazyBlocks struct{ s mandate.BrowserBlocks }

func (l *lazyBlocks) KeepBrowserBlock(ctx context.Context, tenantID, proposalID string, block json.RawMessage) error {
	return l.s.KeepBrowserBlock(ctx, tenantID, proposalID, block)
}

func (l *lazyBlocks) BrowserBlock(ctx context.Context, tenantID, proposalID string) (json.RawMessage, error) {
	return l.s.BrowserBlock(ctx, tenantID, proposalID)
}

// Invariant: the block a held sealed fill's card draws is the one planque signed for it on the decision that held it, and nothing sent after replaces it.
// Category: authority
// Attack: a taf decides its held sealed fill again with another block planque really signed (for this call, but showing another page), so the card changes under the owner before they allow
// Refused: the block kept on the holding decision stays; a later decision of the same call, with any block, changes nothing, and a plain call's block is never kept
func TestASealedFillsBlockIsKeptOnTheDecisionThatHoldsIt(t *testing.T) {
	ctx := context.Background()
	planque, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	blocks := &lazyBlocks{}
	r := newGrantRig(t, handlers.WithProposalsBrowserBlocks(blocks), handlers.WithProposalsBlockVerifier(handlers.VerifyWith(&planque.PublicKey)))
	blocks.s = r.store
	resolved, err := r.rt.For(ctx, r.tnt)
	if err != nil {
		t.Fatal(err)
	}
	ingested := map[string]bool{}
	decide := func(call string, sealed bool, block json.RawMessage) string {
		t.Helper()
		if !ingested[call] {
			ingested[call] = true
			_, _ = resolved.IngestRaw(ctx, kiffadapter.RawInput{ID: "evt-" + call, Adapter: "cloud", Type: "TOOL_CALL_REQUESTED",
				Source: "kiff-gateway", ReceivedAt: time.Now().UTC(), EntityID: call, EntityType: "ToolCall", ActorID: "refund-agent", Payload: map[string]any{}})
		}
		params := map[string]any{"amount": 100}
		if sealed {
			params[handlers.GatewayOwnerAskParam] = "sealed_fill"
		}
		rec := drivePost(t, r.serve(agentKey), "/v1/proposals/decide", map[string]any{"id": call, "entity_id": call, "entity_type": "ToolCall",
			"action_name": "refund_payment", "actor_id": "refund-agent", "parameters": params, "browser_block": block})
		var a struct {
			Outcome string `json:"outcome"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &a)
		return a.Outcome
	}
	shows := func(words string) func(map[string]any) {
		return func(b map[string]any) { b["action_words"] = words }
	}
	first := handlers.SignedBlock(t, planque, r.tnt, "call-fill", shows("Fill your password into “Password”"))
	if got := decide("call-fill", true, first); got != "owner_required" {
		t.Fatalf("the sealed fill wasn't held: %s", got)
	}
	kept, err := r.store.BrowserBlock(ctx, r.tnt, "call-fill")
	if err != nil || string(kept) != string(first) {
		t.Fatalf("the holding decision's block wasn't kept: %s %v", kept, err)
	}
	second := handlers.SignedBlock(t, planque, r.tnt, "call-fill", shows("Read the news"))
	if got := decide("call-fill", true, second); got != "owner_required" {
		t.Fatalf("the second decision: %s", got)
	}
	if kept, _ := r.store.BrowserBlock(ctx, r.tnt, "call-fill"); string(kept) != string(first) {
		t.Fatalf("a later block replaced the one the card drew: %s", kept)
	}

	// A call the gateway didn't mark sealed_fill keeps no block, even one
	// planque signed for it.
	plain := handlers.SignedBlock(t, planque, r.tnt, "call-plain", nil)
	decide("call-plain", false, plain)
	if got, _ := r.store.BrowserBlock(ctx, r.tnt, "call-plain"); got != nil {
		t.Fatalf("a plain call's block was kept: %s", got)
	}
}
