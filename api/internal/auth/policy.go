package auth

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/kiff/cloud/api/internal/membership"
)

// RoutePolicy is the membership capability table applied at the API's edge
// for people on an account. Each rule maps a route pattern to the
// capability it needs. A read (GET, HEAD) not named needs CapRead; any
// other request not named needs the admin role, so a route added later
// is closed to non-admins until someone decides who may use it.
//
// Handlers keep their own checks (an agent's key, the hold gate, step-up);
// the policy is the floor that makes "the UI may hide controls; the API
// independently refuses" true for every route at once. Account keys are
// not policed here: they carry their own roles and their handlers check
// them. Runtime keys reach only RuntimeKeyRules (DefaultRoutePolicy).
type RoutePolicy struct {
	mux *http.ServeMux
	// keys, when set, is the runtime-key allowlist (RuntimeKeyRules).
	keys *http.ServeMux
	// readOnly is the read-only account key's allowlist
	// (ReadOnlyKeyRules). Unset, that key reaches nothing.
	readOnly *http.ServeMux
	// phones, when set, is the allowlist for a phone's session
	// (PhoneSessionRules); signed, the routes it reaches only with the
	// phone's signature (PhoneSignedRules).
	phones *http.ServeMux
	signed *http.ServeMux
	// approval, the signed routes that grant authority
	// (PhoneApprovalRules): signed by the approval key, once the session
	// has one.
	approval *http.ServeMux
	// granted, the writes a phone's session makes once each with a live
	// device grant (DeviceGrantRoutes).
	granted *http.ServeMux
	// window, the approval routes that are routine (PhoneWindowRules):
	// inside an open approval window, the device key signs them.
	window *http.ServeMux
	// made records the signed setup calls already made (key and signed
	// message), until they could no longer verify, so none is replayed.
	// In memory unless WithSignedRequests gives it a shared, durable record.
	made SignedRequests
	// windows keeps the approval windows (kiff/tafs#494). Nil: no window
	// is ever open, so every approval route needs the approval key.
	windows WindowStore
	now     func() time.Time
}

// PhoneApprovalRules are the signed calls that grant authority
// (kiff/tafs#233): issuing or raising a Card, submitting or confirming a
// draft (a phone's draft is confirmed by submitting it, its trial too),
// and resuming. Once a session has an approval key, whose own access
// control requires Face ID or the passcode, these count only with its
// signature, so code that skips the prompt can't grant anything. Every
// other signed call keeps the session's device key: what reads or takes
// authority away may run with no one at the screen.
var PhoneApprovalRules = []string{
	// "No" to "Did this happen?" gives a Card its room back.
	"POST /v1/me/did-this-happen/{proposal}",
	"POST /v1/me/mandates",
	"POST /v1/me/mandates/{id}/extra-room",
	"POST /v1/me/agent-drafts",
	"POST /v1/me/agent-drafts/{id}/confirm",
	"POST /v1/agents/{id}/resume",
	"POST /v1/resume",
	// Sharing an agent gives people its use.
	"POST /v1/agents/{id}/grants",
	// Opening an approval window is the fingerprint the window stands for.
	"POST /v1/me/window",
}

// PhoneWindowRules are the approval routes that are routine
// (kiff/tafs#494): inside an open approval window, one the phone's
// approval key opened behind its fingerprint within the last minutes, the
// device key signs them, with the window's id in the signed message. Each
// is routine only by its content, which its handler checks with
// SignedWithFrom and refuses with critical_key_required otherwise: "No" to
// "Did this happen?" gives a Card its room back, and issuing a Card is
// routine for a new one and for a reissue that widens nothing, as is a
// draft's confirm: a revision is routine only if every term is the same or
// narrower than the Card in force (mandate.Widens). A draft's submit,
// which can confirm in one step, stays critical.
// The approval routes not listed here (extra room, resume, sharing) are
// critical: the approval key, every time, and a window never counts.
var PhoneWindowRules = []string{
	"POST /v1/me/did-this-happen/{proposal}",
	"POST /v1/me/mandates",
	"POST /v1/me/agent-drafts/{id}/confirm",
}

