package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/gatewayclient"
	"github.com/kiff/cloud/web/internal/pages"
)

// The review is the owner's last look before a Card is issued, so it must
// state the amount the server will issue, read the way that server parser
// reads it (a review: "250.50" was previewed as €25,050 a day).
//
// The web binary carries no JS toolchain, so this drives the real pages
// and their real script in headless Chrome when one is installed (or
// named by KIFF_TEST_CHROME), and skips otherwise. Each value is typed
// into the actual field, and the review text is compared with what the
// Go parser makes of the same value.
func TestWalkthroughReviewStatesTheIssuedAmount(t *testing.T) {
	t.Parallel()
	chrome := findChrome()
	if chrome == "" || testing.Short() {
		t.Skip("needs Chrome (or KIFF_TEST_CHROME) and no -short: it runs the walkthrough's script")
	}

	money := []string{"1000", "1,000", "1 000", "€1000", "250.50", "250,50", "250.5", "1,000.25", "0.01", "0.50",
		"", "0", "-5", "abc", "1.234", "12e3", "1000000000000000", "1.2.3"}
	counts := []string{"1,200", "40", "0", "2.5", "x"}
	toolCalls := []string{"1,000", "20", "1 000", "0", "2.5", "x"}
	issueLimits := []string{"1000", "+5", "1,000", "10.5", " 7 "}

	none := "—"
	var cases []walkCase
	for _, in := range money {
		want := none
		if c, err := parseTaskLimit(in, true); err == nil {
			want = euros(c) + " a day"
		}
		cases = append(cases, walkCase{Page: "quick", Task: "refunds", In: in, Want: want})
	}
	for _, in := range counts {
		want := none
		if n, err := parseTaskLimit(in, false); err == nil {
			want = groupThousands(n) + " a day"
		}
		cases = append(cases, walkCase{Page: "quick", Task: "plan-changes", In: in, Want: want})
	}
	for _, in := range toolCalls {
		want := none
		if n, ok, _ := optionalPositive(in, "calls"); ok {
			want = groupThousands(n) + " calls · each day"
		}
		cases = append(cases, walkCase{Page: "tool", In: in, Want: want})
	}
	for _, in := range issueLimits {
		want := none
		if n, err := strconv.ParseInt(strings.TrimSpace(in), 10, 64); err == nil && n > 0 {
			want = groupThousands(n) + " actions · each day"
		}
		cases = append(cases, walkCase{Page: "issue", In: in, Want: want})
	}

	pagesByName := map[string]templ.Component{
		"quick": pages.CardQuick(pages.CardQuickView{}),
		"tool":  pages.ToolCard(pages.ToolCardView{Tool: gatewayclient.Connection{Name: "lookup"}, Form: pages.ToolCardForm{Agent: "a", Window: "calendar_day"}}),
		"issue": pages.CardNew(pages.CardNewView{SelectedDomain: "d", Actions: []apiclient.DomainAction{{Name: "X"}}}),
	}
	for name, page := range pagesByName {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var mine []walkCase
			for _, c := range cases {
				if c.Page == name {
					mine = append(mine, c)
				}
			}
			got := runWalkCases(t, chrome, page, mine)
			for i, c := range mine {
				if got[i] != c.Want {
					t.Errorf("%q (task %q): review says %q, server issues %q", c.In, c.Task, got[i], c.Want)
				}
			}
		})
	}
}

type walkCase struct {
	Page, Task, In, Want string
}

func euros(cents int64) string {
	s := "€" + groupThousands(cents/100)
	if c := cents % 100; c != 0 {
		s += "." + leftPad2(c)
	}
	return s
}

// runWalkCases renders page, then types each case into its field after
// the walkthrough script has run, and returns the review text it shows.
func runWalkCases(t *testing.T, chrome string, page templ.Component, cases []walkCase) []string {
	t.Helper()
	var buf bytes.Buffer
	if err := page.Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	in, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	driver := `<pre id="wk-out"></pre><script>
(function () {
  var form = document.querySelector(".wk form"), out = [];
  function q(s) { return form.querySelector(s); }
  JSON.parse(` + strconv.Quote(string(in)) + `).forEach(function (c) {
    var read;
    if (c.Page === "quick") {
      q("[name=task][value=" + c.Task + "]").checked = true;
      q("[name=limit]").value = c.In;
      read = "dd[data-wk-val=quick-limit]";
    } else if (c.Page === "tool") {
      q("[name=calls]").value = c.In;
      read = "dd[data-wk-val=tool-limit]";
    } else {
      q("[name=limit]").value = c.In;
      read = "[data-wk-row=limit]";
    }
    form.dispatchEvent(new Event("input", { bubbles: true }));
    out.push(q(read).textContent);
  });
  document.getElementById("wk-out").textContent = JSON.stringify(out);
})();
</script></body>`
	html := strings.Replace(buf.String(), "</body>", driver, 1)
	path := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(path, []byte(html), 0o600); err != nil {
		t.Fatal(err)
	}
	dom, err := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-sandbox",
		"--virtual-time-budget=1000", "--dump-dom", "file://"+path).Output()
	if err != nil {
		t.Fatalf("chrome: %v", err)
	}
	s := string(dom)
	i, j := strings.Index(s, `<pre id="wk-out">`), strings.Index(s, "</pre>")
	if i < 0 || j < i {
		t.Fatalf("the driver did not run")
	}
	raw := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(s[i+len(`<pre id="wk-out">`) : j])
	var got []string
	if err := json.Unmarshal([]byte(raw), &got); err != nil || len(got) != len(cases) {
		t.Fatalf("driver output %q: %v", raw, err)
	}
	return got
}

