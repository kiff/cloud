package gateway

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// ClientSeen is one agent's MCP client as it described itself.
type ClientSeen struct {
	TenantID, AgentID, Name, Version string
	URLElicitation                   bool
	At                               time.Time
}

// ClientRecorder keeps the latest ClientSeen per agent and client name.
type ClientRecorder interface {
	SeeClient(ctx context.Context, c ClientSeen) error
}

// clientSeenEvery is how often one agent's unchanged client is written.
const clientSeenEvery = 10 * time.Minute

// clientChangeEvery is how often a client whose version or capability
// changed is written: the version is the agent's own claim, so a key
// changing it on every call must not write on every call.
const clientChangeEvery = time.Minute

// maxClientsPerAgent caps the client names kept per agent, in memory and
// in gateway.agent_clients: the name is the agent's own claim, so one key
// varying it must not grow either without limit.
const maxClientsPerAgent = 10

type seenKey struct{ tenantID, agentID, name string }

type seenEntry struct {
	version string
	url     bool
	at      time.Time
}

// seenCache holds one entry per agent and client name, at most
// maxClientsPerAgent names per agent. Entries older than clientSeenEvery
// are dropped once per clientSeenEvery, not on every call, so a call
// never scans the whole cache.
type seenCache struct {
	mu       sync.Mutex
	last     map[seenKey]seenEntry
	perAgent map[[2]string]int
	pruned   time.Time
}

// admit reports whether k should be written now, and records it.
func (c *seenCache) admit(k ClientSeen, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil || now.Sub(c.pruned) >= clientSeenEvery {
		c.prune(now)
	}
	key := seenKey{k.TenantID, k.AgentID, k.Name}
	agent := [2]string{k.TenantID, k.AgentID}
	next := seenEntry{version: k.Version, url: k.URLElicitation, at: now}
	if e, ok := c.last[key]; ok {
		wait := clientSeenEvery
		if e.version != k.Version || e.url != k.URLElicitation {
			wait = clientChangeEvery
		}
		if now.Sub(e.at) < wait {
			return false
		}
		c.last[key] = next
		return true
	}
	if c.perAgent[agent] >= maxClientsPerAgent {
		return false
	}
	c.last[key] = next
	c.perAgent[agent]++
	return true
}

// prune drops entries older than clientSeenEvery and recounts per agent.
func (c *seenCache) prune(now time.Time) {
	if c.last == nil {
		c.last = map[seenKey]seenEntry{}
	}
	c.perAgent = map[[2]string]int{}
	for k, e := range c.last {
		if now.Sub(e.at) >= clientSeenEvery {
			delete(c.last, k)
			continue
		}
		c.perAgent[[2]string{k.tenantID, k.agentID}]++
	}
	c.pruned = now
}

// seeClient records the request's client for this agent, at most every
// clientSeenEvery for the same client, version and capability. Best
// effort: a failure is logged and changes nothing about the call.
func (a *agent) seeClient(ctx context.Context) {
	g := a.g
	c := mcp.CallerFrom(ctx)
	if g.Clients == nil || c.Name == "" {
		return
	}
	k := ClientSeen{TenantID: a.me.TenantID, AgentID: a.me.AgentID, Name: clean(c.Name, 100),
		Version: clean(c.Version, 50), URLElicitation: c.URLElicitation}
	if k.Name == "" {
		return
	}
	now := g.now()
	if !g.seen.admit(k, now) {
		return
	}
	k.At = now
	if err := g.Clients.SeeClient(context.WithoutCancel(ctx), k); err != nil && g.Logger != nil {
		g.Logger.WarnContext(ctx, "gateway: client not recorded", "err", err.Error())
	}
}

// PostgresClients writes gateway.agent_clients.
type PostgresClients struct{ Pool *pgxpool.Pool }

// SeeClient upserts the row, adding a new client name only while the
// agent has fewer than maxClientsPerAgent.
func (p PostgresClients) SeeClient(ctx context.Context, c ClientSeen) error {
	_, err := p.Pool.Exec(ctx, `INSERT INTO gateway.agent_clients
		(tenant_id, agent_id, client_name, client_version, url_elicitation, last_seen_at)
		SELECT $1, $2, $3, $4, $5, $6
		WHERE EXISTS (SELECT 1 FROM gateway.agent_clients WHERE tenant_id = $1 AND agent_id = $2 AND client_name = $3)
		   OR (SELECT count(*) FROM gateway.agent_clients WHERE tenant_id = $1 AND agent_id = $2) < $7
		ON CONFLICT (tenant_id, agent_id, client_name) DO UPDATE SET
		  client_version = EXCLUDED.client_version, url_elicitation = EXCLUDED.url_elicitation,
		  last_seen_at = GREATEST(gateway.agent_clients.last_seen_at, EXCLUDED.last_seen_at)`,
		c.TenantID, c.AgentID, c.Name, c.Version, c.URLElicitation, c.At, maxClientsPerAgent)
	return err
}

// clean keeps at most n characters of s, never splitting one, and drops
// control characters and invalid UTF-8, which Postgres text refuses.
func clean(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		if n == 0 {
			break
		}
		if r == utf8.RuneError || unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
		n--
	}
	return b.String()
}
