package appstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

// Tenant metadata the processor writes with the plan: the Apple
// subscription's original transaction, and when its latest period ends.
const (
	MetaOriginalTransaction = "apple_original_transaction_id"
	MetaExpiresAt           = "apple_expires_at"
	// MetaAutoRenew is "off" once the person turned renewal off with Apple,
	// "on" when they turned it on again: after a move to Team, Solo is said
	// to renew only while it does.
	MetaAutoRenew = "apple_auto_renew"
	// MetaAutoRenewAt is when Apple signed the status MetaAutoRenew holds:
	// notifications can arrive out of order, so an older one is ignored.
	MetaAutoRenewAt = "apple_auto_renew_at"
)

// Notification types acted on (App Store Server Notifications V2).
const (
	Subscribed           = "SUBSCRIBED"
	DidRenew             = "DID_RENEW"
	DidChangeRenewalPref = "DID_CHANGE_RENEWAL_PREF"
	// DidChangeRenewalStatus: the person turned the subscription's renewal
	// off (subtype AUTO_RENEW_DISABLED) or on again (AUTO_RENEW_ENABLED).
	DidChangeRenewalStatus = "DID_CHANGE_RENEWAL_STATUS"
	OfferRedeemed          = "OFFER_REDEEMED"
	RenewalExtended        = "RENEWAL_EXTENDED"
	DidFailToRenew         = "DID_FAIL_TO_RENEW"
	Expired                = "EXPIRED"
	GracePeriodExpired     = "GRACE_PERIOD_EXPIRED"
	Refund                 = "REFUND"
	Revoke                 = "REVOKE"
	OneTimeCharge          = "ONE_TIME_CHARGE"
	purchasedInTheApp      = "" // a transaction the app sent
	billingStatusTrialing  = "trialing"
)

var (
	// ErrNotYours: the transaction's appAccountToken isn't the caller's
	// account's.
	ErrNotYours = errors.New("appstore: the purchase is another account's")
	// ErrBilledElsewhere: the account's plan is billed on kiff.dev or on
	// a contract, so Apple may not bill it too.
	ErrBilledElsewhere = errors.New("appstore: the account's plan is billed elsewhere")
	// ErrNotSolo: Apple bills Solo only; the account is on another plan.
	ErrNotSolo = errors.New("appstore: Apple bills Solo only")
	// ErrUnknownProduct: a product that is neither Solo nor a pack.
	ErrUnknownProduct = errors.New("appstore: unknown product")
	// ErrSandbox: a test purchase (Apple's Sandbox, as TestFlight buys)
	// on an account that may not use them. Nothing is recorded or granted.
	ErrSandbox = errors.New("appstore: a test purchase on an account that takes only real ones")
)

// EnvSandbox is Apple's test environment: TestFlight's and App Review's
// purchases, which nobody pays for.
const EnvSandbox = "Sandbox"

// Caller is who sent a transaction from the app, and the member's
// request it answers, if any.
type Caller struct {
	TenantID  string
	Subject   string
	RequestID string
}

// Processor applies a store's verified transactions (Apple's by default,
// Google Play's WithStore). For Apple: it applies Apple's verified transactions to an account: Solo's
// subscription sets the plan the way billing.Processor does for Stripe,
// with Apple as its billing source; a pack's consumable records the pack.
// A refund or revoke takes back the plan or the pack. Everything is
// idempotent on Apple's ids: notifications on their UUID, packs on their
// transaction, and the plan is written whole each time, never older over
// newer.
type Processor struct {
	tenants  tenant.Store
	accounts Accounts
	ledger   packs.Ledger
	// credits records the packs of credits. Nil: a pack of credits is
	// refused, unrecorded.
	credits packs.CreditLedger
	catalog packs.Catalog
	seen    billing.Store
	// requests and alerts tell a member their request was approved when
	// a pack bought for it is recorded. Nil: nobody is told.
	requests packs.Requests
	alerts   alert.Notifier
	// sandbox are the accounts, besides those on the internal plan, whose
	// test purchases count: App Review's.
	sandbox map[string]bool
	// store is the store whose purchases this processor applies: Apple's
	// App Store unless WithStore names another.
	store  Store
	logger *slog.Logger
	now    func() time.Time
}

