// Package agentdrafts keeps drafts of a new agent and its Cards. A draft
// grants nothing. Once confirmed by an owner its Cards are
// issued, and the draft's one-time claim secret lets whoever holds it
// (a runtime such as tafs cloud) collect the agent's key, once: the key
// is minted at the claim and never stored in plain text.
package agentdrafts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// Status is where a draft is in its life.
type Status string

const (
	Draft       Status = "draft"        // submitted, waiting for an owner
	Confirming  Status = "confirming"   // its Cards are being issued; nothing can claim or discard it
	Confirmed   Status = "confirmed"    // every Card is issued; the key can be claimed
	Claimed     Status = "claimed"      // the key was collected
	RollingBack Status = "rolling_back" // confirming failed; its Cards are being revoked
	Discarding  Status = "discarding"   // thrown away; its Cards are being revoked
	Discarded   Status = "discarded"    // thrown away; its Cards revoked
	Failed      Status = "failed"       // confirming it failed; nothing it issued stands
	// Trying is a revision draft its owner is trying (kiff/tafs#173): its
	// trial Cards are issued, and calls made under its trial are sent.
	// Going back returns it to draft; Install confirms it.
	Trying Status = "trying"
)

// DefaultTTL is how long a draft can be confirmed and claimed.
const DefaultTTL = 24 * time.Hour

// TrialTTL bounds a trial nobody ended (kiff/tafs#173): its Cards lapse
// then, and the draft can still be installed until then.
const TrialTTL = 30 * 24 * time.Hour

// How a trial ended.
const (
	EndedBack    = "back"    // its owner went back to the live version
	EndedInstall = "install" // its owner installed the version
	EndedExpired = "expired" // its agent's runtime ended it idle, or TrialTTL passed
)

// MaxWaiting bounds Waiting's list.
const MaxWaiting = 100

// ConfirmTimeout bounds a confirmation, and each attempt to undo one: it
// runs to its end even if the caller goes away. A draft still confirming
// after StuckAfter was interrupted, and Sweep rolls it back.
const (
	ConfirmTimeout = 30 * time.Second
	StuckAfter     = 5 * time.Minute
)

var (
	ErrNotFound = errors.New("agentdrafts: no such draft")
	ErrState    = errors.New("agentdrafts: the draft is not in a state that allows this")
	ErrVersion  = errors.New("agentdrafts: the draft changed since it was shown")
	ErrExpired  = errors.New("agentdrafts: the draft expired")
)

// D is one draft.
type D struct {
	ID       string
	TenantID string
	AgentID  string
	Label    string
	// Cards are the Card bodies as submitted (a JSON array), issued as is
	// on confirmation.
	Cards json.RawMessage
	// Version names exactly what was submitted; confirmation names it.
	Version     string
	Status      Status
	CreatedBy   string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	ConfirmedBy string
	// ConfirmedRole is the confirmer's role on the account, "" when a key
	// confirmed it: a claim checks a person still holds a role that gives
	// keys.
	ConfirmedRole string
	ConfirmedAt   *time.Time
	// CardIDs are the Cards its confirmation issues, recorded before the
	// first is issued, so an interrupted confirmation can be undone.
	CardIDs   []string
	ClaimHash string
	ClaimedAt *time.Time
	KeyID     string
	Reason    string // why it failed
	// Revision is a draft for an agent that already held Cards
	// (kiff/tafs#107): its Cards revise or add to the agent's, all in one
	// transaction, and it mints no key, since the agent has one.
	Revision bool
	// Trial is the draft's latest trial (kiff/tafs#173); nil when it was
	// never tried.
	Trial *Trial
	// Revises are the agent's Cards it revises, each with the revision
	// it was drafted from. Confirming refuses once one has moved on: the
	// owner would approve over terms they weren't shown.
	Revises []Revised
	// Retires are the agent's Cards confirming revokes, each with the
	// revision it was drafted from: going back to a version that didn't
	// hold them (kiff/tafs#107), so its tools and limits move back
	// together. Revoked in the same transaction as its Cards are issued,
	// and refused the same way once one has moved on.
	Retires []Revised
	// RetireRest is a restore (kiff/tafs#107): confirming retires every
	// other Card the agent holds alone then, even one issued after the
	// draft was made, which then refuses the confirmation. Kept apart from
	// Retires, which can be empty.
	RetireRest bool
	// FromHold is the held call a draft was made for ("Allow this from now
	// on", kiff/tafs/docs/allow-from-now-on.md): the exception's id, on a
	// hold of the draft's own agent. Confirming keeps it on each Card
	// revision it writes, so the revision says where it came from. "" for
	// every other draft.
	FromHold string
}

