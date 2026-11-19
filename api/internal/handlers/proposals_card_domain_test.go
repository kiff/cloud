package handlers_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/mandate"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/templates"
	"github.com/kiff/cloud/api/internal/tenant"
)

// A card scoped to one domain authorizes, and draws, only for
// proposals decided in that domain. These run the real card store and
// evaluator on a tenant with two domains built from the card-refund
// template: "card-refund" (the default) and "card-refund-eu". The
// agent's card is issued for "card-refund" only.

type domainRig struct {
	serve func(auth.Identity) http.Handler
	store *mandate.MemoryStore
	rt    *cloudruntime.TenantRuntime
	src   *domain.MemorySource
	tmpl  []byte
	tnt   string
}

func newDomainRig(t *testing.T) domainRig {
	t.Helper()
	return newDomainRigWith(t, "", nil)
}

// newDomainRigWith issues the card scoped to cardEnv and wires controls,
// when given, as the handler's control store.
func newDomainRigWith(t *testing.T, cardEnv string, controls domain.ControlStore) domainRig {
	t.Helper()
	ctx := context.Background()
	tmpl, err := templates.Get("card-refund")
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	eu := []byte(strings.Replace(string(tmpl.YAML), "domain: card-refund", "domain: card-refund-eu", 1))

	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-card-domain", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, tmpl.YAML); err != nil {
		t.Fatalf("save default: %v", err)
	}
	if err := src.Create(ctx, tnt.ID, eu); err != nil {
		t.Fatalf("create eu: %v", err)
	}
	rt := cloudruntime.New(cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry()))
	// Seed each domain by name only. The unscoped ("") selector resolves
	// the default, card-refund, and must share its runtime and so its
	// state; nothing seeds it separately.
	for _, d := range []string{"card-refund", "card-refund-eu"} {
		resolved, err := rt.ForDomain(ctx, tnt.ID, d)
		if err != nil {
			t.Fatalf("resolve %s: %v", d, err)
		}
		if _, err := resolved.IngestRaw(ctx, kiffadapter.RawInput{
			ID: "paid-o1-" + d, Adapter: "cloud", Type: "ORDER_PAID", Source: "test",
			ReceivedAt: time.Now().UTC(), EntityID: "o1", EntityType: "Order",
			ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-o1-" + d},
			Payload: map[string]any{},
		}); err != nil {
			t.Fatalf("ingest %s: %v", d, err)
		}
	}

	store := mandate.NewMemoryStore()
	if err := store.Issue(ctx, mandate.Mandate{
		ID: "refund-agent-refunds", TenantID: tnt.ID, Subject: "refund-agent", Domain: "card-refund", Environment: cardEnv,
		Grants: []mandate.Grant{{Action: "REFUND_ORDER"}},
		Aggregates: []mandate.Aggregate{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"},
			Limit:    10000, Window: mandate.WindowCalendarDay,
		}},
		ValidFrom: time.Now().Add(-time.Hour),
	}, "owner"); err != nil {
		t.Fatalf("issue: %v", err)
	}

	opts := []handlers.ProposalsOption{handlers.WithProposalsMandates(mandate.NewAuthorizer(store, nil, nil))}
	if controls != nil {
		opts = append(opts, handlers.WithProposalsControlStore(controls))
	}
	h := handlers.NewProposalsHandler(rt, opts...)
	inner := http.NewServeMux()
	h.Register(inner)
	return domainRig{
		serve: func(id auth.Identity) http.Handler {
			id.Tenant = tnt.ID
			return withFakeIdentity(inner, id)
		},
		store: store,
		rt:    rt,
		src:   src,
		tmpl:  tmpl.YAML,
		tnt:   tnt.ID,
	}
}

func (r domainRig) drawn(t *testing.T) int {
	t.Helper()
	draws, err := r.store.Draws(context.Background(), r.tnt, "refund-agent-refunds", time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("draws: %v", err)
	}
	return len(draws)
}

func refundIn(t *testing.T, h http.Handler, domainName string) string {
	t.Helper()
	body := map[string]any{
		"entity_id": "o1", "entity_type": "Order", "action_name": "REFUND_ORDER",
		"actor_id": "refund-agent", "parameters": map[string]any{"amount_cents": 500},
	}
	if domainName != "" {
		body["domain"] = domainName
	}
	rec := drivePost(t, h, "/v1/proposals/decide", body)
	return rec.Body.String()
}

