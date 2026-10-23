package connection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/kiff/cloud/gateway/internal/egress"
	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/mcp"
	"github.com/kiff/cloud/gateway/internal/secret"
)

// API is the part of apps/api the connection routes use, always with the
// caller's own key.
type API interface {
	Me(ctx context.Context, key string) (kiffapi.Me, error)
	ApplyDomain(ctx context.Context, key, domain, yaml string) error
	SetPermitTargets(ctx context.Context, key string, ts []kiffapi.PermitTarget) error
}

// Lister lists a remote server's tools.
type Lister interface {
	ListTools(ctx context.Context, url, bearer string) ([]mcp.RemoteTool, error)
}

// Handler serves /v1/connections for account owners.
type Handler struct {
	Store  *Store
	API    API
	Remote Lister
	Policy egress.Policy
	Logger *slog.Logger
}

// Register mounts the routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/connections", h.authRead(h.list))
	mux.HandleFunc("POST /v1/connections", h.auth(h.create))
	mux.HandleFunc("POST /v1/connections/discover", h.auth(h.discover))
	mux.HandleFunc("POST /v1/connections/{name}/test", h.auth(h.test))
	mux.HandleFunc("PUT /v1/connections/{name}/credential", h.auth(notConnector(h.setCredential)))
	mux.HandleFunc("PUT /v1/connections/{name}/mode", h.auth(notConnector(h.setMode)))
	mux.HandleFunc("PUT /v1/connections/{name}/settings", h.auth(notConnector(h.setSettings)))
	mux.HandleFunc("DELETE /v1/connections/{name}", h.auth(h.remove))
}

type caller struct {
	key string
	me  kiffapi.Me
}

// notConnector refuses a connector key. It connects tools
// and removes the ones it connected; it never re-points, re-keys or
// re-configures a tool, so a leaked one can't take over the owner's.
func notConnector(next func(http.ResponseWriter, *http.Request, caller)) func(http.ResponseWriter, *http.Request, caller) {
	return func(w http.ResponseWriter, r *http.Request, c caller) {
		if c.me.IsConnector() {
			writeErr(w, http.StatusForbidden, connectorOwnOnly)
			return
		}
		next(w, r, c)
	}
}

const connectorOwnOnly = "a connector key connects tools and tests or removes only the ones it connected; change a tool in KIFF"

// mayTouch reports whether c may test or remove conn: anyone who manages the
// account's tools, and a connector key only for a tool it connected.
func mayTouch(c caller, conn Connection) bool {
	return !c.me.IsConnector() || (c.me.CredentialID != "" && conn.CreatedBy == c.me.CredentialID)
}

func (h *Handler) auth(next func(http.ResponseWriter, *http.Request, caller)) http.HandlerFunc {
	return h.authAs(next, kiffapi.Me.CanManage)
}

// authRead admits a phone's session too, to list the tools: it
// may read the account's connections, never change them.
func (h *Handler) authRead(next func(http.ResponseWriter, *http.Request, caller)) http.HandlerFunc {
	return h.authAs(next, kiffapi.Me.CanRead)
}

