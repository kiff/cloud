package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/guard"
	"github.com/kiff/cloud/api/internal/mandate"
)

// the agent-as-unit design: the agents list is the records joined with every name an
// agent already has implicitly — a card's holder, a bound key's agent,
// a guard runtime's agent — each marked with where it was seen.
func TestAgents_ListJoinsRecordsAndImplicitAgents(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	store := agents.NewMemoryStore()
	_ = store.Create(ctx, agents.Agent{TenantID: "t1", ID: "added-agent", CreatedAt: now})

	cards := mandate.NewMemoryStore()
	_ = cards.Issue(ctx, mandate.Mandate{ID: "c1", TenantID: "t1", Subject: "refund-agent"}, "owner")
	archived := now.Add(-time.Hour)
	_ = cards.Issue(ctx, mandate.Mandate{ID: "c2", TenantID: "t1", Subject: "old-agent", ArchivedAt: &archived}, "owner")
	_ = cards.Issue(ctx, mandate.Mandate{ID: "c3", TenantID: "t2", Subject: "other-tenant-agent"}, "owner")

	keys := auth.NewMemoryAPIKeyStore()
	for i, agent := range []string{"keyed-agent", "refund-agent", ""} {
		_ = keys.Insert(ctx, auth.APIKeyRecord{ID: "k" + string(rune('0'+i)), Tenant: "t1", Subject: "owner",
			AgentID: agent, KeyHash: [auth.APIKeyHashSize]byte{byte(i + 1)}, CreatedAt: now})
	}

	runtimes := guard.NewMemoryStore()
	for _, agent := range []string{"guarded-agent", "refund-agent"} {
		if _, err := runtimes.Touch(ctx, "t1", guard.TouchInput{AgentID: agent, Adapter: "agno", Mode: "observe", SeenAt: now}); err != nil {
			t.Fatal(err)
		}
	}

	h := NewAgentsHandler(store, WithAgentsSources(cards, keys, runtimes))
	mux := http.NewServeMux()
	h.Register(mux)

	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Agents []agentView `json:"agents"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	recorded := map[string]bool{}
	for _, a := range body.Agents {
		got[a.ID] = a.Sources
		recorded[a.ID] = a.Recorded
	}
	want := map[string][]string{
		"added-agent":   {"record"},
		"refund-agent":  {"card", "key", "runtime"},
		"keyed-agent":   {"key"},
		"guarded-agent": {"runtime"},
	}
	if len(got) != len(want) {
		t.Errorf("agents = %v, want %v (no archived card holder, no unbound key, no other tenant)", got, want)
	}
	for id, sources := range want {
		if len(got[id]) != len(sources) {
			t.Errorf("%s sources = %v, want %v", id, got[id], sources)
			continue
		}
		for i := range sources {
			if got[id][i] != sources[i] {
				t.Errorf("%s sources = %v, want %v", id, got[id], sources)
			}
		}
	}
	if !recorded["added-agent"] || recorded["refund-agent"] {
		t.Errorf("recorded = %v", recorded)
	}
}

func TestAgents_CreateAndArchive(t *testing.T) {
	store := agents.NewMemoryStore()
	h := NewAgentsHandler(store, WithAgentsClock(func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }))
	mux := http.NewServeMux()
	h.Register(mux)

	if rr := do(t, mux, runtimeKey(), http.MethodPost, "/v1/me/agents", map[string]any{"id": "sneaky"}); rr.Code != http.StatusForbidden {
		t.Errorf("runtime key add = %d, want 403", rr.Code)
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/agents", map[string]any{"id": "  refund-agent "}); rr.Code != http.StatusCreated {
		t.Fatalf("add = %d: %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/agents", map[string]any{"id": "refund-agent"}); rr.Code != http.StatusConflict {
		t.Errorf("duplicate add = %d, want 409", rr.Code)
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/agents", map[string]any{"id": "bad\nname"}); rr.Code != http.StatusBadRequest {
		t.Errorf("invalid id = %d, want 400", rr.Code)
	}
	list, _ := store.List(context.Background(), "t1")
	if len(list) != 1 || list[0].ID != "refund-agent" || list[0].CreatedBy != "alice" {
		t.Fatalf("stored = %+v", list)
	}

	if rr := do(t, mux, runtimeKey(), http.MethodPost, "/v1/me/agents/refund-agent/archive", nil); rr.Code != http.StatusForbidden {
		t.Errorf("runtime key archive = %d, want 403", rr.Code)
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/agents/refund-agent/archive", nil); rr.Code != http.StatusOK {
		t.Errorf("archive = %d", rr.Code)
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/agents/nobody/archive", nil); rr.Code != http.StatusNotFound {
		t.Errorf("archive unknown = %d, want 404", rr.Code)
	}
}

// the agent-as-unit design: issuing a card records its holder as an agent.
func TestIssuingACardRecordsItsHolderAsAnAgent(t *testing.T) {
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	agentStore := agents.NewMemoryStore()
	h := NewMandatesHandler(store, stubDomainSource{yaml: mandateDomainYAML}, WithMandatesAgents(agentStore))
	mux := http.NewServeMux()
	h.Register(mux)

	for i := 0; i < 2; i++ { // re-issuing leaves one record
		rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", map[string]any{
			"id": "refund-agent-refunds", "subject": "refund-agent", "domain": "refund-control",
			"grants":     []map[string]any{{"action": "AUTO_REFUND"}},
			"aggregates": []map[string]any{{"quantity": "sum(amount)", "limit": 110000, "window": "calendar_day"}},
		})
		if rr.Code != http.StatusCreated {
			t.Fatalf("issue = %d: %s", rr.Code, rr.Body.String())
		}
	}
	list, _ := agentStore.List(context.Background(), "t1")
	if len(list) != 1 || list[0].ID != "refund-agent" {
		t.Errorf("agent records = %+v, want refund-agent once", list)
	}
}

// the agent-as-unit design: a card's holder follows the agent id rule, so every new
// holder can be bound to a key and recorded as an agent. A holder issued
// before the rule applied to cards is listed as out of rule and never
// promoted to a record.
func TestAgents_HolderRuleAndLegacyHolders(t *testing.T) {
	ctx := context.Background()
	cards := mandate.NewMemoryStore()
	cards.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	agentStore := agents.NewMemoryStore()
	mh := NewMandatesHandler(cards, stubDomainSource{yaml: mandateDomainYAML}, WithMandatesAgents(agentStore))
	mux := http.NewServeMux()
	mh.Register(mux)

	for _, bad := range []string{strings.Repeat("a", 201), "bad\nholder"} {
		rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", map[string]any{
			"id": "c-bad", "subject": bad, "domain": "refund-control",
			"grants":     []map[string]any{{"action": "AUTO_REFUND"}},
			"aggregates": []map[string]any{{"quantity": "sum(amount)", "limit": 100, "window": "calendar_day"}},
		})
		if rr.Code != http.StatusBadRequest {
			t.Errorf("issuing to %q = %d, want 400", bad, rr.Code)
		}
	}
	if ms, _ := cards.List(ctx, "t1"); len(ms) != 0 {
		t.Errorf("an out-of-rule holder was issued a card: %+v", ms)
	}

	// A legacy card, issued before the rule, straight into the store.
	legacy := "legacy\tholder"
	_ = cards.Issue(ctx, mandate.Mandate{ID: "c-legacy", TenantID: "t1", Subject: legacy}, "owner")
	if err := agentStore.Ensure(ctx, agents.Agent{TenantID: "t1", ID: legacy}); !errors.Is(err, agents.ErrInvalidID) {
		t.Errorf("Ensure(out-of-rule) = %v, want ErrInvalidID", err)
	}

	ah := NewAgentsHandler(agentStore, WithAgentsSources(cards, nil, nil))
	amux := http.NewServeMux()
	ah.Register(amux)
	rr := do(t, amux, owner(), http.MethodGet, "/v1/me/agents", nil)
	var body struct {
		Agents []agentView `json:"agents"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if len(body.Agents) != 1 || body.Agents[0].ID != legacy || !body.Agents[0].OutOfRule || body.Agents[0].Recorded {
		t.Errorf("legacy holder listed as %+v, want out_of_rule and not recorded", body.Agents)
	}
	if list, _ := agentStore.List(ctx, "t1"); len(list) != 0 {
		t.Errorf("a legacy holder was promoted to a record: %+v", list)
	}
}