// Trial is a draft's trial: the trial Cards it issued, when it started,
// when its Cards lapse, and how and when it ended.
type Trial struct {
	CardIDs   []string   `json:"card_ids"`
	StartedAt time.Time  `json:"started_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	EndedBy   string     `json:"ended_by,omitempty"`
}

// TrialOpen reports whether the draft is being tried at `at`: trying, and
// its trial Cards not lapsed.
func (d D) TrialOpen(at time.Time) bool {
	return d.Status == Trying && d.Trial != nil && at.Before(d.Trial.ExpiresAt)
}

// TrialEnd is how the draft's trial ended at `at`, if it did: by the
// recorded end, or by lapsing while still trying.
func (d D) TrialEnd(at time.Time) (time.Time, string, bool) {
	switch {
	case d.Trial == nil:
		return time.Time{}, "", false
	case d.Trial.EndedAt != nil:
		return *d.Trial.EndedAt, d.Trial.EndedBy, true
	case d.Status == Trying && !at.Before(d.Trial.ExpiresAt):
		return d.Trial.ExpiresAt, EndedExpired, true
	}
	return time.Time{}, "", false
}

// Revised is a Card a revision draft revises, and its revision number
// when the draft was made.
type Revised struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
}

// RetiresCard reports whether the draft retires the Card id.
func (d D) RetiresCard(id string) bool {
	for _, r := range d.Retires {
		if r.ID == id {
			return true
		}
	}
	return false
}

// RevisesCard reports whether the draft revises the Card id.
func (d D) RevisesCard(id string) bool {
	for _, r := range d.Revises {
		if r.ID == id {
			return true
		}
	}
	return false
}

// VersionOf names a draft's content: its agent, label and Cards, and the
// Cards it retires, if any, so the version confirmed names those too. A
// draft that retires none has the version it always had.
func VersionOf(agentID, label string, cards json.RawMessage, retires ...string) string {
	h := sha256.New()
	for _, part := range [][]byte{[]byte(agentID), []byte(label), cards} {
		h.Write(part)
		h.Write([]byte{0})
	}
	if len(retires) > 0 {
		h.Write([]byte("retires"))
		h.Write([]byte{0})
		for _, id := range retires {
			h.Write([]byte(id))
			h.Write([]byte{0})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// NewClaimSecret returns a claim secret and the hash a store keeps.
func NewClaimSecret() (secret, hash string) {
	var b [32]byte
	_, _ = rand.Read(b[:])
	secret = "kiff_claim_" + hex.EncodeToString(b[:])
	return secret, HashClaim(secret)
}

// HashClaim is the stored form of a claim secret.
func HashClaim(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Store keeps drafts. Each state change is a compare-and-set, so two
// confirmations, or two claims, act once.
//
// A confirmation goes draft → confirming → confirmed. Only a confirmed
// draft can be claimed, so no key exists while its Cards are being
// issued; and a confirming draft can't be discarded, so a discard can't
// miss Cards issued after it. A confirmation that fails goes to
// rolling_back, and a confirmed draft that is discarded to discarding:
// neither can be claimed, and Settle ends them (failed, discarded) only
// once every Card is revoked.
type Store interface {
	Create(ctx context.Context, d D) error
	Get(ctx context.Context, tenantID, id string) (D, error)
	// Waiting lists an account's drafts still waiting for someone to
	// confirm them, unexpired at `at`, newest first, at most MaxWaiting.
	Waiting(ctx context.Context, tenantID string, at time.Time) ([]D, error)
	// Confirm moves a draft of this version, unexpired at `at`, to
	// confirming by `by`, recording the Cards it will issue. A draft being
	// tried is confirmed too (Install), and its trial ends then.
	Confirm(ctx context.Context, tenantID, id, version, by, role string, cardIDs []string, at time.Time) (D, error)
	// StartTrial moves a revision draft of this version, unexpired at
	// `at`, to trying with its trial t; the draft can be confirmed until
	// the trial's Cards lapse.
	StartTrial(ctx context.Context, tenantID, id, version string, t Trial, at time.Time) (D, error)
	// EndTrial moves a draft being tried back to draft, recording how its
	// trial ended, and returns it.
	EndTrial(ctx context.Context, tenantID, id, by string, at time.Time) (D, error)
	// TryingFor lists the agent's drafts being tried, lapsed or not.
	TryingFor(ctx context.Context, tenantID, agentID string) ([]D, error)
	// Finish moves a confirming draft to confirmed: its Cards are issued.
	Finish(ctx context.Context, tenantID, id string) (D, error)
	// Fail moves a confirming draft to rolling_back.
	Fail(ctx context.Context, tenantID, id, reason string) (D, error)
	// Settle ends a draft whose Cards are all revoked: rolling_back to
	// failed, discarding to discarded.
	Settle(ctx context.Context, tenantID, id string) error
	// Unsettled lists, across accounts, the drafts rolling back or
	// discarding, and those still confirming since before `before`.
	Unsettled(ctx context.Context, before time.Time) ([]D, error)
	// Claim moves a confirmed, unexpired draft whose claim hash matches
	// to claimed. It is found by id alone: the claim carries no account.
	Claim(ctx context.Context, id, claimHash string, at time.Time) (D, error)
	// SetKey records the key minted for a claim; Unclaim undoes a claim
	// whose key could not be minted.
	SetKey(ctx context.Context, tenantID, id, keyID string) error
	Unclaim(ctx context.Context, tenantID, id string) error
	// Discard throws away a draft not yet claimed or confirming,
	// returning it as it was: a draft is discarded at once, a confirmed
	// one moves to discarding until its Cards are revoked. Discarding a
	// draft already discarding returns it again, so a retry can finish it.
	Discard(ctx context.Context, tenantID, id string) (D, error)
}

// Cards is what undoing a draft needs of the Card store.
type Cards interface {
	ForSubject(ctx context.Context, tenantID, subject string) ([]mandate.Mandate, error)
	Revoke(ctx context.Context, tenantID, mandateID string, at time.Time) error
	Revisions(ctx context.Context, tenantID, mandateID string) ([]mandate.Revision, error)
}

// Undo revokes the Cards a rolling-back or discarding draft issued, then
// settles it. A recorded Card its agent doesn't hold was never issued. A
// Card a revision draft revises is the agent's own and is never revoked.
// It fails, leaving the draft unsettled for a retry or Sweep, unless
// every Card is revoked.
func Undo(ctx context.Context, s Store, cards Cards, d D, at time.Time) error {
	if d.Trial != nil {
		// An Install that failed leaves the trial ended: its Cards go too.
		for _, id := range d.Trial.CardIDs {
			if err := cards.Revoke(ctx, d.TenantID, id, at); err != nil && !errors.Is(err, mandate.ErrNotFound) {
				return fmt.Errorf("undo draft %s: revoke trial Card %s: %w", d.ID, id, err)
			}
		}
	}
	if len(d.CardIDs) > 0 {
		held, err := cards.ForSubject(ctx, d.TenantID, d.AgentID)
		if err != nil {
			return fmt.Errorf("undo draft %s: list Cards: %w", d.ID, err)
		}
		issued := map[string]bool{}
		for _, m := range held {
			issued[m.ID] = true
		}
		for _, id := range d.CardIDs {
			if !issued[id] || d.RevisesCard(id) {
				continue
			}
			if err := cards.Revoke(ctx, d.TenantID, id, at); err != nil && !errors.Is(err, mandate.ErrNotFound) {
				return fmt.Errorf("undo draft %s: revoke %s: %w", d.ID, id, err)
			}
		}
	}
	if err := s.Settle(ctx, d.TenantID, d.ID); err != nil {
		return fmt.Errorf("undo draft %s: %w", d.ID, err)
	}
	return nil
}

// Sweep finishes what interrupted confirmations and discards left: a
// draft confirming since before StuckAfter is rolled back, and every
// draft rolling back or discarding is undone. It returns how many it
// settled and the first error.
func Sweep(ctx context.Context, s Store, cards Cards, now time.Time) (int, error) {
	ds, err := s.Unsettled(ctx, now.Add(-StuckAfter))
	if err != nil {
		return 0, err
	}
	n, first := 0, error(nil)
	for _, d := range ds {
		if d.Status == Confirming && d.Revision {
			// Its Cards were issued all together or not at all, so what
			// stands says which: finished, or failed with nothing to undo.
			done, err := revisionIssued(ctx, cards, d)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			if done {
				if _, err := s.Finish(ctx, d.TenantID, d.ID); err != nil && !errors.Is(err, ErrState) && first == nil {
					first = err
				}
				n++
				continue
			}
		}
		if d.Status == Confirming {
			if d, err = s.Fail(ctx, d.TenantID, d.ID, "the confirmation did not finish"); err != nil {
				if !errors.Is(err, ErrState) && first == nil {
					first = err
				}
				continue
			}
		}
		if err := Undo(ctx, s, cards, d, now); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		n++
	}
	return n, first
}

// revisionIssued reports whether a revision draft's Cards were issued: a
// Card it revises is past the revision it was drafted from, or a Card it
// adds is held by its agent. They are issued in one transaction
// (mandate.Store.IssueAll), so one is enough.
func revisionIssued(ctx context.Context, cards Cards, d D) (bool, error) {
	for _, r := range d.Revises {
		revs, err := cards.Revisions(ctx, d.TenantID, r.ID)
		if err != nil {
			return false, fmt.Errorf("settle draft %s: revisions of %s: %w", d.ID, r.ID, err)
		}
		if n := len(revs); n > 0 && revs[n-1].Number > r.Revision {
			return true, nil
		}
	}
	held, err := cards.ForSubject(ctx, d.TenantID, d.AgentID)
	if err != nil {
		return false, fmt.Errorf("settle draft %s: list Cards: %w", d.ID, err)
	}
	for _, m := range held {
		if slices.Contains(d.CardIDs, m.ID) && !d.RevisesCard(m.ID) {
			return true, nil
		}
	}
	return false, nil
}

// MemoryStore is the in-process Store.
type MemoryStore struct {
	mu     sync.Mutex
	drafts map[string]D // id
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{drafts: map[string]D{}} }

func (m *MemoryStore) Create(_ context.Context, d D) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.drafts[d.ID]; ok {
		return ErrState
	}
	m.drafts[d.ID] = d
	return nil
}

func (m *MemoryStore) get(tenantID, id string) (D, bool) {
	d, ok := m.drafts[id]
	return d, ok && d.TenantID == tenantID
}

func (m *MemoryStore) Get(_ context.Context, tenantID, id string) (D, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.get(tenantID, id); ok {
		return d, nil
	}
	return D{}, ErrNotFound
}

func (m *MemoryStore) Waiting(_ context.Context, tenantID string, at time.Time) ([]D, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []D
	for _, d := range m.drafts {
		if d.TenantID == tenantID && d.Status == Draft && at.Before(d.ExpiresAt) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > MaxWaiting {
		out = out[:MaxWaiting]
	}
	return out, nil
}

func (m *MemoryStore) Confirm(_ context.Context, tenantID, id, version, by, role string, cardIDs []string, at time.Time) (D, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.get(tenantID, id)
	switch {
	case !ok:
		return D{}, ErrNotFound
	case d.Status != Draft && d.Status != Trying:
		return D{}, ErrState
	case !at.Before(d.ExpiresAt):
		return D{}, ErrExpired
	case d.Version != version:
		return D{}, ErrVersion
	}
	when := at
	if d.Status == Trying {
		d.Trial = endedTrial(d.Trial, EndedInstall, at)
	}
	d.Status, d.ConfirmedBy, d.ConfirmedRole, d.ConfirmedAt = Confirming, by, role, &when
	d.CardIDs = append([]string(nil), cardIDs...)
	m.drafts[id] = d
	return d, nil
}

// backExpiry is when a draft whose trial ended at `at` expires: as a
// fresh draft would, never later than it already did.
func backExpiry(was, at time.Time) time.Time {
	if e := at.Add(DefaultTTL); e.Before(was) {
		return e
	}
	return was
}

// endedTrial is t ended at `at` by `by`.
func endedTrial(t *Trial, by string, at time.Time) *Trial {
	if t == nil {
		return nil
	}
	c := *t
	c.CardIDs = slices.Clone(t.CardIDs)
	when := at
	c.EndedAt, c.EndedBy = &when, by
	return &c
}

func (m *MemoryStore) StartTrial(_ context.Context, tenantID, id, version string, t Trial, at time.Time) (D, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.get(tenantID, id)
	switch {
	case !ok:
		return D{}, ErrNotFound
	case d.Status != Draft || !d.Revision:
		return D{}, ErrState
	case !at.Before(d.ExpiresAt):
		return D{}, ErrExpired
	case d.Version != version:
		return D{}, ErrVersion
	}
	t.CardIDs = slices.Clone(t.CardIDs)
	d.Status, d.Trial, d.ExpiresAt = Trying, &t, t.ExpiresAt
	m.drafts[id] = d
	return d, nil
}

func (m *MemoryStore) EndTrial(_ context.Context, tenantID, id, by string, at time.Time) (D, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.get(tenantID, id)
	switch {
	case !ok:
		return D{}, ErrNotFound
	case d.Status != Trying:
		return D{}, ErrState
	}
	d.Status, d.Trial, d.ExpiresAt = Draft, endedTrial(d.Trial, by, at), backExpiry(d.ExpiresAt, at)
	m.drafts[id] = d
	return d, nil
}

func (m *MemoryStore) TryingFor(_ context.Context, tenantID, agentID string) ([]D, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []D
	for _, d := range m.drafts {
		if d.TenantID == tenantID && d.AgentID == agentID && d.Status == Trying {
			out = append(out, d)
		}
	}
	return out, nil
}

// move changes a draft's status from one of `from`; s.mu must be held.
func (m *MemoryStore) move(tenantID, id string, to Status, from ...Status) (D, error) {
	d, ok := m.get(tenantID, id)
	if !ok {
		return D{}, ErrNotFound
	}
	for _, f := range from {
		if d.Status == f {
			d.Status = to
			m.drafts[id] = d
			return d, nil
		}
	}
	return D{}, ErrState
}

func (m *MemoryStore) Finish(_ context.Context, tenantID, id string) (D, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.move(tenantID, id, Confirmed, Confirming)
}

func (m *MemoryStore) Fail(_ context.Context, tenantID, id, reason string) (D, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.move(tenantID, id, RollingBack, Confirming)
	if err != nil {
		return D{}, err
	}
	d.Reason = reason
	m.drafts[id] = d
	return d, nil
}

func (m *MemoryStore) Settle(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.move(tenantID, id, Failed, RollingBack); err == nil {
		return nil
	}
	_, err := m.move(tenantID, id, Discarded, Discarding)
	return err
}

func (m *MemoryStore) Unsettled(_ context.Context, before time.Time) ([]D, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []D
	for _, d := range m.drafts {
		if d.Status == RollingBack || d.Status == Discarding || (d.Status == Confirming && d.ConfirmedAt.Before(before)) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (m *MemoryStore) Claim(_ context.Context, id, claimHash string, at time.Time) (D, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drafts[id]
	switch {
	case !ok || d.ClaimHash != claimHash:
		return D{}, ErrNotFound
	case d.Status != Confirmed || d.Revision:
		return D{}, ErrState
	case !at.Before(d.ExpiresAt):
		return D{}, ErrExpired
	}
	when := at
	d.Status, d.ClaimedAt = Claimed, &when
	m.drafts[id] = d
	return d, nil
}

func (m *MemoryStore) SetKey(_ context.Context, tenantID, id, keyID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.get(tenantID, id)
	if !ok {
		return ErrNotFound
	}
	d.KeyID = keyID
	m.drafts[id] = d
	return nil
}

func (m *MemoryStore) Unclaim(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.get(tenantID, id)
	if !ok || d.Status != Claimed || d.KeyID != "" {
		return ErrState
	}
	d.Status, d.ClaimedAt = Confirmed, nil
	m.drafts[id] = d
	return nil
}

func (m *MemoryStore) Discard(_ context.Context, tenantID, id string) (D, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.get(tenantID, id)
	switch {
	case !ok:
		return D{}, ErrNotFound
	case d.Status == Draft:
		d.Status = Discarded
	case d.Revision && d.Status != Draft:
		// Its Cards are the agent's own, revised: going back is another
		// revision, never revoking them.
		return D{}, ErrState
	case d.Status == Confirmed || d.Status == Discarding:
		d.Status = Discarding
	default:
		return D{}, ErrState
	}
	was := m.drafts[id]
	m.drafts[id] = d
	return was, nil
}
