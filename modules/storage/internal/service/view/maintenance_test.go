package view

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestViewBarsComeFromThePolicyForTimeSeriesViews(t *testing.T) {
	if got := viewBars(timeSeriesSpec("s", "d", "1m"), MaintenanceOptions{Bars: 4320}); got != 4320 {
		t.Fatalf("time-series View bars = %d, want the policy 4320", got)
	}
	if got := viewBars(timeSeriesSpec("s", "d", "1h"), MaintenanceOptions{}); got != storagepolicy.DefaultViewBars {
		t.Fatalf("time-series View bars without a policy = %d, want %d", got, storagepolicy.DefaultViewBars)
	}
	if got := viewBars(recordSpec("s", "d"), MaintenanceOptions{Bars: 4320}); got != 0 {
		t.Fatalf("record View bars = %d, want 0 (keeps every row)", got)
	}
}

// 建数据集后不调任何 View 接口：维护流程按数据集建索引，GetViewState 返回非空 index_id。
func TestMaintainerCreatesAndActivatesInitialView(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"))
	if err != nil {
		t.Fatal(err)
	}
	metadata := metadataFor(recordSpec("space", "records", "title"))
	opts, err := svc.normalizeMaintenanceOptions(MaintenanceOptions{Metadata: metadata, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.maintainOnce(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	indexID := viewindex.IndexID("space", "records")
	if len(svc.views) != 1 {
		t.Fatalf("views=%v", svc.views)
	}
	runtime := svc.runtimeOf(datasetRef{spaceID: "space", datasetID: "records"})
	if runtime.phase != viewPhaseActive {
		t.Fatalf("建成后 phase=%d, want viewPhaseActive", runtime.phase)
	}
	state, err := svc.GetViewState(context.Background(), &pb.GetViewStateReq{SpaceId: "space", DatasetId: "records"})
	if err != nil || state.GetIndexId() != indexID || len(state.GetColumns()) != 1 || state.GetColumns()[0].GetDisplayName() != "title" {
		t.Fatalf("GetViewState = %v, %v", state, err)
	}
}

// 数据集启用的列按建列顺序编号；停用的列进隐藏列；加列只追加，已建列不会被去掉。
func TestViewSpecNumbersColumnsAndAppendsOnly(t *testing.T) {
	spec := newViewSpec(&pb.Dataset{SpaceId: "s", DatasetId: "d", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES}, []*pb.DatasetColumn{
		{ColumnName: "b", Status: "active"}, {ColumnName: "old", Status: "disabled"}, {ColumnName: "a"},
	})
	if len(spec.Columns) != 2 || spec.Columns[0].ColumnName != "b" || spec.Columns[1].GetSortOrder() != 1 || !reflect.DeepEqual(spec.HiddenColumns, []string{"old"}) {
		t.Fatalf("声明 = %+v", spec)
	}
	extended, ok := appendViewColumns(spec.Columns, []*viewindex.Column{{ColumnName: "a"}, {ColumnName: "c"}})
	if !ok || !viewindex.IsAppendOnlyViewColumns(spec.Columns, extended) {
		t.Fatalf("追加结果 = %v ok=%v", extended, ok)
	}
	if _, ok := appendViewColumns(spec.Columns, []*viewindex.Column{{ColumnName: "a"}}); ok {
		t.Fatal("声明里少了已建列不算加列，已建列也不会被去掉")
	}
}

func TestInitialBuildFailureDiscardsPartialIndexAndRetries(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	spec := timeSeriesSpec("space", "market", "1m", "close")
	svc := newBuildService(engine)
	reader := &pagedRangeReader{pageSize: 1, failPage: 2, rows: []*pb.Row{
		historyRow("A", "2026-08-18T00:00:00Z", ""), historyRow("B", "2026-08-18T00:00:00Z", ""),
	}}
	opts := MaintenanceOptions{Metadata: metadataFor(spec), PrimaryRange: reader, Bars: 10, BackfillPageSize: 100}
	if err := svc.maintainView(context.Background(), opts, spec); err == nil {
		t.Fatal("回填失败应当返回错误")
	}
	runtime := svc.runtimeOf(spec.ref())
	if runtime.phase != viewPhaseEmpty || runtime.schema.SchemaHash != "" {
		t.Fatalf("失败后 phase=%d schema=%+v", runtime.phase, runtime.schema)
	}
	reader.failPage = 0
	reader.requests = nil
	if err := svc.maintainView(context.Background(), opts, spec); err != nil {
		t.Fatalf("下一轮重建：%v", err)
	}
	if runtime.phase != viewPhaseActive {
		t.Fatalf("重建后 phase=%d", runtime.phase)
	}
}

// 数据集删除后按同一 ID 立即重建（同名任务删了再建）：索引文件记下的是旧数据集的创建时间，新数据集不挂上旧索引，删掉重建。
func TestRecreatedDatasetDoesNotAdoptOldIndex(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	svc := newBuildService(engine)
	old := timeSeriesSpec("space", "market", "1m", "close")
	runtime := testRuntime(svc, old)
	attachTestIndex(runtime, viewindex.ViewIndexSchema{SpaceID: "space", DatasetID: "market", ViewVersion: 3, SchemaHash: "old", DatasetCreatedAt: old.CreatedAt})
	stats := builtStats("market", 3, "old")
	engine.stats = &stats

	recreated := timeSeriesSpec("space", "market", "1m", "close")
	recreated.CreatedAt = 1795507200000
	opts := MaintenanceOptions{Metadata: metadataFor(recreated), PrimaryRange: &pagedRangeReader{}, Bars: 10}
	if err := svc.maintainView(context.Background(), opts, recreated); err != nil {
		t.Fatal(err)
	}
	if engine.removes != 1 {
		t.Fatalf("应当删掉旧文件重建，remove=%d", engine.removes)
	}
	if schema := runtime.queryState().schema; schema.DatasetCreatedAt != recreated.CreatedAt || schema.ViewVersion != initialViewRevision {
		t.Fatalf("重建后的契约 = %+v", schema)
	}
}

// 索引文件不见了：维护流程重新建索引（先停掉跟随器、清掉内存里的活动索引），不需要元数据参与。
func TestMaintenanceRebuildsMissingIndex(t *testing.T) {
	engine := &queryEngine{stats: viewindex.ViewIndexStats{Exists: false}}
	svc := newTestService(map[string]viewindex.Engine{"duckdb": engine})
	spec := timeSeriesSpec("space", "prices", "1m", "close")
	runtime := testRuntime(svc, spec)
	attachTestIndex(runtime, viewindex.ViewIndexSchema{SpaceID: "space", DatasetID: "prices", ViewVersion: 1, SchemaHash: "hash"})
	if err := svc.maintainView(context.Background(), MaintenanceOptions{Metadata: metadataFor(spec), PrimaryRange: &pagedRangeReader{}}, spec); err != nil {
		t.Fatalf("索引缺失 err=%v", err)
	}
	if runtime.phase != viewPhaseActive {
		t.Fatalf("应当重新建好索引：phase=%d", runtime.phase)
	}
	if schema := runtime.queryState().schema; len(schema.Columns) != 1 || schema.Columns[0].GetColumnName() != "close" {
		t.Fatalf("新索引应按数据集的列建：%+v", schema)
	}
}

// 跟随器正在原地重建时（期间文件可能暂时不存在），维护流程不另建。
func TestMaintenanceLeavesRebuildToFollower(t *testing.T) {
	engine := &queryEngine{stats: viewindex.ViewIndexStats{Exists: false}}
	svc := newTestService(map[string]viewindex.Engine{"duckdb": engine})
	spec := timeSeriesSpec("space", "prices", "1m")
	runtime := testRuntime(svc, spec)
	runtime.phase = viewPhaseRebuilding
	runtime.publishReadStateLocked()
	if err := svc.maintainView(context.Background(), MaintenanceOptions{Metadata: metadataFor(spec)}, spec); err != nil {
		t.Fatal(err)
	}
	if engine.statCalls != 0 || runtime.phase != viewPhaseRebuilding {
		t.Fatalf("不应查看或另建索引：stat=%d phase=%d", engine.statCalls, runtime.phase)
	}
}

// 文件超过告警线只告警：不改动索引，也不触发任何重建；摘掉的 View 不再计入。
func TestViewFileOverLimitOnlyAlerts(t *testing.T) {
	svc := newTestService(map[string]viewindex.Engine{"duckdb": &queryEngine{}})
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	svc.SetMetrics(metrics)
	runtime := testRuntime(svc, timeSeriesSpec("space", "prices", "1m"))
	opts := MaintenanceOptions{MaxViewFileBytes: 100}
	svc.observeViewFileSize(runtime, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 100}, opts)
	if !runtime.fileOverLimit.Load() {
		t.Fatal("超限的 View 没有记下")
	}
	svc.observeViewFileSize(runtime, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 99}, opts)
	if runtime.fileOverLimit.Load() {
		t.Fatal("恢复后仍记为超限")
	}
}

// 维护在锁外读到的索引契约早于定时任务刚提交的加列时，不能把写入契约改回旧修订。
func TestAttachActiveViewIgnoresStaleRevision(t *testing.T) {
	svc := newTestService(map[string]viewindex.Engine{"bleve": &queryEngine{}})
	spec := recordSpec("space", "prices")
	spec.HiddenColumns = []string{"old"}
	runtime := testRuntime(svc, spec)
	svc.attachActiveView(runtime, spec, builtStats("prices", 2, "new"))
	fresh := recordSpec("space", "prices")
	svc.attachActiveView(runtime, fresh, builtStats("prices", 1, "old"))
	if schema := runtime.queryState().schema; schema.ViewVersion != 2 || schema.SchemaHash != "new" {
		t.Fatalf("写入契约被旧快照改回：%+v", schema)
	}
	if hidden := runtime.queryState().hiddenColumns; len(hidden) != 0 {
		t.Fatalf("隐藏列仍要按这次的声明刷新：%v", hidden)
	}
}

func TestRestoreActiveViewsAttachesBuiltIndexFile(t *testing.T) {
	stats := builtStats("prices", 1, "hash")
	stats.Columns = closeColumns()
	svc := newTestService(map[string]viewindex.Engine{"bleve": &queryEngine{stats: stats}})
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadataFor(recordSpec("space", "prices"))}); err != nil {
		t.Fatalf("restore active view: %v", err)
	}
	runtime := svc.runtimeOf(datasetRef{spaceID: "space", datasetID: "prices"})
	if runtime == nil || !runtime.queryState().active() || runtime.indexID != viewindex.IndexID("space", "prices") {
		t.Fatalf("restored runtime=%v", runtime)
	}
	if schema := runtime.queryState().schema; schema.ViewVersion != 1 || schema.SchemaHash != "hash" || len(schema.Columns) != 1 {
		t.Fatalf("写入契约应取自索引文件：%+v", schema)
	}
}