func (h *Handler) authAs(next func(http.ResponseWriter, *http.Request, caller), allowed func(kiffapi.Me) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(key) == "" {
			writeErr(w, http.StatusUnauthorized, "a KIFF key or session is required")
			return
		}
		// A person on several accounts names one; the header is
		// forwarded on every call this request makes to apps/api.
		r = r.WithContext(kiffapi.WithTenant(r.Context(), strings.TrimSpace(r.Header.Get(kiffapi.TenantHeader))))
		me, err := h.API.Me(r.Context(), strings.TrimSpace(key))
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "the key was not accepted")
			return
		}
		if !allowed(me) {
			msg := "connecting tools requires an admin or editor on the account; an agent's key cannot change its own tools"
			if me.DeviceSession {
				msg = "a phone's session can list the tools but not change them; connect tools in KIFF"
			}
			writeErr(w, http.StatusForbidden, msg)
			return
		}
		next(w, r, caller{key: strings.TrimSpace(key), me: me})
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, c caller) {
	conns, err := h.Store.List(r.Context(), c.me.TenantID)
	if err != nil {
		h.fail(w, r, "list", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": conns})
}

type createRequest struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Tool      string `json:"tool"`
	AmountArg string `json:"amount_argument"`
	// AmountUnit and AmountScale say what the amount means, for display:
	// "EUR" with 2 when the tool takes cents. Optional.
	AmountUnit     string `json:"amount_unit"`
	AmountScale    *int   `json:"amount_scale"`
	IdempotencyArg string `json:"idempotency_argument"`
	// ResourceArg names the string argument saying what a call is on (an
	// order, a customer), so a Card can be limited to named ones.
	ResourceArg string `json:"resource_argument"`
	// AmountPath and ResourcePath say where a generic tool carries the
	// amount and the resource ("parameters.amount"); FixedArgs are sent on
	// every call and the agent can't set them.
	AmountPath        string          `json:"amount_path"`
	ResourcePath      string          `json:"resource_path"`
	FixedArgs         json.RawMessage `json:"fixed_arguments"`
	OutcomeContract   string          `json:"outcome_contract"`
	OutcomeLookupTool string          `json:"outcome_lookup_tool"`
	// UnitLookup names the read that says which unit a call is in (a
	// payment's currency), so a call in another unit is refused.
	UnitLookup  *gateway.UnitLookup `json:"unit_lookup"`
	Credential  string              `json:"credential"`
	Description string              `json:"description"`
	// Title is the name people see. Empty: the vendor's own MCP title, or
	// its tool's name in words.
	Title string `json:"title"`
	// ReadOnly declares the tool only reads; default false keeps the
	// safe behaviour (identical calls within the window are one).
	ReadOnly bool `json:"read_only"`
	// Mode and Audience choose who holds the downstream credential (RFC
	// 046): sealed (the default), verify or relay.
	Mode     string `json:"mode"`
	Audience string `json:"audience"`
}

const maxCredential = 8 << 10

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "the request body must be a JSON object with the documented fields")
		return false
	}
	return true
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request, c caller) {
	var req createRequest
	if !decode(w, r, &req) {
		return
	}
	req.OutcomeContract = strings.TrimSpace(req.OutcomeContract)
	req.OutcomeLookupTool = strings.TrimSpace(req.OutcomeLookupTool)
	if req.OutcomeContract != "" && (req.OutcomeContract != gateway.TerminalNoEffectV1 || req.IdempotencyArg == "") {
		writeErr(w, http.StatusBadRequest, "outcome_contract must be terminal_no_effect_v1 with an idempotency argument; enable it only for a trusted executor that guarantees terminal non-actions")
		return
	}
	req.Tool = strings.TrimSpace(req.Tool)
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = req.Tool
	}
	if req.Tool == "" {
		writeErr(w, http.StatusBadRequest, "tool is required: the name of the tool on the remote server")
		return
	}
	if !gateway.ValidToolName(req.Name) {
		writeErr(w, http.StatusBadRequest, "name must start with a letter and use only letters, digits and underscores (up to 64)")
		return
	}
	if gateway.ReservedToolName(req.Name) {
		writeErr(w, http.StatusBadRequest, req.Name+" is the name of KIFF's own tool; choose another name")
		return
	}
	if len(req.Credential) > maxCredential {
		writeErr(w, http.StatusBadRequest, "the credential is too long")
		return
	}
	u, err := h.Policy.ValidateURL(r.Context(), strings.TrimSpace(req.URL))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	remote, msg := h.probe(r.Context(), u.String(), req.Credential, req.Tool)
	if remote == nil {
		writeErr(w, http.StatusBadGateway, msg)
		return
	}
	if msg := checkGoverned(remote.InputSchema, &req); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if req.OutcomeLookupTool != "" {
		if req.OutcomeContract != gateway.TerminalNoEffectV1 || req.OutcomeLookupTool == req.Tool || req.IdempotencyArg == "" {
			writeErr(w, http.StatusBadRequest, "outcome_lookup_tool must be a separate read-only status tool under terminal_no_effect_v1")
			return
		}
		lookup, msg := h.probe(r.Context(), u.String(), req.Credential, req.OutcomeLookupTool)
		if lookup == nil {
			writeErr(w, http.StatusBadGateway, msg)
			return
		}
		if lookup.Annotations == nil || !lookup.Annotations.ReadOnlyHint || checkArgs(lookup.InputSchema, "", req.IdempotencyArg) != "" {
			writeErr(w, http.StatusBadRequest, "the status tool must declare readOnlyHint and accept the operation's idempotency argument")
			return
		}
	}
	mode, audience, msg := checkMode(req.Mode, req.Audience, req.IdempotencyArg)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if c.me.IsConnector() && (mode == gateway.ModeSealed || c.me.CredentialID == "") {
		// The connector's holder keeps the tool's credential (relay), or
		// the tool checks KIFF's permit itself (verify).
		writeErr(w, http.StatusForbidden, "a connector key connects tools in relay or verify mode; KIFF holds no credential for them")
		return
	}
	if mode != gateway.ModeSealed && (req.AmountPath != "" || req.ResourcePath != "") {
		writeErr(w, http.StatusBadRequest, pathsSealedOnly)
		return
	}
	if mode == gateway.ModeVerify && req.Credential != "" {
		writeErr(w, http.StatusBadRequest, "in verify mode KIFF holds no credential for the tool; leave credential empty")
		return
	}
	scale, msg := checkUnit(req.AmountArg, strings.TrimSpace(req.AmountUnit), req.AmountScale)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if l := req.UnitLookup; l != nil {
		l.Tool, l.Argument, l.Path = strings.TrimSpace(l.Tool), strings.TrimSpace(l.Argument), strings.TrimSpace(l.Path)
		if msg := l.Valid(gateway.Tool{Name: req.Name, AmountArg: req.AmountArg, AmountUnit: strings.TrimSpace(req.AmountUnit), ResourceArg: req.ResourceArg}); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
	}
	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		desc = remote.Description
	}
	title, msg := toolTitle(req.Title, req.Tool, remote)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	schema := remote.InputSchema
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	conn := Connection{Name: req.Name, Title: title, Description: desc, URL: u.String(), UpstreamTool: req.Tool,
		AmountArg: req.AmountArg, AmountUnit: strings.TrimSpace(req.AmountUnit), AmountScale: scale,
		IdempotencyArg: req.IdempotencyArg, ResourceArg: req.ResourceArg, AmountPath: req.AmountPath, ResourcePath: req.ResourcePath, FixedArgs: req.FixedArgs, InputSchema: schema, ReadOnly: req.ReadOnly, Mode: mode, Audience: audience, OutcomeContract: req.OutcomeContract, OutcomeLookupTool: req.OutcomeLookupTool,
		UnitLookup: req.UnitLookup, CreatedBy: c.me.CredentialID}
	err = h.Store.Change(r.Context(), c.me.TenantID,
		func(tx *Tx) error { return tx.Insert(r.Context(), conn, req.Credential) },
		h.applier(r.Context(), c))
	if errors.Is(err, ErrExists) {
		writeErr(w, http.StatusConflict, "a tool named "+req.Name+" is already connected; remove it first, or choose another name")
		return
	}
	var ce contractError
	if errors.As(err, &ce) {
		writeErr(w, http.StatusUnprocessableEntity, "KIFF could not update the account's tool contract, so the tool was not connected: "+ce.Error())
		return
	}
	if err != nil {
		h.fail(w, r, "create", err)
		return
	}
	h.log(r, "tool connected", c, req.Name, slog.String("host", u.Host))
	saved, _ := h.Store.Get(r.Context(), c.me.TenantID, req.Name)
	writeJSON(w, http.StatusCreated, saved)
}

