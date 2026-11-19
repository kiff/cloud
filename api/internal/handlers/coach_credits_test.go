package handlers_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/permit"
)

// coachRunsStore reads kept runs as meter.Postgres's Runs does: the
// tenant's that ended in [from, to), only runtimeAgent's when set, only
// scope's when set (the subject's own and its agents'), newest first, up to
// limit.
type coachRunsStore struct{ runs []meter.RunEntry }

func (s *coachRunsStore) Runs(_ context.Context, tenantID, runtimeAgent string, scope *meter.TaskScope, from, to time.Time, _ *meter.RunPage, limit int) ([]meter.RunEntry, *meter.RunPage, error) {
	out := []meter.RunEntry{}
	for _, e := range s.runs {
		if e.Tenant != tenantID || e.Ended.Before(from) || !e.Ended.Before(to) || (runtimeAgent != "" && e.RuntimeAgent != runtimeAgent) {
			continue
		}
		if scope != nil && !(e.AskedBy != "" && e.AskedBy == scope.Subject) && !slices.Contains(scope.Agents, e.Agent) {
			continue
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b meter.RunEntry) int {
		if c := b.Ended.Compare(a.Ended); c != 0 {
			return c
		}
		return strings.Compare(b.Run, a.Run)
	})
	if len(out) > limit {
		last := out[limit-1]
		return out[:limit], &meter.RunPage{At: last.Ended, Run: last.Run}, nil
	}
	return out, nil, nil
}

