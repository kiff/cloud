package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

// Setup's reads go through KIFF (kiff/tafs#138, gate A). Setup reads a
// vendor's read-only tool to set a taf up (the account list, an operation's
// fields), and each read is a call like any other: KIFF decides it on a Card,
// issues a permit, keeps a receipt and a line in the Record, and planque
// sends nothing without that permit.
//
// Setup's Card for those reads is issued here, by planque's connector key,
// only on the owner's approval of the connection: planque passes the request
// the owner's device signed (planque.request.v1, or v2 inside an approval window; POST …/grants/{grant}/approve),
// and KIFF checks the signature itself, as /v1/devices/verify does. The
// connector key gains exactly this: a Card that only reads, only the tools it
// connected and marked read-only, for the agent that asked, a few reads a day.
//
//	POST /v1/me/setup-reads  {agent, grant, tools: [names], approval: {device_id, message, signature}}

// SetupReadsPerDay is how many reads a day Setup's Card for one connection
// allows, across its tools; past that a read is refused.
const SetupReadsPerDay = 50

// setupReadsApprovalAge is how long after the owner signed the connection's
// approval its Card may be issued: the vendor's sign-in comes in between.
const setupReadsApprovalAge = 24 * time.Hour

// SetupReadTools is what KIFF knows of the account's gateway tools: which
// only read, and which key connected each.
type SetupReadTools interface {
	GatewayToolOwners
	ReadOnlyTools(ctx context.Context, tenantID string) (map[string]bool, error)
}

// setupReads holds what the route needs; nil leaves it unregistered.
type setupReads struct {
	approvals planqueApproval
	tools     SetupReadTools
	keys      auth.APIKeyStore
	// checks are KIFF's records of a connection's approval it verified
	// inside an open window at the approve (auth.ApprovalCheck).
	checks auth.ApprovalCheckStore
}

// WithMandatesSetupReads serves POST /v1/me/setup-reads (kiff/tafs#138):
// Setup's Card for its reads, issued on the owner's approval, which devices
// and members check, of tools that tools says only read, for an agent whose
// live key in keys carries setup_reader.
func WithMandatesSetupReads(devices auth.DeviceStore, members auth.Memberships, tools SetupReadTools, keys auth.APIKeyStore) MandatesOption {
	return func(h *MandatesHandler) {
		if devices != nil && members != nil && tools != nil && keys != nil {
			h.setupReads = &setupReads{approvals: planqueApproval{devices: devices, members: members}, tools: tools, keys: keys}
		}
	}
}

// WithMandatesApprovalWindows lets the owner's approval behind Setup's read
// Card count when it was signed inside an approval window (kiff/tafs#494):
// sessions find the key that opened it, windows whether it's open. Given
// after WithMandatesSetupReads; without it, only a v1 approval counts.
func WithMandatesApprovalWindows(sessions auth.DeveloperSessionStore, windows auth.WindowStore) MandatesOption {
	return func(h *MandatesHandler) {
		if h.setupReads != nil {
			h.setupReads.approvals.sessions, h.setupReads.approvals.windows = sessions, windows
		}
	}
}

// WithMandatesApprovalPasskeys lets a window opened by a computer's own
// passkey count for Setup's read Card, for a passkey session, while that
// passkey is still registered to the device. Given after
// WithMandatesSetupReads.
func WithMandatesApprovalPasskeys(passkeys auth.PasskeyStore) MandatesOption {
	return func(h *MandatesHandler) {
		if h.setupReads != nil {
			h.setupReads.approvals.passkeys = passkeys
		}
	}
}

// WithMandatesApprovalChecks lets a connection's approval that KIFF verified
// inside an open window at the approve (/v1/devices/verify) count for
// Setup's read Card once, within auth.ApprovalCheckLife of that check, after
// the window has closed: the vendor's sign-in comes between the two, and the
// phone closes its window when it leaves the app (kiff/tafs window_required).
// Given after WithMandatesSetupReads.
func WithMandatesApprovalChecks(checks auth.ApprovalCheckStore) MandatesOption {
	return func(h *MandatesHandler) {
		if h.setupReads != nil {
			h.setupReads.checks = checks
		}
	}
}

