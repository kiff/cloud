package purchase

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/purchase.sql
var schemaSQL string

// ApplySchema creates the purchase tables.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgschema.Apply(ctx, pool, "purchase/purchase", schemaSQL)
}

// Store keeps cases, their room and their authorizations in Postgres.
type Store struct {
	Pool *pgxpool.Pool
	Now  func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// The errors a decision, a lease or a reconciliation answers with.
var (
	ErrNoCase        = errors.New("purchase: no such case version")
	ErrStale         = errors.New("purchase: the answer is for another version or digest")
	ErrNotHeld       = errors.New("purchase: the case isn't waiting for a decision")
	ErrRoomTaken     = errors.New("purchase: another purchase used this room")
	ErrOutsideLimits = errors.New("purchase: outside the Card's limits")
	ErrNotUnknown    = errors.New("purchase: the case's outcome isn't unknown")
)

// Decision is how a case is decided: by the owner's signature, or by KIFF
// within the Card's limits. Limits are the Card's at that moment.
type Decision struct {
	By           string `json:"decided_by"`
	Signature    string `json:"signature,omitempty"`
	ApproverRole string `json:"approver_role,omitempty"`
	// Approver is who decided (their KIFF subject), and Method how:
	// device_biometric or security_key. The receipt says both.
	Approver string  `json:"approver,omitempty"`
	Method   string  `json:"method,omitempty"`
	Card     CardRef `json:"card"`
	Limits   Limits  `json:"limits"`
}

// Decided by.
const (
	ByOwner      = "owner"
	ByCardLimits = "card_limits"
)

// Limits are a Card's purchase limits; zero is none. PerDay is the Card's
// room for a day in the case's currency, kept for either decision. A day is
// the Card's calendar day in its time zone (Location, an IANA name; "" or
// one the time zone database doesn't know is UTC), as every other day on a
// Card is.
type Limits struct {
	PerPurchase  int64    `json:"per_purchase,omitempty"`
	PerDay       int64    `json:"per_day,omitempty"`
	PerHourCount int      `json:"purchases_per_hour,omitempty"`
	PerDayCount  int      `json:"purchases_per_day,omitempty"`
	Categories   []string `json:"categories,omitempty"`
	Location     string   `json:"location,omitempty"`
}

// In is l with its days in the time zone named loc.
func (l Limits) In(loc string) Limits {
	l.Location = loc
	return l
}

// day is the Card's calendar day at now: the date its room is kept under,
// and the instant it started.
func (l Limits) day(now time.Time) (date, start time.Time) {
	loc := time.UTC
	if l.Location != "" {
		if z, err := time.LoadLocation(l.Location); err == nil {
			loc = z
		}
	}
	t := now.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// Row is a case version as kept.
type Row struct {
	Case         Case
	Digest       string
	State        string
	Ended        string
	Reserved     int64
	ValidUntil   time.Time
	ApprovedOnce bool
	Authorized   int64
	LeaseHolder  string
	Outcome      string
	IssuedCard   string
	roomDay      time.Time
}

// Open records a case's version as held, waiting for its decision. A
// version after the first supersedes the earlier ones and releases any
// room they reserved, in the same transaction.
func (s *Store) Open(ctx context.Context, c Case) (string, error) {
	if err := c.Check(); err != nil {
		return "", err
	}
	digest, err := c.Digest()
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(c)
	now := s.now()
	return digest, pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT version, reserved, room_day, state FROM purchase.cases
			WHERE tenant_id = $1 AND case_id = $2 AND version < $3 AND state IN ('held', 'approved') FOR UPDATE`, c.Tenant, c.CaseID, c.Version)
		if err != nil {
			return err
		}
		type old struct {
			version  int
			reserved int64
			day      *time.Time
			state    string
		}
		var olds []old
		for rows.Next() {
			var o old
			if err := rows.Scan(&o.version, &o.reserved, &o.day, &o.state); err != nil {
				rows.Close()
				return err
			}
			olds = append(olds, o)
		}
		rows.Close()
		for _, o := range olds {
			if err := release(ctx, tx, c.Tenant, c.Card.ID, c.Currency, o.day, o.reserved); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE purchase.cases SET state = 'superseded', reserved = 0, updated_at = $4
				WHERE tenant_id = $1 AND case_id = $2 AND version = $3`, c.Tenant, c.CaseID, o.version, now); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO purchase.cases (tenant_id, case_id, version, digest, body, state, card_id, currency, max_amount, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, 'held', $6, $7, $8, $9, $9)`,
			c.Tenant, c.CaseID, c.Version, digest, body, c.Card.ID, c.Currency, c.Price.Max, now)
		return err
	})
}

// Decide decides the case's version: the answer must name its current
// version and digest (else ErrStale). Its room is reserved in the same
// transaction, by a conditional update that fails if another purchase took
// the room first (ErrRoomTaken; the case stays held). Within limits, each
// limit is checked first (ErrOutsideLimits: the case stays held for the
// owner). Approved, it holds for 15 minutes.
func (s *Store) Decide(ctx context.Context, tenant, caseID string, version int, digest string, d Decision) error {
	if d.By != ByOwner && d.By != ByCardLimits {
		return fmt.Errorf("purchase: decided by %q", d.By)
	}
	now := s.now()
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		r, err := lockLatest(ctx, tx, tenant, caseID)
		if err != nil {
			return err
		}
		if r.Case.Version != version || r.Digest != digest {
			return ErrStale
		}
		if r.State != StateHeld {
			return ErrNotHeld
		}
		c := r.Case
		// The Card's room row is locked before anything is counted, so two
		// decisions on one Card serialize here: the second counts the first.
		day, _ := d.Limits.day(now)
		if _, err := tx.Exec(ctx, `INSERT INTO purchase.room (tenant_id, card_id, day, currency) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
			tenant, c.Card.ID, day, c.Currency); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM purchase.room WHERE tenant_id = $1 AND card_id = $2 AND day = $3 AND currency = $4 FOR UPDATE`,
			tenant, c.Card.ID, day, c.Currency); err != nil {
			return err
		}
		if d.By == ByCardLimits {
			if err := s.withinLimits(ctx, tx, c, d.Limits, now); err != nil {
				return err
			}
		}
		limit := d.Limits.PerDay
		tag, err := tx.Exec(ctx, `UPDATE purchase.room SET used = used + $5
			WHERE tenant_id = $1 AND card_id = $2 AND day = $3 AND currency = $4 AND ($6 = 0 OR used + $5 <= $6)`,
			tenant, c.Card.ID, day, c.Currency, c.Price.Max, limit)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrRoomTaken
		}
		decided, _ := json.Marshal(d)
		_, err = tx.Exec(ctx, `UPDATE purchase.cases SET state = 'approved', decided = $4, reserved = $5, room_day = $6, decided_at = $7, valid_until = $8, updated_at = $7
			WHERE tenant_id = $1 AND case_id = $2 AND version = $3`,
			tenant, caseID, version, decided, c.Price.Max, day, now, now.Add(ValidFor*time.Second))
		return err
	})
}

// withinLimits checks a case against a Card's limits, its counts read in the
// decision's transaction.
func (s *Store) withinLimits(ctx context.Context, tx pgx.Tx, c Case, l Limits, now time.Time) error {
	// A force capture or an over-capture on one of its purchases pauses the
	// Card's buying until the owner resumes it.
	var paused bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM purchase.paused WHERE tenant_id = $1 AND card_id = $2)`, c.Tenant, c.Card.ID).Scan(&paused); err != nil {
		return err
	}
	if paused {
		return fmt.Errorf("%w: the Card's buying is paused", ErrOutsideLimits)
	}
	if l.PerPurchase > 0 && c.Price.Max > l.PerPurchase {
		return fmt.Errorf("%w: over the per-purchase limit", ErrOutsideLimits)
	}
	if len(l.Categories) > 0 && !oneOf(c.Merchant.Category, l.Categories) {
		return fmt.Errorf("%w: a category the Card doesn't open", ErrOutsideLimits)
	}
	count := func(since time.Time) (int, error) {
		var n int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM purchase.cases WHERE tenant_id = $1 AND card_id = $2 AND decided_at >= $3
			AND decided->>'decided_by' = 'card_limits' AND state <> 'superseded'`, c.Tenant, c.Card.ID, since).Scan(&n)
		return n, err
	}
	if l.PerHourCount > 0 {
		n, err := count(now.Add(-time.Hour))
		if err != nil {
			return err
		}
		if n >= l.PerHourCount {
			return fmt.Errorf("%w: the purchases an hour", ErrOutsideLimits)
		}
	}
	if l.PerDayCount > 0 {
		_, start := l.day(now)
		n, err := count(start)
		if err != nil {
			return err
		}
		if n >= l.PerDayCount {
			return fmt.Errorf("%w: the purchases a day", ErrOutsideLimits)
		}
	}
	return nil
}

// Refuse ends a held case the owner refused. No room was reserved.
func (s *Store) Refuse(ctx context.Context, tenant, caseID string, version int, digest string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		r, err := lockLatest(ctx, tx, tenant, caseID)
		if err != nil {
			return err
		}
		if r.Case.Version != version || r.Digest != digest {
			return ErrStale
		}
		if r.State != StateHeld {
			return ErrNotHeld
		}
		_, err = tx.Exec(ctx, `UPDATE purchase.cases SET state = 'refused', updated_at = $4 WHERE tenant_id = $1 AND case_id = $2 AND version = $3`,
			tenant, caseID, version, s.now())
		return err
	})
}

// Lease takes the case version's one execution: an approved case, within
// its time, whose outcome isn't unknown. A second call gets the case as it
// is, never a second execution: taken reports whether this call took it.
func (s *Store) Lease(ctx context.Context, tenant, caseID string, version int, holder string) (Row, bool, error) {
	now := s.now()
	tag, err := s.Pool.Exec(ctx, `UPDATE purchase.cases SET state = 'executing', lease_holder = $4, lease_until = valid_until, updated_at = $5
		WHERE tenant_id = $1 AND case_id = $2 AND version = $3 AND state = 'approved' AND valid_until > $5 AND outcome <> 'unknown'`,
		tenant, caseID, version, holder, now)
	if err != nil {
		return Row{}, false, err
	}
	r, err := s.Get(ctx, tenant, caseID, version)
	return r, err == nil && tag.RowsAffected() == 1, err
}

// Finish ends the lease's execution: ordered is done; unknown blocks every
// further execution until reconciled; not ordered, with why, ends it with
// nothing bought and releases its room, unless an authorization stands on
// its card: then it's unknown. Only the lease's holder finishes.
func (s *Store) Finish(ctx context.Context, tenant, caseID string, version int, holder, outcome, ended string) error {
	now := s.now()
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		r, err := lockVersion(ctx, tx, tenant, caseID, version)
		if err != nil {
			return err
		}
		if r.State != StateExecuting || r.LeaseHolder != holder {
			return fmt.Errorf("purchase: %s doesn't hold this case's lease", holder)
		}
		switch outcome {
		case OutcomeOrdered:
			return setState(ctx, tx, r, StateDone, OutcomeOrdered, "", now)
		case OutcomeUnknown:
			return setState(ctx, tx, r, StateUnknown, OutcomeUnknown, "", now)
		case OutcomeNotOrdered:
			// Money authorized on the card isn't "not ordered" for sure:
			// it's unknown until reconciled, and its room stays reserved.
			if r.Authorized > 0 {
				return setState(ctx, tx, r, StateUnknown, OutcomeUnknown, "", now)
			}
			if err := release(ctx, tx, tenant, r.Case.Card.ID, r.Case.Currency, dayOf(r), r.Reserved); err != nil {
				return err
			}
			return setState(ctx, tx, r, StateNotBought, OutcomeNotOrdered, ended, now)
		}
		return fmt.Errorf("purchase: outcome %q", outcome)
	})
}

// Reconcile settles an unknown outcome: an order exists (done), or none does
// (approved again under the same case id, only with no authorization
// standing and time left; otherwise not bought, its room released).
// Nothing else moves an unknown case.
func (s *Store) Reconcile(ctx context.Context, tenant, caseID string, version int, ordered bool) error {
	now := s.now()
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		r, err := lockVersion(ctx, tx, tenant, caseID, version)
		if err != nil {
			return err
		}
		if r.State != StateUnknown {
			return ErrNotUnknown
		}
		if ordered {
			return setState(ctx, tx, r, StateDone, OutcomeOrdered, "", now)
		}
		// Back to approved only with no authorization standing on the card
		// and time left; otherwise it ends with nothing bought.
		if now.Before(r.ValidUntil) && r.Authorized == 0 {
			_, err := tx.Exec(ctx, `UPDATE purchase.cases SET state = 'approved', outcome = 'not_ordered', lease_holder = '', lease_until = NULL, updated_at = $4
				WHERE tenant_id = $1 AND case_id = $2 AND version = $3`, tenant, caseID, version, now)
			return err
		}
		if err := release(ctx, tx, tenant, r.Case.Card.ID, r.Case.Currency, dayOf(r), r.Reserved); err != nil {
			return err
		}
		ended := EndedNotUsedInTime
		if r.Authorized > 0 {
			ended = EndedDeclinedAtAuth
		}
		return setState(ctx, tx, r, StateNotBought, OutcomeNotOrdered, ended, now)
	})
}

// Expire ends approved cases past their time with no execution, releasing
// their room. It answers how many ended.
func (s *Store) Expire(ctx context.Context) (int, error) {
	now := s.now()
	n := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT tenant_id, case_id, version FROM purchase.cases WHERE state = 'approved' AND valid_until <= $1 FOR UPDATE SKIP LOCKED`, now)
		if err != nil {
			return err
		}
		type key struct {
			tenant, id string
			version    int
		}
		var keys []key
		for rows.Next() {
			var k key
			if err := rows.Scan(&k.tenant, &k.id, &k.version); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, k)
		}
		rows.Close()
		for _, k := range keys {
			r, err := lockVersion(ctx, tx, k.tenant, k.id, k.version)
			if err != nil {
				return err
			}
			if err := release(ctx, tx, k.tenant, r.Case.Card.ID, r.Case.Currency, dayOf(r), r.Reserved); err != nil {
				return err
			}
			if err := setState(ctx, tx, r, StateExpired, r.Outcome, EndedNotUsedInTime, now); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// SweepLeases moves an execution whose holder never finished, past its
// lease, to unknown: nothing executes it again until reconciliation says
// whether an order exists. It answers how many it moved.
func (s *Store) SweepLeases(ctx context.Context) (int, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE purchase.cases SET state = 'unknown', outcome = 'unknown', updated_at = $1
		WHERE state = 'executing' AND lease_until <= $1`, s.now())
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// AttachCard records the single-use card issued for an approved case: the
// one card the live authorization finds it by.
func (s *Store) AttachCard(ctx context.Context, tenant, caseID string, version int, card string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE purchase.cases SET issued_card = $4, updated_at = $5
		WHERE tenant_id = $1 AND case_id = $2 AND version = $3 AND state IN ('approved', 'executing') AND issued_card = ''`,
		tenant, caseID, version, card, s.now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("purchase: the case can't take a card now")
	}
	return nil
}

