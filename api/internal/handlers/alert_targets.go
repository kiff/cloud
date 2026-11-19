package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
)

// AlertTargetsHandler is the minimal management surface for outbound
// alert targets:
//
//	POST   /v1/me/alert-targets         register a webhook target
//	GET    /v1/me/alert-targets         list the tenant's targets
//	DELETE /v1/me/alert-targets/{id}    remove a target
//
// Everything is tenant-scoped from the authenticated Identity; a
// caller can never read or write another tenant's targets.
//
// A phone's session registers, lists and removes only targets at
// phoneURLs, for held calls only; see phoneTarget.
type AlertTargetsHandler struct {
	store        alert.Store
	allowPrivate bool
	phoneURLs    []string
	now          func() time.Time
}

// NewAlertTargetsHandler wires the target store. allowPrivate (the
// env-gated dev escape hatch) permits registering private/loopback
// webhook URLs; production passes false so the SSRF guard
// rejects them at registration as well as at delivery.
func NewAlertTargetsHandler(store alert.Store, allowPrivate bool) *AlertTargetsHandler {
	return &AlertTargetsHandler{store: store, allowPrivate: allowPrivate, now: func() time.Time { return time.Now().UTC() }}
}

// WithPhoneURLs sets the app backends a phone's session may send its
// account's held calls to (KIFF_PHONE_ALERT_URLS): an app such as tafs
// registers its own backend from the phone, and nothing else. With none,
// a phone registers no target.
func (h *AlertTargetsHandler) WithPhoneURLs(urls []string) *AlertTargetsHandler {
	h.phoneURLs = urls
	return h
}

// phoneOutcomes are the outcomes a phone's target receives: a call held
// for its owner, and that call answered. These are the held calls a
// phone answers (card-exceptions); nothing else about the account's
// decisions leaves KIFF through a target a phone set up. And a member's
// request for a pack of tasks, and its answer, which the app's admins
// answer. And a connection a vendor signed out, which the owner signs in
// to again.
var phoneOutcomes = []string{"owner_required", alert.HoldAnswered, alert.PackRequested, alert.PackRequestAnswered, alert.ConnectionSignedOut, alert.RefundNotice,
	// A step a taf handed its owner closed: the app backend hands it to the
	// taf's runtime, which continues the run waiting on it.
	alert.HandoffAnswered}

// Register attaches the routes.
func (h *AlertTargetsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/me/alert-targets", h.create)
	mux.HandleFunc("GET /v1/me/alert-targets", h.list)
	mux.HandleFunc("DELETE /v1/me/alert-targets/{id}", h.delete)
	mux.HandleFunc("POST /v1/me/alert-targets/{id}/rotate-secret", h.rotateSecret)
	mux.HandleFunc("POST /v1/me/alert-targets/{id}/enable", h.enable)
}

// knownOutcomes is the set a target may subscribe to. Only blocked,
// approval_required, owner_required and card_warning actually fire today (the
// decide path alerts on consequential outcomes, the warning worker after commit);
// the others are accepted
// so a forward-looking config doesn't error, and documented as inert.
var knownOutcomes = map[string]bool{
	"approval_required": true,
	"owner_required":    true,
	"blocked":           true,
	"allowed":           true,
	"invalid":           true,
	"limit_exceeded":    true,
	"card_warning":      true,
	// Sent when a held call is answered, only to targets that
	// list it.
	alert.HoldAnswered: true,
	// A member's request for a pack, and its answer; only to targets
	// that list them.
	alert.PackRequested:       true,
	alert.PackRequestAnswered: true,
	// A connection a vendor signed out; only to targets that list it.
	alert.ConnectionSignedOut: true,
	// A refund that failed at the processor, or a charge also refunded
	// outside KIFF; only to targets that list it.
	alert.RefundNotice: true,
	// A step a taf handed to its owner closed ("Your step"): for the
	// agent's runtime, to continue the run waiting on it; only to targets
	// that list it.
	alert.HandoffAnswered: true,
}

type createAlertTargetRequest struct {
	URL      string   `json:"url"`
	Action   string   `json:"action,omitempty"`
	Outcomes []string `json:"outcomes,omitempty"`
	Enabled  *bool    `json:"enabled,omitempty"`
}

func (h *AlertTargetsHandler) create(w http.ResponseWriter, r *http.Request) {
	id, ok := h.tenant(w, r)
	if !ok {
		return
	}

	var req createAlertTargetRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body is not valid JSON"})
			return
		}
	}

	phone := len(id.DeviceKey) > 0
	if phone {
		status, body := h.phoneTarget(&req)
		if status != 0 {
			writeJSON(w, status, body)
			return
		}
	}
	target, errMsg := h.normalizeTarget(r.Context(), id.Tenant, req)
	if errMsg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
		return
	}
	target.Secret = alert.NewSigningSecret()
	save := h.store.CreateTarget
	if phone {
		// One target per app backend: setting it up again (a new phone,
		// a lost secret) replaces the one before, so the backend never
		// gets the same hold twice under two secrets. Concurrent setups
		// leave one target.
		save = h.store.ReplaceTarget
	}
	if err := save(r.Context(), target); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, withSigningSecret(target))
}

