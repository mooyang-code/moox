package view

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/serviceauth"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/client"
)

type maintenanceMetadata struct {
	view *pb.View
}

func (m *maintenanceMetadata) ListViews(context.Context, *pb.ListViewsReq, ...client.Option) (*pb.ListViewsRsp, error) {
	return &pb.ListViewsRsp{RetInfo: successRetInfo(), Views: []*pb.View{m.view}, PageResult: &pb.PageResult{Page: 1, Size: 100}}, nil
}

// builtStats 是一个建好的索引文件记下的状态：属于 dataset、记下了跟随进度。
func builtStats(dataset string, revision uint64, hash string) viewindex.ViewIndexStats {
	return viewindex.ViewIndexStats{Exists: true, DatasetID: dataset, ViewVersion: revision, SchemaHash: hash, Follow: viewindex.FollowState{Cursor: viewindex.ChangeCursor{StoreID: "store", Sequence: 1}}}
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
	auth := &pb.AuthInfo{AppId: "caller", AppKey: serviceauth.Key("view-secret", "caller")}
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
	if runtime.building != "" || runtime.active != "" {
		t.Fatalf("失败后 building=%q active=%q", runtime.building, runtime.active)
	}
	if _, ok := svc.indexView[indexID]; ok {
		t.Fatal("半成品的映射应当清掉，后续事件保持未确认")
	}
	reader.failPage = 0
	reader.requests = nil
	if err := svc.maintainView(context.Background(), opts, svc.internalAuth(), metadata.view); err != nil {
		t.Fatalf("下一轮重建：%v", err)
	}
	if runtime.active != indexID {
		t.Fatalf("重建后 active=%q", runtime.active)
	}
}

// 索引文件属于另一个数据集（View 删除后同名重建、旧文件还没清理）：残留在内存里的活动索引不再有效，重新建。
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
	if engine.removes != 1 {
		t.Fatalf("应当删掉旧文件重建，remove=%d", engine.removes)
	}
	if schema := svc.schemas[indexID]; schema.PrimaryDatasetID != "market" || schema.ViewVersion != initialViewRevision {
		t.Fatalf("重建后的契约 = %+v", schema)
	}
}

// 索引文件不见了：维护流程重新建索引（先停掉跟随器、清掉内存里的活动索引），不需要元数据参与。
func TestMaintenanceRebuildsMissingIndex(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	engine := &queryEngine{stats: viewindex.ViewIndexStats{Exists: false}}
	svc.engines["duckdb"] = engine
	metadata := &maintenanceMetadata{view: &pb.View{SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "prices", Columns: closeColumns()}}
	indexID := viewindex.ViewIndexID("space", "prices")
	svc.views[viewRef{spaceID: "space", viewID: "prices"}] = newViewRuntime()
	svc.views[viewRef{spaceID: "space", viewID: "prices"}].active = indexID
	if err := svc.maintainView(context.Background(), MaintenanceOptions{Metadata: metadata}, svc.internalAuth(), metadata.view); err != nil {
		t.Fatalf("索引缺失 err=%v", err)
	}
	runtime := svc.views[viewRef{spaceID: "space", viewID: "prices"}]
	if runtime.active != indexID || runtime.building != "" {
		t.Fatalf("应当重新建好索引：active=%q building=%q", runtime.active, runtime.building)
	}
	if schema := svc.schemas[indexID]; len(schema.Columns) != 1 || schema.Columns[0].GetColumnName() != "close" {
		t.Fatalf("新索引应按 View 的列建：%+v", schema)
	}
}

// 跟随器正在原地重建时（期间文件可能暂时不存在），维护流程不另建。
func TestMaintenanceLeavesRebuildToFollower(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	engine := &queryEngine{stats: viewindex.ViewIndexStats{Exists: false}}
	svc.engines["duckdb"] = engine
	view := &pb.View{SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "prices"}
	runtime := newViewRuntime()
	runtime.rebuilding = true
	svc.views[viewRef{spaceID: "space", viewID: "prices"}] = runtime
	if err := svc.maintainView(context.Background(), MaintenanceOptions{Metadata: &maintenanceMetadata{view: view}}, svc.internalAuth(), view); err != nil {
		t.Fatal(err)
	}
	if engine.statCalls != 0 || runtime.building != "" {
		t.Fatalf("不应查看或另建索引：stat=%d building=%q", engine.statCalls, runtime.building)
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
	view := &pb.View{SpaceId: "space", ViewId: "prices"}
	opts := MaintenanceOptions{MaxViewFileBytes: 100}
	svc.observeViewFileSize(view, "prices-a", viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 100}, opts)
	if len(svc.fileOverLimit) != 1 {
		t.Fatalf("超限的 View = %v", svc.fileOverLimit)
	}
	svc.observeViewFileSize(view, "prices-a", viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 99}, opts)
	if len(svc.fileOverLimit) != 0 {
		t.Fatalf("恢复后超限的 View = %v", svc.fileOverLimit)
	}
	svc.observeViewFileSize(view, "prices-a", viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 100}, opts)
	svc.retainFileOverLimit(map[observability.ViewKey]struct{}{})
	if len(svc.fileOverLimit) != 0 {
		t.Fatalf("已删除的 View 不应继续计入：%v", svc.fileOverLimit)
	}
}

