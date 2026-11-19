package handlers_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// Actions that declare the conjunction condition, alone and alongside
// the broad one. Identical in every other respect, so a difference in
// outcome is a difference in the declared rule and nothing else.
const runContextConjYAML = `
domain: run-context-conj-test
entity: Order
events:
  - ORDER_PLACED
  - ORDER_PAID
states:
  - CREATED
  - PAID
transitions:
  - on: ORDER_PLACED
    from: ""
    to: CREATED
  - on: ORDER_PAID
    from: CREATED
    to: PAID
actions:
  - name: COMMENT_CONJ
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    risk: low
    approval: never
    executor: refund.auto_refund
    run_context:
      on_untrusted_sensitive_read: approval
  - name: COMMENT_CONJ_BLOCK
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    risk: low
    approval: never
    executor: refund.auto_refund
    run_context:
      on_untrusted_sensitive_read: block
  - name: COMMENT_BOTH
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    risk: low
    approval: never
    executor: refund.auto_refund
    run_context:
      on_untrusted_input: approval
      on_untrusted_sensitive_read: block
permissions:
  roles:
    ops_agent:
      - orders.refund
`

func runContextConjSetup(t *testing.T) (http.Handler, *cloudruntime.TenantRuntime, string) {
	t.Helper()
	ctx := context.Background()

	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-rcc", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(runContextConjYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)
	h := handlers.NewProposalsHandler(rt)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{
		Tenant: tnt.ID, Subject: "subj", Roles: []string{"ops_agent"},
	}))

	resolved, err := rt.For(ctx, tnt.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, kind := range []string{"ORDER_PLACED", "ORDER_PAID"} {
		raw := kiffadapter.RawInput{
			ID: kind + "-rcc", Adapter: "cloud", Type: kind, Source: "test",
			ReceivedAt: time.Now().UTC(), EntityID: "o-rc", EntityType: "Order",
			ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-rcc"},
			Payload: map[string]any{},
		}
		if _, err := resolved.IngestRaw(ctx, raw); err != nil {
			t.Fatalf("ingest %s: %v", kind, err)
		}
	}
	return wrapped, rt, tnt.ID
}
