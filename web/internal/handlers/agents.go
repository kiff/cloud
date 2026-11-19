package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// agentsData is everything the Agents pages join on an agent's one
// name: the agents list, cards, keys, guard runtimes and the tenant's
// recent decisions. Partial names the sources that failed; the agents
// list itself failing is LoadError.
type agentsData struct {
	agents    []apiclient.Agent
	cards     []apiclient.Mandate
	keys      []apiclient.KeyListItem
	runtimes  []apiclient.GuardConnection
	decisions []apiclient.ProposalDecision
	// decisionsTruncated: the page of the tenant's recent decisions was
	// full, so an agent missing from it may simply be crowded out.
	decisionsTruncated bool
	// decisionsFailed: the recent decisions could not be read at all.
	decisionsFailed bool
	loadErr         error
	partial         []string
}

// summaries joins the data into one summary per agent. When the keys
// could not be read, each summary says so, so a missing key reads as
// unknown rather than revoked.
func (d agentsData) summaries() []pages.AgentSummary {
	out := pages.AgentsFrom(d.agents, d.cards, d.keys, d.runtimes, d.decisions, d.decisionsTruncated, d.decisionsFailed)
	if slices.Contains(d.partial, "keys") {
		for i := range out {
			out[i].KeysFailed = true
		}
	}
	return out
}

// agentsRecentDecisions is how many of the tenant's recent decisions
// are read to find each agent's latest (the API's maximum).
const agentsRecentDecisions = 100

func (h *DashboardHandler) loadAgents(ctx context.Context, bearer string) agentsData {
	var d agentsData
	var mu sync.Mutex
	var wg sync.WaitGroup
	fail := func(name string, err error) {
		h.logger.WarnContext(ctx, "agents: read failed", slog.String("source", name), slog.String("err", err.Error()))
		mu.Lock()
		d.partial = append(d.partial, name)
		mu.Unlock()
	}
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	run(func() { d.agents, d.loadErr = h.api.ListAgentsBearer(ctx, bearer) })
	run(func() {
		var err error
		if d.cards, err = h.api.ListMandatesBearer(ctx, bearer); err != nil {
			fail("cards", err)
		}
	})
	run(func() {
		var err error
		if d.keys, err = h.api.ListKeysBearer(ctx, bearer); err != nil {
			fail("keys", err)
		}
	})
	run(func() {
		var err error
		if d.runtimes, err = h.api.ListGuardConnectionsBearer(ctx, bearer); err != nil {
			fail("runtimes", err)
		}
	})
	run(func() {
		resp, err := h.api.ListProposalsBearer(ctx, bearer, agentsRecentDecisions)
		if err != nil {
			fail("recent decisions", err)
			mu.Lock()
			d.decisionsFailed = true
			mu.Unlock()
			return
		}
		d.decisions = resp.Proposals
		d.decisionsTruncated = len(resp.Proposals) >= agentsRecentDecisions
	})
	wg.Wait()
	return d
}

func (h *DashboardHandler) agentsIndex(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	v := pages.AgentsView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, Flash: convertFlash(flash)}
	if h.api == nil {
		v.LoadError = "apps/api is not configured."
		_ = pages.Agents(v).Render(r.Context(), w)
		return
	}
	d := h.loadAgents(r.Context(), bearer)
	if d.loadErr != nil {
		v.LoadError = "Your agents could not be loaded. This is not a statement that none exist."
	} else {
		v.Agents = d.summaries()
		v.Partial = d.partial
	}
	_ = pages.Agents(v).Render(r.Context(), w)
}

func (h *DashboardHandler) agentOverview(w http.ResponseWriter, r *http.Request) {
	h.renderAgent(w, r, "overview")
}

func (h *DashboardHandler) agentCards(w http.ResponseWriter, r *http.Request) {
	h.renderAgent(w, r, "cards")
}

func (h *DashboardHandler) agentKeys(w http.ResponseWriter, r *http.Request) {
	h.renderAgent(w, r, "keys")
}

