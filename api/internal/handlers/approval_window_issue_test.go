package handlers

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
)

// kiff/tafs#494: inside an approval window a tap reissues a Card only when
// the reissue widens nothing. A new Card (a holder's first one included), a
// reissue that raises an aggregate, or a second Card beside one in force is
// refused with critical_key_required: the approval key, behind the
// fingerprint. With the real handler.
func TestAWindowSignedReissueOfACardNeedsTheApprovalKey(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return now })
	windows := auth.NewMemoryWindowStore()
	inner := http.NewServeMux()
	NewMandatesHandler(store, stubDomainSource{yaml: mandateDomainYAML}, WithMandatesClock(func() time.Time { return now })).Register(inner)
	policed := auth.DefaultRoutePolicy().WithWindows(windows).WithClock(func() time.Time { return now }).Wrap(inner)
	key := func() (*ecdsa.PrivateKey, []byte) {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
		return k, der
	}
	deviceKey, deviceDER := key()
	approvalKey, approvalDER := key()
	phone := auth.Identity{Subject: "user-abc", Tenant: "tenant-1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"},
		DeviceKey: deviceDER, DeviceSessionID: "sess-phone", DeviceID: "dev_phone", ApprovalKey: approvalDER}
	sign := func(k *ecdsa.PrivateKey, path string, body []byte, window string) string {
		ts := now.Format(time.RFC3339)
		msg := auth.RequestMessage("tenant-1", http.MethodPost, path, body, ts)
		if window != "" {
			msg = auth.RequestMessageWindow("tenant-1", http.MethodPost, path, body, ts, window)
		}
		digest := sha256.Sum256(msg)
		sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
		if window != "" {
			return "t=" + ts + ",v2=" + base64.StdEncoding.EncodeToString(sig) + ",w=" + window
		}
		return "t=" + ts + ",v1=" + base64.StdEncoding.EncodeToString(sig)
	}
	post := func(path string, body []byte, header string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set(auth.DeviceSignatureHeader, header)
		rec := httptest.NewRecorder()
		policed.ServeHTTP(rec, req.WithContext(auth.WithIdentity(req.Context(), phone)))
		return rec.Code, rec.Body.String()
	}
	var cardFor func(id, subject string, limit int, n int) []byte
	card := func(id string, limit int, n int) []byte { return cardFor(id, "agent-a", limit, n) }
	cardFor = func(id, subject string, limit int, n int) []byte {
		b, _ := json.Marshal(map[string]any{
			"id": id, "subject": subject, "domain": "refund-control", "title": strings.Repeat("x", n),
			"grants":     []map[string]any{{"action": "AUTO_REFUND"}},
			"aggregates": []map[string]any{{"quantity": "sum(amount)", "limit": limit, "window": "calendar_day"}},
		})
		return b
	}
	// The window, opened with the approval key.
	opening, _ := json.Marshal(auth.OpenWindow{Message: auth.WindowMessage, Session: "sess-phone", Device: "dev_phone",
		Expires: now.Add(5 * time.Minute).Format(time.RFC3339), Nonce: strings.Repeat("n", 24)})
	win := auth.ApprovalWindow{ID: auth.NewWindowID(), Tenant: "tenant-1", Subject: "user-abc", DeviceID: "dev_phone", SessionID: "sess-phone",
		OpenedAt: now, ExpiresAt: now.Add(auth.WindowLength), CapAt: now.Add(auth.WindowCap), OpenedBy: auth.DeviceKeyFingerprint(approvalDER)}
	_ = opening
	if err := windows.Open(t.Context(), win); err != nil {
		t.Fatal(err)
	}
	// A new Card, inside the window: refused, nothing issued. Its words say why.
	if code, body := post("/v1/me/mandates", card("card-w", 500000, 1), sign(deviceKey, "/v1/me/mandates", card("card-w", 500000, 1), win.ID)); code != http.StatusForbidden ||
		!strings.Contains(body, "critical_key_required") || !strings.Contains(body, "new_card") {
		t.Fatalf("a new Card inside the window: %d %s", code, body)
	}
	if ms, _ := store.List(t.Context(), "tenant-1"); len(ms) != 0 {
		t.Fatalf("a tap issued a new Card: %+v", ms)
	}
	// The same new Card, with the approval key: issued.
	if code, body := post("/v1/me/mandates", card("card-w", 500000, 1), sign(approvalKey, "/v1/me/mandates", card("card-w", 500000, 1), "")); code != http.StatusCreated {
		t.Fatalf("a new Card with the approval key: %d %s", code, body)
	}
	// The same Card reissued with a higher limit, inside the window: refused, and the Card unchanged.
	if code, body := post("/v1/me/mandates", card("card-w", 900000, 2), sign(deviceKey, "/v1/me/mandates", card("card-w", 900000, 2), win.ID)); code != http.StatusForbidden || !strings.Contains(body, "critical_key_required") {
		t.Fatalf("a reissue inside the window: %d %s", code, body)
	}
	ms, _ := store.List(t.Context(), "tenant-1")
	if len(ms) != 1 || ms[0].Aggregates[0].Limit != 500000 {
		t.Fatalf("the Card after the refused reissue: %+v", ms)
	}
	// The widening rule: a reissue that only narrows is a tap.
	if code, body := post("/v1/me/mandates", card("card-w", 400000, 4), sign(deviceKey, "/v1/me/mandates", card("card-w", 400000, 4), win.ID)); code != http.StatusCreated {
		t.Fatalf("a narrowing reissue inside the window: %d %s", code, body)
	}
	// A second Card for a holder that holds one already adds authority
	// beside it: refused, nothing issued.
	if code, body := post("/v1/me/mandates", card("card-x", 9000000, 5), sign(deviceKey, "/v1/me/mandates", card("card-x", 9000000, 5), win.ID)); code != http.StatusForbidden || !strings.Contains(body, "new_card") {
		t.Fatalf("a second Card beside the first, inside the window: %d %s", code, body)
	}
	if ms, _ := store.List(t.Context(), "tenant-1"); len(ms) != 1 {
		t.Fatalf("a second Card was issued: %+v", ms)
	}
	// Another taf's first Card, inside the window: refused too.
	if code, body := post("/v1/me/mandates", cardFor("card-b", "agent-b", 500000, 5), sign(deviceKey, "/v1/me/mandates", cardFor("card-b", "agent-b", 500000, 5), win.ID)); code != http.StatusForbidden || !strings.Contains(body, "new_card") {
		t.Fatalf("another taf's first Card inside the window: %d %s", code, body)
	}
	// A phone session with no approval key (the CI test phone's): its device
	// key signs approvals, outside any window, as it always has; a new Card
	// is issued.
	keyless := phone
	keyless.ApprovalKey = nil
	req := httptest.NewRequest(http.MethodPost, "/v1/me/mandates", bytes.NewReader(cardFor("card-c", "agent-c", 500000, 6)))
	req.Header.Set(auth.DeviceSignatureHeader, sign(deviceKey, "/v1/me/mandates", cardFor("card-c", "agent-c", 500000, 6), ""))
	rec := httptest.NewRecorder()
	policed.ServeHTTP(rec, req.WithContext(auth.WithIdentity(req.Context(), keyless)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("a new Card from a session with no approval key: %d %s", rec.Code, rec.Body)
	}
	// The reissue with the approval key: as today.
	if code, body := post("/v1/me/mandates", card("card-w", 900000, 3), sign(approvalKey, "/v1/me/mandates", card("card-w", 900000, 3), "")); code != http.StatusCreated {
		t.Fatalf("a reissue with the approval key: %d %s", code, body)
	}
}
