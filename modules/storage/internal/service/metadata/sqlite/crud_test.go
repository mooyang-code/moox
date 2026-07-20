//go:build legacy_metadata_view

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

func TestMetadataKeywordSearch(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	if _, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "space", Name: "测试空间"}); err != nil {
		t.Fatalf("UpsertSpace: %v", err)
	}

	for _, item := range []*pb.DataSource{
		{SpaceId: "space", DataSourceId: "binance", Name: "Binance", Kind: "exchange", Market: "crypto"},
		{SpaceId: "space", DataSourceId: "tushare", Name: "Tushare", Kind: "vendor_api", Market: "stock"},
	} {
		if _, err := store.UpsertDataSource(ctx, item); err != nil {
			t.Fatalf("UpsertDataSource(%s): %v", item.GetDataSourceId(), err)
		}
	}
	for _, item := range []*pb.Subject{
		{SpaceId: "space", SubjectId: "BTC-USDT", SubjectType: "crypto_pair", Name: "比特币", Market: "crypto", Currency: "USDT"},
		{SpaceId: "space", SubjectId: "000001.SZ", SubjectType: "stock", Name: "平安银行", Market: "stock", Currency: "CNY"},
	} {
		if _, err := store.UpsertSubject(ctx, item); err != nil {
			t.Fatalf("UpsertSubject(%s): %v", item.GetSubjectId(), err)
		}
	}

	sources, sourcePage, err := store.ListDataSources(ctx, "space", "", "", "binance", &pb.Page{Page: 1, Size: 20})
	if err != nil || len(sources) != 1 || sources[0].GetDataSourceId() != "binance" || sourcePage.GetTotal() != 1 {
		t.Fatalf("source keyword search = %+v page=%+v err=%v", sources, sourcePage, err)
	}
	subjects, subjectPage, err := store.ListSubjects(ctx, "space", "", "", nil, "平安", &pb.Page{Page: 1, Size: 20})
	if err != nil || len(subjects) != 1 || subjects[0].GetSubjectId() != "000001.SZ" || subjectPage.GetTotal() != 1 {
		t.Fatalf("subject keyword search = %+v page=%+v err=%v", subjects, subjectPage, err)
	}
}

func TestDatasetTopologyLockRejectsPlacementChanges(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSpaceSourceDataset(t, ctx, store)
	if _, err := store.UpsertPrimaryStoreNode(ctx, &pb.PrimaryStoreNode{
		NodeId: "node-1", Name: "Node 1", Endpoint: "127.0.0.1:20101", Weight: 100, Status: "active",
	}); err != nil {
		t.Fatalf("UpsertPrimaryStoreNode: %v", err)
	}
	route := &pb.PrimaryStoreRoute{
		SpaceId: "space", RouteId: "route-1", DatasetId: "dataset", NodeId: "node-1",
		HashRule: "subject_hash", Priority: 100, Status: "active",
	}
	if _, err := store.UpsertPrimaryStoreRoute(ctx, route); err != nil {
		t.Fatalf("UpsertPrimaryStoreRoute: %v", err)
	}
	if _, err := store.UpsertDevice(ctx, &pb.Device{DeviceId: "device-1", NodeId: "node-1", Name: "Pebble", Engine: "pebble", Endpoint: "/data/one", Status: "active", Attributes: map[string]string{"shard_id": "shard-1"}}); err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}
	if err := store.LockDatasetTopology(ctx, "space", "dataset"); err != nil {
		t.Fatalf("LockDatasetTopology: %v", err)
	}
	changedNode := &pb.PrimaryStoreNode{
		NodeId: "node-1", Name: "Node 1", Endpoint: "127.0.0.1:20102", Weight: 100, Status: "active",
	}
	if _, err := store.UpsertPrimaryStoreNode(ctx, changedNode); err == nil {
		t.Fatal("UpsertPrimaryStoreNode accepted a locked endpoint change")
	}
	changedNode = &pb.PrimaryStoreNode{NodeId: "node-1", Name: "Node 1", Endpoint: "127.0.0.1:20101", Weight: 100, Status: "active", Attributes: map[string]string{"shard_id": "shard-2"}}
	if _, err := store.UpsertPrimaryStoreNode(ctx, changedNode); err == nil {
		t.Fatal("UpsertPrimaryStoreNode accepted a locked shard_id change")
	}
	changedRoute := proto.Clone(route).(*pb.PrimaryStoreRoute)
	changedRoute.Priority = 200
	if _, err := store.UpsertPrimaryStoreRoute(ctx, changedRoute); err == nil {
		t.Fatal("UpsertPrimaryStoreRoute accepted a locked priority change")
	}
	changedDevice := &pb.Device{DeviceId: "device-1", NodeId: "node-1", Name: "Pebble", Engine: "pebble", Endpoint: "/data/two", Status: "active", Attributes: map[string]string{"shard_id": "shard-1"}}
	if _, err := store.UpsertDevice(ctx, changedDevice); err == nil {
		t.Fatal("UpsertDevice accepted a locked endpoint change")
	}
	changedDevice = &pb.Device{DeviceId: "device-1", NodeId: "node-1", Name: "Pebble", Engine: "pebble", Endpoint: "/data/one", Status: "active", Attributes: map[string]string{"shard_id": "shard-2"}}
	if _, err := store.UpsertDevice(ctx, changedDevice); err == nil {
		t.Fatal("UpsertDevice accepted a locked shard_id change")
	}
	if _, err := store.UpsertDevice(ctx, &pb.Device{DeviceId: "device-2", NodeId: "node-1", Name: "Second Pebble", Engine: "pebble", Endpoint: "/data/three", Status: "active", Attributes: map[string]string{"shard_id": "shard-2"}}); err == nil {
		t.Fatal("UpsertDevice accepted a new device on a locked node")
	}
}

