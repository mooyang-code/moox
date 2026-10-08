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

func TestDatasetCreateDefaultsAndRequiresActiveDataNode(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	if _, err := store.RegisterDataNode(ctx, "node-a", "trpc://node-a", "Node A"); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset", DataSourceId: "source", DataNodeId: "node-a",
		Name: "Dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m", KeepDuration: "4320h",
		Status: "active", BindingLocked: true, Revision: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetStatus() != "disabled" || created.GetBindingLocked() || created.GetRevision() != 1 {
		t.Fatalf("create defaults = status=%q locked=%t revision=%d", created.GetStatus(), created.GetBindingLocked(), created.GetRevision())
	}
	if created.GetKeepDuration() != "4320h0m0s" {
		t.Fatalf("keep_duration = %q, want 4320h0m0s", created.GetKeepDuration())
	}

	if _, err := store.RegisterDataNode(ctx, "node-disabled", "trpc://disabled", "Disabled"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateDataNode(ctx, "node-disabled", "Disabled", "disabled"); err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "disabled_node_dataset", DataSourceId: "source", DataNodeId: "node-disabled",
		Name: "Disabled node", DataKind: pb.DataKind_DATA_KIND_RECORD,
	})
	if !errors.Is(err, ErrDataNodeDisabled) {
		t.Fatalf("create on disabled node error = %v, want %v", err, ErrDataNodeDisabled)
	}
}

func TestDeleteDatasetRemovesOnlyManagedFactorResultDefaultView(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	if _, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset_factor_result", DataSourceId: "source", DataNodeId: "node-a",
		Name: "Factor result", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1h", KeepDuration: "0",
		Attributes: map[string]string{"owner_module": "factor", "dataset_role": "factor_result", "write_owner": "factor"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateView(ctx, &pb.View{
		SpaceId: "space", ViewId: "view_factor_result", Name: "Factor result",
		DatasetId: "dataset_factor_result", Engine: "duckdb", KeepDuration: "0", GrainKeys: []string{"subject_id", "freq", "data_time", "series_tag"},
		Freq: "1h", Attributes: map[string]string{
			"owner_module": "factor", "view_role": "factor_result", "managed_by": "storage", "primary_dataset_role": "factor_result",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDataset(ctx, "space", "dataset_factor_result"); err != nil {
		t.Fatalf("DeleteDataset() error = %v", err)
	}
	if _, err := store.GetDataset(ctx, "space", "dataset_factor_result"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetDataset() error = %v, want no rows", err)
	}
	if _, err := store.GetView(ctx, "space", "view_factor_result"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetView() error = %v, want no rows", err)
	}
}

func TestDeleteDatasetRejectsReferencedOrdinaryDatasetWithoutDeletingView(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	if _, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset-ordinary", DataSourceId: "source", DataNodeId: "node-a",
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

func TestDeleteFactorResultWithAdditionalViewRollsBackManagedViewDeletion(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	dataset := &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset_factor_result", DataSourceId: "source", DataNodeId: "node-a",
		Name: "Factor result", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1h", KeepDuration: "0",
		Attributes: map[string]string{"owner_module": "factor", "dataset_role": "factor_result", "write_owner": "factor"},
	}
	if _, err := store.CreateDataset(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	managedAttrs := map[string]string{"owner_module": "factor", "view_role": "factor_result", "managed_by": "storage", "primary_dataset_role": "factor_result"}
	for _, view := range []*pb.View{
		{
			SpaceId: "space", ViewId: "view_factor_result", Name: "Factor result", DatasetId: dataset.GetDatasetId(),
			Engine: "duckdb", KeepDuration: "0", GrainKeys: []string{"subject_id", "freq", "data_time", "series_tag"},
			Freq: "1h", Attributes: managedAttrs,
		},
		{SpaceId: "space", ViewId: "view_factor_extra", Name: "Extra view", DatasetId: dataset.GetDatasetId(), Engine: "duckdb"},
	} {
		if _, err := store.CreateView(ctx, view); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteDataset(ctx, "space", dataset.GetDatasetId()); err == nil {
		t.Fatal("DeleteDataset() succeeded while another View references the Dataset")
	}
	for _, viewID := range []string{"view_factor_result", "view_factor_extra"} {
		if _, err := store.GetView(ctx, "space", viewID); err != nil {
			t.Fatalf("View %s was deleted after rollback: %v", viewID, err)
		}
	}
}

func TestCollectorOwnedDatasetMayOmitDataSource(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")

	created, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset_collector_result", DataNodeId: "node-a",
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

	_, err = store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset_missing_source", DataNodeId: "node-a",
		Name: "Missing Source", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m",
	})
	if err == nil || !strings.Contains(err.Error(), "data_source_id is required") {
		t.Fatalf("ordinary dataset without source err = %v", err)
	}
}

func TestDatasetUpdateRevisionAndStatusInvariants(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	created := createTestDataset(t, ctx, store, "dataset", "node-a")

	updated, err := store.UpdateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset", Name: "Dataset v2", Description: "updated",
		DataSourceId: "source", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m",
		KeepDuration: "24h", Status: "disabled", Revision: created.GetRevision(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetRevision() != 2 || updated.GetStatus() != "disabled" || updated.GetDataNodeId() != "node-a" || updated.GetBindingLocked() {
		t.Fatalf("updated dataset = %v", updated)
	}

	before, err := store.GetDataset(ctx, "space", "dataset")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset", Name: "should fail", Status: "active", Revision: before.GetRevision(),
	})
	if !errors.Is(err, ErrDatasetMustBeDisabled) {
		t.Fatalf("disabled to active error = %v, want %v", err, ErrDatasetMustBeDisabled)
	}
	assertDatasetUnchanged(t, ctx, store, before)

	_, err = store.UpdateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset", Name: "stale", Status: "disabled", Revision: before.GetRevision() - 1,
	})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update error = %v, want %v", err, ErrRevisionConflict)
	}
	assertDatasetUnchanged(t, ctx, store, before)
}

