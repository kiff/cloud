// Package posture records and reads a tenant's governance
// posture-change history (gap G5).
//
// `mode` (observe/enforce) is current-state tenant metadata
// (tenant.MetadataKeyMode); the *moment* a tenant's gate flipped
// observe↔enforce is not captured by that field alone. The Control
// Room narrative — "observe as the honest trial, then the flip to
// enforce" — needs the transition itself: from, to, who, when.
//
// As resolved in the design's review, posture changes are
// recorded as a new tamper-evident `kiff_audit` kind on the same
// per-tenant trail as the decisions they govern, rather than a
// separate cloud_meta table. "Easier to query" is the weaker choice
// when the product *is* tamper-evident evidence.
//
// Scope note: posture is tenant-wide today (one gate posture per
// tenant), so a transition applies to every control the tenant runs.
// The record carries the tenant as its entity; if per-control posture
// ever lands, the record can carry control_id without a schema change.
package posture

import (
	"sort"
	"time"

	"github.com/kiff/cloud/api/internal/tenant"
)

const (
	// Kind is the kiff_audit record kind for a posture change.
	Kind = "posture_changed"

	// EntityType labels the posture record's entity. The entity id is
	// the tenant id (the gate whose posture changed).
	EntityType = "governance_posture"

	// dataFrom / dataTo / dataChangedBy are the data-map keys carried
	// on the audit record. Centralized so the writer and reader agree.
	dataFrom      = "from"
	dataTo        = "to"
	dataChangedBy = "changed_by"
)

// Transition is one recorded observe↔enforce change: the posture it
// moved from and to, who made the change, and when.
type Transition struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	ChangedBy string    `json:"changed_by,omitempty"`
	ChangedAt time.Time `json:"changed_at"`
}

// History is a tenant's posture-change timeline plus the current
// effective mode (resolved by the caller from tenant metadata, since
// the current mode is authoritative there and the transitions are the
// evidence of how it got there).
type History struct {
	// Current is the effective mode now (enforce|observe).
	Current string `json:"current"`
	// Transitions is the recorded change history, oldest first.
	Transitions []Transition `json:"transitions"`
}

// LastEnforce returns the most recent transition *into* enforce, for
// the "became enforce on <date> by <whom>" line. ok is false when the
// tenant has never recorded a flip into enforce (e.g. always-enforce,
// or only ever observe).
func (h History) LastEnforce() (Transition, bool) {
	for i := len(h.Transitions) - 1; i >= 0; i-- {
		if h.Transitions[i].To == string(tenant.ModeEnforce) {
			return h.Transitions[i], true
		}
	}
	return Transition{}, false
}

// LastChange returns the most recent transition of any kind, or false
// when there are none.
func (h History) LastChange() (Transition, bool) {
	if len(h.Transitions) == 0 {
		return Transition{}, false
	}
	return h.Transitions[len(h.Transitions)-1], true
}

// ModeAt returns the posture in force at time t given a chronological
// (oldest-first) slice of transitions. This is the primitive the
// evidence timeline uses to classify a decision as observe-period vs
// enforce-period: the posture in force when the decision was made is
// the `to` of the latest transition at or before the decision's time.
//
// Before the first recorded transition the posture is unknown from the
// history alone, so ModeAt returns dflt (the caller passes the tenant's
// current mode, or enforce, as the safe default). This keeps observe-era
// decisions that predate posture recording from being silently
// mislabeled as a definite posture.
func ModeAt(transitions []Transition, t time.Time, dflt string) string {
	mode := dflt
	found := false
	// transitions are oldest-first; walk forward taking every change
	// that happened at or before t.
	for _, tr := range transitions {
		if tr.ChangedAt.After(t) {
			break
		}
		mode = tr.To
		found = true
	}
	if !found {
		return dflt
	}
	return mode
}

// sortTransitions orders transitions oldest-first by ChangedAt, then
// stably. Callers that read from an unordered source normalize through
// this so LastEnforce / ModeAt can assume chronological order.
func sortTransitions(ts []Transition) {
	sort.SliceStable(ts, func(i, j int) bool {
		return ts[i].ChangedAt.Before(ts[j].ChangedAt)
	})
}
