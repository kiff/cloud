package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/playstore"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/words"
)

// Control-plane mutations. Every mutation here:
//   1. flips tenant status via tenant.Store.SetStatus,
//   2. evicts the runtime cache via RuntimeResetter so the change
//      takes effect on the very next call (closing the cache gap that
//      made suspension unreliable), and
//   3. records an append-only operator-audit entry.
//
// Suspension is the platform's one emergency lever. The proposals gate
// returns 403 tenant_suspended for a suspended tenant; the runtime
// resolver already refuses any non-active tenant — the cache eviction
// here is what makes that immediate.

// opsActionRequest is the optional JSON body carrying the operator's
// reason for the action. Reason is free text, surfaced in the audit
// log; empty is allowed but discouraged.
type opsActionRequest struct {
	Reason string `json:"reason"`
}

func (h *OpsHandler) suspend(w http.ResponseWriter, r *http.Request) {
	h.setStatus(w, r, tenant.StatusSuspended, operatoraudit.ActionSuspend,
		func(t tenant.Tenant) error {
			if t.Status == tenant.StatusSuspended {
				return errOpsNoop
			}
			if t.IsErasing() {
				return errOpsConflict
			}
			return nil
		})
}

func (h *OpsHandler) unsuspend(w http.ResponseWriter, r *http.Request) {
	h.setStatus(w, r, tenant.StatusActive, operatoraudit.ActionUnsuspend,
		func(t tenant.Tenant) error {
			if t.Status == tenant.StatusActive {
				return errOpsNoop
			}
			// Only a suspended tenant can be unsuspended back to
			// active; never resurrect an erasing/provisioning tenant.
			if t.Status != tenant.StatusSuspended {
				return errOpsConflict
			}
			return nil
		})
}

// restore brings back an account whose deletion is pending: one made from
// the dashboard, a key or by email, within its grace window. Never one the
// tafs app made (deleted_via "app"): its chats, notes and connections are
// already erased, so a restore would bring back KIFF without them, against
// the app's "can't be undone". The keys stay revoked; the owner makes new
// ones. Its hard delete never runs once it's active again.
func (h *OpsHandler) restore(w http.ResponseWriter, r *http.Request) {
	h.setStatus(w, r, tenant.StatusActive, operatoraudit.ActionRestore,
		func(t tenant.Tenant) error {
			if t.Status == tenant.StatusActive {
				return errOpsNoop
			}
			if t.Status != tenant.StatusDeletedPending {
				return errOpsConflict
			}
			if via, _ := t.Metadata[tenant.MetadataKeyDeletedVia].(string); via == tenant.DeletedViaApp {
				return errOpsAppDeletion
			}
			if intent, _ := t.Metadata["delete_intent"].(string); intent != "" {
				return errOpsAppDeletion
			}
			ends, _ := t.Metadata[tenant.MetadataKeyGraceWindowEnds].(string)
			if at, err := time.Parse(time.RFC3339, ends); err != nil || !time.Now().Before(at) {
				return errOpsGraceOver
			}
			return nil
		})
}

var (
	errOpsNoop     = errors.New("already in target state")
	errOpsConflict = errors.New("tenant state does not allow this transition")
	// errOpsAppDeletion: the tafs app deleted it, and that can't be undone.
	errOpsAppDeletion = errors.New("deleted from the tafs app, which can't be undone: its chats and connections are already erased")
	// errOpsGraceOver: past its grace window, or with none recorded.
	errOpsGraceOver = errors.New("the grace window has ended; the deletion can't be undone")
)

