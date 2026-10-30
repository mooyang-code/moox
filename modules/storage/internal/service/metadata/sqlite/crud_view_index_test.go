package sqlite

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	coreviewindex "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

func testViewColumn(name string, order uint32) *pb.ViewColumn {
	return &pb.ViewColumn{
		SpaceId: "space", ViewId: "source-view", ColumnName: name, OriginId: name,
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, SortOrder: order,
	}
}

func testViewSchemaHash(view *pb.View, columns []*pb.ViewColumn) string {
	return coreviewindex.HashViewIndexSchema(coreviewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), PrimaryDatasetID: view.GetDatasetId(),
		Engine: view.GetEngine(), Columns: columns,
	})
}

// completeTestBuild 把 View 提交为建成状态，已建列只有 close。
func completeTestBuild(t *testing.T, ctx context.Context, store *Store) *pb.View {
	t.Helper()
	view, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	columns := []*pb.ViewColumn{testViewColumn("close", 0)}
	completed, err := store.CompleteViewInitialBuild(ctx, &pb.CompleteViewInitialBuildReq{
		SpaceId: "space", ViewId: "source-view", IndexId: coreviewindex.ViewIndexID("space", "source-view"),
		Columns: columns, ViewSchemaHash: testViewSchemaHash(view, columns),
	})
	if err != nil {
		t.Fatal(err)
	}
	return completed
}

func TestCompleteViewInitialBuildIsIdempotentAndWritesGeneration(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	view := completeTestBuild(t, ctx, store)
	indexID := coreviewindex.ViewIndexID("space", "source-view")
	if view.GetActiveIndexId() != indexID || view.GetViewRevision() != 1 || len(view.GetActiveColumns()) != 1 {
		t.Fatalf("建成后 = %v", view)
	}
	generation := view.GetAttributes()[metadatastore.IndexGenerationAttribute]
	if generation == "" {
		t.Fatal("建成时应当写入索引代次")
	}
	// 响应丢失后重试不改变已提交的内容。
	retried, err := store.CompleteViewInitialBuild(ctx, &pb.CompleteViewInitialBuildReq{
		SpaceId: "space", ViewId: "source-view", IndexId: indexID,
		Columns: view.GetActiveColumns(), ViewSchemaHash: view.GetActiveViewSchemaHash(),
	})
	if err != nil || retried.GetViewRevision() != 1 || retried.GetAttributes()[metadatastore.IndexGenerationAttribute] != generation {
		t.Fatalf("重试 = %v err=%v", retried, err)
	}
	columns := []*pb.ViewColumn{testViewColumn("volume", 0)}
	if _, err := store.CompleteViewInitialBuild(ctx, &pb.CompleteViewInitialBuildReq{
		SpaceId: "space", ViewId: "source-view", IndexId: indexID, Columns: columns, ViewSchemaHash: testViewSchemaHash(view, columns),
	}); !errors.Is(err, ErrViewIndexConflict) {
		t.Fatalf("已建成后用另一份契约提交，err=%v，应当冲突", err)
	}
}

func TestCompleteViewInitialBuildRejectsMismatchedIdentity(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	view, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	columns := []*pb.ViewColumn{testViewColumn("close", 0)}
	if _, err := store.CompleteViewInitialBuild(ctx, &pb.CompleteViewInitialBuildReq{
		SpaceId: "space", ViewId: "source-view", IndexId: coreviewindex.ViewIndexID("space", "other"),
		Columns: columns, ViewSchemaHash: testViewSchemaHash(view, columns),
	}); err == nil {
		t.Fatal("索引标识不属于这个 View 时应当拒绝")
	}
	if _, err := store.CompleteViewInitialBuild(ctx, &pb.CompleteViewInitialBuildReq{
		SpaceId: "space", ViewId: "source-view", IndexId: coreviewindex.ViewIndexID("space", "source-view"),
		Columns: columns, ViewSchemaHash: "stale",
	}); err == nil {
		t.Fatal("哈希与已建列不一致时应当拒绝")
	}
}

func TestViewEditsKeepIndexStateAndCannotForgeGeneration(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	built := completeTestBuild(t, ctx, store)
	generation := built.GetAttributes()[metadatastore.IndexGenerationAttribute]
	edited := proto.Clone(built).(*pb.View)
	edited.Description = "改说明"
	edited.ActiveIndexId = ""
	edited.ViewRevision = 9
	edited.ActiveColumns = nil
	edited.Attributes[metadatastore.IndexGenerationAttribute] = "forged"
	saved, err := store.UpsertView(ctx, edited)
	if err != nil {
		t.Fatal(err)
	}
	if saved.GetActiveIndexId() != built.GetActiveIndexId() || saved.GetViewRevision() != 1 || len(saved.GetActiveColumns()) != 1 ||
		saved.GetAttributes()[metadatastore.IndexGenerationAttribute] != generation {
		t.Fatalf("修改定义改动了索引状态：%v", saved)
	}
}

