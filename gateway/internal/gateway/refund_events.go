package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kiff/cloud/gateway/internal/thune"
)

// The connection service hands KIFF the app event a refund operation's
// case block names (case.refund_event): a refund's later state, pending,
// succeeded, failed or cancelled. It's the watch's main source, since the
// watch holds no authority to read on its own. KIFF reads only the refund
// from it, records it as any read of the charge is recorded, and acts on
// nothing else in it. Receiving one sends nothing and allows nothing.

// RefundEventsPath is where the connection service posts refund events.
const RefundEventsPath = "/v1/planque/refund-events"

// maxRefundEvent bounds an event's body (the connection caps a payload at
// 64 KB).
const maxRefundEvent = 256 << 10

// RegisterRefundEvents serves the refund events route when secret, the
// connection's signing secret for KIFF, is set.
func (g *Gateway) RegisterRefundEvents(mux *http.ServeMux, secret string) {
	if secret == "" || g.Thune == nil || g.Watch == nil {
		return
	}
	mux.HandleFunc("POST "+RefundEventsPath, func(w http.ResponseWriter, r *http.Request) { g.refundEvent(w, r, secret) })
}

// refundEventBody is the connection's planque.event.v1, as KIFF reads it.
type refundEventBody struct {
	ID      string          `json:"id"`
	Tenant  string          `json:"tenant"`
	Grant   string          `json:"grant"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
}

func (g *Gateway) refundEvent(w http.ResponseWriter, r *http.Request, secret string) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRefundEvent+1))
	if err != nil || len(raw) > maxRefundEvent {
		http.Error(w, "the event is over its limit", http.StatusRequestEntityTooLarge)
		return
	}
	id, ts := r.Header.Get("Planque-Event-Id"), r.Header.Get("Planque-Event-Timestamp")
	if !signedByConnection(secret, id, ts, r.Header.Get("Planque-Event-Signature"), raw, g.now()) {
		http.Error(w, "the event's signature is invalid or expired", http.StatusUnauthorized)
		return
	}
	var e refundEventBody
	if json.Unmarshal(raw, &e) != nil || e.ID != id || e.Tenant == "" || e.Grant == "" || e.Event == "" {
		// Malformed: not retried.
		http.Error(w, "the event isn't one KIFF reads", http.StatusUnprocessableEntity)
		return
	}
	ctx := r.Context()
	tools, err := g.Catalog.Tools(ctx, e.Tenant)
	if err != nil {
		http.Error(w, "KIFF can't read the account's tools now", http.StatusServiceUnavailable)
		return
	}
	var t *Tool
	for i := range tools {
		c := tools[i].Case
		if c != nil && c.RefundEvent != nil && c.RefundEvent.Event == e.Event && connectionGrant(tools[i], g.Relays, e.Tenant) == e.Grant {
			t = &tools[i]
			break
		}
	}
	if t == nil {
		// No refund operation on that connection names it: nothing to read.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	f, ok := refundFromEvent(*t.Case, e.Payload)
	if !ok {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	account, known, err := g.Thune.AccountOf(ctx, e.Tenant, f.Charge)
	if err != nil {
		http.Error(w, "KIFF can't read its refunds now", http.StatusServiceUnavailable)
		return
	}
	if !known {
		// A charge KIFF never decided a refund on: the next case reads it.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	f.Account = account
	// Recorded first, then the id kept: a failure is retried by the
	// connection, and recording twice changes nothing.
	if err := g.Watch.Settle(ctx, e.Tenant, f); err != nil {
		if g.Logger != nil {
			g.Logger.WarnContext(ctx, "gateway: a refund event wasn't recorded", slog.String("event", e.ID), slog.String("err", err.Error()))
		}
		http.Error(w, "KIFF couldn't record the event now", http.StatusServiceUnavailable)
		return
	}
	if _, err := g.Thune.RecordEvent(ctx, e.Tenant, e.ID); err != nil && g.Logger != nil {
		g.Logger.WarnContext(ctx, "gateway: a refund event's id wasn't kept", slog.String("event", e.ID), slog.String("err", err.Error()))
	}
	w.WriteHeader(http.StatusAccepted)
}

// refundFromEvent is the one refund an event carries, as the charge's
// facts thune records.
func refundFromEvent(c CaseProfile, payload json.RawMessage) (thune.Facts, bool) {
	dec := json.NewDecoder(strings.NewReader(string(payload)))
	dec.UseNumber()
	var doc any
	if dec.Decode(&doc) != nil {
		return thune.Facts{}, false
	}
	obj, ok := valueAt(doc, c.RefundEvent.Object)
	if !ok {
		return thune.Facts{}, false
	}
	charge, _ := valueAt(obj, c.RefundEvent.Charge)
	id, _ := valueAt(obj, c.RefundsRead.ID)
	amt, _ := valueAt(obj, c.RefundsRead.Amount)
	st, _ := valueAt(obj, c.RefundsRead.Status)
	marker, _ := valueAt(obj, c.RefundsRead.Marker)
	cs, isCharge := charge.(string)
	ids, isID := id.(string)
	n, whole := wholeAmount(amt)
	if !isCharge || cs == "" || !isID || ids == "" || !whole || st == nil {
		return thune.Facts{}, false
	}
	m, _ := marker.(string)
	return thune.Facts{Charge: cs, Refunds: []thune.Refund{{ID: ids, Amount: n, Status: refundStatus(fmt.Sprint(st)), Marker: m}}}, true
}

// signedByConnection checks the connection's signature: v1,<base64 HMAC-
// SHA256 of "<id>.<timestamp>.<body>">, within five minutes of now.
func signedByConnection(secret, id, timestamp, signature string, body []byte, now time.Time) bool {
	if secret == "" || id == "" || len(id) > 256 || len(signature) > 2048 {
		return false
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || strconv.FormatInt(ts, 10) != timestamp {
		return false
	}
	if at := time.Unix(ts, 0); at.Before(now.Add(-5*time.Minute)) || at.After(now.Add(5*time.Minute)) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, candidate := range strings.Fields(signature) {
		if enc, ok := strings.CutPrefix(candidate, "v1,"); ok {
			if got, err := base64.StdEncoding.DecodeString(enc); err == nil && hmac.Equal(got, want) {
				return true
			}
		}
	}
	return false
}
