package connection

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
)

// applyProfile applies a connection profile the owner approved (connection profiles: the owner's approval): only a connector key, only to the connections it made,
// and only when apps/api confirms the owner's signed approval covers this
// profile and KIFF's words for it. A key alone proposes, never applies
// (floor rule 5).
func (h *Handler) applyProfile(w http.ResponseWriter, r *http.Request, c caller) {
	if !c.me.IsConnector() || c.me.CredentialID == "" {
		writeErr(w, http.StatusForbidden, "a profile is applied by the connector key that connected its tools, with the owner's signed approval")
		return
	}
	var req struct {
		Profile  json.RawMessage `json:"profile"`
		Approval json.RawMessage `json:"approval"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 320<<10)).Decode(&req); err != nil || len(req.Profile) == 0 || len(req.Approval) == 0 {
		writeErr(w, http.StatusBadRequest, "send {profile, approval}")
		return
	}
	var ops struct {
		Operations []profileOp `json:"operations"`
	}
	_ = json.Unmarshal(req.Profile, &ops)
	a, err := h.API.CheckProfileApproval(r.Context(), c.key, req.Profile, req.Approval)
	switch {
	case errors.Is(err, kiffapi.ErrInvalidProfile):
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	case errors.Is(err, kiffapi.ErrUnavailable):
		writeErr(w, http.StatusServiceUnavailable, "KIFF couldn't check the approval; nothing was applied, try again")
		return
	case err != nil:
		h.fail(w, r, "profile approval", err)
		return
	case !a.Valid:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "approval_refused", "reason": a.Reason})
		return
	}
	signedAt, err := time.Parse(time.RFC3339, a.SignedAt)
	if err != nil {
		h.fail(w, r, "profile approval", err)
		return
	}
	tools := make([]string, len(ops.Operations))
	byTool := map[string]profileOp{}
	for i, o := range ops.Operations {
		tools[i] = o.Tool
		byTool[o.Tool] = o
	}
	rev, err := h.Store.ApplyProfile(r.Context(), c.me.TenantID, Profile{Raw: req.Profile, ProfileSHA256: a.ProfileSHA256,
		WordsSHA256: a.WordsSHA256, DeviceID: a.DeviceID, SignedAt: signedAt, CreatedBy: c.me.CredentialID}, tools,
		func(conn Connection) (string, error) { return byTool[conn.Name].fits(conn) })
	var mismatch errMismatch
	switch {
	case errors.Is(err, ErrNotOwnTool):
		writeErr(w, http.StatusForbidden, err.Error())
		return
	case errors.As(err, &mismatch):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "profile_mismatch", "detail": mismatch.Error()})
		return
	}
	if err != nil {
		h.fail(w, r, "apply profile", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": rev, "profile_sha256": a.ProfileSHA256, "words_sha256": a.WordsSHA256})
}

// profileOp is what the gateway reads of a profile's operation: what
// must match the connection it names (apps/api checked the rest).
type profileOp struct {
	Tool   string `json:"tool"`
	Kind   string `json:"kind"`
	Amount *struct {
		From     string       `json:"from"`
		Arg      string       `json:"arg"`
		Path     string       `json:"path"`
		Read     *profileRead `json:"read"`
		Format   string       `json:"format"`
		Scale    int          `json:"scale"`
		Unit     string       `json:"unit"`
		UnitRead *profileRead `json:"unit_read"`
	} `json:"amount"`
	Resource *struct {
		Arg  string `json:"arg"`
		Path string `json:"path"`
	} `json:"resource"`
}

// errMismatch: a profile's operation says something other than its
// connection does.
type errMismatch struct{ tool, what string }

func (e errMismatch) Error() string { return e.tool + ": " + e.what }

// fits checks that the connection says what the profile's operation does
// about reading, the amount and the resource, and returns the amount
// format the connection takes from it. A profile describes how the
// connection is counted; it never re-points what the Card limits.
func (o profileOp) fits(c Connection) (string, error) {
	no := func(what string) (string, error) { return "", errMismatch{c.Name, what} }
	if (o.Kind == "read") != c.ReadOnly {
		return no("the profile and the connection disagree on whether it only reads")
	}
	a := o.Amount
	switch {
	case a == nil && (c.AmountArg != "" || c.AmountLookup != nil):
		return no("the connection counts an amount the profile doesn't name")
	case a == nil:
	case a.From == "read" && !a.Read.sameAs(c.AmountLookup, c.ResourceArg),
		a.From == "argument" && (c.AmountArg != a.Arg || c.AmountPath != "" || c.AmountLookup != nil),
		a.From == "path" && (c.AmountPath != a.Path || c.AmountLookup != nil):
		return no("the amount is read from somewhere else than the connection says")
	case a.Scale != c.AmountScale:
		return no("the amount's scale differs from the connection's")
	case a.Unit != "" && a.Unit != c.AmountUnit, a.UnitRead != nil && !a.UnitRead.sameAs(c.UnitLookup, c.ResourceArg):
		return no("the amount's unit differs from the connection's")
	}
	r := o.Resource
	switch {
	case r == nil && c.ResourceArg != "":
		return no("the connection limits a resource the profile doesn't name")
	case r != nil && r.Arg != "" && (c.ResourceArg != r.Arg || c.ResourcePath != ""),
		r != nil && r.Path != "" && c.ResourcePath != r.Path:
		return no("the resource is read from somewhere else than the connection says")
	}
	if a != nil && (a.Format == gateway.FormatDecimal) != (c.AmountFormat == gateway.FormatDecimal) {
		return no("the amount's format differs from the connection's")
	}
	return c.AmountFormat, nil
}

// resume lets a paused operation send again (connection profiles: resuming an operation): the owner's, or
// an admin's, after looking at why its verify read paused it. A connector
// key never resumes what a check paused.
func (h *Handler) resume(w http.ResponseWriter, r *http.Request, c caller) {
	tag, err := h.Store.Pool.Exec(r.Context(), `UPDATE gateway.tools SET paused_reason = '', paused_at = NULL, resumed_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND name = $2`, c.me.TenantID, r.PathValue("name"))
	if err != nil {
		h.fail(w, r, "resume", err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "no tool by that name")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// profileRead is a profile's read of the resource: the amount or the unit.
type profileRead struct {
	Tool string            `json:"tool"`
	Args map[string]string `json:"args"`
	Path string            `json:"path"`
}

// sameAs reports whether the profile's read is the lookup the connection
// actually makes: the same read, the same result path, and the call's
// resource passed under the lookup's argument. Any other argument may only
// pass on the call's own argument of the same name, as the lookup does.
func (r *profileRead) sameAs(l *gateway.UnitLookup, resourceArg string) bool {
	if r == nil || l == nil || r.Tool != l.Tool || r.Path != l.Path || resourceArg == "" {
		return false
	}
	if r.Args[l.Argument] != "$args."+resourceArg {
		return false
	}
	for k, v := range r.Args {
		if k != l.Argument && v != "$args."+k {
			return false
		}
	}
	return true
}
