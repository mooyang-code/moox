package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// 新建的数据集直接启用，修订号从 1 开始；随请求带来的列与数据集在同一事务内写入。
func TestDatasetCreateIsActiveAndCarriesColumns(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	created, err := store.UpsertDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset", DataSourceId: "source",
		Name: "Dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m",
		Status: "disabled", Revision: 99,
	}, &pb.DatasetColumn{ColumnName: "close", OriginId: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetStatus() != "active" || created.GetRevision() != 1 {
		t.Fatalf("create defaults = status=%q revision=%d", created.GetStatus(), created.GetRevision())
	}
	if created.GetRetention() != "" || created.GetRetentionSource() != "" {
		t.Fatalf("stored retention = %q/%q; retention is resolved on read, never stored", created.GetRetention(), created.GetRetentionSource())
	}
	columns, _, err := store.ListDatasetColumns(ctx, "space", "dataset", nil)
	if err != nil || len(columns) != 1 || columns[0].GetColumnName() != "close" || columns[0].GetDatasetId() != "dataset" {
		t.Fatalf("columns=%v err=%v", columns, err)
	}

	// 列定义有误时数据集也不落库。
	_, err = store.UpsertDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "bad_columns", DataSourceId: "source",
		Name: "Bad columns", DataKind: pb.DataKind_DATA_KIND_RECORD,
	}, &pb.DatasetColumn{ColumnName: "untyped"})
	if err == nil {
		t.Fatal("dataset with an untyped column was created")
	}
	if _, err := store.GetDataset(ctx, "space", "bad_columns"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rejected dataset persisted: err=%v", err)
	}
}

