package playstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

// Tenant metadata written with a Google-billed plan: the Play subscription's
// purchase token, and when its current period ends.
const (
	MetaPurchaseToken = "google_purchase_token"
	MetaExpiresAt     = "google_expires_at"
)

// ErrPending is a purchase Play hasn't been paid for yet: nothing applies
// until it is (its notification applies it then).
var ErrPending = errors.New("playstore: the purchase is pending payment")

// GooglePlay is Google Play as an appstore.Store, with catalog's product
// ids.
func GooglePlay(catalog packs.Catalog) appstore.Store {
	return appstore.Store{Name: "google", Billing: tenant.BillingGoogle, PackSource: packs.SourceGoogle,
		MetaSubscription: MetaPurchaseToken, MetaExpiresAt: MetaExpiresAt,
		SoloProduct: catalog.Solo.GoogleProduct, PackProduct: catalog.ByGoogleProduct}
}

// API is the Play Developer API calls KIFF makes; *Client in production.
type API interface {
	PackageName() string
	Subscription(ctx context.Context, token string) (Subscription, error)
	Product(ctx context.Context, product, token string) (ProductPurchase, error)
	Acknowledge(ctx context.Context, product, token string) error
}

// Billing applies Google Play purchases: those the app sends, and those
// Play's notifications name. Each is read back from Play first.
type Billing struct {
	api       API
	processor *appstore.Processor
	accounts  appstore.Accounts
	tenants   tenant.Store
	purchases Purchases
	catalog   packs.Catalog
	logger    *slog.Logger
	now       func() time.Time
}

// NewBilling applies purchases read through api with processor's plan and
// pack rules, as Google Play (processor is set to that store), recording
// each in purchases. accounts resolves Play's obfuscatedExternalAccountId,
// the account's app_account_token.
func NewBilling(api API, processor *appstore.Processor, accounts appstore.Accounts, tenants tenant.Store, purchases Purchases, catalog packs.Catalog, logger *slog.Logger) *Billing {
	if logger == nil {
		logger = slog.Default()
	}
	return &Billing{api: api, processor: processor.WithStore(GooglePlay(catalog)), accounts: accounts, tenants: tenants,
		purchases: purchases, catalog: catalog, logger: logger, now: time.Now}
}

// WithClock sets the clock, for tests.
func (b *Billing) WithClock(now func() time.Time) *Billing {
	b.now = now
	b.processor.WithClock(now)
	return b
}

// Outcome is what applying a purchase did.
type Outcome struct {
	billing.Result
	// Acknowledged: KIFF acknowledged Solo's subscription with Play now.
	Acknowledged bool
}

// Purchased verifies and applies a purchase the app sent: product is the
// Play product id, token its purchase token. Solo is read with
// subscriptionsv2 and acknowledged once applied; a pack with products.get,
// and consumed by the app. Errors are appstore's (ErrNotYours,
// ErrBilledElsewhere, ErrNotSolo, ErrSandbox, ErrUnknownProduct),
// ErrNoPurchase, ErrPending, or a failure to retry.
func (b *Billing) Purchased(ctx context.Context, product, token string, c appstore.Caller) (Outcome, error) {
	product, token = strings.TrimSpace(product), strings.TrimSpace(token)
	if product == "" || token == "" {
		return Outcome{}, ErrNoPurchase
	}
	switch {
	case product == b.catalog.Solo.GoogleProduct:
		sub, err := b.api.Subscription(ctx, token)
		if err != nil {
			return Outcome{}, err
		}
		if sub.Product() != product {
			return Outcome{}, fmt.Errorf("%w: %s", appstore.ErrUnknownProduct, sub.Product())
		}
		kind, ok := subKind(sub.SubscriptionState)
		if !ok {
			return Outcome{}, ErrPending
		}
		if err := b.relink(ctx, sub, token); err != nil {
			return Outcome{}, err
		}
		res, err := b.processor.PurchasedAs(ctx, kind, b.subTxn(sub, token, kind), c)
		if err != nil {
			return Outcome{Result: res}, err
		}
		return b.recordSub(ctx, res, sub, token)
	default:
		if _, ok := b.catalog.ByGoogleProduct(product); !ok {
			return Outcome{}, fmt.Errorf("%w: %s", appstore.ErrUnknownProduct, product)
		}
		pp, err := b.api.Product(ctx, product, token)
		if err != nil {
			return Outcome{}, err
		}
		if pp.PurchaseState == ProductPending {
			return Outcome{}, ErrPending
		}
		kind := ""
		if pp.PurchaseState == ProductCanceled {
			kind = appstore.Refund
		}
		res, err := b.processor.PurchasedAs(ctx, kind, b.productTxn(pp, token, kind), c)
		if err != nil {
			return Outcome{Result: res}, err
		}
		return b.recordProduct(ctx, res, pp, token)
	}
}

