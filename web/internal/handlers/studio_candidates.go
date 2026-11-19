package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	studio "github.com/kiff/kiff-studio"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// studio_candidates.go is the action-first onboarding seam: a first-run
// tenant leads with the actions KIFF already observed from a connected
// guard runtime (GET /v1/guard/candidates, the API), renders them as
// candidate-control cards, and lets the user confirm one into a domain
// draft — instead of authoring a domain cold.
//
// Honesty boundary: candidates are runtime- grounded; the web side
// never invents thresholds or states. The confirmed draft carries the
// observed action surface (entity, fields, risk) and leaves the
// lifecycle + policy for the user to map in the ladder. The
// paste/replay path reuses the same blueprint seam so a single
// tool-call JSON derives the same shape with no live connection.

// renderActionFirst loads the tenant's runtime candidates and renders the
// action-first first-run view. On any candidates-load error it degrades to
// the empty state (with the error surfaced), never to a cold authoring
// ladder — the ladder was retired; the on-ramps are derive (observe) and
// the codebase handoff.
func (h *StudioHandler) renderActionFirst(w http.ResponseWriter, r *http.Request, bearer string, view pages.StudioView) {
	view.ActionFirst = true

	runtimes, err := h.api.ListGuardCandidatesBearer(r.Context(), bearer, "", "", "", "")
	if err != nil {
		h.logger.WarnContext(r.Context(), "studio: list guard candidates failed",
			slog.String("err", err.Error()))
		view.ErrMsg = "could not load observed actions: " + err.Error()
		_ = pages.Studio(view).Render(r.Context(), w)
		return
	}
	view.Candidates = studioCandidatesFrom(runtimes)
	_ = pages.Studio(view).Render(r.Context(), w)
}

// studioCandidatesFrom flattens the per-runtime candidate sets into a flat
// list of view-model cards, each carrying its runtime coordinate so the
// confirm form can re-fetch the exact candidate. API order is preserved
// (already risk-ranked within each runtime).
func studioCandidatesFrom(runtimes []apiclient.GuardRuntimeCandidates) []pages.StudioCandidate {
	out := make([]pages.StudioCandidate, 0)
	for _, rc := range runtimes {
		for _, c := range rc.Candidates {
			fields := make([]pages.StudioCandidateField, 0, len(c.Fields))
			for _, f := range c.Fields {
				fields = append(fields, pages.StudioCandidateField{
					Name:     f.Name,
					Type:     f.Type,
					Required: f.Required,
				})
			}
			out = append(out, pages.StudioCandidate{
				Tool:              c.Tool,
				Action:            c.Action,
				EntityType:        c.EntityType,
				EntityArg:         c.EntityArg,
				Fields:            fields,
				Risk:              c.Risk,
				RiskReasons:       append([]string(nil), c.RiskReasons...),
				ObservedCallCount: c.ObservedCallCount,
				Adapter:           rc.Runtime.Adapter,
				Project:           rc.Runtime.Project,
				Environment:       rc.Runtime.Environment,
				Workflow:          rc.Runtime.Workflow,
				AgentID:           rc.Runtime.AgentID,
				Mode:              rc.Runtime.Mode,
				GroundingOK:       rc.Grounding.OK,
			})
		}
	}
	return out
}

// confirmCandidate turns one chosen runtime candidate into a domain draft.
// It re-fetches the candidate (scoped to its runtime coordinate) so the
// confirmed control is the live one, never a client-supplied shape, builds
// the blueprint via the shared seam, persists it as the draft, and routes
// to the existing draft/edit path.
func (h *StudioHandler) confirmCandidate(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "API not configured"})
		http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
		return
	}
	bearer, err := h.bearer(r, s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	project := strings.TrimSpace(r.PostFormValue("project"))
	environment := strings.TrimSpace(r.PostFormValue("environment"))
	agentID := strings.TrimSpace(r.PostFormValue("agent_id"))
	workflow := strings.TrimSpace(r.PostFormValue("workflow"))
	tool := strings.TrimSpace(r.PostFormValue("tool"))
	if tool == "" {
		h.flash(w, "which action to protect was not specified")
		http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
		return
	}

	runtimes, err := h.api.ListGuardCandidatesBearer(r.Context(), bearer, project, environment, agentID, workflow)
	if err != nil {
		h.flash(w, "could not load observed actions: "+err.Error())
		http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
		return
	}
	rt, cand, ok := findCandidate(runtimes, tool)
	if !ok {
		h.flash(w, "that observed action is no longer available")
		http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
		return
	}

	yaml, err := studio.BuildYAML(candidateBlueprint(rt, cand))
	if err != nil {
		// A grounded candidate can be incomplete (no lifecycle yet); the
		// draft store accepts invalid mid-edit drafts, so persist the
		// partial YAML rather than blocking the confirmation.
		yaml = []byte(candidateDraftFallbackYAML(rt, cand))
	}
	if _, err := h.api.PutDraftBearer(r.Context(), bearer, yaml); err != nil {
		h.logger.WarnContext(r.Context(), "studio: confirm candidate put draft failed",
			slog.String("err", err.Error()))
		h.flash(w, "could not save the control draft: "+err.Error())
		http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
		return
	}

	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind:    session.FlashDomainValidated,
		Message: fmt.Sprintf("Protecting %s. Map the lifecycle and set the policy, then activate in observe.", candidateActionName(cand)),
	})
	http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
}