func TestCardDomain_OtherDomainIsNotCovered(t *testing.T) {
	t.Parallel()
	rig := newDomainRig(t)
	key := rig.serve(runtimeKey("refund-agent"))

	// Tenant-wide key selecting the other domain: the card does not
	// reach it, so the action that requires a card has none.
	if body := refundIn(t, key, "card-refund-eu"); !strings.Contains(body, `"no_mandate_issued"`) {
		t.Errorf("domain-B proposal must find no card: %s", body)
	}
	if n := rig.drawn(t); n != 0 {
		t.Fatalf("a domain-A card drew %d for a domain-B proposal", n)
	}

	// The card's own domain still works, explicitly and by default.
	if body := refundIn(t, key, "card-refund"); !strings.Contains(body, `"outcome":"allowed"`) {
		t.Errorf("matching-domain proposal must be allowed: %s", body)
	}
	if body := refundIn(t, key, ""); !strings.Contains(body, `"outcome":"allowed"`) {
		t.Errorf("default-domain proposal (the card's domain) must be allowed: %s", body)
	}
	if n := rig.drawn(t); n != 2 {
		t.Errorf("draws = %d, want 2 (both in the card's domain)", n)
	}
}

func TestCardDomain_DomainBoundKeys(t *testing.T) {
	t.Parallel()
	rig := newDomainRig(t)

	euKey := runtimeKey("refund-agent")
	euKey.DomainID = "card-refund-eu"
	if body := refundIn(t, rig.serve(euKey), ""); !strings.Contains(body, `"no_mandate_issued"`) {
		t.Errorf("a key bound to domain B must not use the domain-A card: %s", body)
	}
	if n := rig.drawn(t); n != 0 {
		t.Fatalf("a domain-B key drew %d on the domain-A card", n)
	}

	homeKey := runtimeKey("refund-agent")
	homeKey.DomainID = "card-refund"
	if body := refundIn(t, rig.serve(homeKey), ""); !strings.Contains(body, `"outcome":"allowed"`) {
		t.Errorf("a key bound to the card's domain must be allowed: %s", body)
	}
	if n := rig.drawn(t); n != 1 {
		t.Errorf("draws = %d, want 1", n)
	}
}

// An unscoped decision is answered by the tenant's default
// domain, so it must see the state of events reported to that domain by
// name, and be stamped with that domain rather than left to the active
// control's. The rig seeds o1 only through "card-refund".
func TestUnscopedDecision_UsesDefaultDomainRuntime(t *testing.T) {
	t.Parallel()
	rig := newDomainRig(t)
	ctx := context.Background()

	byName, err := rig.rt.ForDomain(ctx, rig.tnt, "card-refund")
	if err != nil {
		t.Fatalf("resolve card-refund: %v", err)
	}
	unscoped, err := rig.rt.ForDomain(ctx, rig.tnt, "")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if byName != unscoped {
		t.Fatal("the default-domain selector and the domain's own name resolved two runtimes")
	}

	body := refundIn(t, rig.serve(runtimeKey("refund-agent")), "")
	if !strings.Contains(body, `"outcome":"allowed"`) {
		t.Fatalf("unscoped refund on an order paid in the default domain must be allowed: %s", body)
	}
	assertStampedDomain(t, byName, "card-refund")
}

