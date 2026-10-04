package sqlite

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestDeviceCRUDUsesSchemaV6WithoutNodeBinding(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	created, err := store.UpsertDevice(ctx, &pb.Device{
		DeviceId: "device-a", Name: "Pebble A", Engine: "pebble", Endpoint: "file:///data/a",
		ConfigJson: `{"cache_size":"1G"}`, Attributes: map[string]string{"zone": "a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetDeviceId() != "device-a" || created.GetEngine() != "pebble" || created.GetEndpoint() != "file:///data/a" {
		t.Fatalf("created device = %v", created)
	}

	updated, err := store.UpsertDevice(ctx, &pb.Device{
		DeviceId: "device-a", Name: "DuckDB A", Engine: "duckdb", Endpoint: "file:///data/b",
		ConfigJson: `{"threads":"4"}`, Status: "disabled", Attributes: map[string]string{"zone": "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetName() != "DuckDB A" || updated.GetEngine() != "duckdb" || updated.GetStatus() != "disabled" {
		t.Fatalf("updated device = %v", updated)
	}

	items, page, err := store.ListDevices(ctx, "duckdb", &pb.Page{Page: 1, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].GetDeviceId() != "device-a" || page.GetTotal() != 1 {
		t.Fatalf("listed devices=%v page=%v", items, page)
	}

	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	if _, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset", DataSourceId: "source", DataNodeId: "node-a",
		Name: "Dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterArchiveFile(ctx, &pb.ArchiveFile{
		SpaceId: "space", ArchiveFileId: "archive-a", DatasetId: "dataset", DeviceId: "device-a",
		PartitionKey: "date=2026-07-22", FileUri: "file:///data/a/archive.parquet", Columns: []string{"close"},
	}); err != nil {
		t.Fatal(err)
	}
	archives, page, err := store.ListArchiveFiles(ctx, "space", "dataset", &pb.Page{Page: 1, Size: 10})
	if err != nil || len(archives) != 1 || page.GetTotal() != 1 {
		t.Fatalf("listed archives=%v page=%v err=%v", archives, page, err)
	}
}

func TestDeviceCRUDAcceptsParquetArchiveDevice(t *testing.T) {
	store := openTestStore(t, t.Context())
	created, err := store.UpsertDevice(t.Context(), &pb.Device{
		DeviceId: "parquet-local",
		Name:     "Market Parquet Archive",
		Engine:   "parquet",
		Endpoint: "../data/archive",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetEngine() != "parquet" || created.GetEndpoint() != "../data/archive" {
		t.Fatalf("created parquet device = %v", created)
	}
}
