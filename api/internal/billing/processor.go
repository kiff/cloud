package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

// Tenant metadata keys the processor writes. plan reuses the
// canonical key the limiter and dashboard already read; the rest are
// billing bookkeeping, never exposed on a public surface.
const (
	MetaStripeCustomerID     = "stripe_customer_id"
	MetaStripeSubscriptionID = "stripe_subscription_id"
	MetaBillingStatus        = "billing_status"
)

// Billing statuses written to MetaBillingStatus. These are
// informational/operational: the proposal limiter reads only the
// plan, never billing_status, so a past_due tenant keeps its paid
// allowance and overage behavior — a payment problem is a billing
// conversation, never a runtime limit_exceeded outage.
const (
	StatusActive   = "active"
	StatusPastDue  = "past_due"
	StatusCanceled = "canceled"
)

// metadataKeyPlan is the canonical plan key (tenant.MetadataKeyPlan).
const metadataKeyPlan = tenant.MetadataKeyPlan

// Result describes what Process did, for the handler's response and
// operational logging. Exactly one of Duplicate/Unmapped/Handled is
// the meaningful signal; Ignored marks an event type we accept but
// do not act on.
type Result struct {
	EventID   string
	EventType string
	TenantID  string
	Action    string // human-readable: "activated", "canceled", "grace", "renewed"
	Duplicate bool   // already processed — no-op
	Unmapped  bool   // could not resolve to a tenant — recorded for reconciliation
	Ignored   bool   // event type not acted on
}

// Processor applies verified Stripe events to tenant plan state.
type Processor struct {
	store   Store
	tenants tenant.Store
	logger  *slog.Logger
	// subs reads a subscription as Stripe has it now.
	// Nil: subscription events change only the billing status.
	subs SubscriptionReader
	// packs records the packs paid for, from the pack table catalog. Nil:
	// a pack's payment is recorded for reconciliation only.
	packs   packs.Ledger
	catalog packs.Catalog
	// requests and alerts tell a member their request was approved when
	// a pack bought for it is recorded. Nil: nobody is told.
	requests packs.Requests
	alerts   alert.Notifier
	now      func() time.Time
}

// WithRequestAlerts tells the account's alert targets when a pack bought
// for a member's request approves it.
func (p *Processor) WithRequestAlerts(requests packs.Requests, n alert.Notifier) *Processor {
	p.requests, p.alerts = requests, n
	return p
}

// TaskOverageDropper removes the retired task overage price from a
// subscription; *Stripe is one.
type TaskOverageDropper interface {
	DropTaskOverage(ctx context.Context, sub Subscription) (bool, error)
}

// WithPacks makes a completed pack payment record its pack in ledger, with
// its tasks from catalog, and a refund take it back.
func (p *Processor) WithPacks(ledger packs.Ledger, catalog packs.Catalog) *Processor {
	p.packs, p.catalog = ledger, catalog
	return p
}

// SubscriptionReader reads a Stripe subscription; *Stripe is one.
type SubscriptionReader interface {
	Subscription(ctx context.Context, id string) (Subscription, error)
}

// WithSubscriptions makes subscription events set the account's plan from
// the subscription as Stripe has it now, so their order doesn't matter.
func (p *Processor) WithSubscriptions(r SubscriptionReader) *Processor {
	if r != nil {
		p.subs = r
	}
	return p
}

// WithClock sets the clock, for tests.
func (p *Processor) WithClock(now func() time.Time) *Processor {
	p.now = now
	return p
}

// NewProcessor wires the billing store and the tenant store. Both
// required.
func NewProcessor(store Store, tenants tenant.Store, logger *slog.Logger) *Processor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Processor{store: store, tenants: tenants, logger: logger, now: time.Now}
}