// PhoneSignedRules are setup calls a phone's session makes only with its
// key's signature over the request (KIFF-Device-Signature):
// starting a taf creates its agent, issues its Cards and mints its
// gateway key. Each one is the owner's act behind Face ID, so a stolen
// token alone makes none of them. A key minted this way is always bound
// to an agent (KeysHandler).
var PhoneSignedRules = []string{
	// The owner's answer to "Did this happen?", behind the device's unlock.
	"POST /v1/me/did-this-happen/{proposal}",
	// "Not sure": asking for help checking it decides nothing, so the
	// device key alone takes it (kiff/tafs#498).
	"POST /v1/me/did-this-happen/{proposal}/check",
	"POST /v1/me/agents",
	"POST /v1/me/mandates",
	"POST /v1/keys",
	// Revoking an agent's key, when its taf is removed or a run's fresh
	// agent is done. The handler refuses an account or connector
	// key from a phone.
	"DELETE /v1/keys/{id}",
	// A draft a phone submits is confirmed by submitting it.
	"POST /v1/me/agent-drafts",
	"POST /v1/me/agent-drafts/{id}/confirm",
	// Going back from trying a version (kiff/tafs#173): its trial Cards are
	// revoked. Starting one is a confirm, above.
	"DELETE /v1/me/agent-drafts/{id}/trial",
	// Held-call alerts to the phone's app backend, and turning them off.
	// The handler limits which URLs and outcomes.
	"POST /v1/me/alert-targets",
	"DELETE /v1/me/alert-targets/{id}",
	// Extra room on a Card. A replay can't add it twice: the
	// revision id makes a second grant a no-op, and expected_base_limit
	// refuses one made against a stale view of the Card.
	"POST /v1/me/mandates/{id}/extra-room",
	// The account's time zone, the day a Card's "a day" follows
	// (kiff/tafs#96): an account setting, so the owner's act behind the
	// device's unlock. The route keeps its account.settings rule.
	"PUT /v1/me/time-zone",
	// Resuming a halted agent, or the account, gives
	// back its authority: behind Face ID. Halting only takes it away, so it
	// is one tap, unsigned (PhoneSessionRules).
	"POST /v1/agents/{id}/resume",
	"POST /v1/resume",
	// Deleting the account, from the app (kiff/tafs#185, App Store Review
	// Guideline 5.1.1(v)). The signature over this request, behind Face
	// ID, is the handler's recent sign-in.
	"POST /v1/me/delete",
	// Groups and sharing, administered from the tafs app: each
	// change is the person's act behind Face ID. The handlers check who
	// may make it.
	"POST /v1/groups",
	"PATCH /v1/groups/{id}",
	"DELETE /v1/groups/{id}",
	"PUT /v1/groups/{id}/members/{subject}",
	"DELETE /v1/groups/{id}/members/{subject}",
	"POST /v1/agents/{id}/grants",
	"DELETE /v1/agents/{id}/grants",
	// The session's approval key (kiff/tafs#233), once, for a session that
	// signed in without one: signed by the device key, as signing in is.
	// A second is refused, so code holding the device key alone can't
	// swap in a key of its own; replacing it means signing in again.
	"POST /v1/me/device/approval-key",
	// Re-linking it after the phone's faces or fingerprints changed: signed by
	// the device key, with the new key's own proof. It only asks; the person
	// approves the new key in their browser, so the phone alone can't swap it.
	"POST /v1/me/device/relink",
	// Opening an approval window (kiff/tafs#494): the approval key signs
	// it, behind the fingerprint, so the handler refuses a device-signed
	// opening. Closing it takes nothing: the device key.
	"POST /v1/me/window",
	"POST /v1/me/window/{id}/close",
	// Going on past the AI fair-use bound lets the tafs spend more.
	"PUT /v1/me/usage/ai",
	// Raising the spend limit lets the tafs spend more: the signature
	// covers the body, which names the limit it raises from.
	"PUT /v1/me/usage/limit",
	// Declining a member's request for a pack is a billing admin's answer,
	// behind Face ID like the other billing routes. Approving is buying the
	// pack: Apple's signed transaction is its own proof.
	"POST /v1/me/pack-requests/{id}/decline",
	// Archiving an agent the phone made, once it's done: the Eval's
	// cleanup after each run, and a taf removed for good (kiff/agents#78).
	// It only takes authority away, but the phone's rules stay tight, so
	// it's signed: a stolen token alone archives nothing. The route keeps
	// its cards.manage rule.
	"POST /v1/me/agents/{id}/archive",
}

// maxSignedBody bounds a signed setup call's body, read to verify it.
const maxSignedBody = 1 << 20

// PhoneSessionRules are the only routes a developer session bound to a
// phone's key reaches. Such a session answers held calls only
// with the key's signature; if it could reach anything else, a stolen
// token could mint a credential without that requirement (a keyless
// device session, an API key, an approver grant) and approve with it.
// So it reads its Cards and held calls, answers with
// a signature, revokes a Card, and manages its own sign-in. A route not
// named here is refused, including routes added later.
//
// change-card is an answer: its handler requires the phone's
// kiff.answer.v1 signature, as for approve-once and reject. A held call's
// proposal_id is its receipt's trace_id, so the phone reads the receipt
// of a call it answered; and it lists the drafts waiting for someone who
// may confirm them.
var PhoneSessionRules = []string{
	// The money calls KIFF can't prove, for "Did this happen?".
	"GET /v1/me/did-this-happen",
	// KIFF's words for a connection's profile, which the phone renders and
	// checks before the owner signs the approval (connection profiles: the owner's approval). It reads
	// nothing of the account and changes nothing.
	"POST /v1/profiles/words",
	// Accepting the account's deletion: the body carries
	// the intent signed by the approval key, behind the screen lock, which
	// the handler verifies; nothing is erased until it finishes.
	"POST /v1/account-deletions",
	"GET /v1/me",
	// Groups and who each agent is shared with, and the person's own
	// access to each agent: the tafs app shows what KIFF says.
	"GET /v1/groups",
	"GET /v1/agents/{id}/grants",
	"GET /v1/me/access",
	// Reading another person's work, from the app: recorded by KIFF before
	// it grants the read, unsigned as it grants nothing to act. The person
	// whose work it is reads who read it; an admin, the account's reads.
	"POST /v1/oversight/reads",
	"GET /v1/me/oversight-reads",
	"GET /v1/oversight/reads",
	// The account's people, so the app can add one to a group or share
	// an agent with them. Read only: inviting or changing anyone stays on
	// the dashboard.
	"GET /v1/tenant/members",
	// A person's run grant for an agent they may use: no signature, as it
	// adds nothing beyond that use, which KIFF checks, and the app asks
	// for one with every message.
	"POST /v1/me/run-grants",
	"GET /v1/me/mandates",
	"GET /v1/me/mandates/{id}/statement",
	"POST /v1/me/mandates/{id}/revoke",
	"GET /v1/me/card-exceptions",
	"GET /v1/me/card-exceptions/{id}",
	"POST /v1/me/card-exceptions/{id}/approve-once",
	"POST /v1/me/card-exceptions/{id}/reject",
	"POST /v1/me/card-exceptions/{id}/change-card",
	"GET /v1/receipts/{trace_id}",
	// Whether this phone signed a request another KIFF service received
	// (kiff/tafs#206): the session forwarded with the signature, never a
	// signature on its own.
	"POST /v1/me/device-signature",
	// The account's record of every call (kiff/tafs#102): what a taf did,
	// for the app's Activity and Record. Read only.
	"GET /v1/me/activity",
	// The account's two meters, counts only.
	"GET /v1/me/usage",
	"GET /v1/me/usage/tasks",
	// The credits its tafs' runs used, and those runs (read only).
	"GET /v1/me/usage/credits",
	"GET /v1/me/usage/credits/runs",
	// A purchase made in the app through Apple: the transaction is Apple's
	// signed JWS, carrying the account's appAccountToken, so it needs no
	// signature of the phone's. The handler refuses another account's.
	"POST /v1/me/billing/apple/transactions",
	// The same through Google Play: the purchase token is read back from
	// Play, and must carry the account's app_account_token.
	"POST /v1/me/billing/google/purchases",
	// A member asks an admin for a pack, and may withdraw it; neither
	// spends anything. Declining is signed (PhoneSignedRules).
	"GET /v1/me/pack-requests",
	"POST /v1/me/pack-requests",
	"POST /v1/me/pack-requests/{id}/withdraw",
	"GET /v1/me/devices",
	"DELETE /v1/me/devices/{id}",
	// The open approval window, with KIFF's words for the next tap (kiff/tafs#494).
	"GET /v1/me/window",
	"GET /v1/me/agent-drafts",
	"GET /v1/me/agent-drafts/{id}",
	"DELETE /v1/me/agent-drafts/{id}",
	"GET /v1/me/agent-drafts/{id}/trial",
	"DELETE /v1/me/agent-drafts/{id}/trial",
	// A draft that only waits (kiff/tafs#107): going back to an earlier
	// version's Card terms, or drafting a stale one again. Submitting either
	// confirms nothing; the owner confirms with the phone's signature.
	"POST /v1/me/agent-drafts/restore",
	// How a draft would read, written nowhere (batch connections): tafs cloud
	// shows the phone KIFF's lines for the draft the phone then signs.
	"POST /v1/me/agent-drafts/preview",
	"POST /v1/me/agent-drafts/{id}/redraft",
	"GET /v1/me/alert-targets",
	// The owner's halt: halting is one tap; resuming
	// is signed (PhoneSignedRules); reading shows halting or halted.
	"POST /v1/agents/{id}/halt",
	"POST /v1/halt",
	"POST /v1/agents/{id}/resume",
	"POST /v1/resume",
	"GET /v1/agents/{id}/halt",
	"GET /v1/halt",
	"POST /v1/auth/device/logout",
}