// replayCandidate is the secondary fallback: derive a candidate control
// from a single pasted/replayed tool-call JSON when no live runtime
// candidate exists. It reuses the same blueprint seam as the primary path
// — the only difference is where the candidate shape comes from.
func (h *StudioHandler) replayCandidate(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "API not configured"})
		http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
		return
	}
	bearer, err := h.bearer(r, s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}

	raw := strings.TrimSpace(r.PostFormValue("tool_call"))
	if raw == "" {
		h.flash(w, "paste one tool-call JSON to derive a control")
		http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
		return
	}
	rt, cand, err := candidateFromToolCall(raw)
	if err != nil {
		h.flash(w, "could not read that tool call: "+err.Error())
		http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
		return
	}

	yaml, err := studio.BuildYAML(candidateBlueprint(rt, cand))
	if err != nil {
		yaml = []byte(candidateDraftFallbackYAML(rt, cand))
	}
	if _, err := h.api.PutDraftBearer(r.Context(), bearer, yaml); err != nil {
		h.logger.WarnContext(r.Context(), "studio: replay candidate put draft failed",
			slog.String("err", err.Error()))
		h.flash(w, "could not save the control draft: "+err.Error())
		http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
		return
	}

	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind:    session.FlashDomainValidated,
		Message: fmt.Sprintf("Derived %s from the replayed call. Map the lifecycle and set the policy, then activate in observe.", candidateActionName(cand)),
	})
	http.Redirect(w, r, "/dashboard/build", http.StatusSeeOther)
}

// findCandidate locates the candidate with the given tool name across all
// runtimes, returning its runtime coordinate too.
func findCandidate(runtimes []apiclient.GuardRuntimeCandidates, tool string) (apiclient.GuardRuntimeRef, apiclient.GuardCandidate, bool) {
	for _, rc := range runtimes {
		for _, c := range rc.Candidates {
			if strings.EqualFold(c.Tool, tool) {
				return rc.Runtime, c, true
			}
		}
	}
	return apiclient.GuardRuntimeRef{}, apiclient.GuardCandidate{}, false
}

// candidateBlueprint is the shared derivation seam: it maps a runtime-
// grounded candidate to a studio.Blueprint. It carries the observed action
// surface (entity, required fields, risk, permissions) and deliberately
// leaves the state machine empty — KIFF derives the action, never the
// lifecycle. Thresholds are never set here (the enforcement-posture
// design: server-controlled, user-set). Both the live-candidate path and
// the paste/replay path call this, so the derivation lives once.
func candidateBlueprint(rt apiclient.GuardRuntimeRef, c apiclient.GuardCandidate) studio.Blueprint {
	domain := lowerHyphen(firstNonEmpty(rt.Workflow, rt.AgentID, c.Tool, "guard-workflow"))
	action := candidateActionName(c)
	entity := strings.TrimSpace(c.EntityType)
	if entity == "" {
		// Structural placeholder only — the card shows the real (empty)
		// value as "not reported"; the user names the entity in the ladder.
		entity = "Entity"
	}
	perm := domain + "." + strings.ToLower(action)
	return studio.Blueprint{
		Domain: domain,
		Entity: entity,
		Actions: []studio.Action{{
			Name:                action,
			AllowedStates:       []string{}, // lifecycle is the user's to map; never fabricated
			RequiredParameters:  candidateRequiredParams(c),
			RequiredPermissions: []string{perm, perm + ".approve"},
			Risk:                candidateRisk(c),
			ApprovalRequired:    false,
			Executor:            "cloud.proxy",
		}},
		Roles: map[string][]string{
			"tenant_owner": {perm, perm + ".approve"},
		},
	}
}

