package purchase

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kiff/cloud/api/internal/permit"
)

// ErrNotEnded: a case still being decided or bought has no receipt yet.
var ErrNotEnded = errors.New("purchase: the case hasn't ended")

// Order is the order the shop confirmed, kept from the execution.
type confirmedOrder struct {
	ref string
	at  *time.Time
}

// FinishOrdered ends an execution the shop confirmed: done, with the order's
// reference and when the page confirmed it, for the receipt.
func (s *Store) FinishOrdered(ctx context.Context, tenant, caseID string, version int, holder, ref string, confirmedAt time.Time) error {
	if err := s.Finish(ctx, tenant, caseID, version, holder, OutcomeOrdered, ""); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `UPDATE purchase.cases SET order_ref = $4, order_confirmed_at = $5 WHERE tenant_id = $1 AND case_id = $2 AND version = $3`,
		tenant, caseID, version, oneLine(ref, 100), confirmedAt.UTC())
	return err
}

// receiptStates are the case states a receipt is given for: ended, or
// unknown until reconciled.
var receiptStates = map[string]bool{StateDone: true, StateNotBought: true, StateExpired: true, StateUnknown: true}

// ReceiptOf is the receipt of a case's latest version, unsigned: what was
// decided and what happened, the issuer's merchant beside the case's, the
// money's status from what KIFF kept, unknown said as unknown. reader is the
// KIFF subject reading it (approver.self).
func (s *Store) ReceiptOf(ctx context.Context, tenant, caseID, reader string) (Receipt, error) {
	r, err := s.Latest(ctx, tenant, caseID)
	if err != nil {
		return Receipt{}, err
	}
	if !receiptStates[r.State] {
		return Receipt{}, ErrNotEnded
	}
	c := r.Case
	var decided []byte
	var updated time.Time
	var order confirmedOrder
	if err := s.Pool.QueryRow(ctx, `SELECT decided, updated_at, order_ref, order_confirmed_at FROM purchase.cases WHERE tenant_id = $1 AND case_id = $2 AND version = $3`,
		tenant, caseID, c.Version).Scan(&decided, &updated, &order.ref, &order.at); err != nil {
		return Receipt{}, err
	}
	var d Decision
	if len(decided) > 0 {
		_ = json.Unmarshal(decided, &d)
	}
	var issuer NameCategory
	var verification string
	err = s.Pool.QueryRow(ctx, `SELECT merchant_name, merchant_category, verification FROM purchase.authorizations
		WHERE tenant_id = $1 AND case_id = $2 AND version = $3 AND approved ORDER BY at LIMIT 1`, tenant, caseID, c.Version).Scan(&issuer.Name, &issuer.Category, &verification)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, err
	}
	money, err := s.Settlement(ctx, tenant, caseID, c.Version)
	if err != nil {
		return Receipt{}, err
	}
	method := d.Method
	if method == "" && d.By == ByOwner {
		method = "device_biometric"
	}
	rc := Receipt{
		ReceiptID: receiptID(c.CaseID, c.Version), CaseID: c.CaseID, Version: c.Version, Digest: r.Digest, Path: c.Path, DecidedBy: d.By,
		Approver:     Approver{Role: d.ApproverRole, Self: reader != "" && reader == d.Approver, Method: method},
		Merchant:     ReceiptMerchant{Case: NameCategory{Name: c.Merchant.Name, Category: c.Merchant.Category, StripeProfile: c.Merchant.StripeProfile}, Issuer: issuer},
		Amounts:      Amounts{ApprovedMax: c.Price.Max, Authorized: money.Authorized, Captured: money.Captured, Refunded: money.Refunded, Currency: c.Currency},
		States:       States{Case: r.State, Money: money.Money, Order: orderState(r, order)},
		Verification: verification, IssuedAt: updated.UTC().Format(time.RFC3339),
	}
	if d.By == ByCardLimits {
		rc.Approver = Approver{Role: "card", Method: "card_limits"}
	}
	if order.ref != "" && order.at != nil {
		rc.Order = &Order{Reference: order.ref, ConfirmedAt: order.at.UTC().Format(time.RFC3339)}
	}
	rc.Words = receiptWords(c, r, rc, d)
	return rc, nil
}

func orderState(r Row, o confirmedOrder) string {
	switch {
	case r.State == StateUnknown:
		return "unknown"
	case r.State == StateDone && o.ref != "":
		return "confirmed"
	case r.State == StateDone:
		return "placed"
	}
	return "none"
}

// receiptID is the receipt's id, the same each time it's read.
func receiptID(caseID string, version int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", caseID, version)))
	return "rc_" + hex.EncodeToString(sum[:8])
}