// 维护在锁外读到的索引契约早于定时任务刚提交的加列时，不能把写入契约改回旧修订。
func TestAttachActiveViewIgnoresStaleRevision(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	view := &pb.View{SpaceId: "space", ViewId: "prices", DatasetId: "prices", Engine: "bleve", Status: "active", HiddenColumns: []string{"old"}}
	indexID := viewindex.ViewIndexID("space", "prices")
	if err := svc.AttachActiveView(context.Background(), view, builtStats("prices", 2, "new")); err != nil {
		t.Fatal(err)
	}
	view.HiddenColumns = nil
	if err := svc.AttachActiveView(context.Background(), view, builtStats("prices", 1, "old")); err != nil {
		t.Fatal(err)
	}
	if schema := svc.schemas[indexID]; schema.ViewVersion != 2 || schema.SchemaHash != "new" {
		t.Fatalf("写入契约被旧快照改回：%+v", schema)
	}
	if hidden := svc.views[viewRef{spaceID: "space", viewID: "prices"}].queryState().hiddenColumns; len(hidden) != 0 {
		t.Fatalf("隐藏列仍要按这次的 View 刷新：%v", hidden)
	}
}

func TestRestoreActiveViewsAttachesBuiltIndexFile(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	stats := builtStats("prices", 1, "hash")
	stats.Columns = closeColumns()
	svc.engines["bleve"] = &queryEngine{stats: stats}
	metadata := &maintenanceMetadata{view: &pb.View{SpaceId: "space", ViewId: "prices", Engine: "bleve", DatasetId: "prices"}}
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadata}); err != nil {
		t.Fatalf("restore active view: %v", err)
	}
	indexID := viewindex.ViewIndexID("space", "prices")
	svc.mu.RLock()
	engineName := svc.indexEngine[indexID]
	schema := svc.schemas[indexID]
	runtime := svc.views[viewRef{spaceID: "space", viewID: "prices"}]
	svc.mu.RUnlock()
	if engineName != "bleve" || runtime == nil || runtime.queryState().active != indexID {
		t.Fatalf("restored engine=%q runtime=%v", engineName, runtime)
	}
	if schema.ViewVersion != 1 || schema.SchemaHash != "hash" || len(schema.Columns) != 1 {
		t.Fatalf("写入契约应取自索引文件：%+v", schema)
	}
}

// 没有文件、没建完（没记下跟随进度）或属于另一个数据集的索引都不挂接，也不让启动失败，由维护流程重新建。
func TestRestoreActiveViewsSkipsUnbuiltIndexFiles(t *testing.T) {
	half := builtStats("prices", 1, "hash")
	half.Follow = viewindex.FollowState{}
	for name, stats := range map[string]viewindex.ViewIndexStats{
		"missing":       {},
		"half_built":    half,
		"other_dataset": builtStats("fundamentals", 1, "hash"),
	} {
		t.Run(name, func(t *testing.T) {
			svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
			if err != nil {
				t.Fatal(err)
			}
			svc.engines["bleve"] = &queryEngine{stats: stats}
			metadata := &maintenanceMetadata{view: &pb.View{SpaceId: "space", ViewId: "prices", Engine: "bleve", DatasetId: "prices"}}
			if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadata}); err != nil {
				t.Fatalf("restore error = %v", err)
			}
			if indexID, runtime := svc.activeIndex("space", "prices"); runtime != nil && strings.TrimSpace(indexID) != "" {
				t.Fatalf("不应挂接 active=%q", indexID)
			}
		})
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
	view := &pb.View{SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "prices"}
	err = svc.maintainView(context.Background(), MaintenanceOptions{}, svc.internalAuth(), view)
	if !errors.Is(err, statErr) {
		t.Fatalf("maintenance error=%v want=%v", err, statErr)
	}
	if indexID, runtime := svc.activeIndex("space", "prices"); runtime != nil && indexID != "" {
		t.Fatalf("invalid active index attached before stat succeeded: %q", indexID)
	}
}