// A domain replaced in place (its YAML renamed) keeps its first,
// immutable domain_id; its current name is what cards are issued under
// and what an explicit selector names. Production: a tenant whose first
// domain was replaced with the card-refund YAML keeps domain_id
// prod-guardian, and matching cards by that id refused every unscoped
// card decision. An unscoped request must be decided
// exactly as one naming the domain's current name, and both are stamped
// with the immutable id.
func TestUnscopedDecision_ReplacedDefaultDomainMatchesCardsByCurrentName(t *testing.T) {
	t.Parallel()
	rig := newDomainRig(t) // default card-refund; card refund-agent-refunds scoped to it
	ctx := context.Background()

	renamed := []byte(strings.Replace(string(rig.tmpl), "domain: card-refund", "domain: card-refund-v2", 1))
	if err := rig.src.Save(ctx, rig.tnt, renamed); err != nil {
		t.Fatalf("replace default: %v", err)
	}
	rig.rt.ResetTenant(rig.tnt)
	if id, err := rig.rt.DomainIDFor(ctx, rig.tnt, ""); err != nil || id != "card-refund" {
		t.Fatalf("default domain_id after replace = %q, %v; want card-refund (immutable)", id, err)
	}
	// A card issued under the domain's current name, as issuance does.
	if err := rig.store.Issue(ctx, mandate.Mandate{
		ID: "refund-agent-refunds-v2", TenantID: rig.tnt, Subject: "refund-agent", Domain: "card-refund-v2",
		Grants: []mandate.Grant{{Action: "REFUND_ORDER"}},
		Aggregates: []mandate.Aggregate{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"},
			Limit:    10000, Window: mandate.WindowCalendarDay,
		}},
		ValidFrom: time.Now().Add(-time.Hour),
	}, "owner"); err != nil {
		t.Fatalf("issue v2 card: %v", err)
	}
	rt, err := rig.rt.ForDomain(ctx, rig.tnt, "")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if _, err := rt.IngestRaw(ctx, kiffadapter.RawInput{
		ID: "paid-o1-replaced", Adapter: "cloud", Type: "ORDER_PAID", Source: "test",
		ReceivedAt: time.Now().UTC(), EntityID: "o1", EntityType: "Order",
		ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-o1-replaced"},
		Payload: map[string]any{},
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	key := rig.serve(runtimeKey("refund-agent"))
	for _, selector := range []string{"", "card-refund-v2"} {
		if body := refundIn(t, key, selector); !strings.Contains(body, `"outcome":"allowed"`) {
			t.Fatalf("selector %q: the card issued under the current name must apply: %s", selector, body)
		}
	}
	v2, err := rig.store.Draws(ctx, rig.tnt, "refund-agent-refunds-v2", time.Now().Add(-24*time.Hour))
	if err != nil || len(v2) != 2 {
		t.Errorf("current-name card draws = %d (err %v), want 2 (unscoped and named alike)", len(v2), err)
	}
	if n := rig.drawn(t); n != 0 {
		t.Errorf("the old-name card drew %d; it names a domain that no longer exists", n)
	}
	assertStampedDomain(t, rt, "card-refund")
}

func assertStampedDomain(t *testing.T, rt *kiffruntime.Runtime, want string) {
	t.Helper()
	records, err := rt.Audit.Query(context.Background(), kiffaudit.Filter{})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	stamped := false
	for _, rec := range records {
		if rec.Kind != kiffaudit.Kind("proposal_decision") {
			continue
		}
		stamped = true
		if rec.Data["domain_id"] != want {
			t.Errorf("domain_id = %v, want %s (the domain whose runtime decided)", rec.Data["domain_id"], want)
		}
	}
	if !stamped {
		t.Fatal("no proposal_decision audit record recorded")
	}
}

// A request that names no environment is in the environment of the
// control governing its action in the card's domain. A card scoped to
// another environment does not apply to it, and draws nothing; one
// scoped to that environment, or a request naming it, does.
func TestCardEnvironment_UnscopedRequestUsesTheControlEnvironment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	withEnv := func(env string) map[string]any {
		return map[string]any{
			"entity_id": "o1", "entity_type": "Order", "action_name": "REFUND_ORDER",
			"actor_id": "refund-agent", "parameters": map[string]any{"amount_cents": 500}, "environment": env,
		}
	}

	// A production card on a tenant whose card-refund control is dev.
	cs := domain.NewMemoryControlStore()
	rig := newDomainRigWith(t, "production", cs)
	if err := cs.Create(ctx, domain.Control{ID: "ctl-dev", TenantID: rig.tnt, Domain: "card-refund",
		Actions: []string{"REFUND_ORDER"}, Environment: "dev"}); err != nil {
		t.Fatalf("control: %v", err)
	}
	key := rig.serve(runtimeKey("refund-agent"))
	if body := refundIn(t, key, ""); !strings.Contains(body, `"no_mandate_issued"`) {
		t.Errorf("a production card must not apply to a request in dev: %s", body)
	}
	if n := rig.drawn(t); n != 0 {
		t.Fatalf("a production card drew %d for a dev request", n)
	}
	if body := drivePost(t, key, "/v1/proposals/decide", withEnv("production")).Body.String(); !strings.Contains(body, `"outcome":"allowed"`) {
		t.Errorf("a request naming production must be covered by the production card: %s", body)
	}

	// The same card where the governing control is in production.
	prod := domain.NewMemoryControlStore()
	rig2 := newDomainRigWith(t, "production", prod)
	if err := prod.Create(ctx, domain.Control{ID: "ctl-prod", TenantID: rig2.tnt, Domain: "card-refund",
		Actions: []string{"REFUND_ORDER"}, Environment: "production"}); err != nil {
		t.Fatalf("control: %v", err)
	}
	if body := refundIn(t, rig2.serve(runtimeKey("refund-agent")), ""); !strings.Contains(body, `"outcome":"allowed"`) {
		t.Errorf("a production card must apply where the governing control is production: %s", body)
	}

	// No control: the environment is unknown, so the scoped card does not apply.
	rig3 := newDomainRigWith(t, "production", domain.NewMemoryControlStore())
	if body := refundIn(t, rig3.serve(runtimeKey("refund-agent")), ""); !strings.Contains(body, `"no_mandate_issued"`) {
		t.Errorf("an environment-scoped card must not apply when the environment is unknown: %s", body)
	}
}

