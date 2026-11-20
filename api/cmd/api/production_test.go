package main

import (
	"strings"
	"testing"
)

func TestCheckProductionPosture(t *testing.T) {
	prod := map[string]string{
		envDeployEnv:        "production",
		envAuthProv:         "clerk",
		envDatabaseURL:      "postgresql://x",
		envAnchorPrivateKey: "0xabc",
		envRegionTable:      "/opt/kiff/geo/country.csv.gz",
	}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for kk, vv := range prod {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	cases := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"production, complete", prod, ""},
		{"development skips the checks", map[string]string{envDeployEnv: "development"}, ""},
		{"unset skips the checks", map[string]string{}, ""},
		{"unknown value", map[string]string{envDeployEnv: "prod"}, "unknown KIFF_ENV"},
		{"stub auth", with(envAuthProv, "stub"), "KIFF_AUTH_PROVIDER=clerk"},
		{"auth unset", with(envAuthProv, ""), "KIFF_AUTH_PROVIDER=clerk"},
		{"no database", with(envDatabaseURL, ""), "requires KIFF_DATABASE_URL"},
		{"receipts disabled", with(envReceiptMode, "Disabled"), "KIFF_RECEIPT_MODE=disabled"},
		{"no signing key", with(envAnchorPrivateKey, " "), "random key"},
		{"no region table", with(envRegionTable, ""), "requires KIFF_REGION_TABLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkProductionPosture(func(k string) string { return tc.env[k] })
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
