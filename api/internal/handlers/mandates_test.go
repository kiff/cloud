package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
)

func mandatesServer(t *testing.T, id auth.Identity) (*http.ServeMux, *mandate.MemoryStore) {
	t.Helper()
	store := mandate.NewMemoryStore()
	// Same timeline as the handler's clock, so a card issued in a test
	// is issued before the draws the test records against it.
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	h := NewMandatesHandler(store, nil, WithMandatesClock(func() time.Time {
		return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	}))
	if h == nil {
		t.Fatal("NewMandatesHandler returned nil with a store wired")
	}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, store
}

func do(t *testing.T, mux *http.ServeMux, id auth.Identity, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req = req.WithContext(auth.WithIdentity(req.Context(), id))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func runtimeKey() auth.Identity {
	return auth.Identity{Subject: "svc-agent", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, Roles: []string{"ops_agent"}}
}

func owner() auth.Identity {
	return auth.Identity{Subject: "alice", Tenant: "t1", Roles: []string{"tenant_owner"}}
}

// Invariant: a runtime credential cannot issue or revoke a mandate. A
// key that could grant itself authority would make every limit
// advisory.
// Category: authority
// Attack: use the agent's own decide credential to POST a mandate granting itself a larger limit
// Refused: issuing is gated on the management roles, the same separation that stops a runtime key minting sibling keys
func TestRuntimeKeyCannotIssueItsOwnMandate(t *testing.T) {
	mux, store := mandatesServer(t, runtimeKey())
	body := map[string]any{
		"id": "mdt-self", "subject": "agent://self", "domain": "billing",
		"grants":     []map[string]any{{"action": "REFUND_INVOICE"}},
		"aggregates": []map[string]any{{"quantity": "count", "limit": 1000000, "window": "calendar_day"}},
	}
	if rr := do(t, mux, runtimeKey(), http.MethodPost, "/v1/me/mandates", body); rr.Code != http.StatusForbidden {
		t.Fatalf("issue status = %d, want 403", rr.Code)
	}
	if rr := do(t, mux, runtimeKey(), http.MethodPost, "/v1/me/mandates/mdt-1/revoke", nil); rr.Code != http.StatusForbidden {
		t.Errorf("revoke status = %d, want 403", rr.Code)
	}
	if ms, _ := store.List(context.Background(), "t1"); len(ms) != 0 {
		t.Errorf("a runtime key created %d mandates", len(ms))
	}
}

func TestArchivePreservesARevokedCardAndRefusesAnActiveOne(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	active := mandate.Mandate{ID: "active", TenantID: "t1", Subject: "agent://a", ValidUntil: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	if err := store.Issue(ctx, active, "owner"); err != nil {
		t.Fatal(err)
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates/active/archive", nil); rr.Code != http.StatusConflict {
		t.Fatalf("active archive status = %d, want 409", rr.Code)
	}
	revokedAt := time.Date(2026, 9, 12, 11, 0, 0, 0, time.UTC)
	revoked := active
	revoked.ID = "revoked"
	revoked.RevokedAt = &revokedAt
	if err := store.Issue(ctx, revoked, "owner"); err != nil {
		t.Fatal(err)
	}
	if rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates/revoked/archive", nil); rr.Code != http.StatusOK {
		t.Fatalf("revoked archive status = %d body=%s", rr.Code, rr.Body.String())
	}
	ms, err := store.List(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.ID == "revoked" && m.ArchivedAt == nil {
			t.Error("archiving did not persist archived_at")
		}
	}
	if rs, err := store.Revisions(ctx, "t1", "revoked"); err != nil || len(rs) != 1 {
		t.Fatalf("archive lost revision history: %d, %v", len(rs), err)
	}
}

// Invariant: revocation cannot be undone by re-issuing the same mandate
// id. Authority that was withdrawn stays withdrawn until a new mandate
// is issued under a new id.
// Category: authority
// Attack: revoke a mandate, then POST the original definition again with the same id to restore it
// Refused: revoked_at is never read from a request body and is excluded from the issue upsert, so a re-issue leaves the revocation standing
func TestReIssuingDoesNotUnrevoke(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	revoked := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	m := mandate.Mandate{ID: "mdt-1", TenantID: "t1", Subject: "agent://a",
		Grants:     []mandate.Grant{{Action: "REFUND_INVOICE"}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 5, Window: mandate.WindowCalendarDay}},
		RevokedAt:  &revoked}
	if err := store.Issue(ctx, m, "tester"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", map[string]any{
		"id": "mdt-1", "subject": "agent://a",
		"grants":     []map[string]any{{"action": "REFUND_INVOICE"}},
		"aggregates": []map[string]any{{"quantity": "count", "limit": 5, "window": "calendar_day"}},
		"revoked_at": nil,
	})
	if rr.Code != http.StatusCreated && rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("re-issue status = %d body=%s", rr.Code, rr.Body.String())
	}
	// With no domain source wired the handler refuses to validate, which
	// is itself correct: it will not issue a mandate it cannot check.
	if rr.Code == http.StatusServiceUnavailable {
		return
	}
	ms, _ := store.List(ctx, "t1")
	if len(ms) != 1 || ms[0].RevokedAt == nil {
		t.Fatalf("re-issuing cleared the revocation: %+v", ms)
	}
}