// A key bound to an environment is decided in it. It reaches the
// card of its own environment without naming it, and can neither leave
// it by naming another nor reach another environment's card that way.
func TestCardEnvironment_KeyBoundEnvironmentIsAuthoritative(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cs := domain.NewMemoryControlStore()
	rig := newDomainRigWith(t, "production", cs) // a production card
	if err := cs.Create(ctx, domain.Control{ID: "ctl-dev", TenantID: rig.tnt, Domain: "card-refund",
		Actions: []string{"REFUND_ORDER"}, Environment: "dev"}); err != nil {
		t.Fatalf("control: %v", err)
	}
	boundTo := func(env string) http.Handler {
		id := runtimeKey("refund-agent")
		id.Environment = env
		return rig.serve(id)
	}
	refund := func(env string) map[string]any {
		body := map[string]any{"entity_id": "o1", "entity_type": "Order", "action_name": "REFUND_ORDER",
			"actor_id": "refund-agent", "parameters": map[string]any{"amount_cents": 500}}
		if env != "" {
			body["environment"] = env
		}
		return body
	}

	prod := boundTo("production")
	if body := refundIn(t, prod, ""); !strings.Contains(body, `"outcome":"allowed"`) {
		t.Errorf("a production-bound key must be decided in production without naming it: %s", body)
	}
	if rec := drivePost(t, prod, "/v1/proposals/decide", refund("Production")); !strings.Contains(rec.Body.String(), `"outcome":"allowed"`) {
		t.Errorf("repeating the bound environment (any case) must be accepted: %d %s", rec.Code, rec.Body.String())
	}
	if rec := drivePost(t, prod, "/v1/proposals/decide", refund("dev")); rec.Code != http.StatusForbidden {
		t.Errorf("a production-bound key naming dev: status %d, want 403; %s", rec.Code, rec.Body.String())
	}

	dev := boundTo("dev")
	if rec := drivePost(t, dev, "/v1/proposals/decide", refund("production")); rec.Code != http.StatusForbidden {
		t.Errorf("a dev-bound key naming production: status %d, want 403; %s", rec.Code, rec.Body.String())
	}
	if body := refundIn(t, dev, ""); !strings.Contains(body, `"no_mandate_issued"`) {
		t.Errorf("a dev-bound key must not reach the production card: %s", body)
	}
	if n := rig.drawn(t); n != 2 {
		t.Errorf("draws = %d, want 2 (the production-bound key's two allowed refunds)", n)
	}
}

// A card issued with its domain's immutable id keeps applying after
// the domain is renamed, to unscoped and named requests alike. A card
// carrying the new name but another domain's id does not apply.
func TestCardWithDomainID_SurvivesARenameOfItsDomain(t *testing.T) {
	t.Parallel()
	rig := newDomainRig(t)
	ctx := context.Background()
	issue := func(id, name, domainID string) {
		t.Helper()
		if err := rig.store.Issue(ctx, mandate.Mandate{
			ID: id, TenantID: rig.tnt, Subject: "refund-agent", Domain: name, DomainID: domainID,
			Grants: []mandate.Grant{{Action: "REFUND_ORDER"}},
			Aggregates: []mandate.Aggregate{{
				Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"},
				Limit:    10000, Window: mandate.WindowCalendarDay,
			}},
			ValidFrom: time.Now().Add(-time.Hour),
		}, "owner"); err != nil {
			t.Fatalf("issue %s: %v", id, err)
		}
	}
	// Revoke the rig's legacy name-matched card so only these two decide.
	if err := rig.store.Revoke(ctx, rig.tnt, "refund-agent-refunds", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("revoke legacy card: %v", err)
	}
	issue("by-id", "card-refund", "card-refund")
	issue("wrong-id", "card-refund-v2", "card-refund-eu")

	renamed := []byte(strings.Replace(string(rig.tmpl), "domain: card-refund", "domain: card-refund-v2", 1))
	if err := rig.src.Save(ctx, rig.tnt, renamed); err != nil {
		t.Fatalf("rename default: %v", err)
	}
	rig.rt.ResetTenant(rig.tnt)
	rt, err := rig.rt.ForDomain(ctx, rig.tnt, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := rt.IngestRaw(ctx, kiffadapter.RawInput{
		ID: "paid-o1-renamed-id", Adapter: "cloud", Type: "ORDER_PAID", Source: "test",
		ReceivedAt: time.Now().UTC(), EntityID: "o1", EntityType: "Order",
		ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-o1-renamed-id"},
		Payload: map[string]any{},
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	key := rig.serve(runtimeKey("refund-agent"))
	for _, selector := range []string{"", "card-refund-v2"} {
		if body := refundIn(t, key, selector); !strings.Contains(body, `"outcome":"allowed"`) {
			t.Fatalf("selector %q: the card issued with the domain's id must still apply: %s", selector, body)
		}
	}
	for id, want := range map[string]int{"by-id": 2, "wrong-id": 0} {
		d, err := rig.store.Draws(ctx, rig.tnt, id, time.Now().Add(-24*time.Hour))
		if err != nil || len(d) != want {
			t.Errorf("%s draws = %d (err %v), want %d", id, len(d), err, want)
		}
	}
}
