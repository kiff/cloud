// Package templates ships the cloud's curated starter domains.
//
// A "starter" is a domain definition and sample event that the
// dashboard applies in one click against a fresh tenant. Some starters
// also carry a decision script for a runnable first demonstration.
//
// Templates are vendored snapshots. The canonical sources live in
// the framework repo for cafe-ops (kiff/kiff/examples/ai-cafe-ops)
// and at api/scripts/cafe.kiff.yaml for the cloud-side
// counterpart. refund-agno's YAML is authored here directly because
// the framework ships the example as Go code, not YAML. Either way:
//
//   - The cloud and the framework ship independently. The cloud
//     never reads YAML out of the framework's filesystem layout.
//   - Updates land via `make sync-templates` or a manual edit to
//     these files. Both paths are reviewed; nothing is generated
//     at startup.
//
// Each template's YAML is validated against
// executors.DefaultRegistry() in tests, so a registry change that
// would orphan an executor key fails the build before reaching
// production.
package templates

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// fsRoot embeds the YAML + sample_event files under the per-slug
// subdirectories. Layout:
//
//	embed/
//	  cafe-ops/
//	    kiff.yaml
//	    sample_event.json
//	  refund-agno/
//	    kiff.yaml
//	    sample_event.json
//
//go:embed cafe-ops refund-agno duplicate-payment refund-ceiling chargeback-dispute collections-promise kyb-verification vulnerability-escalation card-refund retention-credit subscription-plan-change
var fsRoot embed.FS

// Template is one starter the cloud ships.
type Template struct {
	// Slug is the URL-safe identifier the apply endpoint accepts.
	// Lowercase, ASCII, hyphen-separated.
	Slug string `json:"slug"`

	// Name is a short human label rendered on the onboarding card.
	Name string `json:"name"`

	// Description is one sentence describing the domain. Shown
	// under the Name on the onboarding card.
	Description string `json:"description"`

	// YAML is the embedded kiff.yaml bytes for this template. The
	// apply path passes it through domain.ParseAndBuild before
	// upserting into cloud_meta.tenant_domains; tests parse it at
	// load time so an invalid template fails the build.
	YAML []byte `json:"-"`

	// SampleEvent is a representative event payload the dashboard
	// uses verbatim in its "send your first event" curl block.
	// Stored as raw JSON so tests can parse it and the handler can
	// hand it to the dashboard without re-marshalling.
	SampleEvent json.RawMessage `json:"sample_event"`

	// DecisionScript is a small JSON document that drives the
	// dashboard's "Make a decision" button: an ordered list of
	// (ingest|execute) steps that produce one auditable
	// action_executed record (and therefore one receipt). The
	// dashboard runs the steps server-side after a single click
	// so a brand-new tenant can see a real, anchored decision in
	// 30 seconds.
	DecisionScript json.RawMessage `json:"decision_script,omitempty"`
}

// catalog holds the registered templates indexed by slug.
type catalog struct {
	mu      sync.RWMutex
	bySlug  map[string]Template
	ordered []string
}

// global catalog populated at package init from the embedded FS.
// Loaded once; no production caller mutates it. Tests may
// substitute a fresh catalog through the unexported newCatalog
// helper if they need to.
var globalCatalog = mustLoad()

func mustLoad() *catalog {
	c, err := newCatalog(fsRoot)
	if err != nil {
		panic("templates: load embedded catalog: " + err.Error())
	}
	return c
}

