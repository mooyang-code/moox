package view

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

type maintenanceMetadata struct {
	view           *pb.View
	datasetColumns []*pb.DatasetColumn
	completes      int
	extensions     int
	completeErr    error
	// completeRet 不为空时按这个返回，同时不写入 view，模拟提交失败。
	completeRet *pb.RetInfo
	// commitThenFail 模拟提交成功但响应丢失。
	commitThenFail bool
}

func (m *maintenanceMetadata) ListViews(context.Context, *pb.ListViewsReq, ...client.Option) (*pb.ListViewsRsp, error) {
	return &pb.ListViewsRsp{RetInfo: successRetInfo(), Views: []*pb.View{m.view}, PageResult: &pb.PageResult{Page: 1, Size: 100}}, nil
}
func (m *maintenanceMetadata) GetView(context.Context, *pb.GetViewReq, ...client.Option) (*pb.GetViewRsp, error) {
	return &pb.GetViewRsp{RetInfo: successRetInfo(), View: proto.Clone(m.view).(*pb.View)}, nil
}
func (m *maintenanceMetadata) GetDataset(_ context.Context, req *pb.GetDatasetReq, _ ...client.Option) (*pb.GetDatasetRsp, error) {
	kind := pb.DataKind_DATA_KIND_RECORD
	if req.GetDatasetId() == "prices" {
		kind = pb.DataKind_DATA_KIND_TIME_SERIES
	}
	return &pb.GetDatasetRsp{RetInfo: successRetInfo(), Dataset: &pb.Dataset{SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), DataKind: kind}}, nil
}
func (m *maintenanceMetadata) ListDatasetSubjects(_ context.Context, req *pb.ListDatasetSubjectsReq, _ ...client.Option) (*pb.ListDatasetSubjectsRsp, error) {
	var items []*pb.DatasetSubject
	if req.GetDatasetId() == "prices" {
		items = []*pb.DatasetSubject{
			{SpaceId: req.GetSpaceId(), DatasetId: "prices", SubjectId: "A"},
			{SpaceId: req.GetSpaceId(), DatasetId: "prices", SubjectId: "B"},
		}
	}
	return &pb.ListDatasetSubjectsRsp{RetInfo: successRetInfo(), DatasetSubjects: items, PageResult: &pb.PageResult{Page: 1, Size: 1000}}, nil
}
func (m *maintenanceMetadata) ListDatasetColumns(context.Context, *pb.ListDatasetColumnsReq, ...client.Option) (*pb.ListDatasetColumnsRsp, error) {
	return &pb.ListDatasetColumnsRsp{RetInfo: successRetInfo(), Columns: m.datasetColumns, PageResult: &pb.PageResult{Page: 1, Size: 1000}}, nil
}

func (m *maintenanceMetadata) CompleteViewInitialBuild(_ context.Context, req *pb.CompleteViewInitialBuildReq, _ ...client.Option) (*pb.CompleteViewInitialBuildRsp, error) {
	m.completes++
	if m.completeErr != nil && !m.commitThenFail {
		return nil, m.completeErr
	}
	if m.completeRet != nil {
		return &pb.CompleteViewInitialBuildRsp{RetInfo: m.completeRet}, nil
	}
	view := proto.Clone(m.view).(*pb.View)
	view.ActiveIndexId = req.GetIndexId()
	view.ViewRevision = 1
	view.ActiveColumns = req.GetColumns()
	view.ActiveViewSchemaHash = req.GetViewSchemaHash()
	m.view = view
	if m.commitThenFail {
		return nil, m.completeErr
	}
	return &pb.CompleteViewInitialBuildRsp{RetInfo: successRetInfo(), View: proto.Clone(view).(*pb.View)}, nil
}

func (m *maintenanceMetadata) CommitViewSchemaExtension(_ context.Context, req *pb.CommitViewSchemaExtensionReq, _ ...client.Option) (*pb.CommitViewSchemaExtensionRsp, error) {
	m.extensions++
	view := proto.Clone(m.view).(*pb.View)
	view.ViewRevision = req.GetExpectedViewRevision() + 1
	view.ActiveViewSchemaHash = req.GetViewSchemaHash()
	view.ActiveColumns = nil
	for _, column := range req.GetColumns() {
		if column != nil {
			view.ActiveColumns = append(view.ActiveColumns, proto.Clone(column).(*pb.ViewColumn))
		}
	}
	m.view = view
	return &pb.CommitViewSchemaExtensionRsp{RetInfo: successRetInfo(), View: proto.Clone(view).(*pb.View)}, nil
}

