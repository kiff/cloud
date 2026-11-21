package handlers_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/permit"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/templates"
	"github.com/kiff/cloud/api/internal/tenant"
)

// the execution-permits design at the decide path: an allowed call gets its one permit, an
// operation is bound to its exact call at its first decision whatever the
// outcome, and a held call's release must be the call that was held.

// heldCards holds every request until approve is called, then allows it
// and reports the covering Card. It counts draws.
type heldCards struct {
	mu       sync.Mutex
	hold     bool
	approved bool
	draws    int
}

func (c *heldCards) Covers(context.Context, string, mandate.Request) (bool, error) { return true, nil }

func (c *heldCards) Authorize(_ context.Context, _, _ string, _ mandate.Request) (mandate.Decision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hold && !c.approved {
		return mandate.Decision{Outcome: mandate.OutcomeOwnerRequired, ExceptionID: "exc-1"}, nil
	}
	c.draws++
	return mandate.Decision{Outcome: mandate.OutcomeWithinLimit, Covering: []string{"card-refunds"}, Consumed: true}, nil
}

func permitRig(t *testing.T, issuer *permit.Issuer, cards *heldCards) http.Handler {
	t.Helper()
	return permitRigLocked(t, issuer, cards, &memLocker{})
}

// permitRigLocked is permitRig with the given locker; nil installs none.
func permitRigLocked(t *testing.T, issuer *permit.Issuer, cards *heldCards, locker handlers.ProposalLocker) http.Handler {
	t.Helper()
	ctx := context.Background()
	tmpl, err := templates.Get("card-refund")
	if err != nil {
		t.Fatal(err)
	}
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-permit", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatal(err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, tmpl.YAML); err != nil {
		t.Fatal(err)
	}
	rt := cloudruntime.New(cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry()))
	resolved, err := rt.For(ctx, tnt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolved.IngestRaw(ctx, kiffadapter.RawInput{
		ID: "paid-o1", Adapter: "cloud", Type: "ORDER_PAID", Source: "test",
		ReceivedAt: time.Now().UTC(), EntityID: "o1", EntityType: "Order",
		ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-o1"}, Payload: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	opts := []handlers.ProposalsOption{handlers.WithProposalsMandates(cards)}
	if locker != nil {
		opts = append(opts, handlers.WithProposalsLocker(locker))
	}
	if issuer != nil {
		opts = append(opts, handlers.WithProposalsPermits(issuer))
	}
	inner := http.NewServeMux()
	handlers.NewProposalsHandler(rt, opts...).Register(inner)
	id := runtimeKey("refund-agent")
	id.Tenant = tnt.ID
	return withFakeIdentity(inner, id)
}

func testIssuer(t *testing.T) *permit.Issuer {
	t.Helper()
	k, err := permit.ParseKey("k1:" + base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	targets := permit.NewMemoryTargets()
	// Every tenant the rigs create gets the same target, as an owner would
	// set it when connecting the tool in verify mode.
	return &permit.Issuer{Key: k, Store: permit.NewMemoryStore(), Iss: "https://api.kiff.dev", Targets: anyTenantTargets{targets}}
}

type decideResult struct {
	Outcome        string     `json:"outcome"`
	Reasons        []string   `json:"reasons"`
	Permit         string     `json:"permit"`
	Deduplicated   bool       `json:"deduplicated"`
	PermitIssuedAt *time.Time `json:"permit_issued_at"`
}

// anyTenantTargets answers every tenant with one target for REFUND_ORDER.
type anyTenantTargets struct{ *permit.MemoryTargets }

func (anyTenantTargets) Get(_ context.Context, _, dom, action string) (permit.Target, bool, error) {
	if dom != refundTarget.Domain || action != "REFUND_ORDER" {
		return permit.Target{}, false, nil
	}
	return refundTarget, true, nil
}

var refundTarget = permit.Target{Domain: "card-refund", Action: "REFUND_ORDER", Tool: "refund_order",
	Audience: "https://refunds.example.com/mcp", AmountArg: "amount_cents"}

func decideWithPermit(t *testing.T, h http.Handler, id string, args string) decideResult {
	t.Helper()
	var a map[string]any
	_ = json.Unmarshal([]byte(args), &a)
	params := map[string]any{}
	if v, ok := a["amount_cents"]; ok {
		params["amount_cents"] = v
	}
	return decideAs(t, h, id, "REFUND_ORDER", params, map[string]any{"arguments": json.RawMessage(args)})
}

func decideAs(t *testing.T, h http.Handler, id, action string, params map[string]any, pr map[string]any) decideResult {
	t.Helper()
	body := map[string]any{
		"id": id, "entity_id": "o1", "entity_type": "Order", "action_name": action,
		"actor_id": "refund-agent", "parameters": params, "permit": pr,
	}
	rr := drivePost(t, h, "/v1/proposals/decide", body)
	var r decideResult
	if err := json.Unmarshal(rr.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode %d %s", rr.Code, rr.Body.String())
	}
	return r
}

func permitClaims(t *testing.T, tok string, pub ed25519.PublicKey) permit.Claims {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWS: %q", tok)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		t.Fatal("permit signature does not verify")
	}
	var c permit.Claims
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(payload, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

const refundArgs = `{"order_number":"1042","amount_cents":500,"idempotency_key":"op-1"}`

func TestPermit_AllowedCallGetsItsOnePermit(t *testing.T) {
	t.Parallel()
	is := testIssuer(t)
	cards := &heldCards{}
	h := permitRig(t, is, cards)

	r := decideWithPermit(t, h, "op-1", refundArgs)
	if r.Outcome != "allowed" || r.Permit == "" {
		t.Fatalf("first decision: %+v", r)
	}
	c := permitClaims(t, r.Permit, is.Key.Public().Pub)
	want, _ := permit.ArgsSHA256(json.RawMessage(refundArgs))
	d := c.Details[0]
	if c.Sub != "refund-agent" || c.Aud != "https://refunds.example.com/mcp" || d.Op != "op-1" ||
		d.Tool != "refund_order" || d.ArgsSHA256 != want || d.Card != "card-refunds" {
		t.Fatalf("claims: %+v %+v", c, d)
	}

	// The same call again: the decision stands, no second permit, no
	// second draw.
	again := decideWithPermit(t, h, "op-1", ` {"amount_cents":500.0, "idempotency_key":"op-1","order_number":"1042"}`)
	if again.Outcome != "allowed" || again.Permit != "" || !again.Deduplicated || again.PermitIssuedAt == nil ||
		len(again.Reasons) != 1 || again.Reasons[0] != "permit_already_issued" {
		t.Fatalf("second decision: %+v", again)
	}
	if cards.draws != 1 {
		t.Fatalf("drew %d times", cards.draws)
	}
}

// The gateway's read of a call's unit is a fact it adds beside the governed
// argument, not one of the arguments the permit signs (kiff/tafs#138): KIFF
// checks the permit without it. Before, a call the gateway refused for its
// currency was recorded as invalid (permit_parameters_mismatch), not as
// blocked for its currency: the owner's record had no reason they could
// read, and no receipt.
func TestPermit_TheGatewaysUnitIsNotAGovernedArgument(t *testing.T) {
	t.Parallel()
	h := permitRig(t, testIssuer(t), &heldCards{})
	r := decideAs(t, h, "op-1", "REFUND_ORDER", map[string]any{"amount_cents": 500, handlers.GatewayUnitParam: "eur"},
		map[string]any{"arguments": json.RawMessage(refundArgs)})
	if r.Outcome != "allowed" || r.Permit == "" {
		t.Fatalf("decision: %+v", r)
	}
}

func TestPermit_ReusedOperationWithOtherArgumentsIsRefused(t *testing.T) {
	t.Parallel()
	cards := &heldCards{}
	h := permitRig(t, testIssuer(t), cards)
	if r := decideWithPermit(t, h, "op-1", refundArgs); r.Outcome != "allowed" {
		t.Fatalf("first: %+v", r)
	}
	// Same amount (so the same Card fingerprint), another order.
	r := decideWithPermit(t, h, "op-1", `{"order_number":"9999","amount_cents":500,"idempotency_key":"op-1"}`)
	if r.Outcome != "invalid" || r.Permit != "" || len(r.Reasons) != 1 || r.Reasons[0] != "idempotency_key_reuse" {
		t.Fatalf("reuse: %+v", r)
	}
	if cards.draws != 1 {
		t.Fatalf("drew %d times", cards.draws)
	}
}

// the execution-permits design "Done when": a held call released with different arguments
// gets no permit; released as held, it gets one.
func TestPermit_HeldCallReleasedWithOtherArgumentsGetsNoPermit(t *testing.T) {
	t.Parallel()
	is := testIssuer(t)
	cards := &heldCards{hold: true}
	h := permitRig(t, is, cards)

	if r := decideWithPermit(t, h, "op-1", refundArgs); r.Outcome != "owner_required" || r.Permit != "" {
		t.Fatalf("held: %+v", r)
	}
	cards.mu.Lock()
	cards.approved = true
	cards.mu.Unlock()

	swapped := decideWithPermit(t, h, "op-1", `{"order_number":"9999","amount_cents":500,"idempotency_key":"op-1"}`)
	if swapped.Outcome != "invalid" || swapped.Permit != "" || swapped.Reasons[0] != "idempotency_key_reuse" {
		t.Fatalf("release with other arguments: %+v", swapped)
	}
	released := decideWithPermit(t, h, "op-1", refundArgs)
	if released.Outcome != "allowed" || released.Permit == "" {
		t.Fatalf("release: %+v", released)
	}
	want, _ := permit.ArgsSHA256(json.RawMessage(refundArgs))
	if got := permitClaims(t, released.Permit, is.Key.Public().Pub).Details[0].ArgsSHA256; got != want {
		t.Fatalf("permit for %s, want the held call %s", got, want)
	}
}

func TestPermit_RefusedWhenPermitsAreOff(t *testing.T) {
	t.Parallel()
	cards := &heldCards{}
	h := permitRig(t, nil, cards)
	r := decideWithPermit(t, h, "op-1", refundArgs)
	if r.Outcome != "blocked" || r.Permit != "" || r.Reasons[0] != "permits_unavailable" {
		t.Fatalf("permits off: %+v", r)
	}
	if cards.draws != 0 {
		t.Fatal("a call that cannot get its permit drew on the Card")
	}
}

// a review (non-blocking, at 2180dea): with no locker installed,
// a permit request is refused as when the lock fails, not run unlocked.
func TestPermit_RefusedWithoutALocker(t *testing.T) {
	t.Parallel()
	is := testIssuer(t)
	cards := &heldCards{}
	h := permitRigLocked(t, is, cards, nil)
	r := decideWithPermit(t, h, "op-1", refundArgs)
	if r.Outcome != "blocked" || r.Permit != "" || len(r.Reasons) != 1 || r.Reasons[0] != "permits_unavailable" {
		t.Fatalf("decision: %+v", r)
	}
	if cards.draws != 0 {
		t.Fatalf("drew %d times", cards.draws)
	}
}

func TestPermit_RequestNeedsAnIDAndAnObject(t *testing.T) {
	t.Parallel()
	h := permitRig(t, testIssuer(t), &heldCards{})
	if r := decideWithPermit(t, h, "op-1", `[1,2]`); r.Outcome != "invalid" || r.Reasons[0] != "invalid_permit_request" {
		t.Fatalf("array arguments: %+v", r)
	}
	body := map[string]any{"entity_id": "o1", "entity_type": "Order", "action_name": "REFUND_ORDER",
		"actor_id": "refund-agent", "parameters": map[string]any{"amount_cents": 500},
		"permit": map[string]any{"arguments": map[string]any{"amount_cents": 500}}}
	rr := drivePost(t, h, "/v1/proposals/decide", body)
	if !strings.Contains(rr.Body.String(), "permit_needs_proposal_id") {
		t.Fatalf("no id: %s", rr.Body.String())
	}
}

// a review, P1: the permit must sign the call the Card evaluated.
// On a FIRST request (no earlier binding), a caller must not get the Card
// to judge one amount while the permit carries another, nor point the
// permit at another tool or audience, nor get one for an action that is
// not connected in verify or relay mode. Nothing is drawn.
func TestPermit_FirstRequestMustBeTheCallTheCardJudges(t *testing.T) {
	t.Parallel()
	cards := &heldCards{}
	h := permitRig(t, testIssuer(t), cards)
	cases := []struct {
		name   string
		action string
		params map[string]any
		pr     map[string]any
		reason string
	}{
		{"amount judged 1, signed 50000", "REFUND_ORDER", map[string]any{"amount_cents": 1},
			map[string]any{"arguments": json.RawMessage(`{"order_number":"1042","amount_cents":50000}`)}, "permit_parameters_mismatch"},
		{"governed argument missing from what the Card reads", "REFUND_ORDER", map[string]any{},
			map[string]any{"arguments": json.RawMessage(`{"order_number":"1042","amount_cents":50000}`)}, "permit_parameters_mismatch"},
		{"another upstream tool", "REFUND_ORDER", map[string]any{"amount_cents": 500},
			map[string]any{"tool": "delete_account", "arguments": json.RawMessage(`{"amount_cents":500}`)}, "permit_target_mismatch"},
		{"another audience", "REFUND_ORDER", map[string]any{"amount_cents": 500},
			map[string]any{"audience": "https://evil.example/mcp", "arguments": json.RawMessage(`{"amount_cents":500}`)}, "permit_target_mismatch"},
		{"an action with no verify or relay connection", "ISSUE_CREDIT", map[string]any{"amount_cents": 500},
			map[string]any{"arguments": json.RawMessage(`{"amount_cents":500}`)}, "permit_not_configured"},
	}
	for i, c := range cases {
		r := decideAs(t, h, "op-first-"+string(rune('a'+i)), c.action, c.params, c.pr)
		if r.Permit != "" || len(r.Reasons) == 0 || r.Reasons[0] != c.reason {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
	if cards.draws != 0 {
		t.Fatalf("drew %d times for calls that got no permit", cards.draws)
	}
}

// The signed target is the configured one even when the request names
// nothing, and the signed hash covers the amount the Card judged.
func TestPermit_SignsTheConfiguredTarget(t *testing.T) {
	t.Parallel()
	is := testIssuer(t)
	h := permitRig(t, is, &heldCards{})
	args := `{"order_number":"1042","amount_cents":500,"idempotency_key":"op-t"}`
	r := decideAs(t, h, "op-t", "REFUND_ORDER", map[string]any{"amount_cents": 500},
		map[string]any{"arguments": json.RawMessage(args)})
	if r.Permit == "" {
		t.Fatalf("%+v", r)
	}
	d := permitClaims(t, r.Permit, is.Key.Public().Pub)
	if d.Aud != refundTarget.Audience || d.Details[0].Tool != refundTarget.Tool {
		t.Fatalf("signed %s / %s", d.Aud, d.Details[0].Tool)
	}
}

// switchTargets is a target store the test can fill later: a tool that
// starts sealed and is moved to verify.
type switchTargets struct {
	mu sync.Mutex
	on bool
	// domain, when set, is the domain the target belongs to instead of
	// the rig's own (card-refund).
	domain string
}

func (s *switchTargets) set(on bool) { s.mu.Lock(); s.on = on; s.mu.Unlock() }

func (s *switchTargets) Get(_ context.Context, _, dom, action string) (permit.Target, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := refundTarget.Domain
	if s.domain != "" {
		want = s.domain
	}
	if !s.on || dom != want || action != "REFUND_ORDER" {
		return permit.Target{}, false, nil
	}
	t := refundTarget
	t.Domain = want
	return t, true, nil
}
func (s *switchTargets) Replace(context.Context, string, []permit.Target, string, time.Time) error {
	return nil
}
func (s *switchTargets) List(context.Context, string) ([]permit.Target, error) { return nil, nil }

// realCardRig decides through the real mandate authorizer: a Card with a
// 1000-cent per-call limit that sends larger calls to its owner.
func realCardRig(t *testing.T, targets *switchTargets, clock *time.Time) (http.Handler, *mandate.MemoryStore, string, *permit.Issuer) {
	t.Helper()
	return realCardRigWith(t, targets, clock, nil, nil)
}

// pausingCards is the real authorizer with hooks on Decided: it can pause
// the first call, or fail calls, so a test can drive concurrent and failed
// late-binding checks.
type pausingCards struct {
	*mandate.Authorizer
	mu      sync.Mutex
	pause   chan struct{} // closed by the test to let a paused call go on
	paused  chan struct{} // closed when the first call pauses
	once    sync.Once
	failing int // the next n calls fail
}

func (p *pausingCards) Decided(ctx context.Context, tenantID, proposalID string) (bool, error) {
	p.mu.Lock()
	if p.failing > 0 {
		p.failing--
		p.mu.Unlock()
		return false, errors.New("store unavailable")
	}
	p.mu.Unlock()
	if p.pause != nil {
		first := false
		p.once.Do(func() { first = true; close(p.paused) })
		if first {
			<-p.pause
		}
	}
	return p.Authorizer.Decided(ctx, tenantID, proposalID)
}

// memLocker serializes per key, as the advisory locker does.
type memLocker struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (l *memLocker) Acquire(_ context.Context, key string) (func(), error) {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[string]*sync.Mutex{}
	}
	k, ok := l.m[key]
	if !ok {
		k = &sync.Mutex{}
		l.m[key] = k
	}
	l.mu.Unlock()
	k.Lock()
	var once sync.Once
	return func() { once.Do(k.Unlock) }, nil
}

func realCardRigWith(t *testing.T, targets *switchTargets, clock *time.Time, wrap func(*mandate.Authorizer) handlers.MandateAuthorizer,
	locker handlers.ProposalLocker) (http.Handler, *mandate.MemoryStore, string, *permit.Issuer) {
	t.Helper()
	ctx := context.Background()
	tmpl, _ := templates.Get("card-refund")
	ts := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-real-card", Status: tenant.StatusActive}
	_ = ts.Create(ctx, tnt)
	src := domain.NewMemorySource()
	_ = src.Save(ctx, tnt.ID, tmpl.YAML)
	rt := cloudruntime.New(cloudruntime.NewMemoryResolver(ts, src, executors.DefaultRegistry()))
	resolved, _ := rt.For(ctx, tnt.ID)
	_, _ = resolved.IngestRaw(ctx, kiffadapter.RawInput{ID: "paid-o1", Adapter: "cloud", Type: "ORDER_PAID", Source: "test",
		ReceivedAt: time.Now().UTC(), EntityID: "o1", EntityType: "Order", ActorID: "a",
		Metadata: kiffevent.Metadata{TraceID: "t-o1"}, Payload: map[string]any{}})
	store := mandate.NewMemoryStore()
	if err := store.Issue(ctx, mandate.Mandate{ID: "card-r", TenantID: tnt.ID, Subject: "refund-agent", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_ORDER", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, Max: 1000}}}}}, "owner"); err != nil {
		t.Fatal(err)
	}
	is := testIssuer(t)
	is.Targets = targets
	var auth handlers.MandateAuthorizer = mandate.NewAuthorizer(store, nil, nil)
	if wrap != nil {
		auth = wrap(mandate.NewAuthorizer(store, nil, nil))
	}
	opts := []handlers.ProposalsOption{handlers.WithProposalsMandates(auth),
		handlers.WithProposalsPermits(is), handlers.WithProposalsPermitTargets(targets),
		handlers.WithProposalsClock(func() time.Time { return *clock })}
	if locker == nil {
		locker = &memLocker{}
	}
	opts = append(opts, handlers.WithProposalsLocker(locker))
	m := http.NewServeMux()
	handlers.NewProposalsHandler(rt, opts...).Register(m)
	id := runtimeKey("refund-agent")
	id.Tenant = tnt.ID
	return withFakeIdentity(m, id), store, tnt.ID, is
}

// a review (P1 at 879f1dc), sealed then verify: an operation held,
// approved once and released while its tool was sealed must not, once the
// tool is in verify mode, get a permit for another call. The approval was
// already redeemed; this request consumes nothing.
func TestPermit_RedeemedSealedOperationGetsNoPermitForAnotherCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	targets := &switchTargets{}
	clock := time.Now()
	h, store, tenantID, _ := realCardRig(t, targets, &clock)

	params := map[string]any{"amount_cents": 5000}
	body := map[string]any{"id": "op-1", "entity_id": "o1", "entity_type": "Order", "action_name": "REFUND_ORDER",
		"actor_id": "refund-agent", "parameters": params}
	var held struct {
		Outcome   string `json:"outcome"`
		Exception *struct {
			ID string `json:"id"`
		} `json:"exception"`
	}
	_ = json.Unmarshal(drivePost(t, h, "/v1/proposals/decide", body).Body.Bytes(), &held)
	if held.Outcome != "owner_required" || held.Exception == nil {
		t.Fatalf("held: %+v", held)
	}
	if _, err := store.ApproveOnce(ctx, tenantID, held.Exception.ID, "owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	if rr := drivePost(t, h, "/v1/proposals/decide", body); !strings.Contains(rr.Body.String(), `"outcome":"allowed"`) {
		t.Fatalf("sealed release: %s", rr.Body.String())
	}

	targets.set(true) // the owner moves the tool to verify
	clock = clock.Add(25 * time.Hour)
	r := decideAs(t, h, "op-1", "REFUND_ORDER", params, map[string]any{
		"arguments": json.RawMessage(`{"order_number":"9999","amount_cents":5000,"idempotency_key":"op-1"}`)})
	if r.Permit != "" {
		t.Fatalf("a permit was minted for a call the owner never approved: %+v", r)
	}
	// Refused before evaluation (the operation was already decided when it
	// was first bound), or, were it bound earlier, by the consumed rule.
	if len(r.Reasons) == 0 || (r.Reasons[0] != "permit_binding_late" && r.Reasons[len(r.Reasons)-1] != "permit_not_consumed") {
		t.Fatalf("reasons: %+v", r)
	}
}

// Once a tool is in verify mode, its action is decided only with a permit
// request, so every decision of it records its binding first.
func TestPermit_TargetedActionNeedsAPermitRequest(t *testing.T) {
	t.Parallel()
	targets := &switchTargets{}
	targets.set(true)
	clock := time.Now()
	h, _, _, _ := realCardRig(t, targets, &clock)
	rr := drivePost(t, h, "/v1/proposals/decide", map[string]any{"id": "op-2", "entity_id": "o1", "entity_type": "Order",
		"action_name": "REFUND_ORDER", "actor_id": "refund-agent", "parameters": map[string]any{"amount_cents": 500}})
	if !strings.Contains(rr.Body.String(), `"permit_required"`) || strings.Contains(rr.Body.String(), `"allowed"`) {
		t.Fatalf("decided without a permit request: %s", rr.Body.String())
	}
}

// After the purge: a minted binding is never purged, so the operation
// never gets a second permit, however old.
func TestPermit_MintedBindingOutlivesThePurge(t *testing.T) {
	t.Parallel()
	targets := &switchTargets{}
	targets.set(true)
	clock := time.Now()
	h, _, _, is := realCardRig(t, targets, &clock)
	args := `{"order_number":"1042","amount_cents":500,"idempotency_key":"op-3"}`
	if r := decideWithPermit(t, h, "op-3", args); r.Permit == "" {
		t.Fatalf("first: %+v", r)
	}
	if _, err := is.Store.Purge(context.Background(), time.Now().Add(365*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(31 * 24 * time.Hour)
	if r := decideWithPermit(t, h, "op-3", args); r.Permit != "" {
		t.Fatalf("a second permit after the purge: %+v", r)
	}
}

// a review at 13cf1f6 (P2): a hold still open when the tool moves
// from sealed to verify has no binding. Its release must not create one
// from its own arguments and get a permit: the owner approved a call
// whose arguments KIFF never bound. Refused, every time, whatever the
// release names.
func TestPermit_HoldOpenAcrossTheMoveToVerifyGetsNoPermit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	targets := &switchTargets{}
	clock := time.Now()
	h, store, tenantID, _ := realCardRig(t, targets, &clock)

	body := map[string]any{"id": "op-1", "entity_id": "o1", "entity_type": "Order", "action_name": "REFUND_ORDER",
		"actor_id": "refund-agent", "parameters": map[string]any{"amount_cents": 5000}}
	var held struct {
		Outcome   string `json:"outcome"`
		Exception *struct {
			ID string `json:"id"`
		} `json:"exception"`
	}
	_ = json.Unmarshal(drivePost(t, h, "/v1/proposals/decide", body).Body.Bytes(), &held)
	if held.Outcome != "owner_required" || held.Exception == nil {
		t.Fatalf("held: %+v", held)
	}
	targets.set(true) // the owner moves the tool to verify
	if _, err := store.ApproveOnce(ctx, tenantID, held.Exception.ID, "owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	for i, args := range []string{
		`{"order_number":"9999","amount_cents":5000,"idempotency_key":"op-1"}`, // another order, same amount
		`{"order_number":"9999","amount_cents":5000,"idempotency_key":"op-1"}`, // and again: the refusal left no binding
		`{"order_number":"1042","amount_cents":5000,"idempotency_key":"op-1"}`, // even the held order
	} {
		r := decideAs(t, h, "op-1", "REFUND_ORDER", map[string]any{"amount_cents": 5000},
			map[string]any{"arguments": json.RawMessage(args)})
		if r.Permit != "" || len(r.Reasons) == 0 || r.Reasons[0] != "permit_binding_late" {
			t.Fatalf("attempt %d: %+v", i, r)
		}
	}
}

// a review at 13cf1f6 (P1): the target belongs to the domain whose
// Cards authorize the action. A same-named action decided in another
// domain, on that domain's Card, never gets the target's permit, and the
// other Card is not drawn for a permit request that names nothing it owns.
func TestPermit_TargetInAnotherDomainIsNotThisActionsTarget(t *testing.T) {
	t.Parallel()
	targets := &switchTargets{domain: "gateway"} // the gateway's REFUND_ORDER
	targets.set(true)
	clock := time.Now()
	h, store, tenantID, _ := realCardRig(t, targets, &clock) // decided in card-refund
	r := decideAs(t, h, "op-x", "REFUND_ORDER", map[string]any{"amount_cents": 500},
		map[string]any{"arguments": json.RawMessage(`{"order_number":"1042","amount_cents":500,"idempotency_key":"op-x"}`)})
	if r.Permit != "" || len(r.Reasons) == 0 || r.Reasons[0] != "permit_not_configured" {
		t.Fatalf("a permit for another domain's tool: %+v", r)
	}
	if draws, _ := store.ProposalDraws(context.Background(), tenantID, "op-x"); len(draws) != 0 {
		t.Fatalf("the Card was drawn: %+v", draws)
	}
	// Without a permit request, the card-refund action is decided as usual:
	// the gateway's target does not make it a permit action here.
	rr := drivePost(t, h, "/v1/proposals/decide", map[string]any{"id": "op-y", "entity_id": "o1", "entity_type": "Order",
		"action_name": "REFUND_ORDER", "actor_id": "refund-agent", "parameters": map[string]any{"amount_cents": 500}})
	if strings.Contains(rr.Body.String(), "permit_required") {
		t.Fatalf("another domain's target applied here: %s", rr.Body.String())
	}
}

// sealedHoldApproved holds op-1 for 5000 while the tool is sealed, moves the
// tool to verify, and approves the hold.
func sealedHoldApproved(t *testing.T, h http.Handler, store *mandate.MemoryStore, tenantID string, targets *switchTargets) {
	t.Helper()
	body := map[string]any{"id": "op-1", "entity_id": "o1", "entity_type": "Order", "action_name": "REFUND_ORDER",
		"actor_id": "refund-agent", "parameters": map[string]any{"amount_cents": 5000}}
	var held struct {
		Outcome   string `json:"outcome"`
		Exception *struct {
			ID string `json:"id"`
		} `json:"exception"`
	}
	_ = json.Unmarshal(drivePost(t, h, "/v1/proposals/decide", body).Body.Bytes(), &held)
	if held.Outcome != "owner_required" || held.Exception == nil {
		t.Fatalf("held: %+v", held)
	}
	targets.set(true)
	if _, err := store.ApproveOnce(context.Background(), tenantID, held.Exception.ID, "owner", time.Now()); err != nil {
		t.Fatal(err)
	}
}

func releaseWith(t *testing.T, h http.Handler, order string) decideResult {
	return decideAs(t, h, "op-1", "REFUND_ORDER", map[string]any{"amount_cents": 5000},
		map[string]any{"arguments": json.RawMessage(`{"order_number":"` + order + `","amount_cents":5000,"idempotency_key":"op-1"}`)})
}

func approvalUnconsumed(t *testing.T, store *mandate.MemoryStore, tenantID string) {
	t.Helper()
	ex, found, err := store.GetException(context.Background(), tenantID, "op-1")
	if err != nil || !found || ex.Status != mandate.ExceptionApproved {
		t.Fatalf("the old approval was used: %+v %v %v", ex.Status, found, err)
	}
}

// a review at da46af8: request A pauses inside the late-binding
// check; request B, naming a substituted order, runs to completion. The
// check runs before anything is written and under the operation's lock,
// so neither gets a permit and the approval is not consumed.
func TestPermit_ConcurrentLateReleasesGetNoPermit(t *testing.T) {
	t.Parallel()
	targets := &switchTargets{}
	clock := time.Now()
	pc := &pausingCards{}
	h, store, tenantID, is := realCardRigWith(t, targets, &clock,
		func(a *mandate.Authorizer) handlers.MandateAuthorizer { pc.Authorizer = a; return pc }, &memLocker{})
	sealedHoldApproved(t, h, store, tenantID, targets)

	pc.pause, pc.paused = make(chan struct{}), make(chan struct{})
	freed := false
	defer func() {
		if !freed {
			close(pc.pause)
		}
	}()
	resA := make(chan decideResult, 1)
	go func() { resA <- releaseWith(t, h, "9999") }()
	<-pc.paused
	resB := make(chan decideResult, 1)
	go func() { resB <- releaseWith(t, h, "8888") }()
	select {
	case b := <-resB:
		t.Fatalf("B completed while A held the operation's lock: %+v", b)
	case <-time.After(200 * time.Millisecond):
	}
	if _, found, _ := is.Store.Get(context.Background(), tenantID, "op-1"); found {
		t.Fatal("a binding was written before the check finished")
	}
	close(pc.pause)
	freed = true
	for name, ch := range map[string]chan decideResult{"A": resA, "B": resB} {
		select {
		case r := <-ch:
			if r.Permit != "" || len(r.Reasons) == 0 || r.Reasons[0] != "permit_binding_late" {
				t.Fatalf("%s: %+v", name, r)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not finish", name)
		}
	}
	approvalUnconsumed(t, store, tenantID)
}

// A failed check writes nothing, so a retry runs the check again.
func TestPermit_FailedLateCheckThenRetryGetsNoPermit(t *testing.T) {
	t.Parallel()
	targets := &switchTargets{}
	clock := time.Now()
	pc := &pausingCards{failing: 0}
	h, store, tenantID, is := realCardRigWith(t, targets, &clock,
		func(a *mandate.Authorizer) handlers.MandateAuthorizer { pc.Authorizer = a; return pc }, &memLocker{})
	sealedHoldApproved(t, h, store, tenantID, targets)
	pc.mu.Lock()
	pc.failing = 1
	pc.mu.Unlock()
	if r := releaseWith(t, h, "9999"); r.Permit != "" || r.Reasons[0] != "permits_unavailable" {
		t.Fatalf("failed check: %+v", r)
	}
	if _, found, _ := is.Store.Get(context.Background(), tenantID, "op-1"); found {
		t.Fatal("a failed check left a binding")
	}
	if r := releaseWith(t, h, "9999"); r.Permit != "" || r.Reasons[0] != "permit_binding_late" {
		t.Fatalf("retry: %+v", r)
	}
	approvalUnconsumed(t, store, tenantID)
}

// A permit minted from the owner's approval names that approval, so a tool
// that must never run without its owner can tell; one a Card's room
// allowed names none.
func TestPermit_NamesTheOwnersApprovalItWasMintedFrom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	targets := &switchTargets{}
	targets.set(true)
	clock := time.Now()
	h, store, tenantID, is := realCardRig(t, targets, &clock)
	pub := is.Key.Public().Pub

	within := decideAs(t, h, "op-in", "REFUND_ORDER", map[string]any{"amount_cents": 500},
		map[string]any{"arguments": json.RawMessage(`{"order_number":"1042","amount_cents":500,"idempotency_key":"op-in"}`)})
	if within.Outcome != "allowed" || within.Permit == "" {
		t.Fatalf("within the Card: %+v", within)
	}
	if d := permitClaims(t, within.Permit, pub).Details[0]; d.Approval != "" {
		t.Fatalf("a call the Card's room allowed names an approval: %+v", d)
	}

	args := json.RawMessage(`{"order_number":"1042","amount_cents":5000,"idempotency_key":"op-big"}`)
	body := map[string]any{"id": "op-big", "entity_id": "o1", "entity_type": "Order", "action_name": "REFUND_ORDER",
		"actor_id": "refund-agent", "parameters": map[string]any{"amount_cents": 5000}, "permit": map[string]any{"arguments": args}}
	var held struct {
		Outcome   string `json:"outcome"`
		Exception *struct {
			ID string `json:"id"`
		} `json:"exception"`
	}
	_ = json.Unmarshal(drivePost(t, h, "/v1/proposals/decide", body).Body.Bytes(), &held)
	if held.Outcome != "owner_required" || held.Exception == nil {
		t.Fatalf("held: %+v", held)
	}
	if _, err := store.ApproveOnce(ctx, tenantID, held.Exception.ID, "owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	released := decideAs(t, h, "op-big", "REFUND_ORDER", map[string]any{"amount_cents": 5000}, map[string]any{"arguments": args})
	if released.Outcome != "allowed" || released.Permit == "" {
		t.Fatalf("released: %+v", released)
	}
	if d := permitClaims(t, released.Permit, pub).Details[0]; d.Approval != held.Exception.ID {
		t.Fatalf("the approved call's permit names %q, not the approval %q", d.Approval, held.Exception.ID)
	}
}