// 没有文件、没建完（没记下跟随进度）、属于另一个数据集或删除后同 ID 重建的数据集的索引都不挂接，也不让启动失败，由维护流程重新建。
func TestRestoreActiveViewsSkipsUnbuiltIndexFiles(t *testing.T) {
	half := builtStats("prices", 1, "hash")
	half.Follow = viewindex.FollowState{}
	// 删除后按同样 ID 重建的数据集：数据集 ID 相同，创建时间不同。
	recreated := builtStats("prices", 1, "hash")
	recreated.DatasetCreatedAt = 1790812800000
	for name, stats := range map[string]viewindex.ViewIndexStats{
		"missing":           {},
		"half_built":        half,
		"other_dataset":     builtStats("fundamentals", 1, "hash"),
		"recreated_dataset": recreated,
	} {
		t.Run(name, func(t *testing.T) {
			svc := newTestService(map[string]viewindex.Engine{"bleve": &queryEngine{stats: stats}})
			if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadataFor(recordSpec("space", "prices"))}); err != nil {
				t.Fatalf("restore error = %v", err)
			}
			if indexID, runtime := svc.activeIndex("space", "prices"); runtime != nil && strings.TrimSpace(indexID) != "" {
				t.Fatalf("不应挂接 active=%q", indexID)
			}
		})
	}
}

