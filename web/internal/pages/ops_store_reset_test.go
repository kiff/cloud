package pages

import (
	"context"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
)

// The reset's read-back says what it set aside of the account's store packs.
func TestTheStoreResetReadBackShowsThePacksSetAside(t *testing.T) {
	for want, p := range map[string]*apiclient.OpsStorePacksSetAside{
		"2 packs, 5500 credits": {Packs: 2, Credits: 5500},
		"1 pack, 500 credits":   {Packs: 1, Credits: 500},
		"none":                  {},
		"not read back":         nil,
	} {
		var b strings.Builder
		v := &apiclient.OpsStoreReset{ID: "t1", Plan: "trial", From: "solo", Trial: "not_started", PacksSetAside: p}
		if err := OpsStoreResetResult("t1", v, "", components.DashboardChrome{}).Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), "<dt>Store packs set aside</dt><dd>"+want+"</dd>") {
			t.Fatalf("want %q in the read-back", want)
		}
	}
}