func TestDeleteDatasetRejectsReferencedOrdinaryDatasetWithoutDeletingView(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	if _, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset-ordinary", DataSourceId: "source",
		Name: "Ordinary", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateView(ctx, &pb.View{
		SpaceId: "space", ViewId: "view-ordinary", Name: "Ordinary view", DatasetId: "dataset-ordinary", Engine: "duckdb",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDataset(ctx, "space", "dataset-ordinary"); err == nil {
		t.Fatal("DeleteDataset() succeeded while an ordinary View references the Dataset")
	}
	if _, err := store.GetDataset(ctx, "space", "dataset-ordinary"); err != nil {
		t.Fatalf("Dataset was deleted after rejected request: %v", err)
	}
	if _, err := store.GetView(ctx, "space", "view-ordinary"); err != nil {
		t.Fatalf("ordinary View was deleted after rejected request: %v", err)
	}
}

func TestDatasetMayOmitDataSource(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)

	created, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset_collector_result",
		Name: "Collector Result", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m",
		Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetDataSourceId() != "" {
		t.Fatalf("collector result data_source_id = %q, want empty", created.GetDataSourceId())
	}
	var source sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT c_data_source_id FROM t_datasets WHERE c_space_id = ? AND c_dataset_id = ?`, "space", created.GetDatasetId()).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source.Valid {
		t.Fatalf("collector result source persisted as %q, want SQL NULL", source.String)
	}

	// data_source_id 对所有数据集都是可选的。
	if _, err = store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset_missing_source",
		Name: "Missing Source", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m",
	}); err != nil {
		t.Fatalf("dataset without source err = %v", err)
	}
}

func TestDatasetUpdateRevisionAndStatusInvariants(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	created := createTestDataset(t, ctx, store, "dataset")

	updated, err := store.UpdateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset", Name: "Dataset v2", Description: "updated",
		DataSourceId: "source", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m", Status: "disabled", Revision: created.GetRevision(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetRevision() != 2 || updated.GetStatus() != "disabled" {
		t.Fatalf("updated dataset = %v", updated)
	}

	// 停用的数据集可以直接改回启用。
	reenabled, err := store.UpdateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset", Name: "Dataset v2", Status: "active", Revision: updated.GetRevision(),
	})
	if err != nil || reenabled.GetStatus() != "active" || reenabled.GetRevision() != 3 {
		t.Fatalf("re-enable dataset = %v err=%v", reenabled, err)
	}

	before, err := store.GetDataset(ctx, "space", "dataset")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset", Name: "stale", Status: "disabled", Revision: before.GetRevision() - 1,
	})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update error = %v, want %v", err, ErrRevisionConflict)
	}
	assertDatasetUnchanged(t, ctx, store, before)
}

func assertDatasetUnchanged(t *testing.T, ctx context.Context, store *Store, want *pb.Dataset) {
	t.Helper()
	assertDatasetMatchesStore(t, ctx, store, want)
}

func assertDatasetMatchesStore(t *testing.T, ctx context.Context, store *Store, want *pb.Dataset) {
	t.Helper()
	got, err := store.GetDataset(ctx, want.GetSpaceId(), want.GetDatasetId())
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, want) {
		t.Fatalf("dataset changed: got=%v want=%v", got, want)
	}
}

func createTestDataset(t *testing.T, ctx context.Context, store *Store, datasetID string) *pb.Dataset {
	t.Helper()
	item, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: datasetID, DataSourceId: "source",
		Name: datasetID, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m",
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func openTestStore(t *testing.T, ctx context.Context) *Store {
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

func seedDatasetParents(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	if _, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "space", Name: "Space"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"source", "other"} {
		if _, err := store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "space", DataSourceId: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTimeSeriesDatasetRequiresOneCanonicalImmutableFreq(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	for _, freq := range []string{"", "1H", "60m", "quarter"} {
		_, err := store.CreateDataset(ctx, &pb.Dataset{
			SpaceId: "space", DatasetId: "dataset_bad", DataSourceId: "source",
			Name: "Bad", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: freq,
		})
		if err == nil || !strings.Contains(err.Error(), "time_series dataset freq must be one of") {
			t.Fatalf("freq %q error = %v, want canonical freq rejection", freq, err)
		}
	}
	record, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset_record", DataSourceId: "source",
		Name: "Record", DataKind: pb.DataKind_DATA_KIND_RECORD, Freq: "quarter",
	})
	if err != nil || record.GetFreq() != "quarter" {
		t.Fatalf("record dataset = %v, %v; a record Dataset keeps a descriptive freq", record, err)
	}

	created := createTestDataset(t, ctx, store, "dataset")
	if _, err := store.UpdateDataset(ctx, &pb.Dataset{SpaceId: "space", DatasetId: "dataset", Name: "dataset", Freq: "5m"}); err == nil || !strings.Contains(err.Error(), "freq is immutable") {
		t.Fatalf("freq change error = %v, want immutable", err)
	}
	updated, err := store.UpdateDataset(ctx, &pb.Dataset{SpaceId: "space", DatasetId: "dataset", Name: "dataset", Description: "renamed"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetFreq() != created.GetFreq() {
		t.Fatalf("update without freq changed it to %q", updated.GetFreq())
	}
	listed, _, err := store.ListDatasets(ctx, DatasetQuery{SpaceID: "space", Freq: "1m"})
	if err != nil || len(listed) != 1 || listed[0].GetDatasetId() != "dataset" {
		t.Fatalf("list by freq = %v, %v", listed, err)
	}
}

func TestViewTakesItsDatasetFreq(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	createTestDataset(t, ctx, store, "dataset")
	view, err := store.CreateView(ctx, &pb.View{SpaceId: "space", ViewId: "view", Name: "View", DatasetId: "dataset", Engine: "duckdb"})
	if err != nil {
		t.Fatal(err)
	}
	if view.GetFreq() != "1m" {
		t.Fatalf("view freq = %q, want the Dataset freq 1m", view.GetFreq())
	}
	stored, err := store.GetView(ctx, "space", "view")
	if err != nil || stored.GetFreq() != "1m" {
		t.Fatalf("stored view freq = %q, %v", stored.GetFreq(), err)
	}
	if _, err := store.UpsertView(ctx, &pb.View{SpaceId: "space", ViewId: "view", Name: "View", DatasetId: "dataset", Engine: "duckdb", Freq: "1h"}); err == nil || !strings.Contains(err.Error(), "a View takes its Dataset's freq") {
		t.Fatalf("mismatched view freq error = %v", err)
	}
}
