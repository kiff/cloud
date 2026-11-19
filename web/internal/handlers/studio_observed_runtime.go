package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	studio "github.com/kiff/kiff-studio"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/studio_view"
)

func (h *StudioHandler) loadObservedRuntimeProof(r *http.Request, bearer string, draft studio_view.StudioDraft) *pages.StudioObservedRuntime {
	conns, err := h.api.ListGuardConnectionsBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "studio: list guard connections for observed proof failed", slog.String("err", err.Error()))
		return nil
	}
	for _, conn := range conns {
		obs, err := h.api.GetGuardObservationBearer(r.Context(), bearer, conn)
		if err != nil {
			if !errors.Is(err, apiclient.ErrReceiptNotFound) {
				h.logger.WarnContext(r.Context(), "studio: get guard observation for observed proof failed",
					slog.String("project", conn.Project),
					slog.String("environment", conn.Environment),
					slog.String("agent_id", conn.AgentID),
					slog.String("workflow", conn.Workflow),
					slog.String("err", err.Error()))
			}
			continue
		}
		if proof := observedRuntimeProofFrom(draft.Blueprint, conn, obs, time.Now()); proof != nil {
			return proof
		}
	}
	return nil
}

func observedRuntimeProofFrom(bp studio.Blueprint, conn apiclient.GuardConnection, obs *apiclient.GuardObservation, now time.Time) *pages.StudioObservedRuntime {
	if obs == nil || len(obs.Tools) == 0 {
		return nil
	}
	if !matchesDraftDomain(bp, conn, obs) {
		return nil
	}
	for _, tool := range obs.Tools {
		action := observedActionName(tool)
		if action == "" || !blueprintHasAction(bp, action) {
			continue
		}
		entity := strings.TrimSpace(tool.EntityType)
		if entity == "" {
			entity = bp.Entity
		}
		return &pages.StudioObservedRuntime{
			Project:            firstNonEmpty(obs.Project, conn.Project),
			Environment:        firstNonEmpty(obs.Environment, conn.Environment),
			AgentID:            firstNonEmpty(obs.AgentID, conn.AgentID),
			Workflow:           firstNonEmpty(obs.Workflow, conn.Workflow),
			Adapter:            conn.Adapter,
			ToolName:           tool.Name,
			ActionName:         action,
			EntityType:         entity,
			EntityArg:          strings.TrimSpace(tool.EntityArg),
			RequiredParameters: observedRequiredParameters(tool),
			LastSeenLabel:      studioRuntimeLastSeenLabel(conn.LastSeenAt, now),
		}
	}
	return nil
}

func matchesDraftDomain(bp studio.Blueprint, conn apiclient.GuardConnection, obs *apiclient.GuardObservation) bool {
	domain := strings.TrimSpace(bp.Domain)
	if domain == "" {
		return false
	}
	for _, candidate := range []string{conn.Workflow, obs.Workflow, guardDraftDomain(conn)} {
		if strings.EqualFold(domain, strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func blueprintHasAction(bp studio.Blueprint, action string) bool {
	for _, a := range bp.Actions {
		if strings.EqualFold(a.Name, action) {
			return true
		}
	}
	return false
}

func observedActionName(tool apiclient.GuardToolObservation) string {
	if action := strings.TrimSpace(tool.Action); action != "" {
		return action
	}
	return upperSnake(tool.Name)
}

func studioRuntimeLastSeenLabel(lastSeen, now time.Time) string {
	if lastSeen.IsZero() || now.IsZero() {
		return ""
	}
	if lastSeen.After(now) {
		return "just now"
	}
	age := now.Sub(lastSeen)
	switch {
	case age < 90*time.Second:
		return "just now"
	case age < time.Hour:
		return pluralCount(int(age.Minutes()), "minute") + " ago"
	case age < 48*time.Hour:
		return pluralCount(int(age.Hours()), "hour") + " ago"
	default:
		return pluralCount(int(age.Hours()/24), "day") + " ago"
	}
}

func pluralCount(n int, unit string) string {
	if n <= 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// loadActiveControl returns the tenant's active protected control
// matching the draft's domain, if any. It is the truthful
// enforcement signal for the Studio header (the proof pricing
// design-06-04): a control with Status "active" whose domain
// matches the draft means KIFF is governing that action before it
// runs. Returns nil on any error or when no matching active
// control exists — the header degrades to the "observed" or
// "drafting" state rather than claiming enforcement that isn't
// there.
func (h *StudioHandler) loadActiveControl(r *http.Request, bearer string, draft studio_view.StudioDraft) *apiclient.Control {
	controls, err := h.api.ListControlsBearer(r.Context(), bearer)
	if err != nil {
		h.logger.WarnContext(r.Context(), "studio: list controls for enforcement header failed", slog.String("err", err.Error()))
		return nil
	}
	domain := strings.TrimSpace(draft.Blueprint.Domain)
	for i := range controls {
		c := controls[i]
		if c.Status != "active" {
			continue
		}
		// Prefer a control whose domain matches the draft so the
		// header speaks to the action being authored. When the
		// draft has no domain yet (fresh placeholder), any active
		// control still proves the tenant is enforcing something.
		if domain == "" || c.Domain == domain {
			return &c
		}
	}
	return nil
}