func (h *DashboardHandler) agentTools(w http.ResponseWriter, r *http.Request) {
	h.renderAgent(w, r, "tools")
}

func (h *DashboardHandler) renderAgent(w http.ResponseWriter, r *http.Request, tab string) {
	s, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	v := pages.AgentDetailView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, Flash: convertFlash(flash), Tab: tab}
	if h.api == nil {
		v.LoadError = "apps/api is not configured."
		_ = pages.AgentDetail(v).Render(r.Context(), w)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	d := h.loadAgents(r.Context(), bearer)
	if d.loadErr != nil {
		v.LoadError = "This agent could not be loaded."
		_ = pages.AgentDetail(v).Render(r.Context(), w)
		return
	}
	found := false
	for _, a := range d.summaries() {
		if a.ID == id {
			v.Agent, found = a, true
			break
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	for _, dec := range d.decisions {
		if dec.ActorID == id {
			v.Decisions = append(v.Decisions, dec)
		}
	}
	v.Partial = d.partial
	if h.gateway != nil {
		v.GatewayURL = h.publicGatewayURL()
		if tab == "tools" {
			if tools, err := h.gateway.List(r.Context(), bearer); err != nil {
				h.logger.WarnContext(r.Context(), "agents: tools list failed", slog.String("err", err.Error()))
				v.ToolsError = "Your tools could not be loaded. This is not a statement that none are connected."
			} else {
				v.Tools = tools
			}
			// Connect links: URLs that name this agent.
			if h.api != nil {
				if links, err := h.api.ConnectLinksBearer(r.Context(), bearer, strings.TrimSpace(r.PathValue("id"))); err != nil {
					h.logger.WarnContext(r.Context(), "agents: connect links failed", slog.String("err", err.Error()))
					v.ConnectLinksError = "Connect links could not be loaded."
				} else {
					v.ConnectLinks = links
				}
			}
		}
	}
	if tab == "keys" {
		v.Revoke = r.URL.Query().Get("revoke")
	}
	_ = pages.AgentDetail(v).Render(r.Context(), w)
}

// DefaultPublicGatewayURL is the gateway's MCP endpoint in production.
const DefaultPublicGatewayURL = "https://mcp.kiff.dev/mcp"

func (h *DashboardHandler) publicGatewayURL() string {
	if u := strings.TrimSpace(h.PublicGatewayURL); u != "" {
		return u
	}
	return DefaultPublicGatewayURL
}

// createGatewayKey connects an agent to the KIFF MCP gateway: a
// standard key, bound to the agent, holding only gateway_agent, so it
// can ask for decisions and nothing else. Minting that role needs
// admin; the API decides, and anyone else is told why. The key is
// shown once on the agent's page, inside its MCP config.
func (h *DashboardHandler) createGatewayKey(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.PathValue("id"))
	target := "/agents/" + url.PathEscape(agentID) + "/tools"
	fail := func(msg string) {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: msg})
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
	if h.api == nil || h.gateway == nil {
		fail("Agents cannot be connected to the KIFF gateway from this deployment.")
		return
	}
	if agentID == "" {
		http.NotFound(w, r)
		return
	}
	created, err := h.api.CreateAgentKeyBearer(r.Context(), bearer, "Gateway key for "+agentID, []string{"gateway_agent"}, agentID)
	switch {
	case errors.Is(err, apiclient.ErrForbidden):
		fail("Only an admin of this account can connect an agent to the KIFF gateway, because its key holds a role only an admin can grant. Ask an admin to open this page and connect it.")
		return
	case err != nil:
		h.logger.WarnContext(r.Context(), "agents: gateway key failed", slog.String("agent", agentID), slog.String("err", err.Error()))
		fail("The gateway key could not be created. Nothing was changed.")
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind: session.FlashGatewayKeyRevealed,
		Key:  &session.FlashKeyPayload{ID: created.ID, Plaintext: created.Plaintext, Label: created.Label, Owner: s.Subject},
	})
	// Straight to the configuration, on the agent's Tools tab.
	http.Redirect(w, r, target+"#gateway", http.StatusSeeOther)
}

