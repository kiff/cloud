package main

import (
	"errors"
	"fmt"
	"strings"
)

// envDeployEnv names the environment the binary runs in. The deploy script writes
// KIFF_ENV=production on the production box; anything else is a
// development or test run. A production run refuses to start in a posture
// that would quietly weaken a security property: stub sign-in, which lets
// anyone sign in as anyone, or receipts signed by a throwaway key nobody
// can verify after a restart.
const envDeployEnv = "KIFF_ENV"

// checkProductionPosture returns an error when KIFF_ENV=production and the
// rest of the environment would start the API with stub auth, without
// Postgres, with receipts disabled, or with receipts signed by a random
// key. Outside production it only rejects an unknown KIFF_ENV value, so a
// typo cannot turn the checks off.
func checkProductionPosture(getenv func(string) string) error {
	switch strings.ToLower(strings.TrimSpace(getenv(envDeployEnv))) {
	case "", "development", "test":
		return nil
	case "production":
	default:
		return fmt.Errorf("unknown %s value %q (expected production, development, or test)", envDeployEnv, getenv(envDeployEnv))
	}
	var errs []error
	if p := strings.TrimSpace(getenv(envAuthProv)); p != "clerk" {
		errs = append(errs, fmt.Errorf("%s=production requires %s=clerk (got %q); stub sign-in accepts any typed subject", envDeployEnv, envAuthProv, p))
	}
	if strings.TrimSpace(getenv(envDatabaseURL)) == "" {
		errs = append(errs, fmt.Errorf("%s=production requires %s", envDeployEnv, envDatabaseURL))
	}
	if strings.EqualFold(strings.TrimSpace(getenv(envReceiptMode)), string(receiptModeDisabled)) {
		errs = append(errs, fmt.Errorf("%s=production does not allow %s=disabled", envDeployEnv, envReceiptMode))
	}
	if strings.TrimSpace(getenv(envAnchorPrivateKey)) == "" {
		errs = append(errs, fmt.Errorf("%s=production requires %s; without it receipts are signed by a random key that cannot be verified after a restart", envDeployEnv, envAnchorPrivateKey))
	}
	if strings.TrimSpace(getenv(envRegionTable)) == "" {
		errs = append(errs, fmt.Errorf("%s=production requires %s; without it no session-starting route checks the caller's country", envDeployEnv, envRegionTable))
	}
	return errors.Join(errs...)
}