// Process handles one verified event idempotently. A duplicate
// delivery (same event id) is a no-op. An event that cannot be mapped
// to a tenant is recorded as unmapped (for operator reconciliation),
// not an error — returning an error would make Stripe retry a
// genuinely unmappable event forever. A real failure (store write
// error) returns an error so Stripe retries.
//
// Ordering matters for money state. We check Seen BEFORE applying and
// MarkProcessed only AFTER a successful apply:
//
//   - mark-after-apply: if the apply fails (DB blip, mid-request
//     restart, context cancel) the event stays unmarked, the handler
//     returns 500, and the Stripe retry re-applies it. Marking first
//     would silently dedupe the retry and lose the write — a
//     customer charged with no plan change, or a lost cancellation.
//   - seen-first: an already-applied event (including a later one
//     that superseded it) is skipped, so a redelivery cannot clobber
//     newer state with stale state.
//
// The seen-then-mark pair is not a single atomic transaction, but the
// applies are absolute and idempotent, so two concurrent
// first-deliveries that both pass Seen re-apply the same state
// harmlessly before both mark.
func (p *Processor) Process(ctx context.Context, e Event) (Result, error) {
	res := Result{EventID: e.ID, EventType: e.Type}

	seen, err := p.store.Seen(ctx, e.ID)
	if err != nil {
		return res, fmt.Errorf("billing: idempotency check: %w", err)
	}
	if seen {
		res.Duplicate = true
		return res, nil
	}

	res, err = p.apply(ctx, e, res)
	if err != nil {
		// Apply failed: do NOT mark. The Stripe retry will re-run it.
		return res, err
	}

	// Apply succeeded (including intentional no-ops like unmapped /
	// ignored): record the event so a later redelivery is skipped.
	if _, mErr := p.store.MarkProcessed(ctx, e.ID); mErr != nil {
		return res, fmt.Errorf("billing: mark processed: %w", mErr)
	}
	return res, nil
}

// apply routes a (not-yet-processed) event to its handler. Pure of
// idempotency bookkeeping — Process owns the seen/mark ordering.
func (p *Processor) apply(ctx context.Context, e Event, res Result) (Result, error) {
	switch e.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		if e.Data.Object.Mode == "payment" && e.Data.Object.Metadata[MetaPack] != "" {
			return p.packPaid(ctx, e, res)
		}
		if e.Type != "checkout.session.completed" {
			res.Ignored = true
			return res, nil
		}
		if t := e.Data.Object.Metadata[MetaTenant]; t != "" && p.subs != nil {
			return p.checkedOut(ctx, e, res, t)
		}
		return p.activate(ctx, e, res)
	case "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted",
		"customer.subscription.paused", "customer.subscription.resumed":
		if p.subs != nil {
			return p.syncSubscription(ctx, e, res, e.Data.Object.ID)
		}
	}
	switch e.Type {
	case "customer.subscription.deleted":
		return p.setStatusByCustomer(ctx, e, res, StatusCanceled, "canceled", tenant.PlanTrial)
	case "customer.subscription.updated":
		// Status drives grace vs recovery; plan is left untouched.
		switch strings.ToLower(e.Data.Object.Status) {
		case StatusPastDue, "unpaid", "incomplete":
			return p.setStatusByCustomer(ctx, e, res, StatusPastDue, "grace", "")
		case StatusActive:
			return p.setStatusByCustomer(ctx, e, res, StatusActive, "renewed", "")
		default:
			res.Ignored = true
			return res, nil
		}
	case "invoice.payment_failed":
		return p.setStatusByCustomer(ctx, e, res, StatusPastDue, "grace", "")
	case "invoice.paid", "invoice.payment_succeeded":
		return p.setStatusByCustomer(ctx, e, res, StatusActive, "renewed", "")
	case "charge.refunded":
		return p.packRefunded(ctx, e, res)
	case "charge.dispute.created":
		// The pack stays until the dispute closes; said so, for support.
		if e.Data.Object.PaymentIntent != "" {
			p.logger.WarnContext(ctx, "billing: a payment is disputed; any pack it bought stays until the dispute closes",
				slog.String("event", e.ID), slog.String("payment", e.Data.Object.PaymentIntent))
		}
		res.Ignored = true
		return res, nil
	case "charge.dispute.closed":
		return p.packDisputeClosed(ctx, e, res)
	default:
		res.Ignored = true
		return res, nil
	}
}

