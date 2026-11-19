package tenant

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var createCloudMeta = regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS cloud_meta\.(\w+)\s*\((.*?)\n\s*\)\s*;`)

// Every cloud_meta table, whatever it is keyed by (tenant, person, parent
// row), is classified for a hard delete: erased, erased by
// person, erased through its parent, retained, or holding no tenant's
// data. A new table must be added to one of the lists in erasure.go, or
// this fails.
func TestCloudMetaTablesAreClassified(t *testing.T) {
	found := map[string]bool{}
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(path, "_test.go") ||
			!(strings.HasSuffix(path, ".sql") || strings.HasSuffix(path, ".go")) {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range createCloudMeta.FindAllStringSubmatch(string(src), -1) {
			found[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) < 20 {
		t.Fatalf("found only %d cloud_meta tables; the scan is broken", len(found))
	}
	for table := range found {
		var in []string
		if slices.Contains(cloudMetaErased, table) {
			in = append(in, "erased")
		}
		if slices.Contains(cloudMetaErasedBySubject, table) {
			in = append(in, "erased by subject")
		}
		if _, ok := cloudMetaErasedThroughParent[table]; ok {
			in = append(in, "erased through parent")
		}
		if _, ok := cloudMetaRetained[table]; ok {
			in = append(in, "retained")
		}
		if _, ok := cloudMetaNotPerTenant[table]; ok {
			in = append(in, "not per-tenant")
		}
		switch len(in) {
		case 0:
			t.Errorf("cloud_meta.%s is not classified for a hard delete; add it to one of the lists in erasure.go", table)
		case 1:
		default:
			t.Errorf("cloud_meta.%s is in more than one list: %v", table, in)
		}
	}
	listed := slices.Concat(cloudMetaErased, cloudMetaErasedBySubject)
	for table := range cloudMetaErasedThroughParent {
		listed = append(listed, table)
	}
	for table := range cloudMetaRetained {
		listed = append(listed, table)
	}
	for table := range cloudMetaNotPerTenant {
		listed = append(listed, table)
	}
	for _, table := range listed {
		if !found[table] {
			t.Errorf("cloud_meta.%s is classified but no longer exists", table)
		}
	}
	// The subject-keyed tables must be the wallet tables the review on
	// asked to pin, and erased (not retained).
	for _, table := range []string{"user_wallets", "wallet_nonces"} {
		if !slices.Contains(cloudMetaErasedBySubject, table) {
			t.Errorf("cloud_meta.%s must be erased by subject", table)
		}
	}
}
