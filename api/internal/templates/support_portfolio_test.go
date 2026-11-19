package templates_test

import (
	"encoding/json"
	"testing"

	"github.com/kiff/cloud/api/internal/templates"
	"gopkg.in/yaml.v3"
)

// TestSupportPortfolio keeps the three public examples as three cards
// held by one support agent. Combining the actions into one broad card
// would also combine their balances and revocation, which is a different
// product story from the one the landing page teaches.
func TestSupportPortfolio(t *testing.T) {
	t.Parallel()

	tests := []struct {
		slug, action, quantity string
		limit                  int64
	}{
		{"card-refund", "REFUND_ORDER", "sum(amount_cents)", 500000},
		{"retention-credit", "ISSUE_STORE_CREDIT", "sum(amount_cents)", 200000},
		{"subscription-plan-change", "CHANGE_PLAN", "count", 40},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.slug, func(t *testing.T) {
			t.Parallel()
			tmpl, err := templates.Get(tt.slug)
			if err != nil {
				t.Fatal(err)
			}

			var domain struct {
				Actions []struct {
					Name              string `yaml:"name"`
					RequiresAuthority bool   `yaml:"requires_authority"`
				} `yaml:"actions"`
			}
			if err := yaml.Unmarshal(tmpl.YAML, &domain); err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, action := range domain.Actions {
				if action.Name == tt.action {
					found = true
					if !action.RequiresAuthority {
						t.Fatalf("%s must require an issued card", tt.action)
					}
				}
			}
			if !found {
				t.Fatalf("domain does not declare %s", tt.action)
			}

			var script struct {
				Steps []struct {
					Kind string `json:"kind"`
					Card *struct {
						Subject  string   `json:"subject"`
						Actions  []string `json:"actions"`
						Quantity string   `json:"quantity"`
						Limit    int64    `json:"limit"`
					} `json:"card"`
				} `json:"steps"`
			}
			if err := json.Unmarshal(tmpl.DecisionScript, &script); err != nil {
				t.Fatal(err)
			}
			for _, step := range script.Steps {
				if step.Kind != "issue_card" || step.Card == nil {
					continue
				}
				if step.Card.Subject != "support-agent" {
					t.Fatalf("holder = %q, want support-agent", step.Card.Subject)
				}
				if len(step.Card.Actions) != 1 || step.Card.Actions[0] != tt.action {
					t.Fatalf("actions = %v, want only %s", step.Card.Actions, tt.action)
				}
				if step.Card.Quantity != tt.quantity || step.Card.Limit != tt.limit {
					t.Fatalf("aggregate = %s/%d, want %s/%d", step.Card.Quantity, step.Card.Limit, tt.quantity, tt.limit)
				}
				return
			}
			t.Fatal("decision script does not issue its card")
		})
	}
}
