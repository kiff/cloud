package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	studio "github.com/kiff/kiff-studio"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/session"
)

func (h *DashboardHandler) seedGuardDraft(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{
			Kind: session.FlashError, Message: "API not configured",
		})
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	want := guardDraftRequest{
		Project:     strings.TrimSpace(r.PostFormValue("project")),
		Environment: strings.TrimSpace(r.PostFormValue("environment")),
		AgentID:     strings.TrimSpace(r.PostFormValue("agent_id")),
		Workflow:    strings.TrimSpace(r.PostFormValue("workflow")),
	}
	connections, err := h.api.ListGuardConnectionsBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: list guard connections for draft failed", slog.String("err", err.Error()))
		h.flashDashboardError(w, "could not load guard runtimes: "+err.Error())
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	conn, ok := findGuardConnection(connections, want)
	if !ok {
		h.flashDashboardError(w, "that guard runtime is no longer connected")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	var observation *apiclient.GuardObservation
	if obs, err := h.api.GetGuardObservationBearer(r.Context(), bearer, conn); err == nil {
		observation = obs
	} else if !errors.Is(err, apiclient.ErrReceiptNotFound) {
		h.logger.WarnContext(r.Context(), "dashboard: get guard observation for draft failed", slog.String("err", err.Error()))
	}

	yaml, err := guardConnectionDraftYAML(conn, observation)
	if err != nil {
		h.flashDashboardError(w, "could not create Studio draft: "+err.Error())
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	if _, err := h.api.PutDraftBearer(r.Context(), bearer, yaml); err != nil {
		h.logger.WarnContext(r.Context(), "dashboard: seed guard draft failed", slog.String("err", err.Error()))
		h.flashDashboardError(w, "could not save Studio draft: "+err.Error())
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind:    session.FlashDomainValidated,
		Message: fmt.Sprintf("Drafted a %s domain from %s/%s. Review it before activating.", guardDraftDomain(conn), conn.AgentID, conn.Workflow),
	})
	http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
}

type guardDraftRequest struct {
	Project     string
	Environment string
	AgentID     string
	Workflow    string
}

func findGuardConnection(rows []apiclient.GuardConnection, want guardDraftRequest) (apiclient.GuardConnection, bool) {
	for _, c := range rows {
		if c.Project == want.Project &&
			c.Environment == want.Environment &&
			c.AgentID == want.AgentID &&
			c.Workflow == want.Workflow {
			return c, true
		}
	}
	return apiclient.GuardConnection{}, false
}

func guardConnectionDraftYAML(c apiclient.GuardConnection, obs *apiclient.GuardObservation) ([]byte, error) {
	bp := guardConnectionBlueprint(c, obs)
	yaml, err := studio.BuildYAML(bp)
	if err != nil {
		return nil, err
	}
	return yaml, nil
}

func guardConnectionBlueprint(c apiclient.GuardConnection, obs *apiclient.GuardObservation) studio.Blueprint {
	if obs != nil && len(obs.Tools) > 0 {
		return observedGuardBlueprint(c, obs)
	}
	if strings.EqualFold(c.Workflow, "duplicate-payment") {
		return duplicatePaymentBlueprint(c)
	}
	return genericGuardBlueprint(c)
}

func observedGuardBlueprint(c apiclient.GuardConnection, obs *apiclient.GuardObservation) studio.Blueprint {
	bp := genericGuardBlueprint(c)
	if strings.EqualFold(c.Workflow, "duplicate-payment") {
		bp = duplicatePaymentBlueprint(c)
	}

	actions := make([]studio.Action, 0, len(obs.Tools))
	roles := map[string][]string{"tenant_owner": {}}
	for _, tool := range obs.Tools {
		actionName := strings.TrimSpace(tool.Action)
		if actionName == "" {
			actionName = upperSnake(tool.Name)
		}
		if actionName == "" {
			continue
		}
		entity := strings.TrimSpace(tool.EntityType)
		if entity != "" {
			bp.Entity = entity
		}
		params := observedRequiredParameters(tool)
		perm := bp.Domain + "." + strings.ToLower(actionName)
		actions = append(actions, studio.Action{
			Name:                actionName,
			AllowedStates:       observedAllowedStates(bp),
			RequiredParameters:  params,
			RequiredPermissions: []string{perm, perm + ".approve"},
			Risk:                observedRisk(tool),
			ApprovalRequired:    false,
			Executor:            "cloud.proxy",
		})
		roles["tenant_owner"] = append(roles["tenant_owner"], perm, perm+".approve")
	}
	if len(actions) > 0 {
		bp.Actions = actions
		bp.Roles = roles
	}
	return bp
}