func TestFieldGovernanceQueryAndCounts(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedFieldGovernance(t, ctx, store)

	items, page, err := store.ListFields(ctx, coremetadata.FieldQuery{
		SpaceID: "stock_cn", GroupID: "market", IncludeDescendants: true,
		Keyword: "价", Status: "active", SortBy: "field_id", SortOrder: "desc",
	})
	if err != nil {
		t.Fatalf("ListFields: %v", err)
	}
	if len(items) != 2 || items[0].GetFieldId() != "open" || items[1].GetFieldId() != "close" || page.GetTotal() != 2 {
		t.Fatalf("items=%+v page=%+v, want open and close", items, page)
	}
	literal, _, err := store.ListFields(ctx, coremetadata.FieldQuery{SpaceID: "stock_cn", Keyword: "%_"})
	if err != nil || len(literal) != 0 {
		t.Fatalf("literal wildcard query items=%+v err=%v, want no matches", literal, err)
	}

	counts, err := store.CountFieldsByGroup(ctx, "stock_cn")
	if err != nil {
		t.Fatalf("CountFieldsByGroup: %v", err)
	}
	if counts.Total != 3 || counts.Ungrouped != 0 || counts.ByGroup["market"] != 3 || counts.ByGroup["quote"] != 2 || counts.ByGroup["trading"] != 1 {
		t.Fatalf("counts=%+v", counts)
	}
}

func TestBatchUpdateFieldsIsTransactional(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedFieldGovernance(t, ctx, store)

	updated, err := store.BatchUpdateFields(ctx, "stock_cn", []string{"close", "open"}, "trading", "disabled")
	if err != nil || updated != 2 {
		t.Fatalf("BatchUpdateFields updated=%d err=%v", updated, err)
	}
	for _, id := range []string{"close", "open"} {
		field, getErr := store.GetField(ctx, "stock_cn", id)
		if getErr != nil || field.GetGroupId() != "trading" || field.GetStatus() != "disabled" || field.GetUpdatedAt() == "" {
			t.Fatalf("field %s = %+v err=%v", id, field, getErr)
		}
	}

	if _, err := store.BatchUpdateFields(ctx, "stock_cn", []string{"close", "missing"}, "quote", "active"); err == nil {
		t.Fatal("BatchUpdateFields accepted a missing field")
	}
	field, err := store.GetField(ctx, "stock_cn", "close")
	if err != nil || field.GetGroupId() != "trading" || field.GetStatus() != "disabled" {
		t.Fatalf("failed batch partially updated close: %+v err=%v", field, err)
	}
}

func TestCreateFieldIsAtomicUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedFieldGovernance(t, ctx, store)
	const fieldID = "concurrent_create"
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.CreateField(ctx, &pb.Field{SpaceId: "stock_cn", GroupId: "quote", FieldId: fieldID, Name: "并发字段"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	succeeded := 0
	for err := range errs {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful creates = %d, want exactly 1", succeeded)
	}
	if _, err := store.UpdateField(ctx, &pb.Field{SpaceId: "stock_cn", GroupId: "quote", FieldId: "missing", Name: "不存在"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UpdateField missing error = %v, want sql.ErrNoRows", err)
	}
}

func TestDeleteFieldGroupRejectsNonEmptyGroups(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedFieldGovernance(t, ctx, store)
	if err := store.DeleteFieldGroup(ctx, "stock_cn", "market"); err == nil {
		t.Fatal("DeleteFieldGroup deleted a group with children")
	}
	if err := store.DeleteFieldGroup(ctx, "stock_cn", "quote"); err == nil {
		t.Fatal("DeleteFieldGroup deleted a group with fields")
	}
	if _, err := store.UpsertFieldGroup(ctx, &pb.FieldGroup{SpaceId: "stock_cn", GroupId: "empty", Name: "空字段组"}); err != nil {
		t.Fatalf("UpsertFieldGroup(empty): %v", err)
	}
	if err := store.DeleteFieldGroup(ctx, "stock_cn", "empty"); err != nil {
		t.Fatalf("DeleteFieldGroup(empty): %v", err)
	}
	if _, err := store.GetFieldGroup(ctx, "stock_cn", "empty"); err == nil {
		t.Fatal("empty group still exists")
	}
}

func seedFieldGovernance(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	if _, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "stock_cn", Name: "A股市场"}); err != nil {
		t.Fatalf("UpsertSpace: %v", err)
	}
	for _, group := range []*pb.FieldGroup{
		{SpaceId: "stock_cn", GroupId: "market", Name: "市场数据"},
		{SpaceId: "stock_cn", GroupId: "quote", ParentGroupId: "market", Name: "行情价格"},
		{SpaceId: "stock_cn", GroupId: "trading", ParentGroupId: "market", Name: "成交数据"},
	} {
		if _, err := store.UpsertFieldGroup(ctx, group); err != nil {
			t.Fatalf("UpsertFieldGroup(%s): %v", group.GetGroupId(), err)
		}
	}
	for _, field := range []*pb.Field{
		{SpaceId: "stock_cn", GroupId: "quote", FieldId: "close", Name: "收盘价", Description: "交易收盘价格", Status: "active"},
		{SpaceId: "stock_cn", GroupId: "quote", FieldId: "open", Name: "开盘价", Description: "交易开盘价格", Status: "active"},
		{SpaceId: "stock_cn", GroupId: "trading", FieldId: "volume", Name: "成交量", Status: "disabled"},
	} {
		if _, err := store.UpsertField(ctx, field); err != nil {
			t.Fatalf("UpsertField(%s): %v", field.GetFieldId(), err)
		}
	}
}

func TestFieldGroupsAndGroupedFields(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	if _, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "stock_cn", Name: "A股市场"}); err != nil {
		t.Fatalf("UpsertSpace: %v", err)
	}
	for _, group := range []*pb.FieldGroup{
		{SpaceId: "stock_cn", GroupId: "market", Name: "市场数据", SortOrder: 20},
		{SpaceId: "stock_cn", GroupId: "quote", Name: "行情字段", ParentGroupId: "market", SortOrder: 10},
		{SpaceId: "stock_cn", GroupId: "security", Name: "证券基础", SortOrder: 10},
	} {
		if _, err := store.UpsertFieldGroup(ctx, group); err != nil {
			t.Fatalf("UpsertFieldGroup(%s): %v", group.GetGroupId(), err)
		}
	}
	if _, err := store.UpsertFieldGroup(ctx, &pb.FieldGroup{SpaceId: "stock_cn", GroupId: "too_deep", Name: "第三级", ParentGroupId: "quote"}); err == nil {
		t.Fatal("UpsertFieldGroup accepted a third-level group")
	}
	if _, err := store.UpsertFieldGroup(ctx, &pb.FieldGroup{SpaceId: "stock_cn", GroupId: "market", Name: "市场数据", ParentGroupId: "security"}); err == nil {
		t.Fatal("UpsertFieldGroup reparented a group with children into a third level")
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE t_field_groups SET c_parent_group_id = 'security' WHERE c_space_id = 'stock_cn' AND c_group_id = 'market'`); err == nil {
		t.Fatal("database trigger allowed a root group with children to become a child")
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE t_field_groups SET c_parent_group_id = 'quote' WHERE c_space_id = 'stock_cn' AND c_group_id = 'security'`); err == nil {
		t.Fatal("database trigger allowed a third-level field group")
	}
	if _, err := store.UpsertField(ctx, &pb.Field{SpaceId: "stock_cn", FieldId: "close", Name: "收盘价", GroupId: "quote", SortOrder: 20}); err != nil {
		t.Fatalf("UpsertField: %v", err)
	}
	if _, err := store.UpsertField(ctx, &pb.Field{SpaceId: "stock_cn", FieldId: "orphan", Name: "孤立字段"}); err == nil {
		t.Fatal("UpsertField accepted a field without group_id")
	}

	groups, page, err := store.ListFieldGroups(ctx, "stock_cn", "market", nil)
	if err != nil {
		t.Fatalf("ListFieldGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].GetGroupId() != "quote" || page.GetTotal() != 1 {
		t.Fatalf("groups=%+v page=%+v, want quote", groups, page)
	}
	fields, page, err := store.ListFields(ctx, coremetadata.FieldQuery{SpaceID: "stock_cn", GroupID: "quote"})
	if err != nil {
		t.Fatalf("ListFields: %v", err)
	}
	if len(fields) != 1 || fields[0].GetGroupId() != "quote" || fields[0].GetSortOrder() != 20 || page.GetTotal() != 1 {
		t.Fatalf("fields=%+v page=%+v, want grouped close field", fields, page)
	}
}

