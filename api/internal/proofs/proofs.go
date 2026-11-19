// Package proofs keeps the live proofs of KIFF's integrations: one summary
// per run of the proof runner (kiff/agents), each app's state worked out
// from them (green, red or unknown), and the operators' overrides.
//
// The runner sends a run's summary to POST /v1/ops/proofs, authenticated by
// GitHub OIDC only (oidc.go). The run's step-by-step events stay in the
// private reports bucket; KIFF keeps their key. Only test tenants' runs are
// accepted, so nothing here is a customer's data.
//
// An app's state (state.go) is what a later change of the gateway reads to
// hold a red app's writes for the owner. This package decides the state;
// it holds nothing itself.
package proofs

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SchemaVersion is the run record's shape this KIFF reads.
const SchemaVersion = 1

// Outcomes of a run. An error run is the runner itself breaking: it is
// shown and alerted on, never counted for or against the app.
const (
	OutcomePass  = "pass"
	OutcomeFail  = "fail"
	OutcomeError = "error"
)

// Triggers of a run.
const (
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
)

// The checks a run may report. A run names the ones its app's job has; a
// check that never ran is sent as not ok.
const (
	CheckConnects        = "connects"
	CheckReads           = "reads"
	CheckWrites          = "writes"
	CheckCardLimitHeld   = "card_limit_held"
	CheckHoldRaised      = "hold_raised"
	CheckApprovalApplied = "approval_applied"
	CheckVendorReceived  = "vendor_received"
	CheckWords           = "words"
)

var knownChecks = map[string]bool{
	CheckConnects: true, CheckReads: true, CheckWrites: true, CheckCardLimitHeld: true,
	CheckHoldRaised: true, CheckApprovalApplied: true, CheckVendorReceived: true, CheckWords: true,
}

// requiredChecks must be in every pass or fail: whether it connected, and
// whether the Card's limits held.
var requiredChecks = []string{CheckConnects, CheckCardLimitHeld}

// Check is one named check of a run.
type Check struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Why  string `json:"why,omitempty"`
}

// Run is one proof run's summary, as the runner sends it (schema 1).
type Run struct {
	Schema        int       `json:"schema"`
	RunID         string    `json:"run_id"`
	App           string    `json:"app"`
	Job           string    `json:"job"`
	Tenant        string    `json:"tenant"`
	Trigger       string    `json:"trigger"`
	Thread        string    `json:"thread,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	EndedAt       time.Time `json:"ended_at"`
	Outcome       string    `json:"outcome"`
	Checks        []Check   `json:"checks"`
	LimitsHeld    bool      `json:"limits_held"`
	P50Ms         int       `json:"p50_ms"`
	P95Ms         int       `json:"p95_ms"`
	RunnerVersion string    `json:"runner_version"`
	EventsKey     string    `json:"events_key"`
	// Entry is the catalog entry revision the run proved, name@rev. An
	// app's state is kept per entry: a new revision starts unknown.
	Entry string `json:"entry"`
	// Rules are the digests of what judged the run (Coach's wording rules,
	// the judges' texts), a JSON object kept as given.
	Rules json.RawMessage `json:"rules"`

	// Set by KIFF, never by the runner.
	Counted    bool      `json:"counted"`
	ReceivedAt time.Time `json:"received_at"`
	// GitHubRunID and GitHubRunAttempt are the GitHub run that sent this
	// one, from its signed token (run_id, run_attempt).
	GitHubRunID      string `json:"github_run_id,omitempty"`
	GitHubRunAttempt string `json:"github_run_attempt,omitempty"`
}

// Check returns the named check and whether the run has it.
func (r Run) Check(name string) (Check, bool) {
	for _, c := range r.Checks {
		if c.Name == name {
			return c, true
		}
	}
	return Check{}, false
}

// LimitsFailed reports whether the run's card_limit_held check failed. A
// limits failure makes an app red at once.
func (r Run) LimitsFailed() bool {
	c, ok := r.Check(CheckCardLimitHeld)
	return r.Outcome == OutcomeFail && ok && !c.OK
}

// ErrInvalid is wrapped by every refusal of a run's summary; the message
// after it is a short code the runner can act on.
var ErrInvalid = errors.New("proofs: invalid run")

func invalid(code string, args ...any) error {
	if len(args) > 0 {
		code = fmt.Sprintf(code, args...)
	}
	return fmt.Errorf("%w: %s", ErrInvalid, code)
}

var (
	reRunID   = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
	reApp     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	reVersion = regexp.MustCompile(`^[0-9a-f]{40}$`)
	reEntry   = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}@[A-Za-z0-9_.-]{1,64}$`)
	// reEventsKey is the runner's layout, one prefix per run:
	// proofs/<run_id>/<path>. No segment starts with a dot.
	reEventsKey = regexp.MustCompile(`^proofs/([A-Za-z0-9_-]{8,64})(/[A-Za-z0-9_-][A-Za-z0-9._-]{0,127})*/?$`)
)