// KeyScope says which runtime keys may use a route in RuntimeKeyRules.
type KeyScope int

const (
	// AnyRuntimeKey: every runtime key, bound to an agent or not.
	AnyRuntimeKey KeyScope = iota + 1
	// AgentKeyOnly: a key bound to an agent; the handler scopes what it
	// reads to that agent.
	AgentKeyOnly
	// UnboundKeyOnly: a runtime key not bound to an agent, such as the
	// key a KIFF Guard runtime connects with.
	UnboundKeyOnly
)

type keyScopeHandler struct{ s KeyScope }

func (keyScopeHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

// RuntimeKeyRules are the only routes a runtime key reaches. A
// runtime key is any API key without the account roles: an agent's key,
// a gateway key, or a role-less key minted for a runtime. It acts or
// asks; it does not read or change the rest of the account. Before this
// list, keys skipped the route policy entirely, and handlers that only
// checked the tenant served every agent the account's activity (the
// decision stream, evidence, receipts, proof packs, usage), its
// settings (alert targets, billing budget, notification preferences)
// and its people's things (support tickets, wallet), and let it change
// some of them. A route not named here is refused to runtime keys,
// including routes added later. Account keys and people are unaffected.
// ReadOnlyKeyRules are the routes a read-only account key reaches, all
// reads (GET, and HEAD with it). Deny by default: a route not named
// here is refused to it, a GET added later included, even one the
// table would count as CapRead. Adding a route is a decision about what
// the key may read.
var ReadOnlyKeyRules = []string{
	"GET /v1/me",
	"GET /v1/me/activity",
	"GET /v1/me/card-exceptions",
	"GET /v1/me/card-exceptions/{id}",
	"GET /v1/me/mandates",
	"GET /v1/me/mandates/{id}/statement",
	"GET /v1/me/stream",
}

var RuntimeKeyRules = map[string]KeyScope{
	// Who am I, and my own Card.
	"GET /v1/me":       AnyRuntimeKey,
	"GET /v1/me/card":  AgentKeyOnly,
	"GET /v1/me/holds": AgentKeyOnly, // only the agent's own held calls
	// An agent withdraws one of its own held calls when its run is
	// cancelled; the handler checks the call is the agent's.
	"POST /v1/me/holds/{id}/withdraw": AgentKeyOnly,
	// A taf's runtime, or Setup's, reports each of its runs that ended
	// having used AI, once, for its credits. The handler keeps it only for
	// the key's own agent, a taf or Setup, and keeps a run's first report.
	"PUT /v1/me/runs/{run}/usage": AgentKeyOnly,
	// The same runtime asks, before each new run, whether one may start:
	// the balance, for the key's own agent, a taf or Setup.
	"GET /v1/me/credits/allowance":       AgentKeyOnly,
	"GET /v1/me/mandates":                AgentKeyOnly, // only Cards the agent holds
	"GET /v1/me/mandates/{id}/statement": AgentKeyOnly,
	// This only consumes a durable trusted outcome for the caller's own
	// operation. The handler never accepts an agent's effect assertion.
	"POST /v1/me/card-effects/{proposal}/reconcile": AgentKeyOnly,
	"POST /v1/me/card-effects/{proposal}/surcharge": AgentKeyOnly,

	// Acting: decisions, events, and the entities a runtime works on.
	"POST /v1/proposals/decide": AnyRuntimeKey,
	// What a person may do with an agent: the runtime that runs
	// it asks before a person's run, and at each turn, with the agent's own
	// key. The handler refuses another agent's.
	"POST /v1/agents/{id}/access": AgentKeyOnly,
	// The same, for the key's own agent, without naming it.
	"POST /v1/me/access": AgentKeyOnly,
	// A run's grant, resolved by the agent's runtime with the agent's own
	// key; the handler refuses another agent's grant.
	"POST /v1/me/run-grants/resolve": AgentKeyOnly,
	// A read grant, resolved the same way before the runtime serves another
	// person's work to the reader KIFF recorded.
	"POST /v1/me/read-grants/resolve": AgentKeyOnly,
	// A trial's call through the gateway (kiff/tafs#107): decided for the
	// key's own agent, and nothing recorded.
	"POST /v1/gateway/trial": AgentKeyOnly,
	// A draft grants nothing until an owner confirms it, so
	// an agent or runtime may submit one; it cannot confirm it.
	"POST /v1/me/agent-drafts": AnyRuntimeKey,
	// An agent reads its own agent's drafts, to see one confirmed
	// (kiff/tafs#107); the handler refuses anyone else's.
	"GET /v1/me/agent-drafts/{id}": AgentKeyOnly,
	// A trial of its own draft (kiff/tafs#173): the agent's runtime reads
	// it, and ends it when idle, which only takes authority away. The
	// handler refuses another agent's draft.
	"GET /v1/me/agent-drafts/{id}/trial":    AgentKeyOnly,
	"DELETE /v1/me/agent-drafts/{id}/trial": AgentKeyOnly,
	"POST /v1/events/raw":                   AnyRuntimeKey,
	"GET /v1/entities/":                     AnyRuntimeKey,
	"POST /v1/entities/":                    AnyRuntimeKey,
	"PUT /v1/entities/":                     AnyRuntimeKey,
	"DELETE /v1/entities/":                  AnyRuntimeKey,

	// KIFF Guard: a runtime connects, reports what it observed, and in
	// observe mode saves a draft domain. Activating a draft stays a
	// person's or an account key's act.
	"POST /v1/guard/connect":      UnboundKeyOnly,
	"POST /v1/guard/observations": UnboundKeyOnly,
	"PUT /v1/me/domain/draft":     UnboundKeyOnly,
}

// runtimeKeyAllowed reports whether runtime key id may use r.
func (p *RoutePolicy) runtimeKeyAllowed(r *http.Request, id Identity) bool {
	h, pattern := p.keys.Handler(r)
	ks, ok := h.(keyScopeHandler)
	if pattern == "" || !ok {
		return false
	}
	switch ks.s {
	case AnyRuntimeKey:
		return true
	case AgentKeyOnly:
		return id.AgentID != ""
	case UnboundKeyOnly:
		return id.AgentID == ""
	}
	return false
}

// isRuntimeKey reports whether id is an API key without the account
// roles. People and account keys are not, and neither is a connector key
// (the auth middleware already keeps it to its own routes) or a
// read-only account key (Wrap keeps it to reads).
func (id Identity) isRuntimeKey() bool {
	return id.APIKey && id.MemberRole == "" && !id.ManagesAccount() && !id.IsConnector() && !id.ReadOnlyAccountKey()
}

type capHandler struct{ c membership.Capability }

func (capHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

// NewRoutePolicy builds a policy from pattern → capability rules. The
// patterns use http.ServeMux syntax and precedence.
func NewRoutePolicy(rules map[string]membership.Capability) *RoutePolicy {
	mux := http.NewServeMux()
	for pattern, c := range rules {
		mux.Handle(pattern, capHandler{c})
	}
	return &RoutePolicy{mux: mux, made: NewMemorySignedRequests()}
}

// WithSignedRequests keeps the record of signed setup calls in made, so a
// replay is refused after a restart and by every API process.
func (p *RoutePolicy) WithSignedRequests(made SignedRequests) *RoutePolicy {
	p.made = made
	return p
}

// WithClock reads the time from now, for tests of the approval window.
func (p *RoutePolicy) WithClock(now func() time.Time) *RoutePolicy {
	p.now = now
	return p
}

// WithWindows keeps the approval windows in windows (kiff/tafs#494), so a
// routine approval route accepts the device key inside an open one.
func (p *RoutePolicy) WithWindows(windows WindowStore) *RoutePolicy {
	p.windows = windows
	return p
}

// Required returns the capability a request needs, and false when no
// rule names it and it is not a read (admin only).
func (p *RoutePolicy) Required(r *http.Request) (membership.Capability, bool) {
	if h, pattern := p.mux.Handler(r); pattern != "" {
		if ch, ok := h.(capHandler); ok {
			return ch.c, true
		}
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return membership.CapRead, true
	}
	return "", false
}

// Wrap refuses a person's request their role does not cover. It must
// run inside the auth middleware, which puts the identity on the context.
func (p *RoutePolicy) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := IdentityFrom(r.Context())
		if err == nil && len(id.DeviceKey) > 0 {
			with, signed, ok := p.phoneAllowed(w, r, id)
			if !ok {
				return
			}
			if signed {
				r = r.WithContext(withDeviceSigned(r.Context(), with))
			}
		}
		if err == nil && id.ReadOnlyAccountKey() {
			// A read-only account key makes the GET and HEAD requests
			// ReadOnlyKeyRules names, and nothing else (deny by default).
			// The method matters: the table names some POSTs CapRead
			// (asking for access, drafting an agent), and this key makes
			// none. The route must still need CapRead, so a listed read
			// the table later names another capability for is refused.
			read := r.Method == http.MethodGet || r.Method == http.MethodHead
			listed := false
			if p.readOnly != nil {
				_, pattern := p.readOnly.Handler(r)
				listed = pattern != ""
			}
			if c, named := p.Required(r); !read || !listed || !named || c != membership.CapRead {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error":   "read_only_key",
					"message": "this key only reads the account; it changes nothing",
				})
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if err == nil && p.keys != nil && id.isRuntimeKey() {
			if !p.runtimeKeyAllowed(r, id) {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error":   "runtime_key_forbidden",
					"message": "this key acts for an agent or a runtime; this route is for people and account keys",
				})
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if err != nil || id.MemberRole == "" {
			next.ServeHTTP(w, r)
			return
		}
		c, named := p.Required(r)
		allowed := id.MemberRole == membership.RoleAdmin
		if named {
			allowed = membership.Can(id.MemberRole, c)
		}
		if !allowed {
			body := map[string]string{
				"error":   "role_forbidden",
				"role":    string(id.MemberRole),
				"message": "your role on this account (" + string(id.MemberRole) + ") does not allow this",
			}
			if named {
				body["capability"] = string(c)
			}
			writeJSON(w, http.StatusForbidden, body)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// DefaultRoutePolicy is the table for the API's routes. Every write
// route the API registers is named here; a test in the API's server
// keeps it that way.
func DefaultRoutePolicy() *RoutePolicy {
	p := NewRoutePolicy(DefaultRules)
	p.keys = http.NewServeMux()
	for pattern, s := range RuntimeKeyRules {
		p.keys.Handle(pattern, keyScopeHandler{s})
	}
	p.readOnly = http.NewServeMux()
	for _, pattern := range ReadOnlyKeyRules {
		p.readOnly.Handle(pattern, http.NotFoundHandler())
	}
	p.phones = http.NewServeMux()
	for _, pattern := range PhoneSessionRules {
		p.phones.Handle(pattern, http.NotFoundHandler())
	}
	p.signed = http.NewServeMux()
	for _, pattern := range PhoneSignedRules {
		p.signed.Handle(pattern, http.NotFoundHandler())
	}
	p.approval = http.NewServeMux()
	for _, pattern := range PhoneApprovalRules {
		p.approval.Handle(pattern, http.NotFoundHandler())
	}
	p.window = http.NewServeMux()
	for _, pattern := range PhoneWindowRules {
		p.window.Handle(pattern, http.NotFoundHandler())
	}
	p.granted = http.NewServeMux()
	for _, pattern := range DeviceGrantRoutes {
		p.granted.Handle(pattern, http.NotFoundHandler())
	}
	return p
}

// DefaultRules is DefaultRoutePolicy's table.
var DefaultRules = map[string]membership.Capability{
	"PUT /v1/me/mandates/{id}/warnings":       membership.CapGovernance,
	"GET /v1/me/approver-grants":              membership.CapKeysManage,
	"POST /v1/me/approver-grants":             membership.CapKeysManage,
	"POST /v1/me/approver-grants/{id}/revoke": membership.CapKeysManage,
	// This public route authenticates only the dedicated scoped token.
	"POST /v1/approver/holds/{id}/answer": membership.CapHoldsAnswer,
	// Keys: minting one is creating a new principal (admin).
	"POST /v1/keys":        membership.CapKeysManage,
	"PATCH /v1/keys/{id}":  membership.CapKeysManage,
	"DELETE /v1/keys/{id}": membership.CapKeysManage,

	// A person's own things.
	"POST /v1/auth/device/approve":           membership.CapPersonal,
	"POST /v1/auth/device/logout":            membership.CapPersonal,
	"POST /v1/me/device/approval-key":        membership.CapPersonal,
	"POST /v1/me/device/relink":              membership.CapPersonal,
	"POST /v1/me/window":                     membership.CapPersonal,
	"POST /v1/me/window/{id}/close":          membership.CapPersonal,
	"DELETE /v1/me/devices/{id}":             membership.CapPersonal,
	"POST /v1/me/wallet/nonce":               membership.CapPersonal,
	"POST /v1/me/wallet/verify":              membership.CapPersonal,
	"DELETE /v1/me/wallet":                   membership.CapPersonal,
	"POST /v1/me/attention/seen":             membership.CapPersonal,
	"POST /v1/me/support/tickets":            membership.CapPersonal,
	"POST /v1/me/support/tickets/{id}/reply": membership.CapPersonal,

	// Acting from a person's own session.
	"POST /v1/proposals/decide": membership.CapActionsPropose,
	// Its handler answers only an agent's key (kiff/tafs#107).
	"POST /v1/gateway/trial":              membership.CapActionsPropose,
	"POST /v1/events/raw":                 membership.CapActionsPropose,
	"POST /v1/entities/":                  membership.CapActionsPropose,
	"PUT /v1/entities/":                   membership.CapActionsPropose,
	"DELETE /v1/entities/":                membership.CapActionsPropose,
	"POST /v1/approvals/":                 membership.CapActionsPropose,
	"POST /v1/me/attention/{id}/{action}": membership.CapHoldsAnswer, // claim, release, resolve a work item

	// Holds. change-card is a Card edit reached from a hold.
	"POST /v1/approvals/{id}/grant":                 membership.CapHoldsAnswer,
	"POST /v1/approvals/{id}/deny":                  membership.CapHoldsAnswer,
	"POST /v1/me/card-exceptions/{id}/approve-once": membership.CapHoldsAnswer,
	"POST /v1/me/card-exceptions/{id}/reject":       membership.CapHoldsAnswer,
	"POST /v1/me/card-exceptions/{id}/change-card":  membership.CapCardsManage,

	// Cards and the agents that hold them.
	"POST /v1/me/mandates":                          membership.CapCardsManage,
	"POST /v1/me/mandates/{id}/extra-room":          membership.CapCardsManage,
	"POST /v1/me/card-effects/{proposal}/reconcile": membership.CapCardsManage,
	"POST /v1/me/card-effects/{proposal}/surcharge": membership.CapCardsManage,
	// "Did this happen?" (connection profiles): whoever answers held calls answers it.
	"GET /v1/me/did-this-happen":                   membership.CapHoldsAnswer,
	"POST /v1/me/did-this-happen/{proposal}":       membership.CapHoldsAnswer,
	"POST /v1/me/did-this-happen/{proposal}/check": membership.CapHoldsAnswer,
	"POST /v1/me/mandates/{id}/archive":            membership.CapCardsManage,
	"POST /v1/me/mandates/{id}/move":               membership.CapCardsManage,
	"POST /v1/me/mandates/{id}/revoke":             membership.CapCardsManage,
	"PUT /v1/me/mandates/{id}/holders":             membership.CapCardsManage,
	"PUT /v1/me/mandates/{id}/title":               membership.CapCardsManage,
	"POST /v1/me/agents":                           membership.CapCardsManage,
	// Groups and sharing. A group's managers change its
	// members too, so those routes check in the handler.
	"POST /v1/groups":                          membership.CapMembersManage,
	"PATCH /v1/groups/{id}":                    membership.CapMembersManage,
	"DELETE /v1/groups/{id}":                   membership.CapMembersManage,
	"PUT /v1/groups/{id}/members/{subject}":    membership.CapPersonal,
	"DELETE /v1/groups/{id}/members/{subject}": membership.CapPersonal,
	"POST /v1/agents/{id}/grants":              membership.CapCardsManage,
	"DELETE /v1/agents/{id}/grants":            membership.CapCardsManage,
	// The permission answer: a person asks about themselves (the handler
	// checks), a runtime about whoever its run is for.
	"POST /v1/agents/{id}/access":    membership.CapRead,
	"POST /v1/me/access":             membership.CapRead,
	"POST /v1/me/run-grants":         membership.CapRead,
	"POST /v1/me/run-grants/resolve": membership.CapRead,
	// Reading another person's work: the handler checks the reader
	// oversees the agent, and records the read before granting it.
	"POST /v1/oversight/reads":        membership.CapRead,
	"POST /v1/me/read-grants/resolve": membership.CapRead,
	"GET /v1/me/oversight-reads":      membership.CapRead,
	"GET /v1/oversight/reads":         membership.CapMembersManage,
	"POST /v1/me/agents/{id}/archive": membership.CapCardsManage,

	// Domains.
	"PUT /v1/me/domain":                 membership.CapDomainsManage,
	"PUT /v1/me/domain/draft":           membership.CapDomainsManage,
	"DELETE /v1/me/domain/draft":        membership.CapDomainsManage,
	"POST /v1/me/domain/draft/activate": membership.CapDomainsManage,
	"POST /v1/me/domain/template":       membership.CapDomainsManage,
	"POST /v1/me/domain/validate":       membership.CapDomainsManage,
	"POST /v1/me/domains":               membership.CapDomainsManage,
	"PUT /v1/me/domains/{domain}":       membership.CapDomainsManage,
	"PUT /v1/me/domains/{domain}/mode":  membership.CapDomainsManage,
	"DELETE /v1/me/domains/{domain}":    membership.CapDomainsManage,
	// Where each action's execution permit goes. Set with the
	// gateway contract, by the same people.
	"PUT /v1/me/permit-targets": membership.CapDomainsManage,
	// Planque's connector key checks a person's device signature.
	// The handler serves only a connector key; a person is refused there.
	"POST /v1/devices/verify": membership.CapToolsConnect,
	// Another KIFF service asks whether the caller's phone signed a request
	// it received: the gateway, connecting the account's control server
	// (kiff/tafs#206), which only someone who may connect tools does.
	"POST /v1/me/device-signature": membership.CapToolsConnect,
	// Setup's read Card, on the owner's approval of a connection (kiff/tafs#138).
	"POST /v1/me/setup-reads": membership.CapToolsConnect,
	// planque's connector key says a vendor signed a connection out; the
	// handler serves only a connector key.
	"POST /v1/me/connections/signed-out": membership.CapToolsConnect,
	// Connection profiles: anyone on the account may read KIFF's
	// words for a profile; only a connector key, for someone who may
	// connect tools, checks the owner's approval of one.
	"POST /v1/profiles/words":       membership.CapRead,
	"POST /v1/me/profile-approvals": membership.CapToolsConnect,

	// Tools a runtime connects.
	"POST /v1/guard/connect": membership.CapToolsConnect,
	// Connecting an agent is for admins and editors; anyone
	// signed in may decline a request.
	"POST /v1/oauth/consent": membership.CapToolsConnect,
	"POST /v1/oauth/deny":    membership.CapPersonal,
	// Connect links: admins and editors.
	"POST /v1/me/agents/{id}/connect-links":    membership.CapToolsConnect,
	"POST /v1/oauth/connect-links/{id}/revoke": membership.CapToolsConnect,
	// Device flow and Connections.
	"POST /v1/oauth/device/approve":     membership.CapToolsConnect,
	"POST /v1/oauth/device/deny":        membership.CapPersonal,
	"POST /v1/oauth/grants/{id}/revoke": membership.CapToolsConnect,
	"POST /v1/guard/observations":       membership.CapToolsConnect,

	// Account-wide settings, billing, export, deletion.
	"PUT /v1/me/account-kind":      membership.CapAccountSettings,
	"PUT /v1/me/usage/ceiling":     membership.CapBilling,
	"PUT /v1/me/usage/ai":          membership.CapBilling,
	"PUT /v1/me/usage/limit":       membership.CapBilling,
	"POST /v1/me/billing/checkout": membership.CapBilling,
	// Packs: bought by the billing admin; asked for by anyone on the
	// account, who alone may withdraw their request (the handler checks).
	"POST /v1/me/billing/apple/transactions":       membership.CapBilling,
	"POST /v1/me/billing/google/purchases":         membership.CapBilling,
	"POST /v1/me/pack-requests":                    membership.CapPersonal,
	"POST /v1/me/pack-requests/{id}/withdraw":      membership.CapPersonal,
	"POST /v1/me/pack-requests/{id}/decline":       membership.CapBilling,
	"PUT /v1/me/time-zone":                         membership.CapAccountSettings,
	"PUT /v1/me/account-name":                      membership.CapAccountSettings,
	"PUT /v1/me/chat-approvals":                    membership.CapAccountSettings,
	"PUT /v1/me/notification-prefs":                membership.CapAccountSettings,
	"POST /v1/me/alert-targets":                    membership.CapAccountSettings,
	"DELETE /v1/me/alert-targets/{id}":             membership.CapAccountSettings,
	"POST /v1/me/alert-targets/{id}/rotate-secret": membership.CapAccountSettings,
	"POST /v1/me/alert-targets/{id}/enable":        membership.CapAccountSettings,
	// A person submitting a draft confirms it when they may; one who may
	// not leaves it for someone who can.
	"POST /v1/me/agent-drafts": membership.CapRead,
	// Drafts that only wait (kiff/tafs#107); confirming them is cards.manage.
	"POST /v1/me/agent-drafts/restore":      membership.CapRead,
	"POST /v1/me/agent-drafts/preview":      membership.CapRead,
	"POST /v1/me/agent-drafts/{id}/redraft": membership.CapRead,
	// Agent keys only (RuntimeKeyRules); a person's session is refused by
	// the handler, which needs a key bound to an agent.
	"POST /v1/me/holds/{id}/withdraw":       membership.CapRead,
	"PUT /v1/me/runs/{run}/usage":           membership.CapRead,
	"GET /v1/me/credits/allowance":          membership.CapRead,
	"POST /v1/me/agent-drafts/{id}/confirm": membership.CapCardsManage,
	// The owner's halt and resume.
	"POST /v1/agents/{id}/halt":             membership.CapCardsManage,
	"POST /v1/halt":                         membership.CapCardsManage,
	"POST /v1/agents/{id}/resume":           membership.CapCardsManage,
	"POST /v1/resume":                       membership.CapCardsManage,
	"DELETE /v1/me/agent-drafts/{id}":       membership.CapCardsManage,
	"DELETE /v1/me/agent-drafts/{id}/trial": membership.CapCardsManage,
	// This public route authenticates only the draft's claim secret.
	"POST /v1/agent-drafts/{id}/claim": membership.CapRead,
	"POST /v1/me/billing-portal":       membership.CapBilling,
	"PUT /v1/me/billing-budget":        membership.CapBilling,
	"GET /v1/me/export":                membership.CapExport,
	"POST /v1/me/delete":               membership.CapDelete,
	"POST /v1/account-deletions":       membership.CapDelete,
	// This public route authenticates only the deletion's signed intent,
	// which KIFF accepted from the phone (account deletion). It is registered
	// outside the session mux and the handler reads no identity: this entry
	// only names it for the route checks, it authorizes nothing.
	"POST /v1/account-deletions/finish": membership.CapRead,
	"POST /v1/account-deletions/status": membership.CapRead,

	// People and policy.
	"POST /v1/tenant/invites":                      membership.CapMembersManage,
	"POST /v1/tenant/invites/{id}/revoke":          membership.CapMembersManage,
	"PUT /v1/tenant/members/{subject}/role":        membership.CapMembersManage,
	"POST /v1/tenant/members/{subject}/suspend":    membership.CapMembersManage,
	"POST /v1/tenant/members/{subject}/reactivate": membership.CapMembersManage,
	"POST /v1/tenant/members/{subject}/remove":     membership.CapMembersManage,
	"POST /v1/tenant/leave":                        membership.CapPersonal,
	"PUT /v1/tenant/governance":                    membership.CapGovernance,
	"GET /v1/tenant/audit":                         membership.CapMembersManage,

	// The operator console checks its own allow-list per request (and the
	// web's RequireOperator checks it before that); the policy lets the
	// request reach that check on purpose. Neither replaces the other.
	"POST /v1/ops/": membership.CapPersonal,
}

// phoneMux is the phone allowlist; a policy built without one refuses
// every route to a phone's session.
func (p *RoutePolicy) phoneMux() *http.ServeMux {
	if p.phones == nil {
		return http.NewServeMux()
	}
	return p.phones
}

// phoneAllowed applies a phone's session's limits: its own routes,
// and the setup calls only with its key's signature. Anything else is
// refused. It writes the refusal and reports false; signed says the
// request is a setup call whose signature it verified.
func (p *RoutePolicy) phoneAllowed(w http.ResponseWriter, r *http.Request, id Identity) (with SignedWith, signed, ok bool) {
	if p.signed != nil {
		if _, pattern := p.signed.Handler(r); pattern != "" {
			with, ok := p.verifySigned(w, r, id, p.grantsAuthority(r, id))
			return with, true, ok
		}
	}
	// A write another KIFF service makes for the phone, with the grant its
	// signed request earned (kiff/tafs#206).
	if p.granted != nil && r.Header.Get(DeviceGrantHeader) != "" {
		if _, pattern := p.granted.Handler(r); pattern != "" {
			return SignedWith{}, false, p.useGrant(w, r, id, pattern)
		}
	}
	if _, pattern := p.phoneMux().Handler(r); pattern == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":   "phone_session_forbidden",
			"message": "a phone's session reads Cards and held calls and answers with its key's signature; do this in KIFF",
		})
		return SignedWith{}, false, false
	}
	return SignedWith{}, false, true
}