func TestCommitViewSchemaExtensionCASAndRetry(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	built := completeTestBuild(t, ctx, store)
	columns := []*pb.ViewColumn{testViewColumn("close", 0), testViewColumn("bias_20", 1)}
	req := &pb.CommitViewSchemaExtensionReq{
		SpaceId: "space", ViewId: "source-view", ActiveIndexId: built.GetActiveIndexId(),
		ExpectedViewRevision: 1, ExpectedSchemaHash: built.GetActiveViewSchemaHash(),
		Columns: columns, ViewSchemaHash: testViewSchemaHash(built, columns),
	}
	committed, err := store.CommitViewSchemaExtension(ctx, req)
	if err != nil {
		t.Fatalf("提交加列：%v", err)
	}
	if committed.GetViewRevision() != 2 || committed.GetActiveViewSchemaHash() != req.GetViewSchemaHash() || len(committed.GetActiveColumns()) != 2 {
		t.Fatalf("提交后 = %v", committed)
	}
	// 响应丢失后重试是幂等的，不会再加一次修订号。
	retried, err := store.CommitViewSchemaExtension(ctx, req)
	if err != nil || retried.GetViewRevision() != 2 {
		t.Fatalf("重试 = %v err=%v", retried, err)
	}
	stale := proto.Clone(req).(*pb.CommitViewSchemaExtensionReq)
	stale.ExpectedSchemaHash = "stale"
	stale.ViewSchemaHash = "other"
	if _, err := store.CommitViewSchemaExtension(ctx, stale); !errors.Is(err, metadatastore.ErrViewSchemaExtensionConflict) {
		t.Fatalf("过期的提交 err=%v，应当冲突", err)
	}
	removal := proto.Clone(req).(*pb.CommitViewSchemaExtensionReq)
	removal.ExpectedViewRevision = 2
	removal.ExpectedSchemaHash = committed.GetActiveViewSchemaHash()
	removal.Columns = []*pb.ViewColumn{testViewColumn("close", 0)}
	removal.ViewSchemaHash = testViewSchemaHash(built, removal.Columns)
	if _, err := store.CommitViewSchemaExtension(ctx, removal); !errors.Is(err, metadatastore.ErrViewSchemaExtensionConflict) {
		t.Fatalf("删列的提交 err=%v，应当冲突", err)
	}
}

func TestViewColumnsAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	if _, err := store.UpsertViewColumn(ctx, testViewColumn("close", 0)); err != nil {
		t.Fatal(err)
	}
	changedType := testViewColumn("close", 0)
	changedType.ValueType = pb.FieldValueType_FIELD_VALUE_TYPE_STRING
	if _, err := store.UpsertViewColumn(ctx, changedType); err == nil || !strings.Contains(err.Error(), "不能修改") {
		t.Fatalf("改列类型 err=%v，应当拒绝", err)
	}
	moved := testViewColumn("close", 7)
	moved.Attributes = map[string]string{"display_name": "收盘价"}
	saved, err := store.UpsertViewColumn(ctx, moved)
	if err != nil {
		t.Fatal(err)
	}
	if saved.GetSortOrder() != 0 {
		t.Fatalf("已有列的排序应当保持原值，sort_order=%d", saved.GetSortOrder())
	}
	added, err := store.UpsertViewColumn(ctx, testViewColumn("volume", 0))
	if err != nil {
		t.Fatal(err)
	}
	if added.GetSortOrder() != 1 {
		t.Fatalf("新列应当排在现有列之后，sort_order=%d", added.GetSortOrder())
	}
	view, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	view.Columns = []*pb.ViewColumn{testViewColumn("close", 0)}
	if _, err := store.ReplaceViewColumns(ctx, view); err == nil || !strings.Contains(err.Error(), "volume") {
		t.Fatalf("整体替换时去掉列 err=%v，应当拒绝", err)
	}
}

func TestReplaceViewColumnsMustKeepBuiltColumns(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	built := completeTestBuild(t, ctx, store)
	view := proto.Clone(built).(*pb.View)
	view.Columns = []*pb.ViewColumn{testViewColumn("volume", 0)}
	if _, err := store.ReplaceViewColumns(ctx, view); err == nil || !strings.Contains(err.Error(), "close") {
		t.Fatalf("声明里去掉已建列 err=%v，应当拒绝", err)
	}
	view.Columns = []*pb.ViewColumn{testViewColumn("close", 0), testViewColumn("volume", 1)}
	if _, err := store.ReplaceViewColumns(ctx, view); err != nil {
		t.Fatal(err)
	}
}