func TestViewBarsComeFromThePolicyForTimeSeriesViews(t *testing.T) {
	if got := viewBars(&pb.View{Freq: "1m"}, MaintenanceOptions{Bars: 4320}); got != 4320 {
		t.Fatalf("time-series View bars = %d, want the policy 4320", got)
	}
	if got := viewBars(&pb.View{Freq: "1h"}, MaintenanceOptions{}); got != storagepolicy.DefaultViewBars {
		t.Fatalf("time-series View bars without a policy = %d, want %d", got, storagepolicy.DefaultViewBars)
	}
	if got := viewBars(&pb.View{Engine: "bleve"}, MaintenanceOptions{Bars: 4320}); got != 0 {
		t.Fatalf("record View bars = %d, want 0 (keeps every row)", got)
	}
}

func TestMaintainerCreatesAndActivatesInitialView(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "records", Engine: "bleve", DatasetId: "records",
		Columns: []*pb.ViewColumn{{SpaceId: "space", ViewId: "records", OriginId: "title", ColumnName: "title"}},
	}}
	stop, err := svc.StartViewMaintainer(context.Background(), MaintenanceOptions{Metadata: metadata, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	indexID := viewindex.ViewIndexID("space", "records")
	if metadata.completes != 1 || metadata.view.GetActiveIndexId() != indexID {
		t.Fatalf("首次建索引没有提交：completes=%d active=%q", metadata.completes, metadata.view.GetActiveIndexId())
	}
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	list, err := svc.ListViewIndexes(context.Background(), &pb.ListViewIndexesReq{AuthInfo: auth})
	if err != nil || len(list.GetIndexes()) != 1 {
		t.Fatalf("indexes=%v err=%v", list, err)
	}
	runtime := svc.views[viewRef{spaceID: "space", viewID: "records"}]
	if runtime.active != indexID || runtime.building != "" {
		t.Fatalf("建成后 active=%q building=%q", runtime.active, runtime.building)
	}
	if state := runtime.queryState(); state.active != indexID {
		t.Fatalf("查询路径的副本 active=%q", state.active)
	}
}

// 没有显式投影的 View 取数据集的列，这些列没有排序值：建索引时要编号，否则之后的追加校验永远通不过。
func TestInitialBuildNumbersColumns(t *testing.T) {
	columns := numberedViewColumns([]*pb.ViewColumn{{ColumnName: "b"}, nil, {ColumnName: "a"}})
	if len(columns) != 2 || columns[0].GetSortOrder() != 0 || columns[1].GetSortOrder() != 1 {
		t.Fatalf("编号结果 = %v", columns)
	}
	extended, ok := appendViewColumns(columns, []*pb.ViewColumn{{ColumnName: "a"}, {ColumnName: "c"}})
	if !ok || !viewindex.IsAppendOnlyViewColumns(columns, extended) {
		t.Fatalf("追加结果 = %v ok=%v", extended, ok)
	}
	if _, ok := appendViewColumns(columns, []*pb.ViewColumn{{ColumnName: "a"}}); ok {
		t.Fatal("声明里少了已建列不算加列，已建列也不会被去掉")
	}
}

func TestInitialBuildFailureDiscardsPartialIndexAndRetries(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	view := &pb.View{SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "market", Freq: "1m", Columns: closeColumns()}
	metadata := &maintenanceMetadata{view: view}
	svc := newBuildService(engine)
	reader := &pagedRangeReader{pageSize: 1, failPage: 2, rows: []*pb.TimeSeriesRow{
		historyRow("A", "2026-08-18T00:00:00Z", ""), historyRow("B", "2026-08-18T00:00:00Z", ""),
	}}
	opts := MaintenanceOptions{Metadata: metadata, PrimaryRange: reader, Bars: 10, BackfillPageSize: 100}
	if err := svc.maintainView(context.Background(), opts, svc.internalAuth(), metadata.view); err == nil {
		t.Fatal("回填失败应当返回错误")
	}
	indexID := viewindex.ViewIndexID("space", "prices")
	runtime := svc.views[viewRef{spaceID: "space", viewID: "prices"}]
	if runtime.building != "" || metadata.completes != 0 {
		t.Fatalf("失败后 building=%q completes=%d", runtime.building, metadata.completes)
	}
	if _, ok := svc.indexView[indexID]; ok {
		t.Fatal("半成品的映射应当清掉，后续事件保持未确认")
	}
	reader.failPage = 0
	reader.requests = nil
	if err := svc.maintainView(context.Background(), opts, svc.internalAuth(), metadata.view); err != nil {
		t.Fatalf("下一轮重建：%v", err)
	}
	if metadata.view.GetActiveIndexId() != indexID || runtime.active != indexID {
		t.Fatalf("重建后 metadata=%q runtime=%q", metadata.view.GetActiveIndexId(), runtime.active)
	}
}

// 提交成功但响应丢失：读回元数据确认后转为活动索引，不能当作失败删除。
func TestCompleteResponseLossReadsBackCommittedIndex(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	metadata := &maintenanceMetadata{
		view:        &pb.View{SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "market", Freq: "1m", Status: "active", Columns: closeColumns()},
		completeErr: errors.New("响应丢失"), commitThenFail: true,
	}
	svc := newBuildService(engine)
	opts := MaintenanceOptions{Metadata: metadata, PrimaryRange: &pagedRangeReader{}, Bars: 10}
	if err := svc.maintainView(context.Background(), opts, svc.internalAuth(), metadata.view); err != nil {
		t.Fatal(err)
	}
	indexID := viewindex.ViewIndexID("space", "prices")
	if runtime := svc.views[viewRef{spaceID: "space", viewID: "prices"}]; runtime.active != indexID || runtime.building != "" {
		t.Fatalf("读回后 active=%q building=%q", runtime.active, runtime.building)
	}
	if engine.removes != 1 {
		t.Fatalf("只有准备索引时删过一次半成品，实际 %d 次", engine.removes)
	}
}

// 提交失败且读回看不到活动索引：保留现场，下一轮按元数据决定，这里不能删除索引。
func TestCompleteFailureKeepsIndexForNextPass(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	metadata := &maintenanceMetadata{
		view:        &pb.View{SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "market", Freq: "1m", Status: "active", Columns: closeColumns()},
		completeErr: errors.New("元数据不可用"),
	}
	svc := newBuildService(engine)
	opts := MaintenanceOptions{Metadata: metadata, PrimaryRange: &pagedRangeReader{}, Bars: 10}
	if err := svc.maintainView(context.Background(), opts, svc.internalAuth(), metadata.view); err == nil {
		t.Fatal("提交失败应当返回错误")
	}
	if runtime := svc.views[viewRef{spaceID: "space", viewID: "prices"}]; runtime.building == "" || runtime.active != "" {
		t.Fatalf("提交失败后 active=%q building=%q", runtime.active, runtime.building)
	}
	if engine.removes != 1 {
		t.Fatalf("提交失败不能删除索引，remove=%d", engine.removes)
	}
}

// 元数据说没有活动索引时（View 删除后同名重建），残留在内存里的活动索引不再有效。
func TestInitialBuildClearsStaleRuntimeActive(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	metadata := &maintenanceMetadata{view: &pb.View{SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "market", Freq: "1m", Columns: closeColumns()}}
	svc := newBuildService(engine)
	indexID := viewindex.ViewIndexID("space", "prices")
	svc.views[viewRef{spaceID: "space", viewID: "prices"}] = &viewRuntime{active: indexID}
	opts := MaintenanceOptions{Metadata: metadata, PrimaryRange: &pagedRangeReader{}, Bars: 10}
	if err := svc.maintainView(context.Background(), opts, svc.internalAuth(), metadata.view); err != nil {
		t.Fatal(err)
	}
	if metadata.completes != 1 {
		t.Fatalf("completes=%d", metadata.completes)
	}
}

// 活动索引不存在时只报错，不自动重建。
func TestMaintenanceDoesNotRebuildMissingActiveIndex(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	svc.engines["duckdb"] = &queryEngine{stats: viewindex.ViewIndexStats{Exists: false}}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "prices",
		ActiveIndexId: "prices-a", ViewRevision: 1,
	}}
	err = svc.maintainView(context.Background(), MaintenanceOptions{Metadata: metadata}, svc.internalAuth(), metadata.view)
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("活动索引缺失 err=%v", err)
	}
	if metadata.completes != 0 {
		t.Fatalf("不应重建：completes=%d", metadata.completes)
	}
}