// Store is a store the app sells Solo and the packs through: who it makes
// the plan's billing source, where its packs are recorded, the tenant
// metadata that holds its subscription, and its product ids. The plan and
// pack rules are the same for every store.
type Store struct {
	// Name is for logs: "apple", "google".
	Name string
	// Billing is the plan's billing source while this store bills Solo.
	Billing tenant.BillingSource
	// PackSource records the packs bought through it.
	PackSource packs.Source
	// MetaSubscription holds the id of the store subscription that bills
	// the account (Apple's original transaction, Play's purchase token);
	// MetaExpiresAt, when its latest period ends.
	MetaSubscription, MetaExpiresAt string
	// SoloProduct is Solo's subscription product id; PackProduct, the pack
	// sold as a product id.
	SoloProduct string
	PackProduct func(product string) (packs.Pack, bool)
}

// ResetSuffix makes a store's subscription key into the key that keeps the
// subscription an operator reset the account's billing from (ResetMeta).
const ResetSuffix = "_reset"

// MetaResetAt is when an operator last reset the account's store billing.
const MetaResetAt = "store_billing_reset_at"

// ResetMeta is the metadata that clears this store's subscription from t,
// as an operator's reset writes it: the subscription and its period cleared,
// and the subscription kept under its reset key, so the store's later word on
// it is ignored (WasReset). Empty values read as absent everywhere.
func (s Store) ResetMeta(t tenant.Tenant) map[string]any {
	set := map[string]any{s.MetaSubscription: "", s.MetaExpiresAt: ""}
	if stored, _ := t.Metadata[s.MetaSubscription].(string); stored != "" {
		set[s.MetaSubscription+ResetSuffix] = stored
	}
	return set
}

// ResetBilling is the metadata an operator's reset of t's store billing
// writes (Reset store billing, at now): t ends as a subscription that ended
// does (plan trial, its trial over, billing canceled, no billing source),
// Apple's renewal status is cleared, and each of stores' subscriptions is
// cleared and kept aside (ResetMeta), so the store's later word on it is
// ignored.
func ResetBilling(t tenant.Tenant, now time.Time, stores ...Store) map[string]any {
	at := now.UTC().Format(time.RFC3339)
	set := map[string]any{
		tenant.MetadataKeyPlan:          string(tenant.PlanTrial),
		tenant.MetadataKeyTrialEndsAt:   at,
		billing.MetaBillingStatus:       billing.StatusCanceled,
		tenant.MetadataKeyBillingSource: string(tenant.BillingNone),
		MetaAutoRenew:                   "",
		MetaAutoRenewAt:                 "",
		MetaResetAt:                     at,
	}
	for _, s := range stores {
		for k, v := range s.ResetMeta(t) {
			set[k] = v
		}
	}
	return set
}

// WasReset is the subscription an operator reset t's store billing from, for
// this store, and when; false when there's none.
func (s Store) WasReset(t tenant.Tenant) (string, time.Time, bool) {
	id, _ := t.Metadata[s.MetaSubscription+ResetSuffix].(string)
	at, ok := metaTime(t, MetaResetAt)
	return id, at, id != "" && ok
}

// AppStore is Apple's App Store, with catalog's product ids.
func AppStore(catalog packs.Catalog) Store {
	return Store{Name: "apple", Billing: tenant.BillingApple, PackSource: packs.SourceApple,
		MetaSubscription: MetaOriginalTransaction, MetaExpiresAt: MetaExpiresAt,
		SoloProduct: catalog.Solo.AppleProduct, PackProduct: catalog.ByAppleProduct}
}

// WithStore applies another store's purchases with the same rules: Google
// Play's.
func (p *Processor) WithStore(s Store) *Processor {
	p.store = s
	return p
}

// WithSandboxAccounts lets these accounts' test purchases count too, as
// the internal plan's do: App Review's account, which buys in Apple's
// Sandbox against the production server.
func (p *Processor) WithSandboxAccounts(tenantIDs ...string) *Processor {
	p.sandbox = map[string]bool{}
	for _, id := range tenantIDs {
		if id = strings.TrimSpace(id); id != "" {
			p.sandbox[id] = true
		}
	}
	return p
}

