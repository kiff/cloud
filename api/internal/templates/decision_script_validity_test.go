package templates

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

// decision_script_validity_test.go pins a property the templates
// must hold but nothing checked before: a template's decision
// script must be *runnable* against its own domain state machine.
//
// The cafe-ops script shipped broken — it ingested SHIFT_SCHEDULED
// (entity → NEW) then immediately ran AUTO_ORDER_INVENTORY, which
// requires OPEN. The one-click demo could never succeed. This test
// walks each script's execute steps, tracking the entity's state,
// and fails if an action runs from a state its allowed_states does
// not permit — exactly the bug that reached a user.
//
// State advances via the follow-up event each action's executor
// emits (start_shift → SHIFT_STARTED, mark_paid → ORDER_PAID, …).
// That action→event mapping is the executors' contract, encoded
// here as a fixture; if an executor's follow-up event changes, the
// mapping below must change with it (and this test is where you'd
// notice).

// actionEmits maps an action name to the follow-up event its
// executor emits (or "" when the action emits no state-advancing
// event). Sourced from internal/executors: CafeStartShift emits
// SHIFT_STARTED, the refund mark/refund executors emit the matching
// ORDER_* events.
var actionEmits = map[string]string{
	// refund-agno
	"MARK_PAID":    "ORDER_PAID",
	"AUTO_REFUND":  "ORDER_REFUNDED",
	"REFUND_ORDER": "ORDER_REFUNDED",
	// cafe-ops
	"START_SHIFT":          "SHIFT_STARTED",
	"AUTO_ORDER_INVENTORY": "INVENTORY_ORDERED",
	"ORDER_INVENTORY":      "INVENTORY_ORDERED",
	"REQUEST_SPECIALTY":    "SPECIALTY_REQUESTED",
	"SEND_STAFF_MESSAGE":   "STAFF_MESSAGED",
	"ESCALATE_SUPPLIER":    "SUPPLIER_ESCALATED",
}

// minimal YAML shapes we need to validate a script.
type ymlDomain struct {
	Transitions []struct {
		On   string `yaml:"on"`
		From string `yaml:"from"`
		To   string `yaml:"to"`
	} `yaml:"transitions"`
	Actions []struct {
		Name          string   `yaml:"name"`
		AllowedStates []string `yaml:"allowed_states"`
		// An action that cannot run without an issued card,
		// whatever the state machine says.
		RequiresAuthority bool `yaml:"requires_authority"`
	} `yaml:"actions"`
}

type ymlEvent struct {
	Type string `json:"type"`
}

type scriptShape struct {
	Steps []struct {
		Card *struct {
			ID      string   `json:"id"`
			Subject string   `json:"subject"`
			Actions []string `json:"actions"`
			Limit   int64    `json:"limit"`
		} `json:"card"`
		Kind         string `json:"kind"`
		ActionName   string `json:"action_name"`
		Expect       string `json:"expect"`
		ExpectReason string `json:"expect_reason"`
		Event        struct {
			Type string `json:"type"`
		} `json:"event"`
	} `json:"steps"`
}

