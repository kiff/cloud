package handlers

import (
	studio "github.com/kiff/kiff-studio"

	"github.com/kiff/cloud/web/internal/studio_view"
)

// studio_preview.go builds canned, read-only Studio drafts for
// the four stages. These power the API-less preview mode: when
// apps/api is not configured (local glimpse, or a degraded
// deploy), the Studio page renders a static walk-through of all
// four stages instead of a dead single-stage placeholder. The
// stage strip becomes a set of tabs (?stage=1..4) and the
// builder renders read-only.
//
// The example mirrors the kiff-studio-evolution mockup's
// hermes-tool-governance domain so the preview matches the
// design reference exactly.

// studioPreviewDraft returns the canned draft for a preview
// stage (1..4). Out-of-range values clamp to stage 1.
func studioPreviewDraft(stage int) studio_view.StudioDraft {
	switch stage {
	case 2:
		return previewStageShape()
	case 3:
		return previewStageRules()
	case 4:
		return previewStageFull()
	default:
		return previewStageScratch()
	}
}

// previewStageScratch — entity + one action, no states.
func previewStageScratch() studio_view.StudioDraft {
	return studio_view.StudioDraft{
		Blueprint: studio.Blueprint{
			Domain: "hermes-tool-governance",
			Entity: "AgentToolCall",
			Actions: []studio.Action{
				{
					Name:               "EXECUTE_TERMINAL",
					RequiredParameters: []string{"command", "working_dir"},
					Executor:           "cloud.proxy",
				},
			},
		},
		Audit: studio_view.AuditConfig{Store: "in-memory", TracePropagation: true},
	}
}

// previewStageShape — adds the state machine + a second action.
func previewStageShape() studio_view.StudioDraft {
	d := previewStageScratch()
	d.Blueprint.Events = []string{
		"TOOL_PROPOSED", "TOOL_VALIDATED", "TOOL_STARTED", "TOOL_BLOCKED", "TOOL_DONE",
	}
	d.Blueprint.States = []string{
		"REQUESTED", "VALIDATED", "EXECUTING", "BLOCKED", "COMPLETED",
	}
	d.Blueprint.Transitions = []studio.Transition{
		{On: "TOOL_PROPOSED", From: "", To: "REQUESTED"},
		{On: "TOOL_VALIDATED", From: "REQUESTED", To: "VALIDATED"},
		{On: "TOOL_STARTED", From: "VALIDATED", To: "EXECUTING"},
		{On: "TOOL_BLOCKED", From: "REQUESTED", To: "BLOCKED"},
		{On: "TOOL_DONE", From: "EXECUTING", To: "COMPLETED"},
	}
	d.Blueprint.Actions = []studio.Action{
		{
			Name:               "EXECUTE_TERMINAL",
			AllowedStates:      []string{"VALIDATED"},
			RequiredParameters: []string{"command", "working_dir"},
			Executor:           "cloud.proxy",
		},
		{
			Name:               "SPAWN_SUBAGENT",
			AllowedStates:      []string{"VALIDATED"},
			RequiredParameters: []string{"task", "model", "budget"},
			Executor:           "cloud.proxy",
		},
	}
	return d
}

// previewStageRules — adds risk, approval, permissions, roles.
func previewStageRules() studio_view.StudioDraft {
	d := previewStageShape()
	d.Blueprint.Actions = []studio.Action{
		{
			Name:                "EXECUTE_TERMINAL",
			AllowedStates:       []string{"VALIDATED"},
			RequiredParameters:  []string{"command", "working_dir"},
			RequiredPermissions: []string{"hermes.execute_terminal", "hermes.execute_terminal.approve"},
			Risk:                "high",
			ApprovalRequired:    true,
			Executor:            "cloud.proxy",
		},
		{
			Name:                "SPAWN_SUBAGENT",
			AllowedStates:       []string{"VALIDATED"},
			RequiredParameters:  []string{"task", "model", "budget"},
			RequiredPermissions: []string{"hermes.spawn_subagent", "hermes.spawn_subagent.approve"},
			Risk:                "high",
			ApprovalRequired:    true,
			Executor:            "cloud.proxy",
		},
		{
			Name:                "CREATE_SKILL",
			AllowedStates:       []string{"VALIDATED"},
			RequiredParameters:  []string{"skill_name", "trigger", "steps"},
			RequiredPermissions: []string{"hermes.create_skill"},
			Risk:                "medium",
			ApprovalRequired:    false,
			Executor:            "cloud.proxy",
		},
	}
	d.Blueprint.Roles = map[string][]string{
		"operator": {
			"hermes.execute_terminal",
			"hermes.spawn_subagent",
			"hermes.create_skill",
		},
		"supervisor": {
			"hermes.execute_terminal.approve",
			"hermes.spawn_subagent.approve",
		},
	}
	return d
}

// previewStageFull — adds audit + an inbound adapter, which
// promotes the draft to StageFull.
func previewStageFull() studio_view.StudioDraft {
	d := previewStageRules()
	d.Audit = studio_view.AuditConfig{Store: "in-memory", TracePropagation: true}
	d.Adapters = []studio_view.AdapterConfig{
		{Name: "hermes-webhook", Source: "hermes-agent", Kind: "webhook"},
	}
	return d
}