// takesSandbox reports whether t's test purchases count: an account on the
// internal plan, or one named with WithSandboxAccounts.
func (p *Processor) takesSandbox(t tenant.Tenant) bool {
	return p.sandbox[t.ID] || tenant.PlanFor(t).Canonical == tenant.PlanInternal
}

// WithCredits records the packs of credits bought in credits.
func (p *Processor) WithCredits(credits packs.CreditLedger) *Processor {
	p.credits = credits
	return p
}

// WithRequestAlerts tells the account's alert targets when a pack bought
// for a member's request approves it.
func (p *Processor) WithRequestAlerts(requests packs.Requests, n alert.Notifier) *Processor {
	p.requests, p.alerts = requests, n
	return p
}

// NewProcessor applies transactions to tenants, resolving accounts by
// their token, recording packs in ledger by catalog, and notifications
// once each in seen.
func NewProcessor(tenants tenant.Store, accounts Accounts, ledger packs.Ledger, catalog packs.Catalog, seen billing.Store, logger *slog.Logger) *Processor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Processor{tenants: tenants, accounts: accounts, ledger: ledger, catalog: catalog, seen: seen, store: AppStore(catalog), logger: logger, now: time.Now}
}

// WithClock sets the clock, for tests.
func (p *Processor) WithClock(now func() time.Time) *Processor {
	p.now = now
	return p
}

// Notify applies one verified notification, once: a redelivery is a
// no-op. A notification KIFF can't map to an account is recorded for
// reconciliation, not an error, so Apple stops retrying it; a real failure
// is an error, so Apple retries.
func (p *Processor) Notify(ctx context.Context, n Notification, txn *Transaction) (billing.Result, error) {
	res := billing.Result{EventID: n.NotificationUUID, EventType: n.NotificationType}
	key := "apple:" + n.NotificationUUID
	seen, err := p.seen.Seen(ctx, key)
	if err != nil {
		return res, fmt.Errorf("appstore: idempotency check: %w", err)
	}
	if seen {
		res.Duplicate = true
		return res, nil
	}
	switch n.NotificationType {
	case Subscribed, DidRenew, DidChangeRenewalPref, OfferRedeemed, RenewalExtended, DidFailToRenew,
		Expired, GracePeriodExpired, Refund, Revoke, OneTimeCharge:
		if txn == nil {
			res.Ignored = true
			break
		}
		if res, err = p.ApplyEvent(ctx, res, n.NotificationType, *txn); err != nil {
			return res, err
		}
	case DidChangeRenewalStatus:
		// A fact about the App Store subscription, kept whoever bills the plan
		// now: after a move to Team, Solo is said to renew only while it does.
		if txn == nil || (n.Subtype != "AUTO_RENEW_DISABLED" && n.Subtype != "AUTO_RENEW_ENABLED") {
			res.Ignored = true
			break
		}
		at := time.UnixMilli(n.SignedDate).UTC()
		if n.SignedDate == 0 {
			at = p.now().UTC()
		}
		if res, err = p.renewal(ctx, res, *txn, n.Subtype == "AUTO_RENEW_ENABLED", at); err != nil {
			return res, err
		}
	default:
		res.Ignored = true
	}
	if _, err := p.seen.MarkProcessed(ctx, key); err != nil {
		return res, fmt.Errorf("appstore: mark processed: %w", err)
	}
	return res, nil
}

// ApplyEvent applies a transaction the store reported itself (a
// notification), as kind: one of the notification types above. What KIFF
// can't apply, and the store's retry wouldn't change (billed elsewhere,
// not Solo, an unknown product, a test purchase), is logged for support
// and answered Unmapped, not as an error; a real failure is an error, so
// the store retries.
func (p *Processor) ApplyEvent(ctx context.Context, res billing.Result, kind string, txn Transaction) (billing.Result, error) {
	res, err := p.apply(ctx, res, kind, txn, nil)
	switch {
	case errors.Is(err, ErrBilledElsewhere), errors.Is(err, ErrNotSolo), errors.Is(err, ErrUnknownProduct), errors.Is(err, ErrSandbox):
		p.logger.WarnContext(ctx, "appstore: notification not applied; recorded for reconciliation",
			slog.String("store", p.store.Name), slog.String("event", res.EventID), slog.String("type", kind),
			slog.String("tenant", res.TenantID), slog.String("err", err.Error()))
		res.Unmapped = true
		return res, nil
	}
	return res, err
}