// setStatus is the shared suspend/unsuspend body: load, precondition,
// flip status, evict cache, audit. Audit is written only after the
// status change succeeds, so the trail never claims an action that did
// not happen.
func (h *OpsHandler) setStatus(w http.ResponseWriter, r *http.Request, target tenant.Status, action string, precondition func(tenant.Tenant) error) {
	ctx := r.Context()
	id := r.PathValue("id")
	op := auth.MustIdentity(ctx).Subject

	t, err := h.tenants.Get(ctx, id)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if perr := precondition(t); perr != nil {
		if errors.Is(perr, errOpsNoop) {
			// Idempotent: already in the target state. Return 200
			// with the current status, no audit row (nothing changed).
			writeJSON(w, http.StatusOK, map[string]string{"id": t.ID, "status": string(t.Status), "note": "no change"})
			return
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": perr.Error(), "status": string(t.Status)})
		return
	}

	reason := readReason(r)

	// Atomic: the status change and its audit row commit together.
	// A failure leaves the tenant unchanged, so a retry re-applies and
	// audits rather than silently no-op'ing.
	entry := operatoraudit.Entry{
		OperatorSubject: op,
		Action:          action,
		TargetTenantID:  t.ID,
		Reason:          reason,
		Metadata:        map[string]any{"from": string(t.Status), "to": string(target)},
	}
	if err := h.mutator.SetTenantStatus(ctx, t.ID, t.Status, target, t.ProvisionedAt, entry); err != nil {
		if errors.Is(err, tenant.ErrStatusChanged) {
			// Moved on since it was read (a restore racing the hard-delete
			// sweep): nothing changed, nothing audited.
			writeJSON(w, http.StatusConflict, map[string]string{"error": "the account changed while this ran; read it again", "id": t.ID})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "mutation failed; tenant unchanged: " + err.Error(),
			"id":     t.ID,
			"status": string(t.Status),
		})
		return
	}
	// Evict the cached runtime AFTER the change commits so the status
	// takes effect on the next call. Without this a previously-active
	// tenant keeps serving from cache (the gap that made suspension
	// unreliable).
	if h.resetter != nil {
		h.resetter.ResetTenant(t.ID)
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"id":     t.ID,
		"status": string(target),
		"action": action,
	})

	// Customer notification: best-effort and detached, kicked
	// off only after the 200 is written. The state change + audit have
	// already committed; a notification failure never affects them.
	h.notifyOwner(t.ID, action)
}

// notifyOwner sends the customer suspend/unsuspend email best-effort.
// It resolves the owner email at send time (Clerk, never stored) and
// dispatches the send detached so it never blocks or rolls back the
// operator action. No-op when notifications are not wired.
func (h *OpsHandler) notifyOwner(tenantID, action string) {
	if h.ownerEmail == nil || h.notifier == nil {
		return
	}
	h.dispatch(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		email, err := h.ownerEmail.Email(ctx, tenantID)
		if err != nil {
			// No mapping / no Clerk email / lookup failed: skip
			// cleanly. Logged, not fatal. (No email address logged.)
			h.logger.InfoContext(ctx, "ops: customer notification skipped",
				slog.String("tenant", tenantID), slog.String("action", action),
				slog.String("reason", err.Error()))
			return
		}
		// The account's own language, as its people last used KIFF; English
		// when it isn't known or the account can't be read.
		lang := words.EN
		if t, err := h.tenants.Get(ctx, tenantID); err == nil {
			lang = personlang.Account(t)
		}
		var sendErr error
		switch action {
		case operatoraudit.ActionSuspend:
			sendErr = h.notifier.TenantSuspended(ctx, email, lang)
		case operatoraudit.ActionUnsuspend:
			sendErr = h.notifier.TenantUnsuspended(ctx, email, lang)
		default:
			return
		}
		if sendErr != nil {
			h.logger.ErrorContext(ctx, "ops: customer notification failed",
				slog.String("tenant", tenantID), slog.String("action", action),
				slog.String("err", sendErr.Error()))
			return
		}
		h.logger.InfoContext(ctx, "ops: customer notification sent",
			slog.String("tenant", tenantID), slog.String("action", action))
	})
}

