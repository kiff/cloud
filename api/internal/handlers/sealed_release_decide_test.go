package handlers_test

import (
	"context"
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
		return json.RawMessage(fmt.Sprintf(`{"chrome_id":"chr_1","op":"call-fill.1","origin":"https://shop.example","nonce":%q,"exp":%d,"sig":"SIGNED-BY-THE-PHONE"}`,
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
