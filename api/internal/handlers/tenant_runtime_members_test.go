package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/membership"
)

// the governance-roles design on the runtime approval surface: an approver answers wherever
// the domain lets tenant_owner answer (decision 6, no re-publishing),
// and the hold gate applies separation of duties here too.

// ownerReviewYAML grants the .approve permission to tenant_owner, the
// way KIFF-generated domains do (approvalReviewYAML already does).
var ownerReviewYAML = approvalReviewYAML

func memberReview(t *testing.T, h http.Handler, approvalID string, role membership.Role) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/approvals/"+approvalID+"/grant",
		strings.NewReader(`{"actor":{"id":"x","kind":"human"},"reason":"checked"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "user_" + string(role), Tenant: "t1", Method: auth.MethodSession,
		MemberRole: role, Roles: membership.IdentityRoles(role),
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestApprovalReview_ApproverAnswersWhereTenantOwnerDoes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		role membership.Role
		want int
	}{
		{membership.RoleApprover, http.StatusOK},
		{membership.RoleViewer, http.StatusForbidden},
		{membership.RoleEditor, http.StatusForbidden}, // the domain grants .approve to tenant_owner only
	} {
		rt, approvalID := buildApprovalReviewRuntime(t, ownerReviewYAML)
		h := handlers.NewTenantRuntimeHandler(&stubAccessor{rt: rt, spec: specOf(t, ownerReviewYAML)}, "/v1", nil)
		if rec := memberReview(t, h, approvalID, c.role); rec.Code != c.want {
			t.Errorf("%s: %d, want %d; body=%s", c.role, rec.Code, c.want, rec.Body.String())
		}
	}
}

func TestApprovalReview_SeparationOfDutiesAppliesToRuntimeApprovals(t *testing.T) {
	t.Parallel()
	members := membership.NewMemoryStore()
	svc := membership.NewService(members)
	ctx := context.Background()
	if _, err := svc.EnsureFounder(ctx, "t1", "user_admin"); err != nil {
		t.Fatal(err)
	}
	_, tok, _ := svc.CreateInvite(ctx, "t1", membership.Actor{Subject: "user_admin"}, membership.InviteRequest{Email: "a@x.io", Role: membership.RoleApprover})
	if _, err := svc.AcceptInvite(ctx, tok, membership.Actor{Subject: "user_approver"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetSeparationOfDuties(ctx, "t1", membership.Actor{Subject: "user_admin"}, true); err != nil {
		t.Fatal(err)
	}
	rt, approvalID := buildApprovalReviewRuntime(t, ownerReviewYAML)
	h := handlers.NewTenantRuntimeHandler(&stubAccessor{rt: rt, spec: specOf(t, ownerReviewYAML)}, "/v1", nil).WithHoldGate(members)
	if rec := memberReview(t, h, approvalID, membership.RoleAdmin); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "separation_of_duties_role") {
		t.Fatalf("admin with SoD on: %d %s", rec.Code, rec.Body.String())
	}
	if rec := memberReview(t, h, approvalID, membership.RoleApprover); rec.Code != http.StatusOK {
		t.Fatalf("approver with SoD on: %d %s", rec.Code, rec.Body.String())
	}
	answers, _ := members.HoldAnswers(ctx, "t1", []string{approvalID})
	if a, ok := answers[approvalID]; !ok || a.Role != membership.RoleApprover || !a.SeparationOfDuties || a.Answer != "grant" {
		t.Fatalf("attribution = %+v (ok=%v)", a, ok)
	}
}