// Notify applies one Real-time Developer Notification: the purchase it
// names is read back from Play and applied as it is now, so a redelivery,
// or notifications out of order, apply the same state. What can't be
// applied (no account, billed elsewhere, a test purchase) is logged and
// answered without error, so Pub/Sub stops retrying; a failure is an error,
// so it retries.
func (b *Billing) Notify(ctx context.Context, n DeveloperNotification) (Outcome, error) {
	res := billing.Result{EventID: n.MessageID, EventType: "google:" + n.Kind()}
	if n.PackageName != b.api.PackageName() {
		b.logger.WarnContext(ctx, "playstore: notification for another app; ignored", slog.String("package", n.PackageName))
		res.Ignored = true
		return Outcome{Result: res}, nil
	}
	switch {
	case n.SubscriptionNotification != nil:
		sn := n.SubscriptionNotification
		return b.notifySub(ctx, res, sn.PurchaseToken, sn.NotificationType == SubRevoked)
	case n.OneTimeProductNotification != nil:
		on := n.OneTimeProductNotification
		return b.notifyProduct(ctx, res, on.Sku, on.PurchaseToken, false)
	case n.VoidedPurchaseNotification != nil:
		vn := n.VoidedPurchaseNotification
		if vn.ProductType == VoidedSubscription {
			return b.notifySub(ctx, res, vn.PurchaseToken, true)
		}
		product, err := b.productOf(ctx, vn.PurchaseToken)
		if err != nil {
			return Outcome{Result: res}, err
		}
		if product == "" {
			res.Unmapped = true
			b.logger.WarnContext(ctx, "playstore: voided purchase KIFF can't find; recorded for reconciliation", slog.String("order", vn.OrderID))
			return Outcome{Result: res}, nil
		}
		return b.notifyProduct(ctx, res, product, vn.PurchaseToken, true)
	}
	// A test notification from the Play Console, or a kind KIFF doesn't act on.
	res.Ignored = true
	return Outcome{Result: res}, nil
}

// notifySub applies a subscription as Play has it now. revoked: Play said
// it was revoked or voided; it's taken back unless Play still entitles it.
func (b *Billing) notifySub(ctx context.Context, res billing.Result, token string, revoked bool) (Outcome, error) {
	sub, err := b.api.Subscription(ctx, token)
	if errors.Is(err, ErrNoPurchase) {
		res.Unmapped = true
		return Outcome{Result: res}, nil
	}
	if err != nil {
		return Outcome{Result: res}, err
	}
	kind, ok := subKind(sub.SubscriptionState)
	if !ok {
		res.Ignored = true
		return Outcome{Result: res}, nil
	}
	if revoked && kind != appstore.DidRenew && kind != appstore.DidFailToRenew {
		kind = appstore.Revoke
	}
	if err := b.relink(ctx, sub, token); err != nil {
		return Outcome{Result: res}, err
	}
	res, err = b.processor.ApplyEvent(ctx, res, kind, b.subTxn(sub, token, kind))
	if err != nil || res.TenantID == "" || res.Unmapped {
		return Outcome{Result: res}, err
	}
	return b.recordSub(ctx, res, sub, token)
}

