package billing_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

func catalog(t *testing.T) packs.Catalog {
	t.Helper()
	c, err := packs.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func packEvent(id, typ, tenantID, pack, payment, status string) billing.Event {
	var e billing.Event
	e.ID, e.Type = id, typ
	o := &e.Data.Object
	o.ID, o.Mode, o.PaymentStatus, o.PaymentIntent, o.Customer = "cs_"+id, "payment", status, payment, "cus_1"
	o.Created = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC).Unix()
	o.Metadata = map[string]string{billing.MetaTenant: tenantID, billing.MetaPack: pack}
	return e
}

// heard records the alerts a processor sends.
type heard struct {
	mu     sync.Mutex
	events []alert.Event
}

func (h *heard) Notify(ev alert.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, ev)
}

func (h *heard) all() []alert.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]alert.Event(nil), h.events...)
}

// A paid pack checkout records its pack once, with its tasks from the pack
// table, and answers the member's request it carries, telling the member
// once; a full refund takes it back.
func TestPackPaymentRecordsThePack(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	store, tt := newTenantStoreWith(t, "acme")
	ledger := packs.NewMemory()
	bs := billing.NewMemoryStore()
	told := &heard{}
	p := billing.NewProcessor(bs, store, nil).WithPacks(ledger, catalog(t)).WithRequestAlerts(ledger, told).
		WithClock(func() time.Time { return now })

	req, err := ledger.Ask(ctx, packs.Request{TenantID: tt.ID, AskedBy: "member", Pack: "rayo", CreatedAt: now.AddDate(0, 0, -3), ExpiresAt: now.AddDate(0, 0, 9)})
	if err != nil {
		t.Fatal(err)
	}
	// Not paid yet (a delayed payment method): nothing recorded.
	if res, err := p.Process(ctx, packEvent("evt_unpaid", "checkout.session.completed", tt.ID, "rayo", "pi_1", "unpaid")); err != nil || !res.Ignored {
		t.Fatalf("unpaid: %+v %v", res, err)
	}
	paid := packEvent("evt_paid", "checkout.session.async_payment_succeeded", tt.ID, "rayo", "pi_1", "paid")
	paid.Data.Object.Metadata[billing.MetaRequest] = req.ID
	paid.Data.Object.Metadata[billing.MetaAnsweredBy] = "admin"
	if res, err := p.Process(ctx, paid); err != nil || res.Action != "pack" || res.TenantID != tt.ID {
		t.Fatalf("paid: %+v %v", res, err)
	}
	valid, _ := ledger.Valid(ctx, tt.ID, now)
	if len(valid) != 1 || valid[0].Tasks != 70 || valid[0].Source != packs.SourceStripe || !valid[0].BoughtAt.Equal(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("recorded: %+v", valid)
	}
	if r, _ := ledger.Get(ctx, tt.ID, req.ID, now); r.Status != packs.RequestApproved || r.AnsweredBy != "admin" {
		t.Fatalf("request: %+v", r)
	}
	if ev := told.all(); len(ev) != 1 || ev[0].Outcome != alert.PackRequestAnswered || ev[0].EntityID != req.ID || ev[0].TenantID != tt.ID ||
		len(ev[0].Reasons) != 2 || ev[0].Reasons[0] != "rayo" || ev[0].Reasons[1] != packs.RequestApproved {
		t.Fatalf("the member isn't told: %+v", ev)
	}
	if got, _ := bs.TenantForCustomer(ctx, "cus_1"); got != tt.ID {
		t.Fatalf("the customer isn't linked: %q", got)
	}
	// The same payment through another event records nothing more.
	if res, err := p.Process(ctx, packEvent("evt_again", "checkout.session.completed", tt.ID, "rayo", "pi_1", "paid")); err != nil || !res.Duplicate {
		t.Fatalf("again: %+v %v", res, err)
	}
	if valid, _ := ledger.Valid(ctx, tt.ID, now); len(valid) != 1 {
		t.Fatalf("recorded twice: %+v", valid)
	}
	if ev := told.all(); len(ev) != 1 {
		t.Fatalf("told again: %+v", ev)
	}
	// A pack the table doesn't have, or no account: recorded for
	// reconciliation, never guessed.
	for name, e := range map[string]billing.Event{
		"unknown pack": packEvent("evt_unknown", "checkout.session.completed", tt.ID, "giga", "pi_2", "paid"),
		"no account":   packEvent("evt_noacct", "checkout.session.completed", tenant.NewID(), "pico", "pi_3", "paid"),
	} {
		if res, err := p.Process(ctx, e); err != nil || !res.Unmapped {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
	}

	// A partial refund keeps the pack; a full one takes it back.
	refund := func(id string, full bool) billing.Event {
		var e billing.Event
		e.ID, e.Type = id, "charge.refunded"
		e.Data.Object.PaymentIntent, e.Data.Object.Refunded = "pi_1", full
		return e
	}
	if res, err := p.Process(ctx, refund("evt_partial", false)); err != nil || !res.Ignored {
		t.Fatalf("partial: %+v %v", res, err)
	}
	if res, err := p.Process(ctx, refund("evt_full", true)); err != nil || res.Action != "pack refunded" {
		t.Fatalf("full: %+v %v", res, err)
	}
	if valid, _ := ledger.Valid(ctx, tt.ID, now); len(valid) != 0 {
		t.Fatalf("a refunded pack still counts: %+v", valid)
	}
	// rayo has no Stripe price any more: a take-back goes by the payment,
	// never by the table's price.
	if p, _ := catalog(t).Pack("rayo"); p.StripePrice != "" {
		t.Fatalf("rayo is sold again: %+v", p)
	}
}

// A pack of credits is sold in the app stores only: one paid through
// Stripe all the same is recorded for reconciliation, never as tasks.
func TestCreditPackPaidThroughStripe(t *testing.T) {
	ctx := context.Background()
	c, err := packs.ParseCatalog([]byte(`{"packs": [
		{"id": "pico", "name": "Pico", "tasks": 12, "stripe_price": "kiff_pack_pico", "apple_product": "bot.tafs.pack.pico"},
		{"id": "c-test", "name": "Test", "credits": 500, "apple_product": "bot.tafs.credits.test"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	store, tt := newTenantStoreWith(t, "acme")
	ledger := packs.NewMemory()
	p := billing.NewProcessor(billing.NewMemoryStore(), store, nil).WithPacks(ledger, c)
	if res, err := p.Process(ctx, packEvent("evt_credits", "checkout.session.completed", tt.ID, "c-test", "pi_c", "paid")); err != nil || !res.Unmapped {
		t.Fatalf("a pack of credits: %+v %v", res, err)
	}
	valid, _ := ledger.Valid(ctx, tt.ID, time.Now())
	credits, _ := ledger.CreditPacks(ctx, tt.ID)
	if len(valid) != 0 || len(credits) != 0 {
		t.Fatalf("recorded: %+v %+v", valid, credits)
	}
}

// A lost dispute takes a pack back like a full refund; a won one leaves it.
// A refund or a lost dispute that comes before its payment is kept: the
// pack is recorded taken back when the payment arrives, with no tasks and
// answering no request.
func TestPackTakeBacks(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	store, tt := newTenantStoreWith(t, "acme")
	ledger := packs.NewMemory()
	told := &heard{}
	p := billing.NewProcessor(billing.NewMemoryStore(), store, nil).WithPacks(ledger, catalog(t)).WithRequestAlerts(ledger, told).
		WithClock(func() time.Time { return now })
	dispute := func(id, payment, status string) billing.Event {
		var e billing.Event
		e.ID, e.Type = id, "charge.dispute.closed"
		e.Data.Object.ID, e.Data.Object.PaymentIntent, e.Data.Object.Status = "du_"+id, payment, status
		return e
	}
	refund := func(id, payment string) billing.Event {
		var e billing.Event
		e.ID, e.Type = id, "charge.refunded"
		e.Data.Object.PaymentIntent, e.Data.Object.Refunded = payment, true
		return e
	}
	left := func() int {
		valid, _ := ledger.Valid(ctx, tt.ID, now)
		n := 0
		for _, v := range valid {
			n += v.TasksLeft
		}
		return n
	}

	// Opened, won, then lost.
	if _, err := p.Process(ctx, packEvent("evt_a", "checkout.session.completed", tt.ID, "rayo", "pi_a", "paid")); err != nil {
		t.Fatal(err)
	}
	opened := dispute("evt_opened", "pi_a", "needs_response")
	opened.Type = "charge.dispute.created"
	if res, err := p.Process(ctx, opened); err != nil || !res.Ignored || left() != 70 {
		t.Fatalf("opened: %+v %v, %d left", res, err, left())
	}
	if res, err := p.Process(ctx, dispute("evt_won", "pi_a", "won")); err != nil || !res.Ignored || left() != 70 {
		t.Fatalf("won: %+v %v, %d left", res, err, left())
	}
	if res, err := p.Process(ctx, dispute("evt_lost", "pi_a", "lost")); err != nil || res.Action != "pack dispute lost" || res.TenantID != tt.ID || left() != 0 {
		t.Fatalf("lost: %+v %v, %d left", res, err, left())
	}

	// Taken back first, by a refund or a lost dispute, then paid: recorded
	// taken back. The member's request it carried stays open, unanswered.
	req, _ := ledger.Ask(ctx, packs.Request{TenantID: tt.ID, AskedBy: "member", Pack: "rayo", CreatedAt: now.AddDate(0, 0, -1), ExpiresAt: now.AddDate(0, 0, 9)})
	for name, first := range map[string]billing.Event{"refund": refund("evt_r", "pi_r"), "dispute": dispute("evt_d", "pi_d", "lost")} {
		res, err := p.Process(ctx, first)
		if err != nil || res.Ignored || !strings.Contains(res.Action, "kept") {
			t.Fatalf("%s first: %+v %v", name, res, err)
		}
		paid := packEvent("evt_paid_"+name, "checkout.session.completed", tt.ID, "rayo", first.Data.Object.PaymentIntent, "paid")
		paid.Data.Object.Metadata[billing.MetaRequest] = req.ID
		if _, err := p.Process(ctx, paid); err != nil {
			t.Fatalf("%s, then paid: %v", name, err)
		}
		if left() != 0 {
			t.Fatalf("%s came first, yet the pack counts: %d left", name, left())
		}
	}
	if r, _ := ledger.Get(ctx, tt.ID, req.ID, now); r.Status != packs.RequestOpen {
		t.Fatalf("a pack taken back answered the request: %+v", r)
	}
	if ev := told.all(); len(ev) != 0 {
		t.Fatalf("told of a pack taken back: %+v", ev)
	}
	// A pack paid later on another payment counts as usual.
	if _, err := p.Process(ctx, packEvent("evt_b", "checkout.session.completed", tt.ID, "rayo", "pi_b", "paid")); err != nil || left() != 70 {
		t.Fatalf("another payment: %v, %d left", err, left())
	}
}

// A Stripe subscription sets the billing source; one that ends clears it.
// On an account Apple bills, a Stripe subscription that ends ends nothing.
// The retired task overage price is dropped on the subscription's update.
func TestSubscriptionsSetTheBillingSource(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	store, tt := newTenantStoreWith(t, "acme")
	subs := &droppingSubs{fakeSubs: fakeSubs{subs: map[string]billing.Subscription{}}}
	p := billing.NewProcessor(billing.NewMemoryStore(), store, nil).WithSubscriptions(subs).WithClock(func() time.Time { return now })
	start, end := now.AddDate(0, 0, -1), now.AddDate(0, 1, -1)

	active := sub("sub_1", tt.ID, "active", billing.PriceSoloMonthly, 1, time.Time{}, start, end)
	old := billing.SubscriptionItem{ID: "si_task"}
	old.Price.LookupKey = billing.PriceTaskOverage
	active.Items.Data = append(active.Items.Data, old)
	subs.subs["sub_1"] = active
	if _, err := p.Process(ctx, subEvent("evt_1", "customer.subscription.updated", "sub_1")); err != nil {
		t.Fatal(err)
	}
	if got := metaOf(t, store, tt.ID, tenant.MetadataKeyBillingSource); got != "stripe" {
		t.Fatalf("source: %q", got)
	}
	if len(subs.dropped) != 1 || subs.dropped[0] != "sub_1" {
		t.Fatalf("task overage dropped from %v", subs.dropped)
	}

	subs.subs["sub_1"] = sub("sub_1", tt.ID, "canceled", billing.PriceSoloMonthly, 1, time.Time{}, start, end)
	if _, err := p.Process(ctx, subEvent("evt_2", "customer.subscription.deleted", "sub_1")); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(ctx, tt.ID)
	if tenant.BillingSourceOf(got) != tenant.BillingNone {
		t.Fatalf("an ended plan keeps its source: %q", tenant.BillingSourceOf(got))
	}

	// The account moved to the app: Apple bills its Solo now.
	for k, v := range map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "apple"} {
		if err := store.SetMetadata(ctx, tt.ID, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := p.Process(ctx, subEvent("evt_3", "customer.subscription.deleted", "sub_1")); err != nil || !res.Ignored {
		t.Fatalf("an old Stripe subscription ending: %+v %v", res, err)
	}
	if l := planOf(t, store, tt.ID); l.Canonical != tenant.PlanSolo || metaOf(t, store, tt.ID, tenant.MetadataKeyBillingSource) != "apple" {
		t.Fatalf("Apple's plan was ended by Stripe: %+v", l)
	}
}

type droppingSubs struct {
	fakeSubs
	dropped []string
}

func (d *droppingSubs) DropTaskOverage(_ context.Context, s billing.Subscription) (bool, error) {
	for _, it := range s.Items.Data {
		if it.Price.LookupKey == billing.PriceTaskOverage {
			d.dropped = append(d.dropped, s.ID)
			return true, nil
		}
	}
	return false, nil
}

// stripeAPI is Stripe's REST API as the client calls it, recording each
// request's method, path and form.
type stripeAPI struct {
	mu    sync.Mutex
	calls []stripeCall
	sub   string // the subscription GET answers with
	// failDelete makes every DELETE fail, as when Stripe refuses it.
	failDelete bool
	// missing are lookup keys Stripe has no active price for.
	missing map[string]bool
}

type stripeCall struct {
	method, path string
	form         url.Values
}

func (s *stripeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	if r.Method == http.MethodGet {
		form = r.URL.Query()
	}
	s.mu.Lock()
	s.calls = append(s.calls, stripeCall{r.Method, r.URL.Path, form})
	s.mu.Unlock()
	switch {
	case s.failDelete && r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error": {"message": "refused"}}`)
	case r.URL.Path == "/v1/prices" && s.missing[form.Get("lookup_keys[]")]:
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{}})
	case r.URL.Path == "/v1/prices":
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "price_" + form.Get("lookup_keys[]")}}})
	case r.URL.Path == "/v1/checkout/sessions":
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "https://checkout.stripe.test/s"})
	case strings.HasPrefix(r.URL.Path, "/v1/subscriptions/"):
		_, _ = io.WriteString(w, s.sub)
	default:
		_, _ = io.WriteString(w, "{}")
	}
}