func TestViewDefinitionIsImmutable(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	view, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	view.GrainKeys = []string{"subject_id", "data_time"}
	if view, err = store.UpsertView(ctx, view); err != nil {
		t.Fatal(err)
	}
	engine := proto.Clone(view).(*pb.View)
	engine.Engine = "bleve"
	if _, err := store.UpsertView(ctx, engine); err == nil {
		t.Fatal("改引擎应当被拒绝")
	}
	grain := proto.Clone(view).(*pb.View)
	grain.GrainKeys = append(grain.GrainKeys, "extra")
	if _, err := store.UpsertView(ctx, grain); err == nil {
		t.Fatal("改主键维度应当被拒绝")
	}
}

// 列被禁用，或引用的字段不再启用，都算逻辑上已删除：随 View 下发，View 的列定义不变。
func TestViewReportsHiddenDatasetColumns(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO t_field_groups (c_space_id, c_group_id, c_name) VALUES ('space', 'hidden-group', '分组')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO t_fields (c_space_id, c_field_id, c_group_id, c_name, c_value_type, c_status) VALUES ('space', 'gone', 'hidden-group', '已删', 'double', 'deleted')`); err != nil {
		t.Fatal(err)
	}
	for _, column := range []struct{ name, originType, originID, status string }{
		{"kept", "custom", "", "active"},
		{"disabled_column", "custom", "", "disabled"},
		{"gone", "field", "gone", "active"},
	} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO t_dataset_columns (c_space_id, c_dataset_id, c_column_name, c_origin_type, c_origin_id, c_value_type, c_status) VALUES ('space', 'prices', ?, ?, ?, 'double', ?)`, column.name, column.originType, column.originID, column.status); err != nil {
			t.Fatal(err)
		}
	}
	view, err := store.GetView(ctx, "space", "source-view")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(view.GetHiddenColumns(), []string{"disabled_column", "gone"}) {
		t.Fatalf("hidden_columns = %v", view.GetHiddenColumns())
	}
	views, _, err := store.ListViews(ctx, "space", "prices", "", nil)
	if err != nil || len(views) != 1 || !slices.Equal(views[0].GetHiddenColumns(), view.GetHiddenColumns()) {
		t.Fatalf("ListViews = %v err=%v", views, err)
	}
	// 客户端带回的值不落库，读取时总是重新计算。
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := store.db.QueryRowContext(ctx, `SELECT c_attrs_json FROM t_views WHERE c_view_id = 'source-view'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "hidden_columns") {
		t.Fatalf("hidden_columns 不应落库：%s", raw)
	}
}

// 删除字段是逻辑上的：引用它的列被隐藏，重新启用后恢复。
func TestBatchDeleteFieldHidesColumnUntilReEnabled(t *testing.T) {
	ctx := context.Background()
	store := openViewPeriodTestStore(t, ctx)
	if _, err := store.UpsertFieldGroup(ctx, &pb.FieldGroup{SpaceId: "space", GroupId: "hidden-group", Name: "分组"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertField(ctx, &pb.Field{SpaceId: "space", GroupId: "hidden-group", FieldId: "gone", Name: "将删除", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertDatasetColumn(ctx, &pb.DatasetColumn{
		SpaceId: "space", DatasetId: "prices", ColumnName: "gone", OriginId: "gone",
		OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}); err != nil {
		t.Fatal(err)
	}
	hidden := func() []string {
		t.Helper()
		view, err := store.GetView(ctx, "space", "source-view")
		if err != nil {
			t.Fatal(err)
		}
		return view.GetHiddenColumns()
	}
	if got := hidden(); len(got) != 0 {
		t.Fatalf("启用状态不应隐藏：%v", got)
	}
	if _, err := store.BatchUpdateFields(ctx, "space", []string{"gone"}, "", "deleted"); err != nil {
		t.Fatal(err)
	}
	if got := hidden(); !slices.Equal(got, []string{"gone"}) {
		t.Fatalf("删除后应隐藏：%v", got)
	}
	field, err := store.GetField(ctx, "space", "gone")
	if err != nil || field.GetStatus() != "deleted" {
		t.Fatalf("字段状态 = %v err=%v", field.GetStatus(), err)
	}
	if _, err := store.BatchUpdateFields(ctx, "space", []string{"gone"}, "", "active"); err != nil {
		t.Fatal(err)
	}
	if got := hidden(); len(got) != 0 {
		t.Fatalf("重新启用后应恢复：%v", got)
	}
	if _, err := store.BatchUpdateFields(ctx, "space", []string{"gone"}, "", "archived"); err == nil {
		t.Fatal("批量操作不应接受其他状态")
	}
}