// routine reports whether the request's route is routine (PhoneWindowRules):
// inside an open approval window, the device key signs it.
func (p *RoutePolicy) routine(r *http.Request) bool {
	if p.window == nil {
		return false
	}
	_, pattern := p.window.Handler(r)
	return pattern != ""
}

// grantsAuthority reports whether the request must be signed by the
// session's approval key: a call that grants authority
// (PhoneApprovalRules), from a session that has one (kiff/tafs#233).
func (p *RoutePolicy) grantsAuthority(r *http.Request, id Identity) bool {
	if p.approval == nil || len(id.ApprovalKey) == 0 {
		return false
	}
	_, pattern := p.approval.Handler(r)
	return pattern != ""
}

// verifySigned checks a setup call's KIFF-Device-Signature over the
// tenant, method, path and query, and body, and refuses a signature it
// has already accepted. The body is read and put back for the handler.
// With approval, the signature must be the approval key's, unless the
// route is routine and the phone signed inside an open approval window
// (kiff/tafs#494): then the device key's, with the window's id in the
// signed message, and the window restarts. It returns which key signed.
func (p *RoutePolicy) verifySigned(w http.ResponseWriter, r *http.Request, id Identity, approval bool) (SignedWith, bool) {
	var body []byte
	if r.Body != nil {
		b, err := io.ReadAll(io.LimitReader(r.Body, maxSignedBody+1))
		if err != nil || len(b) > maxSignedBody {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request too large to sign"})
			return SignedWith{}, false
		}
		body = b
		r.Body = io.NopCloser(bytes.NewReader(b))
	}
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	header := r.Header.Get(DeviceSignatureHeader)
	key, with := id.DeviceKey, SignedWith{Key: KeyDevice}
	if approval {
		key, with = id.ApprovalKey, SignedWith{Key: KeyCritical}
	}
	// Signed inside a window, on a routine route: the device key, and the
	// window must be open for this phone. On a critical route the window
	// counts for nothing: the approval key must have signed.
	var window ApprovalWindow
	inWindow := approval && signatureWindowNamed(header) && p.routine(r)
	if inWindow {
		if p.windows == nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "critical_key_required",
				"message": "no approval window is open here: sign it with the phone's approval key, behind its fingerprint"})
			return SignedWith{}, false
		}
		key, with = id.DeviceKey, SignedWith{Key: KeyDevice}
	}
	replayID, named, err := VerifyRequestWindow(key, id.Tenant, r.Method, r.URL.RequestURI(), body, header, now)
	switch {
	case errors.Is(err, ErrAnswerSignedAt):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "signed_at_out_of_range",
			"message": "the signature's time must be RFC 3339, within 5 minutes of KIFF's clock"})
		return SignedWith{}, false
	case err != nil && approval && named != "" || err != nil && approval && signatureWindowNamed(header):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "critical_key_required",
			"message": "this widens what a taf may do, or gives authority back: an approval window doesn't cover it. Sign it with the phone's approval key, behind its fingerprint"})
		return SignedWith{}, false
	case err != nil && approval:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "approval_key_required",
			"message": "this grants authority: sign it with the phone's approval key, behind Face ID"})
		return SignedWith{}, false
	case err != nil:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "signature_required",
			"message": "from a phone, this needs " + DeviceSignatureHeader + " over the request: kiff.request.v1, tenant, method, path, sha256 of the body, signed_at"})
		return SignedWith{}, false
	}
	if inWindow {
		open, err := p.windows.OpenFor(r.Context(), id.DeviceID, now)
		if err != nil && !errors.Is(err, ErrWindowNotFound) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "signature_check_unavailable",
				"message": "KIFF couldn't read the approval window; sign it again and retry"})
			return SignedWith{}, false
		}
		// The window counts only while the key that opened it is the session's
		// approval key: a new enrolment and re-link end it.
		if err != nil || open.ID != named || open.Tenant != id.Tenant || open.Subject != id.Subject || open.OpenedBy != DeviceKeyFingerprint(id.ApprovalKey) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "window_required",
				"message": "the approval window this was signed in isn't open: open one with the phone's approval key, behind its fingerprint, and sign it again"})
			return SignedWith{}, false
		}
		window, with.Window = open, open.ID
	}
	// The same request, signed by the same key, is used once, however its
	// signature is encoded. Its signed_at is within MaxAnswerSkew of now, so
	// it can't verify after now + 2*MaxAnswerSkew.
	fresh, err := p.made.Use(r.Context(), replayID, now, now.Add(2*MaxAnswerSkew))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "signature_check_unavailable",
			"message": "KIFF couldn't check this signed request; sign it again and retry"})
		return SignedWith{}, false
	}
	if !fresh {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "signature_replayed",
			"message": "this signed request was already made; sign it again to repeat it"})
		return SignedWith{}, false
	}
	// Each approval inside the window restarts it, never past its cap. Best
	// effort: a restart that fails leaves the window as it was.
	if inWindow {
		_ = p.windows.Restart(r.Context(), window.ID, window.Restarted(now))
	}
	return with, true
}

// signatureWindowNamed reports whether a DeviceSignatureHeader names a
// window (a v2 signature).
func signatureWindowNamed(header string) bool { return SignatureWindow(header) != "" }