// receiptWords are the receipt's lines in KIFF's words.
func receiptWords(c Case, r Row, rc Receipt, d Decision) map[string]string {
	money := func(v int64) string { return Money(v, c.Currency) }
	w := map[string]string{}
	switch r.State {
	case StateDone:
		w["state"] = "Bought"
		line := fmt.Sprintf("Bought from %s · %s", c.Merchant.Name, money(rc.Amounts.Captured))
		if extra := rc.Amounts.Captured - c.Price.Total; extra > 0 && rc.Amounts.Captured <= c.Price.Max && c.Price.Rule != nil {
			// Inside what the owner approved, or what the Card allowed when its limits decided it.
			within := "inside what you approved"
			if d.By == ByCardLimits {
				within = "inside what your Card allowed"
			}
			line += fmt.Sprintf(" (%s more for %s, %s)", money(extra), strings.Join(c.Price.Rule.For, " and "), within)
		}
		if rc.Amounts.Captured == 0 {
			line = fmt.Sprintf("Bought from %s · %s", c.Merchant.Name, money(c.Price.Total))
		}
		w["line"] = line
	case StateUnknown:
		w["state"] = "Not known yet"
		w["line"] = fmt.Sprintf("Not known yet: %s · %s", c.Merchant.Name, money(c.Price.Total))
	default:
		w["state"] = "Not bought"
		if r.State == StateExpired || r.Ended == EndedNotUsedInTime {
			w["line"] = "Not bought: the shop didn't complete it within 15 minutes. Nothing was charged."
		} else {
			w["line"] = fmt.Sprintf("Not bought: %s · %s. Nothing was charged.", c.Merchant.Name, money(c.Price.Total))
		}
	}
	if d.By == ByCardLimits {
		w["allowed"] = fmt.Sprintf("Your Card allowed up to %s at this shop, once.", money(c.Price.Max))
	} else {
		w["allowed"] = fmt.Sprintf("You approved up to %s at this shop, once.", money(c.Price.Max))
	}
	if c.Path == "card_fill" {
		w["card"] = "One card for this purchase, used once. Only KIFF's payment worker used its number, once. The taf never saw it."
	} else {
		w["card"] = "One card for this purchase, used once. Its number never reached tafs or KIFF."
	}
	switch {
	case rc.Order != nil:
		w["shipping"] = fmt.Sprintf("Order %s confirmed by the shop.", rc.Order.Reference)
	case r.State == StateUnknown:
		w["shipping"] = "KIFF is checking whether the shop placed the order. Nothing is tried again until then."
	case r.State == StateDone:
		w["shipping"] = "The shop took the order."
	default:
		w["shipping"] = "No order."
	}
	if rc.Amounts.Refunded > 0 {
		w["refunds"] = fmt.Sprintf("%s refunded.", money(rc.Amounts.Refunded))
	} else {
		w["refunds"] = "No refunds."
	}
	return w
}

// ReceiptKey is a published receipt key: a retired one stays listed so its
// receipts still check; a revoked one (exposed) carries RevokedAt, and a
// receipt it signed at or after then is refused.
type ReceiptKey struct {
	permit.PublicKey
	RevokedAt *time.Time
}

// ParseReceiptKeys reads published receipt keys,
// "kid:base64url(pub)[:revoked_at RFC 3339],…".
func ParseReceiptKeys(s string) ([]ReceiptKey, error) {
	var out []ReceiptKey
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kid, rest, ok := strings.Cut(part, ":")
		enc, revoked, hasRevoked := strings.Cut(rest, ":")
		pubs, err := permit.ParsePublicKeys(kid + ":" + enc)
		if !ok || err != nil || len(pubs) != 1 {
			return nil, fmt.Errorf("purchase: receipt key %q must be kid:base64url(public key)[:revoked_at]", kid)
		}
		k := ReceiptKey{PublicKey: pubs[0]}
		if hasRevoked {
			at, err := time.Parse(time.RFC3339, revoked)
			if err != nil {
				return nil, fmt.Errorf("purchase: receipt key %q's revoked_at must be RFC 3339", kid)
			}
			k.RevokedAt = &at
		}
		out = append(out, k)
	}
	return out, nil
}

// ReceiptJWK is a published receipt key as JWKS lists it.
type ReceiptJWK struct {
	Kty       string `json:"kty"`
	Crv       string `json:"crv"`
	X         string `json:"x"`
	Kid       string `json:"kid"`
	Alg       string `json:"alg"`
	Use       string `json:"use"`
	RevokedAt string `json:"revoked_at,omitempty"`
}

// ReceiptJWKS lists the current receipt key and the published ones.
func ReceiptJWKS(current permit.PublicKey, others []ReceiptKey) map[string][]ReceiptJWK {
	jwk := func(p permit.PublicKey, revoked *time.Time) ReceiptJWK {
		k := ReceiptJWK{Kty: "OKP", Crv: "Ed25519", X: base64.RawURLEncoding.EncodeToString(p.Pub), Kid: p.ID, Alg: "EdDSA", Use: "sig"}
		if revoked != nil {
			k.RevokedAt = revoked.UTC().Format(time.RFC3339)
		}
		return k
	}
	keys := []ReceiptJWK{jwk(current, nil)}
	for _, o := range others {
		if o.ID != current.ID {
			keys = append(keys, jwk(o.PublicKey, o.RevokedAt))
		}
	}
	return map[string][]ReceiptJWK{"keys": keys}
}
