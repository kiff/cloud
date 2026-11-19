package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

// NotificationPrefsHandler serves the tenant's per-rule notification
// preferences: which notification kinds surface in the dashboard bell +
// feed, and whether held actions are emailed (held_email):
//
//	GET /v1/me/notification-prefs → { "prefs": { "<rule>": <bool>, ... } }
//	PUT /v1/me/notification-prefs   { "prefs": { "<rule>": <bool>, ... } }
//
// Preferences are stored in the caller's OWN tenant metadata as a small
// JSON object (identity-scoped, exactly like the billing budget), so a
// caller can only read/write their own. Absent rules default to enabled,
// so a fresh tenant sees every notification until it opts out.
//
// This is intentionally a thin persistence surface: the notification
// FEED is composed in the web tier from live signals (approvals, runtime
// health); these prefs just gate which kinds it includes.
type NotificationPrefsHandler struct {
	tenants tenant.Store
	logger  *slog.Logger
}

// notifyPrefsMetaKey is the tenant-metadata key holding the prefs JSON.
const notifyPrefsMetaKey = "notify_prefs"

// knownNotifyRules is the set of rule keys the product implements today.
// The wire contract is the string keys; unknown keys on PUT are dropped
// so a forward-looking client cannot persist junk. Kept in sync with the
// web tier's notify.Rule* constants by the shared JSON keys.
var knownNotifyRules = []string{"approval", "runtime_idle", ruleHeldEmail}

// ruleHeldEmail turns off the email about each held action
// for the account: Needs you, the bell and phone alerts still show it.
// An account whose holds are routine (an eval account) mutes it here.
const ruleHeldEmail = "held_email"

// NewNotificationPrefsHandler wires the tenant store. Returns nil when
// the store is nil so the route is simply unmounted.
func NewNotificationPrefsHandler(tenants tenant.Store, logger *slog.Logger) *NotificationPrefsHandler {
	if tenants == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &NotificationPrefsHandler{tenants: tenants, logger: logger}
}

// Register mounts the GET/PUT routes. No-op on a nil handler.
func (h *NotificationPrefsHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("GET /v1/me/notification-prefs", h.get)
	mux.HandleFunc("PUT /v1/me/notification-prefs", h.put)
}

type notificationPrefsBody struct {
	Prefs map[string]bool `json:"prefs"`
}

func (h *NotificationPrefsHandler) tenantID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no tenant identity"})
		return "", false
	}
	return id.Tenant, true
}

func (h *NotificationPrefsHandler) get(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.tenantID(w, r)
	if !ok {
		return
	}
	t, err := h.tenants.Get(r.Context(), tid)
	if err != nil {
		h.notFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusOK, notificationPrefsBody{Prefs: notifyPrefsFromMeta(t)})
}

func (h *NotificationPrefsHandler) put(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.tenantID(w, r)
	if !ok {
		return
	}
	var body notificationPrefsBody
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
	}

	// Keep only known rules; seed defaults for any the caller omitted so
	// the stored object is always complete and self-describing.
	clean := map[string]bool{}
	for _, rule := range knownNotifyRules {
		if v, present := body.Prefs[rule]; present {
			clean[rule] = v
		} else {
			clean[rule] = true
		}
	}

	encoded, err := json.Marshal(clean)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "encode prefs failed"})
		return
	}
	if err := h.tenants.SetMetadata(r.Context(), tid, notifyPrefsMetaKey, string(encoded)); err != nil {
		h.notFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusOK, notificationPrefsBody{Prefs: clean})
}

func (h *NotificationPrefsHandler) notFoundOr500(w http.ResponseWriter, err error) {
	if errors.Is(err, tenant.ErrNotFound) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "tenant not found"})
		return
	}
	h.logger.Error("notification prefs: store error", slog.String("err", err.Error()))
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

// notifyPrefsFromMeta reads the tenant's stored prefs and returns a
// complete map seeded with defaults (absent rule = enabled), so callers
// never have to nil-check a rule.
func notifyPrefsFromMeta(t tenant.Tenant) map[string]bool {
	out := map[string]bool{}
	for _, rule := range knownNotifyRules {
		out[rule] = true
	}
	raw := metaString(t, notifyPrefsMetaKey)
	if raw == "" {
		return out
	}
	stored := map[string]bool{}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return out // corrupt value → safe defaults (all on)
	}
	for _, rule := range knownNotifyRules {
		if v, ok := stored[rule]; ok {
			out[rule] = v
		}
	}
	return out
}