// phoneTarget limits a target a phone's session registers: the
// URL must be one of phoneURLs, and it receives held calls only. Without
// the limit, a phone's key could send the account's holds anywhere. It
// fills in the outcomes when none are named, and returns a refusal (a
// status and body) when the request asks for more.
func (h *AlertTargetsHandler) phoneTarget(req *createAlertTargetRequest) (int, map[string]string) {
	if !slices.Contains(h.phoneURLs, strings.TrimSpace(req.URL)) {
		return http.StatusForbidden, map[string]string{"error": "phone_alert_url_forbidden",
			"message": "from a phone, alerts go only to an app backend KIFF lists; add other targets in KIFF"}
	}
	if req.Action != "" {
		return http.StatusBadRequest, map[string]string{"error": "from a phone, a target covers every action"}
	}
	if req.Enabled != nil && !*req.Enabled {
		return http.StatusBadRequest, map[string]string{"error": "from a phone, a target is registered on; delete it to stop alerts"}
	}
	if len(req.Outcomes) == 0 {
		req.Outcomes = phoneOutcomes
	}
	for _, o := range req.Outcomes {
		if !slices.Contains(phoneOutcomes, o) {
			return http.StatusBadRequest, map[string]string{"error": "from a phone, a target receives only owner_required, hold_answered, pack_requested, pack_request_answered, connection_signed_out, refund_notice and handoff_answered"}
		}
	}
	return 0, nil
}

// phoneOwns reports whether a phone's session may see or remove t: only
// targets at an app backend a phone may register.
func (h *AlertTargetsHandler) phoneOwns(t alert.Target) bool {
	return slices.Contains(h.phoneURLs, t.URL)
}

// withSigningSecret is a target with its signing secret, the one time it
// is shown: when the target is made or its secret rotated.
func withSigningSecret(t alert.Target) map[string]any {
	return map[string]any{"id": t.ID, "tenant_id": t.TenantID, "action": t.Action, "url": t.URL,
		"outcomes": t.Outcomes, "enabled": t.Enabled, "created_at": t.CreatedAt, "signing_secret": t.Secret}
}

// rotateSecret gives a target a new signing secret and shows it once.
// Deliveries are signed with it from then on; a target made before
// signing starts being signed here.
func (h *AlertTargetsHandler) rotateSecret(w http.ResponseWriter, r *http.Request) {
	id, ok := h.tenant(w, r)
	if !ok {
		return
	}
	secret := alert.NewSigningSecret()
	found, err := h.store.RotateSecret(r.Context(), id.Tenant, strings.TrimSpace(r.PathValue("id")), secret)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "rotate failed"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such alert target"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "signing_secret": secret})
}

func (h *AlertTargetsHandler) list(w http.ResponseWriter, r *http.Request) {
	id, ok := h.tenant(w, r)
	if !ok {
		return
	}
	targets, err := h.store.ListTargets(r.Context(), id.Tenant)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(id.DeviceKey) > 0 {
		// A phone sees only its app's targets, not where else the
		// account's alerts go.
		targets = slices.DeleteFunc(targets, func(t alert.Target) bool { return !h.phoneOwns(t) })
	}
	if targets == nil {
		targets = []alert.Target{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": targets})
}

func (h *AlertTargetsHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := h.tenant(w, r)
	if !ok {
		return
	}
	targetID := strings.TrimSpace(r.PathValue("id"))
	if targetID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target id is required"})
		return
	}
	if len(id.DeviceKey) > 0 {
		// A phone removes only its app's targets. Like the delete below,
		// another target answers 204 and is left alone.
		targets, err := h.store.ListTargets(r.Context(), id.Tenant)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if i := slices.IndexFunc(targets, func(t alert.Target) bool { return t.ID == targetID }); i < 0 || !h.phoneOwns(targets[i]) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	// Silently idempotent: a delete for a missing id, or an id that
	// belongs to another tenant, matches no row (the store keys on
	// tenant_id + id) and still returns 204. This is deliberate — it
	// avoids leaking a cross-tenant existence oracle, and a delete
	// that "already happened" is the desired end state regardless.
	if err := h.store.DeleteTarget(r.Context(), id.Tenant, targetID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// normalizeTarget validates the request and builds a Target. Returns
// a non-empty error message when the request is invalid.
//
// URL validation (scheme/host + the SSRF range check) is delegated to
// alert.ValidateTargetURL so registration rejects private/metadata
// addresses the same way delivery does. The hard guard is at
// delivery (the dialer Control hook), which also catches DNS
// rebinding; this is the fast-feedback layer.
func (h *AlertTargetsHandler) normalizeTarget(ctx context.Context, tenantID string, req createAlertTargetRequest) (alert.Target, string) {
	raw := strings.TrimSpace(req.URL)
	if raw == "" {
		return alert.Target{}, "url is required"
	}
	if err := alert.ValidateTargetURL(ctx, raw, h.allowPrivate); err != nil {
		return alert.Target{}, err.Error()
	}

	for _, o := range req.Outcomes {
		if !knownOutcomes[o] {
			return alert.Target{}, "unknown outcome: " + o
		}
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	return alert.Target{
		ID:        newAlertTargetID(),
		TenantID:  tenantID,
		Action:    strings.TrimSpace(req.Action),
		URL:       raw,
		Outcomes:  req.Outcomes,
		Enabled:   enabled,
		CreatedAt: h.now(),
	}, ""
}

// tenant resolves the authenticated tenant or writes a 401 and
// returns ok=false.
func (h *AlertTargetsHandler) tenant(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return auth.Identity{}, false
	}
	if id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return auth.Identity{}, false
	}
	return id, true
}

// newAlertTargetID returns an opaque "alrt_<hex>" id.
func newAlertTargetID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "alrt_" + hex.EncodeToString(b[:])
}

// enable switches a target back on, after KIFF switched it off because its
// deliveries kept failing, or after its owner did. Deliveries
// resume with the next event; the ones given up on are not resent.
func (h *AlertTargetsHandler) enable(w http.ResponseWriter, r *http.Request) {
	id, ok := h.tenant(w, r)
	if !ok {
		return
	}
	found, err := h.store.SetEnabled(r.Context(), id.Tenant, strings.TrimSpace(r.PathValue("id")), true, "", h.now())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enable failed"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such alert target"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "enabled": true})
}
