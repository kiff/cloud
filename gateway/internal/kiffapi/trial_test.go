package kiffapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// kiff/tafs#173: whether a draft is being tried, as KIFF answers it. Only a
// draft KIFF says is trying sends its calls. A 4xx about the request (never
// tried, forbidden, another agent's) is a trial that only decides, as before
// owner-started trials. A 5xx, or a refused key, is an error, and then nothing is sent.
func TestTrialOpenReadsKIFFsAnswer(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		open   bool
		err    bool
	}{
		{200, `{"status":"trying"}`, true, false},
		{200, `{"status":"ended"}`, false, false},
		{404, `{"error":"this draft was never tried"}`, false, false},
		{403, `{"error":"forbidden"}`, false, false},
		{401, `{"error":"unauthorized"}`, false, true}, // a refused key sends nothing either way
		{500, `{"error":"boom"}`, false, true},
		{503, `{"error":"unavailable"}`, false, true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/v1/me/agent-drafts/drf_1/trial" {
				t.Errorf("request %s %s", r.Method, r.URL)
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		open, err := New(srv.URL, srv.Client()).TrialOpen(context.Background(), "key", "drf_1")
		srv.Close()
		if open != tc.open || (err != nil) != tc.err {
			t.Errorf("%d %s: open %v, err %v", tc.status, tc.body, open, err)
		}
	}
}