func TestListDatasetSubjectsPagesInSQL(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)

	seedDatasetSubjects(t, ctx, store, "s1", "s2", "s3")
	if _, err := store.db.ExecContext(ctx, `UPDATE t_dataset_subjects SET c_attrs_json = '{bad json' WHERE c_subject_id = 's3'`); err != nil {
		t.Fatalf("corrupt third row: %v", err)
	}

	items, page, err := store.ListDatasetSubjects(ctx, "space", "dataset", "", &pb.Page{Page: 1, Size: 2})
	if err != nil {
		t.Fatalf("ListDatasetSubjects page 1: %v", err)
	}
	if got := len(items); got != 2 {
		t.Fatalf("len(items) = %d, want 2", got)
	}
	if page.GetTotal() != 3 || !page.GetHasMore() {
		t.Fatalf("page = %+v, want total=3 has_more=true", page)
	}
}

func TestListSubjectSymbolsPagesInSQL(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)

	seedSubjectSymbols(t, ctx, store, "s1", "s2", "s3")
	if _, err := store.db.ExecContext(ctx, `UPDATE t_subject_symbols SET c_attrs_json = '{bad json' WHERE c_subject_id = 's3'`); err != nil {
		t.Fatalf("corrupt third row: %v", err)
	}

	items, page, err := store.ListSubjectSymbols(ctx, "space", "", "source", "", &pb.Page{Page: 1, Size: 2})
	if err != nil {
		t.Fatalf("ListSubjectSymbols page 1: %v", err)
	}
	if got := len(items); got != 2 {
		t.Fatalf("len(items) = %d, want 2", got)
	}
	if page.GetTotal() != 3 || !page.GetHasMore() {
		t.Fatalf("page = %+v, want total=3 has_more=true", page)
	}
}

