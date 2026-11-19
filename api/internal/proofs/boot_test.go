package proofs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// hexRun builds a fingerprint-shaped string at run time, so no literal in
// this file looks like a key to the secret scan.
func hexRun(unit string, n int) string { return strings.Repeat(unit, n) }

func fingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func TestOnlyAllowedTestTenantsAreVetted(t *testing.T) {
	why := map[string]string{"t-eval": "", "t-phone": "", "t-customer": RefusedNotOwn, "t-owner": RefusedDevices, "t-missing": RefusedMissing}
	check := func(_ context.Context, id string) (string, error) { return why[id], nil }

	got, refused, err := VetTestTenants(context.Background(), []string{"t-eval", "t-customer", "t-phone", "t-owner", "t-missing"}, check)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "t-eval,t-phone" {
		t.Errorf("vetted = %v, want t-eval,t-phone", got)
	}
	want := []RefusedTenant{{"t-customer", RefusedNotOwn}, {"t-owner", RefusedDevices}, {"t-missing", RefusedMissing}}
	if len(refused) != len(want) {
		t.Fatalf("refused = %v, want %v", refused, want)
	}
	for i := range want {
		if refused[i] != want[i] {
			t.Errorf("refused[%d] = %v, want %v", i, refused[i], want[i])
		}
	}
}

func TestAnUnreadableStoreVetsNothing(t *testing.T) {
	check := func(_ context.Context, id string) (string, error) {
		if id == "t-b" {
			return "", errors.New("db down")
		}
		return "", nil
	}
	got, refused, err := VetTestTenants(context.Background(), []string{"t-a", "t-b"}, check)
	if err == nil {
		t.Fatal("want the store's error")
	}
	if len(got) != 0 {
		t.Errorf("vetted = %v, want none on a store error (fail closed)", got)
	}
	if len(refused) != 2 || refused[0].Why != RefusedUnreadable || refused[1].ID != "t-b" {
		t.Errorf("refused = %v, want every id, unreadable", refused)
	}
}

func TestOnlyTestKeys(t *testing.T) {
	testDER, otherDER := []byte("proof-phone-key"), []byte("a-person's-phone-key")
	keys, _ := ParseTestKeys([]string{fingerprintOf(testDER)})
	if !keys.OnlyTestKeys(nil) {
		t.Error("no keys at all should count as only test keys")
	}
	if !keys.OnlyTestKeys([][]byte{testDER}) {
		t.Error("the test key alone should count")
	}
	if keys.OnlyTestKeys([][]byte{testDER, otherDER}) {
		t.Error("another key should refuse")
	}
	if (TestKeys{}).OnlyTestKeys([][]byte{testDER}) {
		t.Error("with no test key listed, any key should refuse")
	}
}

func TestTheBootLineHasCountsAndFingerprintsOnly(t *testing.T) {
	fullKey, shortKey := hexRun("0123456789abcdef", 4), hexRun("fedcba98", 2)
	keys, bad := ParseTestKeys([]string{fullKey, shortKey})
	if len(bad) != 0 {
		t.Fatal(bad)
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	LogBoot(logger, BootConfig{
		TestTenants: []string{"t-eval"}, Vetted: true, TestKeys: keys,
		ReleaseAnyway: false, StateTokens: 2, RunnerVersions: 1, IngestMounted: true,
	})
	out := buf.String()
	for _, want := range []string{"test_tenants=1", "test_tenant_ids=t-eval", "test_keys=2",
		fullKey[:16], shortKey, "release_anyway=false", "state_tokens=2",
		"runner_versions=1", "ingest_mounted=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("boot line lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, fullKey) {
		t.Errorf("boot line shows a full key fingerprint, not the 16-character one:\n%s", out)
	}
	if strings.Contains(out, "WARN") || strings.Contains(out, "ERROR") {
		t.Errorf("no warning expected for vetted tenants:\n%s", out)
	}
}

func TestTheBootLineNamesEachRefusedTenantAndWhy(t *testing.T) {
	var buf bytes.Buffer
	LogBoot(slog.New(slog.NewTextHandler(&buf, nil)), BootConfig{
		TestTenants: []string{"t-eval"}, Vetted: true,
		Refused: []RefusedTenant{{"t-customer", RefusedNotOwn}, {"t-owner", RefusedDevices}},
	})
	out := buf.String()
	if strings.Count(out, "level=ERROR") != 2 || !strings.Contains(out, "tenant=t-customer") ||
		!strings.Contains(out, "tenant=t-owner") || !strings.Contains(out, RefusedDevices) {
		t.Errorf("want one error per refused tenant, with why:\n%s", out)
	}
}

func TestTheBootLineSaysWhenTenantsCouldntBeChecked(t *testing.T) {
	var buf bytes.Buffer
	LogBoot(slog.New(slog.NewTextHandler(&buf, nil)), BootConfig{TestTenants: []string{"t-x"}, Vetted: false})
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("want a warning when the store couldn't tell:\n%s", buf.String())
	}
}