// Hold records the hold KIFF opened for a held case version, which the
// owner answers: once, and only while the case is held.
func (s *Store) Hold(ctx context.Context, tenant, caseID string, version int, exceptionID string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE purchase.cases SET exception_id = $4, updated_at = $5
		WHERE tenant_id = $1 AND case_id = $2 AND version = $3 AND state = 'held' AND exception_id = ''`, tenant, caseID, version, exceptionID, s.now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotHeld
	}
	return nil
}

// ByException reads the case version a hold was opened for.
func (s *Store) ByException(ctx context.Context, tenant, exceptionID string) (Row, error) {
	if exceptionID == "" {
		return Row{}, ErrNoCase
	}
	return scanRow(s.Pool.QueryRow(ctx, `SELECT `+rowCols+` FROM purchase.cases WHERE tenant_id = $1 AND exception_id = $2`, tenant, exceptionID))
}

// Latest reads a case's newest version.
func (s *Store) Latest(ctx context.Context, tenant, caseID string) (Row, error) {
	return scanRow(s.Pool.QueryRow(ctx, `SELECT `+rowCols+` FROM purchase.cases WHERE tenant_id = $1 AND case_id = $2 ORDER BY version DESC LIMIT 1`, tenant, caseID))
}

// Get reads a case version.
func (s *Store) Get(ctx context.Context, tenant, caseID string, version int) (Row, error) {
	return scanRow(s.Pool.QueryRow(ctx, `SELECT `+rowCols+` FROM purchase.cases WHERE tenant_id = $1 AND case_id = $2 AND version = $3`, tenant, caseID, version))
}

const rowCols = `body, digest, state, ended, reserved, valid_until, approved_once, authorized, lease_holder, outcome, issued_card, room_day`

func scanRow(row pgx.Row) (Row, error) {
	var r Row
	var body []byte
	var until, day *time.Time
	err := row.Scan(&body, &r.Digest, &r.State, &r.Ended, &r.Reserved, &until, &r.ApprovedOnce, &r.Authorized, &r.LeaseHolder, &r.Outcome, &r.IssuedCard, &day)
	if errors.Is(err, pgx.ErrNoRows) {
		return Row{}, ErrNoCase
	}
	if err != nil {
		return Row{}, err
	}
	if until != nil {
		r.ValidUntil = *until
	}
	if day != nil {
		r.roomDay = *day
	}
	return r, json.Unmarshal(body, &r.Case)
}

func lockLatest(ctx context.Context, tx pgx.Tx, tenant, caseID string) (Row, error) {
	return scanRow(tx.QueryRow(ctx, `SELECT `+rowCols+` FROM purchase.cases WHERE tenant_id = $1 AND case_id = $2 ORDER BY version DESC LIMIT 1 FOR UPDATE`, tenant, caseID))
}

func lockVersion(ctx context.Context, tx pgx.Tx, tenant, caseID string, version int) (Row, error) {
	return scanRow(tx.QueryRow(ctx, `SELECT `+rowCols+` FROM purchase.cases WHERE tenant_id = $1 AND case_id = $2 AND version = $3 FOR UPDATE`, tenant, caseID, version))
}

func setState(ctx context.Context, tx pgx.Tx, r Row, state, outcome, ended string, now time.Time) error {
	if !CanMove(r.State, state) {
		return fmt.Errorf("purchase: a %s case can't become %s", r.State, state)
	}
	reserved := r.Reserved
	if state == StateNotBought || state == StateExpired {
		reserved = 0
	}
	_, err := tx.Exec(ctx, `UPDATE purchase.cases SET state = $4, outcome = $5, ended = $6, reserved = $7, updated_at = $8
		WHERE tenant_id = $1 AND case_id = $2 AND version = $3`, r.Case.Tenant, r.Case.CaseID, r.Case.Version, state, outcome, ended, reserved, now)
	return err
}

func release(ctx context.Context, tx pgx.Tx, tenant, card, currency string, day *time.Time, amount int64) error {
	if day == nil || amount == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE purchase.room SET used = GREATEST(used - $5, 0) WHERE tenant_id = $1 AND card_id = $2 AND day = $3 AND currency = $4`,
		tenant, card, *day, currency, amount)
	return err
}

func dayOf(r Row) *time.Time {
	if r.roomDay.IsZero() {
		return nil
	}
	d := r.roomDay
	return &d
}