// discoveredTool is a remote tool as the connect form offers it: the
// arguments a Card can sum and the ones KIFF can fill with an
// idempotency key.
type discoveredTool struct {
	Name string `json:"name"`
	// Title is the vendor's name for people, when it gives one.
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description"`
	IntegerArgs []string `json:"integer_arguments"`
	StringArgs  []string `json:"string_arguments"`
}

// maxDiscovered bounds how many tools discover returns.
const maxDiscovered = 200

// discover lists a server's tools with a credential, so an owner can
// choose one before connecting it. Nothing is stored, and the credential
// is never returned.
func (h *Handler) discover(w http.ResponseWriter, r *http.Request, c caller) {
	var req struct {
		URL        string `json:"url"`
		Credential string `json:"credential"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Credential) > maxCredential {
		writeErr(w, http.StatusBadRequest, "the credential is too long")
		return
	}
	u, err := h.Policy.ValidateURL(r.Context(), strings.TrimSpace(req.URL))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	tools, err := h.Remote.ListTools(r.Context(), u.String(), req.Credential)
	if err != nil {
		writeErr(w, http.StatusBadGateway, secret.Redact("KIFF could not list the tools at that URL: "+err.Error(), req.Credential))
		return
	}
	if len(tools) > maxDiscovered {
		tools = tools[:maxDiscovered]
	}
	out := make([]discoveredTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, discoveredTool{Name: t.Name, Title: secret.Redact(vendorTitle(t), req.Credential), Description: secret.Redact(t.Description, req.Credential),
			IntegerArgs: argsOfType(t.InputSchema, "integer"), StringArgs: argsOfType(t.InputSchema, "string")})
	}
	h.log(r, "tools discovered", c, "", slog.String("host", u.Host), slog.Int("tools", len(out)))
	writeJSON(w, http.StatusOK, map[string]any{"url": u.String(), "tools": out})
}

func (h *Handler) test(w http.ResponseWriter, r *http.Request, c caller) {
	conn, cred, ok := h.load(w, r, c)
	if !ok {
		return
	}
	if !mayTouch(c, conn) {
		writeErr(w, http.StatusForbidden, connectorOwnOnly)
		return
	}
	if _, err := h.Policy.ValidateURL(r.Context(), conn.URL); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if remote, msg := h.probe(r.Context(), conn.URL, cred, conn.UpstreamTool); remote == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": msg})
		return
	}
	how := "with the stored credential"
	if conn.Mode == gateway.ModeVerify {
		how = "without a credential (the tool checks KIFF's execution permit on each call)"
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "KIFF reached the tool server " + how + " and found " + conn.UpstreamTool + "."})
}

func (h *Handler) setCredential(w http.ResponseWriter, r *http.Request, c caller) {
	var req struct {
		Credential string `json:"credential"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Credential) > maxCredential {
		writeErr(w, http.StatusBadRequest, "the credential is too long")
		return
	}
	conn, _, ok := h.load(w, r, c)
	if !ok {
		return
	}
	if conn.Mode == gateway.ModeVerify {
		writeErr(w, http.StatusBadRequest, "in verify mode KIFF holds no credential for this tool; change the connection's mode to give KIFF one")
		return
	}
	// The new credential must work before it replaces the old one.
	if remote, msg := h.probe(r.Context(), conn.URL, req.Credential, conn.UpstreamTool); remote == nil {
		writeErr(w, http.StatusBadGateway, "the new credential was not saved: "+msg)
		return
	}
	err := h.Store.Change(r.Context(), c.me.TenantID,
		func(tx *Tx) error { return tx.SetCredential(r.Context(), conn.Name, req.Credential) },
		func([]gateway.Tool) error { return nil })
	if err != nil {
		h.fail(w, r, "credential", err)
		return
	}
	h.log(r, "tool credential replaced", c, conn.Name)
	saved, _ := h.Store.Get(r.Context(), c.me.TenantID, conn.Name)
	writeJSON(w, http.StatusOK, saved)
}

