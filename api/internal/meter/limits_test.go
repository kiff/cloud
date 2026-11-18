package meter_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/tenant"
)

// A call that would count past what the plan allows is refused before
// it's sent; reads, calls outside the gateway, Company's paid actions and a
// taf's calls never are (its runs use credits, checked before each starts).
// On Solo, which is tafs only, the account's own agents' counted calls are
// refused. An account with no plan, or whose plan ended, has everything
// stopped.
func TestLimitsRefuseWhatWouldCountPastThePlan(t *testing.T) {
	ctx := context.Background()
	pool := fixture(t)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	tenants := tenant.NewMemoryStore()
	ids := map[string]string{}
	for name, meta := range map[string]map[string]any{
		"trial":       {tenant.MetadataKeyPlan: "trial", tenant.MetadataKeyTrialEndsAt: "2026-10-25T00:00:00Z"},
		"trial-ended": {tenant.MetadataKeyPlan: "trial", tenant.MetadataKeyTrialEndsAt: "2026-10-21T00:00:00Z"},
		"solo":        {tenant.MetadataKeyPlan: "solo"},
		"solo-old":    {tenant.MetadataKeyPlan: "solo", "task_ceiling": float64(40)},
		"company":     {tenant.MetadataKeyPlan: "team", tenant.MetadataKeySeats: float64(1)},
		"internal":    {tenant.MetadataKeyPlan: "internal"},
		"no-trial":    {},
	} {
		id := tenant.NewID()
		ids[name] = id
		if err := tenants.Create(ctx, tenant.Tenant{ID: id, Slug: name, Status: tenant.StatusActive, Metadata: meta}); err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{
			`INSERT INTO cloud_meta.tenants VALUES ($1)`,
			`INSERT INTO cloud_meta.agents VALUES ($1, 'taf', 'taf'), ($1, 'bot', '')`,
			`INSERT INTO gateway.tools VALUES ($1, 'refund', false), ($1, 'list', true)`,
		} {
			if _, err := pool.Exec(ctx, q, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	// counted puts n counted calls on unit for the account, inside the period.
	counted := func(name string, unit meter.Unit, agent string, n int) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.billable_calls (tenant_id, key, agent_id, tool, unit, counted, at)
			SELECT $1, $2 || g, $3, 'refund', $2, true, $4 FROM generate_series(1, $5) g`,
			ids[name], string(unit), agent, now.Add(-time.Hour), n); err != nil {
			t.Fatal(err)
		}
	}
	counted("trial", meter.UnitTask, "taf", 25)
	counted("trial", meter.UnitAction, "bot", 500)
	counted("solo", meter.UnitTask, "taf", 20)
	counted("solo", meter.UnitAction, "bot", 600)
	counted("solo-old", meter.UnitTask, "taf", 20)
	counted("company", meter.UnitAction, "bot", 600)
	counted("internal", meter.UnitTask, "taf", 1000)

	l := meter.NewLimits(tenants, meter.NewPostgres(pool)).WithClock(func() time.Time { return now })
	for name, c := range map[string]struct {
		account, agent, tool, want string
	}{
		"a taf past its tasks goes on":      {"trial", "taf", "refund", ""},
		"a read is never stopped":           {"trial", "taf", "list", ""},
		"a call outside the gateway":        {"trial", "taf", "not_a_gateway_tool", ""},
		"the trial's 500th action":          {"trial", "bot", "refund", meter.ReasonPlanLimitReached},
		"Company's paid actions never stop": {"company", "bot", "refund", ""},
		"Solo's own agent is refused":       {"solo", "bot", "refund", meter.ReasonPlanOwnAgentsCompany},
		"Solo's own agent may read":         {"solo", "bot", "list", ""},
		"Solo past its tasks goes on":       {"solo", "taf", "refund", ""},
		"an old ceiling counts nothing":     {"solo-old", "taf", "refund", ""},
		"an ended trial stops a read":       {"trial-ended", "taf", "list", meter.ReasonPlanTrialEnded},
		"an ended trial stops anything":     {"trial-ended", "bot", "anything", meter.ReasonPlanTrialEnded},
		"internal never stops":              {"internal", "taf", "refund", ""},
		"a trial never started":             {"no-trial", "taf", "list", meter.ReasonPlanNotStarted},
		"an unknown account":                {"nobody", "taf", "refund", ""},
	} {
		account := ids[c.account]
		if account == "" {
			account = tenant.NewID()
		}
		reason, msg, err := l.Refusal(ctx, account, c.agent, c.tool)
		if err != nil || reason != c.want || (reason != "" && !strings.Contains(msg, "Nothing was sent")) {
			t.Errorf("%s: %q %q %v; want %q", name, reason, msg, err, c.want)
		}
	}
}