// notifyProduct applies a pack as Play has it now; voided takes it back.
func (b *Billing) notifyProduct(ctx context.Context, res billing.Result, product, token string, voided bool) (Outcome, error) {
	pp, err := b.api.Product(ctx, product, token)
	if errors.Is(err, ErrNoPurchase) {
		res.Unmapped = true
		return Outcome{Result: res}, nil
	}
	if err != nil {
		return Outcome{Result: res}, err
	}
	if pp.PurchaseState == ProductPending {
		res.Ignored = true
		return Outcome{Result: res}, nil
	}
	kind := appstore.OneTimeCharge
	if voided || pp.PurchaseState == ProductCanceled {
		kind = appstore.Refund
	}
	res, err = b.processor.ApplyEvent(ctx, res, kind, b.productTxn(pp, token, kind))
	if err != nil || res.TenantID == "" || res.Unmapped {
		return Outcome{Result: res}, err
	}
	return b.recordProduct(ctx, res, pp, token)
}

// productOf is the pack product a voided one-time purchase was for: from
// KIFF's record of it, else by asking Play under each pack's product.
func (b *Billing) productOf(ctx context.Context, token string) (string, error) {
	if p, ok, err := b.purchases.Get(ctx, token); err != nil {
		return "", err
	} else if ok {
		return p.Product, nil
	}
	for _, p := range b.catalog.Packs {
		if p.GoogleProduct == "" {
			continue
		}
		_, err := b.api.Product(ctx, p.GoogleProduct, token)
		if err == nil {
			return p.GoogleProduct, nil
		}
		if !errors.Is(err, ErrNoPurchase) {
			return "", err
		}
	}
	return "", nil
}

// recordSub records Solo's purchase, then acknowledges it with Play if it
// isn't yet: Play refunds a subscription not acknowledged within three days.
// Only a subscription KIFF applied is acknowledged.
func (b *Billing) recordSub(ctx context.Context, res billing.Result, sub Subscription, token string) (Outcome, error) {
	out := Outcome{Result: res}
	p := Purchase{Token: token, TenantID: res.TenantID, Product: sub.Product(), Kind: KindSubscription,
		OrderID: sub.LatestOrderID, State: sub.SubscriptionState, Test: sub.TestPurchase != nil}
	created, err := b.purchases.Record(ctx, p)
	if err != nil {
		return out, err
	}
	out.Duplicate = !created
	switch sub.SubscriptionState {
	case StateActive, StateCanceled, StateInGrace:
	default:
		return out, nil
	}
	if sub.AcknowledgementState != AckPending {
		return out, nil
	}
	if err := b.api.Acknowledge(ctx, sub.Product(), token); err != nil {
		return out, fmt.Errorf("playstore: acknowledge: %w", err)
	}
	out.Acknowledged = true
	if err := b.purchases.Acknowledged(ctx, token, b.now().UTC()); err != nil {
		return out, err
	}
	b.logger.InfoContext(ctx, "playstore: subscription acknowledged", slog.String("tenant", res.TenantID), slog.String("order", sub.LatestOrderID))
	return out, nil
}

func (b *Billing) recordProduct(ctx context.Context, res billing.Result, pp ProductPurchase, token string) (Outcome, error) {
	state := "purchased"
	if pp.PurchaseState == ProductCanceled {
		state = "canceled"
	}
	if res.Action == "pack refunded" {
		state = "refunded"
	}
	_, err := b.purchases.Record(ctx, Purchase{Token: token, TenantID: res.TenantID, Product: pp.ProductID, Kind: KindProduct,
		OrderID: pp.OrderID, State: state, Test: pp.Test()})
	return Outcome{Result: res}, err
}