// renewal records whether the account's App Store subscription renews, as
// Apple said at `at`: only for the subscription the account holds (another
// Apple ID's on the same account says nothing about it), and only when newer
// than the status recorded.
func (p *Processor) renewal(ctx context.Context, res billing.Result, txn Transaction, on bool, at time.Time) (billing.Result, error) {
	tenantID, err := p.accounts.TenantFor(ctx, txn.AppAccountToken)
	if err != nil {
		return res, err
	}
	if tenantID == "" {
		res.Unmapped = true
		return res, nil
	}
	res.TenantID = tenantID
	t, err := p.tenants.Get(ctx, tenantID)
	if errors.Is(err, tenant.ErrNotFound) {
		res.Unmapped = true
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("appstore: read account: %w", err)
	}
	if txn.Environment == EnvSandbox && !p.takesSandbox(t) {
		res.Ignored = true
		return res, nil
	}
	if stored, _ := t.Metadata[p.store.MetaSubscription].(string); stored == "" || stored != txn.OriginalTransactionID {
		p.logger.WarnContext(ctx, "appstore: renewal status for a subscription the account doesn't hold; ignored",
			slog.String("tenant", tenantID), slog.String("transaction", txn.TransactionID))
		res.Ignored = true
		return res, nil
	}
	if last, ok := metaTime(t, MetaAutoRenewAt); ok && !at.After(last) {
		res.Ignored = true
		return res, nil
	}
	v := "off"
	if on {
		v = "on"
	}
	return p.write(ctx, res, tenantID, "auto_renew_"+v, map[string]any{
		MetaAutoRenew:   v,
		MetaAutoRenewAt: at.Format(time.RFC3339Nano),
	})
}

// Purchased applies a transaction the app sent after a purchase, a
// restore or an update: only to the caller's own account.
func (p *Processor) Purchased(ctx context.Context, txn Transaction, c Caller) (billing.Result, error) {
	return p.PurchasedAs(ctx, purchasedInTheApp, txn, c)
}

// PurchasedAs is Purchased for a store that says what the purchase's state
// is (Google Play's grace period, expiry): kind is one of the notification
// types above, or "" for a purchase that is simply current.
func (p *Processor) PurchasedAs(ctx context.Context, kind string, txn Transaction, c Caller) (billing.Result, error) {
	return p.apply(ctx, billing.Result{EventID: txn.TransactionID, EventType: "app"}, kind, txn, &c)
}

func (p *Processor) apply(ctx context.Context, res billing.Result, kind string, txn Transaction, c *Caller) (billing.Result, error) {
	tenantID, err := p.accounts.TenantFor(ctx, txn.AppAccountToken)
	if err != nil {
		return res, err
	}
	if c != nil && tenantID != c.TenantID {
		return res, ErrNotYours
	}
	if tenantID == "" {
		p.logger.WarnContext(ctx, "appstore: transaction names no account; recorded for reconciliation",
			slog.String("transaction", txn.TransactionID), slog.String("type", kind))
		res.Unmapped = true
		return res, nil
	}
	res.TenantID = tenantID
	t, err := p.tenants.Get(ctx, tenantID)
	if errors.Is(err, tenant.ErrNotFound) {
		res.Unmapped = true
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("appstore: read account: %w", err)
	}
	if txn.Environment == EnvSandbox && !p.takesSandbox(t) {
		// A test purchase is free: on a real account it would be tasks or
		// a plan nobody paid for. Refused before anything is written.
		p.logger.WarnContext(ctx, "appstore: test purchase refused on an account that takes only real ones",
			slog.String("tenant", t.ID), slog.String("transaction", txn.TransactionID), slog.String("type", kind))
		return res, ErrSandbox
	}
	takeBack := kind == Refund || kind == Revoke || txn.Revoked()
	if p.store.PackProduct != nil {
		if pack, ok := p.store.PackProduct(txn.ProductID); ok && txn.ProductID != "" {
			return p.pack(ctx, res, t, pack, txn, takeBack, c)
		}
	}
	if txn.ProductID != "" && txn.ProductID == p.store.SoloProduct {
		return p.solo(ctx, res, t, kind, txn, takeBack, c != nil)
	}
	return res, fmt.Errorf("%w: %s", ErrUnknownProduct, txn.ProductID)
}

