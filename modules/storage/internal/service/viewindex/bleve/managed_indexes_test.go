package bleve

import (
	"context"
	"errors"
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
		viewindex.ViewIndexID("space", "records"),
		viewindex.ViewIndexID("space", "orders"),
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
	if err := os.WriteFile(filepath.Join(root, viewindex.ViewIndexID("space", "file")), nil, 0o600); err != nil {
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

	id := viewindex.ViewIndexID("space", "records")
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

func TestExtendColumnsAppendsWithoutTouchingExistingRows(t *testing.T) {
	ctx := context.Background()
	index, err := Open(Options{Path: filepath.Join(t.TempDir(), "bleve")})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	column := func(name string, order uint32) *pb.ViewColumn {
		return &pb.ViewColumn{ColumnName: name, OriginId: name, OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, SortOrder: order}
	}
	current := viewindex.ViewIndexSchema{SpaceID: "space", ViewID: "records", PrimaryDatasetID: "dataset", ViewVersion: 1, Engine: "bleve", Columns: []*pb.ViewColumn{column("name", 0)}}
	current.SchemaHash = viewindex.HashViewIndexSchema(current)
	id := viewindex.ViewIndexID("space", "records")
	if err := index.Prepare(ctx, id, current); err != nil {
		t.Fatal(err)
	}
	next := current
	next.ViewVersion = 2
	next.Columns = []*pb.ViewColumn{column("name", 0), column("note", 1)}
	next.SchemaHash = viewindex.HashViewIndexSchema(next)
	if err := index.ExtendColumns(ctx, id, current, next); err != nil {
		t.Fatal(err)
	}
	// 响应丢失后重试是幂等的。
	if err := index.ExtendColumns(ctx, id, current, next); err != nil {
		t.Fatal(err)
	}
	stats, err := index.StatMetadata(ctx, id)
	if err != nil || stats.ViewVersion != 2 || stats.SchemaHash != next.SchemaHash {
		t.Fatalf("加列后 = %#v err=%v", stats, err)
	}
	removal := next
	removal.ViewVersion = 3
	removal.Columns = []*pb.ViewColumn{column("name", 0)}
	if err := index.ExtendColumns(ctx, id, next, removal); !errors.Is(err, viewindex.ErrSchemaExtensionConflict) {
		t.Fatalf("删列应当被拒绝，err=%v", err)
	}
}
