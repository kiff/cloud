package purchase

import (
	"encoding/json"
	"os"
	"testing"
)

type contractDoc struct {
	Case struct {
		Value  json.RawMessage `json:"value"`
		Digest string          `json:"digest"`
	} `json:"case"`
}

func contractCase(t *testing.T) (Case, string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/purchase.json")
	if err != nil {
		t.Fatal(err)
	}
	var d contractDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	var c Case
	dec := json.NewDecoder(bytesReader(d.Case.Value))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("the contract's case: %v", err)
	}
	return c, d.Case.Digest
}

// KIFF's digest of the contract's case is the contract's own, as tafs
// computes it: the same canonical JSON on both sides.
func TestTheCasesDigestIsTheContracts(t *testing.T) {
	c, want := contractCase(t)
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	got, err := c.Digest()
	if err != nil || got != want {
		t.Fatalf("digest %s %v, the contract's %s", got, err, want)
	}
	moved := c
	moved.Delivery.Line1 = "1 other street"
	if d, _ := moved.Digest(); d == want {
		t.Fatal("the delivery address isn't in the digest")
	}
}

func TestACaseThatBreaksItsRulesIsntSigned(t *testing.T) {
	base, _ := contractCase(t)
	for name, mutate := range map[string]func(*Case){
		"a total that isn't its parts": func(c *Case) { c.Price.Total++ },
		"a max past the rule":          func(c *Case) { c.Price.Max++ },
		"a cap off the max":            func(c *Case) { c.Grant.Cap-- },
		"two uses":                     func(c *Case) { c.Grant.Uses = 2 },
		"an hour":                      func(c *Case) { c.ValidForSeconds = 3600 },
		"another mode":                 func(c *Case) { c.Mode = "always" },
		"no address":                   func(c *Case) { c.Delivery.Line1 = "" },
		"no path for a card":           func(c *Case) { c.Path = "none" },
		"a lower-case currency":        func(c *Case) { c.Currency = "eur" },
		"an empty rule":                func(c *Case) { c.Price.Rule = &PriceRule{} },
	} {
		c := base
		c.Lines = append([]Line(nil), base.Lines...)
		r := *base.Price.Rule
		c.Price.Rule = &r
		mutate(&c)
		if c.Check() == nil {
			t.Errorf("%s: signed", name)
		}
	}
}

// KIFF's states and where each may go are the contract's.
func TestTheStatesAreTheContracts(t *testing.T) {
	raw, _ := os.ReadFile("testdata/purchase.json")
	var d struct {
		States struct {
			Case []struct {
				State string   `json:"state"`
				Next  []string `json:"next"`
			} `json:"case"`
		} `json:"states"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.States.Case) != len(next) {
		t.Fatalf("%d states in the contract, %d here", len(d.States.Case), len(next))
	}
	for _, s := range d.States.Case {
		got, ok := next[s.State]
		if !ok || len(got) != len(s.Next) {
			t.Fatalf("%s: %v here, %v in the contract", s.State, got, s.Next)
		}
		for _, n := range s.Next {
			if !CanMove(s.State, n) {
				t.Errorf("%s → %s isn't allowed here", s.State, n)
			}
		}
	}
}