func TestDatasetRebindUsesRevisionAndLifecycleGuards(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	registerActiveNode(t, ctx, store, "node-b")
	registerActiveNode(t, ctx, store, "node-disabled")
	if _, err := store.UpdateDataNode(ctx, "node-disabled", "Disabled", "disabled"); err != nil {
		t.Fatal(err)
	}
	created := createTestDataset(t, ctx, store, "dataset", "node-a")

	_, err := store.RebindDatasetDataNode(ctx, "space", "dataset", "node-b", created.GetRevision()-1)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale rebind error = %v, want %v", err, ErrRevisionConflict)
	}
	assertDatasetUnchanged(t, ctx, store, created)

	rebound, err := store.RebindDatasetDataNode(ctx, "space", "dataset", "node-b", created.GetRevision())
	if err != nil {
		t.Fatal(err)
	}
	if rebound.GetRevision() != 2 || rebound.GetDataNodeId() != "node-b" {
		t.Fatalf("rebound dataset = %v", rebound)
	}
	assertDatasetMatchesStore(t, ctx, store, rebound)
	if _, err := store.RebindDatasetDataNode(ctx, "space", "dataset", "node-b", rebound.GetRevision()); err == nil {
		t.Fatal("rebind to the current data node succeeded")
	}
	if _, err := store.RebindDatasetDataNode(ctx, "space", "dataset", "node-disabled", rebound.GetRevision()); !errors.Is(err, ErrDataNodeDisabled) {
		t.Fatalf("rebind to disabled node error = %v, want %v", err, ErrDataNodeDisabled)
	}
	assertDatasetUnchanged(t, ctx, store, rebound)
}