func TestListViewsPagesBeforeEnrichment(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	for _, viewID := range []string{"view-1", "view-2", "view-3"} {
		if _, err := store.UpsertView(ctx, sqliteTestView("crypto", viewID)); err != nil {
			t.Fatalf("UpsertView(%s): %v", viewID, err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE t_views SET c_attrs_json = '{bad json' WHERE c_view_id = 'view-3'`); err != nil {
		t.Fatalf("corrupt third view: %v", err)
	}

	items, page, err := store.ListViews(ctx, "crypto", "dataset", "active", &pb.Page{Page: 1, Size: 2})
	if err != nil {
		t.Fatalf("ListViews page 1: %v", err)
	}
	if len(items) != 2 || page.GetTotal() != 3 || !page.GetHasMore() {
		t.Fatalf("items=%d page=%+v, want two of three views", len(items), page)
	}
}

func TestRegisterDataSubjectRollsBackAggregateOnBindingFailure(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSpaceSourceDataset(t, ctx, store)
	for _, datasetID := range []string{"dataset-a", "dataset-b"} {
		if _, err := store.UpsertDataset(ctx, &pb.Dataset{
			SpaceId: "space", DatasetId: datasetID, DataSourceId: "source", Name: datasetID,
			DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
		}); err != nil {
			t.Fatalf("UpsertDataset(%s): %v", datasetID, err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TRIGGER reject_second_binding BEFORE INSERT ON t_dataset_subjects
		WHEN NEW.c_dataset_id = 'dataset-b'
		BEGIN SELECT RAISE(ABORT, 'blocked binding'); END
	`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	_, _, err := store.RegisterDataSubject(ctx,
		&pb.Subject{SpaceId: "space", SubjectId: "BTCUSDT", SubjectType: "instrument", Name: "BTC/USDT"},
		&pb.SubjectSymbol{SpaceId: "space", SubjectId: "BTCUSDT", DataSourceId: "source", ExternalSymbol: "BTCUSDT"},
		[]*pb.DatasetSubject{
			{SpaceId: "space", DatasetId: "dataset-a", SubjectId: "BTCUSDT"},
			{SpaceId: "space", DatasetId: "dataset-b", SubjectId: "BTCUSDT"},
		},
	)
	if err == nil {
		t.Fatal("RegisterDataSubject succeeded despite binding failure")
	}
	if _, err := store.GetSubject(ctx, "space", "BTCUSDT"); err == nil {
		t.Fatal("subject remained after aggregate rollback")
	}
	symbols, _, listErr := store.ListSubjectSymbols(ctx, "space", "BTCUSDT", "source", "BTCUSDT", nil)
	if listErr != nil {
		t.Fatalf("ListSubjectSymbols: %v", listErr)
	}
	if len(symbols) != 0 {
		t.Fatalf("symbols = %d, want rollback", len(symbols))
	}
	bindings, _, listErr := store.ListDatasetSubjects(ctx, "space", "", "BTCUSDT", nil)
	if listErr != nil {
		t.Fatalf("ListDatasetSubjects: %v", listErr)
	}
	if len(bindings) != 0 {
		t.Fatalf("bindings = %d, want rollback", len(bindings))
	}
}

func TestUpsertViewColumnBumpsVersionAndPreemptsBuild(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 1
	view.ActiveViewVersion = 1
	view.ActiveIndexId = testIndexA
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	claim := claimBuildReq("owner-1", "build-1", 1)
	claim.ExpectedActiveIndexId = testIndexA
	claim.IndexId = testIndexB
	if _, _, err := store.ClaimViewIndexBuild(ctx, claim); err != nil {
		t.Fatalf("ClaimViewIndexBuild: %v", err)
	}

	_, err := store.UpsertViewColumn(ctx, &pb.ViewColumn{
		SpaceId:    "crypto",
		ViewId:     "spot_kline_1m_view",
		ColumnName: "volume",
		OriginId:   "binance_spot_kline.volume",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	})
	if err != nil {
		t.Fatalf("UpsertViewColumn: %v", err)
	}

	got, err := store.GetView(ctx, "crypto", "spot_kline_1m_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if got.GetViewVersion() != 2 {
		t.Fatalf("view version = %d, want 2", got.GetViewVersion())
	}
	if got.GetIndexBuild() != nil {
		t.Fatalf("index build = %+v, want preempted", got.GetIndexBuild())
	}
	if got.GetActiveIndexId() != testIndexA {
		t.Fatalf("active index changed to %q", got.GetActiveIndexId())
	}
}

func TestUpsertViewShapeChangeAndBuildPreemptionAreAtomic(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.KeepDuration = "24h"
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-1", "build-1", 1)); err != nil {
		t.Fatalf("ClaimViewIndexBuild: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TRIGGER reject_view_build_delete BEFORE DELETE ON t_view_index_builds
		BEGIN SELECT RAISE(ABORT, 'blocked build delete'); END
	`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	changed := sqliteTestView("crypto", "spot_kline_1m_view")
	changed.KeepDuration = "48h"
	if _, err := store.UpsertView(ctx, changed); err == nil {
		t.Fatal("shape change succeeded despite build-delete failure")
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_view_build_delete`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	got, err := store.GetView(ctx, "crypto", "spot_kline_1m_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if got.GetKeepDuration() != "24h" || got.GetViewVersion() != 1 || got.GetIndexBuild().GetBuildId() != "build-1" {
		t.Fatalf("partial shape preemption persisted: %+v", got)
	}
}

func TestUpsertViewColumnAndBuildPreemptionAreAtomic(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-1", "build-1", 1)); err != nil {
		t.Fatalf("ClaimViewIndexBuild: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TRIGGER reject_view_build_delete BEFORE DELETE ON t_view_index_builds
		BEGIN SELECT RAISE(ABORT, 'blocked build delete'); END
	`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	column := &pb.ViewColumn{
		SpaceId: "crypto", ViewId: "spot_kline_1m_view", ColumnName: "volume",
		OriginId: "binance_spot_kline.volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}
	if _, err := store.UpsertViewColumn(ctx, column); err == nil {
		t.Fatal("column upsert succeeded despite build-delete failure")
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_view_build_delete`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	columns, _, err := store.ListViewColumns(ctx, "crypto", "spot_kline_1m_view", nil)
	if err != nil {
		t.Fatalf("ListViewColumns: %v", err)
	}
	for _, got := range columns {
		if got.GetColumnName() == "volume" {
			t.Fatalf("partial column persisted: %+v", got)
		}
	}
	got, err := store.GetView(ctx, "crypto", "spot_kline_1m_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if got.GetViewVersion() != 1 || got.GetIndexBuild().GetBuildId() != "build-1" {
		t.Fatalf("partial column preemption persisted: %+v", got)
	}
}

func TestClaimViewIndexBuildHasSingleOwner(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 2
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}

	build, resumed, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-1", "build-1", 2))
	if err != nil {
		t.Fatalf("first ClaimViewIndexBuild: %v", err)
	}
	if resumed || build.GetState() != pb.ViewIndexBuild_PREPARING {
		t.Fatalf("first claim = resumed %v state %v", resumed, build.GetState())
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-2", "build-2", 2)); !errors.Is(err, ErrViewIndexBuildConflict) {
		t.Fatalf("second claim error = %v, want conflict", err)
	}

	got, err := store.GetView(ctx, "crypto", "spot_kline_1m_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if got.GetIndexBuild().GetBuildId() != "build-1" || got.GetIndexBuild().GetOwnerId() != "owner-1" {
		t.Fatalf("index build = %+v, want first owner", got.GetIndexBuild())
	}
}

func TestClaimViewIndexBuildFencesActivePointer(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ActiveIndexId = testIndexA
	view.ActiveViewVersion = 1
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	wrong := claimBuildReq("owner-1", "build-1", 1)
	wrong.ExpectedActiveIndexId = testIndexB
	if _, _, err := store.ClaimViewIndexBuild(ctx, wrong); !errors.Is(err, ErrViewIndexBuildConflict) {
		t.Fatalf("claim with stale active pointer error = %v, want conflict", err)
	}
	right := claimBuildReq("owner-1", "build-1", 1)
	right.ExpectedActiveIndexId = testIndexA
	right.IndexId = testIndexB
	if _, _, err := store.ClaimViewIndexBuild(ctx, right); err != nil {
		t.Fatalf("claim with current active pointer: %v", err)
	}
}

func TestClaimViewIndexBuildRejectsCurrentActiveSlot(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ActiveIndexId = testIndexA
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	req := claimBuildReq("owner-1", "build-1", 1)
	req.ExpectedActiveIndexId = testIndexA
	req.IndexId = testIndexA
	if _, _, err := store.ClaimViewIndexBuild(ctx, req); err == nil {
		t.Fatal("ClaimViewIndexBuild accepted current active slot")
	}
}

func TestExpiredViewIndexBuildLeaseResumesSameBuild(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	now := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 2
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-1", "build-1", 2)); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	now = now.Add(91 * time.Second)
	req := claimBuildReq("owner-2", "build-1", 2)
	build, resumed, err := store.ClaimViewIndexBuild(ctx, req)
	if err != nil {
		t.Fatalf("resume claim: %v", err)
	}
	if !resumed || build.GetOwnerId() != "owner-2" || build.GetBuildId() != "build-1" {
		t.Fatalf("resumed build = %+v resumed=%v", build, resumed)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-3", "build-2", 2)); !errors.Is(err, ErrViewIndexBuildConflict) {
		t.Fatalf("different build takeover error = %v, want conflict", err)
	}
}

func TestExpiredViewIndexBuildLeaseUsesChronologicalTextOrdering(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	now := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 2
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-1", "build-1", 2)); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	// RFC3339Nano omits zero fractional seconds. Comparing those strings in
	// SQLite puts "...00Z" after "...00.5Z", despite the opposite time order.
	now = now.Add(90*time.Second + 500*time.Millisecond)
	build, resumed, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-2", "build-1", 2))
	if err != nil {
		t.Fatalf("resume claim after fractional boundary: %v", err)
	}
	if !resumed || build.GetOwnerId() != "owner-2" {
		t.Fatalf("resumed build = %+v resumed=%v", build, resumed)
	}
}

func TestUpdateAndActivateViewIndexUseCAS(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 2
	view.ActiveViewVersion = 1
	view.ActiveIndexId = testIndexA
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}

	claim := claimBuildReq("owner-1", "build-1", 2)
	claim.ExpectedActiveIndexId = testIndexA
	claim.IndexId = testIndexB
	if _, _, err := store.ClaimViewIndexBuild(ctx, claim); err != nil {
		t.Fatalf("ClaimViewIndexBuild: %v", err)
	}
	wrongOwner := updateBuildReq("owner-2", pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING)
	if _, err := store.UpdateViewIndexBuild(ctx, wrongOwner); !errors.Is(err, ErrViewIndexBuildConflict) {
		t.Fatalf("wrong-owner update error = %v, want conflict", err)
	}
	building := updateBuildReq("owner-1", pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING)
	building.CursorJson = `{"cursor":"page-1"}`
	building.CoverageStart = "2026-07-09T00:00:00Z"
	building.CoverageEnd = "2026-07-10T00:00:00Z"
	building.EntriesWritten = 25
	if _, err := store.UpdateViewIndexBuild(ctx, building); err != nil {
		t.Fatalf("PREPARING -> BUILDING: %v", err)
	}
	if got, _ := store.GetView(ctx, "crypto", "spot_kline_1m_view"); got.GetActiveIndexId() != testIndexA {
		t.Fatalf("active index before switch = %q, want %q", got.GetActiveIndexId(), testIndexA)
	}
	catchingUp := updateBuildReq("owner-1", pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_CATCHING_UP)
	if _, err := store.UpdateViewIndexBuild(ctx, catchingUp); err != nil {
		t.Fatalf("BUILDING -> CATCHING_UP: %v", err)
	}
	ready := updateBuildReq("owner-1", pb.ViewIndexBuild_CATCHING_UP, pb.ViewIndexBuild_READY)
	ready.CoverageStart = building.CoverageStart
	ready.CoverageEnd = building.CoverageEnd
	ready.EntriesWritten = 25
	if _, err := store.UpdateViewIndexBuild(ctx, ready); err != nil {
		t.Fatalf("CATCHING_UP -> READY: %v", err)
	}
	activated, err := store.ActivateViewIndex(ctx, &pb.ActivateViewIndexReq{
		SpaceId: "crypto", ViewId: "spot_kline_1m_view", BuildId: "build-1", OwnerId: "owner-1",
	})
	if err != nil {
		t.Fatalf("ActivateViewIndex: %v", err)
	}
	if activated.GetActiveIndexId() != testIndexB || activated.GetActiveViewVersion() != 2 {
		t.Fatalf("active after switch = %q/%d", activated.GetActiveIndexId(), activated.GetActiveViewVersion())
	}
	if activated.GetIndexBuild() != nil || activated.GetActiveViewSchemaHash() != "schema-2" {
		t.Fatalf("activated metadata = %+v", activated)
	}
	if len(activated.GetActiveColumns()) != 1 || activated.GetActiveColumns()[0].GetColumnName() != "close" {
		t.Fatalf("active columns = %+v", activated.GetActiveColumns())
	}
}

func TestActivateViewIndexRequiresLiveLease(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	now := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 2
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-1", "build-1", 2)); err != nil {
		t.Fatalf("ClaimViewIndexBuild: %v", err)
	}
	for _, transition := range []*pb.UpdateViewIndexBuildReq{
		updateBuildReq("owner-1", pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING),
		updateBuildReq("owner-1", pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_CATCHING_UP),
		updateBuildReq("owner-1", pb.ViewIndexBuild_CATCHING_UP, pb.ViewIndexBuild_READY),
	} {
		if _, err := store.UpdateViewIndexBuild(ctx, transition); err != nil {
			t.Fatalf("UpdateViewIndexBuild %s: %v", transition.GetNextState(), err)
		}
	}
	now = now.Add(91 * time.Second)
	if _, err := store.ActivateViewIndex(ctx, &pb.ActivateViewIndexReq{
		SpaceId: "crypto", ViewId: "spot_kline_1m_view", BuildId: "build-1", OwnerId: "owner-1",
	}); !errors.Is(err, ErrViewIndexBuildConflict) {
		t.Fatalf("expired owner activation error = %v, want conflict", err)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-2", "build-1", 2)); err != nil {
		t.Fatalf("take over ready build: %v", err)
	}
	if _, err := store.ActivateViewIndex(ctx, &pb.ActivateViewIndexReq{
		SpaceId: "crypto", ViewId: "spot_kline_1m_view", BuildId: "build-1", OwnerId: "owner-2",
	}); err != nil {
		t.Fatalf("new owner activation: %v", err)
	}
}

func TestFailViewIndexBuildRequiresLiveLease(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	now := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 2
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	if _, _, err := store.ClaimViewIndexBuild(ctx, claimBuildReq("owner-1", "build-1", 2)); err != nil {
		t.Fatalf("ClaimViewIndexBuild: %v", err)
	}
	if _, err := store.UpdateViewIndexBuild(ctx, updateBuildReq("owner-1", pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING)); err != nil {
		t.Fatalf("UpdateViewIndexBuild: %v", err)
	}
	now = now.Add(91 * time.Second)
	if _, err := store.FailViewIndexBuild(ctx, &pb.FailViewIndexBuildReq{
		SpaceId: "crypto", ViewId: "spot_kline_1m_view", BuildId: "build-1", OwnerId: "owner-1", Error: "late failure",
	}); !errors.Is(err, ErrViewIndexBuildConflict) {
		t.Fatalf("expired owner failure error = %v, want conflict", err)
	}
}

func TestUpsertViewCannotOverwriteActiveIndexState(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedSQLiteViewDataset(t, ctx, store, "crypto")
	view := sqliteTestView("crypto", "spot_kline_1m_view")
	view.ActiveIndexId = testIndexA
	view.ActiveViewVersion = 1
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("initial UpsertView: %v", err)
	}
	view.ActiveIndexId = testIndexB
	view.ActiveViewVersion = 99
	view.ActiveViewSchemaHash = "forged"
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("second UpsertView: %v", err)
	}
	got, err := store.GetView(ctx, "crypto", "spot_kline_1m_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if got.GetActiveIndexId() != testIndexA || got.GetActiveViewVersion() != 1 || got.GetActiveViewSchemaHash() != "" {
		t.Fatalf("active state overwritten by UpsertView: %+v", got)
	}
}

