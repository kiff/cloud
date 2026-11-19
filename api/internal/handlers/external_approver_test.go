package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
)

func TestExternalApproverHTTP(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	s := mandate.NewMemoryStore()
	s.SetApproverPolicy(func(context.Context, string, string) error { return nil })
	m := mandate.Mandate{ID: "card", TenantID: "t1", Subject: "worker", Domain: "billing", OnExceed: mandate.OnExceedOwner, ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour), Grants: []mandate.Grant{{Action: "refund", PerAction: []mandate.PerActionLimit{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 50}}}}, Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 200, Window: mandate.WindowCalendarDay}}}
	if err := s.Issue(ctx, m, "alice"); err != nil {
		t.Fatal(err)
	}
	h := NewMandatesHandler(s, nil, WithMandatesClock(func() time.Time { return now }))
	mux := http.NewServeMux()
	h.Register(mux)
	h.RegisterExternalApprover(mux)
	admin := owner()
	admin.Method = auth.MethodSession
	admin.MemberRole = membership.RoleAdmin
	rr := do(t, mux, admin, "POST", "/v1/me/approver-grants", map[string]any{"name": "night-shift", "card_id": "card", "worker": "worker", "action": "refund", "parameter": "amount", "max": 100, "expires_at": now.Add(time.Hour)})
	if rr.Code != 201 {
		t.Fatalf("create %d %s", rr.Code, rr.Body)
	}
	var created struct {
		Credential string                `json:"credential"`
		Grant      mandate.ApproverGrant `json:"grant"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	request := mandate.Request{Subject: "worker", Domain: "billing", Action: "refund", EntityID: "order-1", Parameters: map[string]int64{"amount": 75}}
	a := mandate.NewAuthorizer(s, nil, func() time.Time { return now })
	d, err := a.Authorize(ctx, "t1", "op", request)
	if err != nil {
		t.Fatal(err)
	}
	send := func(method, path string, body any, key string) *httptest.ResponseRecorder {
		var b bytes.Buffer
		if body != nil {
			json.NewEncoder(&b).Encode(body)
		}
		req := httptest.NewRequest(method, path, &b)
		req.Header.Set("Authorization", "Bearer "+key)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}
	path := "/v1/approver/holds/" + d.ExceptionID
	rr = send("GET", path, nil, created.Credential)
	if rr.Code != 200 {
		t.Fatalf("read %d %s", rr.Code, rr.Body)
	}
	var view approverView
	json.Unmarshal(rr.Body.Bytes(), &view)
	if view.Hold["entity_id"] != "order-1" || view.ReviewToken == "" {
		t.Fatal(view)
	}
	rr = send("GET", "/v1/approver/holds", nil, created.Credential)
	if rr.Code != 200 {
		t.Fatalf("poll %d %s", rr.Code, rr.Body)
	}
	listed := do(t, mux, admin, "GET", "/v1/me/approver-grants?card_id=card", nil)
	var management struct {
		Grants []mandate.ApproverGrant `json:"grants"`
	}
	json.Unmarshal(listed.Body.Bytes(), &management)
	if listed.Code != 200 || len(management.Grants) != 1 || management.Grants[0].ConnectionStatus != "connected" || management.Grants[0].LastSeenAt == nil {
		t.Fatalf("management %d %s", listed.Code, listed.Body)
	}
	if bytes.Contains(listed.Body.Bytes(), []byte(created.Credential)) {
		t.Fatal("credential appeared in management list")
	}
	body := map[string]any{"review_token": view.ReviewToken, "decision": "approve", "reason_code": "fixture", "reason": "Independent order record permits this refund."}
	stale := map[string]any{"review_token": "old", "decision": "approve", "reason_code": "fixture"}
	if rr = send("POST", path+"/answer", stale, created.Credential); rr.Code != 409 {
		t.Fatalf("stale %d", rr.Code)
	}
	if rr = send("POST", path+"/answer", body, created.Credential); rr.Code != 200 {
		t.Fatalf("answer %d %s", rr.Code, rr.Body)
	}
	var answered struct {
		ExternalAnswer *mandate.ExternalAnswer `json:"external_answer"`
	}
	json.Unmarshal(rr.Body.Bytes(), &answered)
	if answered.ExternalAnswer == nil || answered.ExternalAnswer.Name != created.Grant.Name || answered.ExternalAnswer.Reason != body["reason"] {
		t.Fatalf("answer attribution %s", rr.Body)
	}
	if d, err = a.Authorize(ctx, "t1", "op", request); err != nil || !d.Allowed() {
		t.Fatalf("collect %+v %v", d, err)
	}
	if rr = send("POST", path+"/answer", body, created.Credential); rr.Code != 200 {
		t.Fatalf("duplicate after consumption %d %s", rr.Code, rr.Body)
	}
	if rr = send("GET", "/v1/approver/holds/not-this-hold", nil, created.Credential); rr.Code != 404 {
		t.Fatalf("scope %d", rr.Code)
	}
	if rr = send("GET", path, nil, "worker-runtime-key"); rr.Code != 401 {
		t.Fatalf("worker read %d", rr.Code)
	}
	if rr = do(t, mux, runtimeKey(), "POST", "/v1/me/approver-grants", nil); rr.Code != 403 {
		t.Fatalf("runtime mint %d", rr.Code)
	}
	second, err := a.Authorize(ctx, "t1", "second-op", request)
	if err != nil || second.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("second hold %+v %v", second, err)
	}
	secondPath := "/v1/approver/holds/" + second.ExceptionID
	rr = send("GET", secondPath, nil, created.Credential)
	if rr.Code != 200 {
		t.Fatalf("second review %d %s", rr.Code, rr.Body)
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	body["review_token"] = view.ReviewToken
	if rr = send("POST", secondPath+"/answer", body, created.Credential); rr.Code != 200 {
		t.Fatalf("second approval %d %s", rr.Code, rr.Body)
	}
	if rr = do(t, mux, admin, "POST", "/v1/me/approver-grants/"+created.Grant.ID+"/revoke", nil); rr.Code != 200 {
		t.Fatalf("revoke %d", rr.Code)
	}
	if rr = send("GET", path, nil, created.Credential); rr.Code != 403 {
		t.Fatalf("withdrawn %d", rr.Code)
	}
	rr = do(t, mux, admin, "POST", "/v1/me/approver-grants", map[string]any{"name": "replacement", "principal": "replacement", "card_id": "card", "worker": "worker", "action": "refund", "parameter": "amount", "max": 100, "expires_at": now.Add(time.Hour)})
	if rr.Code != 201 {
		t.Fatalf("replacement %d %s", rr.Code, rr.Body)
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	rr = send("GET", "/v1/approver/holds", nil, created.Credential)
	var list struct {
		Holds []approverView `json:"holds"`
	}
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &list) != nil || len(list.Holds) != 1 || list.Holds[0].Hold["status"] != "held" {
		t.Fatalf("projected outstanding list %d %s", rr.Code, rr.Body)
	}
	body["review_token"] = list.Holds[0].ReviewToken
	if rr = send("POST", secondPath+"/answer", body, created.Credential); rr.Code != 200 {
		t.Fatalf("replacement answer without worker retry %d %s", rr.Code, rr.Body)
	}
	third, err := a.Authorize(ctx, "t1", "deferred-op", request)
	if err != nil || third.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("deferred hold %+v %v", third, err)
	}
	thirdPath := "/v1/approver/holds/" + third.ExceptionID
	rr = send("GET", thirdPath, nil, created.Credential)
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &view) != nil {
		t.Fatalf("defer review %d %s", rr.Code, rr.Body)
	}
	deferToken := view.ReviewToken
	body["review_token"], body["decision"], body["reason"] = deferToken, "defer", "Independent facts require human review."
	if rr = send("POST", thirdPath+"/answer", body, created.Credential); rr.Code != 200 {
		t.Fatalf("defer %d %s", rr.Code, rr.Body)
	}
	if d, err = a.Authorize(ctx, "t1", "deferred-op", request); err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("deferred retry %+v %v", d, err)
	}
	rr = do(t, mux, admin, "GET", "/v1/me/card-exceptions/"+third.ExceptionID, nil)
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &answered) != nil || answered.ExternalAnswer == nil || answered.ExternalAnswer.Name != "replacement" || answered.ExternalAnswer.Reason != body["reason"] || answered.ExternalAnswer.Decision != "defer" {
		t.Fatalf("human view lost defer %d %s", rr.Code, rr.Body)
	}
	rr = send("GET", thirdPath, nil, created.Credential)
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &view) != nil || view.ReviewToken != deferToken {
		t.Fatalf("identical retry changed review token %d %s", rr.Code, rr.Body)
	}
	m.Grants[0].PerAction[0].Max = 40
	if err := s.Issue(ctx, m, "alice"); err != nil {
		t.Fatal(err)
	}
	if d, err = a.Authorize(ctx, "t1", "deferred-op", request); err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("new review %+v %v", d, err)
	}
	rr = do(t, mux, admin, "GET", "/v1/me/card-exceptions/"+third.ExceptionID, nil)
	var history struct {
		ExternalAnswer *mandate.ExternalAnswer  `json:"external_answer"`
		PriorApprovals []mandate.ApprovalRecord `json:"prior_approvals"`
	}
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &history) != nil || history.ExternalAnswer != nil || len(history.PriorApprovals) != 1 || history.PriorApprovals[0].ExternalAnswer == nil || history.PriorApprovals[0].ExternalAnswer.Reason != body["reason"] {
		t.Fatalf("human view lost historical defer %d %s", rr.Code, rr.Body)
	}
}

func TestExternalApproverUnavailableOriginalArguments(t *testing.T) {
	now := time.Now().UTC()
	s := mandate.NewMemoryStore()
	s.SetApproverPolicy(func(context.Context, string, string) error { return nil })
	m := mandate.Mandate{ID: "card", TenantID: "t1", Subject: "worker", Domain: "gateway", OnExceed: mandate.OnExceedOwner, ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour), Grants: []mandate.Grant{{Action: "refund", PerAction: []mandate.PerActionLimit{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 50}}}}}
	s.Issue(context.Background(), m, "alice")
	g := mandate.ApproverGrant{ID: "grant", TenantID: "t1", Name: "reviewer", Principal: "reviewer", Worker: "worker", CardID: "card", Action: "refund", Parameter: "amount", Max: 100, CreatedBy: "alice", ExpiresAt: now.Add(time.Hour), KeyHash: tokenHash("kiff_approver_fixture")}
	if err := s.CreateApproverGrant(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(s, nil, func() time.Time { return now })
	d, err := a.Authorize(context.Background(), "t1", "gateway-op", mandate.Request{Subject: "worker", Domain: "gateway", Action: "refund", Parameters: map[string]int64{"amount": 75}})
	if err != nil {
		t.Fatal(err)
	}
	h := NewMandatesHandler(s, nil)
	mux := http.NewServeMux()
	h.RegisterExternalApprover(mux)
	req := httptest.NewRequest("GET", "/v1/approver/holds/"+d.ExceptionID, nil)
	req.Header.Set("Authorization", "Bearer kiff_approver_fixture")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 503 {
		t.Fatalf("missing arguments %d %s", rr.Code, rr.Body)
	}
}
