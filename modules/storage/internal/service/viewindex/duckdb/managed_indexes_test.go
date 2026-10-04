//go:build cgo

package duckdb

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
)

func TestListManagedIndexesReturnsOnlyOfficialDuckDBFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "duckdb")
	manager, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	ids := []string{
		viewindex.ViewIndexID("space", "prices", viewindex.SlotA),
		viewindex.ViewIndexID("space", "prices", viewindex.SlotB),
	}
	for _, id := range ids {
		if err := os.WriteFile(filepath.Join(root, id+".duckdb"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, id+".duckdb.wal"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{
		"junk.duckdb",
		"view_s7370616365_v707269636573_z.duckdb",
		ids[0] + ".duckdb.prepare-123",
		"notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := manager.ListManagedIndexes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	sort.Strings(ids)
	if !reflect.DeepEqual(got, ids) {
		t.Fatalf("managed indexes = %v, want %v", got, ids)
	}
}