func (h *DashboardHandler) agentNew(w http.ResponseWriter, r *http.Request) {
	s, _, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	_ = pages.AgentNew(pages.AgentNewView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, Flash: convertFlash(flash)}).Render(r.Context(), w)
}

// createAgent adds an agent and issues its key in one step. The key
// is bound to the agent and carries no role: observing needs none,
// and a card's page supplies the role its job requires.
func (h *DashboardHandler) createAgent(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	v := pages.AgentNewView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, Name: name}
	if h.api == nil {
		v.Error = "apps/api is not configured."
		_ = pages.AgentNew(v).Render(r.Context(), w)
		return
	}
	if name == "" {
		v.Error = "Give the agent a name."
		w.WriteHeader(http.StatusBadRequest)
		_ = pages.AgentNew(v).Render(r.Context(), w)
		return
	}
	if _, err := h.api.CreateAgentBearer(r.Context(), bearer, name); err != nil {
		if errors.Is(err, apiclient.ErrAgentExists) {
			v.Error = "An agent named " + name + " already exists."
		} else {
			h.logger.WarnContext(r.Context(), "agents: add failed", slog.String("err", err.Error()))
			v.Error = "The agent could not be added: " + err.Error()
		}
		w.WriteHeader(http.StatusBadRequest)
		_ = pages.AgentNew(v).Render(r.Context(), w)
		return
	}
	h.mintAgentKey(w, r, s, bearer, name, "")
}

// createAgentKeyForAgent mints another key bound to an existing agent.
func (h *DashboardHandler) createAgentKeyForAgent(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	if h.api == nil {
		http.Error(w, "apps/api is not configured", http.StatusServiceUnavailable)
		return
	}
	h.mintAgentKey(w, r, s, bearer, strings.TrimSpace(r.PathValue("id")), "/keys")
}

// tab is where the key is shown: the Keys tab when created there, the
// Overview (with the connect steps) for a newly added agent.
func (h *DashboardHandler) mintAgentKey(w http.ResponseWriter, r *http.Request, s session.Session, bearer, agentID, tab string) {
	target := "/agents/" + url.PathEscape(agentID)
	created, err := h.api.CreateAgentKeyBearer(r.Context(), bearer, "Agent key for "+agentID, nil, agentID)
	if err != nil {
		h.logger.WarnContext(r.Context(), "agents: mint key failed", slog.String("agent", agentID), slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError,
			Message: "The agent is added, but its key could not be created. Use \"Create a kiff-guard key\" on its Keys tab."})
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind: session.FlashKeyRevealed,
		Key:  &session.FlashKeyPayload{ID: created.ID, Plaintext: created.Plaintext, Label: created.Label, Owner: s.Subject},
	})
	http.Redirect(w, r, target+tab, http.StatusSeeOther)
}

