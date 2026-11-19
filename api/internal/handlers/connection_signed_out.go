package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
)

// A vendor ending the sign-in planque holds for a taf's connection (its
// refresh refused, or a token expired with no refresh) left the taf unable
// to use the tool, and no one was told until they opened it. planque now
// tells KIFF, once per sign-out, with the account's connector key, and KIFF
// sends its signed alert, connection_signed_out, to the account's targets
// that list it: tafs cloud then tells the people who see the taf.
//
//	POST /v1/me/connections/signed-out  {grant, agent, vendor, reason, signed_out_at}

// connectionSignOutReasons are planque's reasons, kept for the receiver's
// records; people read only the vendor and the taf.
var connectionSignOutReasons = []string{"refresh_refused", "no_refresh_token"}

// connectionSignOutEvery is how often one grant may alert: a vendor that
// flaps between refusing and accepting tells the owner once an hour at most.
const connectionSignOutEvery = time.Hour

// ConnectionsHandler serves planque's notices about its connections.
type ConnectionsHandler struct {
	alerts alert.Notifier
	now    func() time.Time

	mu   sync.Mutex
	last map[string]time.Time // tenant/grant → when it last alerted
}

// NewConnectionsHandler wires the handler; alerts may be nil (nothing is sent).
func NewConnectionsHandler(alerts alert.Notifier) *ConnectionsHandler {
	return &ConnectionsHandler{alerts: alerts, now: func() time.Time { return time.Now().UTC() }, last: map[string]time.Time{}}
}

// WithClock sets the handler's clock, for tests.
func (h *ConnectionsHandler) WithClock(now func() time.Time) *ConnectionsHandler {
	h.now = now
	return h
}

// Register attaches the route to the authenticated mux.
func (h *ConnectionsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/me/connections/signed-out", h.signedOut)
}

func (h *ConnectionsHandler) signedOut(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if !id.IsConnector() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connector_key_required",
			"detail": "planque's connector key says when a vendor signed a connection out"})
		return
	}
	var body struct {
		Grant       string `json:"grant"`
		Agent       string `json:"agent"`
		Vendor      string `json:"vendor"`
		Reason      string `json:"reason"`
		SignedOutAt string `json:"signed_out_at"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body is {grant, agent, vendor, reason, signed_out_at}"})
		return
	}
	refuse := func(msg string) { writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg}) }
	vendor, okVendor := cleanDeviceName(body.Vendor) // a name for people: short, no control characters
	at, errAt := time.Parse(time.RFC3339, strings.TrimSpace(body.SignedOutAt))
	now := h.now()
	switch {
	case !validGrantID(body.Grant):
		refuse("grant must be planque's grant id")
	case !validConnectionAgent(body.Agent):
		refuse("agent must be the taf's agent id")
	case !okVendor || vendor == "":
		refuse("vendor is the vendor's name for people, at most 64 characters")
	case !containsString(connectionSignOutReasons, body.Reason):
		refuse("reason is refresh_refused or no_refresh_token")
	case errAt != nil || at.After(now.Add(5*time.Minute)) || at.Before(now.Add(-7*24*time.Hour)):
		refuse("signed_out_at is RFC 3339, within the last 7 days")
	default:
		h.tell(id.Tenant, body.Grant, body.Agent, vendor, body.Reason, at.UTC(), now)
		w.WriteHeader(http.StatusAccepted)
	}
}

// tell sends the alert, at most once an hour per grant. A resend of the same
// sign-out (planque's sweep) carries the same event_id, so a receiver that
// got the first drops it.
func (h *ConnectionsHandler) tell(tenant, grant, agent, vendor, reason string, at, now time.Time) {
	if h.alerts == nil {
		return
	}
	key := tenant + "/" + grant
	h.mu.Lock()
	if last, ok := h.last[key]; ok && now.Sub(last) < connectionSignOutEvery {
		h.mu.Unlock()
		return
	}
	h.last[key] = now
	for k, t := range h.last { // keep the map small
		if now.Sub(t) >= connectionSignOutEvery {
			delete(h.last, k)
		}
	}
	h.mu.Unlock()
	h.alerts.Notify(alert.Event{TenantID: tenant, EntityID: grant, EntityType: "connection",
		Outcome: alert.ConnectionSignedOut, DecidedAt: at,
		EventID:    "conn-out:" + grant + ":" + at.Format(time.RFC3339),
		Connection: &alert.Connection{Grant: grant, Agent: agent, Vendor: vendor, Reason: reason, SignedOutAt: at}})
}

// validConnectionAgent is an agent id as KIFF keeps them: short, printable,
// never a trial holder ("~").
func validConnectionAgent(a string) bool {
	if a == "" || len(a) > 200 || strings.Contains(a, "~") {
		return false
	}
	for _, c := range a {
		if c <= 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}
