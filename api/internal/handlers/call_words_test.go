package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/callabout"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
)

// Invariant: what a call is about, in the taf's words, is kept beside its decision and never in the record a receipt's chain hashes: an admin can erase it, and a hold's words with it, and every receipt still verifies.
// Category: privacy
// Attack: a taf writes a customer's phone number into kiff_about, and the account needs it gone from KIFF's record
// Refused: the number is taken out on arrival; what's left is erased without touching the decision or its receipt, whose chain is the same before and after
func TestWhatACallIsAboutIsErasableAndReceiptsStillVerify(t *testing.T) {
	ctx := context.Background()
	about := callabout.NewMemory()
	r := newGrantRig(t, handlers.WithProposalsCallAbout(about))
	resolved, err := r.rt.For(ctx, r.tnt)
	if err != nil {
		t.Fatal(err)
	}
	decide := func(call string, amount int, words, reason string) decided {
		t.Helper()
		_, _ = resolved.IngestRaw(ctx, kiffadapter.RawInput{ID: "evt-" + call, Adapter: "cloud", Type: "TOOL_CALL_REQUESTED",
			Source: "kiff-gateway", ReceivedAt: time.Now().UTC(), EntityID: call, EntityType: "ToolCall", ActorID: "refund-agent", Payload: map[string]any{}})
		rec := drivePost(t, r.serve(agentKey), "/v1/proposals/decide", map[string]any{"id": call, "entity_id": call, "entity_type": "ToolCall",
			"action_name": "refund_payment", "actor_id": "refund-agent", "parameters": map[string]any{"amount": amount},
			"taf_about": words, "taf_reason": reason})
		var d decided
		_ = json.Unmarshal(rec.Body.Bytes(), &d)
		return d
	}
	if d := decide("call-allowed", 100, "Order 1043, call +33 6 12 34 56 78", "damaged box"); d.Outcome != "allowed" {
		t.Fatalf("allowed: %+v", d)
	}
	if d := decide("call-held", 6000, "Order 1042", "arrived broken, ring (+44) 20-7946-0958"); d.Outcome != "owner_required" {
		t.Fatalf("held: %+v", d)
	}
	ids := []string{"call-allowed", "call-held"}
	if got, _ := about.About(ctx, r.tnt, ids); !reflect.DeepEqual(got, map[string]string{"call-allowed": "Order 1043, call …", "call-held": "Order 1042"}) {
		t.Fatalf("kept %v", got)
	}
	held, _ := r.store.Exceptions(ctx, r.tnt, mandate.ExceptionHeld)
	if len(held) != 1 || held[0].Request.TafReason != "arrived broken, ring (…" {
		t.Fatalf("the hold keeps %+v", held)
	}
	// No decision's record carries the words: a receipt's chain hashes those records.
	records, _ := resolved.Audit.Query(ctx, kiffaudit.Filter{})
	for _, rec := range records {
		b, _ := json.Marshal(rec.Data)
		if strings.Contains(string(b), "Order 104") || strings.Contains(string(b), "broken") {
			t.Errorf("record %s keeps the taf's words: %s", rec.ID, b)
		}
	}
	before, err := receipt.BuildDecision(ctx, resolved.Audit, r.tnt, "call-allowed")
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	handlers.NewCallWordsHandler(about, r.store, nil).Register(mux)
	erase := func(id auth.Identity, call string) (int, map[string]any) {
		id.Tenant = r.tnt
		rec := httptest.NewRecorder()
		withFakeIdentity(mux, id).ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/me/calls/"+call+"/words", nil))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, _ := erase(agentKey, "call-allowed"); code != http.StatusForbidden {
		t.Fatalf("an agent's key erased words: %d", code)
	}
	admin := auth.Identity{Subject: "ada", Method: auth.MethodSession}
	for _, call := range ids {
		if code, out := erase(admin, call); code != http.StatusOK || out["erased"] != true {
			t.Fatalf("erase %s: %d %v", call, code, out)
		}
	}
	if code, out := erase(admin, "call-held"); code != http.StatusOK || out["erased"] != false {
		t.Fatalf("erasing again: %d %v", code, out)
	}
	if got, _ := about.About(ctx, r.tnt, ids); len(got) != 0 {
		t.Fatalf("still kept %v", got)
	}
	held, _ = r.store.Exceptions(ctx, r.tnt, mandate.ExceptionHeld)
	if len(held) != 1 || held[0].Request.TafAbout != "" || held[0].Request.TafReason != "" || held[0].Fingerprint == "" {
		t.Fatalf("the hold after erasing: %+v", held)
	}
	after, err := receipt.BuildDecision(ctx, resolved.Audit, r.tnt, "call-allowed")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.CausalChain, after.CausalChain) {
		t.Fatalf("the receipt's chain changed: %+v vs %+v", before.CausalChain, after.CausalChain)
	}
}

