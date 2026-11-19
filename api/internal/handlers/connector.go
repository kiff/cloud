package handlers

import (
	"context"
	"net/http"
	"reflect"
	"slices"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/permit"
)

// connectorExecutor is the executor every gateway action names: KIFF
// decides the call and the gateway makes it.
const connectorExecutor = "cloud.proxy"

// gatewayAgentRole is the role the gateway's contract grants each tool's
// permission to.
const gatewayAgentRole = "gateway_agent"

// GatewayToolOwners reads who connected each of an account's gateway tools
// (gateway.tools.created_by): a key's id, or "" for a person.
type GatewayToolOwners interface {
	ToolOwners(ctx context.Context, tenantID string) (map[string]string, error)
}

// connectorScope is what a connector key may change in the gateway's
// contract and permit targets: through the gateway it only adds a tool or
// removes one it connected, so a direct write may do no more.
// A tool another key or a person connected is protected; so is
// every tool when who connected what can't be read.
type connectorScope struct {
	owners map[string]string // nil: unknown, every tool protected
	caller string            // the connector key's id
}

func newConnectorScope(ctx context.Context, owners GatewayToolOwners, tenantID, caller string) connectorScope {
	s := connectorScope{caller: caller}
	if owners == nil || caller == "" {
		return s
	}
	if m, err := owners.ToolOwners(ctx, tenantID); err == nil {
		s.owners = m
	}
	return s
}

// protected reports whether the connector may not remove or change tool.
// A tool with no connection (not yet committed, or already gone) is not.
func (s connectorScope) protected(tool string) bool {
	if s.owners == nil {
		return true
	}
	owner, ok := s.owners[tool]
	return ok && owner != s.caller
}

func connectorRefusal(w http.ResponseWriter, msg string) bool {
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "connector_key_forbidden", "message": msg})
	return false
}

