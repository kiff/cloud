package connection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/kiff/cloud/gateway/internal/gateway"
)

// RelayFields are the fields a vendor relay owns on a tool it registered:
// its app, the vendor's destructive_hint and what the relay pins in the
// vendor's body. A relay's PATCH says all three, every time, so nothing is
// "unchanged by omission".
type RelayFields struct {
	App             *string              `json:"app"`
	DestructiveHint *bool                `json:"destructive_hint"`
	VendorPins      *[]gateway.VendorPin `json:"vendor_pins"`
}

type relayState struct {
	App             string              `json:"app"`
	DestructiveHint bool                `json:"destructive_hint"`
	VendorPins      []gateway.VendorPin `json:"vendor_pins"`
}

var (
	errNotRelayTool = errors.New("connection: not a relay's tool")
	errLowers       = errors.New("connection: the change would lower the tool's class")
)

// patchRelay brings a relay's tool up to what the relay registers today
// (K4's app and destructive_hint, step 3's vendor_pins), in place: no Card,
// held call or owner's tap changes. Only the credential that registered the
// tool may, and only for a tool whose stored URL is on a relay's host. It
// never lowers a tool's class: an app may be given to a tool kept with none,
// only one whose reviewed table names the tool, and never changed after;
// the destructive_hint may only be set. A pin may be added, changed or
// removed (a removal is logged), and every PATCH is recorded beside the
// tool, old and new values, with no call's contents.
func (h *Handler) patchRelay(w http.ResponseWriter, r *http.Request, c caller) {
	var req RelayFields
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "send {\"app\", \"destructive_hint\", \"vendor_pins\"}, all three and nothing else")
		return
	}
	if req.App == nil || req.DestructiveHint == nil || req.VendorPins == nil {
		writeErr(w, http.StatusBadRequest, "a relay's change says app, destructive_hint and vendor_pins, all three")
		return
	}
	if msg := gateway.VendorPinsRefusal(*req.VendorPins); msg != "" {
		writeErr(w, http.StatusUnprocessableEntity, msg)
		return
	}
	name := r.PathValue("name")
	var old, next relayState
	var why, vendor string
	err := h.Store.Change(r.Context(), c.me.TenantID, func(tx *Tx) error {
		conn, err := tx.Get(r.Context(), name)
		if err != nil {
			return err
		}
		// The credential that registered it, never a person's session or
		// another key; and only a relay's tool, by its stored URL.
		if c.me.CredentialID == "" || conn.CreatedBy != c.me.CredentialID {
			return errNotYours
		}
		if !h.onRelay(conn.URL) {
			return errNotRelayTool
		}
		old = relayState{App: conn.App, DestructiveHint: conn.DestructiveHint, VendorPins: conn.VendorPins}
		next = relayState{App: appSlug(*req.App), DestructiveHint: *req.DestructiveHint, VendorPins: *req.VendorPins}
		vendor = conn.VendorTool
		if vendor == "" {
			vendor = conn.UpstreamTool
		}
		switch {
		case next.App != old.App && (old.App != "" || !gateway.TableNames(next.App, vendor)):
			why = fmt.Sprintf("app %q to %q", old.App, next.App)
			return errLowers
		case old.DestructiveHint && !next.DestructiveHint:
			why = "destructive_hint true to false"
			return errLowers
		}
		if old.App == next.App && old.DestructiveHint == next.DestructiveHint && samePins(old.VendorPins, next.VendorPins) {
			return nil
		}
		return tx.SetRelay(r.Context(), name, next, old, c.me.CredentialID)
	}, h.applier(r.Context(), c))
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "no tool named "+name+" is connected")
		return
	case errors.Is(err, errNotYours):
		writeErr(w, http.StatusForbidden, "only the key that registered a tool may change what its relay says of it")
		return
	case errors.Is(err, errNotRelayTool):
		writeErr(w, http.StatusConflict, "this tool isn't a relay's: what KIFF keeps of it changes only in KIFF")
		return
	case errors.Is(err, errLowers):
		if h.Logger != nil {
			h.Logger.WarnContext(r.Context(), "connections: a relay's change that would lower a tool's class was refused",
				slog.String("tenant", c.me.TenantID), slog.String("tool", name), slog.String("change", why))
		}
		writeErr(w, http.StatusConflict, "this change would lower the tool's class ("+why+"), so nothing changed")
		return
	}
	var ce contractError
	if errors.As(err, &ce) {
		writeErr(w, http.StatusUnprocessableEntity, "KIFF could not update the account's tool contract, so the tool was not changed: "+ce.Error())
		return
	}
	if err != nil {
		h.fail(w, r, "patch", err)
		return
	}
	for _, p := range removedPins(old.VendorPins, next.VendorPins) {
		if h.Logger != nil {
			h.Logger.WarnContext(r.Context(), "connections: a relay stopped pinning a vendor path",
				slog.String("tenant", c.me.TenantID), slog.String("tool", name), slog.String("path", p))
		}
	}
	if h.ClassLog && h.Logger != nil {
		before := gateway.Tool{Name: name, VendorTool: vendor, App: old.App, DestructiveHint: old.DestructiveHint}.Class()
		after := gateway.Tool{Name: name, VendorTool: vendor, App: next.App, DestructiveHint: next.DestructiveHint}.Class()
		if before != after {
			h.Logger.InfoContext(r.Context(), "connections: a relay's change moved a tool's class",
				slog.String("tenant", c.me.TenantID), slog.String("tool", name), slog.String("from", string(before)), slog.String("to", string(after)))
		}
	}
	h.log(r, "relay fields updated", c, name)
	w.WriteHeader(http.StatusNoContent)
}

func samePins(a, b []gateway.VendorPin) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return string(x) == string(y)
}

// removedPins are the paths a, and not b, pins.
func removedPins(a, b []gateway.VendorPin) []string {
	kept := map[string]bool{}
	for _, p := range b {
		kept[p.Path] = true
	}
	var out []string
	for _, p := range a {
		if !kept[p.Path] {
			out = append(out, p.Path)
		}
	}
	return out
}

// SetRelay keeps a relay's fields on its tool, and the change beside it.
func (t *Tx) SetRelay(ctx context.Context, name string, next, old relayState, keyID string) error {
	tag, err := t.tx.Exec(ctx, `UPDATE gateway.tools SET app = $3, destructive_hint = $4, vendor_pins = $5, updated_at = now()
		WHERE tenant_id = $1 AND name = $2`, t.tenantID, name, next.App, next.DestructiveHint, pinsJSON(next.VendorPins))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	o, _ := json.Marshal(old)
	n, _ := json.Marshal(next)
	_, err = t.tx.Exec(ctx, `INSERT INTO gateway.tool_patches (tenant_id, name, key_id, old, new) VALUES ($1, $2, $3, $4, $5)`,
		t.tenantID, name, keyID, o, n)
	return err
}