// createConnectLink creates a connect link for the agent: a gateway URL
// that names it, so an app connected with it acts as this agent once an
// admin or editor allows it.
func (h *DashboardHandler) createConnectLink(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.PathValue("id"))
	target := "/agents/" + url.PathEscape(agentID) + "/tools/link"
	if h.api == nil {
		http.Error(w, "apps/api is not configured", http.StatusServiceUnavailable)
		return
	}
	if _, err := h.api.CreateConnectLinkBearer(r.Context(), bearer, agentID); err != nil {
		msg := "The connect link could not be created. Nothing was changed."
		if errors.Is(err, apiclient.ErrForbidden) {
			msg = "Only an admin or editor of this account can create a connect link."
		}
		h.logger.WarnContext(r.Context(), "agents: create connect link", slog.String("agent", agentID), slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: msg})
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// revokeConnectLink revokes a link; every app connected through it stops.
func (h *DashboardHandler) revokeConnectLink(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.PathValue("id"))
	target := "/agents/" + url.PathEscape(agentID) + "/tools/link"
	if h.api == nil {
		http.Error(w, "apps/api is not configured", http.StatusServiceUnavailable)
		return
	}
	if err := h.api.RevokeConnectLinkBearer(r.Context(), bearer, r.PathValue("link")); err != nil {
		msg := "The connect link could not be revoked."
		if errors.Is(err, apiclient.ErrForbidden) {
			msg = "Only an admin or editor of this account can revoke a connect link."
		}
		h.logger.WarnContext(r.Context(), "agents: revoke connect link", slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: msg})
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// connections lists every way an agent reaches the gateway: OAuth
// connections from the API, and agent-bound gateway keys that are not the
// key of one of those connections.
func (h *DashboardHandler) connections(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	v := pages.ConnectionsView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, Flash: convertFlash(flash)}
	if h.api == nil {
		v.LoadError = "apps/api is not configured."
		_ = pages.Connections(v).Render(r.Context(), w)
		return
	}
	day := func(t time.Time) string { return t.UTC().Format("2006-01-02") }
	grantKeys := map[string]bool{}
	if conns, err := h.api.ConnectionsBearer(r.Context(), bearer); err != nil {
		h.logger.WarnContext(r.Context(), "connections: list failed", slog.String("err", err.Error()))
		v.LoadError = "Connections could not be loaded. This is not a statement that none exist."
	} else {
		for _, c := range conns {
			grantKeys[c.KeyID] = true
			row := pages.ConnectionRow{Agent: c.AgentID, Client: c.ClientName, How: "Sign-in", GrantedBy: c.GrantedBy,
				Created: day(c.CreatedAt), LastUsed: day(c.LastUsedAt), Status: "Live"}
			if c.Method == "code" {
				row.How = "Code"
			}
			switch c.Status {
			case "ended":
				row.Status = "Ended"
				if c.EndedReason != "" {
					row.Status += " (" + strings.ReplaceAll(c.EndedReason, "_", " ") + ")"
				}
			case "idle":
				row.Status = "Idle"
			}
			if c.Status != "ended" {
				row.RevokeURL = "/connections/grants/" + url.PathEscape(c.ID) + "/revoke"
			}
			v.Rows = append(v.Rows, row)
		}
	}
	if keys, err := h.api.ListKeysBearer(r.Context(), bearer); err == nil {
		for _, k := range keys {
			if k.AgentID == "" || grantKeys[k.ID] || !slices.Contains(k.Roles, "gateway_agent") {
				continue
			}
			row := pages.ConnectionRow{Agent: k.AgentID, Client: k.Label, How: "Key", GrantedBy: k.Subject,
				Created: strings.SplitN(k.CreatedAt, "T", 2)[0], LastUsed: "—", Status: "Live",
				RevokeURL: "/dashboard/keys/" + url.PathEscape(k.ID) + "/revoke", KeyRevoke: true}
			if k.LastUsedAt != nil {
				row.LastUsed = strings.SplitN(*k.LastUsedAt, "T", 2)[0]
			}
			v.Rows = append(v.Rows, row)
		}
	} else {
		h.logger.WarnContext(r.Context(), "connections: keys failed", slog.String("err", err.Error()))
	}
	_ = pages.Connections(v).Render(r.Context(), w)
}

// revokeConnection ends one OAuth connection; its key is revoked.
func (h *DashboardHandler) revokeConnection(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	if h.api == nil {
		http.Error(w, "apps/api is not configured", http.StatusServiceUnavailable)
		return
	}
	if err := h.api.RevokeGrantBearer(r.Context(), bearer, r.PathValue("id")); err != nil {
		msg := "The connection could not be revoked."
		if errors.Is(err, apiclient.ErrForbidden) {
			msg = "Only an admin or editor of this account can revoke a connection."
		}
		h.logger.WarnContext(r.Context(), "connections: revoke failed", slog.String("err", err.Error()))
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: msg})
	}
	http.Redirect(w, r, "/connections", http.StatusSeeOther)
}