// connectorContract reports whether a connector key may write next over
// current (nil when the gateway's domain doesn't exist yet), and writes
// the refusal when it may not. next must be the contract the
// gateway generates: its domain, its fixed shape and role map, every
// action needing a Card and run by cloud.proxy. Every action of current
// stays exactly as it is, or is removed when the connector connected it.
// If a gateway release ever writes an action differently, a connector's
// writes are refused until a person's or an admin's change regenerates the
// contract: this fails closed.
func connectorContract(w http.ResponseWriter, current *domain.ParseResult, next domain.ParseResult, scope connectorScope) bool {
	spec := next.Spec
	if spec.Domain != gatewayDomain {
		return connectorRefusal(w, "a connector key changes only the gateway's domain")
	}
	if spec.Entity != "ToolCall" || !slices.Equal(spec.Events, []string{"TOOL_CALL_REQUESTED"}) || !slices.Equal(spec.States, []string{"requested"}) ||
		!slices.Equal(spec.Transitions, []domain.TransitionSpec{{On: "TOOL_CALL_REQUESTED", From: "", To: "requested"}}) {
		return connectorRefusal(w, "from a connector key, the gateway's contract keeps the shape the gateway writes")
	}
	perms := make([]string, 0, len(spec.Actions))
	var reads []string
	for _, a := range spec.Actions {
		perm := "tools." + a.Name
		if !a.RequiresAuthority || a.Executor != connectorExecutor || !slices.Equal(a.RequiredPermissions, []string{perm}) {
			return connectorRefusal(w, "from a connector key, every gateway action needs requires_authority: true, executor: "+
				connectorExecutor+" and its own tools.<name> permission ("+a.Name+")")
		}
		perms = append(perms, perm)
		if a.Risk == "low" { // the gateway writes a read-only tool as low risk
			reads = append(reads, perm)
		}
	}
	granted := slices.Clone(spec.Permissions.Roles[gatewayAgentRole])
	slices.Sort(granted)
	slices.Sort(perms)
	if !slices.Equal(granted, perms) {
		return connectorRefusal(w, "from a connector key, the gateway's contract grants each tool to "+gatewayAgentRole+" and nothing else")
	}
	// setup_reader, the key of an agent that holds no tools of its own
	// (Setup), is granted every read-only tool (kiff/tafs#138), and the
	// tools of the account's own control server (kiff/tafs#206), which only
	// a person connects. So a connector key, which connects vendors, grants
	// it every read and changes nothing about which writes it holds: a
	// vendor's tool that writes never goes to Setup, and a control server's
	// grant stays. Each call is still decided on a Card naming that agent.
	setup := spec.Permissions.Roles[auth.RoleSetupReader]
	if len(spec.Permissions.Roles) > 2 || (len(spec.Permissions.Roles) == 2 && setup == nil) {
		return connectorRefusal(w, "from a connector key, the gateway's contract grants tools to "+gatewayAgentRole+
			" and "+auth.RoleSetupReader+" only")
	}
	var was []string
	if current != nil {
		was = current.Spec.Permissions.Roles[auth.RoleSetupReader]
	}
	for _, p := range perms {
		read := slices.Contains(reads, p)
		granted := slices.Contains(setup, p)
		if read && !granted {
			return connectorRefusal(w, "from a connector key, the gateway's contract grants every read-only tool to "+auth.RoleSetupReader+" ("+p+")")
		}
		if !read && granted != slices.Contains(was, p) {
			return connectorRefusal(w, "a connector key doesn't change which tools that write "+auth.RoleSetupReader+" holds ("+p+")")
		}
	}
	for _, p := range setup {
		if !slices.Contains(perms, p) {
			return connectorRefusal(w, "from a connector key, "+auth.RoleSetupReader+" is granted only the contract's own tools ("+p+")")
		}
	}
	if current == nil {
		return true
	}
	for _, was := range current.Spec.Actions {
		i := slices.IndexFunc(spec.Actions, func(a domain.ActionSpec) bool { return a.Name == was.Name })
		switch {
		case i >= 0 && !reflect.DeepEqual(spec.Actions[i], was):
			return connectorRefusal(w, "a connector key adds tools and removes its own; it doesn't change "+was.Name)
		case i < 0 && scope.protected(was.Name):
			return connectorRefusal(w, "a connector key removes only the tools it connected; "+was.Name+" isn't one")
		}
	}
	return true
}

// connectorMayReplace reports whether a connector key may replace the
// account's permit targets with next, and writes the refusal when it may
// not. Only the gateway's targets; each existing one stays as it
// is or goes with the connector's own tool; a new one is for a tool no one
// else connected. Replacing the list would drop any other domain's, so an
// account that has them keeps them until a person or an admin key changes
// the list.
func (h *PermitTargetsHandler) connectorMayReplace(w http.ResponseWriter, r *http.Request, tenant string, next []permit.Target, scope connectorScope) bool {
	for _, t := range next {
		if t.Domain != gatewayDomain {
			return connectorRefusal(w, "a connector key sets only the gateway's permit targets")
		}
	}
	existing, err := h.targets.List(r.Context(), tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "permit targets: list failed", "err", err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the permit targets were not changed"})
		return false
	}
	was := map[string]permit.Target{}
	for _, t := range existing {
		if t.Domain != gatewayDomain {
			return connectorRefusal(w, "this account has permit targets outside the gateway; a connector key cannot replace them")
		}
		was[t.Action] = t
	}
	now := map[string]permit.Target{}
	for _, t := range next {
		now[t.Action] = t
	}
	for action, t := range was {
		if n, ok := now[action]; (ok && n != t) || (!ok && scope.protected(action)) {
			return connectorRefusal(w, "a connector key sets targets for the tools it connects; it doesn't change "+action+"'s")
		}
	}
	for action := range now {
		if _, ok := was[action]; !ok && scope.protected(action) {
			return connectorRefusal(w, "a connector key sets targets for the tools it connects; "+action+" isn't one")
		}
	}
	return true
}
