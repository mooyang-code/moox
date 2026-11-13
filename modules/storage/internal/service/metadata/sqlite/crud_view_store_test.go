package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestCreateViewIsAtomicAndDoesNotReplaceExistingIdentity(t *testing.T) {
	ctx := context.Background()
	store := openViewTestStore(t, ctx)
	original, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateView(ctx, &pb.View{SpaceId: "space", ViewId: "source-view", Name: "替代视图", DatasetId: "fundamentals", Engine: "duckdb"})
	if !errors.Is(err, metadatastore.ErrViewExists) {
		t.Fatalf("CreateView error = %v, want ErrViewExists", err)
	}
	after, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	if after.GetName() != original.GetName() || after.GetDatasetId() != original.GetDatasetId() {
		t.Fatalf("create-only call replaced existing view: before=%v after=%v", original, after)
	}
}

func TestUpsertViewMergesPartialColumnsAndReplaceKeepsExplicitMarker(t *testing.T) {
	ctx := context.Background()
	store := openViewTestStore(t, ctx)
	view, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	view.Columns = []*pb.ViewColumn{{SpaceId: "space", ViewId: "source-view", ColumnName: "close"}, {SpaceId: "space", ViewId: "source-view", ColumnName: "old"}}
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatal(err)
	}
	view.Columns = []*pb.ViewColumn{{SpaceId: "space", ViewId: "source-view", ColumnName: "close"}}
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatal(err)
	}
	columns, _, err := store.ListViewColumns(ctx, "space", "source-view", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 2 {
		t.Fatalf("columns after partial update = %v", columns)
	}
	view.Columns = []*pb.ViewColumn{{SpaceId: "space", ViewId: "source-view", ColumnName: "close"}, {SpaceId: "space", ViewId: "source-view", ColumnName: "old"}}
	if _, err := store.ReplaceViewColumns(ctx, view); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	if stored.GetAttributes()["moox.columns_explicit"] != "true" {
		t.Fatalf("empty replacement lost explicit-column marker: attrs=%v", stored.GetAttributes())
	}
	stored.Attributes = nil
	stored.Description = "ordinary metadata update"
	if _, err := store.UpsertView(ctx, stored); err != nil {
		t.Fatal(err)
	}
	stored, err = store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	if stored.GetAttributes()["moox.columns_explicit"] != "true" {
		t.Fatalf("ordinary update cleared explicit-column marker: attrs=%v", stored.GetAttributes())
	}
}

func TestUpsertViewRejectsEngineAndPrimaryChangeWithActiveIndex(t *testing.T) {
	ctx := context.Background()
	store := openViewTestStore(t, ctx)
	view, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	view.ActiveIndexId = "source-a"
	view.Engine = "duckdb"
	view.DatasetId = "prices"
	raw, err := marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE t_views SET c_active_index_id = ?, c_engine = ?, c_dataset_id = ?, c_attrs_json = ? WHERE c_space_id = ? AND c_view_id = ?`, "source-a", "duckdb", "prices", raw, "space", "source-view"); err != nil {
		t.Fatal(err)
	}
	view, err = store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	view.Engine = "bleve"
	if _, err := store.UpsertView(ctx, view); err == nil || !strings.Contains(err.Error(), "引擎不能修改") {
		t.Fatalf("engine change error = %v", err)
	}
	view.Engine = "duckdb"
	view.DatasetId = "fundamentals"
	if _, err := store.UpsertView(ctx, view); err == nil || !strings.Contains(err.Error(), "数据集不能修改") {
		t.Fatalf("primary change error = %v", err)
	}
	unchanged, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.GetActiveIndexId() != "source-a" || unchanged.GetEngine() != "duckdb" || unchanged.GetDatasetId() != "prices" {
		t.Fatalf("active view mutated after rejected update: %v", unchanged)
	}
}

func TestGetAndListViewsExposePersistedTimestamps(t *testing.T) {
	ctx := context.Background()
	store := openViewTestStore(t, ctx)

	view, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	if view.GetCreatedAt() == "" || view.GetUpdatedAt() == "" {
		t.Fatalf("GetView timestamps = created %q updated %q", view.GetCreatedAt(), view.GetUpdatedAt())
	}

	views, _, err := store.ListViews(ctx, "space", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) == 0 || views[0].GetCreatedAt() == "" || views[0].GetUpdatedAt() == "" {
		t.Fatalf("ListViews timestamps = %v", views)
	}
}

func openViewTestStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	createTestDataset(t, ctx, store, "prices")
	createTestDataset(t, ctx, store, "fundamentals")
	if _, err := store.UpsertView(ctx, &pb.View{
		SpaceId: "space", ViewId: "source-view", Name: "Source view", DatasetId: "prices",
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestUpsertViewRejectsColumnsOwnedByAnotherView(t *testing.T) {
	ctx := context.Background()
	store := openViewTestStore(t, ctx)
	if _, err := store.UpsertView(ctx, &pb.View{
		SpaceId: "space", ViewId: "other-view", Name: "Other view", DatasetId: "prices",
		Columns: []*pb.ViewColumn{{ColumnName: "close"}},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetView(ctx, "space", "other-view")
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	source.Columns = []*pb.ViewColumn{{SpaceId: "space", ViewId: "other-view", ColumnName: "close", SortOrder: 99}}
	if _, err := store.UpsertView(ctx, source); err == nil {
		t.Fatal("期望拒绝携带其他 view_id 的列")
	}
	if _, err := store.ReplaceViewColumns(ctx, source); err == nil {
		t.Fatal("ReplaceViewColumns 期望拒绝携带其他 view_id 的列")
	}
	after, err := store.GetView(ctx, "space", "other-view")
	if err != nil {
		t.Fatal(err)
	}
	if after.GetColumns()[0].GetSortOrder() == 99 {
		t.Fatalf("另一个 View 被改动: before=%v after=%v", before, after)
	}
}
