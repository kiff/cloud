package pages

import (
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

func TestBuildDomainVerify_BackingClassification(t *testing.T) {
	t.Parallel()
	actions := []apiclient.DomainAction{
		{Name: "MARK_PAID", Executor: "refund.mark_paid"}, // cloud-registered
		{Name: "RUN_IT", Executor: "cloud.proxy"},         // client-run
		{Name: "ORPHAN", Executor: ""},                    // drift
	}

	v := BuildDomainVerify(actions, nil)
	if !v.Available {
		t.Fatal("nil rows is not an error; Available should be true")
	}
	if len(v.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(v.Rows))
	}

	byName := map[string]DomainVerifyRow{}
	for _, r := range v.Rows {
		byName[r.ActionName] = r
	}

	if got := byName["MARK_PAID"].Backing; got != BackingCloud {
		t.Errorf("MARK_PAID backing = %v, want BackingCloud", got)
	}
	if got := byName["RUN_IT"].Backing; got != BackingClient {
		t.Errorf("RUN_IT backing = %v, want BackingClient", got)
	}
	if got := byName["ORPHAN"].Backing; got != BackingUnknown {
		t.Errorf("ORPHAN backing = %v, want BackingUnknown", got)
	}
}

func TestBuildDomainVerify_LoopClosedSemantics(t *testing.T) {
	t.Parallel()
	actions := []apiclient.DomainAction{
		{Name: "CLOUD_QUIET", Executor: "refund.mark_paid"},
		{Name: "CLOUD_BUSY", Executor: "refund.issue_refund"},
		{Name: "CLIENT_QUIET", Executor: "cloud.proxy"},
		{Name: "CLIENT_BUSY", Executor: "cloud.proxy"},
		{Name: "ORPHAN", Executor: ""},
	}
	now := time.Now().UTC()
	rows := []apiclient.ProposalDecision{
		{ActionName: "CLOUD_BUSY", Outcome: "allowed", CreatedAt: now, ReceiptTraceID: "rcpt-1"},
		{ActionName: "CLIENT_BUSY", Outcome: "allowed", CreatedAt: now},
	}

	v := BuildDomainVerify(actions, rows)
	byName := map[string]DomainVerifyRow{}
	for _, r := range v.Rows {
		byName[r.ActionName] = r
	}

	// Cloud-run is closed regardless of traffic (runtime backs it).
	if !byName["CLOUD_QUIET"].LoopClosed() {
		t.Error("cloud-run with no traffic should still be LoopClosed")
	}
	if got := byName["CLOUD_QUIET"].VerdictLabel(); got != "Ready" {
		t.Errorf("CLOUD_QUIET verdict = %q, want Ready", got)
	}
	if got := byName["CLOUD_BUSY"].VerdictLabel(); got != "Live" {
		t.Errorf("CLOUD_BUSY verdict = %q, want Live", got)
	}

	// Client-run is only closed once observed.
	if byName["CLIENT_QUIET"].LoopClosed() {
		t.Error("client-run with no traffic must NOT be LoopClosed")
	}
	if got := byName["CLIENT_QUIET"].VerdictLabel(); got != "Not yet connected" {
		t.Errorf("CLIENT_QUIET verdict = %q, want 'Not yet connected'", got)
	}
	if !byName["CLIENT_BUSY"].LoopClosed() {
		t.Error("client-run with observed traffic should be LoopClosed")
	}
	if got := byName["CLIENT_BUSY"].VerdictLabel(); got != "Connected" {
		t.Errorf("CLIENT_BUSY verdict = %q, want Connected", got)
	}

	// Unbacked is never closed.
	if byName["ORPHAN"].LoopClosed() {
		t.Error("unbacked action must never be LoopClosed")
	}
}

func TestBuildDomainVerify_ObservedAggregation(t *testing.T) {
	t.Parallel()
	actions := []apiclient.DomainAction{{Name: "REFUND", Executor: "cloud.proxy"}}
	older := time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	rows := []apiclient.ProposalDecision{
		{ActionName: "REFUND", Outcome: "allowed", CreatedAt: older},
		{ActionName: "REFUND", Outcome: "blocked", CreatedAt: newer, ReceiptTraceID: "r-2"},
		{ActionName: "OTHER", Outcome: "allowed", CreatedAt: newer}, // not declared — ignored
	}

	v := BuildDomainVerify(actions, rows)
	if len(v.Rows) != 1 {
		t.Fatalf("rows = %d, want 1 (only declared actions)", len(v.Rows))
	}
	r := v.Rows[0]
	if !r.Observed || r.Decisions != 2 {
		t.Errorf("REFUND observed=%v decisions=%d, want observed=true decisions=2", r.Observed, r.Decisions)
	}
	if !r.ReceiptSeen {
		t.Error("ReceiptSeen should be true when any sampled decision carried a receipt")
	}
	if r.LastSeen == nil || !r.LastSeen.Equal(newer) {
		t.Errorf("LastSeen = %v, want %v (most recent)", r.LastSeen, newer)
	}
}

func TestDomainVerify_OK(t *testing.T) {
	t.Parallel()

	// Empty domain is not OK (nothing verified).
	if (DomainVerify{Available: true}).OK() {
		t.Error("empty DomainVerify should not be OK")
	}

	// All loops closed → OK.
	closed := BuildDomainVerify(
		[]apiclient.DomainAction{
			{Name: "A", Executor: "refund.mark_paid"},
			{Name: "B", Executor: "cloud.proxy"},
		},
		[]apiclient.ProposalDecision{{ActionName: "B", CreatedAt: time.Now()}},
	)
	if !closed.OK() {
		t.Error("all-closed verify should be OK")
	}

	// One client-run action never exercised → not OK.
	open := BuildDomainVerify(
		[]apiclient.DomainAction{{Name: "B", Executor: "cloud.proxy"}},
		nil,
	)
	if open.OK() {
		t.Error("verify with an unexercised client-run action should not be OK")
	}
}