// activate handles checkout.session.completed: the only event that
// carries the tenant_slug correlation. The plan comes from the
// session metadata copied off our own Payment Link (server-
// controlled — never the buyer's word); the tenant is resolved
// against our own store by the buyer-entered slug.
func (p *Processor) activate(ctx context.Context, e Event, res Result) (Result, error) {
	obj := e.Data.Object

	rawPlan := strings.TrimSpace(obj.Metadata["kiff_plan"])
	if rawPlan == "" {
		// No catalog plan on the session — cannot safely activate.
		p.logger.WarnContext(ctx, "billing: checkout completed without kiff_plan metadata; recorded for reconciliation",
			slog.String("event", e.ID), slog.String("customer", obj.Customer))
		res.Unmapped = true
		return res, nil
	}
	canonical := tenant.CanonicalPlan(tenant.Plan(rawPlan))

	slug := obj.CustomField("tenant_slug")
	if slug == "" {
		p.logger.WarnContext(ctx, "billing: checkout completed without tenant_slug; recorded for reconciliation",
			slog.String("event", e.ID), slog.String("customer", obj.Customer))
		res.Unmapped = true
		return res, nil
	}
	t, err := p.tenants.GetBySlug(ctx, slug)
	if err != nil {
		if err == tenant.ErrNotFound {
			p.logger.WarnContext(ctx, "billing: checkout tenant_slug matched no tenant; recorded for reconciliation",
				slog.String("event", e.ID), slog.String("slug", slug), slog.String("customer", obj.Customer))
			res.Unmapped = true
			return res, nil
		}
		return res, fmt.Errorf("billing: resolve tenant by slug: %w", err)
	}

	// Persist the correlation first so later lifecycle events (which
	// carry only the customer id) resolve to this tenant.
	if obj.Customer != "" {
		if err := p.store.LinkCustomer(ctx, obj.Customer, t.ID); err != nil {
			return res, fmt.Errorf("billing: link customer: %w", err)
		}
	}
	if err := p.setMeta(ctx, t.ID, metadataKeyPlan, string(canonical)); err != nil {
		return res, err
	}
	if obj.Customer != "" {
		if err := p.setMeta(ctx, t.ID, MetaStripeCustomerID, obj.Customer); err != nil {
			return res, err
		}
	}
	if obj.Subscription != "" {
		if err := p.setMeta(ctx, t.ID, MetaStripeSubscriptionID, obj.Subscription); err != nil {
			return res, err
		}
	}
	if err := p.setMeta(ctx, t.ID, MetaBillingStatus, StatusActive); err != nil {
		return res, err
	}

	res.TenantID = t.ID
	res.Action = "activated"
	p.logger.InfoContext(ctx, "billing: tenant activated",
		slog.String("event", e.ID), slog.String("tenant", t.ID),
		slog.String("plan", string(canonical)), slog.String("customer", obj.Customer))
	return res, nil
}

