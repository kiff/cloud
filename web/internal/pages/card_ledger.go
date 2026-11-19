package pages

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// ledgerRow is one action on a Card's statement. The API records one
// movement per quantity (an action under a Card capping both the amount
// and the number of actions draws twice), so a reader saw every action
// twice. A row is the action, with what it moved in each quantity.
type ledgerRow struct {
	ProposalID, ExceptionID string
	Action, EntityID        string
	At                      time.Time
	Amounts                 map[string]int64
}

// drawRows groups a statement's draws by the proposal that drew them,
// in the order the statement lists them.
func drawRows(draws []apiclient.MandateDraw) []ledgerRow {
	var rows []ledgerRow
	at := map[string]int{}
	for _, d := range draws {
		i, ok := at[d.ProposalID]
		if !ok || d.ProposalID == "" {
			i = len(rows)
			at[d.ProposalID] = i
			rows = append(rows, ledgerRow{ProposalID: d.ProposalID, Action: d.Action, EntityID: d.EntityID, At: d.DrawnAt, Amounts: map[string]int64{}})
		}
		rows[i].Amounts[d.Quantity] += d.Amount
	}
	return rows
}

// approvedRows groups what the owner approved once outside the Card by
// the held action it answered, most recent first.
func approvedRows(es []apiclient.ApprovedOutsideEntry) []ledgerRow {
	var rows []ledgerRow
	at := map[string]int{}
	for _, e := range es {
		key := e.ExceptionID
		if key == "" {
			key = e.ProposalID
		}
		i, ok := at[key]
		if !ok || key == "" {
			i = len(rows)
			at[key] = i
			rows = append(rows, ledgerRow{ProposalID: e.ProposalID, ExceptionID: e.ExceptionID, Action: e.Action, EntityID: e.EntityID, At: e.RecordedAt, Amounts: map[string]int64{}})
		}
		rows[i].Amounts[e.Quantity] += e.Amount
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].At.After(rows[j].At) })
	return rows
}

// ledgerQuantities are the columns of a ledger: the Card's own limits in
// their order, then any other quantity the rows carry.
func ledgerQuantities(limits []apiclient.MandateLimit, rows []ledgerRow) []string {
	var qs []string
	seen := map[string]bool{}
	add := func(q string) {
		if !seen[q] {
			seen[q] = true
			qs = append(qs, q)
		}
	}
	for _, l := range limits {
		add(l.Quantity)
	}
	var rest []string
	for _, r := range rows {
		for q := range r.Amounts {
			if !seen[q] {
				rest = append(rest, q)
			}
		}
	}
	sort.Strings(rest)
	for _, q := range rest {
		add(q)
	}
	// A quantity no row moved is an empty column.
	var out []string
	for _, q := range qs {
		for _, r := range rows {
			if _, ok := r.Amounts[q]; ok {
				out = append(out, q)
				break
			}
		}
	}
	return out
}

// quantityLabel names a quantity for a column head: "count" is the
// number of actions, "sum(amount_cents)" is the amount.
func quantityLabel(q string) string {
	if q == "count" {
		return "Actions"
	}
	q = strings.TrimSuffix(strings.TrimPrefix(q, "sum("), ")")
	q = strings.TrimSuffix(q, "_cents")
	return sentenceCase(strings.ReplaceAll(q, "_", " "))
}

// ledgerCell is one row's value in a quantity, or a dash when the action
// did not move it.
func ledgerCell(r ledgerRow, q string, sign string) string {
	n, ok := r.Amounts[q]
	if !ok {
		return "—"
	}
	return sign + authorityValue(n, q)
}

// approvedTotals says what was approved once outside the Card in total,
// one phrase per quantity: "2 actions, 40 amount". The totals map is
// unordered, so the phrases follow the ledger's columns.
func approvedTotals(st *apiclient.MandateStatement) string {
	if st == nil || st.ApprovedOutside == nil {
		return ""
	}
	rows := approvedRows(st.ApprovedOutside.Entries)
	var parts []string
	for _, q := range ledgerQuantities(st.Limits, rows) {
		total, ok := st.ApprovedOutside.Totals[q]
		if !ok {
			continue
		}
		if q == "count" {
			parts = append(parts, countOf(total, "action", "actions"))
			continue
		}
		parts = append(parts, authorityValue(total, q)+" "+strings.ToLower(quantityLabel(q)))
	}
	if len(parts) == 0 {
		return countOf(int64(len(rows)), "action", "actions")
	}
	return strings.Join(parts, ", ")
}

func countOf(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.FormatInt(n, 10) + " " + many
}

// shortID keeps the start of a long call id, which is enough to tell
// calls apart; the full id is in the title and on the linked page.
func shortID(id string) string {
	if r := []rune(id); len(r) > 14 {
		return string(r[:14]) + "…"
	}
	return id
}

// ledgerOutcomes is what the Approved once ledger knows of each held
// action: the answered holds by id, or that they could not be read. A
// failed read keeps the column and says so: dropping it would make a
// partial ledger look complete.
type ledgerOutcomes struct {
	byID       map[string]apiclient.CardException
	unreadable bool
}

// approvedOutcome is what became of an action approved once, read from
// its held action: whether the approval was used and what the tool did.
// A row whose held action is not among those read says so rather than
// leaving the cell blank.
func approvedOutcome(o ledgerOutcomes, id string) (label string, warn bool) {
	if o.unreadable {
		return "Could not be read", true
	}
	e, ok := o.byID[id]
	if !ok || id == "" {
		return "Answer record unavailable", true
	}
	return answeredOutcomeLabel(e), e.ToolOutcome != nil && toolOutcomeWarns(e.ToolOutcome.Status)
}

// approvedCount includes charged exceptions and historical outside-Card entries.
func approvedCount(st *apiclient.MandateStatement) int {
	if st == nil {
		return 0
	}
	var entries []apiclient.ApprovedOutsideEntry
	if st.ApprovedOutside != nil {
		entries = append(entries, st.ApprovedOutside.Entries...)
	}
	if st.ApprovedExceptions != nil {
		entries = append(entries, st.ApprovedExceptions.Entries...)
	}
	return len(approvedRows(entries))
}

func historicalApprovedCount(st *apiclient.MandateStatement) int {
	if st == nil || st.ApprovedOutside == nil {
		return 0
	}
	return len(approvedRows(st.ApprovedOutside.Entries))
}

// ledgerAction names the row's action; a draw recorded before the ledger
// carried the action stays unnamed rather than invented.
func ledgerAction(r ledgerRow) string {
	if r.Action == "" {
		return "unnamed movement"
	}
	return r.Action
}

// ledgerColumns is the grid class for a ledger with n quantity columns,
// and an outcome column for approvals.
func ledgerColumns(n int, outcome bool) string {
	if n > 3 {
		n = 3
	}
	c := "ac-ledger-q" + strconv.Itoa(n)
	if outcome {
		c += " ac-ledger-outcomes"
	}
	return c
}