// Coach's credits read answers, for the person the permit names, one
// agent's runs of the last 30 days in credits, newest first, at most 500:
// counted runs with the credits KIFF kept, a trial with what it would have
// used, other free runs with none. Never the AI cost. Someone who may
// neither use nor oversee the agent, or an agent the account doesn't have,
// gets no runs; a permit act refuses is refused the same way.
func TestCoachCreditsReadsOneAgentsRecentRuns(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	key := permit.Key{ID: "k1", Priv: priv}
	members := membership.NewMemoryStore()
	if err := members.Mutate(ctx, "t1", func(tx membership.Tx) error {
		for s, role := range map[string]membership.Role{"ana": membership.RoleApprover, "bo": membership.RoleViewer, "dee": membership.RoleAdmin} {
			if err := tx.InsertMember(membership.Member{TenantID: "t1", Subject: s, Role: role, Status: membership.StatusActive}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	agentStore := agents.NewMemoryStore()
	for _, a := range []agents.Agent{{TenantID: "t1", ID: "coach"}, {TenantID: "t1", ID: "refunds"}, {TenantID: "t1", ID: "busy"},
		{TenantID: "t1", ID: "bills"}, {TenantID: "t2", ID: "ledger"}} {
		a.Kind, a.CreatedAt = agents.KindTaf, now
		if err := agentStore.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	keys := auth.NewMemoryAPIKeyStore()
	if err := keys.Insert(ctx, auth.APIKeyRecord{ID: "k-coach", Tenant: "t1", Subject: "ana", Roles: []string{auth.RoleSetupReader},
		AgentID: "coach", KeyHash: [32]byte{1}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	grants := access.NewMemoryStore()
	for _, g := range []access.Grant{
		{TenantID: "t1", Agent: "refunds", Subject: "ana"},
		{TenantID: "t1", Agent: "busy", Subject: "ana"},
		{TenantID: "t1", Agent: "bills", Subject: "ana"},
		{TenantID: "t2", Agent: "ledger", Subject: "ana"},
	} {
		if err := grants.AddGrant(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	targets := permit.NewMemoryTargets()
	if err := targets.Replace(ctx, "t1", []permit.Target{
		{Domain: "gateway", Action: "taf_estimate", Tool: "taf_estimate", Audience: "https://tafs.example/v1/mcp"},
		{Domain: "gateway", Action: "taf_task", Tool: "taf_task", Audience: "https://tafs.example/v1/mcp"},
	}, "ana", now); err != nil {
		t.Fatal(err)
	}

	// The runs, classified as the ledger keeps them.
	const trialCost, countedCost = 123_456, 30_000
	kept := func(tenantID, agent, run, trigger string, failed bool, ended time.Time, cost int64) meter.RunEntry {
		return meter.ClassifyRun(meter.RunReport{Tenant: tenantID, Run: run, Agent: agent, RuntimeAgent: "taf-" + agent, Trigger: trigger,
			Started: ended.Add(-time.Minute), Ended: ended, CostMicroUSD: cost, Status: "COMPLETED", Failed: failed}, 0)
	}
	store := &coachRunsStore{runs: []meter.RunEntry{
		kept("t1", "refunds", "r-failed", "owner", true, now.Add(-3*time.Hour), 5_000),
		kept("t1", "refunds", "r-counted", "owner", false, now.Add(-time.Hour), countedCost),
		kept("t1", "refunds", "r-trial", "trial", false, now.Add(-2*time.Hour), trialCost),
		kept("t1", "refunds", "r-old", "owner", false, now.Add(-31*24*time.Hour), countedCost),
		kept("t1", "bills", "b-1", "owner", false, now.Add(-time.Hour), countedCost),
		kept("t2", "refunds", "x-1", "owner", false, now.Add(-time.Hour), countedCost),
		kept("t2", "ledger", "l-1", "owner", false, now.Add(-time.Hour), countedCost),
	}}
	for i := range 600 {
		store.runs = append(store.runs, kept("t1", "busy", fmt.Sprintf("busy-%03d", i), "schedule", false, now.Add(-time.Duration(i+1)*time.Minute), 1_000))
	}

	ah := handlers.NewAccessHandler(grants, members, agentStore, nil).WithKeys(keys)
	c := &handlers.CoachCredits{Grants: &handlers.RunGrants{Key: key, Published: []permit.PublicKey{key.Public()}, Iss: "https://api.kiff.dev", Access: ah,
		Now: func() time.Time { return now }}, Targets: targets, Runs: store}
	mux := http.NewServeMux()
	c.Register(mux)

	signedFor := func(tool, sub, aud, tenant string, act *permit.Actor, k permit.Key, exp time.Time) string {
		t.Helper()
		tok, err := k.Sign(permit.Claims{Iss: "https://api.kiff.dev", Aud: aud, Sub: sub, Iat: now.Add(-time.Minute).Unix(), Exp: exp.Unix(), Jti: permit.NewJTI(),
			Details: []permit.Detail{{Type: permit.DetailType, Tenant: tenant, Tool: tool}}, Act: act})
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	signedAt := func(sub, aud, tenant string, act *permit.Actor, k permit.Key, exp time.Time) string {
		return signedFor(handlers.CoachEstimateTool, sub, aud, tenant, act, k, exp)
	}
	const aud = "https://tafs.example/v1/mcp"
	signed := func(sub, aud, tenant string, act *permit.Actor, k permit.Key) string {
		return signedAt(sub, aud, tenant, act, k, now.Add(time.Minute))
	}
	type run struct {
		Run     string `json:"run"`
		Ended   string `json:"ended"`
		Credits int64  `json:"credits"`
		Counted bool   `json:"counted"`
		Free    string `json:"free"`
	}
	ask := func(p, agent string) (int, []run, string) {
		t.Helper()
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(map[string]string{"permit": p, "agent": agent})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/coach/credits", &buf))
		var out struct {
			Runs []run `json:"runs"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.Runs, rec.Body.String()
	}
	anas := signed("coach", aud, "t1", &permit.Actor{Sub: "ana"}, key)

	t.Run("a person who uses the agent reads its runs in credits", func(t *testing.T) {
		code, runs, body := ask(anas, "refunds")
		want := []run{
			{Run: "r-counted", Ended: now.Add(-time.Hour).Format(time.RFC3339), Credits: meter.Credits(countedCost), Counted: true, Free: ""},
			{Run: "r-trial", Ended: now.Add(-2 * time.Hour).Format(time.RFC3339), Credits: meter.Credits(trialCost), Counted: false, Free: meter.FreeTrial},
			{Run: "r-failed", Ended: now.Add(-3 * time.Hour).Format(time.RFC3339), Credits: 0, Counted: false, Free: meter.FreeFailed},
		}
		if code != http.StatusOK || !slices.Equal(runs, want) {
			t.Fatalf("Ana's read of Refunds: %d %s, want %+v", code, body, want)
		}
		if meter.Credits(trialCost) == 0 {
			t.Fatal("the trial's would-be credits are worth asserting")
		}
		// Each run is exactly its five fields: no AI cost, multiplier or rate.
		var raw struct {
			Runs []map[string]any `json:"runs"`
		}
		if err := json.Unmarshal([]byte(body), &raw); err != nil {
			t.Fatal(err)
		}
		for _, r := range raw.Runs {
			fields := []string{}
			for k := range r {
				fields = append(fields, k)
			}
			slices.Sort(fields)
			if !slices.Equal(fields, []string{"counted", "credits", "ended", "free", "run"}) {
				t.Errorf("a run's fields: %v", fields)
			}
		}
		for _, word := range []string{"cost", "micro", "usd", "multiplier", "rate"} {
			if strings.Contains(strings.ToLower(body), word) {
				t.Errorf("the answer says %q: %s", word, body)
			}
		}
		for _, r := range runs {
			if e, err := time.Parse(time.RFC3339, r.Ended); err != nil || !strings.HasSuffix(r.Ended, "Z") || e.Before(now.Add(-30*24*time.Hour)) {
				t.Errorf("ended is RFC 3339 UTC, within 30 days: %q %v", r.Ended, err)
			}
		}
	})

	t.Run("at most 500, newest first", func(t *testing.T) {
		code, runs, _ := ask(anas, "busy")
		if code != http.StatusOK || len(runs) != 500 || runs[0].Run != "busy-000" || runs[499].Run != "busy-499" {
			t.Fatalf("Ana's read of Busy: %d, %d runs", code, len(runs))
		}
		for i := 1; i < len(runs); i++ {
			if runs[i-1].Ended < runs[i].Ended {
				t.Fatalf("not newest first at %d: %s before %s", i, runs[i-1].Ended, runs[i].Ended)
			}
		}
	})

	t.Run("an admin, who oversees the agent, reads it too", func(t *testing.T) {
		if code, runs, body := ask(signed("coach", aud, "t1", &permit.Actor{Sub: "dee"}, key), "refunds"); code != http.StatusOK || len(runs) != 3 {
			t.Errorf("Dee's read of Refunds: %d %s", code, body)
		}
	})

	for name, tc := range map[string]struct{ permit, agent string }{
		"neither uses nor oversees it": {signed("coach", aud, "t1", &permit.Actor{Sub: "bo"}, key), "refunds"},
		"not on the account":           {signed("coach", aud, "t1", &permit.Actor{Sub: "zed"}, key), "refunds"},
		"another account's agent":      {anas, "ledger"},
		"an agent the account hasn't":  {anas, "nobody"},
	} {
		t.Run(name+": no runs", func(t *testing.T) {
			code, runs, body := ask(tc.permit, tc.agent)
			if code != http.StatusOK || runs == nil || len(runs) != 0 || !strings.Contains(body, `"runs":[]`) {
				t.Errorf("%d %s", code, body)
			}
		})
	}

	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	for name, p := range map[string]string{
		"names no one":     signed("coach", aud, "t1", nil, key),
		"not for Coach":    signed("refunds", aud, "t1", &permit.Actor{Sub: "ana"}, key),
		"another audience": signed("coach", "https://evil.example/v1/mcp", "t1", &permit.Actor{Sub: "ana"}, key),
		"another account":  signed("coach", aud, "t2", &permit.Actor{Sub: "ana"}, key),
		"not KIFF's":       signed("coach", aud, "t1", &permit.Actor{Sub: "ana"}, permit.Key{ID: "k1", Priv: otherPriv}),
		"expired":          signedAt("coach", aud, "t1", &permit.Actor{Sub: "ana"}, key, now.Add(-time.Minute)),
		// A valid Coach permit, for a target the account registered, but
		// issued for another of Coach's tools.
		"for another tool": signedFor("taf_task", "coach", aud, "t1", &permit.Actor{Sub: "ana"}, key, now.Add(time.Minute)),
	} {
		t.Run("a permit act refuses, "+name, func(t *testing.T) {
			if code, runs, body := ask(p, "refunds"); code != http.StatusUnauthorized || len(runs) != 0 {
				t.Errorf("%d %s", code, body)
			}
		})
	}

	t.Run("no permit, or no agent", func(t *testing.T) {
		for _, tc := range [][2]string{{"", "refunds"}, {anas, ""}} {
			if code, _, body := ask(tc[0], tc[1]); code != http.StatusBadRequest {
				t.Errorf("%q %q: %d %s", tc[0], tc[1], code, body)
			}
		}
	})
}