// candidateDraftFallbackYAML renders a minimal, valid-to-store draft for a
// candidate when BuildYAML rejects the (intentionally lifecycle-less)
// blueprint. Mirrors the action surface; no fabricated states or
// thresholds. The draft store accepts invalid mid-edit drafts, so this is
// only a serialization fallback, not a second model.
func candidateDraftFallbackYAML(rt apiclient.GuardRuntimeRef, c apiclient.GuardCandidate) string {
	domain := lowerHyphen(firstNonEmpty(rt.Workflow, rt.AgentID, c.Tool, "guard-workflow"))
	action := candidateActionName(c)
	entity := strings.TrimSpace(c.EntityType)
	if entity == "" {
		entity = "Entity"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "domain: %s\n", domain)
	fmt.Fprintf(&b, "entity: %s\n", entity)
	b.WriteString("states: []\n")
	b.WriteString("actions:\n")
	fmt.Fprintf(&b, "  - name: %s\n", action)
	b.WriteString("    allowed_states: []\n")
	params := candidateRequiredParams(c)
	if len(params) > 0 {
		fmt.Fprintf(&b, "    required_parameters: [%s]\n", strings.Join(params, ", "))
	} else {
		b.WriteString("    required_parameters: []\n")
	}
	fmt.Fprintf(&b, "    risk: %s\n", candidateRisk(c))
	return b.String()
}

// candidateRequiredParams is the proposed required-field set: the
// candidate's required fields, minus the entity-id arg (which is the
// entity selector, not a parameter). Grounded in the candidate; nothing
// added.
func candidateRequiredParams(c apiclient.GuardCandidate) []string {
	entityArg := strings.TrimSpace(c.EntityArg)
	out := make([]string, 0, len(c.Fields))
	seen := map[string]bool{}
	for _, f := range c.Fields {
		name := strings.TrimSpace(f.Name)
		if name == "" || !f.Required || name == entityArg || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// candidateActionName resolves the action label: the candidate's action,
// or the upper-snake of the tool name as a fallback.
func candidateActionName(c apiclient.GuardCandidate) string {
	if a := strings.TrimSpace(c.Action); a != "" {
		return a
	}
	if a := upperSnake(c.Tool); a != "" {
		return a
	}
	return "RUN_ACTION"
}

// candidateRisk passes through the API's runtime-grounded risk tier,
// defaulting to "low" when absent. The web side never re-derives risk for
// the live path; the value is the deterministic Path A label.
func candidateRisk(c apiclient.GuardCandidate) string {
	switch strings.ToLower(strings.TrimSpace(c.Risk)) {
	case "high":
		return "high"
	case "medium":
		return "medium"
	case "low":
		return "low"
	default:
		return "low"
	}
}

// candidateFromToolCall parses a single pasted/replayed tool-call JSON into
// a candidate shape for the paste/replay fallback. Accepts the reference
// shape: {tool, action?, entity_type?, entity_id?, fields{}}. Every
// provided field name becomes a proposed required field; risk is a
// mechanical name heuristic, clearly a starting point the user refines.
func candidateFromToolCall(raw string) (apiclient.GuardRuntimeRef, apiclient.GuardCandidate, error) {
	var in struct {
		Tool       string                     `json:"tool"`
		Action     string                     `json:"action"`
		EntityType string                     `json:"entity_type"`
		EntityArg  string                     `json:"entity_arg"`
		Fields     map[string]json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return apiclient.GuardRuntimeRef{}, apiclient.GuardCandidate{}, fmt.Errorf("invalid JSON")
	}
	tool := strings.TrimSpace(in.Tool)
	if tool == "" {
		return apiclient.GuardRuntimeRef{}, apiclient.GuardCandidate{}, fmt.Errorf("missing \"tool\"")
	}

	fields := make([]apiclient.GuardCandidateField, 0, len(in.Fields))
	for name := range in.Fields {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		fields = append(fields, apiclient.GuardCandidateField{Name: name, Type: "unknown", Required: true})
	}

	cand := apiclient.GuardCandidate{
		Tool:        tool,
		Action:      strings.TrimSpace(in.Action),
		EntityType:  strings.TrimSpace(in.EntityType),
		EntityArg:   strings.TrimSpace(in.EntityArg),
		Fields:      fields,
		Risk:        replayRisk(tool + " " + in.Action + " " + in.EntityType),
		RiskReasons: nil,
	}
	rt := apiclient.GuardRuntimeRef{
		Workflow: lowerHyphen(firstNonEmpty(in.EntityType, tool)),
		Mode:     "observe",
		Adapter:  "replay",
	}
	return rt, cand, nil
}

// replayRisk is the paste/replay mechanical risk heuristic, matching the
// server's Path A signal set. Deterministic and transparent; the user
// refines it.
func replayRisk(text string) string {
	hay := strings.ToLower(text)
	for _, s := range []string{"pay", "payment", "refund", "charge", "transfer", "delete", "wire", "payout"} {
		if strings.Contains(hay, s) {
			return "high"
		}
	}
	for _, s := range []string{"send", "email", "notify", "update", "create", "escalate", "offer"} {
		if strings.Contains(hay, s) {
			return "medium"
		}
	}
	return "low"
}