const (
	maxJob    = 128
	maxThread = 128
	maxWhy    = 500
	maxRules  = 16 << 10
	clockSkew = 5 * time.Minute
)

// Validate checks a run's summary as the runner sent it, before KIFF
// keeps it. testTenants are the only tenants a run may name.
func Validate(r Run, testTenants map[string]bool, now time.Time) error {
	if r.Schema != SchemaVersion {
		return invalid("schema %d", r.Schema)
	}
	switch {
	case !reRunID.MatchString(r.RunID):
		return invalid("run_id")
	case !reApp.MatchString(r.App):
		return invalid("app")
	case r.Tenant == "" || !testTenants[r.Tenant]:
		// Test tenants only: a proof never runs on a customer's account.
		return invalid("tenant_not_test")
	case r.Trigger != TriggerSchedule && r.Trigger != TriggerManual:
		return invalid("trigger")
	case len(r.Job) > maxJob || !printable(r.Job):
		return invalid("job")
	case len(r.Thread) > maxThread || !printable(r.Thread):
		return invalid("thread")
	case r.StartedAt.IsZero() || r.EndedAt.IsZero() || r.EndedAt.Before(r.StartedAt):
		return invalid("times")
	case r.EndedAt.After(now.Add(clockSkew)):
		return invalid("times_future")
	case r.P50Ms < 0 || r.P95Ms < 0 || r.P50Ms > r.P95Ms:
		return invalid("timings")
	case !reVersion.MatchString(r.RunnerVersion):
		return invalid("runner_version")
	case !ValidEntry(r.Entry):
		return invalid("entry")
	}
	if err := validRules(r.Rules); err != nil {
		return err
	}
	if err := validEventsKey(r); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, c := range r.Checks {
		switch {
		case !knownChecks[c.Name]:
			return invalid("check %q", c.Name)
		case seen[c.Name]:
			return invalid("check %q twice", c.Name)
		case len(c.Why) > maxWhy || !printable(c.Why):
			return invalid("check %q why", c.Name)
		}
		seen[c.Name] = true
	}
	if err := consistent(r); err != nil {
		return err
	}
	return scanRunText(r)
}

// consistent holds the outcome to the checks: a pass has every check ok,
// a fail has at least one not ok, and limits_held is the card_limit_held
// check itself, never a second judgement.
func consistent(r Run) error {
	switch r.Outcome {
	case OutcomePass, OutcomeFail:
	case OutcomeError:
		return nil
	default:
		return invalid("outcome")
	}
	for _, name := range requiredChecks {
		if _, ok := r.Check(name); !ok {
			return invalid("check %q missing", name)
		}
	}
	allOK := true
	for _, c := range r.Checks {
		allOK = allOK && c.OK
	}
	if r.Outcome == OutcomePass && !allOK {
		return invalid("pass_with_failed_check")
	}
	if r.Outcome == OutcomeFail && allOK {
		return invalid("fail_without_failed_check")
	}
	if limits, _ := r.Check(CheckCardLimitHeld); r.LimitsHeld != limits.OK {
		return invalid("limits_held_disagrees")
	}
	return nil
}