// The Record shows what a call is about, as kept beside its decision, and
// shows none once an admin has erased it.
func TestTheRecordShowsWhatACallIsAboutUntilItsErased(t *testing.T) {
	ctx := context.Background()
	about := callabout.NewMemory()
	r := newGrantRig(t, handlers.WithProposalsCallAbout(about))
	resolved, err := r.rt.For(ctx, r.tnt)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = resolved.IngestRaw(ctx, kiffadapter.RawInput{ID: "evt-call-1", Adapter: "cloud", Type: "TOOL_CALL_REQUESTED",
		Source: "kiff-gateway", ReceivedAt: time.Now().UTC(), EntityID: "call-1", EntityType: "ToolCall", ActorID: "refund-agent", Payload: map[string]any{}})
	rec := drivePost(t, r.serve(agentKey), "/v1/proposals/decide", map[string]any{"id": "call-1", "entity_id": "call-1", "entity_type": "ToolCall",
		"action_name": "refund_payment", "actor_id": "refund-agent", "parameters": map[string]any{"amount": 100}, "taf_about": "Order 1042"})
	if rec.Code != http.StatusOK {
		t.Fatalf("decide: %d %s", rec.Code, rec.Body)
	}

	// The Record reads the decision as KIFF's reader lists it, and what it's about beside it.
	reader := proposals.NewMemoryReader()
	reader.Append(r.tnt, proposals.DecisionRow{ProposalID: "call-1", ActorID: "refund-agent", ActionName: "refund_payment", Outcome: "allowed", CreatedAt: time.Now()})
	reader.Append(r.tnt, proposals.DecisionRow{ProposalID: "call-2", ActorID: "refund-agent", ActionName: "refund_payment", Outcome: "allowed", CreatedAt: time.Now().Add(-time.Minute)})
	controls := handlers.NewControlsHandler(domain.NewMemoryControlStore(), nil, handlers.WithControlsDecisionsReader(reader), handlers.WithControlsCallAbout(about))
	mux := http.NewServeMux()
	controls.Register(mux)
	handlers.NewCallWordsHandler(about, r.store, nil).Register(mux)
	admin := withFakeIdentity(mux, auth.Identity{Tenant: r.tnt, Subject: "ada", Method: auth.MethodSession})
	abouts := func() map[string]string {
		t.Helper()
		code, page := getActivity(t, admin, "")
		if code != http.StatusOK {
			t.Fatalf("activity: %d", code)
		}
		out := map[string]string{}
		for _, c := range page.Calls {
			out[c.ProposalID] = c.About
		}
		return out
	}
	if got := abouts(); got["call-1"] != "Order 1042" || got["call-2"] != "" {
		t.Fatalf("the Record shows %v", got)
	}
	erased := httptest.NewRecorder()
	admin.ServeHTTP(erased, httptest.NewRequest(http.MethodDelete, "/v1/me/calls/call-1/words", nil))
	if erased.Code != http.StatusOK {
		t.Fatalf("erase: %d %s", erased.Code, erased.Body)
	}
	if got := abouts(); got["call-1"] != "" {
		t.Fatalf("after erasing, the Record shows %v", got)
	}
}