// auditLog serves GET /v1/ops/audit — the read-only operator audit
// trail, newest first. ?tenant=<id> scopes to one tenant.
func (h *OpsHandler) auditLog(w http.ResponseWriter, r *http.Request) {
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant"))
	entries, err := h.audit.List(r.Context(), tenantID, 200)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// readReason extracts the optional reason from a JSON body. Tolerant:
// a missing/!json body yields an empty reason rather than an error.
func readReason(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	var body opsActionRequest
	_ = json.NewDecoder(r.Body).Decode(&body)
	return strings.TrimSpace(body.Reason)
}

// opsPlanRequest is the body of POST /v1/ops/tenants/{id}/plan.
type opsPlanRequest struct {
	Plan   string `json:"plan"`
	Reason string `json:"reason"`
}

// opsAssignablePlans is the set an operator may set from the console:
// the v1 plans. Legacy ids are never written.
var opsAssignablePlans = map[string]struct{}{
	string(tenant.PlanTrial):    {},
	string(tenant.PlanSolo):     {},
	string(tenant.PlanTeam):     {},
	string(tenant.PlanCompany):  {},
	string(tenant.PlanInternal): {},
}

// changePlan serves POST /v1/ops/tenants/{id}/plan: set the tenant's
// plan metadata, evict the runtime cache (limits change), and audit.
func (h *OpsHandler) changePlan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	op := auth.MustIdentity(ctx).Subject

	var body opsPlanRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if r.FormValue("plan") != "" { // tolerate form posts from the console
		body.Plan = r.FormValue("plan")
		body.Reason = r.FormValue("reason")
	}
	plan := strings.TrimSpace(body.Plan)
	if _, ok := opsAssignablePlans[plan]; !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown or non-assignable plan: " + plan})
		return
	}

	t, err := h.tenants.Get(ctx, id)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	from := string(tenant.PlanFor(t).Plan)
	if from == plan {
		writeJSON(w, http.StatusOK, map[string]string{"id": t.ID, "plan": plan, "note": "no change"})
		return
	}
	// Atomic plan change + audit.
	entry := operatoraudit.Entry{
		OperatorSubject: op, Action: operatoraudit.ActionPlanChange, TargetTenantID: t.ID,
		Reason: strings.TrimSpace(body.Reason), Metadata: map[string]any{"from": from, "to": plan},
	}
	if err := h.mutator.SetTenantPlan(ctx, t.ID, plan, entry); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "mutation failed; plan unchanged: " + err.Error(), "id": t.ID, "plan": from,
		})
		return
	}
	if h.resetter != nil {
		h.resetter.ResetTenant(t.ID) // limits live on the cached runtime manifest
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": t.ID, "plan": plan, "from": from})
}

// maxBonusCredits bounds one bonus, against a slip of the thumb.
const maxBonusCredits = 10000

// opsBonusRequest is the body of POST /v1/ops/tenants/{id}/bonus.
type opsBonusRequest struct {
	Credits int64  `json:"credits"`
	Reason  string `json:"reason"`
}