// pack records a pack's purchase, or takes it back.
func (p *Processor) pack(ctx context.Context, res billing.Result, t tenant.Tenant, pack packs.Pack, txn Transaction, takeBack bool, c *Caller) (billing.Result, error) {
	if pack.Credits > 0 {
		return p.creditPack(ctx, res, t, pack, txn, takeBack, c)
	}
	if takeBack {
		at := ms(txn.RevocationDate)
		if at.IsZero() {
			at = p.now()
		}
		// A refund can come before the purchase was recorded: record it,
		// so it is taken back.
		if _, _, err := p.ledger.Record(ctx, packs.Grant{TenantID: t.ID, Pack: pack.ID, Tasks: pack.Tasks * max(txn.Quantity, 1),
			Kind: packs.KindBought, Source: p.store.PackSource, SourceTxn: txn.TransactionID, At: txn.Purchased()}); err != nil {
			return res, err
		}
		if _, _, err := p.ledger.Revoke(ctx, p.store.PackSource, txn.TransactionID, at); err != nil {
			return res, err
		}
		res.Action = "pack refunded"
		p.logger.InfoContext(ctx, "appstore: pack taken back", slog.String("store", p.store.Name), slog.String("tenant", t.ID), slog.String("transaction", txn.TransactionID))
		return res, nil
	}
	g := packs.Grant{TenantID: t.ID, Pack: pack.ID, Tasks: pack.Tasks * max(txn.Quantity, 1), Kind: packs.KindBought,
		Source: p.store.PackSource, SourceTxn: txn.TransactionID, At: txn.Purchased()}
	if c != nil {
		g.RequestID, g.AnsweredBy = c.RequestID, c.Subject
	}
	e, created, err := p.ledger.Record(ctx, g)
	if err != nil {
		return res, err
	}
	res.Action, res.Duplicate = "pack", !created
	if created {
		packs.TellApproved(ctx, p.requests, p.alerts, t.ID, g.RequestID, e.ID, p.now().UTC())
	}
	p.logger.InfoContext(ctx, "appstore: pack recorded", slog.String("tenant", t.ID), slog.String("pack", pack.ID),
		slog.String("transaction", txn.TransactionID), slog.Bool("created", created))
	return res, nil
}

// creditPack records a pack of credits' purchase, its credits times the
// quantity on the store's receipt, or takes it back. Credits a refunded
// pack had already given stay spent, as with any consumable; each take-back
// is logged with what the pack had left, so repeated refunds show.
func (p *Processor) creditPack(ctx context.Context, res billing.Result, t tenant.Tenant, pack packs.Pack, txn Transaction, takeBack bool, c *Caller) (billing.Result, error) {
	if p.credits == nil {
		return res, fmt.Errorf("appstore: pack of credits %q with no credits ledger", pack.ID)
	}
	g := packs.CreditGrant{TenantID: t.ID, Pack: pack.ID, Credits: pack.Credits * int64(max(txn.Quantity, 1)), Kind: packs.KindBought,
		Source: p.store.PackSource, SourceTxn: txn.TransactionID, At: txn.Purchased()}
	if takeBack {
		at := ms(txn.RevocationDate)
		if at.IsZero() {
			at = p.now()
		}
		// A refund can come before the purchase was recorded: record it,
		// so it is taken back.
		if _, _, err := p.credits.RecordCredits(ctx, g); err != nil {
			return res, err
		}
		back, _, err := p.credits.RevokeCredits(ctx, p.store.PackSource, txn.TransactionID, at)
		if err != nil {
			return res, err
		}
		res.Action = "pack refunded"
		p.logger.InfoContext(ctx, "appstore: pack of credits taken back", slog.String("store", p.store.Name), slog.String("tenant", t.ID),
			slog.String("transaction", txn.TransactionID), slog.Int64("credits", back.Credits), slog.Int64("taken_back", back.TakenBack))
		return res, nil
	}
	if c != nil {
		g.RequestID, g.AnsweredBy = c.RequestID, c.Subject
	}
	e, created, err := p.credits.RecordCredits(ctx, g)
	if err != nil {
		return res, err
	}
	res.Action, res.Duplicate = "pack", !created
	if created {
		packs.TellApproved(ctx, p.requests, p.alerts, t.ID, g.RequestID, e.ID, p.now().UTC())
	}
	p.logger.InfoContext(ctx, "appstore: pack of credits recorded", slog.String("tenant", t.ID), slog.String("pack", pack.ID),
		slog.String("transaction", txn.TransactionID), slog.Int64("credits", g.Credits), slog.Bool("created", created))
	return res, nil
}

