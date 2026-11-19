package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
)

func renderEvidence(t *testing.T, v pages.DashboardDomainEvidenceView) string {
	t.Helper()
	var buf bytes.Buffer
	if err := pages.DashboardDomainEvidence(v).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render evidence: %v", err)
	}
	return buf.String()
}

func evidenceWithRatio(r *apiclient.ReviewRatio) pages.DashboardDomainEvidenceView {
	return pages.DashboardDomainEvidenceView{
		DomainName: "refund-control",
		Tab:        "decisions",
		// The summary strip renders only for a signed-in view.
		Me:    &apiclient.Me{},
		Ratio: r,
		Evidence: &pages.ControlEvidence{
			Decisions: pages.ControlEvidenceDecisions{Total: 100},
		},
	}
}

// The figure and the counts have to agree, because a reader will check
// one against the other in front of us.
func TestRatioPanelShowsTheFigureAndItsSplit(t *testing.T) {
	t.Parallel()
	body := renderEvidence(t, evidenceWithRatio(&apiclient.ReviewRatio{
		Status: "ok", Percent: 84, Total: 100,
		WouldRun: 84, WouldReachAPerson: 12, WouldBeRefused: 4,
		Enforcing: true, Note: "Share of proposed actions that ran without reaching a person.",
	}))

	for _, want := range []string{
		"84%", "of 100 proposed actions", "ran without reaching a person",
		"84</b> needed nobody", "12</b> reached a person",
		"4</b> refused before anyone looked",
	} {
		if !strings.Contains(collapse(body), collapse(want)) {
			t.Errorf("panel missing %q", want)
		}
	}
}

// In observe mode nothing was withheld. Saying "ran" would claim we
// stopped something we did not, which is the error a customer catches.
func TestObserveModeReadsAsCounterfactual(t *testing.T) {
	t.Parallel()
	body := collapse(renderEvidence(t, evidenceWithRatio(&apiclient.ReviewRatio{
		Status: "ok", Percent: 84, Total: 100, WouldRun: 84, Enforcing: false,
	})))

	if !strings.Contains(body, "would have run without reaching a person") {
		t.Error("observe mode did not render in the counterfactual tense")
	}
}

// A window too thin to answer must not render as a measured zero. It
// shows a dash and says how many decisions it would need.
func TestAThinWindowRendersAsUnanswerableNotZero(t *testing.T) {
	t.Parallel()
	body := collapse(renderEvidence(t, evidenceWithRatio(&apiclient.ReviewRatio{
		Status: "insufficient_sample", Total: 4, MinimumSample: 30,
		Note: "Fewer decisions than this figure needs to mean anything.",
	})))

	if strings.Contains(body, "0%") {
		t.Error("an unanswerable window rendered as 0%")
	}
	if !strings.Contains(body, "4 decisions in this window; this figure needs 30") {
		t.Error("the panel does not say how short the window is")
	}
}

// A failed read leaves the panel out rather than showing a confident
// figure built from nothing.
func TestAnUnreadableRatioRendersNothing(t *testing.T) {
	t.Parallel()
	body := renderEvidence(t, evidenceWithRatio(nil))

	if strings.Contains(body, "cr-ratio") {
		t.Error("the ratio panel rendered with no ratio to show")
	}
}

// The ratio sits directly above the outcome tiles, and a reader will
// check one against the other. It must therefore be scoped to the same
// control: a tenant-wide figure over domain-scoped counts is two
// numbers that cannot reconcile, which costs trust in both.
func TestRatioIsScopedToTheControlTheTilesShow(t *testing.T) {
	t.Parallel()

	var gotControl string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/me/review-ratio" {
			gotControl = r.URL.Query().Get("control")
			_, _ = w.Write([]byte(`{"status":"ok","percent":80,"total":44,"would_run":35}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, err := c.ReviewRatioBearer(context.Background(), "tok", "ctl-refunds", "",
		time.Now().Add(-7*24*time.Hour), time.Now()); err != nil {
		t.Fatalf("ratio: %v", err)
	}
	if gotControl != "ctl-refunds" {
		t.Errorf("control = %q, want ctl-refunds; an unscoped ratio does not reconcile with the tiles beneath it", gotControl)
	}
}
