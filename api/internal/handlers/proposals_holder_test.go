package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/templates"
	"github.com/kiff/cloud/api/internal/tenant"
)

// Card-holder identity. A card names its holder, and the card check
// draws on whichever actor_id the proposal carries, so the credential
// has to be able to speak for that actor: a key bound to an agent may
// only act as that agent, and an action that requires a card refuses a
// runtime key not bound to the card's holder. These run through the
// real decide path on the card-refund template, where REFUND_ORDER
// requires a card and ISSUE_CREDIT does not.

// cardLedger stands in for the card store: holders maps each agent to
// the actions its cards cover. A covered action is allowed with room
// left and records whose card was drawn on, so a test can tell a
// refusal made before the card check from one made by it; anything
// else is not covered.
type cardLedger struct {
	mu       sync.Mutex
	holders  map[string][]string
	subjects []string
}

func (s *cardLedger) covers(req mandate.Request) bool {
	for _, a := range s.holders[req.Subject] {
		if a == req.Action {
			return true
		}
	}
	return false
}

func (s *cardLedger) Covers(_ context.Context, _ string, req mandate.Request) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.covers(req), nil
}

func (s *cardLedger) Authorize(_ context.Context, _, _ string, req mandate.Request) (mandate.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.covers(req) {
		return mandate.Decision{Outcome: mandate.OutcomeNotCovered}, nil
	}
	s.subjects = append(s.subjects, req.Subject)
	return mandate.Decision{Outcome: mandate.OutcomeWithinLimit, MandateID: "card-" + req.Subject}, nil
}

func (s *cardLedger) drawn() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.subjects...)
}

// holderRig serves the decide path for one tenant on the card-refund
// template, with one paid order, as the given identity.
func holderRig(t *testing.T, id auth.Identity) (http.Handler, *cardLedger) {
	t.Helper()
	serve, cards := holderRigAs(t)
	return serve(id), cards
}