// solo applies Solo's subscription: the trial during Apple's free trial,
// Solo with Apple's period while it renews, and an ended trial once it
// expired or was taken back.
func (p *Processor) solo(ctx context.Context, res billing.Result, t tenant.Tenant, kind string, txn Transaction, takeBack, fromApp bool) (billing.Result, error) {
	now := p.now().UTC()
	if reset, at, ok := p.store.WasReset(t); ok && reset == txn.OriginalTransactionID && (!fromApp || !txn.Purchased().After(at)) {
		// An operator reset this account's store billing (Reset store billing):
		// nothing the store says of that subscription since applies, its
		// renewals and its end alike. A purchase the app sends that was made
		// after the reset is a new one, and applies.
		p.logger.InfoContext(ctx, "appstore: a subscription the account's billing was reset from; ignored",
			slog.String("store", p.store.Name), slog.String("tenant", t.ID), slog.String("transaction", txn.TransactionID), slog.String("type", kind))
		res.Ignored = true
		return res, nil
	}
	source := tenant.BillingSourceOf(t)
	expired := kind == Expired || kind == GracePeriodExpired ||
		(kind != DidFailToRenew && !txn.Expires().IsZero() && !txn.Expires().After(now))
	ends := takeBack || expired
	stored, _ := t.Metadata[p.store.MetaSubscription].(string)
	ours := source == p.store.Billing && (stored == "" || stored == txn.OriginalTransactionID)
	if ends {
		if !ours {
			// Not the subscription that bills this account: nothing to end.
			res.Ignored = true
			return res, nil
		}
		if !takeBack {
			if last, ok := metaTime(t, p.store.MetaExpiresAt); ok && last.After(txn.Expires()) {
				// A newer period was recorded since: this is stale.
				res.Ignored = true
				return res, nil
			}
		}
		return p.write(ctx, res, t.ID, "ended", map[string]any{
			tenant.MetadataKeyPlan:          string(tenant.PlanTrial),
			tenant.MetadataKeyTrialEndsAt:   now.Format(time.RFC3339),
			billing.MetaBillingStatus:       billing.StatusCanceled,
			tenant.MetadataKeyBillingSource: string(tenant.BillingNone),
		})
	}
	if source == tenant.BillingStripe && stored != "" && stored == txn.OriginalTransactionID && !txn.Expires().IsZero() {
		// The Solo this account had before it moved to Team renewed
		// with Apple: Stripe bills the plan now, so only Solo's date is kept, for the
		// line that says it still renews. The person pays both until it's cancelled
		// with Apple: support is told.
		if last, ok := metaTime(t, p.store.MetaExpiresAt); ok && !txn.Expires().After(last) {
			res.Ignored = true
			return res, nil
		}
		p.logger.WarnContext(ctx, "appstore: solo renewed with the store on an account kiff.dev bills for team; the account is paying twice",
			slog.String("tenant", t.ID), slog.String("transaction", txn.TransactionID))
		set := map[string]any{p.store.MetaExpiresAt: txn.Expires().Format(time.RFC3339)}
		// Apple charged, so renewal was on when it did: unless it was turned
		// off since.
		if said, ok := metaTime(t, MetaAutoRenewAt); !ok || txn.Purchased().After(said) {
			set[MetaAutoRenew] = "on"
		}
		return p.write(ctx, res, t.ID, "solo_renewed_after_team", set)
	}
	if source != tenant.BillingNone && source != p.store.Billing {
		// Stripe, a contract, or the other store bills the plan.
		return res, ErrBilledElsewhere
	}
	switch tenant.PlanFor(t).Canonical {
	case tenant.PlanTeam, tenant.PlanCompany, tenant.PlanInternal:
		return res, ErrNotSolo
	}
	if source == p.store.Billing && stored != "" && stored != txn.OriginalTransactionID {
		// A second subscription on one account (another Apple ID, another
		// Google account): the one already billing it stays.
		return res, ErrBilledElsewhere
	}
	if kind == DidFailToRenew {
		if !ours {
			res.Ignored = true
			return res, nil
		}
		// Apple is retrying the payment, or in its grace period: the plan
		// stays until EXPIRED or GRACE_PERIOD_EXPIRED.
		return p.write(ctx, res, t.ID, "grace", map[string]any{billing.MetaBillingStatus: billing.StatusPastDue})
	}
	if last, ok := metaTime(t, p.store.MetaExpiresAt); ok && ours && last.After(txn.Expires()) {
		res.Ignored = true
		return res, nil
	}
	set := map[string]any{
		tenant.MetadataKeyBillingSource: string(p.store.Billing),
		p.store.MetaSubscription:        txn.OriginalTransactionID,
		p.store.MetaExpiresAt:           txn.Expires().Format(time.RFC3339),
	}
	if reset, _, ok := p.store.WasReset(t); ok && reset == txn.OriginalTransactionID {
		// Bought again in the app after a reset: it bills the account now,
		// and its renewals apply.
		set[p.store.MetaSubscription+ResetSuffix] = ""
	}
	if txn.FreeTrial() {
		// The store's one-week free trial is KIFF's trial.
		set[tenant.MetadataKeyPlan] = string(tenant.PlanTrial)
		set[tenant.MetadataKeyTrialEndsAt] = txn.Expires().Format(time.RFC3339)
		set[billing.MetaBillingStatus] = billingStatusTrialing
		if _, ok := t.Metadata[tenant.MetadataKeyTrialStartedAt]; !ok {
			set[tenant.MetadataKeyTrialStartedAt] = txn.Purchased().Format(time.RFC3339)
		}
		return p.write(ctx, res, t.ID, "trialing", set)
	}
	set[tenant.MetadataKeyPlan] = string(tenant.PlanSolo)
	set[tenant.MetadataKeyPeriodStart] = txn.Purchased().Format(time.RFC3339)
	set[tenant.MetadataKeyPeriodEnd] = txn.Expires().Format(time.RFC3339)
	set[billing.MetaBillingStatus] = billing.StatusActive
	return p.write(ctx, res, t.ID, "solo", set)
}

