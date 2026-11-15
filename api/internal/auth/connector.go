package auth

import (
	"net/http"
	"slices"
	"strings"
	"time"
)

// RoleConnector is the only role a connector key holds. A
// connector key connects tools to the gateway and nothing else: planque
// keeps one per account to register the tools an owner connects from the
// tafs app, so no account-wide key leaves the owner's hands. It is never
// bound to an agent, and it is minted only alone, by an admin's session or
// a phone's signed setup call.
const RoleConnector = "connector"

// ConnectorUnusedTTL is how long a connector key may wait for its first
// use. A connector key is minted to be handed to planque at once, in the
// same enrolment, and a device can't revoke one. So a key planque never
// took (its enrolment failed after the mint, or the app stopped between
// the two) would otherwise stay live in KIFF, held by no one. One that
// hasn't authenticated by then stops authenticating, as an expired key
// does. Its first use (planque asking who it is, at enrolment) stamps
// LastUsedAt, after which it lives as any other connector key.
const ConnectorUnusedTTL = 15 * time.Minute

// UnusedConnectorExpired reports whether r is a connector key that was
// never used and is past ConnectorUnusedTTL at now.
func (r APIKeyRecord) UnusedConnectorExpired(now time.Time) bool {
	return slices.Contains(r.Roles, RoleConnector) && r.LastUsedAt == nil && !now.Before(r.CreatedAt.Add(ConnectorUnusedTTL))
}

// connectorRoutes are the only routes a connector key reaches: who it is,
// the two writes the gateway makes with the caller's key when a
// connection changes (the gateway contract and its permit targets), the
// check of a person's device signature, the owner's halt, and Setup's read
// Card on the owner's approval. The handlers keep those writes to the
// gateway's own domain.
var connectorRoutes = []string{
	"GET /v1/me",
	"POST /v1/me/domains",
	"PUT /v1/me/permit-targets",
	// planque asks whether a person's device signed an approval
	// (kiff/planque#10), instead of keeping devices of its own.
	"POST /v1/devices/verify",
	// planque reads the owner's halt before every send.
	"GET /v1/halt",
	// planque asks for Setup's read Card, with the owner's signed approval
	// of the connection (kiff/tafs#138).
	"POST /v1/me/setup-reads",
	// planque says a vendor signed a connection out, so the owner hears of
	// it (connection_signed_out).
	"POST /v1/me/connections/signed-out",
	// planque reads KIFF's words for a profile, and the gateway checks the
	// owner's approval of one with planque's key (connection profiles: the owner's approval).
	"POST /v1/profiles/words",
	"POST /v1/me/profile-approvals",
}

// ConnectorRouteAllowed reports whether r may be served to a connector key.
func ConnectorRouteAllowed(r *http.Request) bool {
	path := strings.TrimRight(r.URL.Path, "/")
	if r.Method == http.MethodPut {
		if name, ok := strings.CutPrefix(path, "/v1/me/domains/"); ok && name != "" && !strings.Contains(name, "/") {
			return true
		}
	}
	if r.Method == http.MethodGet {
		if rest, ok := strings.CutPrefix(path, "/v1/agents/"); ok {
			if agent, ok := strings.CutSuffix(rest, "/halt"); ok && agent != "" && !strings.Contains(agent, "/") {
				return true
			}
		}
	}
	return slices.Contains(connectorRoutes, r.Method+" "+path)
}

// IsConnector reports whether id is a connector key. Whatever other roles
// a record holds, a key with this role acts only as a connector.
func (id Identity) IsConnector() bool {
	return id.APIKey && slices.Contains(id.Roles, RoleConnector)
}

func connectorForbidden(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "connector_key_forbidden",
		"message": "a connector key connects tools to the gateway and does nothing else"})
}