// newCatalog reads templates from the given FS. Exposed for tests
// that want to point at an alternative tree.
func newCatalog(srcFS fs.FS) (*catalog, error) {
	c := &catalog{bySlug: make(map[string]Template)}

	// Hard-coded metadata keeps the public catalog deliberate. Adding a
	// directory alone must never expose an unfinished starter.
	defs := []struct {
		slug, name, desc string
	}{
		{"cafe-ops", "Cafe Ops", "An agent that runs a small cafe — books shifts, orders inventory, escalates when something needs a human."},
		{"refund-agno", "Refund Agent", "An automated e-commerce refund agent — marks orders paid, refunds them under policy, escalates the rest."},
		{"duplicate-payment", "Duplicate payment", "An accounts-payable agent pays invoices. The state machine enforces pay-once: a PAID invoice refuses every retry, so a flaky-connection storm can't double-debit."},
		{"refund-ceiling", "Refund after close", "A support agent issues refunds. Your app tracks the running total and marks the order fully refunded; from that point KIFF refuses every further refund — the closed order can't be reopened."},
		{"chargeback-dispute", "Chargeback resubmit", "A disputes agent files chargebacks. Once a dispute is submitted, re-submission is refused — no duplicate chargebacks to the card scheme, no duplicate penalty fees."},
		{"collections-promise", "Collections after promise", "A collections agent contacts borrowers. While a promise-to-pay is active, contact is refused — protecting the borrower and the lender from FDCPA / CONC violations."},
		{"card-refund", "Refund on an issued card", "A support agent with no card is refused even on a refundable order. Issue a Refund Card and each authorization draws against its own balance until the ceiling stops it."},
		{"retention-credit", "Retention credit", "A support agent issues store credit only for an eligible retention case, drawing each authorization against its own daily credit budget."},
		{"subscription-plan-change", "Subscription plan change", "A support agent changes active subscriptions under a separate count-based card, without sharing the refund or store-credit balance."},
		{"kyb-verification", "Repeated KYB check", "A KYB workflow runs a paid bureau check. Once verified, every re-check is refused — the paid verification runs exactly once, even on a retried workflow."},
		{"vulnerability-escalation", "Vulnerability escalation", "A customer-ops team acts on accounts. One vulnerability signal halts every automated action for the whole team until a human takes over (FCA Consumer Duty)."},
	}

	for _, d := range defs {
		yamlBytes, err := fs.ReadFile(srcFS, d.slug+"/kiff.yaml")
		if err != nil {
			return nil, fmt.Errorf("read %s/kiff.yaml: %w", d.slug, err)
		}
		eventBytes, err := fs.ReadFile(srcFS, d.slug+"/sample_event.json")
		if err != nil {
			return nil, fmt.Errorf("read %s/sample_event.json: %w", d.slug, err)
		}
		// Validate the JSON shape early so a malformed event file
		// fails the build, not the first dashboard click.
		var probe map[string]any
		if err := json.Unmarshal(eventBytes, &probe); err != nil {
			return nil, fmt.Errorf("%s/sample_event.json is not valid JSON: %w", d.slug, err)
		}
		// Decision script is optional during the rollout; absence
		// means the dashboard renders the legacy ingest-only flow.
		var scriptBytes []byte
		if raw, err := fs.ReadFile(srcFS, d.slug+"/decision_script.json"); err == nil {
			var probeScript map[string]any
			if err := json.Unmarshal(raw, &probeScript); err != nil {
				return nil, fmt.Errorf("%s/decision_script.json is not valid JSON: %w", d.slug, err)
			}
			scriptBytes = append([]byte(nil), raw...)
		}
		c.bySlug[d.slug] = Template{
			Slug:           d.slug,
			Name:           d.name,
			Description:    d.desc,
			YAML:           yamlBytes,
			SampleEvent:    json.RawMessage(append([]byte(nil), eventBytes...)),
			DecisionScript: json.RawMessage(scriptBytes),
		}
		c.ordered = append(c.ordered, d.slug)
	}
	sort.Strings(c.ordered)
	return c, nil
}

// ErrTemplateNotFound is returned when a slug does not match any
// registered template. The HTTP layer maps it to 404.
var ErrTemplateNotFound = errors.New("templates: not found")

// Get returns the template registered under slug, or
// ErrTemplateNotFound. Slug match is case-sensitive on the
// canonical (lowercase) form; callers should normalize before
// calling.
func Get(slug string) (Template, error) {
	return globalCatalog.get(slug)
}

func (c *catalog) get(slug string) (Template, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return Template{}, ErrTemplateNotFound
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.bySlug[slug]
	if !ok {
		return Template{}, ErrTemplateNotFound
	}
	return cloneTemplate(t), nil
}

// List returns all registered templates in lexicographic slug
// order. The returned slice is safe for callers to mutate; the
// catalog holds its own copies.
func List() []Template {
	return globalCatalog.list()
}

func (c *catalog) list() []Template {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Template, 0, len(c.ordered))
	for _, slug := range c.ordered {
		out = append(out, cloneTemplate(c.bySlug[slug]))
	}
	return out
}

// cloneTemplate returns a deep-enough copy that callers can mutate
// the returned value without affecting the catalog. YAML and
// SampleEvent are byte slices; we copy them.
func cloneTemplate(t Template) Template {
	yaml := append([]byte(nil), t.YAML...)
	event := append(json.RawMessage(nil), t.SampleEvent...)
	var script json.RawMessage
	if len(t.DecisionScript) > 0 {
		script = append(json.RawMessage(nil), t.DecisionScript...)
	}
	return Template{
		Slug:           t.Slug,
		Name:           t.Name,
		Description:    t.Description,
		YAML:           yaml,
		SampleEvent:    event,
		DecisionScript: script,
	}
}