// checkedAtApprove says whether KIFF recorded, at the approve, that it
// verified this exact approval (message and signature) of grant on tenant
// by deviceID inside an open window, within auth.ApprovalCheckLife of now and
// not used yet; and that the device and its person still may connect a
// tool now. It answers the device and member as approved does, and the
// record's id to mark used last; a reason when it doesn't count.
func (s *setupReads) checkedAtApprove(ctx context.Context, tenant, deviceID, grant, message, sig string, now time.Time) (
	auth.ApprovalDevice, membership.Member, string, string, error) {
	_, window, ok := planqueLines(message)
	if !ok || window == "" || s.checks == nil {
		return auth.ApprovalDevice{}, membership.Member{}, "", "window_required", nil
	}
	id := auth.ApprovalCheckID([]byte(message), sig)
	c, err := s.checks.Get(ctx, id)
	if errors.Is(err, auth.ErrApprovalCheckNotFound) {
		return auth.ApprovalDevice{}, membership.Member{}, "", "window_required", nil
	}
	if err != nil {
		return auth.ApprovalDevice{}, membership.Member{}, "", "", errors.New("approval_check_unavailable")
	}
	if c.Tenant != tenant || c.DeviceID != deviceID || c.Grant != grant || c.Window != window {
		return auth.ApprovalDevice{}, membership.Member{}, "", "window_required", nil
	}
	if c.UsedAt != nil {
		return auth.ApprovalDevice{}, membership.Member{}, "", "approval_used", nil
	}
	// The window's own key, still its session's: a re-link or a new
	// enrolment since ends the window, and its record with it.
	if s.approvals.windows == nil || s.approvals.sessions == nil {
		return auth.ApprovalDevice{}, membership.Member{}, "", "window_required", nil
	}
	win, err := s.approvals.windows.Get(ctx, c.Window)
	if errors.Is(err, auth.ErrWindowNotFound) {
		return auth.ApprovalDevice{}, membership.Member{}, "", "window_required", nil
	}
	if err != nil {
		return auth.ApprovalDevice{}, membership.Member{}, "", "", errors.New("window_check_unavailable")
	}
	if win.Tenant != tenant || win.DeviceID != deviceID || !windowOpenedByCurrentKey(ctx, s.approvals.sessions, s.approvals.passkeys, win, now) {
		return auth.ApprovalDevice{}, membership.Member{}, "", "window_required", nil
	}
	if now.Before(c.CheckedAt) || now.Sub(c.CheckedAt) > auth.ApprovalCheckLife {
		return auth.ApprovalDevice{}, membership.Member{}, "", "window_required", nil
	}
	// The device and its person, now: a device revoked, or a member who may
	// no longer connect a tool, since the approve doesn't count.
	d, m, reason, err := checkDevice(ctx, s.approvals.devices, s.approvals.members, tenant, deviceID, []byte(message), sig, membership.CapToolsConnect)
	if err != nil || reason != "" {
		return d, m, "", reason, err
	}
	return d, m, id, "", nil
}

// SetupReadsCardID is the id of Setup's Card for grant's reads.
func SetupReadsCardID(grant string) string { return "setup-reads-" + grant }

