package mandate_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
)

// A total per item: "one reminder a week for each invoice".

var perInvoice = mandate.Quantity{Kind: mandate.KindCount, Per: "invoice_id"}

// reminderCard sends reminders, at most one a week for each invoice and
// ten a day in all, and lists invoices, which names none.
func reminderCard(onExceed string) mandate.Mandate {
	return mandate.Mandate{
		ID: "card-remind", TenantID: tenantID, Subject: "agent://refund-agent", Domain: "billing", Environment: "prod",
		Grants: []mandate.Grant{{Action: "SEND_REMINDER"}, {Action: "LIST_INVOICES"}},
		Aggregates: []mandate.Aggregate{
			{Quantity: perInvoice, Limit: 1, Window: mandate.WindowRollingWeek},
			{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 10, Window: mandate.WindowCalendarDay},
		},
		OnExceed:  onExceed,
		ValidFrom: base.Add(-24 * time.Hour), ValidUntil: base.Add(60 * 24 * time.Hour),
	}
}

// remind is a reminder call on invoice ("" names none); its action declares invoice_id.
func remind(invoice string) mandate.Request {
	r := req("agent://refund-agent", "SEND_REMINDER", "", nil)
	r.Declares = map[string]bool{"invoice_id": true, "to": true}
	if invoice != "" {
		r.Resources = map[string]string{"invoice_id": invoice}
	}
	return r
}

func listInvoices() mandate.Request {
	r := req("agent://refund-agent", "LIST_INVOICES", "", nil)
	r.Declares = map[string]bool{"status": true}
	return r
}

func (r *rig) authorize(proposalID string, q mandate.Request) mandate.Decision {
	r.t.Helper()
	d, err := r.a.Authorize(context.Background(), tenantID, proposalID, q)
	if err != nil {
		r.t.Fatalf("authorize %s: %v", proposalID, err)
	}
	return d
}