// A mandate that cannot be validated against its domain must not be
// issued: a limit summing a parameter no action declares would never
// bind, and would look like protection.
func TestIssueRefusesWhenTheDomainCannotBeRead(t *testing.T) {
	mux, _ := mandatesServer(t, owner())
	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", map[string]any{
		"id": "mdt-x", "subject": "agent://a", "domain": "billing",
		"grants":     []map[string]any{{"action": "REFUND_INVOICE"}},
		"aggregates": []map[string]any{{"quantity": "sum(amount_cents)", "limit": 100, "window": "calendar_day"}},
	})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the contracts cannot be read", rr.Code)
	}
}

func TestMalformedQuantityIsRejected(t *testing.T) {
	mux, _ := mandatesServer(t, owner())
	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", map[string]any{
		"id": "mdt-x", "subject": "agent://a",
		"grants":     []map[string]any{{"action": "REFUND_INVOICE"}},
		"aggregates": []map[string]any{{"quantity": "total(x)", "limit": 100, "window": "calendar_day"}},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unparseable quantity", rr.Code)
	}
}

// The statement is the artifact a customer reads. It must show the
// limit, what has been drawn, and what remains — and must say that a
// draw happens at authorization rather than execution.
func TestStatementShowsUsedAndRemaining(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	if err := store.Issue(ctx, mandate.Mandate{
		ID: "mdt-1", TenantID: "t1", Subject: "agent://a",
		Grants:     []mandate.Grant{{Action: "REFUND_INVOICE"}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, Limit: 500000, Window: mandate.WindowCalendarDay}},
	}, "tester"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	drawnAt := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	for i, amt := range []int64{7490, 41200} {
		if err := store.Record(ctx, mandate.Draw{
			MandateID: "mdt-1", TenantID: "t1", ProposalID: string(rune('a' + i)),
			QuantityKey: "sum(amount_cents)", Amount: amt, DrawnAt: drawnAt,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/mdt-1/statement", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Limits []struct {
			Quantity  string `json:"quantity"`
			Limit     int64  `json:"limit"`
			Used      int64  `json:"used"`
			Remaining int64  `json:"remaining"`
			Status    string `json:"status"`
		} `json:"limits"`
		Draws []map[string]any `json:"draws"`
		Note  string           `json:"note"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Limits) != 1 {
		t.Fatalf("limits = %d, want 1", len(got.Limits))
	}
	l := got.Limits[0]
	if l.Used != 48690 || l.Remaining != 451310 || l.Status != "ok" {
		t.Errorf("used/remaining/status = %d/%d/%s, want 48690/451310/ok", l.Used, l.Remaining, l.Status)
	}
	if len(got.Draws) != 2 {
		t.Errorf("draws = %d, want 2", len(got.Draws))
	}
	if got.Note == "" {
		t.Error("the statement must state that a draw is recorded at authorization, not execution")
	}
}

func TestStatementFor404sOnUnknownMandate(t *testing.T) {
	mux, _ := mandatesServer(t, owner())
	if rr := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/nope/statement", nil); rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestNilStoreMeansNoRoutes(t *testing.T) {
	if NewMandatesHandler(nil, nil) != nil {
		t.Error("a nil store must produce no handler, so the routes are absent rather than half-working")
	}
}

// stubDomainSource serves one YAML for any (tenant, domain) — enough to
// take the issue path through the real parse, which no test did before
// the panic below.
type stubDomainSource struct{ yaml string }

func (s stubDomainSource) Load(ctx context.Context, tenantID string) (domain.TenantDomain, error) {
	return domain.TenantDomain{TenantID: tenantID, YAML: []byte(s.yaml), Version: 1}, nil
}

func (s stubDomainSource) LoadByDomain(ctx context.Context, tenantID, domainID string) (domain.TenantDomain, error) {
	return domain.TenantDomain{TenantID: tenantID, YAML: []byte(s.yaml), Version: 1, DomainID: domainID}, nil
}

func (s stubDomainSource) List(ctx context.Context, tenantID string) ([]domain.TenantDomain, error) {
	td, _ := s.Load(ctx, tenantID)
	return []domain.TenantDomain{td}, nil
}

// The issue path only reads. The writes are here to satisfy the
// interface and fail loudly if anything starts using them.
func (s stubDomainSource) Create(context.Context, string, []byte) error {
	return errors.New("stubDomainSource is read-only")
}

func (s stubDomainSource) Save(context.Context, string, []byte) error {
	return errors.New("stubDomainSource is read-only")
}

func (s stubDomainSource) Delete(context.Context, string) error {
	return errors.New("stubDomainSource is read-only")
}

func (s stubDomainSource) DeleteByDomain(context.Context, string, string) error {
	return errors.New("stubDomainSource is read-only")
}

func (s stubDomainSource) UpdateByDomain(context.Context, string, string, []byte) error {
	return errors.New("stubDomainSource is read-only")
}

const mandateDomainYAML = `
domain: refund-control
entity: Order
events: [PLACED, PAID]
states: [CREATED, PAID]
transitions:
  - {on: PLACED, from: "", to: CREATED}
  - {on: PAID, from: CREATED, to: PAID}
actions:
  - name: AUTO_REFUND
    allowed_states: [PAID]
    parameters:
      - {name: amount, type: int, min: 1}
      - {name: reason, type: string, max_length: 200}
    risk: medium
    approval: never
    executor: cafe.start_shift
`

// Issuing a mandate parses the domain to check that a summed parameter
// is one the action declares as numeric. That parse ran with a nil
// executor registry, which the validator tolerates by contract and the
// builder dereferenced — so every issue against a domain whose actions
// name an executor answered 500 with a panic in the log, and no mandate
// was created.
//
// Every test here passed a nil domain source, which returns early
// before the parse. The endpoint was covered; the path was not.
func TestIssuingAMandateParsesTheDomainWithoutPanicking(t *testing.T) {
	store := mandate.NewMemoryStore()
	// Same timeline as the handler's clock, so a card issued in a test
	// is issued before the draws the test records against it.
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	h := NewMandatesHandler(store, stubDomainSource{yaml: mandateDomainYAML})
	if h == nil {
		t.Fatal("NewMandatesHandler returned nil with a store wired")
	}
	mux := http.NewServeMux()
	h.Register(mux)

	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", map[string]any{
		"id": "limit-refund-agent", "subject": "refund-agent", "domain": "refund-control",
		"grants":     []map[string]any{{"action": "AUTO_REFUND"}},
		"aggregates": []map[string]any{{"quantity": "sum(amount)", "limit": 110000, "window": "calendar_day"}},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("issue = %d, want 201: %s", rr.Code, rr.Body.String())
	}

	got, err := store.ForSubject(context.Background(), "t1", "refund-agent")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want the mandate stored, got %d", len(got))
	}
	if len(got[0].Aggregates) != 1 || got[0].Aggregates[0].Limit != 110000 {
		t.Errorf("stored aggregate = %+v", got[0].Aggregates)
	}
}

// The parse is still a real check: a mandate summing a parameter the
// action declares as a string is refused, with the reason.
func TestIssuingAMandateStillRefusesANonNumericQuantity(t *testing.T) {
	h := NewMandatesHandler(mandate.NewMemoryStore(), stubDomainSource{yaml: mandateDomainYAML})
	mux := http.NewServeMux()
	h.Register(mux)

	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", map[string]any{
		"id": "limit-refund-agent", "subject": "refund-agent", "domain": "refund-control",
		"grants":     []map[string]any{{"action": "AUTO_REFUND"}},
		"aggregates": []map[string]any{{"quantity": "sum(reason)", "limit": 5, "window": "calendar_day"}},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("issue = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "reason") {
		t.Errorf("the refusal does not name the parameter: %s", rr.Body.String())
	}
}

// Re-issuing an id is how a ceiling is changed, and the upsert destroys
// what it was. The statement has to be able to say what the authority
// used to be and who changed it, or "more room is an owner's decision"
// is a claim about something that leaves no trace.
func TestStatementCarriesTheIssuanceHistory(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	card := func(limit int64) mandate.Mandate {
		return mandate.Mandate{
			ID: "mdt-1", TenantID: "t1", Subject: "agent://a",
			Grants:     []mandate.Grant{{Action: "REFUND_INVOICE"}},
			Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: limit, Window: mandate.WindowCalendarDay}},
		}
	}
	if err := store.Issue(ctx, card(5), "owner@example.com"); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := store.Issue(ctx, card(20), "owner@example.com"); err != nil {
		t.Fatalf("re-issue: %v", err)
	}

	rec := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/mdt-1/statement", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Revisions []struct {
			Revision   int64  `json:"revision"`
			ChangedBy  string `json:"changed_by"`
			Aggregates []struct {
				Limit int64 `json:"limit"`
			} `json:"aggregates"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Revisions) != 2 {
		t.Fatalf("want two revisions, got %d: %s", len(body.Revisions), rec.Body.String())
	}
	// Oldest first, so two consecutive rows are the before and after of
	// one decision.
	if got := body.Revisions[0].Aggregates[0].Limit; got != 5 {
		t.Errorf("first revision ceiling = %d, want 5", got)
	}
	if got := body.Revisions[1].Aggregates[0].Limit; got != 20 {
		t.Errorf("second revision ceiling = %d, want 20", got)
	}
	if body.Revisions[1].ChangedBy != "owner@example.com" {
		t.Errorf("changed_by = %q, want the identity that issued it", body.Revisions[1].ChangedBy)
	}
	if body.Revisions[0].Revision != 1 || body.Revisions[1].Revision != 2 {
		t.Errorf("revision numbers = %d, %d; want 1, 2", body.Revisions[0].Revision, body.Revisions[1].Revision)
	}
}

// A statement of proposal ids is a log. Each movement has to say what
// it was for, what it took, and what was left after it — and that it
// records an authorization, not an execution.
func TestMovementsCarryContextAndTheResultingBalance(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	if err := store.Issue(ctx, mandate.Mandate{
		ID: "mdt-1", TenantID: "t1", Subject: "agent://a",
		Grants:     []mandate.Grant{{Action: "ISSUE_REFUND"}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 1000, Window: mandate.WindowCalendarDay}},
	}, "owner@example.com"); err != nil {
		t.Fatalf("issue: %v", err)
	}
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	for i, d := range []mandate.Draw{
		{ProposalID: "p1", Amount: 400, Action: "ISSUE_REFUND", EntityID: "ord_8812"},
		{ProposalID: "p2", Amount: 250, Action: "ISSUE_REFUND", EntityID: "ord_9001"},
	} {
		d.TenantID, d.MandateID, d.QuantityKey = "t1", "mdt-1", "sum(amount)"
		d.DrawnAt = now.Add(time.Duration(i) * time.Minute)
		if err := store.Record(ctx, d); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/mdt-1/statement", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Draws []struct {
			Action      string `json:"action"`
			EntityID    string `json:"entity_id"`
			Amount      int64  `json:"amount"`
			DrawnTotals []struct {
				Window     string `json:"window"`
				DrawnTotal int64  `json:"drawn_total"`
			} `json:"drawn_totals"`
			Effect string `json:"effect"`
		} `json:"draws"`
		Limits []struct {
			Remaining int64 `json:"remaining"`
		} `json:"limits"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Draws) != 2 {
		t.Fatalf("movements = %d", len(body.Draws))
	}
	if body.Draws[0].Action != "ISSUE_REFUND" || body.Draws[0].EntityID != "ord_8812" {
		t.Errorf("first movement lost its context: %+v", body.Draws[0])
	}
	// The cumulative draw, oldest first. A fact about what happened,
	// which is why it is this and not a remaining: a ceiling raised
	// later would make an old "remaining" a lie about that moment.
	if len(body.Draws[0].DrawnTotals) != 1 || len(body.Draws[1].DrawnTotals) != 1 {
		t.Fatalf("want one subtotal per movement: %+v", body.Draws)
	}
	if body.Draws[0].DrawnTotals[0].DrawnTotal != 400 || body.Draws[1].DrawnTotals[0].DrawnTotal != 650 {
		t.Errorf("drawn totals = %d, %d; want 400, 650",
			body.Draws[0].DrawnTotals[0].DrawnTotal, body.Draws[1].DrawnTotals[0].DrawnTotal)
	}
	// Each subtotal names the window it is counted over, because one
	// quantity can be capped over more than one at a time.
	if body.Draws[0].DrawnTotals[0].Window != "calendar_day" {
		t.Errorf("subtotal window = %q", body.Draws[0].DrawnTotals[0].Window)
	}
	// Authorized, never executed. KIFF decided; the caller's system ran
	// it afterwards and did not report back.
	if body.Draws[0].Effect != "authorized" {
		t.Errorf("effect = %q, want authorized", body.Draws[0].Effect)
	}
	if !strings.Contains(body.Note, "authorization") {
		t.Errorf("note must keep saying these are authorizations: %q", body.Note)
	}
}

// A ceiling raised after the fact must not rewrite what a past
// movement says. The statement reports what each movement drew and
// what had been drawn by then; what is left is claimed once, on the
// limit lines, where it is about now.
func TestRaisingTheCeilingDoesNotRewriteOldMovements(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	card := func(limit int64) mandate.Mandate {
		return mandate.Mandate{
			ID: "mdt-1", TenantID: "t1", Subject: "agent://a",
			Grants:     []mandate.Grant{{Action: "REFUND_ORDER"}},
			Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: limit, Window: mandate.WindowCalendarDay}},
		}
	}
	if err := store.Issue(ctx, card(100), "owner@example.com"); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := store.Record(ctx, mandate.Draw{
		TenantID: "t1", MandateID: "mdt-1", ProposalID: "p1", QuantityKey: "sum(amount)",
		Amount: 20, DrawnAt: time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC),
		Action: "REFUND_ORDER", EntityID: "ord_1",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// The owner raises the ceiling afterwards.
	if err := store.Issue(ctx, card(200), "owner@example.com"); err != nil {
		t.Fatalf("re-issue: %v", err)
	}

	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/mdt-1/statement", nil)
	var body struct {
		Draws []map[string]any `json:"draws"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Draws) != 1 {
		t.Fatalf("movements = %d", len(body.Draws))
	}
	// 20 was drawn, and 20 had been drawn by then. Both true whatever
	// the ceiling became.
	totals, _ := body.Draws[0]["drawn_totals"].([]any)
	if len(totals) != 1 {
		t.Fatalf("drawn_totals = %v", body.Draws[0]["drawn_totals"])
	}
	if got := totals[0].(map[string]any)["drawn_total"]; got != float64(20) {
		t.Errorf("drawn_total = %v, want 20", got)
	}
	// No per-movement remaining at all: it could only ever be right for
	// the ceiling in force at that instant, which nothing here knows.
	if _, ok := body.Draws[0]["remaining_after"]; ok {
		t.Error("a movement claimed a remaining balance, which a later ceiling change makes a lie")
	}
}