// setStatusByCustomer resolves the tenant from the persisted
// customer link and writes billing_status. When downgradePlan is
// non-empty (cancellation) it also flips the plan; otherwise the
// plan is left untouched (grace/renewal change only the status, never
// the runtime limit).
func (p *Processor) setStatusByCustomer(ctx context.Context, e Event, res Result, status, action string, downgradePlan tenant.Plan) (Result, error) {
	customer := e.Data.Object.Customer
	if customer == "" {
		res.Unmapped = true
		return res, nil
	}
	tenantID, err := p.store.TenantForCustomer(ctx, customer)
	if err != nil {
		return res, fmt.Errorf("billing: resolve tenant for customer: %w", err)
	}
	if tenantID == "" {
		p.logger.WarnContext(ctx, "billing: lifecycle event for unmapped customer; recorded for reconciliation",
			slog.String("event", e.ID), slog.String("type", e.Type), slog.String("customer", customer))
		res.Unmapped = true
		return res, nil
	}

	if downgradePlan != "" {
		if err := p.setMeta(ctx, tenantID, metadataKeyPlan, string(downgradePlan)); err != nil {
			return res, err
		}
	}
	if err := p.setMeta(ctx, tenantID, MetaBillingStatus, status); err != nil {
		return res, err
	}

	res.TenantID = tenantID
	res.Action = action
	p.logger.InfoContext(ctx, "billing: status updated",
		slog.String("event", e.ID), slog.String("type", e.Type),
		slog.String("tenant", tenantID), slog.String("billing_status", status),
		slog.String("action", action))
	return res, nil
}

func (p *Processor) setMeta(ctx context.Context, tenantID, key, value string) error {
	if err := p.tenants.SetMetadata(ctx, tenantID, key, value); err != nil {
		return fmt.Errorf("billing: set %s: %w", key, err)
	}
	return nil
}

// checkedOut is a checkout KIFF opened: it names its
// account, so the customer is linked to it, and the subscription it made
// sets the plan.
func (p *Processor) checkedOut(ctx context.Context, e Event, res Result, tenantID string) (Result, error) {
	obj := e.Data.Object
	if _, err := p.tenants.Get(ctx, tenantID); err != nil {
		if err == tenant.ErrNotFound {
			p.logger.WarnContext(ctx, "billing: checkout names no account; recorded for reconciliation",
				slog.String("event", e.ID), slog.String("tenant", tenantID))
			res.Unmapped = true
			return res, nil
		}
		return res, fmt.Errorf("billing: read account: %w", err)
	}
	if err := p.link(ctx, tenantID, obj.Customer); err != nil {
		return res, err
	}
	if obj.Subscription == "" {
		res.TenantID, res.Action = tenantID, "linked"
		return res, nil
	}
	return p.syncSubscription(ctx, e, res, obj.Subscription)
}

// link records that customer is tenantID's, for the events that carry
// only the customer.
func (p *Processor) link(ctx context.Context, tenantID, customer string) error {
	if customer == "" {
		return nil
	}
	if err := p.store.LinkCustomer(ctx, customer, tenantID); err != nil {
		return fmt.Errorf("billing: link customer: %w", err)
	}
	return p.setMeta(ctx, tenantID, MetaStripeCustomerID, customer)
}