func TestMaintenanceStatsActiveIndexBeforeAttachingIt(t *testing.T) {
	statErr := errors.New("legacy duckdb schema requires cleanup and rebuild")
	svc := newTestService(map[string]viewindex.Engine{"duckdb": &queryEngine{statErr: statErr}})
	err := svc.maintainView(context.Background(), MaintenanceOptions{}, timeSeriesSpec("space", "prices", "1m"))
	if !errors.Is(err, statErr) {
		t.Fatalf("maintenance error=%v want=%v", err, statErr)
	}
	if indexID, runtime := svc.activeIndex("space", "prices"); runtime != nil && indexID != "" {
		t.Fatalf("invalid active index attached before stat succeeded: %q", indexID)
	}
}

// 列停用只是逻辑上的：行里的值还在，只在返回时过滤；重新启用后恢复。
func TestHiddenColumnsAreFilteredFromResults(t *testing.T) {
	key := timeSeriesTestRowKey("")
	engine := &queryEngine{rows: []*pb.Row{{Key: key, Fields: []*pb.FieldValue{{FieldId: "close"}, {FieldId: "old"}}}}}
	svc := queryTestService(engine, true)
	runtime := svc.runtimeOf(marketRef)
	query := func() []*pb.FieldValue {
		t.Helper()
		rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
			SpaceId: "space", DatasetId: "market",
			Selectors: []*pb.TimeSeriesSelector{{SubjectId: "BTC-USDT", Freq: "1m"}},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 1 {
			t.Fatalf("rsp=%v err=%v", rsp, err)
		}
		return rsp.GetRows()[0].GetFields()
	}
	disabled := timeSeriesSpec("space", "market", "1m", "close")
	disabled.HiddenColumns = []string{"old"}
	runtime.setSpec(disabled)
	if fields := query(); len(fields) != 1 || fields[0].GetFieldId() != "close" {
		t.Fatalf("停用的列应当被过滤：%v", fields)
	}
	runtime.setSpec(timeSeriesSpec("space", "market", "1m", "close", "old"))
	if fields := query(); len(fields) != 2 {
		t.Fatalf("重新启用后应当恢复：%v", fields)
	}
}

