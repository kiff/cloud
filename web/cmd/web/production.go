package main

import (
	"fmt"
	"strings"
)

// envDeployEnv names the environment the binary runs in; the deploy script writes
// KIFF_ENV=production on the production box. A production run refuses
// stub sign-in, which lets anyone sign in as anyone.
const envDeployEnv = "KIFF_ENV"

// checkProductionPosture returns an error when KIFF_ENV=production and
// KIFF_AUTH_PROVIDER is anything but clerk. Outside production it only
// rejects an unknown KIFF_ENV value, so a typo cannot turn the check off.
func checkProductionPosture(getenv func(string) string) error {
	switch strings.ToLower(strings.TrimSpace(getenv(envDeployEnv))) {
	case "", "development", "test":
		return nil
	case "production":
	default:
		return fmt.Errorf("unknown %s value %q (expected production, development, or test)", envDeployEnv, getenv(envDeployEnv))
	}
	if p := strings.TrimSpace(getenv(envAuthProvider)); p != "clerk" {
		return fmt.Errorf("%s=production requires %s=clerk (got %q); stub sign-in accepts any typed subject", envDeployEnv, envAuthProvider, p)
	}
	return nil
}