const (
	testIndexA = "view_s63727970746f_v73706f745f6b6c696e655f316d5f76696577_a"
	testIndexB = "view_s63727970746f_v73706f745f6b6c696e655f316d5f76696577_b"
)

func claimBuildReq(ownerID string, buildID string, version uint64) *pb.ClaimViewIndexBuildReq {
	return &pb.ClaimViewIndexBuildReq{
		SpaceId:               "crypto",
		ViewId:                "spot_kline_1m_view",
		BuildId:               buildID,
		IndexId:               testIndexA,
		Engine:                "duckdb",
		TargetViewVersion:     version,
		ExpectedActiveIndexId: "",
		OwnerId:               ownerID,
		LeaseTtlSeconds:       90,
		SchemaHash:            "schema-2",
		Columns: []*pb.ViewColumn{{
			SpaceId: "crypto", ViewId: "spot_kline_1m_view", ColumnName: "close",
		}},
		SnapshotEnd: "2026-07-10T00:00:00Z",
	}
}

func updateBuildReq(ownerID string, expected pb.ViewIndexBuild_State, next pb.ViewIndexBuild_State) *pb.UpdateViewIndexBuildReq {
	return &pb.UpdateViewIndexBuildReq{
		SpaceId: "crypto", ViewId: "spot_kline_1m_view", BuildId: "build-1", OwnerId: ownerID,
		ExpectedState: expected, NextState: next, LeaseTtlSeconds: 90,
	}
}

func openTestStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	store, err := Open(ctx, Options{
		Path:       filepath.Join(t.TempDir(), "metadata.db"),
		SchemaPath: filepath.Join("..", "..", "..", "..", "schema", "metadata.sql"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return store
}

func sqliteTestView(spaceID string, viewID string) *pb.View {
	return &pb.View{
		SpaceId:          spaceID,
		ViewId:           viewID,
		Name:             viewID,
		PrimaryDatasetId: "dataset",
		DatasetIds:       []string{"dataset"},
		Engine:           "duckdb",
		Status:           "active",
	}
}

func seedSQLiteViewDataset(t *testing.T, ctx context.Context, store *Store, spaceID string) {
	t.Helper()
	if _, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: spaceID, Name: "Space", Status: "active"}); err != nil {
		t.Fatalf("UpsertSpace: %v", err)
	}
	if _, err := store.UpsertDataSource(ctx, &pb.DataSource{
		SpaceId:      spaceID,
		DataSourceId: "source",
		Name:         "Source",
		Kind:         "exchange",
		Status:       "active",
	}); err != nil {
		t.Fatalf("UpsertDataSource: %v", err)
	}
	if _, err := store.UpsertDataset(ctx, &pb.Dataset{
		SpaceId:      spaceID,
		DatasetId:    "dataset",
		DataSourceId: "source",
		Name:         "Dataset",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		Status:       "active",
	}); err != nil {
		t.Fatalf("UpsertDataset: %v", err)
	}
}