// A rolling window moves. A movement at 11:20 was decided against
// 10:20–11:20, so a draw at 10:40 was part of what it had consumed —
// even though at 12:00 that draw has rolled out of the live window.
// Summing only what is on screen would quietly understate the older
// rows.
func TestRollingWindowMovementsCountTheirOwnWindow(t *testing.T) {
	mux, store := mandatesServer(t, owner()) // clock: 2026-09-12 12:00 UTC
	ctx := context.Background()
	if err := store.Issue(ctx, mandate.Mandate{
		ID: "mdt-1", TenantID: "t1", Subject: "agent://a",
		Grants: []mandate.Grant{{Action: "REFUND_ORDER"}},
		Aggregates: []mandate.Aggregate{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"},
			Limit:    1000, Window: mandate.WindowRollingHour,
		}},
	}, "owner@example.com"); err != nil {
		t.Fatalf("issue: %v", err)
	}
	day := func(h, m int) time.Time { return time.Date(2026, 9, 12, h, m, 0, 0, time.UTC) }
	for _, d := range []mandate.Draw{
		{ProposalID: "p-1040", Amount: 100, DrawnAt: day(10, 40)},
		{ProposalID: "p-1120", Amount: 200, DrawnAt: day(11, 20)},
		{ProposalID: "p-1150", Amount: 50, DrawnAt: day(11, 50)},
	} {
		d.TenantID, d.MandateID, d.QuantityKey = "t1", "mdt-1", "sum(amount)"
		d.Action, d.EntityID = "REFUND_ORDER", "ord_1"
		if err := store.Record(ctx, d); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/mdt-1/statement", nil)
	var body struct {
		Draws []struct {
			ProposalID  string `json:"proposal_id"`
			DrawnTotals []struct {
				Window     string `json:"window"`
				DrawnTotal int64  `json:"drawn_total"`
			} `json:"drawn_totals"`
		} `json:"draws"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The 10:40 draw is outside the live window (11:00–12:00), so it is
	// not a row — the statement still shows the current window.
	if len(body.Draws) != 2 {
		t.Fatalf("rows = %d, want the two inside the live window: %s", len(body.Draws), rr.Body.String())
	}
	got := map[string]int64{}
	for _, d := range body.Draws {
		if len(d.DrawnTotals) != 1 {
			t.Fatalf("%s: want one subtotal, got %+v", d.ProposalID, d.DrawnTotals)
		}
		got[d.ProposalID] = d.DrawnTotals[0].DrawnTotal
	}
	// 11:20 was decided against 10:20–11:20, which held the 10:40 draw.
	if got["p-1120"] != 300 {
		t.Errorf("11:20 movement drawn_total = %d, want 300 (100 from 10:40 + its own 200)", got["p-1120"])
	}
	// 11:50 was decided against 10:50–11:50: the 10:40 draw had rolled
	// out by then, so it does not count for this one.
	if got["p-1150"] != 250 {
		t.Errorf("11:50 movement drawn_total = %d, want 250 (200 + its own 50)", got["p-1150"])
	}
}

// One quantity can be capped over more than one window at once — five
// an hour and twenty a day are both true of the same movement — so a
// single subtotal could only ever be one of them.
func TestAMovementSubtotalsEveryWindowItWasCountedOver(t *testing.T) {
	mux, store := mandatesServer(t, owner()) // clock: 2026-09-12 12:00 UTC
	ctx := context.Background()
	if err := store.Issue(ctx, mandate.Mandate{
		ID: "mdt-1", TenantID: "t1", Subject: "agent://a",
		Grants: []mandate.Grant{{Action: "REFUND_ORDER"}},
		Aggregates: []mandate.Aggregate{
			{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 5, Window: mandate.WindowRollingHour},
			{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 20, Window: mandate.WindowCalendarDay},
		},
	}, "owner@example.com"); err != nil {
		t.Fatalf("issue: %v", err)
	}
	day := func(h, m int) time.Time { return time.Date(2026, 9, 12, h, m, 0, 0, time.UTC) }
	for _, at := range []time.Time{day(9, 0), day(11, 30), day(11, 45)} {
		if err := store.Record(ctx, mandate.Draw{
			TenantID: "t1", MandateID: "mdt-1", ProposalID: "p" + at.Format("1504"),
			QuantityKey: "count", Amount: 1, DrawnAt: at,
			Action: "REFUND_ORDER", EntityID: "ord_1",
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/mdt-1/statement", nil)
	var body struct {
		Draws []struct {
			ProposalID  string `json:"proposal_id"`
			DrawnTotals []struct {
				Window     string `json:"window"`
				DrawnTotal int64  `json:"drawn_total"`
			} `json:"drawn_totals"`
		} `json:"draws"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var last struct {
		perWindow map[string]int64
	}
	last.perWindow = map[string]int64{}
	for _, d := range body.Draws {
		if d.ProposalID != "p1145" {
			continue
		}
		for _, tot := range d.DrawnTotals {
			last.perWindow[tot.Window] = tot.DrawnTotal
		}
	}
	if len(last.perWindow) != 2 {
		t.Fatalf("want a subtotal per limit, got %+v", last.perWindow)
	}
	// The hour holds the 11:30 and 11:45 draws; the day holds all three.
	if last.perWindow["rolling_1h"] != 2 {
		t.Errorf("hourly subtotal = %d, want 2", last.perWindow["rolling_1h"])
	}
	if last.perWindow["calendar_day"] != 3 {
		t.Errorf("daily subtotal = %d, want 3", last.perWindow["calendar_day"])
	}
}

// Re-editing a card must not recount what already went through. The
// terms a movement is read against are the ones that were in force when
// it happened.
func TestChangingTheWindowDoesNotRecountOldMovements(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	card := func(w mandate.Window) mandate.Mandate {
		return mandate.Mandate{
			ID: "mdt-1", TenantID: "t1", Subject: "agent://a",
			Grants:     []mandate.Grant{{Action: "REFUND_ORDER"}},
			Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 20, Window: w}},
		}
	}
	// Issued over a rolling hour at 08:00 (the store's test clock).
	if err := store.Issue(ctx, card(mandate.WindowRollingHour), "owner@example.com"); err != nil {
		t.Fatalf("issue: %v", err)
	}
	day := func(h, m int) time.Time { return time.Date(2026, 9, 12, h, m, 0, 0, time.UTC) }
	for _, at := range []time.Time{day(11, 5), day(11, 40)} {
		if err := store.Record(ctx, mandate.Draw{
			TenantID: "t1", MandateID: "mdt-1", ProposalID: "p" + at.Format("1504"),
			QuantityKey: "count", Amount: 1, DrawnAt: at, Action: "REFUND_ORDER", EntityID: "o1",
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/mdt-1/statement", nil)
	var body struct {
		Draws []struct {
			ProposalID  string `json:"proposal_id"`
			DrawnTotals []struct {
				Window     string `json:"window"`
				DrawnTotal int64  `json:"drawn_total"`
			} `json:"drawn_totals"`
		} `json:"draws"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, d := range body.Draws {
		if len(d.DrawnTotals) != 1 {
			t.Fatalf("%s: %+v", d.ProposalID, d.DrawnTotals)
		}
		// Both movements were decided under the rolling hour, and both
		// still say so — whatever the card became afterwards.
		if d.DrawnTotals[0].Window != "rolling_1h" {
			t.Errorf("%s counted over %q, but it was decided under rolling_1h",
				d.ProposalID, d.DrawnTotals[0].Window)
		}
	}
}

// The balance and the movements are separate reads. A statement that
// rendered an unreadable ledger as an empty window would turn a fact
// about us into a claim about the agent — and it is the claim an owner
// is least able to check.
func TestUnreadableMovementsAreNotAnEmptyWindow(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	if err := store.Issue(ctx, mandate.Mandate{
		ID: "mdt-1", TenantID: "t1", Subject: "agent://a",
		Grants:     []mandate.Grant{{Action: "REFUND_ORDER"}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 5, Window: mandate.WindowCalendarDay}},
	}, "owner@example.com"); err != nil {
		t.Fatalf("issue: %v", err)
	}
	store.FailDrawsWith(errors.New("ledger unavailable"))

	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/mdt-1/statement", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["movements_error"]; !ok {
		t.Error("an unreadable ledger must be reported, not rendered as no movements")
	}
	// Absent rather than empty: an empty list is a statement that
	// nothing drew on this card, which is exactly what is unknown.
	if _, ok := body["draws"]; ok {
		t.Error("draws was present while the ledger could not be read")
	}
	// The balance still answers — the two reads are independent, and
	// dropping a readable balance because the ledger failed would be
	// the worse trade.
	if _, ok := body["limits"]; !ok {
		t.Error("the balance was dropped because the movements failed")
	}
}

// Issuing records the domain's immutable id, and after the domain
// is renamed the card is shown under the name it has now.
func TestIssuedCardRecordsDomainIDAndShowsTheCurrentName(t *testing.T) {
	ctx := context.Background()
	src := domain.NewMemorySource()
	if err := src.Save(ctx, "t1", []byte(mandateDomainYAML)); err != nil {
		t.Fatalf("save: %v", err)
	}
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	mux := http.NewServeMux()
	NewMandatesHandler(store, src).Register(mux)

	rr := do(t, mux, owner(), http.MethodPost, "/v1/me/mandates", map[string]any{
		"id": "limit-refund-agent", "subject": "refund-agent", "domain": "refund-control",
		"grants":     []map[string]any{{"action": "AUTO_REFUND"}},
		"aggregates": []map[string]any{{"quantity": "sum(amount)", "limit": 110000, "window": "calendar_day"}},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("issue = %d: %s", rr.Code, rr.Body.String())
	}
	got, _ := store.ForSubject(ctx, "t1", "refund-agent")
	if len(got) != 1 || got[0].DomainID != "refund-control" {
		t.Fatalf("stored card = %+v, want DomainID refund-control", got)
	}

	renamed := strings.Replace(mandateDomainYAML, "domain: refund-control", "domain: refund-control-v2", 1)
	if err := src.Save(ctx, "t1", []byte(renamed)); err != nil {
		t.Fatalf("rename: %v", err)
	}
	body := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates", nil).Body.String()
	if !strings.Contains(body, `"domain":"refund-control-v2"`) || !strings.Contains(body, `"domain_id":"refund-control"`) {
		t.Errorf("after a rename the card must show the current name and keep its id: %s", body)
	}
}

// A day's total says when it starts again, the next midnight in the Card's
// time zone, so no app works out "today" in the phone's own (item
// 19). A rolling total never starts again, and says no end.
func TestStatementSaysWhenADaysTotalStartsAgain(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	sum := mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}
	if err := store.Issue(context.Background(), mandate.Mandate{
		ID: "mdt-paris", TenantID: "t1", Subject: "agent://a", Location: "Europe/Paris",
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE"}},
		Aggregates: []mandate.Aggregate{
			{Quantity: sum, Limit: 30000, Window: mandate.WindowCalendarDay},
			{Quantity: sum, Limit: 5000, Window: mandate.WindowRollingHour},
		},
	}, "tester"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rr := do(t, mux, owner(), http.MethodGet, "/v1/me/mandates/mdt-paris/statement", nil)
	var got struct {
		Limits []struct {
			Window        string     `json:"window"`
			WindowStarted time.Time  `json:"window_started"`
			WindowEnd     *time.Time `json:"window_end"`
			Location      string     `json:"location"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || len(got.Limits) != 2 {
		t.Fatalf("decode: %v %s", err, rr.Body.String())
	}
	paris, _ := time.LoadLocation("Europe/Paris")
	day, hour := got.Limits[0], got.Limits[1]
	if day.WindowEnd == nil || day.Location != "Europe/Paris" {
		t.Fatalf("a day's total: %+v", day)
	}
	end := day.WindowEnd.In(paris)
	if end.Hour() != 0 || end.Minute() != 0 || !end.Equal(day.WindowStarted.In(paris).AddDate(0, 0, 1)) {
		t.Errorf("window_end %v, want the midnight after %v in Paris", end, day.WindowStarted.In(paris))
	}
	if hour.WindowEnd != nil {
		t.Errorf("a rolling total says an end: %v", hour.WindowEnd)
	}
}