// setMode moves a connection between custody modes. The
// tool must answer with the credential the new mode uses before anything
// changes; the old credential is then replaced in the same change, so a
// connection moved to verify keeps no credential at KIFF.
func (h *Handler) setMode(w http.ResponseWriter, r *http.Request, c caller) {
	var req struct {
		Mode       string `json:"mode"`
		Audience   string `json:"audience"`
		URL        string `json:"url"`
		Credential string `json:"credential"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Credential) > maxCredential {
		writeErr(w, http.StatusBadRequest, "the credential is too long")
		return
	}
	conn, _, ok := h.load(w, r, c)
	if !ok {
		return
	}
	mode, audience, msg := checkMode(req.Mode, req.Audience, conn.IdempotencyArg)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	switch {
	case strings.TrimSpace(req.Mode) == "":
		writeErr(w, http.StatusBadRequest, "mode is required: sealed, verify or relay")
		return
	case mode == gateway.ModeVerify && req.Credential != "":
		writeErr(w, http.StatusBadRequest, "in verify mode KIFF holds no credential for the tool; leave credential empty")
		return
	case mode == gateway.ModeSealed && req.Credential == "":
		writeErr(w, http.StatusBadRequest, "moving back to sealed needs the tool's credential again: KIFF deleted it when the connection left sealed")
		return
	case mode != gateway.ModeSealed && (conn.AmountPath != "" || conn.ResourcePath != ""):
		writeErr(w, http.StatusBadRequest, pathsSealedOnly)
		return
	}
	target := conn.URL
	if strings.TrimSpace(req.URL) != "" {
		u, err := h.Policy.ValidateURL(r.Context(), strings.TrimSpace(req.URL))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		target = u.String()
	}
	if remote, msg := h.probe(r.Context(), target, req.Credential, conn.UpstreamTool); remote == nil {
		writeErr(w, http.StatusBadGateway, "the connection was not changed: "+msg)
		return
	}
	err := h.Store.Change(r.Context(), c.me.TenantID, func(tx *Tx) error {
		if err := tx.SetMode(r.Context(), conn.Name, mode, audience, target); err != nil {
			return err
		}
		return tx.SetCredential(r.Context(), conn.Name, req.Credential)
	}, h.targets(r.Context(), c))
	var ce contractError
	if errors.As(err, &ce) {
		writeErr(w, http.StatusUnprocessableEntity, "KIFF could not record where this tool's permits go, so the connection was not changed: "+ce.Error())
		return
	}
	if err != nil {
		h.fail(w, r, "mode", err)
		return
	}
	h.log(r, "tool custody mode changed", c, conn.Name, slog.String("mode", mode))
	saved, _ := h.Store.Get(r.Context(), c.me.TenantID, conn.Name)
	writeJSON(w, http.StatusOK, saved)
}

// checkMode validates a custody mode. verify and relay need
// an audience and an idempotency argument: the operation id is part of
// what the permit binds, and the tool's store de-duplicates on it.
func checkMode(mode, audience, idem string) (string, string, string) {
	mode, audience = strings.TrimSpace(mode), strings.TrimSpace(audience)
	switch mode {
	case "", gateway.ModeSealed:
		if audience != "" {
			return "", "", "audience is only for verify and relay connections"
		}
		return gateway.ModeSealed, "", ""
	case gateway.ModeVerify, gateway.ModeRelay:
	default:
		return "", "", "mode must be sealed, verify or relay"
	}
	if audience == "" || len(audience) > 512 || strings.ContainsAny(audience, " \t\r\n") {
		return "", "", "a " + mode + " connection needs an audience: the identifier its tool or relay is configured to expect, such as its URL"
	}
	if idem == "" {
		return "", "", "a " + mode + " connection needs an idempotency_argument: the execution permit binds the operation id the tool de-duplicates on"
	}
	return mode, audience, ""
}

// setSettings changes what KIFF checks on a connected tool without
// reconnecting it: the argument naming what each call is on, and
// whether a trusted no-effect result gives a Card its room back. A field
// left out keeps its value. The contract and permit targets are applied in
// the same change, as when connecting, so a Card's resource scope and the
// permit's governed argument cannot drift from the connection.
func (h *Handler) setSettings(w http.ResponseWriter, r *http.Request, c caller) {
	var req struct {
		ResourceArg     *string `json:"resource_argument"`
		OutcomeContract *string `json:"outcome_contract"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.ResourceArg == nil && req.OutcomeContract == nil {
		writeErr(w, http.StatusBadRequest, "nothing to change: send resource_argument, outcome_contract, or both")
		return
	}
	name := r.PathValue("name")
	var resource, contract string
	// Read, merged and checked inside the change, under the account's
	// lock: two partial changes cannot write back each other's old value,
	// and the checks see the connection as it is now.
	err := h.Store.Change(r.Context(), c.me.TenantID, func(tx *Tx) error {
		conn, err := tx.Get(r.Context(), name)
		if err != nil {
			return err
		}
		lookup := conn.OutcomeLookupTool
		resource, contract = conn.ResourceArg, conn.OutcomeContract
		if req.ResourceArg != nil {
			if conn.ResourcePath != "" {
				// The settings sheet names top-level arguments only.
				return badSettings("this tool reads its resource at " + conn.ResourcePath + "; reconnect it to change that")
			}
			resource = strings.TrimSpace(*req.ResourceArg)
			if msg := checkResourceArg(conn.InputSchema, resource, conn.AmountArg, conn.IdempotencyArg); msg != "" {
				return badSettings(msg)
			}
		}
		if req.OutcomeContract != nil {
			contract = strings.TrimSpace(*req.OutcomeContract)
			if contract != "" && (contract != gateway.TerminalNoEffectV1 || conn.IdempotencyArg == "") {
				return badSettings("outcome_contract must be terminal_no_effect_v1 with an idempotency argument; enable it only for a trusted executor that guarantees terminal non-actions")
			}
			if contract == "" {
				// A status lookup exists only under the contract.
				lookup = ""
			}
		}
		return tx.SetSettings(r.Context(), name, resource, contract, lookup)
	}, h.applier(r.Context(), c))
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no tool named "+name+" is connected")
		return
	}
	var bad badSettings
	if errors.As(err, &bad) {
		writeErr(w, http.StatusBadRequest, string(bad))
		return
	}
	var ce contractError
	if errors.As(err, &ce) {
		writeErr(w, http.StatusUnprocessableEntity, "KIFF could not update the account's tool contract, so the settings were not changed: "+ce.Error())
		return
	}
	if err != nil {
		h.fail(w, r, "settings", err)
		return
	}
	h.log(r, "tool settings changed", c, name, slog.String("resource_argument", resource), slog.String("outcome_contract", contract))
	saved, _ := h.Store.Get(r.Context(), c.me.TenantID, name)
	writeJSON(w, http.StatusOK, saved)
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request, c caller) {
	name := r.PathValue("name")
	err := h.Store.Change(r.Context(), c.me.TenantID,
		func(tx *Tx) error {
			// Checked under the account's lock, so the tool removed is the
			// one checked.
			if c.me.IsConnector() {
				conn, err := tx.Get(r.Context(), name)
				if err != nil {
					return err
				}
				if !mayTouch(c, conn) {
					return errNotYours
				}
			}
			return tx.Delete(r.Context(), name)
		},
		h.applier(r.Context(), c))
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no tool named "+name+" is connected")
		return
	}
	if errors.Is(err, errNotYours) {
		writeErr(w, http.StatusForbidden, connectorOwnOnly)
		return
	}
	var ce contractError
	if errors.As(err, &ce) {
		writeErr(w, http.StatusUnprocessableEntity, "KIFF could not update the account's tool contract, so the tool was not removed: "+ce.Error())
		return
	}
	if err != nil {
		h.fail(w, r, "delete", err)
		return
	}
	h.log(r, "tool removed", c, name)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) load(w http.ResponseWriter, r *http.Request, c caller) (Connection, string, bool) {
	name := r.PathValue("name")
	conn, err := h.Store.Get(r.Context(), c.me.TenantID, name)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no tool named "+name+" is connected")
		return Connection{}, "", false
	}
	if err != nil {
		h.fail(w, r, "load", err)
		return Connection{}, "", false
	}
	cred, err := h.Store.Credential(r.Context(), c.me.TenantID, name)
	if err != nil {
		h.fail(w, r, "credential", err)
		return Connection{}, "", false
	}
	return conn, cred, true
}