func seedDatasetSubjects(t *testing.T, ctx context.Context, store *Store, subjectIDs ...string) {
	t.Helper()
	seedSpaceSourceDataset(t, ctx, store)
	for _, subjectID := range subjectIDs {
		seedSubject(t, ctx, store, subjectID)
		if _, err := store.BindDatasetSubject(ctx, &pb.DatasetSubject{
			SpaceId:   "space",
			DatasetId: "dataset",
			SubjectId: subjectID,
			Status:    "active",
		}); err != nil {
			t.Fatalf("BindDatasetSubject %s: %v", subjectID, err)
		}
	}
}

func seedSubjectSymbols(t *testing.T, ctx context.Context, store *Store, subjectIDs ...string) {
	t.Helper()
	seedSpaceSourceDataset(t, ctx, store)
	for _, subjectID := range subjectIDs {
		seedSubject(t, ctx, store, subjectID)
		if _, err := store.UpsertSubjectSymbol(ctx, &pb.SubjectSymbol{
			SpaceId:        "space",
			SubjectId:      subjectID,
			DataSourceId:   "source",
			ExternalSymbol: subjectID + "_ext",
			Status:         "active",
		}); err != nil {
			t.Fatalf("BindSubjectSymbol %s: %v", subjectID, err)
		}
	}
}

func seedSpaceSourceDataset(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	if _, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "space", Name: "Space", Status: "active"}); err != nil {
		t.Fatalf("UpsertSpace: %v", err)
	}
	if _, err := store.UpsertDataSource(ctx, &pb.DataSource{
		SpaceId:      "space",
		DataSourceId: "source",
		Name:         "Source",
		Kind:         "exchange",
		Status:       "active",
	}); err != nil {
		t.Fatalf("UpsertDataSource: %v", err)
	}
	if _, err := store.UpsertDataset(ctx, &pb.Dataset{
		SpaceId:      "space",
		DatasetId:    "dataset",
		DataSourceId: "source",
		Name:         "Dataset",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		Status:       "active",
	}); err != nil {
		t.Fatalf("UpsertDataset: %v", err)
	}
}

func seedSubject(t *testing.T, ctx context.Context, store *Store, subjectID string) {
	t.Helper()
	if _, err := store.UpsertSubject(ctx, &pb.Subject{
		SpaceId:     "space",
		SubjectId:   subjectID,
		SubjectType: "asset",
		Name:        subjectID,
		Status:      "active",
	}); err != nil {
		t.Fatalf("UpsertSubject %s: %v", subjectID, err)
	}
}
