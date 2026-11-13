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
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/kiff/cloud/gateway/internal/gateway"
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
	if msg := checkBatch(req.Connections); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
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
	toMake, kept, replaced, status, msg := h.planBatch(r.Context(), probeCtx, c, req.Connections, existing)
	if status == http.StatusInternalServerError {
		h.fail(w, r, "create batch", errors.New(msg))
		return
	}
	if status != 0 {
		writeErr(w, status, msg)
		return
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
	status = http.StatusOK
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

// previewBatch serves POST /v1/connections/batch/preview {connections,
// draft}: KIFF's words for an agent draft's Cards as they would read once
// the batch is connected, before anything is. A first start signs the batch
// and the draft together, so the owner reads the draft's Cards over tools
// not connected yet. Each connection is checked as the batch checks it
// (planBatch: its server probed), the contract the batch would apply is
// built as it would be (the account's tools, those it replaces swapped, the
// new ones added), and apps/api words the draft against it. A connection
// the batch would refuse is refused here in its words, and nothing is
// worded. Nothing is stored or connected, and no signature is needed:
// connecting still takes the batch itself.
func (h *Handler) previewBatch(w http.ResponseWriter, r *http.Request, c caller) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 512<<10+1))
	if err != nil || len(b) > 512<<10 {
		writeErr(w, http.StatusBadRequest, "the request body must be a JSON object with the documented fields")
		return
	}
	var req struct {
		Connections []batchConnection `json:"connections"`
		Draft       json.RawMessage   `json:"draft"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "the request body must be {connections, draft}: the batch, and the agent draft to word")
		return
	}
	if msg := checkBatch(req.Connections); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if len(bytes.TrimSpace(req.Draft)) == 0 || bytes.TrimSpace(req.Draft)[0] != '{' {
		writeErr(w, http.StatusBadRequest, "draft is the agent draft to word, as agent-drafts takes it")
		return
	}
	probeCtx := r.Context()
	if c.me.DeviceSession {
		var cancel context.CancelFunc
		probeCtx, cancel = context.WithTimeout(r.Context(), probeLimit)
		defer cancel()
	}
	probeCtx = withListCache(probeCtx)
	existing, err := h.Store.List(r.Context(), c.me.TenantID)
	if err != nil {
		h.fail(w, r, "preview batch", err)
		return
	}
	toMake, _, replaced, status, msg := h.planBatch(r.Context(), probeCtx, c, req.Connections, existing)
	if status == http.StatusInternalServerError {
		h.fail(w, r, "preview batch", errors.New(msg))
		return
	}
	if status != 0 {
		writeErr(w, status, msg)
		return
	}
	tools := make([]gateway.Tool, 0, len(existing)+len(toMake))
	for _, e := range existing {
		if !slices.Contains(replaced, e.Name) {
			tools = append(tools, toolOf(e))
		}
	}
	titles := map[string]string{}
	for _, conn := range toMake {
		tools = append(tools, toolOf(conn))
		if conn.Title != "" {
			titles[conn.Name] = conn.Title
		}
	}
	// By name, as the store lists them, so the contract reads as the one the batch then applies (the draft's
	// contract_digest, checked at submit, sorts on its own).
	slices.SortFunc(tools, func(a, b gateway.Tool) int { return strings.Compare(a.Name, b.Name) })
	y, err := gateway.Contract(tools)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "the batch's tools don't make a contract KIFF takes, so they would not be connected: "+err.Error())
		return
	}
	st, answer, err := h.API.PreviewDraft(r.Context(), c.key, req.Draft, y, titles)
	if err != nil && !errors.Is(err, kiffapi.ErrUnauthorized) {
		writeErr(w, http.StatusServiceUnavailable, "KIFF couldn't word the draft; try again")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(answer)
}

// checkBatch says what's wrong with a batch's size or its kind, or "".
// Only the account's own control server: a vendor's tool is connected one
// at a time, as the owner sets each one up.
func checkBatch(conns []batchConnection) string {
	if len(conns) == 0 || len(conns) > maxBatch {
		return "a batch connects between 1 and " + strconv.Itoa(maxBatch) + " tools"
	}
	for i := range conns {
		if !conns[i].Control {
			return "a batch connects only the account's control server: every connection has control: true"
		}
	}
	return ""
}

// planBatch checks each of a batch's connections as one alone (prepare,
// its server probed) against the account's tools there, and says what the
// batch would do: the connections to make, the names already there on its
// terms (kept), and those it takes over (replaced); else a status and why,
// 500 for a failure of the gateway's own. The batch and its preview share
// it, so a preview words exactly the tools the batch then connects.
func (h *Handler) planBatch(ctx, probeCtx context.Context, c caller, conns []batchConnection, existing []Connection) (toMake []Connection, kept, replaced []string, status int, msg string) {
	there := map[string]Connection{}
	for _, e := range existing {
		there[e.Name] = e
	}
	seen := map[string]bool{}
	for i := range conns {
		one := &conns[i]
		conn, status, msg := h.prepare(ctx, probeCtx, c, &one.createRequest)
		if status != 0 {
			if status == http.StatusInternalServerError {
				return nil, nil, nil, status, msg
			}
			return nil, nil, nil, status, "connection " + strconv.Itoa(i) + ": " + msg
		}
		if seen[conn.Name] {
			return nil, nil, nil, http.StatusBadRequest, "connection " + strconv.Itoa(i) + ": " + conn.Name + " is named twice in the batch"
		}
		seen[conn.Name] = true
		if e, ok := there[conn.Name]; ok {
			if sameConnection(e, conn) {
				kept = append(kept, conn.Name)
				continue
			}
			if !one.Replace || !e.Control || !conn.Control {
				return nil, nil, nil, http.StatusConflict, "a tool named " + conn.Name + " is already connected; remove it first, or choose another name"
			}
			replaced = append(replaced, conn.Name)
		}
		toMake = append(toMake, conn)
	}
	return toMake, kept, replaced, 0, ""
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
