package kiffapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// DeviceSigned hands apps/api the device's approval as it was sent: a
// phone's signature as `signature`, a computer's passkey assertion as
// `passkey` (never one as the other), and a 409 (already used) is
// ErrReplayed as well as ErrNotSigned.
func TestDeviceSignedSendsThePasskeyAsItsOwnField(t *testing.T) {
	var got map[string]string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"grant":"kdg_1"}`))
		} else {
			_, _ = w.Write([]byte(`{"error":"signature_replayed"}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, srv.Client())
	const pair = "t=2026-11-15T18:00:00Z,a=YXNzZXJ0,with=sha256:ab12"
	h := http.Header{}
	h.Set(PasskeyAssertionHeader, pair)
	grant, err := c.DeviceSigned(context.Background(), "k", "POST", "/v1/connections/batch", "ff", DeviceApprovalFrom(h))
	if err != nil || grant != "kdg_1" {
		t.Fatalf("passkey: %q %v", grant, err)
	}
	if got["passkey"] != pair || got["signature"] != "" {
		t.Fatalf("sent %v, want the assertion as passkey alone", got)
	}
	h = http.Header{}
	h.Set(DeviceSignatureHeader, "t=x,v1=sig")
	if _, err := c.DeviceSigned(context.Background(), "k", "POST", "/v1/connections", "ff", DeviceApprovalFrom(h)); err != nil || got["signature"] != "t=x,v1=sig" || got["passkey"] != "" {
		t.Fatalf("signature: sent %v, %v", got, err)
	}
	status = http.StatusConflict
	_, err = c.DeviceSigned(context.Background(), "k", "POST", "/v1/connections", "ff", DeviceApprovalFrom(h))
	if !errors.Is(err, ErrReplayed) || !errors.Is(err, ErrNotSigned) {
		t.Fatalf("409: %v, want ErrReplayed and ErrNotSigned", err)
	}
	status = http.StatusForbidden
	if _, err = c.DeviceSigned(context.Background(), "k", "POST", "/v1/connections", "ff", DeviceApprovalFrom(h)); errors.Is(err, ErrReplayed) || !errors.Is(err, ErrNotSigned) {
		t.Fatalf("403: %v, want ErrNotSigned alone", err)
	}
}