func TestDatasetRebindRejectsLockedDatasetAndPreservesFullRow(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	registerActiveNode(t, ctx, store, "node-b")
	created := createTestDataset(t, ctx, store, "dataset", "node-a")
	active, err := store.CommitDatasetActivation(ctx, "space", "dataset", created.GetRevision())
	if err != nil {
		t.Fatal(err)
	}
	locked, err := store.UpdateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset", Name: active.GetName(),
		Description: "locked row", KeepDuration: "48h",
		Attributes: map[string]string{"owner": "storage"}, Status: "disabled", Revision: active.GetRevision(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.RebindDatasetDataNode(ctx, "space", "dataset", "node-b", locked.GetRevision())
	if !errors.Is(err, ErrBindingLocked) {
		t.Fatalf("locked rebind error = %v, want %v", err, ErrBindingLocked)
	}
	assertDatasetUnchanged(t, ctx, store, locked)
}

func TestDatasetRebindConcurrentCASReturnsOneConflict(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	registerActiveNode(t, ctx, store, "node-b")
	registerActiveNode(t, ctx, store, "node-c")
	created := createTestDataset(t, ctx, store, "dataset", "node-a")

	type result struct {
		dataset *pb.Dataset
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, nodeID := range []string{"node-b", "node-c"} {
		go func(nodeID string) {
			<-start
			item, err := store.RebindDatasetDataNode(ctx, "space", "dataset", nodeID, created.GetRevision())
			results <- result{dataset: item, err: err}
		}(nodeID)
	}
	close(start)

	var success, conflict int
	for range 2 {
		got := <-results
		switch {
		case got.err == nil:
			success++
		case errors.Is(got.err, ErrRevisionConflict):
			conflict++
		default:
			t.Fatalf("concurrent rebind error = %v, want nil or ErrRevisionConflict", got.err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent rebind outcomes = success:%d conflict:%d, want 1:1", success, conflict)
	}
}

func TestDatasetActivationCASAndIdempotency(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	created := createTestDataset(t, ctx, store, "dataset", "node-a")

	_, err := store.CommitDatasetActivation(ctx, "space", "dataset", created.GetRevision()-1)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale activation error = %v, want %v", err, ErrRevisionConflict)
	}
	assertDatasetUnchanged(t, ctx, store, created)

	active, err := store.CommitDatasetActivation(ctx, "space", "dataset", created.GetRevision())
	if err != nil {
		t.Fatal(err)
	}
	if active.GetStatus() != "active" || !active.GetBindingLocked() || active.GetRevision() != 2 {
		t.Fatalf("activated dataset = %v", active)
	}
	assertDatasetMatchesStore(t, ctx, store, active)
	retry, err := store.CommitDatasetActivation(ctx, "space", "dataset", created.GetRevision()-1)
	if err != nil {
		t.Fatal(err)
	}
	if retry.GetRevision() != active.GetRevision() || retry.GetStatus() != active.GetStatus() || retry.GetBindingLocked() != active.GetBindingLocked() {
		t.Fatalf("activation retry = %v, first result = %v", retry, active)
	}

	if _, err := store.UpdateDataset(ctx, &pb.Dataset{SpaceId: "space", DatasetId: "dataset", Name: "Dataset", Status: "disabled", Revision: active.GetRevision()}); err != nil {
		t.Fatal(err)
	}
	disabled, err := store.GetDataset(ctx, "space", "dataset")
	if err != nil {
		t.Fatal(err)
	}
	if !disabled.GetBindingLocked() || disabled.GetStatus() != "disabled" {
		t.Fatalf("disabled locked dataset = %v", disabled)
	}
	reactivated, err := store.CommitDatasetActivation(ctx, "space", "dataset", disabled.GetRevision())
	if err != nil {
		t.Fatal(err)
	}
	if reactivated.GetRevision() != disabled.GetRevision()+1 || reactivated.GetStatus() != "active" || !reactivated.GetBindingLocked() {
		t.Fatalf("reactivated dataset = %v", reactivated)
	}
	assertDatasetMatchesStore(t, ctx, store, reactivated)
}

func TestDatasetActivationRejectsActiveUnlockedState(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	created := createTestDataset(t, ctx, store, "dataset", "node-a")
	activeUnlocked := proto.Clone(created).(*pb.Dataset)
	activeUnlocked.Status = "active"
	activeUnlocked.BindingLocked = false
	raw, err := marshal(activeUnlocked)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE t_datasets SET c_status = 'active', c_binding_locked = 0, c_attrs_json = ? WHERE c_space_id = 'space' AND c_dataset_id = 'dataset'`, raw); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetDataset(ctx, "space", "dataset")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CommitDatasetActivation(ctx, "space", "dataset", created.GetRevision())
	if !errors.Is(err, ErrDatasetMustBeDisabled) {
		t.Fatalf("active unlocked activation error = %v, want %v", err, ErrDatasetMustBeDisabled)
	}
	assertDatasetUnchanged(t, ctx, store, before)
}

func TestListDatasetsFiltersByDataNodeIDs(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	registerActiveNode(t, ctx, store, "node-b")
	createTestDataset(t, ctx, store, "dataset_a", "node-a")
	createTestDataset(t, ctx, store, "dataset_b", "node-b")
	items, page, err := store.ListDatasets(ctx, DatasetQuery{DataNodeIDs: []string{" node-b ", "node-missing"}, Page: &pb.Page{Page: 1, Size: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].GetDatasetId() != "dataset_b" || page.GetTotal() != 1 {
		t.Fatalf("filtered datasets=%v page=%v", items, page)
	}
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

func createTestDataset(t *testing.T, ctx context.Context, store *Store, datasetID, nodeID string) *pb.Dataset {
	t.Helper()
	item, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: datasetID, DataSourceId: "source", DataNodeId: nodeID,
		Name: datasetID, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m", KeepDuration: "24h",
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func registerActiveNode(t *testing.T, ctx context.Context, store *Store, nodeID string) {
	t.Helper()
	if _, err := store.RegisterDataNode(ctx, nodeID, "trpc://"+nodeID, nodeID); err != nil {
		t.Fatal(err)
	}
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
		if _, err := store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "space", DataSourceId: id, Name: id, Kind: "internal"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTimeSeriesDatasetRequiresOneCanonicalImmutableFreq(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")
	for _, freq := range []string{"", "1H", "60m", "quarter"} {
		_, err := store.CreateDataset(ctx, &pb.Dataset{
			SpaceId: "space", DatasetId: "dataset_bad", DataSourceId: "source", DataNodeId: "node-a",
			Name: "Bad", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: freq,
		})
		if err == nil || !strings.Contains(err.Error(), "time_series dataset freq must be one of") {
			t.Fatalf("freq %q error = %v, want canonical freq rejection", freq, err)
		}
	}
	record, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset_record", DataSourceId: "source", DataNodeId: "node-a",
		Name: "Record", DataKind: pb.DataKind_DATA_KIND_RECORD, Freq: "quarter",
	})
	if err != nil || record.GetFreq() != "quarter" {
		t.Fatalf("record dataset = %v, %v; a record Dataset keeps a descriptive freq", record, err)
	}

	created := createTestDataset(t, ctx, store, "dataset", "node-a")
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
	registerActiveNode(t, ctx, store, "node-a")
	createTestDataset(t, ctx, store, "dataset", "node-a")
	view, err := store.CreateView(ctx, &pb.View{SpaceId: "space", ViewId: "view", Name: "View", DatasetId: "dataset", Engine: "duckdb", KeepDuration: "24h"})
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
	if _, err := store.UpsertView(ctx, &pb.View{SpaceId: "space", ViewId: "view", Name: "View", DatasetId: "dataset", Engine: "duckdb", KeepDuration: "24h", Freq: "1h"}); err == nil || !strings.Contains(err.Error(), "a View takes its Dataset's freq") {
		t.Fatalf("mismatched view freq error = %v", err)
	}
}
