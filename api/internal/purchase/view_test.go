package purchase

import "testing"

// The apps' lines add up to the total, and every line is KIFF's.
func TestTheApprovalIsDrawnInKIFFsWords(t *testing.T) {
	c, digest := contractCase(t)
	a := ApprovalOf(Row{Case: c, Digest: digest})
	if a.CaseID != c.CaseID || a.Version != c.Version || a.Digest != digest {
		t.Fatalf("ids %+v", a)
	}
	w := a.Words
	if w.Total != "€84.20" || w.Prompt != "Buy from Papeterie Lumière · €84.20" || w.Grant != "A card for this purchase: up to €86.20, at this shop, used once." ||
		w.PriceRule != "It can go up to €2.00 more for shipping." || len(w.Lines) != 2 || len(w.Charges) != 1 || w.Charges[0].Amount != "€9.50" {
		t.Fatalf("words %+v", w)
	}
	if w.Delivery != "Acme SAS, 12 rue des Lilas, 69003 Lyon, FR" {
		t.Fatalf("delivery %q", w.Delivery)
	}
	if Money(5, "CHF") != "0.05 CHF" || Money(-150, "EUR") != "-€1.50" {
		t.Fatal("money")
	}
}