// Bleve 索引建到一半进程退出（目录在、meta.json 不在）：启动不失败、不挂接，维护流程删掉重建。
func TestHalfPreparedBleveIndexIsRebuiltAfterRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "views")
	svc, err := New(root)
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
	indexID := viewindex.IndexID("space", "records")
	if err := os.MkdirAll(filepath.Join(root, "bleve", indexID), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := recordSpec("space", "records", "title")
	metadata := metadataFor(spec)
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadata}); err != nil {
		t.Fatalf("半成品不能让启动失败：%v", err)
	}
	if active, runtime := svc.activeIndex("space", "records"); runtime != nil && active != "" {
		t.Fatalf("半成品不应挂接：%q", active)
	}
	if err := svc.maintainView(context.Background(), MaintenanceOptions{Metadata: metadata}, spec); err != nil {
		t.Fatalf("维护应当重建：%v", err)
	}
	if active, _ := svc.activeIndex("space", "records"); active != indexID {
		t.Fatalf("重建后 active=%q", active)
	}
	if _, err := os.Stat(filepath.Join(root, "bleve", indexID, "meta.json")); err != nil {
		t.Fatalf("重建后应有 meta.json：%v", err)
	}
}

// 一个数据集的索引读不出来：启动不失败，跳过它。
func TestRestoreActiveViewsSkipsUnreadableIndex(t *testing.T) {
	svc := newTestService(map[string]viewindex.Engine{"bleve": &queryEngine{statErr: errors.New("坏的索引")}})
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadataFor(recordSpec("space", "prices"))}); err != nil {
		t.Fatalf("restore error = %v", err)
	}
}