type contractError struct{ error }

// errNotYours: a connector key asked to remove a tool it did not connect.
var errNotYours = errors.New("connection: not connected by this key")

// badSettings is a settings change the connection does not allow, found
// inside the change; it is the owner's message.
type badSettings string

func (b badSettings) Error() string { return string(b) }

// applier applies the account's gateway contract and its permit targets
// with the caller's key. An account whose last tool is removed keeps its
// last contract: the API needs at least one action, and with no connection
// behind it no call can reach a tool. Its permit targets are cleared.
func (h *Handler) applier(ctx context.Context, c caller) func([]gateway.Tool) error {
	return func(tools []gateway.Tool) error {
		if len(tools) > 0 {
			y, err := gateway.Contract(tools)
			if err != nil {
				return contractError{err}
			}
			if err := h.API.ApplyDomain(ctx, c.key, gateway.Domain, y); err != nil {
				return contractError{err}
			}
		}
		return h.targets(ctx, c)(tools)
	}
}

// targets sets where each verify or relay tool's permit goes, in apps/api,
// with the caller's key: the decide path signs these, never a tool or
// audience a decide request names.
func (h *Handler) targets(ctx context.Context, c caller) func([]gateway.Tool) error {
	return func(tools []gateway.Tool) error {
		ts := []kiffapi.PermitTarget{}
		for _, t := range tools {
			if t.Mode == gateway.ModeVerify || t.Mode == gateway.ModeRelay {
				ts = append(ts, kiffapi.PermitTarget{Domain: gateway.Domain, Action: t.Name, Tool: t.UpstreamTool,
					Audience: t.Audience, AmountArg: t.AmountArg, ResourceArg: t.ResourceArg})
			}
		}
		if err := h.API.SetPermitTargets(ctx, c.key, ts); err != nil {
			return contractError{err}
		}
		return nil
	}
}

