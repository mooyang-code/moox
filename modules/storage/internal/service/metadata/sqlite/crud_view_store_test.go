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

func TestUpsertViewRejectsEngineAndDatasetChange(t *testing.T) {
	ctx := context.Background()
	store := openViewTestStore(t, ctx)
	view, err := store.GetView(ctx, "space", "source-view")
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
		t.Fatalf("dataset change error = %v", err)
	}
	unchanged, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.GetEngine() != "duckdb" || unchanged.GetDatasetId() != "prices" {
		t.Fatalf("view mutated after rejected update: %v", unchanged)
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

// View 的列由数据集列推出：写入 View 时带的列被忽略，跨 View 的 ListViewColumns 每个 View 各自从 0 编号。
func TestViewColumnsAreDerivedFromDatasetColumns(t *testing.T) {
	ctx := context.Background()
	store := openViewTestStore(t, ctx)
	for _, name := range []string{"open", "close"} {
		if _, err := store.UpsertDatasetColumn(ctx, &pb.DatasetColumn{SpaceId: "space", DatasetId: "prices", ColumnName: name, OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_CUSTOM, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Attributes: map[string]string{"display_name": name}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpsertView(ctx, &pb.View{
		SpaceId: "space", ViewId: "other-view", Name: "Other view", DatasetId: "prices",
		Columns: []*pb.ViewColumn{{ColumnName: "ignored"}},
	}); err != nil {
		t.Fatal(err)
	}
	view, err := store.GetView(ctx, "space", "other-view")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.GetColumns()) != 2 || view.GetColumns()[0].GetColumnName() != "open" || view.GetColumns()[1].GetColumnName() != "close" {
		t.Fatalf("View 列应按数据集列的创建顺序：%v", view.GetColumns())
	}
	columns, page, err := store.ListViewColumns(ctx, "space", "", &pb.Page{Page: 2, Size: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.GetTotal() != 4 || len(columns) != 2 || columns[0].GetViewId() != "source-view" || columns[0].GetSortOrder() != 0 || columns[1].GetSortOrder() != 1 {
		t.Fatalf("分页列出全部 View 的列：page=%v columns=%v", page, columns)
	}
}