// relink follows Play's linkedPurchaseToken: a resubscription or a plan
// change replaces the token that bills the account. When the account's
// recorded token is the one replaced, the new one takes its place, so it
// isn't refused as a second subscription.
func (b *Billing) relink(ctx context.Context, sub Subscription, token string) error {
	linked := sub.LinkedPurchaseToken
	if linked == "" || linked == token {
		return nil
	}
	tenantID, err := b.accounts.TenantFor(ctx, sub.ExternalAccountIdentifiers.ObfuscatedExternalAccountID)
	if err != nil || tenantID == "" {
		return err
	}
	t, err := b.tenants.Get(ctx, tenantID)
	if errors.Is(err, tenant.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("playstore: read account: %w", err)
	}
	if stored, _ := t.Metadata[MetaPurchaseToken].(string); stored != linked || tenant.BillingSourceOf(t) != tenant.BillingGoogle {
		return nil
	}
	if err := b.tenants.SetMetadata(ctx, tenantID, MetaPurchaseToken, token); err != nil {
		return fmt.Errorf("playstore: relink: %w", err)
	}
	// The new subscription's period may end before the replaced one's.
	if err := b.tenants.SetMetadata(ctx, tenantID, MetaExpiresAt, ""); err != nil {
		return fmt.Errorf("playstore: relink: %w", err)
	}
	b.logger.InfoContext(ctx, "playstore: subscription replaced by its linked purchase", slog.String("tenant", tenantID))
	return nil
}

// subKind is the appstore event a subscription state applies as; false for
// one not paid yet.
func subKind(state string) (string, bool) {
	switch state {
	case StateActive, StateCanceled:
		// Canceled: renewal is off, and Solo holds until the period ends.
		return appstore.DidRenew, true
	case StateInGrace, StateOnHold:
		// Play is retrying the payment: the plan stays, past due, until
		// the subscription expires.
		return appstore.DidFailToRenew, true
	case StatePaused, StateExpired:
		return appstore.Expired, true
	}
	return "", false
}

// subTxn is Solo's subscription as an appstore.Transaction. The purchase
// token is both its id and the subscription's (renewals keep it). Play
// gives no period start: Solo is monthly, so the period is the month before
// its expiry, not before the subscription started.
func (b *Billing) subTxn(sub Subscription, token, kind string) appstore.Transaction {
	expires := sub.Expires()
	start := sub.StartTime.UTC()
	if !sub.FreeTrial() && !expires.IsZero() {
		if s := expires.AddDate(0, -1, 0); s.After(start) {
			start = s
		}
	}
	txn := appstore.Transaction{TransactionID: token, OriginalTransactionID: token, ProductID: sub.Product(),
		Type: appstore.TypeSubscription, PurchaseDate: start.UnixMilli(), ExpiresDate: expires.UnixMilli(),
		AppAccountToken: sub.ExternalAccountIdentifiers.ObfuscatedExternalAccountID, Environment: "Production", Quantity: 1}
	if start.IsZero() {
		txn.PurchaseDate = 0
	}
	if expires.IsZero() {
		txn.ExpiresDate = 0
	}
	if sub.FreeTrial() {
		txn.OfferType, txn.OfferDiscountType = 1, "FREE_TRIAL"
	}
	if sub.TestPurchase != nil {
		txn.Environment = appstore.EnvSandbox
	}
	if kind == appstore.Revoke {
		txn.RevocationDate = b.now().UnixMilli()
	}
	return txn
}

// productTxn is a pack's purchase as an appstore.Transaction, by its
// purchase token: a pack is recorded once per token.
func (b *Billing) productTxn(pp ProductPurchase, token, kind string) appstore.Transaction {
	txn := appstore.Transaction{TransactionID: token, OriginalTransactionID: token, ProductID: pp.ProductID,
		Type: appstore.TypeConsumable, PurchaseDate: pp.Purchased().UnixMilli(),
		AppAccountToken: pp.ObfuscatedExternalAccountID, Environment: "Production", Quantity: max(pp.Quantity, 1)}
	if pp.Purchased().IsZero() {
		txn.PurchaseDate = 0
	}
	if pp.Test() {
		txn.Environment = appstore.EnvSandbox
	}
	if kind == appstore.Refund {
		txn.RevocationDate = b.now().UnixMilli()
	}
	return txn
}
