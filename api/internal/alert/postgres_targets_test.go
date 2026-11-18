package alert_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
)

// A target's signing secret is stored, read back for delivery, and
// replaced by a rotation; rotating another account's target finds
// nothing.
func TestPostgresStore_SigningSecret(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	if err := alert.ApplySchema(context.Background(), pool); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	tt := provisionTenantRow(t, pool)
	store := alert.NewPostgresStore(pool)
	c := context.Background()
	if err := store.CreateTarget(c, alert.Target{ID: "alrt_1", TenantID: tt.ID, URL: "https://h/1", Enabled: true,
		CreatedAt: time.Now().UTC(), Secret: "whsec_one"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListTargets(c, tt.ID)
	if err != nil || len(got) != 1 || got[0].Secret != "whsec_one" {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if ok, err := store.RotateSecret(c, tt.ID, "alrt_1", "whsec_two"); err != nil || !ok {
		t.Fatalf("rotate: %v %v", ok, err)
	}
	if got, _ := store.ListTargets(c, tt.ID); got[0].Secret != "whsec_two" {
		t.Errorf("after rotation secret = %q", got[0].Secret)
	}
	if ok, err := store.RotateSecret(c, "other-tenant", "alrt_1", "whsec_x"); err != nil || ok {
		t.Errorf("rotated another account's target: %v %v", ok, err)
	}
}

// A target an earlier app registered at an app backend gets the outcomes
// phones take now, once: a request for more tasks and its answer reach it.
// A target elsewhere, or one that never took held calls, is left as is.
func TestPostgresStore_AddPhoneOutcomes(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	if err := alert.ApplySchema(context.Background(), pool); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	tt := provisionTenantRow(t, pool)
	store := alert.NewPostgresStore(pool)
	c := context.Background()
	for _, tg := range []alert.Target{
		{ID: "phone", URL: "https://app/v1/kiff/alerts", Outcomes: []string{"owner_required", "hold_answered"}},
		{ID: "elsewhere", URL: "https://hooks.example/k", Outcomes: []string{"owner_required"}},
		{ID: "denials", URL: "https://app/v1/kiff/alerts", Outcomes: []string{"denied"}},
	} {
		tg.TenantID, tg.Enabled, tg.CreatedAt = tt.ID, true, time.Now().UTC()
		if err := store.CreateTarget(c, tg); err != nil {
			t.Fatal(err)
		}
	}
	add := []string{alert.PackRequested, alert.PackRequestAnswered}
	n, err := store.AddPhoneOutcomes(c, []string{"https://app/v1/kiff/alerts"}, add)
	if err != nil || n != 1 {
		t.Fatalf("changed %d, %v", n, err)
	}
	if n, err := store.AddPhoneOutcomes(c, []string{"https://app/v1/kiff/alerts"}, add); err != nil || n != 0 {
		t.Fatalf("again: changed %d, %v", n, err)
	}
	got, _ := store.ListTargets(c, tt.ID)
	byID := map[string][]string{}
	for _, tg := range got {
		byID[tg.ID] = tg.Outcomes
	}
	if want := []string{"owner_required", "hold_answered", alert.PackRequested, alert.PackRequestAnswered}; !slices.Equal(byID["phone"], want) {
		t.Errorf("phone target: %v", byID["phone"])
	}
	if !slices.Equal(byID["elsewhere"], []string{"owner_required"}) || !slices.Equal(byID["denials"], []string{"denied"}) {
		t.Errorf("left alone: %v", byID)
	}
}

// A target a phone registered before connection_signed_out existed gets it
// at start, with the list KIFF's start uses; one that has it is left alone.
func TestPostgresStore_OlderPhoneTargetsGetConnectionSignedOut(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	if err := alert.ApplySchema(context.Background(), pool); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	tt := provisionTenantRow(t, pool)
	store := alert.NewPostgresStore(pool)
	c := context.Background()
	older := alert.Target{ID: "phone", TenantID: tt.ID, URL: "https://app/v1/kiff/alerts", Enabled: true, CreatedAt: time.Now().UTC(),
		Outcomes: []string{"owner_required", alert.HoldAnswered, alert.PackRequested, alert.PackRequestAnswered}}
	if err := store.CreateTarget(c, older); err != nil {
		t.Fatal(err)
	}
	if n, err := store.AddPhoneOutcomes(c, []string{"https://app/v1/kiff/alerts"}, alert.PhoneOutcomesAdded); err != nil || n != 1 {
		t.Fatalf("changed %d, %v", n, err)
	}
	got, _ := store.ListTargets(c, tt.ID)
	if len(got) != 1 || !got[0].Matches("", alert.ConnectionSignedOut) {
		t.Fatalf("the older target doesn't take connection_signed_out: %+v", got)
	}
	if n, _ := store.AddPhoneOutcomes(c, []string{"https://app/v1/kiff/alerts"}, alert.PhoneOutcomesAdded); n != 0 {
		t.Errorf("a second start changed %d", n)
	}
}
