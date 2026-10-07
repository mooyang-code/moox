package sqlite

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// Collector-owned Datasets may be created without a data source.
var collectorOwner = map[string]string{"owner_module": "collector", "dataset_role": "raw_collection", "collector_task_id": "task"}

func TestUpdateDatasetAssignsDataSourceOnceThenKeepsItImmutable(t *testing.T) {
	ctx, store := newKeepDurationStore(t)
	if _, err := store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "space", DataSourceId: "other", Name: "Other", Kind: "internal", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "unsourced", DataNodeId: "node",
		Name: "unsourced", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, KeepDuration: "0",
		Attributes: collectorOwner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetDataSourceId() != "" {
		t.Fatalf("created data source = %q", created.GetDataSourceId())
	}

	assign := &pb.Dataset{SpaceId: "space", DatasetId: "unsourced", Name: "unsourced", DataSourceId: "source", KeepDuration: "0", Attributes: collectorOwner}
	if _, err := store.UpdateDataset(ctx, assign); err != nil {
		t.Fatalf("assigning a data source to an unsourced Dataset: %v", err)
	}
	got, err := store.GetDataset(ctx, "space", "unsourced")
	if err != nil || got.GetDataSourceId() != "source" {
		t.Fatalf("data source after assignment = %q err=%v", got.GetDataSourceId(), err)
	}

	unchanged := &pb.Dataset{SpaceId: "space", DatasetId: "unsourced", Name: "renamed", KeepDuration: "0", Attributes: collectorOwner}
	if _, err := store.UpdateDataset(ctx, unchanged); err != nil {
		t.Fatalf("update without data source: %v", err)
	}
	if got, _ := store.GetDataset(ctx, "space", "unsourced"); got.GetDataSourceId() != "source" {
		t.Fatalf("an update that omits data_source_id must keep it, got %q", got.GetDataSourceId())
	}

	change := &pb.Dataset{SpaceId: "space", DatasetId: "unsourced", Name: "renamed", DataSourceId: "other", KeepDuration: "0", Attributes: collectorOwner}
	if _, err := store.UpdateDataset(ctx, change); err == nil {
		t.Fatal("changing an assigned data source must be rejected")
	}
}
