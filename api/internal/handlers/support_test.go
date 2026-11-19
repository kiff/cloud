package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/support"
)

// supportMux wires a SupportHandler over a shared store, scoped to one
// fake identity (tenant + subject + email). Two muxes over the same
// store model two tenants for the cross-tenant isolation test.
func supportMux(t *testing.T, store support.Store, tenantID, subject, email string) http.Handler {
	t.Helper()
	h := handlers.NewSupportHandler(store)
	mux := http.NewServeMux()
	h.Register(mux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(mux, auth.Identity{Tenant: tenantID, Subject: subject, Email: email}))
	return wrapped
}

func doJSON(t *testing.T, mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func TestSupport_CreateListGetReply(t *testing.T) {
	t.Parallel()
	store := support.NewMemoryStore()
	mux := supportMux(t, store, "t1", "alice", "alice@example.com")

	// Create.
	rec := doJSON(t, mux, http.MethodPost, "/v1/me/support/tickets",
		`{"title":"Activate step unclear","body":"I got stuck on activate.","category":"bug"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var created support.Ticket
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.ID == "" || created.Status != support.StatusOpen || created.AuthorEmail != "alice@example.com" {
		t.Fatalf("created ticket wrong: %+v", created)
	}

	// List.
	rec = doJSON(t, mux, http.MethodGet, "/v1/me/support/tickets", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	var listBody struct {
		Tickets []support.Ticket `json:"tickets"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listBody)
	if len(listBody.Tickets) != 1 || listBody.Tickets[0].ID != created.ID {
		t.Fatalf("list = %+v, want the created ticket", listBody.Tickets)
	}

	// Reply (user).
	rec = doJSON(t, mux, http.MethodPost, "/v1/me/support/tickets/"+created.ID+"/reply",
		`{"body":"still stuck, any update?"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	// Get: ticket + one message.
	rec = doJSON(t, mux, http.MethodGet, "/v1/me/support/tickets/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}
	var detail struct {
		Ticket   support.Ticket    `json:"ticket"`
		Messages []support.Message `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if len(detail.Messages) != 1 || detail.Messages[0].AuthorKind != support.AuthorUser {
		t.Fatalf("detail messages = %+v, want one user message", detail.Messages)
	}
}

func TestSupport_CrossTenantIsNotFound(t *testing.T) {
	t.Parallel()
	store := support.NewMemoryStore()
	t1 := supportMux(t, store, "t1", "alice", "alice@example.com")
	t2 := supportMux(t, store, "t2", "bob", "bob@example.com")

	rec := doJSON(t, t1, http.MethodPost, "/v1/me/support/tickets", `{"title":"x","body":"y"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	var tk support.Ticket
	_ = json.Unmarshal(rec.Body.Bytes(), &tk)

	// t2 must not see or reply to t1's ticket.
	if rec := doJSON(t, t2, http.MethodGet, "/v1/me/support/tickets/"+tk.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant get = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, t2, http.MethodPost, "/v1/me/support/tickets/"+tk.ID+"/reply", `{"body":"sneaky"}`); rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant reply = %d, want 404", rec.Code)
	}
	// t2's own list stays empty.
	rec = doJSON(t, t2, http.MethodGet, "/v1/me/support/tickets", "")
	var body struct {
		Tickets []support.Ticket `json:"tickets"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Tickets) != 0 {
		t.Errorf("t2 list = %d, want 0", len(body.Tickets))
	}
}

func TestSupport_Validation(t *testing.T) {
	t.Parallel()
	store := support.NewMemoryStore()
	mux := supportMux(t, store, "t1", "alice", "alice@example.com")

	// Missing title/body.
	if rec := doJSON(t, mux, http.MethodPost, "/v1/me/support/tickets", `{"title":"","body":""}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty create = %d, want 400", rec.Code)
	}
	// Empty reply body on a real ticket.
	rec := doJSON(t, mux, http.MethodPost, "/v1/me/support/tickets", `{"title":"t","body":"b"}`)
	var tk support.Ticket
	_ = json.Unmarshal(rec.Body.Bytes(), &tk)
	if rec := doJSON(t, mux, http.MethodPost, "/v1/me/support/tickets/"+tk.ID+"/reply", `{"body":"  "}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty reply = %d, want 400", rec.Code)
	}
}