// TestDecisionScripts_RunnableAgainstDomain is the regression guard.
func TestDecisionScripts_RunnableAgainstDomain(t *testing.T) {
	t.Parallel()

	for _, tmpl := range List() {
		tmpl := tmpl
		t.Run(tmpl.Slug, func(t *testing.T) {
			t.Parallel()

			if len(tmpl.DecisionScript) == 0 {
				t.Skip("no decision script")
			}

			var dom ymlDomain
			if err := yaml.Unmarshal(tmpl.YAML, &dom); err != nil {
				t.Fatalf("parse domain yaml: %v", err)
			}
			// event -> resulting state, for the transitions whose
			// `from` we'll be in. We index by (event, fromState).
			type tk struct{ event, from string }
			transTo := map[tk]string{}
			for _, tr := range dom.Transitions {
				transTo[tk{tr.On, tr.From}] = tr.To
			}
			allowed := map[string]map[string]bool{}
			// Which actions cannot run without an issued card (RFC
			// 040). The state machine says nothing about these: a
			// perfectly refundable order is still refused when nothing
			// has been issued to the agent proposing it, so a script
			// step that expects a refusal there is right even though
			// the state allows the action.
			needsCard := map[string]bool{}
			for _, a := range dom.Actions {
				set := map[string]bool{}
				for _, s := range a.AllowedStates {
					set[s] = true
				}
				allowed[a.Name] = set
				needsCard[a.Name] = a.RequiresAuthority
			}

			var ev ymlEvent
			if err := json.Unmarshal(tmpl.SampleEvent, &ev); err != nil {
				t.Fatalf("parse sample event: %v", err)
			}
			var script scriptShape
			if err := json.Unmarshal(tmpl.DecisionScript, &script); err != nil {
				t.Fatalf("parse decision script: %v", err)
			}

			// The ingest step applies the sample event from the
			// initial (empty) state.
			state := ""
			if to, ok := transTo[tk{ev.Type, ""}]; ok {
				state = to
			} else {
				t.Fatalf("sample event %q has no transition from the initial state", ev.Type)
			}

			// Which actions have a card issued for them by this point
			// in the script. Before that, an action declaring
			// requires_authority is refused whatever the state says;
			// after it, the run can show it being authorized and then
			// refused again at the ceiling.
			carded := map[string]bool{}
			for i, step := range script.Steps {
				switch step.Kind {
				case "issue_card":
					if step.Card == nil || step.Card.Subject == "" || len(step.Card.Actions) == 0 {
						t.Fatalf("step %d: issue_card needs a card with a subject and at least one action", i)
					}
					if step.Card.Limit <= 0 {
						t.Fatalf("step %d: issue_card needs a ceiling above zero; zero is a revocation", i)
					}
					for _, a := range step.Card.Actions {
						if _, ok := allowed[a]; !ok {
							t.Fatalf("step %d: issue_card names %q, which the domain does not declare", i, a)
						}
						carded[a] = true
					}
				case "ingest":
					if i == 0 {
						// The first ingest applies the sample event,
						// already advanced into `state` above.
						continue
					}
					// A setup ingest drives the entity into a later
					// state via its own event. The event must have a
					// transition from the current state, or the demo
					// would not reach the state the next step needs.
					if step.Event.Type == "" {
						t.Fatalf("step %d: non-initial ingest must carry an event.type", i)
					}
					to, ok := transTo[tk{step.Event.Type, state}]
					if !ok {
						t.Fatalf("step %d: ingest event %q has no transition from state %q", i, step.Event.Type, state)
					}
					state = to
				case "execute":
					// Legacy "expect allowed, then advance via the
					// executor's follow-up event" step (cafe-ops /
					// refund-agno, registry-backed executors).
					states, ok := allowed[step.ActionName]
					if !ok {
						t.Fatalf("step %d: action %q not declared in the domain", i, step.ActionName)
					}
					if !states[state] {
						t.Fatalf("step %d: execute %q runs from state %q but its allowed_states are %v — the demo would 400 (state not allowed)",
							i, step.ActionName, state, keys(states))
					}
					emit, mapped := actionEmits[step.ActionName]
					if !mapped {
						t.Fatalf("step %d: action %q has no actionEmits mapping; add it (or \"\")", i, step.ActionName)
					}
					if emit != "" {
						if to, ok := transTo[tk{emit, state}]; ok {
							state = to
						}
					}
				case "attempt":
					// A demonstrated decision: run the action and assert
					// the runtime's verdict matches what the contract
					// will actually return. cloud.proxy executors emit
					// no follow-up event, so an attempt never advances
					// state — setup ingests do that.
					states, ok := allowed[step.ActionName]
					if !ok {
						t.Fatalf("step %d: action %q not declared in the domain", i, step.ActionName)
					}
					switch step.Expect {
					case "allowed":
						if !states[state] {
							t.Fatalf("step %d: attempt %q expects allowed but it is not allowed from state %q (allowed_states %v)",
								i, step.ActionName, state, keys(states))
						}
						// A script runs against a fresh tenant, and
						// nothing issues a card on its way through. An
						// action that requires one is therefore refused
						// here, whatever the state says.
						if needsCard[step.ActionName] && !carded[step.ActionName] {
							t.Fatalf("step %d: attempt %q expects allowed, but the action declares requires_authority and no card has been issued for it yet — it would be refused with no_mandate_issued",
								i, step.ActionName)
						}
					case "blocked":
						// Two ways to be refused, and only one of them
						// is the state machine's. An action that needs
						// an issued card is refused on authority even
						// from a state that allows it.
						// Refused on state, refused for want of a card,
						// or refused at its ceiling. Only the first is
						// something this state walk can check.
						if states[state] && !needsCard[step.ActionName] {
							t.Fatalf("step %d: attempt %q expects blocked but it IS allowed from state %q — the demo would not show a refusal",
								i, step.ActionName, state)
						}
					default:
						t.Fatalf("step %d: attempt %q must declare expect: allowed|blocked (got %q)", i, step.ActionName, step.Expect)
					}
					// A refusal on an action that requires a card has
					// to say which refusal it is. "Refused" covers no
					// card issued, a ceiling reached, a forbidden state
					// and a missing permission, and only one of those
					// is the story a card demo tells — without the
					// reason, any of them would pass as the demo
					// working.
					if step.Expect == "blocked" && needsCard[step.ActionName] && step.ExpectReason == "" {
						t.Fatalf("step %d: attempt %q expects a refusal on an action that requires an issued card; declare expect_reason so the run cannot pass on the wrong refusal",
							i, step.ActionName)
					}
				default:
					t.Fatalf("step %d: unknown kind %q", i, step.Kind)
				}
			}
		})
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