// Known answers for the agents the list shows as usable: recorded or
// implicit, in this tenant, not archived (the gateway-OAuth design consent).
func TestAgents_Known(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	store := agents.NewMemoryStore()
	_ = store.Create(ctx, agents.Agent{TenantID: "t1", ID: "added", CreatedAt: now})
	_ = store.Create(ctx, agents.Agent{TenantID: "t1", ID: "gone", CreatedAt: now})
	_ = store.Archive(ctx, "t1", "gone", now)
	cards := mandate.NewMemoryStore()
	_ = cards.Issue(ctx, mandate.Mandate{ID: "c1", TenantID: "t1", Subject: "holder"}, "owner")
	_ = cards.Issue(ctx, mandate.Mandate{ID: "c2", TenantID: "t2", Subject: "elsewhere"}, "owner")
	h := NewAgentsHandler(store, WithAgentsSources(cards, nil, nil))
	for id, want := range map[string]bool{"added": true, "holder": true, "gone": false, "elsewhere": false, "nobody": false} {
		if got, err := h.Known(ctx, "t1", id); err != nil || got != want {
			t.Errorf("Known(%q) = %v, %v; want %v", id, got, err, want)
		}
	}
}

// Archiving an agent revokes the account's keys bound to it, and only
// those: a removed taf's key reaches nothing, and keys don't pile up with
// each one removed.
func TestAgents_ArchiveRevokesItsKeys(t *testing.T) {
	ctx := context.Background()
	store := agents.NewMemoryStore()
	keys := auth.NewMemoryAPIKeyStore()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	for i, k := range []struct{ id, tenant, agent string }{
		{"k-refund", "t1", "refund-agent"}, {"k-refund-2", "t1", "refund-agent"},
		{"k-other", "t1", "other-agent"}, {"k-account", "t1", ""}, {"k-elsewhere", "t2", "refund-agent"},
	} {
		if err := keys.Insert(ctx, auth.APIKeyRecord{ID: k.id, Tenant: k.tenant, Subject: "alice", AgentID: k.agent,
			KeyHash: [auth.APIKeyHashSize]byte{byte(i + 1)}, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewAgentsHandler(store, WithAgentsSources(nil, keys, nil), WithAgentsClock(func() time.Time { return now }))
	mux := http.NewServeMux()
	h.Register(mux)
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/agents", map[string]any{"id": "refund-agent"}); rr.Code != http.StatusCreated {
		t.Fatalf("add = %d", rr.Code)
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/agents/refund-agent/archive", nil); rr.Code != http.StatusOK {
		t.Fatalf("archive = %d", rr.Code)
	}
	var left []string
	for _, tn := range []string{"t1", "t2"} {
		active, _ := keys.ListActive(ctx, tn)
		for _, k := range active {
			left = append(left, k.ID)
		}
	}
	slices.Sort(left)
	if want := []string{"k-account", "k-elsewhere", "k-other"}; !slices.Equal(left, want) {
		t.Fatalf("active keys after the archive: %v, want %v", left, want)
	}
}

// An agent's own key archives its own agent through /v1/agents/self/archive,
// and reaches no other: the person route still refuses it, the same id on
// another account is that account's and isn't found, a key bound to no
// agent and a person are refused, and the calling key is revoked with the
// agent. Archiving again revokes keys made since.
func TestAgents_OwnKeyArchivesOnlyItsOwnAgent(t *testing.T) {
	ctx := context.Background()
	store := agents.NewMemoryStore()
	keys := auth.NewMemoryAPIKeyStore()
	now := time.Date(2026, 11, 7, 22, 0, 0, 0, time.UTC)
	for _, a := range []struct{ tenant, id string }{{"t1", "taf-a"}, {"t1", "taf-b"}, {"t2", "taf-a"}} {
		if err := store.Create(ctx, agents.Agent{TenantID: a.tenant, ID: a.id, CreatedAt: now, Kind: agents.KindTaf}); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	key := func(tenant, agent string) string {
		t.Helper()
		n++
		id := fmt.Sprintf("k%d", n)
		if err := keys.Insert(ctx, auth.APIKeyRecord{ID: id, Tenant: tenant, Subject: "alice", AgentID: agent,
			KeyHash: [auth.APIKeyHashSize]byte{byte(n)}, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	keyA, keyB, keyA2 := key("t1", "taf-a"), key("t1", "taf-b"), key("t2", "taf-a")
	agentKey := func(tenant, agent, keyID string) auth.Identity {
		return auth.Identity{Subject: "alice", Tenant: tenant, Method: auth.MethodAPIKey, APIKey: true, AgentID: agent,
			Metadata: map[string]string{"api_key_id": keyID}}
	}
	h := NewAgentsHandler(store, WithAgentsSources(nil, keys, nil), WithAgentsClock(func() time.Time { return now }))
	mux := http.NewServeMux()
	h.Register(mux)
	active := func(tenant, id string) bool {
		list, _ := keys.ListActive(ctx, tenant)
		for _, k := range list {
			if k.ID == id {
				return true
			}
		}
		return false
	}
	archived := func(tenant, id string) bool {
		list, _ := store.List(ctx, tenant)
		for _, a := range list {
			if a.ID == id {
				return a.ArchivedAt != nil
			}
		}
		return false
	}
	const self = "/v1/agents/self/archive"

	// 1. Naming another agent takes the person route, which an agent's key
	// can't use: refused, nothing changes.
	if rr := do(t, mux, agentKey("t1", "taf-a", keyA), http.MethodPost, "/v1/me/agents/taf-b/archive", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("an agent's key on the person route = %d, want 403", rr.Code)
	}
	if archived("t1", "taf-b") || !active("t1", keyB) {
		t.Fatal("refused, yet taf-b was archived or its key revoked")
	}
	// 2. A key on another account bound to an agent named alike: that
	// account has no such agent, and these are untouched.
	if rr := do(t, mux, agentKey("t3", "taf-a", "k-t3"), http.MethodPost, self, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("an agent named alike elsewhere = %d, want 404", rr.Code)
	}
	if archived("t1", "taf-a") || archived("t2", "taf-a") || !active("t1", keyA) || !active("t2", keyA2) {
		t.Fatal("another account's archive touched these agents or keys")
	}
	// 3. A key bound to no agent, Setup's key, and a person, are refused here.
	unbound := auth.Identity{Subject: "svc", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{"admin"}}
	setup := agentKey("t1", "taf-b", keyB)
	setup.Roles = []string{auth.RoleSetupReader}
	for name, who := range map[string]auth.Identity{"a key bound to no agent": unbound, "Setup's key": setup, "a person": owner()} {
		if rr := do(t, mux, who, http.MethodPost, self, nil); rr.Code != http.StatusForbidden {
			t.Fatalf("%s = %d, want 403", name, rr.Code)
		}
	}
	if archived("t1", "taf-a") || archived("t1", "taf-b") || !active("t1", keyB) {
		t.Fatal("a refused call archived something or revoked a key")
	}
	// Its own: archived, and the calling key revoked with it, so a retry is
	// refused at sign-in (5). Other agents' keys stay.
	if rr := do(t, mux, agentKey("t1", "taf-a", keyA), http.MethodPost, self, nil); rr.Code != http.StatusOK {
		t.Fatalf("its own = %d: %s", rr.Code, rr.Body.String())
	}
	if !archived("t1", "taf-a") || archived("t1", "taf-b") || active("t1", keyA) || !active("t1", keyB) || !active("t2", keyA2) {
		t.Fatal("its own archive: want only taf-a archived and only its key revoked")
	}
	// Archiving again, by the owner, revokes a key made for it since.
	late := key("t1", "taf-a")
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/agents/taf-a/archive", nil); rr.Code != http.StatusOK {
		t.Fatalf("archive again = %d", rr.Code)
	}
	if active("t1", late) {
		t.Fatal("archiving again left a key made since")
	}
}