func findChrome() string {
	if p := os.Getenv("KIFF_TEST_CHROME"); p != "" {
		return p
	}
	for _, p := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// Connecting an approver: the amount step follows the chosen action, so
// the unit the owner sees is the unit granted. Two actions with
// different units and precisions; choosing the second shows its
// threshold and unit, and choosing back starts again from the first
// one's, even after the amount was edited.
func TestApproverAmountFollowsTheChosenAction(t *testing.T) {
	t.Parallel()
	scopes := []pages.ApproverScope{
		{Action: "refund_order", Parameter: "amount_eur", Unit: "EUR", Scale: 2, PerAction: 125000},
		{Action: "issue_credit", Parameter: "credit_jpy", Unit: "JPY", Scale: 0, PerAction: 7000},
	}
	page := pages.CardSheet(pages.CardSheetView{Kind: "approver-new", Card: apiclient.Mandate{ID: "c", Subject: "a", OnExceed: "owner"}, Scopes: scopes})
	var buf bytes.Buffer
	if err := page.Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-amount="1250.00" data-unit="EUR" data-step="0.01"`, `data-amount="7000" data-unit="JPY" data-step="1"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("a scope lacks its own amount: %s", want)
		}
	}
	chrome := findChrome()
	if chrome == "" || testing.Short() {
		t.Skip("needs Chrome (or KIFF_TEST_CHROME) and no -short: it runs the walkthrough's script")
	}
	driver := `<pre id="wk-out"></pre><script>
(function () {
  var form = document.querySelector(".wk form"), out = [];
  function q(s) { return form.querySelector(s); }
  function pick(i) { var r = form.querySelectorAll("[name=scope]")[i]; r.checked = true; r.dispatchEvent(new Event("change", { bubbles: true })); }
  function read() { out.push(q("[name=max]").value + " " + q("[data-wk-scope-unit]").textContent + " " + q("[name=max]").getAttribute("step")); }
  read(); pick(1); read(); q("[name=max]").value = "123"; pick(0); read();
  document.getElementById("wk-out").textContent = JSON.stringify(out);
})();
</script></body>`
	path := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(path, []byte(strings.Replace(buf.String(), "</body>", driver, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	dom, err := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--virtual-time-budget=1000", "--dump-dom", "file://"+path).Output()
	if err != nil {
		t.Fatalf("chrome: %v", err)
	}
	s := string(dom)
	i, j := strings.Index(s, `<pre id="wk-out">`), strings.Index(s, "</pre>")
	if i < 0 || j < i {
		t.Fatal("the driver did not run")
	}
	var got []string
	if err := json.Unmarshal([]byte(strings.ReplaceAll(s[i+len(`<pre id="wk-out">`):j], "&quot;", `"`)), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"1250.00 EUR 0.01", "7000 JPY 1", "1250.00 EUR 0.01"}
	for k := range want {
		if k >= len(got) || got[k] != want[k] {
			t.Fatalf("amount step = %q, want %q", got, want)
		}
	}
	// What the field is filled with is what the server accepts.
	for k, sc := range scopes {
		if _, err := parseApproverAmount(sc.Input(), sc.Scale); err != nil {
			t.Errorf("scope %d prefill %q is refused: %v", k, sc.Input(), err)
		}
	}
}

// Changing an existing Card opens one step on its own: no progress or
// Continue, Save off until something changes, then the bar says what
// changes.
func TestSingleStepEditSaysWhatChanges(t *testing.T) {
	t.Parallel()
	chrome := findChrome()
	if chrome == "" || testing.Short() {
		t.Skip("needs Chrome (or KIFF_TEST_CHROME) and no -short: it runs the walkthrough's script")
	}
	// A count Card in a domain that can sum renders a per-action amount
	// field the first sync disables; the baseline must not see it as an
	// edit.
	summable := pages.CardNew(pages.CardNewView{SelectedDomain: "d", Actions: []apiclient.DomainAction{{Name: "X", Parameters: []apiclient.DomainParameter{{Name: "amount_eur", Type: "number"}}}}, Editing: "c", Step: "3", Single: true,
		Form: pages.MandateFormValues{ID: "c", MandateSubj: "a", Actions: []string{"X"}, QuantityKind: "count", Limit: "10", Window: "calendar_day"}})
	var sb bytes.Buffer
	if err := summable.Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), `name="per_action_max"`) {
		t.Fatal("the summable fixture does not render the per-action field it is meant to exercise")
	}
	clean := `<pre id="wk-out"></pre><script>
(function () {
  var form = document.querySelector(".wk form");
  var out = [String(form.querySelector("[data-wk-submit]").disabled), String(document.querySelector("[data-wk-change]").hidden)];
  form.querySelector("[name=window][value=rolling_1h]").click();
  form.querySelector("[name=window][value=calendar_day]").click();
  out.push(String(form.querySelector("[data-wk-submit]").disabled));
  document.getElementById("wk-out").textContent = JSON.stringify(out);
})();
</script></body>`
	if got := runDriver(t, chrome, sb.String(), clean); strings.Join(got, ",") != "true,true,true" {
		t.Fatalf("an untouched (or restored) summable count Card reads as changed: %q", got)
	}

	page := pages.CardNew(pages.CardNewView{SelectedDomain: "d", Actions: []apiclient.DomainAction{{Name: "X"}}, Editing: "c", Step: "3", Single: true,
		Form: pages.MandateFormValues{ID: "c", MandateSubj: "a", Actions: []string{"X"}, QuantityKind: "count", Limit: "10", Window: "calendar_day"}})
	var buf bytes.Buffer
	if err := page.Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Save change") || !strings.Contains(buf.String(), "data-wk-single") {
		t.Fatal("the single edit page lacks its Save change bar")
	}
	driver := `<pre id="wk-out"></pre><script>
(function () {
  var root = document.querySelector(".wk"), form = root.querySelector("form"), out = [];
  var submit = form.querySelector("[data-wk-submit]"), next = form.querySelector("[data-wk-next]"), line = root.querySelector("[data-wk-change]");
  var shown = Array.prototype.filter.call(form.querySelectorAll("[data-wk-step]"), function (s) { return !s.hidden; }).map(function (s) { return s.getAttribute("data-wk-step"); });
  out.push(shown.join(","), String(submit.disabled), String(next.hidden), String(line.hidden));
  form.querySelector("[name=limit]").value = "25";
  form.dispatchEvent(new Event("input", { bubbles: true }));
  out.push(String(submit.disabled), line.textContent);
  document.getElementById("wk-out").textContent = JSON.stringify(out);
})();
</script></body>`
	path := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(path, []byte(strings.Replace(buf.String(), "</body>", driver, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	dom, err := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--virtual-time-budget=1000", "--dump-dom", "file://"+path).Output()
	if err != nil {
		t.Fatalf("chrome: %v", err)
	}
	s := string(dom)
	i, j := strings.Index(s, `<pre id="wk-out">`), strings.Index(s, "</pre>")
	if i < 0 || j < i {
		t.Fatal("the driver did not run")
	}
	var got []string
	raw := strings.NewReplacer("&quot;", `"`, "&gt;", ">", "&amp;", "&").Replace(s[i+len(`<pre id="wk-out">`) : j])
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("%q: %v", raw, err)
	}
	want := []string{"3", "true", "true", "true", "false", "Now 10 actions · each day → 25 actions · each day"}
	for k := range want {
		if k >= len(got) || got[k] != want[k] {
			t.Fatalf("single edit = %q, want %q", got, want)
		}
	}
}

// runDriver renders html with driver appended, in headless Chrome, and
// returns the JSON list the driver writes into #wk-out.
func runDriver(t *testing.T, chrome, html, driver string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(path, []byte(strings.Replace(html, "</body>", driver, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	dom, err := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--virtual-time-budget=1000", "--dump-dom", "file://"+path).Output()
	if err != nil {
		t.Fatalf("chrome: %v", err)
	}
	s := string(dom)
	i, j := strings.Index(s, `<pre id="wk-out">`), strings.Index(s, "</pre>")
	if i < 0 || j < i {
		t.Fatal("the driver did not run")
	}
	var got []string
	raw := strings.NewReplacer("&quot;", `"`, "&gt;", ">", "&amp;", "&").Replace(s[i+len(`<pre id="wk-out">`) : j])
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("%q: %v", raw, err)
	}
	return got
}
