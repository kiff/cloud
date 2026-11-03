package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"

	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// maxBatch is the most connections one batch makes: Coach's seven, with room.
const maxBatch = 20

type batchRequest struct {
	Connections []batchConnection `json:"connections"`
}

// batchConnection is one connection of a batch. Replace takes the name over
// from a connection already there on other terms, only when both are the
// account's control server (tafs cloud's tools at an address it no longer
// has, after a move): a vendor's tool is never replaced this way.
type batchConnection struct {
	createRequest
	Replace bool `json:"replace"`
}

// createBatch connects several of the account's control-server tools in one
// request (batch connections): Coach's tools, which tafs cloud lists and the
// owner's phone signs once. Each is checked exactly as create checks one
// (prepare), and they're made in one change with one contract write, so a
// failure connects none. A name already connected with the same terms is
// left as it is, so sending the batch again is safe; a name another
// connection holds refuses the whole batch, unless the batch marks it
// replace and both are the account's control server, when it's replaced in
// the same change.
func (h *Handler) createBatch(w http.ResponseWriter, r *http.Request, c caller) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 256<<10+1))
	if err != nil || len(b) > 256<<10 {
		writeErr(w, http.StatusBadRequest, "the request body must be a JSON object with the documented fields")
		return
	}
	var req batchRequest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "the request body must be a JSON object with the documented fields")
		return
	}
	if len(req.Connections) == 0 || len(req.Connections) > maxBatch {
		writeErr(w, http.StatusBadRequest, "a batch connects between 1 and "+strconv.Itoa(maxBatch)+" tools")
		return
	}
	// Only the account's own control server: a vendor's tool is connected
	// one at a time, as the owner sets each one up.
	for i := range req.Connections {
		if !req.Connections[i].Control {
			writeErr(w, http.StatusBadRequest, "a batch connects only the account's control server: every connection has control: true")
			return
		}
	}
	if c.me.DeviceSession {
		// One signature over the whole batch, the owner's act behind their
		// confirmation, as for one connection.
		sum := sha256.Sum256(b)
		grant, err := h.API.DeviceSigned(r.Context(), c.key, r.Method, r.URL.RequestURI(), hex.EncodeToString(sum[:]), r.Header.Get(kiffapi.DeviceSignatureHeader))
		switch {
		case errors.Is(err, kiffapi.ErrUnavailable):
			writeErr(w, http.StatusServiceUnavailable, "KIFF couldn't check the phone's signature; sign it again and retry")
			return
		case err != nil:
			writeErr(w, http.StatusForbidden, "from a phone, connecting the account's control server needs the phone's signature over this request ("+kiffapi.DeviceSignatureHeader+")")
			return
		}
		r = r.WithContext(kiffapi.WithDeviceGrant(r.Context(), grant))
	}
	probeCtx := r.Context()
	if c.me.DeviceSession {
		var cancel context.CancelFunc
		probeCtx, cancel = context.WithTimeout(r.Context(), probeLimit)
		defer cancel()
	}
	// Each server is listed once for the whole batch: Coach's tools share
	// one, and every listing spends the phone's grant.
	probeCtx = withListCache(probeCtx)
	existing, err := h.Store.List(r.Context(), c.me.TenantID)
	if err != nil {
		h.fail(w, r, "create batch", err)
		return
	}
	there := map[string]Connection{}
	for _, e := range existing {
		there[e.Name] = e
	}
	var toMake []Connection
	var kept, replaced []string
	seen := map[string]bool{}
	for i := range req.Connections {
		one := &req.Connections[i]
		conn, status, msg := h.prepare(r.Context(), probeCtx, c, &one.createRequest)
		if status != 0 {
			if status == http.StatusInternalServerError {
				h.fail(w, r, "create batch", errors.New(msg))
				return
			}
			writeErr(w, status, "connection "+strconv.Itoa(i)+": "+msg)
			return
		}
		if seen[conn.Name] {
			writeErr(w, http.StatusBadRequest, "connection "+strconv.Itoa(i)+": "+conn.Name+" is named twice in the batch")
			return
		}
		seen[conn.Name] = true
		if e, ok := there[conn.Name]; ok {
			if sameConnection(e, conn) {
				kept = append(kept, conn.Name)
				continue
			}
			if !one.Replace || !e.Control || !conn.Control {
				writeErr(w, http.StatusConflict, "a tool named "+conn.Name+" is already connected; remove it first, or choose another name")
				return
			}
			replaced = append(replaced, conn.Name)
		}
		toMake = append(toMake, conn)
	}
	made := make([]string, 0, len(toMake))
	if len(toMake) > 0 {
		err = h.Store.Change(r.Context(), c.me.TenantID, func(tx *Tx) error {
			for _, name := range replaced {
				if err := tx.Delete(r.Context(), name); err != nil {
					return err
				}
			}
			for _, conn := range toMake {
				if err := tx.Insert(r.Context(), conn, ""); err != nil {
					return err
				}
			}
			return nil
		}, h.applier(r.Context(), c))
		if errors.Is(err, ErrExists) {
			writeErr(w, http.StatusConflict, "a tool in the batch was connected meanwhile; send the batch again")
			return
		}
		var ce contractError
		if errors.As(err, &ce) {
			writeErr(w, http.StatusUnprocessableEntity, "KIFF could not update the account's tool contract, so no tool was connected: "+ce.Error())
			return
		}
		if err != nil {
			h.fail(w, r, "create batch", err)
			return
		}
		for _, conn := range toMake {
			made = append(made, conn.Name)
			h.log(r, "tool connected", c, conn.Name, slog.Bool("batch", true))
		}
	}
	status := http.StatusOK
	if len(made) > 0 {
		status = http.StatusCreated
	}
	if kept == nil {
		kept = []string{}
	}
	if replaced == nil {
		replaced = []string{}
	}
	writeJSON(w, status, map[string]any{"connected": made, "already": kept, "replaced": replaced})
}

// sameConnection reports whether a connection already there has the terms
// a batch asks for, so sending the batch again leaves it as it is.
func sameConnection(there, want Connection) bool {
	return there.URL == want.URL && there.UpstreamTool == want.UpstreamTool && there.Control == want.Control &&
		modeOrSealed(there.Mode) == modeOrSealed(want.Mode) && there.Audience == want.Audience &&
		there.ReadOnly == want.ReadOnly && there.IdempotencyArg == want.IdempotencyArg
}

// listCache keeps a batch's tool listings, by server and credential.
type listCache struct {
	mu    sync.Mutex
	lists map[[2]string][]mcp.RemoteTool
}

type listCacheKey struct{}

func withListCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, listCacheKey{}, &listCache{lists: map[[2]string][]mcp.RemoteTool{}})
}

// listTools lists a server's tools, once per batch when ctx carries its
// cache; a failed listing isn't kept.
func (h *Handler) listTools(ctx context.Context, url, credential string) ([]mcp.RemoteTool, error) {
	cache, _ := ctx.Value(listCacheKey{}).(*listCache)
	if cache == nil {
		return h.Remote.ListTools(ctx, url, credential)
	}
	key := [2]string{url, credential}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if tools, ok := cache.lists[key]; ok {
		return tools, nil
	}
	tools, err := h.Remote.ListTools(ctx, url, credential)
	if err == nil {
		cache.lists[key] = tools
	}
	return tools, err
}