// syncSubscription sets the account's plan from subscription id as Stripe
// has it now:
//
//   - trialing: the trial, ending when Stripe's trial ends;
//   - active, past due: Solo or Team, with Team's seats and the period;
//   - canceled, unpaid, ended: the trial, ended, so KIFF refuses the
//     account's calls until a plan is chosen;
//   - incomplete: nothing yet (the checkout isn't finished).
func (p *Processor) syncSubscription(ctx context.Context, e Event, res Result, id string) (Result, error) {
	if id == "" {
		res.Ignored = true
		return res, nil
	}
	sub, err := p.subs.Subscription(ctx, id)
	if err != nil {
		return res, fmt.Errorf("billing: read subscription: %w", err)
	}
	tenantID := sub.Metadata[MetaTenant]
	if tenantID == "" && sub.Customer != "" {
		if tenantID, err = p.store.TenantForCustomer(ctx, sub.Customer); err != nil {
			return res, fmt.Errorf("billing: resolve tenant for customer: %w", err)
		}
	}
	if tenantID == "" {
		p.logger.WarnContext(ctx, "billing: subscription for no account; recorded for reconciliation",
			slog.String("event", e.ID), slog.String("subscription", id))
		res.Unmapped = true
		return res, nil
	}
	if err := p.link(ctx, tenantID, sub.Customer); err != nil {
		return res, err
	}
	if err := p.setMeta(ctx, tenantID, MetaStripeSubscriptionID, sub.ID); err != nil {
		return res, err
	}
	plan, seats, _ := sub.Plan()
	status := strings.ToLower(sub.Status)
	t, err := p.tenants.Get(ctx, tenantID)
	if err != nil {
		return res, fmt.Errorf("billing: read account: %w", err)
	}
	starts := status == "trialing" || status == StatusActive || status == StatusPastDue
	// Team's first days on an account that moved from App Store Solo: the
	// Solo days it already paid for, credited as Team days.
	// Stripe calls them a trial; the account runs as Team.
	credited := status == "trialing" && sub.Metadata[MetaAppleCredit] == "1" && plan == string(tenant.PlanTeam)
	if tenant.BillingSourceOf(t) == tenant.BillingApple && credited {
		p.logger.InfoContext(ctx, "billing: team starts on the solo days apple was paid for",
			slog.String("event", e.ID), slog.String("tenant", tenantID))
	} else if src := tenant.BillingSourceOf(t); src == tenant.BillingApple || src == tenant.BillingGoogle {
		if !starts {
			// Apple or Google Play bills this account's plan now: a Stripe
			// subscription that ended (switching from kiff.dev to the app)
			// ends nothing.
			p.logger.InfoContext(ctx, "billing: stripe subscription ended on an account an app store bills; plan kept",
				slog.String("event", e.ID), slog.String("tenant", tenantID), slog.String("status", sub.Status))
			res.TenantID, res.Ignored = tenantID, true
			return res, nil
		}
		// One source per account: a plan bought on kiff.dev while Apple
		// bills one is a person paying twice. Stripe's is applied, and
		// the overlap logged for support to cancel one.
		p.logger.WarnContext(ctx, "billing: stripe subscription on an account an app store bills; the account is paying twice",
			slog.String("event", e.ID), slog.String("tenant", tenantID), slog.String("source", string(src)), slog.String("status", sub.Status))
	}
	if d, ok := p.subs.(TaskOverageDropper); ok && starts {
		// The retired task overage price goes on the subscription's next
		// update. Best effort: the next event tries again.
		if dropped, derr := d.DropTaskOverage(ctx, sub); derr != nil {
			p.logger.WarnContext(ctx, "billing: drop task overage failed", slog.String("subscription", sub.ID), slog.String("err", derr.Error()))
		} else if dropped {
			p.logger.InfoContext(ctx, "billing: task overage dropped", slog.String("tenant", tenantID), slog.String("subscription", sub.ID))
		}
	}
	set := func(key string, value any) error {
		if err := p.tenants.SetMetadata(ctx, tenantID, key, value); err != nil {
			return fmt.Errorf("billing: set %s: %w", key, err)
		}
		return nil
	}
	res.TenantID = tenantID
	switch {
	case credited:
		err = errors.Join(set(metadataKeyPlan, plan), set(MetaBillingStatus, "trialing"),
			set(tenant.MetadataKeyBillingSource, string(tenant.BillingStripe)),
			set(tenant.MetadataKeySeats, max(seats, tenant.TeamMinSeats)))
		if sub.TrialEnd > 0 {
			err = errors.Join(err, set(tenant.MetadataKeyPeriodStart, p.now().UTC().Format(time.RFC3339)),
				set(tenant.MetadataKeyPeriodEnd, time.Unix(sub.TrialEnd, 0).UTC().Format(time.RFC3339)))
		}
		res.Action = "team_credited"
	case status == "trialing":
		err = errors.Join(set(metadataKeyPlan, string(tenant.PlanTrial)),
			set(tenant.MetadataKeyTrialEndsAt, time.Unix(sub.TrialEnd, 0).UTC().Format(time.RFC3339)),
			set(MetaBillingStatus, "trialing"), set(tenant.MetadataKeyBillingSource, string(tenant.BillingStripe)))
		// The trial's start is kept once stamped: a checkout in the middle
		// of a trial goes on with it, and its counts.
		if t, gerr := p.tenants.Get(ctx, tenantID); gerr != nil {
			err = errors.Join(err, fmt.Errorf("billing: read account: %w", gerr))
		} else if _, ok := t.Metadata[tenant.MetadataKeyTrialStartedAt]; !ok && sub.TrialStart > 0 {
			err = errors.Join(err, set(tenant.MetadataKeyTrialStartedAt, time.Unix(sub.TrialStart, 0).UTC().Format(time.RFC3339)))
		}
		res.Action = "trialing"
	case status == StatusActive || status == StatusPastDue:
		if plan == "" {
			p.logger.WarnContext(ctx, "billing: subscription without a v1 plan; recorded for reconciliation",
				slog.String("event", e.ID), slog.String("subscription", id))
			res.Unmapped = true
			return res, nil
		}
		err = errors.Join(set(metadataKeyPlan, plan), set(MetaBillingStatus, status),
			set(tenant.MetadataKeyBillingSource, string(tenant.BillingStripe)))
		if plan == string(tenant.PlanTeam) {
			err = errors.Join(err, set(tenant.MetadataKeySeats, max(seats, tenant.TeamMinSeats)))
		}
		if from, to, ok := sub.Period(); ok {
			err = errors.Join(err, set(tenant.MetadataKeyPeriodStart, from.Format(time.RFC3339)),
				set(tenant.MetadataKeyPeriodEnd, to.Format(time.RFC3339)))
		}
		res.Action = plan
	case status == "incomplete":
		res.Ignored = true
		return res, nil
	default:
		// Canceled, unpaid, incomplete_expired, paused: no plan follows.
		// No plan is billed now: the account may subscribe again, on
		// kiff.dev or in the app.
		err = errors.Join(set(metadataKeyPlan, string(tenant.PlanTrial)),
			set(tenant.MetadataKeyTrialEndsAt, p.now().UTC().Format(time.RFC3339)),
			set(MetaBillingStatus, StatusCanceled), set(tenant.MetadataKeyBillingSource, string(tenant.BillingNone)))
		res.Action = "ended"
	}
	if err != nil {
		return res, err
	}
	p.logger.InfoContext(ctx, "billing: subscription synced", slog.String("event", e.ID),
		slog.String("tenant", tenantID), slog.String("status", sub.Status), slog.String("plan", plan), slog.Int("seats", seats))
	return res, nil
}