// probe lists the remote server's tools with credential and returns the
// named one, or a message for the owner with the credential redacted.
func (h *Handler) probe(ctx context.Context, url, credential, tool string) (*mcp.RemoteTool, string) {
	tools, err := h.Remote.ListTools(ctx, url, credential)
	if err != nil {
		return nil, secret.Redact("KIFF could not list the tools at that URL: "+err.Error(), credential)
	}
	for i := range tools {
		if tools[i].Name == tool {
			return &tools[i], ""
		}
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	if len(names) > 20 {
		names = append(names[:20], "…")
	}
	return nil, "the server has no tool named " + tool + "; it lists: " + strings.Join(names, ", ")
}

// schemaProps reads the top-level properties of a tool's input schema
// and the types each declares.
func schemaProps(schema json.RawMessage) map[string][]string {
	var s struct {
		Properties map[string]struct {
			Type any `json:"type"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(schema, &s)
	out := make(map[string][]string, len(s.Properties))
	for name, p := range s.Properties {
		switch t := p.Type.(type) {
		case string:
			out[name] = []string{t}
		case []any:
			for _, v := range t {
				if v, ok := v.(string); ok {
					out[name] = append(out[name], v)
				}
			}
		}
	}
	return out
}

// argsOfType lists, sorted, the arguments of the schema that accept typ
// and have a name KIFF can refer to.
func argsOfType(schema json.RawMessage, typ string) []string {
	out := []string{}
	for name, types := range schemaProps(schema) {
		if slices.Contains(types, typ) && gateway.ValidToolName(name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// checkUnit validates the amount's display unit: only with an amount
// argument, letters only, and 0 to 6 decimal places (scale needs a unit).
func checkUnit(amount, unit string, scale *int) (int, string) {
	s := 0
	if scale != nil {
		s = *scale
	}
	if unit == "" && scale == nil {
		return 0, ""
	}
	if amount == "" {
		return 0, "amount_unit and amount_scale need an amount_argument"
	}
	if unit == "" {
		return 0, "amount_scale needs an amount_unit"
	}
	if !gateway.ValidAmountUnit(unit, s) {
		return 0, "amount_unit must be letters only (a currency code such as EUR, or a word such as points), and amount_scale 0 to 6 decimal places (2 for cents)"
	}
	return s, ""
}

// checkArgs requires the amount argument to be an integer property and
// the idempotency argument a string property of the tool's schema.
func checkArgs(schema json.RawMessage, amount, idem string) string {
	props := schemaProps(schema)
	has := func(arg, typ string) bool { return slices.Contains(props[arg], typ) }
	if amount != "" {
		if !gateway.ValidToolName(amount) {
			return "amount_argument is not a valid argument name"
		}
		if !has(amount, "integer") {
			return "amount_argument " + amount + " must be an integer argument of the tool; a Card sums whole numbers (use cents if the tool takes decimals)"
		}
	}
	if idem != "" && !has(idem, "string") {
		return "idempotency_argument " + idem + " must be a string argument of the tool"
	}
	if amount != "" && amount == idem {
		return "amount_argument and idempotency_argument must differ"
	}
	return ""
}

// pathsSealedOnly: apps/api binds a verify or relay permit to top-level
// governed arguments, so nested paths are for sealed connections.
const pathsSealedOnly = "amount_path and resource_path work only for sealed connections for now: a verify or relay tool takes its governed arguments at the top level"

// maxFixedArgsBytes bounds a connection's fixed arguments.
const maxFixedArgsBytes = 4 << 10

// checkGoverned checks the amount, the resource and the fixed arguments
// and settles their names. A one-part path is the argument itself.
// A nested path starts at an object argument of the tool; the amount and
// the resource are then named after the path's last part unless the
// request names them. Fixed arguments must be arguments of the tool, and
// can't be the operation id, the idempotency argument, or where the
// amount or the resource is read.
func checkGoverned(schema json.RawMessage, req *createRequest) string {
	names := schemaPropNames(schema)
	props := schemaProps(schema)
	settle := func(kind string, path, name *string) string {
		*path = strings.TrimSpace(*path)
		*name = strings.TrimSpace(*name)
		if *path == "" {
			return ""
		}
		segs, ok := gateway.PathSegments(*path)
		if !ok {
			return kind + "_path must be argument names separated by dots, at most " + strconv.Itoa(gateway.MaxPathSegments)
		}
		last := segs[len(segs)-1]
		if len(segs) == 1 {
			if *name != "" && *name != last {
				return kind + "_argument and a one-part " + kind + "_path must be the same"
			}
			*name, *path = last, ""
			return ""
		}
		if !names[segs[0]] || (len(props[segs[0]]) > 0 && !slices.Contains(props[segs[0]], "object")) {
			return kind + "_path must start at an object argument of the tool; " + segs[0] + " is not one"
		}
		if segs[0] == gateway.OperationArg || segs[0] == req.IdempotencyArg {
			return kind + "_path cannot start at " + segs[0]
		}
		if *name == "" {
			*name = last
		}
		if !gateway.ValidToolName(*name) {
			return kind + "_argument is not a valid argument name"
		}
		return ""
	}
	if msg := settle("amount", &req.AmountPath, &req.AmountArg); msg != "" {
		return msg
	}
	if msg := settle("resource", &req.ResourcePath, &req.ResourceArg); msg != "" {
		return msg
	}
	// Top-level arguments keep their type checks; a nested one is checked
	// on each call (the tool's schema rarely types nested fields).
	topAmount, topResource := req.AmountArg, req.ResourceArg
	if req.AmountPath != "" {
		topAmount = ""
	}
	if req.ResourcePath != "" {
		topResource = ""
	}
	if msg := checkArgs(schema, topAmount, req.IdempotencyArg); msg != "" {
		return msg
	}
	if req.AmountArg != "" && req.AmountArg == req.IdempotencyArg {
		return "amount_argument and idempotency_argument must differ"
	}
	if topResource != "" {
		if msg := checkResourceArg(schema, topResource, req.AmountArg, req.IdempotencyArg); msg != "" {
			return msg
		}
	} else if req.ResourceArg != "" && (req.ResourceArg == req.AmountArg || req.ResourceArg == req.IdempotencyArg || req.ResourceArg == gateway.OperationArg) {
		return "resource_argument must differ from amount_argument, idempotency_argument and " + gateway.OperationArg
	}
	return checkFixed(req, names, topAmount, topResource)
}

func checkFixed(req *createRequest, names map[string]bool, topAmount, topResource string) string {
	raw := strings.TrimSpace(string(req.FixedArgs))
	if raw == "" || raw == "null" {
		req.FixedArgs = nil
		return ""
	}
	if len(raw) > maxFixedArgsBytes {
		return "fixed_arguments is too long"
	}
	var fixed map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fixed) != nil {
		return "fixed_arguments must be a JSON object of argument names and values"
	}
	if len(fixed) == 0 {
		req.FixedArgs = nil
		return ""
	}
	if len(fixed) > gateway.MaxFixedArgs {
		return "fixed_arguments can fix at most " + strconv.Itoa(gateway.MaxFixedArgs) + " arguments"
	}
	reserved := map[string]bool{gateway.OperationArg: true}
	for _, n := range []string{req.IdempotencyArg, topAmount, topResource, firstSegment(req.AmountPath), firstSegment(req.ResourcePath)} {
		if n != "" {
			reserved[n] = true
		}
	}
	for k := range fixed {
		if !names[k] {
			return "fixed_arguments: " + k + " is not an argument of the tool"
		}
		if reserved[k] {
			return "fixed_arguments: " + k + " is where KIFF reads or sets a governed value; it cannot be fixed"
		}
	}
	req.FixedArgs = json.RawMessage(raw)
	return ""
}

func firstSegment(path string) string {
	first, _, _ := strings.Cut(path, ".")
	return first
}

// schemaPropNames lists every property of the tool's schema, typed or not.
func schemaPropNames(schema json.RawMessage) map[string]bool {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	_ = json.Unmarshal(schema, &s)
	out := make(map[string]bool, len(s.Properties))
	for name := range s.Properties {
		out[name] = true
	}
	return out
}

// checkResourceArg requires the resource argument to be a string property
// of the tool's schema, distinct from the amount, from the idempotency
// argument (which the gateway overwrites with the operation id), and from
// the gateway's own operation argument (which it removes before reading
// the call's parameters).
func checkResourceArg(schema json.RawMessage, resource, amount, idem string) string {
	if resource == "" {
		return ""
	}
	if !gateway.ValidToolName(resource) {
		return "resource_argument is not a valid argument name"
	}
	if !slices.Contains(schemaProps(schema)[resource], "string") {
		return "resource_argument " + resource + " must be a string argument of the tool, such as an order or customer id"
	}
	if resource == amount || resource == idem {
		return "resource_argument must differ from amount_argument and idempotency_argument"
	}
	if resource == gateway.OperationArg {
		return "resource_argument cannot be " + gateway.OperationArg + ", which KIFF uses for its own operation id"
	}
	return ""
}

func (h *Handler) log(r *http.Request, msg string, c caller, name string, attrs ...any) {
	if h.Logger != nil {
		h.Logger.InfoContext(r.Context(), "connections: "+msg,
			append([]any{slog.String("tenant", c.me.TenantID), slog.String("tool", name)}, attrs...)...)
	}
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	if h.Logger != nil {
		h.Logger.ErrorContext(r.Context(), "connections: "+op+" failed", slog.String("err", err.Error()))
	}
	writeErr(w, http.StatusInternalServerError, "the request could not be completed; nothing was changed")
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// maxTitle bounds a tool's title: a name, not a description.
const maxTitle = 100

// toolTitle is the name people see for a connected tool:
// the one asked for, else the vendor's own (its MCP title, then its
// annotations' title), else the vendor tool's name in words. Never empty.
func toolTitle(asked, upstream string, remote *mcp.RemoteTool) (string, string) {
	if t := strings.TrimSpace(asked); t != "" {
		if len([]rune(t)) > maxTitle || strings.ContainsFunc(t, unicode.IsControl) {
			return "", "the title is at most 100 characters, without control characters"
		}
		return t, ""
	}
	if t := vendorTitle(*remote); t != "" && len([]rune(t)) <= maxTitle && !strings.ContainsFunc(t, unicode.IsControl) {
		return t, ""
	}
	return gateway.ToolTitle(upstream), ""
}

// vendorTitle is the name a vendor's tool gives people, if any.
func vendorTitle(t mcp.RemoteTool) string {
	if s := strings.TrimSpace(t.Title); s != "" {
		return s
	}
	if t.Annotations != nil {
		return strings.TrimSpace(t.Annotations.Title)
	}
	return ""
}