// 文件超过告警线只告警：不改动索引，也不触发任何重建。
func TestViewFileOverLimitOnlyAlerts(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	svc.SetMetrics(metrics)
	view := &pb.View{SpaceId: "space", ViewId: "prices", ActiveIndexId: "prices-a"}
	opts := MaintenanceOptions{MaxViewFileBytes: 100}
	svc.observeViewFileSize(view, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 100}, opts)
	if len(svc.fileOverLimit) != 1 {
		t.Fatalf("超限的 View = %v", svc.fileOverLimit)
	}
	svc.observeViewFileSize(view, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 99}, opts)
	if len(svc.fileOverLimit) != 0 {
		t.Fatalf("恢复后超限的 View = %v", svc.fileOverLimit)
	}
	svc.observeViewFileSize(view, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 100}, opts)
	svc.retainFileOverLimit(map[observability.ViewKey]struct{}{})
	if len(svc.fileOverLimit) != 0 {
		t.Fatalf("已删除的 View 不应继续计入：%v", svc.fileOverLimit)
	}
}

// 维护读到的元数据早于定时任务刚提交的加列时，不能把写入契约改回旧修订。
func TestAttachActiveViewIgnoresStaleRevision(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	current := &pb.View{SpaceId: "space", ViewId: "prices", DatasetId: "prices", ActiveIndexId: "prices-a", ViewRevision: 2, ActiveViewSchemaHash: "new", Engine: "bleve", Status: "active"}
	if err := svc.AttachActiveView(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	stale := proto.Clone(current).(*pb.View)
	stale.ViewRevision, stale.ActiveViewSchemaHash = 1, "old"
	if err := svc.AttachActiveView(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	if schema := svc.schemas["prices-a"]; schema.ViewVersion != 2 || schema.SchemaHash != "new" {
		t.Fatalf("写入契约被旧快照改回：%+v", schema)
	}
}

func TestRestoreActiveViewsAttachesExistingPhysicalIndex(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	// Restore must select the engine from View metadata. A fresh Service has
	// no indexID -> engine mapping yet; that mapping is created by attach.
	svc.engines["bleve"] = &queryEngine{stats: viewindex.ViewIndexStats{Exists: true, ViewVersion: 1}}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", DatasetId: "prices",
		ActiveIndexId: "prices-a", ViewRevision: 1,
	}}
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadata}); err != nil {
		t.Fatalf("restore active view: %v", err)
	}
	svc.mu.RLock()
	engineName := svc.indexEngine["prices-a"]
	runtime := svc.views[viewRef{spaceID: "space", viewID: "prices"}]
	svc.mu.RUnlock()
	if engineName != "bleve" {
		t.Fatalf("restored index engine=%q, want bleve", engineName)
	}
	if runtime == nil {
		t.Fatal("restore did not create view runtime")
	}
	runtime.mu.Lock()
	active := runtime.active
	runtime.mu.Unlock()
	if active != "prices-a" {
		t.Fatalf("restored active index=%q, want prices-a", active)
	}
}

