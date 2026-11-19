package pages

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// proofStateClass maps an app's proof state to a dashboard pill.
func proofStateClass(s apiclient.OpsProofState) string {
	switch {
	case s.Held:
		return "dash-pill dash-pill-error"
	case s.State == "green":
		return "dash-pill dash-pill-ok"
	default:
		return "dash-pill dash-pill-muted"
	}
}

// proofStateLabel says what the gateway does with the app's writes.
func proofStateLabel(s apiclient.OpsProofState) string {
	label := s.State
	switch {
	case s.Override == "hold":
		label += " · held by an operator"
	case s.Override == "release":
		label += " · released with the owner's yes"
	case s.Held:
		label += " · writes held"
	}
	if s.Stale {
		label += " · stale"
	}
	return label
}

// proofReason is the reason in words.
func proofReason(reason string) string {
	switch reason {
	case "limits_failed":
		return "the card limit didn't hold"
	case "three_failed":
		return "three counted runs failed"
	case "two_passed":
		return "two counted runs passed"
	case "stale":
		return "state unreadable for over 24 h"
	case "expired":
		return "no counted pass for over 48 h"
	}
	return ""
}

func proofOutcomeClass(outcome string) string {
	switch outcome {
	case "pass":
		return "dash-pill dash-pill-ok"
	case "fail":
		return "dash-pill dash-pill-error"
	case "error":
		return "dash-pill dash-pill-warn"
	}
	return "dash-pill dash-pill-muted"
}

func proofDayClass(d apiclient.OpsProofDay) string { return "ops-strip-day ops-strip-" + d.Status }

func proofDayTitle(d apiclient.OpsProofDay) string { return d.Date + ": " + d.Status }

func proofPassed(r apiclient.OpsProofRow) string {
	if r.Counted == 0 {
		return "—"
	}
	return strconv.Itoa(r.Passed) + " / " + strconv.Itoa(r.Counted)
}

func proofMs(ms int) string {
	if ms <= 0 {
		return "—"
	}
	if ms >= 10000 {
		return strconv.Itoa(ms/1000) + " s"
	}
	return strconv.Itoa(ms) + " ms"
}

func proofCheckClass(c apiclient.OpsProofCheck) string {
	cls := "ops-check"
	if c.OK {
		cls += " ops-check-ok"
	} else {
		cls += " ops-check-fail"
	}
	if c.Name == "card_limit_held" {
		cls += " ops-check-card"
	}
	return cls
}

func proofCheckMark(c apiclient.OpsProofCheck) string {
	if c.OK {
		return "✓"
	}
	return "✗"
}

// proofCheckName is a check's name in words.
func proofCheckName(name string) string {
	switch name {
	case "connects":
		return "Connects"
	case "reads":
		return "Reads"
	case "writes":
		return "Writes"
	case "card_limit_held":
		return "Card limit held"
	case "hold_raised":
		return "Hold raised"
	case "approval_applied":
		return "Approval applied"
	case "vendor_received":
		return "Vendor received"
	case "words":
		return "Words"
	}
	return name
}

// proofCardCheck is the run's card_limit_held check, shown first in the
// replay, with ok=false when the run had none (an error run).
func proofCardCheck(run apiclient.OpsProofRun) (apiclient.OpsProofCheck, bool) {
	for _, c := range run.Checks {
		if c.Name == "card_limit_held" {
			return c, true
		}
	}
	return apiclient.OpsProofCheck{}, false
}

// proofEvent is one replay line, its well-known fields pulled out.
type proofEvent struct {
	Seq  string
	Kind string
	At   string
	Body string // the line, indented
}

func proofEvents(lines []json.RawMessage) []proofEvent {
	out := make([]proofEvent, 0, len(lines))
	for i, line := range lines {
		var head struct {
			Seq  json.Number `json:"seq"`
			Kind string      `json:"kind"`
			At   string      `json:"at"`
			T    string      `json:"t"`
		}
		_ = json.Unmarshal(line, &head)
		e := proofEvent{Seq: head.Seq.String(), Kind: head.Kind, At: head.At}
		if e.Seq == "" {
			e.Seq = strconv.Itoa(i + 1)
		}
		if e.At == "" {
			e.At = head.T
		}
		var buf bytes.Buffer
		if json.Indent(&buf, line, "", "  ") == nil {
			e.Body = buf.String()
		} else {
			e.Body = string(line)
		}
		out = append(out, e)
	}
	return out
}

// proofRules is the run's rules digests on one line, key=value, sorted.
func proofRules(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || len(m) == 0 {
		return "—"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, ok := m[k].(string)
		if !ok {
			b, _ := json.Marshal(m[k])
			v = string(b)
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " · ")
}

// proofGitHubRun names the GitHub run that sent a run: "123 · attempt 2".
func proofGitHubRun(r apiclient.OpsProofRun) string {
	if r.GitHubRunID == "" {
		return "—"
	}
	if r.GitHubRunAttempt == "" {
		return r.GitHubRunID
	}
	return r.GitHubRunID + " · attempt " + r.GitHubRunAttempt
}

func proofShortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
