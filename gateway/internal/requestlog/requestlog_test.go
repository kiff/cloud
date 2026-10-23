package requestlog_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/requestlog"
)

func entries(t *testing.T, b *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(b)
	for dec.More() {
		var row map[string]any
		if err := dec.Decode(&row); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	return out
}

func TestRequestLogStatusAndCorrelation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		serve  http.HandlerFunc
		status int
	}{
		{"implicit", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }, 200},
		{"empty", func(http.ResponseWriter, *http.Request) {}, 200},
		{"notification", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(202) }, 202},
		{"refused", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401); w.WriteHeader(500) }, 401},
		{"failure", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "private error", 500) }, 500},
		{"flush", func(w http.ResponseWriter, _ *http.Request) {
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Error(err)
			}
		}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			logger := slog.New(requestlog.Handler(slog.NewJSONHandler(&b, nil)))
			var ids []string
			mux := http.NewServeMux()
			mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
				ids = append(ids, requestlog.ID(r.Context()))
				logger.With("component", "mcp").InfoContext(r.Context(), "inside request")
				tc.serve(w, r)
			})
			h := requestlog.Wrap(logger, mux)
			for i := 0; i < 2; i++ {
				req := httptest.NewRequest("POST", "/mcp", nil)
				req.Header.Set(requestlog.Header, "client-controlled-secret")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if w.Code != tc.status || w.Header().Get(requestlog.Header) != ids[i] {
					t.Fatalf("status/header: %d %v", w.Code, w.Header())
				}
				if raw, err := hex.DecodeString(ids[i]); err != nil || len(raw) != 16 {
					t.Fatalf("bad generated request ID: %q", ids[i])
				}
			}
			if ids[0] == ids[1] {
				t.Fatal("request IDs were reused")
			}
			for i, row := range entries(t, &b) {
				if row["request_id"] != ids[i/2] {
					t.Fatalf("uncorrelated log: %v", row)
				}
				if row["msg"] == "gateway request" {
					if row["status"] != float64(tc.status) || row["path"] != "/mcp" || row["identity_type"] != "anonymous" || row["completed"] != true {
						t.Fatalf("wrong summary: %v", row)
					}
					if row["time"] == nil || row["duration_ms"].(float64) < 0 {
						t.Fatalf("missing time/duration: %v", row)
					}
				}
			}
		})
	}
}

func TestRequestLogNeverIncludesClientContents(t *testing.T) {
	const secret = "private-key-token-and-user-data"
	var b bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&b, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /a/{id}/mcp", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != secret {
			t.Fatal("logging middleware changed or consumed the body")
		}
		requestlog.Authenticated(r.Context(), "agent", secret, secret)
		requestlog.Operation(r.Context(), secret)
		http.Error(w, secret, 403)
	})
	req := httptest.NewRequest("POST", "/a/"+secret+"/mcp?code="+secret, strings.NewReader(secret))
	for _, h := range []string{"Authorization", "Cookie", "User-Agent", requestlog.Header, "Mcp-Method", "Mcp-Name"} {
		req.Header.Set(h, secret)
	}
	requestlog.Wrap(logger, mux).ServeHTTP(httptest.NewRecorder(), req)
	if strings.Contains(b.String(), secret) {
		t.Fatalf("sensitive input leaked: %s", b.String())
	}
	row := entries(t, &b)[0]
	if row["path"] != "/a/{id}/mcp" || row["mcp_operation"] != "other" || row["identity_type"] != "agent" || row["tenant_ref"] == nil || row["agent_ref"] == nil {
		t.Fatalf("wrong sanitized summary: %v", row)
	}
}

func TestUnmatchedPathAndUnknownHTTPMethodAreNotLogged(t *testing.T) {
	const secret = "sensitive-path-or-method"
	var b bytes.Buffer
	req := httptest.NewRequest(secret, "/"+secret+"?token="+secret, nil)
	requestlog.Wrap(slog.New(slog.NewJSONHandler(&b, nil)), http.NewServeMux()).ServeHTTP(httptest.NewRecorder(), req)
	if strings.Contains(b.String(), secret) {
		t.Fatal("unknown path or HTTP method was logged")
	}
	row := entries(t, &b)[0]
	if row["path"] != "unmatched" || row["method"] != "other" || row["status"] != float64(404) {
		t.Fatalf("wrong unmatched request: %v", row)
	}
}

func TestReferencesAreStableAndAgentReferencesAreTenantScoped(t *testing.T) {
	var b bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&b, nil))
	for _, identity := range []struct{ tenant, agent string }{
		{"tenant-a", "agent-a"}, {"tenant-a", "agent-a"},
		{"tenant-a", "agent-b"}, {"tenant-b", "agent-a"},
	} {
		h := requestlog.Wrap(logger, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			requestlog.Authenticated(r.Context(), "agent", identity.tenant, identity.agent)
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/mcp", nil))
	}
	rows := entries(t, &b)
	if rows[0]["tenant_ref"] != rows[1]["tenant_ref"] || rows[0]["agent_ref"] != rows[1]["agent_ref"] {
		t.Fatal("identity references changed between requests")
	}
	if rows[0]["tenant_ref"] != rows[2]["tenant_ref"] || rows[0]["agent_ref"] == rows[2]["agent_ref"] {
		t.Fatal("different agents in one tenant were conflated")
	}
	if rows[0]["tenant_ref"] == rows[3]["tenant_ref"] || rows[0]["agent_ref"] == rows[3]["agent_ref"] {
		t.Fatal("agent reference was not scoped to its tenant")
	}
	for _, row := range rows {
		for _, field := range []string{"tenant_ref", "agent_ref"} {
			if raw, err := hex.DecodeString(row[field].(string)); err != nil || len(raw) != 16 {
				t.Fatalf("invalid reference: %v", row)
			}
		}
	}
}

func TestPanicIsLoggedWithoutItsPayloadAndPropagates(t *testing.T) {
	var b bytes.Buffer
	h := requestlog.Wrap(slog.New(slog.NewJSONHandler(&b, nil)), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("private panic payload") }))
	func() {
		defer func() {
			if recover() == nil {
				t.Error("logging swallowed the panic")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/mcp", nil))
	}()
	if strings.Contains(b.String(), "private panic payload") {
		t.Fatal("panic payload leaked")
	}
	row := entries(t, &b)[0]
	if row["completed"] != false || row["status"] != float64(0) {
		t.Fatalf("invented a returned status on panic: %v", row)
	}
}

func TestInformationalResponseDoesNotReplaceFinalStatus(t *testing.T) {
	var b bytes.Buffer
	server := httptest.NewServer(requestlog.Wrap(slog.New(slog.NewJSONHandler(&b, nil)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(103)
		w.WriteHeader(201)
	})))
	defer server.Close()
	resp, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	server.Close() // wait for the request log before reading the buffer
	if resp.StatusCode != 201 || entries(t, &b)[0]["status"] != float64(201) {
		t.Fatal("logged the informational response instead of the final response")
	}
}