func (r *rig) count(card string) int64 {
	r.t.Helper()
	n, err := r.store.Used(context.Background(), tenantID, card, mandate.Quantity{Kind: mandate.KindCount}.Key(), base.Add(-48*time.Hour), "")
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

// Invariant: a total per item counts each item apart, over its own window.
// Category: authority
// Attack: send a second reminder on the same invoice within the week, or count one invoice's reminders against another's
// Refused: the second on in_1 passes its total; in_2 has its own; in_1 is free again a week later
func TestPerItemTotal(t *testing.T) {
	r := newRig(t, reminderCard(""))
	if d := r.authorize("p1", remind("in_1")); !d.Allowed() {
		t.Fatalf("first on in_1: %s (%s)", d.Outcome, d.Reason)
	}
	d := r.authorize("p2", remind("in_1"))
	if d.Outcome != mandate.OutcomeLimitReached || d.Quantity.Per != "invoice_id" {
		t.Fatalf("second on in_1: %s %+v", d.Outcome, d.Quantity)
	}
	if !strings.Contains(d.Reason, `for invoice_id "in_1"`) || !strings.Contains(d.Reason, "rolling_7d") {
		t.Errorf("reason = %q", d.Reason)
	}
	if d := r.authorize("p3", remind("in_2")); !d.Allowed() {
		t.Fatalf("in_2 counts apart: %s (%s)", d.Outcome, d.Reason)
	}
	// The Card's plain total still counts every call, whichever invoice.
	if n := r.count("card-remind"); n != 2 {
		t.Errorf("plain count = %d, want 2", n)
	}
	r.clock = r.clock.Add(7*24*time.Hour + time.Minute)
	if d := r.authorize("p4", remind("in_1")); !d.Allowed() {
		t.Fatalf("a week later: %s (%s)", d.Outcome, d.Reason)
	}
}

// Invariant: an item is the same whatever its case or spacing, and a revision of the Card keeps what each item used.
// Category: authority
// Attack: send a second reminder within the week to " Ana@example.com" after one to "ana@example.com", or after the owner edits the Card
// Refused: both are the same item; the second passes its total, and a Card revised under its id still counts what in_1 used
func TestPerItemTotalCountsOneItemWhateverItsCaseOrTheCardsRevision(t *testing.T) {
	r := newRig(t, reminderCard(""))
	if d := r.authorize("c1", remind("ana@example.com")); !d.Allowed() {
		t.Fatalf("first: %s (%s)", d.Outcome, d.Reason)
	}
	for i, same := range []string{"Ana@example.com", " ana@example.com ", "ANA@example.com"} {
		d := r.authorize("c2-"+string(rune('a'+i)), remind(same))
		if d.Outcome != mandate.OutcomeLimitReached {
			t.Fatalf("%q: %s (%s)", same, d.Outcome, d.Reason)
		}
		if !strings.Contains(d.Reason, same) {
			t.Errorf("the reason names the value as sent: %q", d.Reason)
		}
	}
	// The owner edits the Card: a revision under the same id, a higher daily total.
	m := reminderCard("")
	m.Aggregates = append([]mandate.Aggregate(nil), m.Aggregates...)
	m.Aggregates[1].Limit = 20
	if err := r.store.Issue(context.Background(), m, "owner@x"); err != nil {
		t.Fatal(err)
	}
	if d := r.authorize("c3", remind("ana@example.com")); d.Outcome != mandate.OutcomeLimitReached {
		t.Fatalf("after a revision: %s (%s)", d.Outcome, d.Reason)
	}
}

// Invariant: a per-item total holds a call its action could name the item on and doesn't.
// Category: authority
// Attack: leave the optional invoice argument out, or send it empty, to get past the per-invoice total
// Refused: the call is outside the Card (resource_not_on_card), never counted as within it
func TestPerItemTotalHoldsACallNamingNone(t *testing.T) {
	m := reminderCard("")
	for _, invoice := range []string{"", " "} {
		q := remind("")
		if invoice != "" {
			q.Resources = map[string]string{"invoice_id": invoice}
		}
		d := evaluate(t, []mandate.Mandate{m}, q, 0)
		if d.Outcome != mandate.OutcomeResourceOutside || !strings.Contains(d.Reason, "names no invoice_id") {
			t.Fatalf("invoice %q: %s (%s)", invoice, d.Outcome, d.Reason)
		}
	}
}

// Invariant: a per-item total applies only to the actions that declare its parameter.
// Category: authority
// Attack: list invoices more than ten times a day on a Card that counts reminders per invoice, hoping the per-invoice total leaves the listing uncounted
// Refused: the listing isn't counted or held by the per-invoice total, and the Card's daily total of ten refuses the eleventh
func TestPerItemTotalLeavesOtherActionsToTheCard(t *testing.T) {
	r := newRig(t, reminderCard(""))
	for i := 0; i < 10; i++ {
		if d := r.authorize("list-"+string(rune('a'+i)), listInvoices()); !d.Allowed() {
			t.Fatalf("list %d: %s (%s)", i, d.Outcome, d.Reason)
		}
	}
	// The plain total of ten a day is what stops the eleventh.
	if d := r.authorize("list-k", listInvoices()); d.Outcome != mandate.OutcomeLimitReached || d.Quantity.Per != "" {
		t.Fatalf("eleventh: %s %+v", d.Outcome, d.Quantity)
	}
}

// Invariant: a value too long to match is still counted, by a digest of what was sent.
// Category: authority
// Attack: send an invoice id longer than a resource may be, so it can't be read, twice in a week
// Refused: the second passes the per-invoice total
func TestPerItemTotalCountsAnUnreadableValue(t *testing.T) {
	r := newRig(t, reminderCard(""))
	long := remind("")
	long.Unreadable = map[string]string{"invoice_id": "sha256:abc"}
	if d := r.authorize("u1", long); !d.Allowed() {
		t.Fatalf("first: %s (%s)", d.Outcome, d.Reason)
	}
	if d := r.authorize("u2", long); d.Outcome != mandate.OutcomeLimitReached {
		t.Fatalf("second: %s (%s)", d.Outcome, d.Reason)
	}
}

// Invariant: a held call on a per-item total names its item, and changing the Card raises that total.
// Category: authority
// Attack: ask for extra room until a date on a call held past a per-invoice total, to widen it for every invoice for a while
// Refused: room for a set time is refused (ErrNoRoomPerItem); the hold names invoice_id in_1, and only changing the Card raises the limit, to 2
func TestPerItemHold(t *testing.T) {
	r := newRig(t, reminderCard(mandate.OnExceedOwner))
	r.authorize("h1", remind("in_1"))
	d := r.authorize("h2", remind("in_1"))
	if d.Outcome != mandate.OutcomeOwnerRequired || len(d.Breaches) != 1 {
		t.Fatalf("held: %s %+v", d.Outcome, d.Breaches)
	}
	b := d.Breaches[0]
	if b.Outcome != mandate.OutcomeLimitReached || b.Resource != "invoice_id" || b.Value != "in_1" || b.Quantity.Per != "invoice_id" {
		t.Fatalf("breach = %+v", b)
	}
	ctx := context.Background()
	if _, err := r.store.ChangeCardRoom(ctx, tenantID, d.ExceptionID, "owner@x", r.clock, mandate.RoomUntilDate, r.clock.Add(48*time.Hour)); !errors.Is(err, mandate.ErrNoRoomPerItem) {
		t.Fatalf("room until a date: %v", err)
	}
	if _, err := r.store.ChangeCard(ctx, tenantID, d.ExceptionID, "owner@x", r.clock); err != nil {
		t.Fatalf("change the Card: %v", err)
	}
	ms, err := r.store.List(ctx, tenantID)
	if err != nil || len(ms) != 1 {
		t.Fatal(ms, err)
	}
	if got := ms[0].Aggregates[0]; got.Limit != 2 || got.Quantity.Per != "invoice_id" || len(got.ExtraRoom) != 0 {
		t.Fatalf("after the change: %+v", got)
	}
}

func TestPerItemKeyNeverKeepsTheValue(t *testing.T) {
	if k := perInvoice.Key(); k != "count per invoice_id" {
		t.Errorf("key without a call = %q", k)
	}
	if q, per := mandate.SplitKey("sum(amount) per to=0123abcd"); q != "sum(amount)" || per != "to" {
		t.Errorf("split = %q %q", q, per)
	}
	if q, per := mandate.SplitKey("count"); q != "count" || per != "" {
		t.Errorf("plain split = %q %q", q, per)
	}
	if mandate.WindowRollingWeek != "rolling_7d" {
		t.Fatal("window name")
	}
	start, err := mandate.WindowRollingWeek.Start(base, nil)
	if err != nil || !start.Equal(base.Add(-7*24*time.Hour)) {
		t.Errorf("start = %v, %v", start, err)
	}
}

func TestPerItemTotalIsValidatedAgainstTheActions(t *testing.T) {
	actions := []domain.ActionSpec{
		{Name: "SEND_REMINDER", Parameters: []domain.ParameterSpec{{Name: "invoice_id", Type: "string"}, {Name: "attempt", Type: "integer"}}},
		{Name: "LIST_INVOICES", Parameters: []domain.ParameterSpec{{Name: "status", Type: "string"}}},
	}
	if err := mandate.Validate(reminderCard(""), actions); err != nil {
		t.Fatalf("valid: %v", err)
	}
	for name, change := range map[string]func(*mandate.Mandate){
		"undeclared":   func(m *mandate.Mandate) { m.Aggregates[0].Quantity.Per = "customer" },
		"not a string": func(m *mandate.Mandate) { m.Aggregates[0].Quantity.Per = "attempt" },
		"two names":    func(m *mandate.Mandate) { m.Aggregates[0].Quantity.Per = "invoice_id status" },
		"per action": func(m *mandate.Mandate) {
			m.Grants[0].PerAction = []mandate.PerActionLimit{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "attempt", Per: "invoice_id"}, Max: 1}}
		},
	} {
		m := reminderCard("")
		m.Aggregates = append([]mandate.Aggregate(nil), m.Aggregates...)
		m.Grants = append([]mandate.Grant(nil), m.Grants...)
		change(&m)
		if err := mandate.Validate(m, actions); !errors.Is(err, mandate.ErrInvalidMandate) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The per-item total round-trips through Postgres, and its draws are read
// back per item there too: a second reminder on in_1 within the week is
// refused, one on in_2 isn't.
func TestPostgres_PerItemTotal(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, tenantID)
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := store.Issue(ctx, reminderCard(""), "owner"); err != nil {
		t.Fatal(err)
	}
	ms, err := store.ForSubject(ctx, tenantID, "agent://refund-agent")
	if err != nil || len(ms) != 1 || ms[0].Aggregates[0].Quantity.Per != "invoice_id" || ms[0].Aggregates[0].Window != mandate.WindowRollingWeek {
		t.Fatalf("load: %+v %v", ms, err)
	}
	a := mandate.NewAuthorizer(store, nil, at(base))
	for _, c := range []struct {
		id, invoice string
		allowed     bool
	}{{"pg-1", "in_1", true}, {"pg-2", "in_1", false}, {"pg-3", "in_2", true}} {
		d, err := a.Authorize(ctx, tenantID, c.id, remind(c.invoice))
		if err != nil || d.Allowed() != c.allowed {
			t.Fatalf("%s on %s: %s (%s) %v", c.id, c.invoice, d.Outcome, d.Reason, err)
		}
	}
	if n, _ := store.Used(ctx, tenantID, "card-remind", "count", base.Add(-48*time.Hour), ""); n != 2 {
		t.Fatalf("plain count = %d, want 2", n)
	}
}
