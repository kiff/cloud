package evidence

import (
	"context"
	"errors"
	"log/slog"
	"sort"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
)

// ErrControlNotFound is returned by Compose when a ControlID scope
// resolves to no control for the tenant (the Control Room maps this to
// a 404). The proof-pack (action) scope never returns it — a missing
// control there is tolerated.
var ErrControlNotFound = errors.New("evidence: control not found")

// receiptCap bounds the receipt list pulled for filtering. Matches the
// proof pack's prior cap so Phase 1 stays byte-identical; the scoped
// per-decision read is the Phase 2 improvement.
const receiptCap = 10000

// collectDecisions reads the window-complete counts and the capped row
// sample, computing the precise Truncated flag (full-window total >
// returned sample). The query's scope (action vs control_id) is the
// caller's; this collector only owns the count/sample/truncation
// semantics so both surfaces share them.
func (c *Composer) collectDecisions(ctx context.Context, tenantID string, q proposals.DecisionQuery) (DecisionsSection, error) {
	if c.reader == nil {
		return DecisionsSection{Status: StatusUnavailable, Counts: map[string]int{}, Rows: []proposals.DecisionRow{}}, nil
	}
	// Counts are window-complete across ALL outcomes, independent of any
	// outcome filter on the row query: the summary tiles need every
	// outcome's count even while the rows are filtered to one. Stripping
	// Outcomes for the count query is what stops the tiles collapsing to 0
	// when a filter is active (the row query below still honors the filter).
	countQ := q
	countQ.Outcomes = nil
	counts, windowTotal, err := c.reader.CountDecisionsByOutcome(ctx, tenantID, countQ)
	if err != nil {
		return DecisionsSection{}, err
	}
	rows, err := c.reader.ListDecisionsInWindow(ctx, tenantID, q)
	if err != nil {
		return DecisionsSection{}, err
	}
	if counts == nil {
		counts = map[string]int{}
	}
	if rows == nil {
		rows = []proposals.DecisionRow{}
	}
	// The matching total for truncation reflects the active outcome filter
	// (the sum of the filtered outcomes' window-complete counts), else the
	// window total — so Truncated is precise for whatever the rows show.
	matchTotal := windowTotal
	if len(q.Outcomes) > 0 {
		matchTotal = 0
		for _, o := range q.Outcomes {
			matchTotal += counts[o]
		}
	}
	sec := DecisionsSection{
		Status:    StatusComplete,
		Counts:    counts,
		Total:     windowTotal,
		Truncated: matchTotal > len(rows),
		Rows:      rows,
	}
	if windowTotal == 0 {
		sec.Status = StatusEmpty
	}
	return sec, nil
}

// collectByAction returns one window-complete decision tally per action
// the control governs. It reuses CountDecisionsByOutcome per action
// (scoped to the base query's control + window), so it needs no new
// store method and works identically for the memory and Postgres
// readers. The outcome filter and row limit are stripped — each entry is
// the full per-action, per-outcome count over the window. A per-action
// read error is skipped (best-effort), not fatal: the breakdown is
// decoration on the decisions payload, never the payload itself.
func (c *Composer) collectByAction(ctx context.Context, tenantID string, base proposals.DecisionQuery, actions []string) []ActionCounts {
	if c.reader == nil || len(actions) == 0 {
		return nil
	}
	out := make([]ActionCounts, 0, len(actions))
	for _, action := range actions {
		q := base
		q.Action = action
		q.Outcomes = nil
		q.Limit = 0
		q.Offset = 0
		counts, total, err := c.reader.CountDecisionsByOutcome(ctx, tenantID, q)
		if err != nil {
			c.logger.WarnContext(ctx, "evidence: per-action count failed",
				slog.String("tenant", tenantID), slog.String("action", action), slog.String("err", err.Error()))
			continue
		}
		if counts == nil {
			counts = map[string]int{}
		}
		out = append(out, ActionCounts{Action: action, Counts: counts, Total: total})
	}
	return out
}

// collectReceipts lists the tenant's receipts and filters to the given
// actions + window, tallying signed/anchored, newest-first. Phase 1
// reproduces the proof pack's List(cap)+Go-filter exactly so the
// artifact is byte-stable; the scoped per-decision read
// (GetByProposalID) is the Phase 2 improvement applied to both
// renderers at once.
func (c *Composer) collectReceipts(ctx context.Context, tenantID string, actions []string, window Window) (ReceiptsSection, error) {
	if c.receipts == nil {
		return ReceiptsSection{Status: StatusUnavailable, Samples: []receipt.Receipt{}}, nil
	}
	rows, err := c.receipts.List(ctx, tenantID, receiptCap)
	if err != nil {
		return ReceiptsSection{}, err
	}
	want := map[string]bool{}
	for _, a := range actions {
		want[a] = true
	}
	sec := ReceiptsSection{Status: StatusComplete, Scope: ScopeWindow, Samples: []receipt.Receipt{}}
	for _, rec := range rows {
		if len(want) > 0 && !want[rec.ActionName] {
			continue
		}
		if rec.IssuedAt.Before(window.From) || rec.IssuedAt.After(window.To) {
			continue
		}
		sec.Samples = append(sec.Samples, rec)
		sec.Count++
		if rec.Attestation != nil && rec.Attestation.Signature != "" {
			sec.SignedCount++
		}
		if rec.Attestation != nil && rec.Attestation.AnchorTxHash != "" {
			sec.AnchoredCount++
		}
	}
	// Newest-first within the section for readability (matches the
	// proof pack's prior sort).
	sort.Slice(sec.Samples, func(i, j int) bool {
		return sec.Samples[i].IssuedAt.After(sec.Samples[j].IssuedAt)
	})
	if sec.Count == 0 {
		sec.Status = StatusEmpty
	}
	return sec, nil
}

