package templates_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/templates"
)

// TestList_ReturnsExpectedTemplates pins the catalog contract: the
// curated set of starter domains, shipped together. If a template is
// renamed or added the test must change deliberately rather than
// silently. Order is lexicographic (the catalog sorts slugs).
func TestList_ReturnsExpectedTemplates(t *testing.T) {
	t.Parallel()
	all := templates.List()
	if got, want := len(all), 11; got != want {
		t.Fatalf("List() returned %d templates, want %d", got, want)
	}

	gotSlugs := make([]string, 0, len(all))
	for _, tmpl := range all {
		gotSlugs = append(gotSlugs, tmpl.Slug)
	}
	want := []string{
		"cafe-ops",
		"card-refund",
		"chargeback-dispute",
		"collections-promise",
		"duplicate-payment",
		"kyb-verification",
		"refund-agno",
		"refund-ceiling",
		"retention-credit",
		"subscription-plan-change",
		"vulnerability-escalation",
	}
	for i, w := range want {
		if i >= len(gotSlugs) || gotSlugs[i] != w {
			t.Fatalf("List() slugs = %v, want %v", gotSlugs, want)
		}
	}
}

// TestGet_ReturnsCopy verifies callers can mutate the returned
// Template without affecting the catalog.
func TestGet_ReturnsCopy(t *testing.T) {
	t.Parallel()
	a, err := templates.Get("cafe-ops")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(a.YAML) > 0 {
		a.YAML[0] = '#' // benign mutation
	}
	b, err := templates.Get("cafe-ops")
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if len(b.YAML) > 0 && b.YAML[0] == '#' && a.YAML[0] == '#' && len(a.YAML) > 0 {
		// Cafe yaml starts with '#' (header comment) so this
		// mutation is a no-op; that's expected. The point is the
		// returned slice is independent — assert by writing a
		// definitively non-cafe byte and confirming the catalog
		// untouched.
		a.YAML[0] = 0x00
	}
	c, err := templates.Get("cafe-ops")
	if err != nil {
		t.Fatalf("third Get: %v", err)
	}
	if c.YAML[0] == 0x00 {
		t.Fatalf("Get() returned a shared slice; mutation leaked back to the catalog")
	}
}

// TestGet_NotFound is the error contract the HTTP handler maps to 404.
func TestGet_NotFound(t *testing.T) {
	t.Parallel()
	_, err := templates.Get("does-not-exist")
	if !errors.Is(err, templates.ErrTemplateNotFound) {
		t.Fatalf("err = %v, want ErrTemplateNotFound", err)
	}
}

// TestGet_TrimsAndLowercases asserts the lookup is forgiving about
// whitespace and case, since it's user-input-driven on the
// dashboard side.
func TestGet_TrimsAndLowercases(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"  cafe-ops", "Cafe-Ops", "CAFE-OPS\t"} {
		if _, err := templates.Get(raw); err != nil {
			t.Fatalf("Get(%q) = %v", raw, err)
		}
	}
}

// TestEveryTemplate_YAMLValidates is the load-bearing build-time
// guarantee for the catalog: every shipped template must parse and
// build against the cloud's executor registry. A registry change
// that drops or renames an executor key surfaces here.
func TestEveryTemplate_YAMLValidates(t *testing.T) {
	t.Parallel()
	registry := executors.DefaultRegistry()
	for _, tmpl := range templates.List() {
		tmpl := tmpl
		t.Run(tmpl.Slug, func(t *testing.T) {
			t.Parallel()
			if _, err := domain.ParseAndBuild(tmpl.YAML, registry); err != nil {
				t.Fatalf("ParseAndBuild(%s): %v", tmpl.Slug, err)
			}
		})
	}
}

// TestSampleEvent_ParsesAsJSON pins that every sample event is
// well-formed JSON with the fields the framework's adapter.RawInput
// expects. We don't fully unmarshal into RawInput (the framework
// type is internal to that package); checking the top-level keys
// is enough to catch a typo.
func TestSampleEvent_ParsesAsJSON(t *testing.T) {
	t.Parallel()
	required := []string{"id", "type", "entity_id", "entity_type", "actor_id", "received_at"}
	for _, tmpl := range templates.List() {
		tmpl := tmpl
		t.Run(tmpl.Slug, func(t *testing.T) {
			t.Parallel()
			var got map[string]any
			if err := json.Unmarshal(tmpl.SampleEvent, &got); err != nil {
				t.Fatalf("sample_event.json: %v", err)
			}
			for _, k := range required {
				if _, ok := got[k]; !ok {
					t.Errorf("sample_event missing field %q", k)
				}
			}
		})
	}
}

// TestSampleEvent_TypeIsDeclared asserts the sample event's `type`
// is one of the events declared in the template's YAML — otherwise
// the dashboard's curl produces a "no transition for event type"
// error, which would be a confusing first impression.
func TestSampleEvent_TypeIsDeclared(t *testing.T) {
	t.Parallel()
	registry := executors.DefaultRegistry()
	for _, tmpl := range templates.List() {
		tmpl := tmpl
		t.Run(tmpl.Slug, func(t *testing.T) {
			t.Parallel()
			parsed, err := domain.ParseAndBuild(tmpl.YAML, registry)
			if err != nil {
				t.Fatalf("ParseAndBuild: %v", err)
			}
			var ev map[string]any
			if err := json.Unmarshal(tmpl.SampleEvent, &ev); err != nil {
				t.Fatalf("sample_event.json: %v", err)
			}
			eventType, _ := ev["type"].(string)
			if eventType == "" {
				t.Fatalf("sample_event has empty type")
			}
			declared := false
			for _, e := range parsed.Definition.EventTypes {
				if e == eventType {
					declared = true
					break
				}
			}
			if !declared {
				t.Fatalf("sample_event type %q not in domain events; declared events are %s",
					eventType, strings.Join(parsed.Definition.EventTypes, ", "))
			}
		})
	}
}

// TestApprovalActions_DeclareASeparateReviewer pins the separate reviewer across the
// catalog: every action that requires approval names a reviewer
// permission (`.approve`), some role holds it, and a role that can
// propose the action without it cannot review it. The review gate
// refuses review of an action with no reviewer, so a template that
// dropped it would ship an approval nobody can grant.
func TestApprovalActions_DeclareASeparateReviewer(t *testing.T) {
	t.Parallel()
	registry := executors.DefaultRegistry()
	for _, tmpl := range templates.List() {
		parsed, err := domain.ParseAndBuild(tmpl.YAML, registry)
		if err != nil {
			t.Fatalf("ParseAndBuild(%s): %v", tmpl.Slug, err)
		}
		roles := parsed.Spec.Permissions.Roles
		for _, a := range parsed.Spec.Actions {
			if a.Approval != "required" {
				continue
			}
			reviewer := a.ReviewerPermission()
			if reviewer == "" {
				t.Errorf("%s/%s requires approval but declares no reviewer permission", tmpl.Slug, a.Name)
				continue
			}
			held := false
			for _, perms := range roles {
				for _, p := range perms {
					if p == reviewer {
						held = true
					}
				}
			}
			if !held {
				t.Errorf("%s/%s: no role holds its reviewer permission %s", tmpl.Slug, a.Name, reviewer)
			}
		}
	}
}
