package profile

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden vectors in testdata/valid/*.expected.json")

// expected is a golden vector: what KIFF answers for a profile. planque,
// tafs and both apps test against the same files.
type expected struct {
	ProfileSHA256 string `json:"profile_sha256"`
	Words         Words  `json:"words"`
	WordsSHA256   string `json:"words_sha256"`
}

func TestGoldenVectors(t *testing.T) {
	files, _ := filepath.Glob("testdata/valid/*.json")
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, ".expected.json") {
			continue
		}
		n++
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			p, err := Parse(raw)
			if err != nil {
				t.Fatalf("a valid vector is refused: %v", err)
			}
			got := expected{Words: Render(p)}
			if got.ProfileSHA256, err = Digest(raw); err != nil {
				t.Fatal(err)
			}
			if got.WordsSHA256, err = WordsDigest(got.Words); err != nil {
				t.Fatal(err)
			}
			out, _ := json.MarshalIndent(got, "", "  ")
			out = append(out, '\n')
			golden := strings.TrimSuffix(f, ".json") + ".expected.json"
			if *update {
				if err := os.WriteFile(golden, out, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("no golden vector (go test -run TestGoldenVectors -update): %v", err)
			}
			if !bytes.Equal(out, want) {
				t.Errorf("%s changed. Words an owner signed must render the same: a new text is a new WordsVersion.\ngot:\n%s", golden, out)
			}
		})
	}
	if n < 5 {
		t.Fatalf("only %d valid vectors", n)
	}
}

func TestInvalidVectorsAreRefused(t *testing.T) {
	want := map[string]string{
		"no-effect-drafted.json":    "floor rule 1",
		"no-effect-5xx.json":        "503 can't mean nothing happened",
		"mcp-pinned.json":           "can't be pinned",
		"money-without-amount.json": "says where its amount is",
		"unknown-field.json":        `unknown field "releases_on"`,
		"check-by-a-write.json":     "only a read can check",
		"array-in-call-path.json":   "names only",
	}
	for name, msg := range want {
		raw, err := os.ReadFile(filepath.Join("testdata/invalid", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, msg)
		}
	}
}

// The digest is over the profile's JCS form: key order and whitespace
// don't change it, a value does.
func TestDigestIgnoresLayoutNotValues(t *testing.T) {
	a := []byte(`{"v":"kiff.profile.v1","vendor":"Notion","transport":"mcp","pin":{"kind":"none"},"source":"drafted","test_proven":false,"operations":[{"tool":"s","title":"Search","kind":"read"}]}`)
	b := []byte("{\n \"vendor\": \"Notion\", \"v\": \"kiff.profile.v1\", \"pin\": {\"kind\": \"none\"}, \"transport\": \"mcp\",\n \"test_proven\": false, \"source\": \"drafted\", \"operations\": [{\"kind\": \"read\", \"title\": \"Search\", \"tool\": \"s\"}]}")
	c := bytes.Replace(a, []byte(`"Search"`), []byte(`"Search pages"`), 1)
	da, _ := Digest(a)
	db, _ := Digest(b)
	dc, _ := Digest(c)
	if da != db || da == dc || len(da) != 64 {
		t.Fatalf("digests: %s %s %s", da, db, dc)
	}
}

// kiff/tafs#498: an operation may say where the owner looks at the vendor's
// record of its resource (record_url, reviewed catalog only) and which read
// gives the resource's own name (label_read); the id is path-escaped into the
// address, and an id a browser would read as a step up gives no link.
func TestRecordURLAndLabelRead(t *testing.T) {
	raw, err := os.ReadFile("testdata/valid/stripe-refunds.json")
	if err != nil {
		t.Fatal(err)
	}
	with := func(changes map[string]any, source string) error {
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		if source != "" {
			p["source"] = source
			delete(p, "catalog_entry")
		}
		for _, o := range p["operations"].([]any) {
			if op := o.(map[string]any); op["tool"] == "create_refund" {
				if source != "" {
					delete(op, "no_effect") // a drafted profile has none either
				}
				for k, v := range changes {
					op[k] = v
				}
			}
		}
		out, _ := json.Marshal(p)
		_, err := Parse(out)
		return err
	}
	good := map[string]any{"record_url": "https://dashboard.stripe.com/payments/{resource}",
		"label_read": map[string]any{"tool": "get_payment_intent", "args": map[string]string{"payment_intent": "$args.payment_intent"}, "path": "description"}}
	if err := with(good, ""); err != nil {
		t.Fatalf("a record_url and a label_read on a catalog profile: %v", err)
	}
	for name, c := range map[string]struct {
		changes map[string]any
		source  string
		want    string
	}{
		"a drafted profile's record_url":  {map[string]any{"record_url": "https://dashboard.stripe.com/payments/{resource}"}, "drafted", "reviewed catalog only"},
		"http":                            {map[string]any{"record_url": "http://dashboard.stripe.com/payments/{resource}"}, "", "https"},
		"credentials in it":               {map[string]any{"record_url": "https://user:pw@localhost/{resource}"}, "", "no credentials"},
		"two placeholders":                {map[string]any{"record_url": "https://dashboard.stripe.com/{resource}/{resource}"}, "", "at most once"},
		"another brace":                   {map[string]any{"record_url": "https://dashboard.stripe.com/{account}/payments"}, "", "only placeholder"},
		"the placeholder as the host":     {map[string]any{"record_url": "https://{resource}/payments"}, "", "path"},
		"the placeholder in the query":    {map[string]any{"record_url": "https://dash.example.com/resources/view?id={resource}"}, "", "path"},
		"the placeholder in the fragment": {map[string]any{"record_url": "https://dash.example.com/resource#{resource}"}, "", "path"},
		"a label_read that is a write":    {map[string]any{"label_read": map[string]any{"tool": "create_refund", "path": "id"}}, "", "is a write"},
		"a label_read's bad path":         {map[string]any{"label_read": map[string]any{"tool": "get_payment_intent", "path": "../x"}}, "", "dotted path"},
	} {
		if err := with(c.changes, c.source); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	for id, want := range map[string]string{
		"pi_3Abc":     "https://dashboard.stripe.com/payments/pi_3Abc",
		"../../admin": "https://dashboard.stripe.com/payments/..%2F..%2Fadmin",
		"x?y=1#z":     "https://dashboard.stripe.com/payments/x%3Fy=1%23z",
		"a@evil/b":    "https://dashboard.stripe.com/payments/a@evil%2Fb",
		"":            "",
		".":           "",
		"..":          "",
	} {
		if got := RecordURL("https://dashboard.stripe.com/payments/{resource}", id); got != want {
			t.Errorf("%q: %s, want %s", id, got, want)
		}
	}
	if got := RecordURL("https://dashboard.stripe.com/payments", "pi_1"); got != "https://dashboard.stripe.com/payments" {
		t.Errorf("no placeholder: %s", got)
	}
}

// thune step one: a refund's case block names reads of the same profile
// whose only reference is the call's charge, finds KIFF's own refunds by
// the operation's marker, and says where a dispute shows.
func TestACaseBlock(t *testing.T) {
	raw, err := os.ReadFile("testdata/valid/stripe-refunds.json")
	if err != nil {
		t.Fatal(err)
	}
	good := func() map[string]any {
		return map[string]any{
			"kind":         "refund",
			"charge_arg":   "payment_intent",
			"account_read": map[string]any{"tool": "get_account", "path": "id"},
			"charge_read": map[string]any{"tool": "get_payment_intent", "args": map[string]any{"payment_intent": "$args.payment_intent"},
				"currency": "currency", "captured": "amount_received", "disputed": "latest_charge.disputed"},
			"refunds_read": map[string]any{"tool": "list_refunds", "args": map[string]any{"payment_intent": "$args.payment_intent"},
				"list": "data", "id": "id", "amount": "amount", "status": "status", "marker": "metadata.kiff_op"},
			"refund_event": map[string]any{"event": "STRIPE_REFUND_UPDATED_TRIGGER", "object": "object", "charge": "payment_intent"},
		}
	}
	with := func(edit func(c map[string]any, op map[string]any)) error {
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		ops := p["operations"].([]any)
		p["operations"] = append(ops, map[string]any{"tool": "get_account", "title": "Look up the account", "kind": "read"})
		for _, o := range ops {
			if op := o.(map[string]any); op["tool"] == "create_refund" {
				// A case's refund has a fixed currency, which KIFF compares
				// with the charge's.
				a := op["amount"].(map[string]any)
				delete(a, "unit_read")
				a["unit"] = "EUR"
				c := good()
				edit(c, op)
				op["case"] = c
			}
		}
		b, _ := json.Marshal(p)
		_, err := Parse(b)
		return err
	}
	if err := with(func(map[string]any, map[string]any) {}); err != nil {
		t.Fatalf("a good case block: %v", err)
	}
	// The same case block in a drafted profile is refused: a refund's money
	// rules are reviewed code.
	{
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		p["source"] = "drafted"
		delete(p, "catalog_entry")
		delete(p, "test_proven")
		ops := p["operations"].([]any)
		p["operations"] = append(ops, map[string]any{"tool": "get_account", "title": "Look up the account", "kind": "read"})
		for _, o := range ops {
			if op := o.(map[string]any); op["tool"] == "create_refund" {
				op["case"] = good()
				delete(op, "no_effect")
			}
		}
		b, _ := json.Marshal(p)
		if _, err := Parse(b); err == nil || !strings.Contains(err.Error(), "case block comes only") {
			t.Fatalf("a drafted case block: %v", err)
		}
	}
	if err := with(func(c map[string]any, _ map[string]any) {
		cr := c["charge_read"].(map[string]any)
		delete(cr, "disputed")
		cr["dispute_status"], cr["dispute_open"] = "dispute.status", []string{"needs_response", "under_review"}
	}); err != nil {
		t.Fatalf("a dispute status: %v", err)
	}
	// A tool narrowed to one operation (planque's stripe_api_write) gets its
	// marker inside the operation's parameters; the refund it makes carries
	// it without them. Exactly that prefix, and nothing looser.
	if err := with(func(_, op map[string]any) {
		op["marker"] = map[string]any{"field": NarrowedParams + ".metadata.kiff_op"}
	}); err != nil {
		t.Fatalf("a marker inside parameters: %v", err)
	}
	for _, field := range []string{"params.metadata.kiff_op", "parameters.parameters.metadata.kiff_op", "x.parameters.metadata.kiff_op", "metadata"} {
		if err := with(func(_, op map[string]any) { op["marker"] = map[string]any{"field": field} }); err == nil || !strings.Contains(err.Error(), "refunds_read.marker") {
			t.Errorf("a marker at %s: %v", field, err)
		}
	}
	for name, edit := range map[string]func(c, op map[string]any){
		"another kind": func(c, _ map[string]any) { c["kind"] = "coupon" },
		"no fixed unit": func(_, op map[string]any) {
			a := op["amount"].(map[string]any)
			delete(a, "unit")
			a["unit_read"] = map[string]any{"tool": "get_payment_intent", "path": "currency"}
		},
		"no marker":         func(_, op map[string]any) { delete(op, "marker") },
		"another marker":    func(c, _ map[string]any) { c["refunds_read"].(map[string]any)["marker"] = "metadata.other" },
		"a write as a read": func(c, _ map[string]any) { c["account_read"].(map[string]any)["tool"] = "create_refund" },
		"an unknown read":   func(c, _ map[string]any) { c["charge_read"].(map[string]any)["tool"] = "get_charge" },
		"another reference": func(c, _ map[string]any) {
			c["refunds_read"].(map[string]any)["args"] = map[string]any{"x": "$args.amount"}
		},
		"a result reference": func(c, _ map[string]any) {
			c["charge_read"].(map[string]any)["args"] = map[string]any{"x": "$result.id"}
		},
		"no dispute": func(c, _ map[string]any) { delete(c["charge_read"].(map[string]any), "disputed") },
		"both disputes": func(c, _ map[string]any) {
			c["charge_read"].(map[string]any)["dispute_status"] = "dispute.status"
			c["charge_read"].(map[string]any)["dispute_open"] = []string{"open"}
		},
		"open values without a status": func(c, _ map[string]any) { c["charge_read"].(map[string]any)["dispute_open"] = []string{"open"} },
		"a bad path":                   func(c, _ map[string]any) { c["refunds_read"].(map[string]any)["amount"] = "data[x]" },
		"a bad charge arg":             func(c, _ map[string]any) { c["charge_arg"] = "$args.payment_intent" },
		"not moving money":             func(_, op map[string]any) { op["effect"] = "records_money" },
		"an unknown field":             func(c, _ map[string]any) { c["claim"] = true },
		"a bad event slug":             func(c, _ map[string]any) { c["refund_event"].(map[string]any)["event"] = "two words" },
		"a bad event path":             func(c, _ map[string]any) { c["refund_event"].(map[string]any)["object"] = "data[x]" },
	} {
		if err := with(edit); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A write's claim names a kind of action on its resource; claim_related a
// related refund's charge, by its path. A read takes neither, and a claim
// needs a resource.
func TestAClaimAndAClaimRelated(t *testing.T) {
	raw, err := os.ReadFile("testdata/valid/stripe-refunds.json")
	if err != nil {
		t.Fatal(err)
	}
	with := func(tool string, changes map[string]any) error {
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		for _, o := range p["operations"].([]any) {
			if op := o.(map[string]any); op["tool"] == tool {
				for k, v := range changes {
					if v == nil {
						delete(op, k)
					} else {
						op[k] = v
					}
				}
			}
		}
		out, _ := json.Marshal(p)
		_, err := Parse(out)
		return err
	}
	related := map[string]any{"kind": "refund", "path": "charge"}
	if err := with("create_refund", map[string]any{"claim": "dispute_answer", "claim_related": related}); err != nil {
		t.Fatalf("a claim and a claim_related on a write with a resource: %v", err)
	}
	for name, c := range map[string]struct {
		tool    string
		changes map[string]any
	}{
		"a claim that isn't a kind":        {"create_refund", map[string]any{"claim": "Dispute Answer"}},
		"a claim too long":                 {"create_refund", map[string]any{"claim": "a_kind_of_action_named_far_too_long"}},
		"a claim without a resource":       {"create_refund", map[string]any{"claim": "dispute_answer", "resource": nil}},
		"a claim on a read":                {"list_refunds", map[string]any{"claim": "dispute_answer"}},
		"a related kind that isn't refund": {"create_refund", map[string]any{"claim_related": map[string]any{"kind": "invoice", "path": "charge"}}},
		"a related path that isn't one":    {"create_refund", map[string]any{"claim_related": map[string]any{"kind": "refund", "path": "$args.charge"}}},
		"a claim_related on a read":        {"list_refunds", map[string]any{"claim_related": related}},
	} {
		if err := with(c.tool, c.changes); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