// 列禁用只是逻辑上的：行里的值还在，只在返回时过滤；重新启用后恢复。
func TestHiddenColumnsAreFilteredFromResults(t *testing.T) {
	key := timeSeriesTestRowKey("")
	engine := &queryEngine{rows: []*pb.RowFieldValues{{Key: key, Fields: []*pb.FieldValue{{FieldId: "close"}, {FieldId: "old"}}}}}
	svc, auth := queryTestService(engine, true)
	view := &pb.View{SpaceId: "space", ViewId: "prices", DatasetId: "market", Engine: "query-test", HiddenColumns: []string{"old"}}
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
	attach := func() {
		t.Helper()
		runtime := svc.views[viewRef{spaceID: "space", viewID: "prices"}]
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		svc.attachActiveViewLocked(view, runtime, "prices-index", svc.schemas["prices-index"], "query-test")
	}
	svc.catalogViews = make(map[viewRef]*pb.View)
	svc.indexView = make(map[string]viewRef)
	attach()
	if fields := query(); len(fields) != 1 || fields[0].GetFieldId() != "close" {
		t.Fatalf("隐藏的列应当被过滤：%v", fields)
	}
	view.HiddenColumns = nil
	attach()
	if fields := query(); len(fields) != 2 {
		t.Fatalf("重新启用后应当恢复：%v", fields)
	}
}

// Bleve 索引建到一半进程退出（目录在、meta.json 不在）：启动不失败、不挂接，维护流程删掉重建。
func TestHalfPreparedBleveIndexIsRebuiltAfterRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "views")
	svc, err := New(root, "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, engine := range svc.engines {
			if closer, ok := engine.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
		}
	}()
	indexID := viewindex.ViewIndexID("space", "records")
	if err := os.MkdirAll(filepath.Join(root, "bleve", indexID), 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := &maintenanceMetadata{view: &pb.View{SpaceId: "space", ViewId: "records", Engine: "bleve", DatasetId: "records", Status: "active",
		Columns: []*pb.ViewColumn{{ColumnName: "title", OriginId: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}}}}
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadata}); err != nil {
		t.Fatalf("半成品不能让启动失败：%v", err)
	}
	if active, runtime := svc.activeIndex("space", "records"); runtime != nil && active != "" {
		t.Fatalf("半成品不应挂接：%q", active)
	}
	if err := svc.maintainView(context.Background(), MaintenanceOptions{Metadata: metadata}, svc.internalAuth(), metadata.view); err != nil {
		t.Fatalf("维护应当重建：%v", err)
	}
	if active, _ := svc.activeIndex("space", "records"); active != indexID {
		t.Fatalf("重建后 active=%q", active)
	}
	if _, err := os.Stat(filepath.Join(root, "bleve", indexID, "meta.json")); err != nil {
		t.Fatalf("重建后应有 meta.json：%v", err)
	}
}

// 一个 View 的索引读不出来：启动不失败，跳过这个 View。
func TestRestoreActiveViewsSkipsUnreadableIndex(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	svc.engines["bleve"] = &queryEngine{statErr: errors.New("坏的索引")}
	metadata := &maintenanceMetadata{view: &pb.View{SpaceId: "space", ViewId: "prices", Engine: "bleve", DatasetId: "prices"}}
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadata}); err != nil {
		t.Fatalf("restore error = %v", err)
	}
}

// 元数据里已删除的 View：维护后不再挂接，GetViewState 返回 VIEW_NOT_FOUND，索引不再受保护（由退役清理回收）。
func TestMaintenanceForgetsDeletedViews(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	svc.engines["bleve"] = &queryEngine{stats: builtStats("prices", 1, "hash")}
	view := &pb.View{SpaceId: "space", ViewId: "prices", Engine: "bleve", DatasetId: "prices", Status: "active"}
	if err := svc.AttachActiveView(context.Background(), view, builtStats("prices", 1, "hash")); err != nil {
		t.Fatal(err)
	}
	auth := &pb.AuthInfo{AppId: "caller", AppKey: serviceauth.Key("view-secret", "caller")}
	state, _ := svc.GetViewState(context.Background(), &pb.GetViewStateReq{AuthInfo: auth, SpaceId: "space", ViewId: "prices"})
	if state.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("删除前 = %v", state.GetRetInfo())
	}
	// 停用的 View 仍在元数据里：不摘掉。
	svc.forgetDeletedViews(context.Background(), &maintenanceMetadata{view: &pb.View{SpaceId: "space", ViewId: "prices", Status: "disabled"}}, svc.internalAuth())
	if active, _ := svc.activeIndex("space", "prices"); active == "" {
		t.Fatal("停用的 View 不应摘掉")
	}
	svc.forgetDeletedViews(context.Background(), &maintenanceMetadata{view: &pb.View{SpaceId: "space", ViewId: "other"}}, svc.internalAuth())
	state, _ = svc.GetViewState(context.Background(), &pb.GetViewStateReq{AuthInfo: auth, SpaceId: "space", ViewId: "prices"})
	if state.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_FOUND {
		t.Fatalf("删除后 = %v，想要 VIEW_NOT_FOUND", state.GetRetInfo())
	}
	protected, err := svc.cleanupProtectedIndexes(context.Background(), &maintenanceMetadata{view: &pb.View{SpaceId: "space", ViewId: "other"}})
	if err != nil {
		t.Fatal(err)
	}
	if cleanupIndexProtected(protected, managedIndexRef{engine: "bleve", indexID: viewindex.ViewIndexID("space", "prices")}) {
		t.Fatal("已删除 View 的索引不应再受保护")
	}
}