// collectReceiptsForDecisions resolves control-scoped receipts by tying
// each decision row to its receipt via the explicit proposal->receipt
// link (receipt.GetByProposalID) — the mechanical association
// that lets the Control Room show receipts for *this* control without
// the cross-control overclaim the action+window list risked.
//
// It enriches the (capped) decision sample, so the section covers the
// decisions shown, not a window-complete receipt list; Truncated
// mirrors the decisions sample being capped. Decisions without a
// receipt (blocked / approval-required / observe) are simply skipped.
// Synthetic demo proof-run receipts are excluded.
//
// Receipts are a secondary section on the Control Room: a hard store
// error degrades the section to Unavailable (logged) rather than
// failing the decisions payload, matching the primary/secondary policy.
func (c *Composer) collectReceiptsForDecisions(ctx context.Context, tenantID string, rows []proposals.DecisionRow, decisionsTruncated bool) ReceiptsSection {
	if c.receipts == nil {
		return ReceiptsSection{Status: StatusUnavailable, Samples: []receipt.Receipt{}}
	}
	sec := ReceiptsSection{Status: StatusComplete, Scope: ScopeSample, Samples: []receipt.Receipt{}, Truncated: decisionsTruncated}
	seen := map[string]bool{}
	for _, row := range rows {
		pid := row.ProposalID
		if pid == "" || seen[pid] {
			continue
		}
		seen[pid] = true
		rec, err := c.receipts.GetByProposalID(ctx, tenantID, pid)
		if errors.Is(err, receipt.ErrReceiptNotFound) {
			continue // no receipt for this decision (blocked/approval/observe)
		}
		if err != nil {
			c.logger.WarnContext(ctx, "evidence: receipt lookup failed",
				slog.String("tenant", tenantID), slog.String("proposal_id", pid), slog.String("err", err.Error()))
			return ReceiptsSection{Status: StatusUnavailable, Samples: []receipt.Receipt{}}
		}
		sec.Samples = append(sec.Samples, rec)
		sec.Count++
		if rec.Attestation != nil && rec.Attestation.Signature != "" {
			sec.SignedCount++
		}
		if rec.Attestation != nil && rec.Attestation.AnchorTxHash != "" {
			sec.AnchoredCount++
		}
	}
	sort.Slice(sec.Samples, func(i, j int) bool {
		return sec.Samples[i].IssuedAt.After(sec.Samples[j].IssuedAt)
	})
	if sec.Count == 0 {
		sec.Status = StatusEmpty
	}
	return sec
}

// collectDomain assembles the contract / state machine / policy from
// the tenant's active domain YAML. Unavailable when no source/registry
// is wired (the Control Room path). Best-effort like the handler it
// replaces: a load/parse failure leaves the section unavailable rather
// than failing the whole composition.
func (c *Composer) collectDomain(ctx context.Context, tenantID, action string) DomainSection {
	if c.source == nil || c.registry == nil {
		return DomainSection{Status: StatusUnavailable}
	}
	td, err := c.source.Load(ctx, tenantID)
	if err != nil {
		return DomainSection{Status: StatusUnavailable}
	}
	res, err := domain.ParseAndBuild(td.YAML, c.registry)
	if err != nil {
		return DomainSection{Status: StatusUnavailable}
	}
	sec := DomainSection{Status: StatusComplete}
	for i := range res.Spec.Actions {
		if res.Spec.Actions[i].Name == action {
			a := res.Spec.Actions[i]
			sec.Contract = &a
			break
		}
	}
	sec.States = res.Spec.States
	sec.Transitions = res.Spec.Transitions
	sec.Policy = relevantPolicy(res.Spec.Permissions.Roles, sec.Contract)
	return sec
}

// relevantPolicy narrows the role→permissions map to roles that grant a
// permission the action requires; falls back to the whole policy when
// the contract declares none. Moved verbatim from the proof-pack
// handler so the artifact's policy block is byte-identical (note the
// nil-on-empty semantics, which omitempty relies on).
func relevantPolicy(roles map[string][]string, contract *domain.ActionSpec) map[string][]string {
	if len(roles) == 0 {
		return nil
	}
	if contract == nil || len(contract.RequiredPermissions) == 0 {
		out := make(map[string][]string, len(roles))
		for k, v := range roles {
			out[k] = append([]string(nil), v...)
		}
		return out
	}
	required := map[string]bool{}
	for _, p := range contract.RequiredPermissions {
		required[p] = true
	}
	out := map[string][]string{}
	for role, perms := range roles {
		for _, p := range perms {
			if required[p] {
				out[role] = append([]string(nil), perms...)
				break
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