// 数据集停用：View 摘掉、不可查、索引文件保留；重新启用后挂回、不重建。数据集删除：View 摘掉、索引文件当场删除，
// GetViewState 返回 VIEW_NOT_FOUND，运行时（含写锁与修订号）不残留。
func TestMaintenanceFollowsDatasetStatus(t *testing.T) {
	engine := &removeRecordingEngine{queryEngine: &queryEngine{stats: builtStats("prices", 1, "hash")}}
	svc := newTestService(map[string]viewindex.Engine{"bleve": engine})
	spec := recordSpec("space", "prices")
	opts := MaintenanceOptions{Metadata: metadataFor(spec)}
	if err := svc.maintainOnce(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	ref := spec.ref()
	first := svc.runtimeOf(ref)
	if first == nil || !first.queryState().active() {
		t.Fatal("启用的数据集应当挂接")
	}
	firstRev := first.revision.Load()
	if firstRev == 0 || firstRev&1 != 0 {
		t.Fatalf("新建运行时应以非零偶数修订号初始化，实际=%d", firstRev)
	}
	first.revision.Store(6)

	disabled := recordSpec("space", "prices")
	disabled.Status = "disabled"
	if err := svc.maintainOnce(context.Background(), MaintenanceOptions{Metadata: metadataFor(disabled)}); err != nil {
		t.Fatal(err)
	}
	if svc.runtimeOf(ref) != nil || len(engine.removed) != 0 {
		t.Fatalf("停用的数据集应当摘掉、不删文件：removed=%v", engine.removed)
	}
	query, _ := svc.SearchRecordRows(context.Background(), &pb.SearchRecordRowsReq{SpaceId: "space", DatasetId: "prices", TextQuery: "x"})
	if query.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_READY {
		t.Fatalf("停用后查询 = %v，想要 VIEW_NOT_READY", query.GetRetInfo())
	}

	if err := svc.maintainOnce(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	again := svc.runtimeOf(ref)
	if again == nil || again == first || !again.queryState().active() || engine.prepares != 0 {
		t.Fatalf("重新启用后应挂回、不重建：prepares=%d", engine.prepares)
	}
	if againRev := again.revision.Load(); againRev == 0 || againRev&1 != 0 || againRev == 6 || againRev <= firstRev {
		t.Fatalf("新的运行时应有新的非零偶数单调修订号，first=%d again=%d", firstRev, againRev)
	}

	if err := svc.maintainOnce(context.Background(), MaintenanceOptions{Metadata: &testMetadata{}}); err != nil {
		t.Fatal(err)
	}
	state, _ := svc.GetViewState(context.Background(), &pb.GetViewStateReq{SpaceId: "space", DatasetId: "prices"})
	if state.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_FOUND {
		t.Fatalf("删除后 = %v，想要 VIEW_NOT_FOUND", state.GetRetInfo())
	}
	if want := []string{viewindex.IndexID("space", "prices")}; !reflect.DeepEqual(engine.removed, want) {
		t.Fatalf("删除的索引 = %v，期望 %v", engine.removed, want)
	}
	if len(svc.views) != 0 {
		t.Fatalf("摘掉后 views 里不应残留：%v", svc.views)
	}
	// 再跑一轮不重复删。
	if err := svc.maintainOnce(context.Background(), MaintenanceOptions{Metadata: &testMetadata{}}); err != nil {
		t.Fatal(err)
	}
	if len(engine.removed) != 1 {
		t.Fatalf("重复删除：%v", engine.removed)
	}
}

// removeRecordingEngine 记下被删除与准备的索引。
type removeRecordingEngine struct {
	*queryEngine
	removed  []string
	prepares int
}

func (e *removeRecordingEngine) Remove(_ context.Context, id string) error {
	e.removed = append(e.removed, id)
	return nil
}

func (e *removeRecordingEngine) Prepare(context.Context, string, viewindex.ViewIndexSchema) error {
	e.prepares++
	return nil
}

// pageMetadata 按页返回固定的数据集列表响应，用来构造分页信息不完整的元数据。
type pageMetadata struct{ pages []*pb.ListDatasetsRsp }

func (m *pageMetadata) ListDatasets(_ context.Context, req *pb.ListDatasetsReq, _ ...client.Option) (*pb.ListDatasetsRsp, error) {
	index := int(req.GetPage().GetPage()) - 1
	if index < 0 || index >= len(m.pages) {
		return &pb.ListDatasetsRsp{RetInfo: successRetInfo(), PageResult: &pb.PageResult{}}, nil
	}
	return m.pages[index], nil
}

func (m *pageMetadata) ListDatasetColumns(context.Context, *pb.ListDatasetColumnsReq, ...client.Option) (*pb.ListDatasetColumnsRsp, error) {
	return &pb.ListDatasetColumnsRsp{RetInfo: successRetInfo(), PageResult: &pb.PageResult{}}, nil
}

// 列出数据集时没有分页信息，或说还有下一页、这一页却是空的：不能断定已经列全，这一轮不摘任何 View。
func TestMaintenanceKeepsViewsOnIncompletePages(t *testing.T) {
	other := &pb.Dataset{SpaceId: "space", DatasetId: "other", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active", CreatedAt: testCreatedAt}
	cases := map[string][]*pb.ListDatasetsRsp{
		"没有分页信息":    {{RetInfo: successRetInfo(), Datasets: []*pb.Dataset{other}}},
		"有下一页但这页为空": {{RetInfo: successRetInfo(), PageResult: &pb.PageResult{Page: 1, Size: 100, HasMore: true}}},
		"第二页为空却说还有": {
			{RetInfo: successRetInfo(), Datasets: []*pb.Dataset{other}, PageResult: &pb.PageResult{Page: 1, Size: 100, HasMore: true}},
			{RetInfo: successRetInfo(), PageResult: &pb.PageResult{Page: 2, Size: 100, HasMore: true}},
		},
	}
	for name, pages := range cases {
		t.Run(name, func(t *testing.T) {
			svc := newTestService(map[string]viewindex.Engine{"bleve": &queryEngine{stats: builtStats("prices", 1, "hash")}})
			spec := recordSpec("space", "prices")
			svc.attachActiveView(testRuntime(svc, spec), spec, builtStats("prices", 1, "hash"))
			if err := svc.maintainOnce(context.Background(), MaintenanceOptions{Metadata: &pageMetadata{pages: pages}}); err == nil {
				t.Fatal("分页信息不完整时这一轮应当报错")
			}
			if active, _ := svc.activeIndex("space", "prices"); active == "" {
				t.Fatal("分页信息不完整时不应摘掉 View")
			}
		})
	}
}