func (p *Processor) write(ctx context.Context, res billing.Result, tenantID, action string, set map[string]any) (billing.Result, error) {
	var errs []error
	for k, v := range set {
		if err := p.tenants.SetMetadata(ctx, tenantID, k, v); err != nil {
			errs = append(errs, fmt.Errorf("appstore: set %s: %w", k, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return res, err
	}
	res.Action = action
	p.logger.InfoContext(ctx, "appstore: plan applied", slog.String("store", p.store.Name), slog.String("tenant", tenantID), slog.String("action", action))
	return res, nil
}

func metaTime(t tenant.Tenant, key string) (time.Time, bool) {
	s, _ := t.Metadata[key].(string)
	at, err := time.Parse(time.RFC3339, s)
	return at, err == nil
}

// SoloRenewsUntil is when the App Store Solo an account had before it moved
// to Team renews next, while it still does: its renewal not
// turned off with Apple, and its date not past. False otherwise.
func SoloRenewsUntil(t tenant.Tenant, now time.Time) (time.Time, bool) {
	until, ok := metaTime(t, MetaExpiresAt)
	if !ok || !until.After(now) || t.Metadata[MetaAutoRenew] == "off" {
		return time.Time{}, false
	}
	return until, true
}

// PaidSoloUntil is when the App Store Solo an account pays for ends, the days
// a move to Team credits; false when Apple bills no paid Solo.
func PaidSoloUntil(t tenant.Tenant) (time.Time, bool) {
	if tenant.BillingSourceOf(t) != tenant.BillingApple || tenant.PlanFor(t).Canonical != tenant.PlanSolo {
		return time.Time{}, false
	}
	return metaTime(t, MetaExpiresAt)
}
