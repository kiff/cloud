package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
)

// The signed receipt is shown to read before it is downloaded: /signed
// renders the JSON indented in the dashboard shell and links the
// download, which still serves the exact bytes the API holds. A receipt
// not yet signed is not called signed: /signed sends you to the receipt
// page, and the download still serves it as it is.
func TestDashboardReceiptSignedThenDownload(t *testing.T) {
	t.Parallel()

	const raw = `{"trace_id":"settled-tc-1","attestation":{"signature":"0xab"}}`
	apiMux := http.NewServeMux()
	const unsigned = `{"trace_id":"decision-tc-2","attestation":null}`
	apiMux.HandleFunc("GET /v1/receipts/{id}", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{"settled-tc-1": raw, "decision-tc-2": unsigned}[r.PathValue("id")]
		if body == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	apiClient, err := apiclient.NewSessionAware(apiSrv.URL, 0)
	if err != nil {
		t.Fatalf("NewSessionAware: %v", err)
	}
	mgr := newTestSessionManager(t)
	dashMux := http.NewServeMux()
	handlers.NewDashboardHandler(apiClient, mgr, nil, nil).Register(dashMux)
	protected := handlers.RequireSession(mgr)(dashMux)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(receiptEventSession(t, mgr))
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/dashboard/receipts/settled-tc-1/signed")
	if rec.Code != http.StatusOK {
		t.Fatalf("signed view status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`class="dash-sidebar"`, "&#34;trace_id&#34;: &#34;settled-tc-1&#34;,\n  &#34;attestation&#34;: {",
		`href="/dashboard/receipts/settled-tc-1/download" download>Download</a>`, `href="/dashboard/receipts/settled-tc-1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("signed view lacks %q", want)
		}
	}
	if rec.Header().Get("Content-Disposition") != "" {
		t.Error("the signed view downloads instead of showing")
	}

	rec = get("/dashboard/receipts/settled-tc-1/download")
	if rec.Code != http.StatusOK || rec.Body.String() != raw || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") {
		t.Errorf("download = %d %q %q, want the exact bytes as an attachment", rec.Code, rec.Body.String(), rec.Header().Get("Content-Disposition"))
	}

	rec = get("/dashboard/receipts/decision-tc-2/signed")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/dashboard/receipts/decision-tc-2" || strings.Contains(rec.Body.String(), "signed it") {
		t.Errorf("unsigned signed view = %d to %q, want a redirect to the receipt page", rec.Code, rec.Header().Get("Location"))
	}
	if rec = get("/dashboard/receipts/decision-tc-2/download"); rec.Code != http.StatusOK || rec.Body.String() != unsigned {
		t.Errorf("unsigned download = %d %q, want the bytes as they are", rec.Code, rec.Body.String())
	}

	if rec = get("/dashboard/receipts/missing/signed"); rec.Code != http.StatusNotFound {
		t.Errorf("missing signed view status = %d, want 404", rec.Code)
	}
}
