package tenant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every cloud_meta table is decided for a prune too: pruned (a step in
// cloudMetaPruned) or kept, with the reason. A new table must be added to
// one of them, or this fails.
func TestCloudMetaTablesAreClassifiedForPrune(t *testing.T) {
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
	pruned := map[string]bool{}
	for _, s := range cloudMetaPruned {
		if pruned[s.table] {
			t.Errorf("cloud_meta.%s has two prune steps", s.table)
		}
		pruned[s.table] = true
		if !strings.Contains(s.sql, "cloud_meta."+s.table+" ") || !strings.Contains(s.sql, "tenant_id = $1") || !strings.Contains(s.sql, "$2") {
			t.Errorf("cloud_meta.%s: its step must delete from that table, for $1's account, before $2: %s", s.table, s.sql)
		}
	}
	for table := range found {
		_, kept := cloudMetaKeptOnPrune[table]
		switch {
		case pruned[table] && kept:
			t.Errorf("cloud_meta.%s is both pruned and kept", table)
		case !pruned[table] && !kept:
			t.Errorf("cloud_meta.%s is not classified for a prune; add a step to cloudMetaPruned or a reason to cloudMetaKeptOnPrune (prune.go)", table)
		}
	}
	for table := range pruned {
		if !found[table] {
			t.Errorf("cloud_meta.%s has a prune step but no longer exists", table)
		}
	}
	for table := range cloudMetaKeptOnPrune {
		if !found[table] {
			t.Errorf("cloud_meta.%s is kept on prune but no longer exists", table)
		}
	}
}