func genericGuardBlueprint(c apiclient.GuardConnection) studio.Blueprint {
	domain := guardDraftDomain(c)
	action := upperSnake(firstNonEmpty(c.Workflow, c.AgentID, "run-workflow"))
	if action == "" {
		action = "RUN_WORKFLOW"
	}
	if !strings.HasSuffix(action, "_WORKFLOW") {
		action = "RUN_" + action
	}
	entityRoot := pascalWords(firstNonEmpty(c.Workflow, c.AgentID, "workflow"))
	if entityRoot == "" {
		entityRoot = "Workflow"
	}
	eventRoot := upperSnake(firstNonEmpty(c.Workflow, c.AgentID, "workflow"))
	if eventRoot == "" {
		eventRoot = "WORKFLOW"
	}
	entity := entityRoot + "Record"
	perm := domain + "." + strings.ToLower(action)

	return studio.Blueprint{
		Domain: domain,
		Entity: entity,
		Events: []string{
			eventRoot + "_OPENED",
			eventRoot + "_DONE",
		},
		States: []string{"OPEN", "DONE"},
		Transitions: []studio.Transition{
			{On: eventRoot + "_OPENED", From: "", To: "OPEN"},
			{On: eventRoot + "_DONE", From: "OPEN", To: "DONE"},
		},
		Actions: []studio.Action{{
			Name:                action,
			AllowedStates:       []string{"OPEN"},
			RequiredParameters:  []string{},
			RequiredPermissions: []string{perm, perm + ".approve"},
			Risk:                "medium",
			ApprovalRequired:    false,
			Executor:            "cloud.proxy",
		}},
		Roles: map[string][]string{
			"tenant_owner": {perm, perm + ".approve"},
		},
	}
}

func observedRequiredParameters(tool apiclient.GuardToolObservation) []string {
	required := tool.Required
	if len(required) == 0 {
		required = schemaRequiredParameters(tool.ParameterSchema)
	}
	entityArg := strings.TrimSpace(tool.EntityArg)
	out := make([]string, 0, len(required))
	seen := map[string]bool{}
	for _, param := range required {
		param = strings.TrimSpace(param)
		if param == "" || param == entityArg || seen[param] {
			continue
		}
		seen[param] = true
		out = append(out, param)
	}
	return out
}

func schemaRequiredParameters(schema json.RawMessage) []string {
	if len(schema) == 0 {
		return nil
	}
	var doc struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		return nil
	}
	return doc.Required
}

func observedAllowedStates(bp studio.Blueprint) []string {
	for _, state := range []string{"PENDING", "OPEN"} {
		for _, candidate := range bp.States {
			if candidate == state {
				return []string{state}
			}
		}
	}
	if len(bp.States) > 0 {
		return []string{bp.States[0]}
	}
	return []string{"OPEN"}
}

func observedRisk(tool apiclient.GuardToolObservation) string {
	name := strings.ToLower(tool.Name + " " + tool.Action + " " + tool.Description)
	for _, high := range []string{"pay", "payment", "refund", "delete", "transfer"} {
		if strings.Contains(name, high) {
			return "medium"
		}
	}
	return "low"
}

func duplicatePaymentBlueprint(c apiclient.GuardConnection) studio.Blueprint {
	domain := guardDraftDomain(c)
	perm := domain + ".pay_invoice"
	return studio.Blueprint{
		Domain: domain,
		Entity: "Invoice",
		Events: []string{
			"INVOICE_CREATED",
			"INVOICE_PAID",
		},
		States: []string{"PENDING", "PAID"},
		Transitions: []studio.Transition{
			{On: "INVOICE_CREATED", From: "", To: "PENDING"},
			{On: "INVOICE_PAID", From: "PENDING", To: "PAID"},
		},
		Actions: []studio.Action{{
			Name:                "PAY_INVOICE",
			AllowedStates:       []string{"PENDING"},
			RequiredParameters:  []string{"amount_cents"},
			RequiredPermissions: []string{perm, perm + ".approve"},
			Risk:                "medium",
			ApprovalRequired:    false,
			Executor:            "cloud.proxy",
		}},
		Roles: map[string][]string{
			"tenant_owner": {perm, perm + ".approve"},
		},
	}
}

func guardDraftDomain(c apiclient.GuardConnection) string {
	return lowerHyphen(firstNonEmpty(c.Workflow, c.AgentID, c.Project, "guard-workflow"))
}

func (h *DashboardHandler) flashDashboardError(w http.ResponseWriter, msg string) {
	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind: session.FlashError, Message: msg,
	})
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func lowerHyphen(s string) string {
	out := tokenized(s, '-')
	if out == "" || out[0] < 'a' || out[0] > 'z' {
		out = "guard-" + out
	}
	return strings.Trim(out, "-")
}

func upperSnake(s string) string {
	out := tokenized(s, '_')
	if out == "" {
		return ""
	}
	return strings.ToUpper(out)
}

func pascalWords(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '/' || r == ' '
	})
	var out string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		lower := strings.ToLower(p)
		out += strings.ToUpper(lower[:1]) + lower[1:]
	}
	return out
}

func tokenized(s string, sep rune) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastSep := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSep = false
		default:
			if !lastSep {
				b.WriteRune(sep)
				lastSep = true
			}
		}
	}
	return strings.Trim(b.String(), string(sep))
}
