package handlers_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/pages"
)

// dashboard_next_action_test.go covers the contextual "next
// action" card. The branch logic is server-side, computed by
// computeNextAction from tenant state; after the Overview
// retired the card renders on the domain home. These tests
// pin the branch table directly (the production
// computeNextAction via the _export_test.go alias) and,
// separately, that the domain home renders a given card.
//
// Branches per the issue body:
//
//   1. No domain, no draft           → Install the guard
//   2. No domain, observed draft      → Review & activate draft
//   3. Domain saved, no API key       → Mint your first API key
//   4. Pending approvals waiting       → Review pending approvals
//   5. Domain + key, no proposal yet   → Send your first proposal
//   6. All operating                   → Add a second action

// TestComputeNextAction_BranchesOnTenantState walks all six
// canonical states. One sub-test per branch keeps failures
// readable when a single branch regresses. Asserts on the
// returned card (Kind / Title / CTA) directly — no render — so
// the branch table is pinned independently of any one surface.
func TestComputeNextAction_BranchesOnTenantState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		mutate      func(v *pages.DashboardView)
		wantKind    string
		wantTitle   string
		wantCTAHref string
	}{
		{
			name: "no-domain-no-draft → install-guard",
			mutate: func(v *pages.DashboardView) {
				v.Domain = nil
				v.Draft = nil
				v.KeyCount = 0
			},
			wantKind:    "install-guard",
			wantTitle:   "Install the guard",
			wantCTAHref: "/docs/connect-an-agent",
		},
		{
			name: "no-domain-observed-draft → activate-draft",
			mutate: func(v *pages.DashboardView) {
				v.Domain = nil
				v.Draft = sampleDraft()
				v.KeyCount = 0
			},
			wantKind:    "activate-draft",
			wantTitle:   "Review and activate your derived domain",
			wantCTAHref: "/dashboard/build",
		},
		{
			name: "domain-no-key → mint-key",
			mutate: func(v *pages.DashboardView) {
				v.Domain = sampleDomain()
				v.KeyCount = 0
			},
			wantKind:    "mint-key",
			wantTitle:   "Mint your first API key",
			wantCTAHref: "/dashboard/keys",
		},
		{
			name: "domain-key-pending-approvals → review-approvals",
			// Pending approvals outranks the "send first proposal"
			// nudge — see the priority order in computeNextAction.
			mutate: func(v *pages.DashboardView) {
				v.Domain = sampleDomain()
				v.KeyCount = 1
				v.PendingApprovalsCount = 2
			},
			wantKind:    "review-approvals",
			wantTitle:   "Review pending approvals",
			wantCTAHref: "/dashboard/approvals",
		},
		{
			name: "domain-key-no-proposal → first-proposal",
			mutate: func(v *pages.DashboardView) {
				v.Domain = sampleDomain()
				v.KeyCount = 1
				v.Usage = &apiclient.Usage{
					Counters: map[string]uint64{},
				}
			},
			wantKind:    "first-proposal",
			wantTitle:   "Send your first proposal",
			wantCTAHref: "/dashboard/proposals",
		},
		{
			name: "operating → add-action",
			mutate: func(v *pages.DashboardView) {
				v.Domain = sampleDomain()
				v.KeyCount = 1
				v.Usage = &apiclient.Usage{
					Counters: map[string]uint64{
						"kiff.cloud.proposals.submitted": 12,
					},
				}
				v.Proposals = []apiclient.ProposalDecision{
					{ProposalID: "p1", Outcome: "allowed", CreatedAt: time.Now()},
				}
			},
			wantKind:    "add-action",
			wantTitle:   "Add a second action in Build",
			wantCTAHref: "/dashboard/build",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := freshDashboardView()
			tc.mutate(&v)

			// Run the actual production logic, not a re-
			// implementation. dashboard_export_test.go aliases
			// the unexported computeNextAction so this call
			// path matches what the domain home does at request time.
			na := handlers.ComputeNextAction(v)
			if na == nil {
				t.Fatalf("ComputeNextAction returned nil; expected branch %q", tc.wantKind)
			}
			if na.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", na.Kind, tc.wantKind)
			}
			if na.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", na.Title, tc.wantTitle)
			}
			if na.CTAHref != tc.wantCTAHref {
				t.Errorf("CTAHref = %q, want %q", na.CTAHref, tc.wantCTAHref)
			}
		})
	}
}

// TestDomainHome_RendersNextAction asserts the domain home renders a
// provided next-action card: the title, body, CTA link, and the
// kind-scoped class the styling hooks onto. This is the surface the
// card moved to when the Overview retired.
func TestDomainHome_RendersNextAction(t *testing.T) {
	t.Parallel()
	v := freshDomainHomeView()
	v.NextAction = &pages.NextAction{
		Kind:    "first-proposal",
		Title:   "Send your first proposal",
		Body:    "Call POST /v1/proposals/decide with your API key.",
		CTAText: "Open Proposals",
		CTAHref: "/dashboard/proposals",
	}

	body := renderDomainHome(t, v)

	for _, want := range []string{
		"Send your first proposal",
		`href="/dashboard/proposals"`,
		"dash-next-first-proposal",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("domain home next-action card missing %q", want)
		}
	}
}

// sampleDomain returns a minimal Domain summary for tests that
// just need v.Domain != nil with a single declared action.
func sampleDomain() *apiclient.Domain {
	return &apiclient.Domain{
		Domain: "refund-flow",
		Entity: "Order",
		Events: []string{"ORDER_PAID"},
		States: []string{"PAID", "REFUNDED"},
		Actions: []apiclient.DomainAction{{
			Name:               "ISSUE_REFUND",
			AllowedStates:      []string{"PAID"},
			RequiredParameters: []string{"amount"},
		}},
	}
}

// sampleDraft returns a minimal observe-mode draft whose parsed
// catalog has actions — the "observed, not yet governed" signal
// the Overview branches on.
func sampleDraft() *apiclient.Draft {
	return &apiclient.Draft{
		YAML:      "domain: observed-draft\n",
		UpdatedAt: 1,
		Parsed: &apiclient.Domain{
			Domain: "observed-draft",
			Actions: []apiclient.DomainAction{
				{Name: "refund_order"},
				{Name: "send_email"},
			},
		},
	}
}
