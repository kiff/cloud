package handlers_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	studio "github.com/kiff/kiff-studio"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/studio_view"
)

// studio_enforcement_header_test.go pins the v3 Studio lead:
// Studio is a guided one-control setup surface, with observed
// runtime facts shown when available. The underlying enforcement
// classifier still carries the truth-state contract.

func renderStudio(t *testing.T, v pages.StudioView) string {
	t.Helper()
	var buf bytes.Buffer
	if err := pages.Studio(v).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render studio: %v", err)
	}
	return buf.String()
}

// Build is a rendered read-only projection of the running domain, not a
// form: no disabled inputs, the source-of-truth is the repo's versioned
// kiff.yaml (docker-compose model), and the kiff-domains skill is the
// authoring launchpad.
func TestStudioBuild_IsReadOnlyProjectionNotAForm(t *testing.T) {
	t.Parallel()
	v := pages.StudioView{Draft: studioDraftFixture()}
	body := renderStudio(t, v)

	if strings.Contains(body, "disabled") {
		t.Errorf("Build must not render disabled form inputs (the fake editor)\n---\n%s", body)
	}
	if !strings.Contains(body, "studio-rofield") {
		t.Error("Build should render read-only projection fields")
	}
	for _, want := range []string{
		"running version",         // docker-compose framing
		"docker-compose",          // the versioned-artifact model
		"/skills/kiff-domains.md", // the agent-authoring launchpad
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Build projection missing %q", want)
		}
	}
}

func studioDraftFixture() studio_view.StudioDraft {
	return studio_view.StudioDraft{
		Blueprint: studio.Blueprint{
			Domain:  "duplicate-payment",
			Entity:  "Invoice",
			Actions: []studio.Action{{Name: "PAY_INVOICE"}},
		},
	}
}

// TestStudio_AssistantPromptIsGroundedAndCopyable: the drafting Build
// view renders the one-instruction prompt (with a Copy button), grounded
// in the draft — the domain, entity, and observed action all appear — and
// carries the skill + verify + activate-back steps so the agent has the
// whole task. This is the "reduce to one instruction" contract.
func TestStudio_AssistantPromptIsGroundedAndCopyable(t *testing.T) {
	t.Parallel()
	v := pages.StudioView{Draft: studioDraftFixture()}
	body := renderStudio(t, v)

	for _, want := range []string{
		`data-copy-target="studio-prompt-text"`, // the Copy button hook
		`Copy instruction`,
		`id="studio-prompt-text"`, // the copyable block
		`Give one instruction`,    // the assistant-first framing
		`duplicate-payment`,       // grounded: the domain
		`PAY_INVOICE`,             // grounded: the observed action
		`kiff-domains skill`,      // the skill reference in the prompt
		`kiff verify`,             // the verify step in the prompt
		`dashboard/build`,         // the activate-back destination
		`studio-journey`,          // the three-step journey cue
	} {
		if !strings.Contains(body, want) {
			t.Errorf("assistant prompt render missing %q", want)
		}
	}
}

// TestStudio_SetupChecklistMirrorsState: a not-yet-active draft renders
// the "Current state" setup checklist — a status mirror, not buttons —
// with the observed/derived milestones marked done and activation still
// pending. It must NOT render once the domain is enforcing.
func TestStudio_SetupChecklistMirrorsState(t *testing.T) {
	t.Parallel()

	// Drafting/observed: an observed runtime + a derived action.
	v := pages.StudioView{
		Draft: studioDraftFixture(),
		ObservedRuntime: &pages.StudioObservedRuntime{
			Project: "cookbook", Environment: "aws", ActionName: "PAY_INVOICE",
		},
	}
	body := renderStudio(t, v)
	for _, want := range []string{
		"Current state · setup",
		"Runtime observed",
		"Action derived",
		"Finish with your agent",
		"Activate in observe",
		"First decision recorded",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("setup checklist missing %q", want)
		}
	}

	// Enforcing: the setup checklist is gone (it's operating now).
	enforcing := pages.StudioView{
		Draft:         studioDraftFixture(),
		ActiveControl: &apiclient.Control{Domain: "duplicate-payment", Status: "active"},
	}
	if b := renderStudio(t, enforcing); strings.Contains(b, "Current state · setup") {
		t.Error("setup checklist must not render once the domain is enforcing")
	}
}

