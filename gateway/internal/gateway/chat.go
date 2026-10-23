package gateway

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// ChatClients are the MCP clients a held call may be offered to in the
// chat, by name with a minimum version: only clients
// whose URL elicitation KIFF has verified.
//
// This list decides HOW the owner is told, never WHO may answer. Name
// and version are whatever the client says, so any client can claim to
// be one of these; that is harmless because the link opens Needs you,
// which only the owner's signed-in session can answer. Never hang a
// permission on it.
type ChatClients map[string][]int

// ParseChatClients reads "claude-code>=2.1.287,other>=1.0". Entries
// that do not parse are skipped.
func ParseChatClients(s string) ChatClients {
	out := ChatClients{}
	for _, e := range strings.Split(s, ",") {
		name, min, ok := strings.Cut(strings.TrimSpace(e), ">=")
		if v := versionOf(min); ok && name != "" && v != nil {
			out[strings.TrimSpace(name)] = v
		}
	}
	return out
}

// DefaultChatClients are the clients verified so far.
const DefaultChatClients = "claude-code>=2.1.287"

// Verified reports whether a client at this version is on the list.
func (c ChatClients) Verified(name, version string) bool {
	min, ok := c[name]
	v := versionOf(version)
	if !ok || v == nil {
		return false
	}
	for i := 0; i < len(min); i++ {
		var n int
		if i < len(v) {
			n = v[i]
		}
		if n != min[i] {
			return n > min[i]
		}
	}
	return true
}

func versionOf(s string) []int {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "-")
	if s == "" {
		return nil
	}
	var out []int
	for _, p := range strings.Split(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// ChatPromptGap is the least time between two chat prompts for one
// agent: an agent working through a batch could otherwise open one per
// hold. Later holds are still emailed and on Needs you.
const ChatPromptGap = time.Minute

// prompts remembers which holds were offered in a chat and when each
// agent was last prompted. It is in memory: after a restart a hold may
// be offered once more, which only repeats a link the owner already has.
type prompts struct {
	mu     sync.Mutex
	holds  map[string]time.Time // exception id → when offered
	agents map[string]time.Time // tenant/agent → last offer
}

// take reports whether hold may be offered to agent now, and if so
// records it.
func (p *prompts) take(hold, agent string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.holds == nil {
		p.holds, p.agents = map[string]time.Time{}, map[string]time.Time{}
	}
	for id, at := range p.holds {
		if now.Sub(at) > 24*time.Hour {
			delete(p.holds, id)
		}
	}
	if _, done := p.holds[hold]; done {
		return false
	}
	if last, ok := p.agents[agent]; ok && now.Sub(last) < ChatPromptGap {
		return false
	}
	p.holds[hold], p.agents[agent] = now, now
	return true
}

// offerInChat asks the agent's chat client to open a held call's Needs
// you page, when everything allows it:
//   - the call is held and the hold can still be answered;
//   - the account allows it (on by default for a personal account);
//   - the client declares URL elicitation and is on the verified list;
//   - the link is this app's own Needs you page;
//   - this hold was not offered before, and the agent was not prompted
//     in the last ChatPromptGap.
//
// Whatever the client answers, it is not an answer to the hold: only
// the owner, on Needs you, answers. The result below it is unchanged.
func (a *agent) offerInChat(ctx context.Context, in invocation.Invocation) {
	g := a.g
	if in.State != invocation.StateHeld || in.ExceptionID == "" || !a.me.ChatApprovals {
		return
	}
	if in.HoldExpiresAt != nil && !g.now().Before(*in.HoldExpiresAt) {
		return
	}
	c := mcp.CallerFrom(ctx)
	// A client running the call as a task waits on it instead.
	if c.TaskRequested || !c.Modern || !c.URLElicitation || !g.ChatClients.Verified(c.Name, c.Version) {
		return
	}
	if !g.ownNeedsYou(in.ReviewURL, in.ExceptionID) {
		return
	}
	id := "kiff-" + in.ExceptionID
	if _, answered := c.Responses[id]; answered {
		return
	}
	if !g.prompts.take(in.ExceptionID, a.me.TenantID+"/"+a.me.AgentID, g.now()) {
		return
	}
	msg := a.me.AgentID + " wants to use " + in.Tool + ", which is outside its Card. " +
		"Open KIFF to approve or reject it, signed in as the account's owner. " +
		"KIFF approval links always go to " + g.appHost() + "."
	mcp.RequestURLElicitation(ctx, mcp.URLElicitation{ID: id, URL: in.ReviewURL, Message: msg})
}

// ownNeedsYou reports whether link is exactly this app's Needs you page
// for exception id: the only URL KIFF ever asks a client to open.
func (g *Gateway) ownNeedsYou(link, exceptionID string) bool {
	app, err := url.Parse(g.AppURL)
	if err != nil || app.Scheme != "https" || app.Host == "" {
		return false
	}
	u, err := url.Parse(link)
	return err == nil && u.Scheme == "https" && u.Host == app.Host && u.User == nil &&
		u.RawQuery == "" && u.Fragment == "" && u.Path == "/needs-you/"+exceptionID
}

func (g *Gateway) appHost() string {
	if u, err := url.Parse(g.AppURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "the KIFF app"
}
