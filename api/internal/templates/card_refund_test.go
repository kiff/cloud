package templates_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/templates"
	kiffaction "github.com/kiff/kiff/pkg/kiff/action"
	"github.com/kiff/kiff/pkg/kiff/actor"
)

func TestCardRefundRequiresPositiveAmount(t *testing.T) {
	tmpl, err := templates.Get("card-refund")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := domain.ParseAndBuild(tmpl.YAML, executors.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	contract, ok := parsed.Definition.Actions.Get("REFUND_ORDER")
	if !ok {
		t.Fatal("REFUND_ORDER missing")
	}
	validator := kiffaction.NewDefaultValidator()
	authCtx := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: "support-agent", Method: auth.MethodAPIKey, Tenant: "t1", Roles: []string{"support"},
	})
	for _, tc := range []struct {
		amount  int
		invalid bool
	}{
		{amount: -1, invalid: true},
		{amount: 0, invalid: true},
		{amount: 1},
	} {
		ctx := kiffaction.ActionContext{
			ActionName: "REFUND_ORDER", EntityID: "ord-1", EntityType: "Order",
			CurrentState: "PAID", Actor: actor.Actor{ID: "support-agent", Roles: []string{"support"}},
			Parameters: map[string]any{"amount_cents": tc.amount},
		}
		_, err := validator.Validate(authCtx, ctx, contract, parsed.Policy)
		if tc.invalid && !errors.Is(err, kiffaction.ErrInvalidParameter) {
			t.Errorf("amount %d: expected invalid parameter, got %v", tc.amount, err)
		}
		if !tc.invalid && err != nil {
			t.Errorf("amount %d: unexpected error: %v", tc.amount, err)
		}
	}
}