// TestStudioV3Lead_Enforcing: an active control still classifies as
// enforcing, while the rendered page stays in the guided setup layout
// and links to Control Room for the proof surface.
func TestStudioV3Lead_Enforcing(t *testing.T) {
	t.Parallel()
	v := pages.StudioView{
		Draft: studioDraftFixture(),
		ActiveControl: &apiclient.Control{
			Domain:  "duplicate-payment",
			Entity:  "Invoice",
			Actions: []string{"PAY_INVOICE"},
			Status:  "active",
		},
	}
	if v.EnforcementState() != "enforcing" {
		t.Fatalf("EnforcementState = %q, want enforcing", v.EnforcementState())
	}
	body := renderStudio(t, v)

	mustContain := []string{
		`studio-v3`,
		`Hand your domain to your AI coding agent.`,
		`Build · AI-assisted`,
		`How authoring works`,
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("enforcing header missing %q", want)
		}
	}
	for _, absent := range []string{`Preview control`, `Your domain lives in your codebase.`} {
		if strings.Contains(body, absent) {
			t.Errorf("Build header should not render the retired SRP element %q", absent)
		}
	}
	if strings.Contains(body, `studio-enforce-on`) || strings.Contains(body, `studio-enforce-observe`) {
		t.Error("v3 Studio should not render the retired enforcement header variants")
	}
}

// TestStudioV3Lead_Observed: an observed runtime with no active
// control renders the observed-runtime source card.
func TestStudioV3Lead_Observed(t *testing.T) {
	t.Parallel()
	v := pages.StudioView{
		Draft: studioDraftFixture(),
		ObservedRuntime: &pages.StudioObservedRuntime{
			Project:     "cookbook",
			Environment: "aws",
			AgentID:     "ap-agent",
			Workflow:    "duplicate-payment",
			ActionName:  "PAY_INVOICE",
		},
	}
	if v.EnforcementState() != "observed" {
		t.Fatalf("EnforcementState = %q, want observed", v.EnforcementState())
	}
	body := renderStudio(t, v)

	mustContain := []string{
		`Build derived this from a live guard runtime.`,
		`PAY_INVOICE`,
		`duplicate-payment`,
		`ap-agent`,
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("observed header missing %q", want)
		}
	}
	if strings.Contains(body, `studio-enforce-on`) {
		t.Error("observed state must not render the retired enforcement header")
	}
}

// TestStudioV3Lead_Drafting: no active control and no observed
// runtime renders the neutral guided setup page.
func TestStudioV3Lead_Drafting(t *testing.T) {
	t.Parallel()
	v := pages.StudioView{
		Draft: studioDraftFixture(),
	}
	if v.EnforcementState() != "drafting" {
		t.Fatalf("EnforcementState = %q, want drafting", v.EnforcementState())
	}
	body := renderStudio(t, v)

	if !strings.Contains(body, `Hand your domain to your AI coding agent.`) {
		t.Error("drafting page missing the Build assistant heading")
	}
	if !strings.Contains(body, `Give one instruction to your agent.`) {
		t.Error("drafting page missing the assistant handoff")
	}
	if !strings.Contains(body, `Derive`) {
		t.Error("drafting page missing the first bridge stage")
	}
	if strings.Contains(body, `studio-enforce-on`) || strings.Contains(body, `studio-enforce-observe`) {
		t.Error("drafting state must not render retired enforcement variants")
	}
}

// TestStudioEnforcementHeader_SupersededControlIsNotEnforcing: a
// control that exists but is superseded must not read as
// enforcing — only Status "active" counts.
func TestStudioEnforcementHeader_SupersededControlIsNotEnforcing(t *testing.T) {
	t.Parallel()
	v := pages.StudioView{
		Draft: studioDraftFixture(),
		ActiveControl: &apiclient.Control{
			Domain: "duplicate-payment",
			Status: "superseded",
		},
	}
	if v.EnforcementState() == "enforcing" {
		t.Error("a superseded control must not yield the enforcing state")
	}
}