// holderRigAs is holderRig for a test that sends as several
// identities against one tenant: it returns a function that serves the
// same decide path, runtime and card ledger as any identity.
func holderRigAs(t *testing.T) (func(auth.Identity) http.Handler, *cardLedger) {
	t.Helper()
	ctx := context.Background()

	tmpl, err := templates.Get("card-refund")
	if err != nil {
		t.Fatalf("load template: %v", err)
	}
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-holder", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, tmpl.YAML); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	rt := cloudruntime.New(cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry()))

	resolved, err := rt.For(ctx, tnt.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := resolved.IngestRaw(ctx, kiffadapter.RawInput{
		ID: "paid-o1", Adapter: "cloud", Type: "ORDER_PAID", Source: "test",
		ReceivedAt: time.Now().UTC(), EntityID: "o1", EntityType: "Order",
		ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-o1"},
		Payload: map[string]any{},
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// refund-agent holds a card for the action that requires one;
	// credit-agent holds one for an action that does not, which a card
	// may still cover and draw on.
	cards := &cardLedger{holders: map[string][]string{
		"refund-agent": {"REFUND_ORDER"},
		"credit-agent": {"ISSUE_CREDIT"},
	}}
	h := handlers.NewProposalsHandler(rt, handlers.WithProposalsMandates(cards))
	inner := http.NewServeMux()
	h.Register(inner)
	return func(id auth.Identity) http.Handler {
		id.Tenant = tnt.ID
		return withFakeIdentity(inner, id)
	}, cards
}

func runtimeKey(agentID string) auth.Identity {
	return auth.Identity{
		Subject: "owner", Method: auth.MethodAPIKey, APIKey: true,
		Roles: []string{"support"}, AgentID: agentID,
		Metadata: map[string]string{"api_key_id": "key-1"},
	}
}

func propose(t *testing.T, h http.Handler, action, actor string) *httptest.ResponseRecorder {
	t.Helper()
	return proposeWithID(t, h, "", action, actor)
}

// proposeWithID sends a caller-supplied stable id, which makes the
// request idempotent: a retry within the window returns the recorded
// decision instead of deciding again.
func proposeWithID(t *testing.T, h http.Handler, proposalID, action, actor string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{
		"entity_id": "o1", "entity_type": "Order", "action_name": action,
		"parameters": map[string]any{"amount_cents": 500},
	}
	if actor != "" {
		body["actor_id"] = actor
	}
	if proposalID != "" {
		body["id"] = proposalID
	}
	return drivePost(t, h, "/v1/proposals/decide", body)
}

func TestHolder_BoundKeyActsAsItsOwnAgent(t *testing.T) {
	t.Parallel()
	h, cards := holderRig(t, runtimeKey("refund-agent"))

	assertOutcome(t, propose(t, h, "REFUND_ORDER", "refund-agent"), http.StatusOK, "allowed", nil)
	if got := cards.drawn(); len(got) != 1 || got[0] != "refund-agent" {
		t.Errorf("card drawn for %v, want [refund-agent]", got)
	}
}

func TestHolder_BoundKeyCannotActAsAnotherAgent(t *testing.T) {
	t.Parallel()
	h, cards := holderRig(t, runtimeKey("refund-agent"))

	// On the card action and on the action that needs no card: a
	// binding that held only where a card is drawn would not be one.
	for _, action := range []string{"REFUND_ORDER", "ISSUE_CREDIT"} {
		assertOutcome(t, propose(t, h, action, "other-agent"), http.StatusOK, "blocked", []string{"actor_not_key_holder"})
	}
	if got := cards.drawn(); len(got) != 0 {
		t.Errorf("another agent's card was consulted for %v; the refusal must come first", got)
	}
}

// the agent-as-unit design: a key bound to an agent carries the agent's identity, so a
// proposal without actor_id acts as that agent (and draws on its card).
// An unbound key has no identity to lend and is still refused by the
// shape check before any card is looked at.
func TestHolder_MissingActorIsTheBoundKeysAgent(t *testing.T) {
	t.Parallel()
	h, cards := holderRig(t, runtimeKey("refund-agent"))
	assertOutcome(t, propose(t, h, "REFUND_ORDER", ""), http.StatusOK, "allowed", nil)
	if got := cards.drawn(); len(got) != 1 || got[0] != "refund-agent" {
		t.Errorf("card drawn for %v, want [refund-agent]", got)
	}

	unbound, cards := holderRig(t, runtimeKey(""))
	assertOutcome(t, propose(t, unbound, "REFUND_ORDER", ""), http.StatusBadRequest, "invalid", []string{"missing_actor_id"})
	if got := cards.drawn(); len(got) != 0 {
		t.Errorf("a card was drawn for a proposal naming no actor: %v", got)
	}
}

func TestHolder_UnboundKeyIsRefusedOnACardAction(t *testing.T) {
	t.Parallel()
	h, cards := holderRig(t, runtimeKey(""))

	assertOutcome(t, propose(t, h, "REFUND_ORDER", "refund-agent"), http.StatusOK, "blocked", []string{"key_not_bound_to_agent"})
	if got := cards.drawn(); len(got) != 0 {
		t.Errorf("an unbound key drew on the card of %v", got)
	}
}

// Nothing that works today on an action needing no card changes.
func TestHolder_UnboundKeyStillActsWhereNoCardIsRequired(t *testing.T) {
	t.Parallel()
	h, _ := holderRig(t, runtimeKey(""))

	assertOutcome(t, propose(t, h, "ISSUE_CREDIT", "anyone"), http.StatusOK, "allowed", nil)
}

// The card page's test request is the signed-in owner exercising the
// card they issued: an operator path, not a runtime key, so it is not
// held to a key binding.
func TestHolder_OwnerSessionMayTestACard(t *testing.T) {
	t.Parallel()
	h, cards := holderRig(t, auth.Identity{
		Subject: "user_owner", Method: auth.MethodSession, Roles: []string{"admin"},
	})

	assertOutcome(t, propose(t, h, "REFUND_ORDER", "refund-agent"), http.StatusOK, "allowed", nil)
	if got := cards.drawn(); len(got) != 1 || got[0] != "refund-agent" {
		t.Errorf("card drawn for %v, want [refund-agent]", got)
	}
}

// The idempotency cache answers a retry before the decide path runs,
// so the holder rule has to hold there too: replaying an allowed card
// proposal's id from a key that cannot speak for its actor must not
// return the recorded allowed, and a refused replay must not become
// the cached answer for the rightful agent's own retry.
func TestHolder_ReplayedIDDoesNotBypassTheBinding(t *testing.T) {
	t.Parallel()
	serve, cards := holderRigAs(t)
	const id = "refund-o1-stable"

	assertOutcome(t, proposeWithID(t, serve(runtimeKey("refund-agent")), id, "REFUND_ORDER", "refund-agent"),
		http.StatusOK, "allowed", nil)

	// An unbound key replaying the same id and payload.
	assertOutcome(t, proposeWithID(t, serve(runtimeKey("")), id, "REFUND_ORDER", "refund-agent"),
		http.StatusOK, "blocked", []string{"key_not_bound_to_agent"})
	// A key bound to another agent, naming the holder…
	assertOutcome(t, proposeWithID(t, serve(runtimeKey("other-agent")), id, "REFUND_ORDER", "refund-agent"),
		http.StatusOK, "blocked", []string{"actor_not_key_holder"})
	// …and naming itself, which passes the binding but is not the
	// decision that was recorded for this id.
	rec := proposeWithID(t, serve(runtimeKey("other-agent")), id, "REFUND_ORDER", "other-agent")
	assertOutcome(t, rec, rec.Code, "invalid", []string{"idempotency_key_reuse"})

	if got := cards.drawn(); len(got) != 1 || got[0] != "refund-agent" {
		t.Errorf("cards drawn %v; only the first, rightful request may draw", got)
	}

	// The rightful agent's retry is still idempotent: the recorded
	// allowed, not a refusal someone else's replay left behind, and
	// no second draw.
	rec = proposeWithID(t, serve(runtimeKey("refund-agent")), id, "REFUND_ORDER", "refund-agent")
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)
	if !strings.Contains(rec.Body.String(), `"deduplicated":true`) {
		t.Errorf("the rightful retry should be answered from the cache: %s", rec.Body.String())
	}
	if got := cards.drawn(); len(got) != 1 {
		t.Errorf("the retry drew again: %v", got)
	}
}

// A card can cover an action that does not require one, and drawing on
// it is as much spending that card as on a required action. So an
// unbound key naming the holder of such a card is refused before the
// draw; the holder's own key still draws.
func TestHolder_UnboundKeyCannotDrawOnACardWhereOneIsOptional(t *testing.T) {
	t.Parallel()
	serve, cards := holderRigAs(t)

	assertOutcome(t, propose(t, serve(runtimeKey("")), "ISSUE_CREDIT", "credit-agent"),
		http.StatusOK, "blocked", []string{"key_not_bound_to_agent"})
	if got := cards.drawn(); len(got) != 0 {
		t.Fatalf("an unbound key drew on the card of %v", got)
	}

	assertOutcome(t, propose(t, serve(runtimeKey("credit-agent")), "ISSUE_CREDIT", "credit-agent"),
		http.StatusOK, "allowed", nil)
	if got := cards.drawn(); len(got) != 1 || got[0] != "credit-agent" {
		t.Errorf("card drawn for %v, want [credit-agent]", got)
	}
}

// The refusal is not cached as the answer for that proposal id: the
// holder's own retry of the same id is decided, and draws.
func TestHolder_OptionalCardRefusalDoesNotPoisonTheHoldersRetry(t *testing.T) {
	t.Parallel()
	serve, cards := holderRigAs(t)
	const id = "credit-o1-stable"

	assertOutcome(t, proposeWithID(t, serve(runtimeKey("")), id, "ISSUE_CREDIT", "credit-agent"),
		http.StatusOK, "blocked", []string{"key_not_bound_to_agent"})
	assertOutcome(t, proposeWithID(t, serve(runtimeKey("credit-agent")), id, "ISSUE_CREDIT", "credit-agent"),
		http.StatusOK, "allowed", nil)
	if got := cards.drawn(); len(got) != 1 || got[0] != "credit-agent" {
		t.Errorf("card drawn for %v, want [credit-agent]", got)
	}
}

// The reverse order: the holder's bound key is allowed first under a
// stable id, then an unbound key replays that id, actor and payload on
// an optional-card action. The cache must not hand it the holder's
// allowed; the check runs before the cache, as for required actions.
func TestHolder_UnboundReplayOfAnOptionalCardDecisionIsRefused(t *testing.T) {
	t.Parallel()
	serve, cards := holderRigAs(t)
	const id = "credit-o1-bound-first"

	assertOutcome(t, proposeWithID(t, serve(runtimeKey("credit-agent")), id, "ISSUE_CREDIT", "credit-agent"),
		http.StatusOK, "allowed", nil)
	rec := proposeWithID(t, serve(runtimeKey("")), id, "ISSUE_CREDIT", "credit-agent")
	assertOutcome(t, rec, http.StatusOK, "blocked", []string{"key_not_bound_to_agent"})
	if strings.Contains(rec.Body.String(), `"deduplicated":true`) {
		t.Errorf("the unbound replay was answered from the cache: %s", rec.Body.String())
	}
	if got := cards.drawn(); len(got) != 1 {
		t.Errorf("cards drawn %v; only the holder's own request may draw", got)
	}

	// The holder's retry is still the cached allowed.
	rec = proposeWithID(t, serve(runtimeKey("credit-agent")), id, "ISSUE_CREDIT", "credit-agent")
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)
	if !strings.Contains(rec.Body.String(), `"deduplicated":true`) {
		t.Errorf("the holder's retry should be answered from the cache: %s", rec.Body.String())
	}
}
