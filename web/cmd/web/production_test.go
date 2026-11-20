package main

import (
	"strings"
	"testing"
)

func TestCheckProductionPosture(t *testing.T) {
	cases := []struct {
		env     map[string]string
		wantErr string
	}{
		{map[string]string{envDeployEnv: "production", envAuthProvider: "clerk", envRegionTable: "/opt/kiff/geo/country.csv.gz"}, ""},
		{map[string]string{envDeployEnv: "production", envAuthProvider: "clerk"}, "requires KIFF_REGION_TABLE"},
		{map[string]string{envDeployEnv: "production", envAuthProvider: "stub"}, "requires KIFF_AUTH_PROVIDER=clerk"},
		{map[string]string{envDeployEnv: "production"}, "requires KIFF_AUTH_PROVIDER=clerk"},
		{map[string]string{envAuthProvider: "stub"}, ""},
		{map[string]string{envDeployEnv: "prod"}, "unknown KIFF_ENV"},
	}
	for _, tc := range cases {
		err := checkProductionPosture(func(k string) string { return tc.env[k] })
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%v: want nil, got %v", tc.env, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%v: want error containing %q, got %v", tc.env, tc.wantErr, err)
		}
	}
}
