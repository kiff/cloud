package pages

import (
	"strconv"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// This file implements the verify-the-loop reconciliation: "is what I'm
// governing real?" answered from cloud-side data only — the active
// contract's declared executors reconciled against the runtime's observed
// decisions. See the slice-4 feasibility resolution in
// the authenticated-surface design for what the
// cloud can and cannot attest. The cloud attests the gate (what it
// decided and recorded); it does not attest a client-run action's in-code
// side effect — that lives in the customer's codebase.

// cloudProxyExecutor is the executor key the cloud emits for actions
// whose side effect runs in the customer's own code (executors/proxy.go).
// Any other executor key on an ACTIVE domain is, by the apply-time
// validation invariant, a cloud-registered executor — so the key string
// alone classifies backing kind without registry introspection.
const cloudProxyExecutor = "cloud.proxy"

// DomainVerifyBacking classifies where an action's executor runs.
type DomainVerifyBacking int

const (
	// BackingUnknown means the action carries no executor key. On an
	// active domain this should not happen (validation rejects it); it
	// is surfaced as drift if it ever does.
	BackingUnknown DomainVerifyBacking = iota
	// BackingCloud means a cloud-registered executor key — the runtime
	// runs the side effect.
	BackingCloud
	// BackingClient means cloud.proxy — KIFF is the gate and the side
	// effect runs in the customer's code.
	BackingClient
)

// DomainVerifyRow is one action's declared-vs-observed reconciliation.
type DomainVerifyRow struct {
	ActionName  string
	Executor    string
	Backing     DomainVerifyBacking
	Observed    bool       // a decision for this action appeared in the sample
	Decisions   int        // decisions for this action within the sample
	ReceiptSeen bool       // at least one sampled decision carried a receipt
	LastSeen    *time.Time // most recent sampled decision for this action
}

// DomainVerify is the verify-the-loop section model. Available is false
// when the evidence read could not be reached; the section then renders
// an honest unavailable state rather than implying "nothing observed."
// Rows reconcile every declared action, in declaration order.
type DomainVerify struct {
	Available bool
	Rows      []DomainVerifyRow
}

// BuildDomainVerify reconciles the active domain's declared actions
// against observed decision rows. rows is a recent sample (newest-first,
// capped by the caller), so counts are "recent activity," not
// window-complete totals — the surface frames them that way.
//
// Passing nil rows is valid and meaningful: every declared action
// renders as backed-but-not-yet-observed (Available stays true). Callers
// set Available=false only when the evidence read itself failed.
func BuildDomainVerify(actions []apiclient.DomainAction, rows []apiclient.ProposalDecision) DomainVerify {
	type agg struct {
		count    int
		receipt  bool
		lastSeen time.Time
	}
	observed := map[string]*agg{}
	for _, row := range rows {
		a := observed[row.ActionName]
		if a == nil {
			a = &agg{}
			observed[row.ActionName] = a
		}
		a.count++
		if row.ReceiptTraceID != "" {
			a.receipt = true
		}
		if row.CreatedAt.After(a.lastSeen) {
			a.lastSeen = row.CreatedAt
		}
	}

	out := DomainVerify{Available: true, Rows: make([]DomainVerifyRow, 0, len(actions))}
	for _, act := range actions {
		row := DomainVerifyRow{
			ActionName: act.Name,
			Executor:   act.Executor,
			Backing:    classifyBacking(act.Executor),
		}
		if a := observed[act.Name]; a != nil && a.count > 0 {
			row.Observed = true
			row.Decisions = a.count
			row.ReceiptSeen = a.receipt
			if !a.lastSeen.IsZero() {
				ls := a.lastSeen
				row.LastSeen = &ls
			}
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

func classifyBacking(executor string) DomainVerifyBacking {
	switch executor {
	case "":
		return BackingUnknown
	case cloudProxyExecutor:
		return BackingClient
	default:
		return BackingCloud
	}
}

// OK reports whether every declared action is in a healthy loop state:
// cloud-run actions need no traffic to be sound (the runtime backs
// them); client-run actions are only proven connected once the gate has
// been exercised. An unbacked action is never OK.
func (v DomainVerify) OK() bool {
	for _, r := range v.Rows {
		if !r.LoopClosed() {
			return false
		}
	}
	return len(v.Rows) > 0
}

// LoopClosed reports whether this action's loop is demonstrably sound
// from the cloud's vantage. Cloud-run executors are backed by the
// runtime, so they are closed regardless of traffic. Client-run
// (cloud.proxy) actions are only proven closed once a decision has been
// observed — that is the evidence the customer's code is calling the
// gate. Unbacked actions are never closed.
func (r DomainVerifyRow) LoopClosed() bool {
	switch r.Backing {
	case BackingCloud:
		return true
	case BackingClient:
		return r.Observed
	default:
		return false
	}
}

// VerdictLabel is the per-action one-word status.
func (r DomainVerifyRow) VerdictLabel() string {
	switch r.Backing {
	case BackingCloud:
		if r.Observed {
			return "Live"
		}
		return "Ready"
	case BackingClient:
		if r.Observed {
			return "Connected"
		}
		return "Not yet connected"
	default:
		return "Unbacked"
	}
}

// VerdictPillClass maps the verdict to a pill style.
func (r DomainVerifyRow) VerdictPillClass() string {
	switch {
	case r.Backing == BackingUnknown:
		return "dash-pill dash-pill-warn"
	case r.LoopClosed():
		return "dash-pill dash-pill-ok"
	default:
		return "dash-pill dash-pill-muted"
	}
}

// BackingLabel describes where the side effect runs.
func (r DomainVerifyRow) BackingLabel() string {
	switch r.Backing {
	case BackingCloud:
		return "Runs in the cloud runtime"
	case BackingClient:
		return "Runs in your code"
	default:
		return "No executor bound"
	}
}

// ObservedDetail is the human sentence about observed traffic for this
// action, honest about the sample scope.
func (r DomainVerifyRow) ObservedDetail() string {
	if !r.Observed {
		switch r.Backing {
		case BackingClient:
			return "no recent decisions: your code hasn't asked KIFF about this action lately"
		case BackingCloud:
			return "no recent decisions for this action"
		default:
			return "no executor: the contract must name one before this action can be governed"
		}
	}
	detail := strconv.Itoa(r.Decisions) + " recent " + pluralize(r.Decisions, "decision", "decisions")
	if r.ReceiptSeen {
		detail += " · receipts recorded"
	}
	if r.LastSeen != nil {
		detail += " · last " + timeAgo(*r.LastSeen)
	}
	return detail
}