// packPaid records a pack paid for through a checkout KIFF opened, once
// per payment: the payment's id is the pack's transaction, so a
// redelivered or async event records nothing more. A pack's tasks come
// from the pack table, never from the event.
func (p *Processor) packPaid(ctx context.Context, e Event, res Result) (Result, error) {
	obj := e.Data.Object
	if obj.PaymentStatus != "paid" {
		// Paid later: checkout.session.async_payment_succeeded records it.
		res.Ignored = true
		return res, nil
	}
	tenantID, packID := obj.Metadata[MetaTenant], obj.Metadata[MetaPack]
	pack, ok := p.catalog.Pack(packID)
	if p.packs == nil || tenantID == "" || !ok {
		p.logger.WarnContext(ctx, "billing: pack payment KIFF can't record; recorded for reconciliation",
			slog.String("event", e.ID), slog.String("tenant", tenantID), slog.String("pack", packID))
		res.Unmapped = true
		return res, nil
	}
	if pack.Credits > 0 {
		// Packs of credits are sold in the app stores only: kiff.dev's
		// checkout refuses them, so one paid here is support's.
		p.logger.WarnContext(ctx, "billing: a pack of credits paid through Stripe; recorded for reconciliation",
			slog.String("event", e.ID), slog.String("tenant", tenantID), slog.String("pack", packID))
		res.Unmapped = true
		return res, nil
	}
	if _, err := p.tenants.Get(ctx, tenantID); err != nil {
		if err == tenant.ErrNotFound {
			p.logger.WarnContext(ctx, "billing: pack payment names no account; recorded for reconciliation",
				slog.String("event", e.ID), slog.String("tenant", tenantID))
			res.Unmapped = true
			return res, nil
		}
		return res, fmt.Errorf("billing: read account: %w", err)
	}
	if err := p.link(ctx, tenantID, obj.Customer); err != nil {
		return res, err
	}
	txn := obj.PaymentIntent
	if txn == "" {
		txn = obj.ID
	}
	at := p.now().UTC()
	if obj.Created > 0 {
		at = time.Unix(obj.Created, 0).UTC()
	}
	entry, created, err := p.packs.Record(ctx, packs.Grant{TenantID: tenantID, Pack: pack.ID, Tasks: pack.Tasks, Kind: packs.KindBought,
		Source: packs.SourceStripe, SourceTxn: txn, RequestID: obj.Metadata[MetaRequest], AnsweredBy: obj.Metadata[MetaAnsweredBy], At: at})
	if err != nil {
		return res, err
	}
	res.TenantID, res.Action = tenantID, "pack"
	if !created {
		res.Duplicate = true
	} else {
		packs.TellApproved(ctx, p.requests, p.alerts, tenantID, obj.Metadata[MetaRequest], entry.ID, p.now().UTC())
	}
	p.logger.InfoContext(ctx, "billing: pack recorded", slog.String("event", e.ID), slog.String("tenant", tenantID),
		slog.String("pack", pack.ID), slog.String("entry", entry.ID), slog.Bool("created", created))
	return res, nil
}