// validEventsKey holds events_key to the runner's layout, proofs/<run_id>
// or a key under it, whose run_id is the run's own.
func validEventsKey(r Run) error {
	m := reEventsKey.FindStringSubmatch(r.EventsKey)
	if m == nil || m[1] != r.RunID {
		return invalid("events_key")
	}
	return nil
}

// ValidEntry reports whether entry is a catalog entry revision, name@rev.
func ValidEntry(entry string) bool { return reEntry.MatchString(entry) }

// validRules holds rules to a JSON object of at most maxRules bytes.
func validRules(raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > maxRules {
		return invalid("rules")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return invalid("rules")
	}
	return nil
}

func printable(s string) bool {
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// secretPatterns are values that look like a credential. The runner
// redacts before it writes; this is KIFF's second check.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                                          // AWS access key id
	regexp.MustCompile(`\b(sk|rk|pk)_(live|test)_[0-9A-Za-z]{10,}`),                 // Stripe
	regexp.MustCompile(`\bgh[pousr]_[0-9A-Za-z]{20,}`),                              // GitHub tokens
	regexp.MustCompile(`\bgithub_pat_[0-9A-Za-z_]{20,}`),                            // GitHub fine-grained
	regexp.MustCompile(`\bxox[abprs]-[0-9A-Za-z-]{10,}`),                            // Slack
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),                        // PEM
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{16,}`),                    // Authorization values
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]+`), // JWT
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`),                                   // Google API key
	regexp.MustCompile(`\bya29\.[0-9A-Za-z_-]{20,}`),                                // Google OAuth token
}

// secretKeyNames are field names whose value is a secret wherever they appear.
var secretKeyNames = []string{"api_key", "apikey", "token", "access_token", "refresh_token", "password", "secret", "client_secret", "authorization", "cookie"}

// LooksSecret reports whether s looks like a credential.
func LooksSecret(s string) bool {
	for _, p := range secretPatterns {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}

// SecretKeyName reports whether a field named k carries a secret.
func SecretKeyName(k string) bool {
	k = strings.ToLower(strings.TrimSpace(k))
	for _, n := range secretKeyNames {
		if k == n {
			return true
		}
	}
	return false
}

func scanRunText(r Run) error {
	for _, s := range []string{r.Job, r.Thread, string(r.Rules)} {
		if LooksSecret(s) {
			return invalid("secret_in_text")
		}
	}
	for _, c := range r.Checks {
		if LooksSecret(c.Why) {
			return invalid("secret_in_text")
		}
	}
	return nil
}

// RedactEvent returns one event line with every secret-named field and
// every secret-looking string replaced by "[redacted]", and whether
// anything was. It is KIFF's second check on the runner's own redaction,
// run on each line before the replay view shows it.
func RedactEvent(line json.RawMessage) (json.RawMessage, bool) {
	var v any
	if err := json.Unmarshal(line, &v); err != nil {
		return json.RawMessage(`{"kind":"unreadable"}`), true
	}
	out, changed := redact(v)
	b, err := json.Marshal(out)
	if err != nil {
		return json.RawMessage(`{"kind":"unreadable"}`), true
	}
	return b, changed
}

func redact(v any) (any, bool) {
	switch t := v.(type) {
	case map[string]any:
		changed := false
		for k, val := range t {
			if SecretKeyName(k) {
				t[k] = "[redacted]"
				changed = true
				continue
			}
			nv, c := redact(val)
			t[k] = nv
			changed = changed || c
		}
		return t, changed
	case []any:
		changed := false
		for i, val := range t {
			nv, c := redact(val)
			t[i] = nv
			changed = changed || c
		}
		return t, changed
	case string:
		if LooksSecret(t) {
			return "[redacted]", true
		}
		return t, false
	}
	return v, false
}
