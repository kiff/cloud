package purchase

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Auth is one live authorization as the issuer asks it.
type Auth struct {
	ID   string
	Card string
	// Account is the connected account the event came from: each business
	// is its own, and the authorization counts only on its tenant's.
	Account           string
	Amount            int64
	Currency          string
	MerchantName      string
	MerchantNetworkID string
	MerchantCountry   string
	Category          string
	// Incremental: an incremental authorization or a re-authorization on
	// the same card, after the first approval.
	Incremental bool
	// Verification is the issuer's result: authenticated, exempted,
	// required, or "".
	Verification string
}

// Answer is the live decision: approved or not, and why.
type Answer struct {
	Approved bool
	Reason   string
}

// Why an authorization is declined.
const (
	DeclineNoCase      = "no_case"
	DeclineAccount     = "another_account"
	DeclineState       = "case_not_executing"
	DeclineExpired     = "past_valid_until"
	DeclineTwice       = "already_approved_once"
	DeclineNotFirst    = "incremental_without_approval"
	DeclineCurrency    = "currency"
	DeclineCategory    = "category"
	DeclineMerchant    = "merchant"
	DeclineAmount      = "over_max"
	DeclineVerify      = "verification_required"
	DeclineInternal    = "internal_error"
	ApprovedReason     = "matches_case"
	authorizationLimit = 2 * time.Second
)

// Check is the live decision for a case as kept, without writing: a lookup,
// never a judgement. It approves only a case whose lease is held, within its
// time, approved at most once (an incremental only after that one, within
// the maximum), in its currency and categories, at its own shop.
func Check(r Row, a Auth, now time.Time) Answer {
	no := func(why string) Answer { return Answer{Reason: why} }
	c := r.Case
	switch {
	case r.State != StateExecuting:
		return no(DeclineState)
	case !now.Before(r.ValidUntil):
		return no(DeclineExpired)
	case !a.Incremental && r.ApprovedOnce:
		return no(DeclineTwice)
	case a.Incremental && !r.ApprovedOnce:
		return no(DeclineNotFirst)
	case a.Currency != c.Currency:
		return no(DeclineCurrency)
	case !oneOf(a.Category, c.Grant.Categories):
		return no(DeclineCategory)
	case !sameShop(c.Merchant, a):
		return no(DeclineMerchant)
	case a.Amount <= 0 || r.Authorized+a.Amount > c.Price.Max:
		return no(DeclineAmount)
	case a.Verification == "required":
		return no(DeclineVerify)
	}
	return Answer{Approved: true, Reason: ApprovedReason}
}

// sameShop: the network id when the case has one, else the folded name, and
// the country, always.
func sameShop(m Merchant, a Auth) bool {
	if !strings.EqualFold(m.Country, a.MerchantCountry) {
		return false
	}
	if m.Match.NetworkID != "" {
		return a.MerchantNetworkID == m.Match.NetworkID
	}
	return m.Match.NameKey != "" && strings.Contains(Fold(a.MerchantName), m.Match.NameKey)
}

// Authorize answers a live authorization: once per authorization id (a
// repeat gets the same answer), and the case's single approval written in
// the same transaction as the answer, so two at once can't both be
// approved. Any error is a decline.
func (s *Store) Authorize(ctx context.Context, a Auth) Answer {
	ctx, cancel := context.WithTimeout(ctx, authorizationLimit)
	defer cancel()
	now := s.now()
	var out Answer
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var approved bool
		var reason string
		err := tx.QueryRow(ctx, `SELECT approved, reason FROM purchase.authorizations WHERE authorization_id = $1`, a.ID).Scan(&approved, &reason)
		if err == nil {
			out = Answer{Approved: approved, Reason: reason}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		r, err := scanRow(tx.QueryRow(ctx, `SELECT `+rowCols+` FROM purchase.cases WHERE issued_card = $1 FOR UPDATE`, a.Card))
		if errors.Is(err, ErrNoCase) || a.Card == "" {
			out = Answer{Reason: DeclineNoCase}
			_, err := tx.Exec(ctx, `INSERT INTO purchase.authorizations (authorization_id, tenant_id, case_id, version, approved, reason, amount, at)
				VALUES ($1, '', '', 0, false, $2, $3, $4)`, a.ID, out.Reason, a.Amount, now)
			return err
		}
		if err != nil {
			return err
		}
		out = Check(r, a, now)
		if out.Approved {
			var account string
			if err := tx.QueryRow(ctx, `SELECT account FROM purchase.accounts WHERE tenant_id = $1`, r.Case.Tenant).Scan(&account); err != nil || account == "" || account != a.Account {
				out = Answer{Reason: DeclineAccount}
			}
		}
		if out.Approved {
			tag, err := tx.Exec(ctx, `UPDATE purchase.cases SET approved_once = true, authorized = authorized + $4, updated_at = $5
				WHERE tenant_id = $1 AND case_id = $2 AND version = $3 AND approved_once = $6`,
				r.Case.Tenant, r.Case.CaseID, r.Case.Version, a.Amount, now, a.Incremental)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				out = Answer{Reason: DeclineTwice}
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO purchase.authorizations (authorization_id, tenant_id, case_id, version, approved, reason, amount, at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, a.ID, r.Case.Tenant, r.Case.CaseID, r.Case.Version, out.Approved, out.Reason, a.Amount, now)
		return err
	})
	if err != nil {
		return Answer{Reason: DeclineInternal}
	}
	return out
}
