package apiclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

func TestApproveDeviceBearer_HappyPath(t *testing.T) {
	t.Parallel()
	var gotAuth, gotCode, gotLabel string
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"POST /v1/auth/device/approve": func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			var body struct {
				UserCode string `json:"user_code"`
				Label    string `json:"label"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotCode, gotLabel = body.UserCode, body.Label
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"approved","user_code":"BCDF-GHJK"}`))
		},
	})
	c := newSessionClient(t, srv)
	if err := c.ApproveDeviceBearer(context.Background(), "abc", "BCDF-GHJK", "cli"); err != nil {
		t.Fatalf("ApproveDeviceBearer: %v", err)
	}
	if gotAuth != "Bearer abc" {
		t.Errorf("Authorization = %q, want Bearer abc", gotAuth)
	}
	if gotCode != "BCDF-GHJK" || gotLabel != "cli" {
		t.Errorf("body user_code/label = %q/%q", gotCode, gotLabel)
	}
}

func TestApproveDeviceBearer_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"POST /v1/auth/device/approve": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no", http.StatusUnauthorized)
		},
	})
	c := newSessionClient(t, srv)
	if err := c.ApproveDeviceBearer(context.Background(), "abc", "BCDF-GHJK", "cli"); !errors.Is(err, apiclient.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestApproveDeviceBearer_UnknownCode(t *testing.T) {
	t.Parallel()
	srv := fakeAPI(t, map[string]http.HandlerFunc{
		"POST /v1/auth/device/approve": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unknown or expired code", http.StatusNotFound)
		},
	})
	c := newSessionClient(t, srv)
	if err := c.ApproveDeviceBearer(context.Background(), "abc", "NOPE-NOPE", "cli"); !errors.Is(err, apiclient.ErrDeviceCodeUnknown) {
		t.Fatalf("err = %v, want ErrDeviceCodeUnknown", err)
	}
}

func TestApproveDeviceBearer_EmptyInputs(t *testing.T) {
	t.Parallel()
	c := newSessionClient(t, fakeAPI(t, map[string]http.HandlerFunc{}))
	if err := c.ApproveDeviceBearer(context.Background(), "", "code", "cli"); err == nil {
		t.Error("empty bearer should error")
	}
	if err := c.ApproveDeviceBearer(context.Background(), "abc", "", "cli"); err == nil {
		t.Error("empty user_code should error")
	}
}