func (h *MandatesHandler) issueSetupReads(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := h.identity(w, r)
	if !ok {
		return
	}
	if !id.IsConnector() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connector_key_required",
			"detail": "planque's connector key asks for Setup's read Card, with the owner's signed approval"})
		return
	}
	var body struct {
		Agent    string   `json:"agent"`
		Grant    string   `json:"grant"`
		Tools    []string `json:"tools"`
		Approval struct {
			DeviceID  string `json:"device_id"`
			Message   string `json:"message"`   // the exact text the device signed
			Body      string `json:"body"`      // the approve request's body, whose SHA-256 the message names
			Signature string `json:"signature"` // base64 DER ECDSA over its SHA-256
			// Request is the approve request's own message when message is a
			// kiff.requests.v1 one, signed over it and the taf's Card.
			Request string `json:"request"`
		} `json:"approval"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body is {agent, grant, tools, approval: {device_id, message, body, signature}}"})
		return
	}
	refuse := func(status int, msg string) {
		writeJSON(w, status, map[string]string{"error": msg})
	}
	if !validGrantID(body.Grant) {
		refuse(http.StatusBadRequest, "grant must be planque's grant id")
		return
	}
	if len(body.Tools) == 0 || len(body.Tools) > 10 {
		refuse(http.StatusBadRequest, "tools names from 1 to 10 of the grant's tools")
		return
	}
	tools := slices.Clone(body.Tools)
	slices.Sort(tools)
	if len(slices.Compact(tools)) != len(body.Tools) {
		refuse(http.StatusBadRequest, "tools names each tool once")
		return
	}

	// Only for an agent that holds no tools: one whose live key carries
	// setup_reader, never a taf's (gateway_agent).
	keys, err := h.setupReads.keys.ListActive(r.Context(), tenantID)
	if err != nil {
		refuse(http.StatusServiceUnavailable, "the agent's keys could not be read; no Card was issued")
		return
	}
	if !slices.ContainsFunc(keys, func(k auth.APIKeyRecord) bool {
		return k.AgentID == body.Agent && slices.Contains(k.Roles, auth.RoleSetupReader)
	}) {
		refuse(http.StatusForbidden, body.Agent+" isn't an agent that holds no tools (no live key carries "+auth.RoleSetupReader+")")
		return
	}

	// The owner's approval of this connection, checked here: the request it
	// stands for (the message, or the request a paired message names) is this
	// grant's approve or key, with this body, and the signature is the owner's.
	request, _, why := approvedRequest(body.Approval.Message, body.Approval.Request, tenantID)
	if why != "" {
		refuse(http.StatusForbidden, "the owner's approval doesn't check ("+why+"); no Card was issued")
		return
	}
	if why := approvesGrant(request, body.Approval.Body, tenantID, body.Grant, h.now()); why != "" {
		refuse(http.StatusForbidden, why)
		return
	}
	_, d, m, reason, err := h.setupReads.approvals.approved(r.Context(), tenantID,
		body.Approval.DeviceID, body.Approval.Message, body.Approval.Request, body.Approval.Signature, membership.CapToolsConnect, h.now())
	// A connection's approval signed inside a window that has closed since:
	// KIFF's own record of verifying it inside that window at the approve,
	// once, within auth.ApprovalCheckLife (checkedAtApprove). Only the
	// connection's own approval, never a paired one.
	checked := ""
	if err == nil && reason == "window_required" && body.Approval.Request == "" {
		d, m, checked, reason, err = h.setupReads.checkedAtApprove(r.Context(), tenantID, body.Approval.DeviceID, body.Grant,
			body.Approval.Message, body.Approval.Signature, h.now())
	}
	if err != nil {
		refuse(http.StatusServiceUnavailable, "the owner's approval could not be checked; no Card was issued")
		return
	}
	if reason != "" {
		refuse(http.StatusForbidden, "the owner's approval doesn't check ("+reason+"); no Card was issued")
		return
	}

	// Only tools this connector connected, each marked read-only.
	owners, err := h.setupReads.tools.ToolOwners(r.Context(), tenantID)
	if err != nil {
		refuse(http.StatusServiceUnavailable, "who connected the tools could not be read; no Card was issued")
		return
	}
	readOnly, err := h.setupReads.tools.ReadOnlyTools(r.Context(), tenantID)
	if err != nil {
		refuse(http.StatusServiceUnavailable, "which tools only read could not be read; no Card was issued")
		return
	}
	for _, t := range tools {
		owner, connected := owners[t]
		if !connected || owner != id.APIKeyID() {
			refuse(http.StatusForbidden, t+" isn't a tool this connector connected")
			return
		}
		if !readOnly[t] {
			refuse(http.StatusForbidden, t+" isn't marked read-only, so Setup can't read it")
			return
		}
	}

	card := mandateBody{ID: SetupReadsCardID(body.Grant), Subject: body.Agent, Domain: gatewayDomain,
		Aggregates: []mandateAggregateBody{{Quantity: string(mandate.KindCount), Limit: SetupReadsPerDay, Window: string(mandate.WindowCalendarDay)}},
		OnExceed:   mandate.OnExceedRefuse}
	for _, t := range tools {
		card.Grants = append(card.Grants, mandateGrantBody{Action: t})
	}
	mc, status, msg := h.prepareCard(r, tenantID, card)
	if status != 0 {
		refuse(status, msg)
		return
	}
	// The record counts once, marked last, after every other check: of two
	// parallel uses, one issues the Card and the other is refused.
	if checked != "" {
		used, err := h.setupReads.checks.Use(r.Context(), checked, h.now())
		if err != nil {
			refuse(http.StatusServiceUnavailable, "the owner's approval could not be checked; no Card was issued")
			return
		}
		if !used {
			refuse(http.StatusForbidden, "the owner's approval doesn't check (approval_used); no Card was issued")
			return
		}
	}
	who := d.Name
	if who == "" {
		who = d.ID
	}
	by := "planque, on the approval of " + m.Subject + " on " + who
	if err := h.store.Issue(r.Context(), mc, by); err != nil {
		h.logger.ErrorContext(r.Context(), "setup reads: issue failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		refuse(http.StatusInternalServerError, "Setup's read Card could not be issued")
		return
	}
	h.ensureHolders(r, tenantID, mc.HolderList(), by)
	writeJSON(w, http.StatusCreated, mandateView(mc, h.now()))
}

// approvesGrant says why message isn't the owner's approval of grant on
// tenant, with body the request's body, signed within setupReadsApprovalAge
// of now, or "". It is planque's signed request (kiff/planque
// internal/device.Message): its kind, planque's base URL, the account, the
// method, the path, the body's SHA-256 and when, and in v2 the approval
// window's id (planqueApproval checks the window). The body names the digest
// of the grant's tools, so the signature covers which tools Setup reads.
func approvesGrant(message, body, tenant, grant string, now time.Time) string {
	lines, _, ok := planqueLines(message)
	if !ok {
		return "the approval isn't a request planque had the owner sign"
	}
	// The connection's approve, or a key app's key given with its approval.
	path := "/v1/accounts/" + tenant + "/grants/" + grant
	if lines[2] != tenant || lines[3] != http.MethodPost || (lines[4] != path+"/approve" && lines[4] != path+"/key") {
		return "the approval is for another account, connection or step"
	}
	sum := sha256.Sum256([]byte(body))
	if lines[5] != hex.EncodeToString(sum[:]) {
		return "the approval's body isn't the one the owner signed"
	}
	at, err := time.Parse(time.RFC3339, lines[6])
	if err != nil || now.Sub(at) > setupReadsApprovalAge || at.Sub(now) > 5*time.Minute {
		return "the approval was signed more than a day ago, or in the future"
	}
	return ""
}

// validGrantID is planque's grant id: letters, digits, _ and -, at most 64.
func validGrantID(g string) bool {
	if g == "" || len(g) > 64 {
		return false
	}
	for _, r := range g {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
