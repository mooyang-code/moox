package bleve

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestListManagedIndexesReturnsOnlyOfficialBleveDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bleve")
	index, err := Open(Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()

	ids := []string{
		viewindex.ViewIndexID("space", "records", viewindex.SlotA),
		viewindex.ViewIndexID("space", "records", viewindex.SlotB),
	}
	for _, id := range ids {
		if err := os.Mkdir(filepath.Join(root, id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, ids[0]+".prepare-123"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "junk"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "view_s7370616365_v7265636f726473_z"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, viewindex.ViewIndexID("space", "file", viewindex.SlotA)), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := index.ListManagedIndexes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	sort.Strings(ids)
	if !reflect.DeepEqual(got, ids) {
		t.Fatalf("managed indexes = %v, want %v", got, ids)
	}
}

func TestRemoveDeletesManagedBleveDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bleve")
	index, err := Open(Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()

	id := viewindex.ViewIndexID("space", "records", viewindex.SlotA)
	if err := index.Prepare(context.Background(), id, viewindex.ViewIndexSchema{
		SpaceID: "space", ViewID: "records", PrimaryDatasetID: "records", ViewVersion: 1,
		Engine: "bleve", SchemaHash: "hash", Columns: []*pb.ViewColumn{{ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, id)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("prepared Bleve directory missing: %v", err)
	}
	if err := index.Remove(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("removed Bleve directory still exists: %v", err)
	}
}