func (s *stripeAPI) find(method, path string) []stripeCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []stripeCall
	for _, c := range s.calls {
		if c.method == method && c.path == path {
			out = append(out, c)
		}
	}
	return out
}

// A new subscription carries the plan and the action overage price only;
// a pack's checkout is a one-time payment that carries the account, the
// pack and the request it answers; the retired task overage price is
// dropped from an existing subscription when its seats change.
func TestStripeCheckoutsAndTheRetiredTaskPrice(t *testing.T) {
	ctx := context.Background()
	api := &stripeAPI{sub: `{"id": "sub_1", "items": {"data": [
		{"id": "si_team", "quantity": 2, "price": {"lookup_key": "kiff_team_monthly"}},
		{"id": "si_task", "price": {"lookup_key": "kiff_task_overage"}},
		{"id": "si_action", "price": {"lookup_key": "kiff_action_overage"}}]}}`}
	srv := httptest.NewServer(api)
	defer srv.Close()
	s := billing.NewStripe("sk_test_x", billing.WithAPIBase(srv.URL))

	// Solo is sold in the app stores: no web checkout.
	if _, err := s.CreateCheckout(ctx, billing.Checkout{TenantID: "t1", Plan: "solo", SuccessURL: "https://app/ok", CancelURL: "https://app/no"}); err == nil {
		t.Fatal("a Solo checkout was opened on the web")
	}
	// Company: its seats, at least one, and both metered prices.
	if _, err := s.CreateCheckout(ctx, billing.Checkout{TenantID: "t1", Plan: "team", SuccessURL: "https://app/ok", CancelURL: "https://app/no"}); err != nil {
		t.Fatal(err)
	}
	sessions := api.find(http.MethodPost, "/v1/checkout/sessions")
	if len(sessions) != 1 {
		t.Fatalf("sessions: %+v", sessions)
	}
	f := sessions[0].form
	if f.Get("line_items[0][price]") != "price_kiff_team_monthly" || f.Get("line_items[0][quantity]") != "1" ||
		f.Get("line_items[1][price]") != "price_kiff_action_overage" || f.Get("line_items[2][price]") != "price_kiff_credit_overage" ||
		f.Get("line_items[3][price]") != "" {
		t.Fatalf("subscription items: %v", f)
	}
	if f.Get("subscription_data[metadata][kiff_apple_credit]") != "" {
		t.Fatalf("a plain checkout marked an apple credit: %v", f)
	}

	// Team on the App Store Solo days already paid for: they end the trial,
	// and the subscription says they're a credit.
	until := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	if _, err := s.CreateCheckout(ctx, billing.Checkout{TenantID: "t1", Plan: "team", Seats: 3, TrialEnd: until, AppleCredit: true,
		SuccessURL: "https://app/ok", CancelURL: "https://app/no"}); err != nil {
		t.Fatal(err)
	}
	if f := api.find(http.MethodPost, "/v1/checkout/sessions")[1].form; f.Get("subscription_data[trial_end]") != strconv.FormatInt(until.Unix(), 10) ||
		f.Get("subscription_data[metadata][kiff_apple_credit]") != "1" {
		t.Fatalf("credited team: %v", f)
	}

	if _, err := s.CreatePackCheckout(ctx, billing.PackCheckout{TenantID: "t1", Pack: "rayo", PriceKey: "kiff_pack_rayo", RequestID: "pr_1", AnsweredBy: "admin",
		SuccessURL: "https://app/ok", CancelURL: "https://app/no"}); err != nil {
		t.Fatal(err)
	}
	f = api.find(http.MethodPost, "/v1/checkout/sessions")[2].form
	for k, want := range map[string]string{
		"mode": "payment", "line_items[0][price]": "price_kiff_pack_rayo", "line_items[0][quantity]": "1",
		"metadata[kiff_tenant]": "t1", "metadata[kiff_pack]": "rayo", "metadata[kiff_request]": "pr_1",
		"payment_intent_data[metadata][kiff_pack]": "rayo", "payment_intent_data[metadata][kiff_tenant]": "t1",
		"customer_creation": "always", "automatic_tax[enabled]": "true",
	} {
		if got := f.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if f.Get("subscription_data[metadata][kiff_tenant]") != "" {
		t.Error("a pack's checkout made a subscription")
	}

	if err := s.SetSeats(ctx, "sub_1", 3); err != nil {
		t.Fatal(err)
	}
	if got := api.find(http.MethodDelete, "/v1/subscription_items/si_task"); len(got) != 1 || got[0].form.Get("proration_behavior") != "none" {
		t.Fatalf("task overage not dropped: %+v", api.calls)
	}
	if got := api.find(http.MethodPost, "/v1/subscription_items/si_team"); len(got) != 1 || got[0].form.Get("quantity") != "3" {
		t.Fatalf("seats: %+v", got)
	}
	if got := api.find(http.MethodDelete, "/v1/subscription_items/si_action"); len(got) != 0 {
		t.Fatal("the action overage price was dropped")
	}
	// A Company subscription from before credits gains the credit overage.
	if got := api.find(http.MethodPost, "/v1/subscription_items"); len(got) != 1 || got[0].form.Get("subscription") != "sub_1" ||
		got[0].form.Get("price") != "price_kiff_credit_overage" || got[0].form.Get("proration_behavior") != "none" {
		t.Fatalf("credit overage not added: %+v", got)
	}
}

// Company is never sold without its credit overage: a catalog without the
// price fails the checkout, and nothing is opened.
func TestCompanyCheckoutNeedsTheCreditOverage(t *testing.T) {
	api := &stripeAPI{missing: map[string]bool{billing.PriceCreditOverage: true}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	s := billing.NewStripe("sk_test_x", billing.WithAPIBase(srv.URL))
	if _, err := s.CreateCheckout(context.Background(), billing.Checkout{TenantID: "t1", Plan: "team", Seats: 2,
		SuccessURL: "https://app/ok", CancelURL: "https://app/no"}); err == nil {
		t.Fatal("a Company checkout opened without the credit overage price")
	}
	if got := api.find(http.MethodPost, "/v1/checkout/sessions"); len(got) != 0 {
		t.Fatalf("a session was opened: %+v", got)
	}
}

// Dropping the retired task overage price never holds a seat change back:
// Stripe refusing it still leaves the seats set, and says so.
func TestSeatsAreSetWhenTheTaskOverageCantBeDropped(t *testing.T) {
	ctx := context.Background()
	api := &stripeAPI{failDelete: true, sub: `{"id": "sub_1", "items": {"data": [
		{"id": "si_team", "quantity": 2, "price": {"lookup_key": "kiff_team_monthly"}},
		{"id": "si_task", "price": {"lookup_key": "kiff_task_overage"}}]}}`}
	srv := httptest.NewServer(api)
	defer srv.Close()
	s := billing.NewStripe("sk_test_x", billing.WithAPIBase(srv.URL))
	err := s.SetSeats(ctx, "sub_1", 4)
	if err == nil || !strings.Contains(err.Error(), "the seats are set") {
		t.Fatalf("err = %v", err)
	}
	if got := api.find(http.MethodPost, "/v1/subscription_items/si_team"); len(got) != 1 || got[0].form.Get("quantity") != "4" {
		t.Fatalf("seats: %+v", api.calls)
	}
}
