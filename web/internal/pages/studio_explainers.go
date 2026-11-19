package pages

import "github.com/kiff/cloud/web/internal/studio_view"

// studioExplainer is one card in the Studio right pane. The
// page renders 3 to 4 of these per stage, swapping the set as
// the user progresses through the four stages. Pure data —
// the template renders the fields directly.
//
// Kind drives the small accent treatment in CSS:
//
//   - "primitive" — a kiff vocabulary primitive (entity, state, role, ...)
//   - "convention" — a naming or shape convention (dotted.lowercase, ...)
//   - "mechanic" — a runtime mechanic (replay, audit, scaffold, ...)
type studioExplainer struct {
	Kind  string // "primitive" / "convention" / "mechanic"
	Name  string // "entity", "state machine", "dotted.lowercase", ...
	Title string // short title shown on the card
	Body  string // one-paragraph explanation
}

// studioExplainersFor returns the explainer set for a given
// stage. Copy is curated — each stage exposes the primitives
// that the user has just unlocked, plus the conventions that
// are about to start mattering.
func studioExplainersFor(s studio_view.Stage) []studioExplainer {
	switch s {
	case studio_view.StageScratch:
		return []studioExplainer{
			{
				Kind: "primitive", Name: "entity",
				Title: "the noun your workflow turns on",
				Body: "An entity is the thing that has a state. " +
					"Order, Refund, Document, Shipment. Pick one " +
					"noun; every action you author will mutate it.",
			},
			{
				Kind: "primitive", Name: "action",
				Title: "the verb the agent proposes",
				Body: "Every action is a typed proposal. The agent " +
					"asks for it, the runtime decides allowed / " +
					"approval_required / blocked. No free-form prompts.",
			},
			{
				Kind: "convention", Name: "UPPER_SNAKE",
				Title: "actions and events name themselves",
				Body: "Actions and events are constants the runtime " +
					"matches by name. ISSUE_REFUND, not issueRefund. " +
					"The convention is what makes audit trails greppable.",
			},
		}

	case studio_view.StageShape:
		return []studioExplainer{
			{
				Kind: "primitive", Name: "state machine",
				Title: "the entity's life on rails",
				Body: "States are the only positions the entity can " +
					"be in. Transitions move it. Actions guard on " +
					"AllowedStates so a refund can't issue twice.",
			},
			{
				Kind: "primitive", Name: "event",
				Title: "what moves the entity forward",
				Body: "Events drive transitions. ORDER_PAID lands; " +
					"the entity advances; the next action becomes " +
					"reachable. Events come from your apps, never " +
					"from the agent.",
			},
			{
				Kind: "mechanic", Name: "replay",
				Title: "every state change is a row",
				Body: "The runtime stores every event and every " +
					"action. Replay the rows in order, you get the " +
					"current state. No hidden variables, no caches.",
			},
		}

	case studio_view.StageRules:
		return []studioExplainer{
			{
				Kind: "primitive", Name: "risk",
				Title: "low / medium / high / critical",
				Body: "Risk classifies what an action can do. Low " +
					"runs immediately. High waits for approval. " +
					"The classification is the policy, not a hint.",
			},
			{
				Kind: "primitive", Name: "approval",
				Title: "required vs never",
				Body: "approval=required gates an action behind a " +
					"second human. The runtime emits " +
					"approval_required and blocks the proposal " +
					"until a supervisor grants.",
			},
			{
				Kind: "primitive", Name: "permission",
				Title: "the dotted name a role holds",
				Body: "Permissions name what a role can do. " +
					"refund-flow.issue_refund proposes; " +
					"refund-flow.issue_refund.approve grants. " +
					"Roles are bags of permissions.",
			},
			{
				Kind: "convention", Name: "dotted.lowercase",
				Title: "domain.action[.approve]",
				Body: "Permissions follow the domain.action pattern. " +
					"The .approve suffix gates the second-human " +
					"step. The runtime matches by string; the " +
					"convention is the contract.",
			},
		}

	case studio_view.StageFull:
		return []studioExplainer{
			{
				Kind: "primitive", Name: "audit",
				Title: "the chain you can hand a regulator",
				Body: "Audit records every event, decision, " +
					"approval, and execution as a typed row. " +
					"in-memory for dev, postgres for prod, file " +
					"for replay.",
			},
			{
				Kind: "primitive", Name: "adapter",
				Title: "where events come from",
				Body: "Adapters bridge your apps into the runtime. " +
					"Webhooks today, polling sources later. The " +
					"adapter normalizes events; the runtime treats " +
					"them as the same shape.",
			},
			{
				Kind: "mechanic", Name: "scaffold",
				Title: "the Go code the YAML implies",
				Body: "Stage 4 unlocks the scaffold tab. " +
					"domain.go is the constants block, actions.go " +
					"the executor stubs, domain_test.go the " +
					"happy / blocked / replay cases.",
			},
			{
				Kind: "convention", Name: "vocabulary vs mechanics",
				Title: "the YAML names; the runtime decides",
				Body: "Naming is the shared contract. The runtime " +
					"never invents an action you haven't declared, " +
					"and your apps never react to a state you " +
					"haven't named.",
			},
		}
	}
	return nil
}