// packRefunded takes back the tasks of a pack whose payment was refunded
// in full. A partial refund leaves the pack: support decides.
func (p *Processor) packRefunded(ctx context.Context, e Event, res Result) (Result, error) {
	obj := e.Data.Object
	if p.packs == nil || obj.PaymentIntent == "" {
		res.Ignored = true
		return res, nil
	}
	if !obj.Refunded {
		p.logger.InfoContext(ctx, "billing: partial refund; any pack is kept", slog.String("event", e.ID), slog.String("payment", obj.PaymentIntent))
		res.Ignored = true
		return res, nil
	}
	return p.takeBack(ctx, e, res, "pack refunded")
}

// packDisputeClosed takes back the tasks of a pack whose payment's dispute
// was lost: the money went back to the buyer, as in a full refund. A
// dispute won, or still open, leaves the pack.
func (p *Processor) packDisputeClosed(ctx context.Context, e Event, res Result) (Result, error) {
	obj := e.Data.Object
	if p.packs == nil || obj.PaymentIntent == "" || !strings.EqualFold(obj.Status, "lost") {
		res.Ignored = true
		return res, nil
	}
	return p.takeBack(ctx, e, res, "pack dispute lost")
}

// takeBack takes back the pack bought with the event's payment. When the
// payment isn't recorded yet (the refund or dispute came first), the
// ledger keeps the take-back, and the pack is recorded taken back when the
// payment arrives. A payment that wasn't a pack's (a plan's invoice) never
// arrives as one, so its kept take-back changes nothing.
func (p *Processor) takeBack(ctx context.Context, e Event, res Result, action string) (Result, error) {
	payment := e.Data.Object.PaymentIntent
	entry, found, err := p.packs.Revoke(ctx, packs.SourceStripe, payment, p.now())
	if err != nil {
		return res, err
	}
	if !found {
		res.Action = action + " before its payment; kept"
		p.logger.InfoContext(ctx, "billing: take-back kept until its payment is recorded", slog.String("event", e.ID),
			slog.String("type", e.Type), slog.String("payment", payment))
		return res, nil
	}
	res.TenantID, res.Action = entry.TenantID, action
	p.logger.InfoContext(ctx, "billing: "+action, slog.String("event", e.ID), slog.String("tenant", entry.TenantID), slog.String("entry", entry.ID))
	return res, nil
}