func TestRestoreActiveViewsFailsWhenMetadataActiveIndexIsMissing(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	svc.engines["bleve"] = &queryEngine{}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", DatasetId: "prices",
		ActiveIndexId: "missing-active", ViewRevision: 1,
	}}
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadata}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("restore missing active error = %v", err)
	}
}

func TestRestoreActiveViewsSkipsPhysicalContractMismatch(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	svc.engines["bleve"] = &queryEngine{stats: viewindex.ViewIndexStats{Exists: true, ViewVersion: 1, SchemaHash: "old"}}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", DatasetId: "prices",
		ActiveIndexId: "prices-a", ViewRevision: 2, ActiveViewSchemaHash: "new",
	}}
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadata}); err != nil {
		t.Fatalf("restore mismatch should skip, got %v", err)
	}
	indexID, runtime := svc.activeIndex("space", "prices")
	if runtime != nil && strings.TrimSpace(indexID) != "" {
		t.Fatalf("mismatched view attached active=%q", indexID)
	}
}

func TestMaintenanceStatsActiveIndexBeforeAttachingIt(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	statErr := errors.New("legacy duckdb schema requires cleanup and rebuild")
	engine := &queryEngine{statErr: statErr}
	svc.engines["duckdb"] = engine
	view := &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "prices",
		ActiveIndexId: "prices-a", ViewRevision: 1,
	}
	err = svc.maintainView(context.Background(), MaintenanceOptions{}, svc.internalAuth(), view)
	if !errors.Is(err, statErr) {
		t.Fatalf("maintenance error=%v want=%v", err, statErr)
	}
	svc.mu.RLock()
	runtime := svc.views[viewRef{spaceID: "space", viewID: "prices"}]
	svc.mu.RUnlock()
	if runtime != nil {
		runtime.mu.Lock()
		active := runtime.active
		runtime.mu.Unlock()
		if active != "" {
			t.Fatalf("invalid active index attached before schema validation: %q", active)
		}
	}
	if _, exists := svc.indexEngine["prices-a"]; exists {
		t.Fatal("invalid active index mapping remains attached")
	}
}