// grantBonus serves POST /v1/ops/tenants/{id}/bonus: give the account
// bonus credits, a pack of credits of its own that never expires, as the
// Terms say of bonus credits. With a reason, and never without its audit
// entry: if the entry can't be written, the bonus is taken back.
func (h *OpsHandler) grantBonus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	op := auth.MustIdentity(ctx).Subject

	var body opsBonusRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	body.Reason = strings.TrimSpace(body.Reason)
	switch {
	case body.Credits < 1 || body.Credits > maxBonusCredits:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "credits is 1 to 10000"})
		return
	case body.Reason == "":
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a reason is required: it goes in the audit log"})
		return
	}
	t, err := h.tenants.Get(ctx, id)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var b [12]byte
	_, _ = rand.Read(b[:])
	txn := "bonus_" + hex.EncodeToString(b[:])
	p, _, err := h.bonus.RecordCredits(ctx, packs.CreditGrant{TenantID: t.ID, Pack: "bonus", Credits: body.Credits, Kind: packs.KindBonus,
		Source: packs.SourceKIFF, SourceTxn: txn, At: h.now().UTC()})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the bonus could not be recorded: " + err.Error()})
		return
	}
	entry := operatoraudit.Entry{
		OperatorSubject: op, Action: operatoraudit.ActionBonusCredits, TargetTenantID: t.ID, Reason: body.Reason,
		Metadata: map[string]any{"credits": body.Credits, "pack": p.ID},
	}
	if err := h.audit.Record(ctx, entry); err != nil {
		if _, _, rerr := h.bonus.RevokeCredits(ctx, packs.SourceKIFF, txn, h.now().UTC()); rerr != nil {
			h.logger.ErrorContext(ctx, "ops: bonus kept without its audit entry", slog.String("tenant", t.ID), slog.String("pack", p.ID),
				slog.String("err", rerr.Error()))
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the audit entry failed; the bonus was taken back: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": t.ID, "pack": p.ID, "credits": p.Credits})
}

// opsStoreResetRequest is the body of POST /v1/ops/tenants/{id}/store-billing/reset.
// Confirm is the account's id, typed back: the reset can't be undone from the
// console.
type opsStoreResetRequest struct {
	Confirm string `json:"confirm"`
	Reason  string `json:"reason"`
}

// storeResetView is the account's billing as stored after a reset: the
// read-back the console shows.
type storeResetView struct {
	ID            string `json:"id"`
	Plan          string `json:"plan"`
	BillingSource string `json:"billing_source"`
	BillingStatus string `json:"billing_status"`
	// Apple and Google: whether a store subscription still bills the account
	// (false after a reset), and the one each reset set aside, whose later
	// notifications are ignored.
	AppleSubscription  bool   `json:"apple_subscription"`
	GoogleSubscription bool   `json:"google_subscription"`
	AppleReset         bool   `json:"apple_reset"`
	GoogleReset        bool   `json:"google_reset"`
	ResetAt            string `json:"reset_at"`
	From               string `json:"from,omitempty"`
}

// storeBilling is the App Store's and Google Play's keys, for a reset. Only
// their metadata keys are read, so no catalog is needed.
func storeBilling() []appstore.Store {
	return []appstore.Store{
		{Name: "apple", MetaSubscription: appstore.MetaOriginalTransaction, MetaExpiresAt: appstore.MetaExpiresAt},
		{Name: "google", MetaSubscription: playstore.MetaPurchaseToken, MetaExpiresAt: playstore.MetaExpiresAt},
	}
}

func storeResetOf(t tenant.Tenant) storeResetView {
	str := func(k string) string { s, _ := t.Metadata[k].(string); return s }
	v := storeResetView{ID: t.ID, Plan: string(tenant.PlanFor(t).Plan), BillingSource: string(tenant.BillingSourceOf(t)),
		BillingStatus: str(billing.MetaBillingStatus), ResetAt: str(appstore.MetaResetAt)}
	stores := storeBilling()
	v.AppleSubscription = str(stores[0].MetaSubscription) != ""
	v.GoogleSubscription = str(stores[1].MetaSubscription) != ""
	_, _, v.AppleReset = stores[0].WasReset(t)
	_, _, v.GoogleReset = stores[1].WasReset(t)
	return v
}

// resetStoreBilling serves POST /v1/ops/tenants/{id}/store-billing/reset: a
// review or test account goes back to no plan after a store purchase (App
// Review's account after a TestFlight Solo). As a subscription that ended: plan
// trial, its trial over, billing canceled, no billing source; and the App
// Store's and Google Play's subscriptions cleared, each kept aside so a later
// renewal or expiry the store sends for it is ignored (appstore WasReset). A
// purchase made in the app after the reset applies as a new one. An account
// that Stripe or a contract bills is refused: this is for store billing only.
// The account's id typed back and a reason are required; the change and its
// audit entry are written together, and the answer reads the account back.
func (h *OpsHandler) resetStoreBilling(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	op := auth.MustIdentity(ctx).Subject

	var body opsStoreResetRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	body.Confirm, body.Reason = strings.TrimSpace(body.Confirm), strings.TrimSpace(body.Reason)
	switch {
	case body.Confirm != id:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "confirm: type the account's id to reset its store billing"})
		return
	case body.Reason == "":
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a reason is required: it goes in the audit log"})
		return
	}
	t, err := h.tenants.Get(ctx, id)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	switch src := tenant.BillingSourceOf(t); src {
	case tenant.BillingStripe, tenant.BillingContract:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the account's plan is billed by " + string(src) + ", not a store: nothing was changed"})
		return
	}
	switch tenant.PlanFor(t).Canonical {
	case tenant.PlanTeam, tenant.PlanCompany, tenant.PlanInternal:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the account is on " + string(tenant.PlanFor(t).Plan) + ", which no store bills: nothing was changed"})
		return
	}
	before := storeResetOf(t)
	set := appstore.ResetBilling(t, h.now(), storeBilling()...)
	entry := operatoraudit.Entry{
		OperatorSubject: op, Action: operatoraudit.ActionStoreBillingReset, TargetTenantID: t.ID, Reason: body.Reason,
		Metadata: map[string]any{"from_plan": before.Plan, "from_billing_source": before.BillingSource,
			"apple_subscription": before.AppleSubscription, "google_subscription": before.GoogleSubscription},
	}
	if err := h.mutator.SetTenantMetadata(ctx, t.ID, set, entry); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mutation failed; nothing was reset: " + err.Error(), "id": t.ID})
		return
	}
	if h.resetter != nil {
		h.resetter.ResetTenant(t.ID) // the plan's limits live on the cached runtime manifest
	}
	after, err := h.tenants.Get(ctx, t.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reset, but the account couldn't be read back: " + err.Error(), "id": t.ID})
		return
	}
	v := storeResetOf(after)
	v.From = before.Plan
	writeJSON(w, http.StatusOK, v)
}
