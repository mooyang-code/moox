package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestUpsertDatasetRequiresDataNodeOnCreate(t *testing.T) {
	ctx := context.Background()
	store := openV2TestStore(t, ctx)
	seedV2DatasetParents(t, ctx, store)
	_, err := store.UpsertDataset(ctx, &pb.Dataset{
		SpaceId:      "space",
		DatasetId:    "dataset",
		DataSourceId: "source",
		Name:         "Dataset",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
	})
	if err == nil {
		t.Fatal("expected data_node_id to be required")
	}
}

func TestUpsertDatasetRejectsIdentityChanges(t *testing.T) {
	ctx := context.Background()
	store := openV2TestStore(t, ctx)
	seedV2DatasetParents(t, ctx, store)
	original := &pb.Dataset{
		SpaceId:      "space",
		DatasetId:    "dataset",
		DataSourceId: "source",
		DataNodeId:   "node-a",
		Name:         "Dataset",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		KeepDuration: "24h",
	}
	if _, err := store.UpsertDataset(ctx, original); err != nil {
		t.Fatal(err)
	}
	tests := []*pb.Dataset{
		{SpaceId: "space", DatasetId: "dataset", DataSourceId: "other", DataNodeId: "node-a", Name: "Dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, KeepDuration: "24h"},
		{SpaceId: "space", DatasetId: "dataset", DataSourceId: "source", DataNodeId: "node-b", Name: "Dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, KeepDuration: "24h"},
		{SpaceId: "space", DatasetId: "dataset", DataSourceId: "source", DataNodeId: "node-a", Name: "Dataset", DataKind: pb.DataKind_DATA_KIND_RECORD, KeepDuration: "0"},
	}
	for _, changed := range tests {
		if _, err := store.UpsertDataset(ctx, changed); err == nil {
			t.Fatalf("accepted immutable dataset change: %+v", changed)
		}
	}
}

func openV2TestStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	store, err := Open(ctx, Options{
		Path:       filepath.Join(t.TempDir(), "metadata.db"),
		SchemaPath: filepath.Join("..", "..", "..", "..", "schema", "metadata.sql"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	return store
}

func seedV2DatasetParents(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	if _, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "space", Name: "Space"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"source", "other"} {
		if _, err := store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "space", DataSourceId: id, Name: id, Kind: "internal"}); err != nil {
			t.Fatal(err)
		}
	}
}