// 共用数据集时，没能挂接的 View 要让行事件保持未确认：否则已挂接的 View 把事件确认掉，这个 View 恢复后补不回这些行。
func TestUnattachedViewHoldsDatasetEvents(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	svc.engines["bleve"] = &queryEngine{stats: viewindex.ViewIndexStats{Exists: true, ViewVersion: 1, SchemaHash: "old"}}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", DatasetId: "market",
		ActiveIndexId: "prices-a", ViewRevision: 2, ActiveViewSchemaHash: "new",
	}}
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "space", DatasetId: "market", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}}
	if err := svc.applyDatasetEvent(context.Background(), "space", "market", []*pb.RowFieldUpsert{row}); err == nil || !strings.Contains(err.Error(), "尚未就绪") {
		t.Fatalf("未挂接的 View 应当让事件保持未确认，err=%v", err)
	}
}

// 数据集列禁用只是逻辑上的：行里的值还在，只在返回时过滤；重新启用后恢复。
func TestDisabledDatasetColumnIsFilteredFromResults(t *testing.T) {
	key := timeSeriesTestRowKey("")
	engine := &queryEngine{rows: []*pb.RowFieldValues{{Key: key, Fields: []*pb.FieldValue{{FieldId: "close"}, {FieldId: "old"}}}}}
	svc, auth := queryTestService(engine, true)
	view := &pb.View{SpaceId: "space", ViewId: "prices", DatasetId: "market"}
	metadata := &maintenanceMetadata{view: view, datasetColumns: []*pb.DatasetColumn{{ColumnName: "close", Status: "active"}, {ColumnName: "old", Status: "disabled"}}}
	query := func() []*pb.FieldValue {
		t.Helper()
		rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
			AuthInfo: auth, SpaceId: "space", ViewId: "prices",
			Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 1 {
			t.Fatalf("rsp=%v err=%v", rsp, err)
		}
		return rsp.GetRows()[0].GetFields()
	}
	svc.refreshHiddenColumns(context.Background(), metadata, auth, view)
	if fields := query(); len(fields) != 1 || fields[0].GetFieldId() != "close" {
		t.Fatalf("禁用的列应当被过滤：%v", fields)
	}
	metadata.datasetColumns[1].Status = "active"
	svc.refreshHiddenColumns(context.Background(), metadata, auth, view)
	if fields := query(); len(fields) != 2 {
		t.Fatalf("重新启用后应当恢复：%v", fields)
	}
}
