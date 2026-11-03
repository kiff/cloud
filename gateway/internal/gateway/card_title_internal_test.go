package gateway

import (
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/kiffapi"
)

// A Card with a title is named by it, quoted, with its id and environment
// beside it; a Card without one reads as before.
func TestCardLineNamesACardByItsTitle(t *testing.T) {
	tool := Tool{Name: "issue_refund"}
	grant := []kiffapi.CardGrant{{Action: "issue_refund"}}
	card := kiffapi.AgentCard{AgentID: "a", Cards: []kiffapi.Card{
		{ID: "mdt-live", Grants: grant, Environment: "production", Title: "Refunds on Stripe"},
		{ID: "mdt-plain", Grants: grant, Environment: "test"},
		{ID: "mdt-any", Grants: grant, Title: "Any \"quoted\" name"},
	}}
	line := cardLine(card, tool, true)
	for _, want := range []string{
		`Card "Refunds on Stripe" (mdt-live, production) covers this tool`,
		"Card mdt-plain (test) covers this tool",
		`Card "Any \"quoted\" name" (mdt-any) covers this tool`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("line lacks %s:\n%s", want, line)
		}
	}
}
