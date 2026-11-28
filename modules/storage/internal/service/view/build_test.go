package view

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-go/client"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type primaryHistoryBackfillEngine struct {
	queryCalls int
	writeRows  int
	removes    int
	queryRows  []*pb.Row
	// stats 是 Stat 返回的索引状态；为空时只说索引存在（不属于任何数据集）。
	stats *viewindex.ViewIndexStats
}

func (*primaryHistoryBackfillEngine) Engine() string { return "duckdb" }
func (*primaryHistoryBackfillEngine) Prepare(context.Context, string, viewindex.ViewIndexSchema) error {
	return nil
}
func (e *primaryHistoryBackfillEngine) Write(_ context.Context, _ string, batch viewindex.ViewIndexWriteBatch) error {
	e.writeRows += len(batch.RowWrites)
	return nil
}
func (e *primaryHistoryBackfillEngine) Query(context.Context, string, viewindex.QuerySpec) ([]*pb.Row, int64, error) {
	e.queryCalls++
	return e.queryRows, int64(len(e.queryRows)), nil
}
func (e *primaryHistoryBackfillEngine) Stat(context.Context, string) (viewindex.ViewIndexStats, error) {
	if e.stats != nil {
		return *e.stats, nil
	}
	return viewindex.ViewIndexStats{Exists: true}, nil
}
func (e *primaryHistoryBackfillEngine) Remove(context.Context, string) error {
	e.removes++
	return nil
}

// pagedRangeReader 模拟 Primary 的「每序列最近 N 根」分页读取：把预置的行按页返回，并记录每次请求。
type pagedRangeReader struct {
	rows     []*pb.Row
	pageSize int
	requests []*pb.ReadTimeSeriesRowsReq
	failPage int // 大于 0 时第 failPage 页返回错误
}

func (r *pagedRangeReader) ReadTimeSeriesRows(_ context.Context, req *pb.ReadTimeSeriesRowsReq, _ ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	r.requests = append(r.requests, req)
	if r.failPage > 0 && len(r.requests) >= r.failPage {
		return nil, context.DeadlineExceeded
	}
	size := r.pageSize
	if size == 0 {
		size = len(r.rows)
	}
	offset := 0
	if cursor := req.GetPage().GetCursor(); cursor != "" {
		offset, _ = strconv.Atoi(cursor)
	}
	end := min(offset+size, len(r.rows))
	rsp := &pb.ReadTimeSeriesRowsRsp{RetInfo: successRetInfo(), Rows: r.rows[offset:end], PageResult: &pb.PageResult{}}
	if end < len(r.rows) {
		rsp.PageResult.HasMore, rsp.PageResult.NextCursor = true, strconv.Itoa(end)
	}
	return rsp, nil
}

func closeColumns() []*viewindex.Column {
	return []*viewindex.Column{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
}

func historyRow(subject, at, tag string) *pb.Row {
	return &pb.Row{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "market", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, Frequency: "1m", DataTime: at, SeriesTag: tag}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}},
	}
}

func newBuildService(engine *primaryHistoryBackfillEngine) *Service {
	return newTestService(map[string]viewindex.Engine{"duckdb": engine})
}

func backfillSchema() viewindex.ViewIndexSchema {
	return viewindex.ViewIndexSchema{SpaceID: "space", DatasetID: "market", Engine: "duckdb", ViewVersion: 1, SchemaHash: "schema", Columns: closeColumns()}
}

func newBackfillService(engine *primaryHistoryBackfillEngine) (*Service, *viewSpec, *viewRuntime) {
	spec := timeSeriesSpec("space", "market", "1m", "close")
	svc := newBuildService(engine)
	runtime := testRuntime(svc, spec)
	runtime.phase = viewPhaseBuilding
	runtime.schema = backfillSchema()
	runtime.publishReadStateLocked()
	return svc, spec, runtime
}

func TestBackfillReadsLatestBarsPerSeriesFromPrimaryPageByPage(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	svc, spec, runtime := newBackfillService(engine)
	reader := &pagedRangeReader{pageSize: 2, rows: []*pb.Row{
		historyRow("BTC-USDT", "2026-08-18T00:01:00Z", "venue:binance"), historyRow("BTC-USDT", "2026-08-18T00:00:00Z", "venue:binance"),
		historyRow("BTC-USDT", "2026-08-18T00:01:00Z", "venue:okx"), historyRow("BTC-USDT", "2026-08-18T00:00:00Z", "venue:okx"),
		historyRow("ETH-USDT", "2026-08-18T00:01:00Z", ""),
	}}
	if err := svc.backfillInitialIndex(context.Background(), runtime, spec, backfillSchema(), MaintenanceOptions{PrimaryRange: reader, BackfillPageSize: 100}, 2); err != nil {
		t.Fatalf("回填：%v", err)
	}
	if engine.writeRows != 5 {
		t.Fatalf("engine_rows=%d，应当写入每一页的每一行", engine.writeRows)
	}
	if len(reader.requests) != 3 {
		t.Fatalf("requests=%d, want 3 pages", len(reader.requests))
	}
	for i, req := range reader.requests {
		if len(req.GetSelectors()) != 0 || req.GetLatestPerSeries() != 2 || req.GetTimeRange() != nil || !reflect.DeepEqual(req.GetColumnNames(), []string{"close"}) {
			t.Fatalf("request %d = %v, want unselected latest-2 read of the View columns", i, req)
		}
		if i > 0 && req.GetPage().GetCursor() == "" {
			t.Fatalf("request %d did not continue from the previous cursor", i)
		}
		// 每页行数取配置的 backfill_page_size，不能被常量覆盖。
		if req.GetPage().GetSize() != 100 {
			t.Fatalf("第 %d 次请求的页大小 = %d，应为配置的 100", i, req.GetPage().GetSize())
		}
	}
}

func TestBackfillFailsWholeBuildWhenAPageFails(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	svc, spec, runtime := newBackfillService(engine)
	reader := &pagedRangeReader{pageSize: 1, failPage: 2, rows: []*pb.Row{
		historyRow("A", "2026-08-18T00:00:00Z", ""), historyRow("B", "2026-08-18T00:00:00Z", ""), historyRow("C", "2026-08-18T00:00:00Z", ""),
	}}
	if err := svc.backfillInitialIndex(context.Background(), runtime, spec, backfillSchema(), MaintenanceOptions{PrimaryRange: reader}, 1); err == nil {
		t.Fatal("一页失败应当让整次建索引失败，不能静默跳过标的")
	}
}

func TestBackfillRequiresPrimaryReaderForTimeSeriesView(t *testing.T) {
	svc, spec, runtime := newBackfillService(&primaryHistoryBackfillEngine{})
	if err := svc.backfillInitialIndex(context.Background(), runtime, spec, backfillSchema(), MaintenanceOptions{}, 2); err == nil {
		t.Fatal("时序 View 没有 Primary 读取客户端时应当报错")
	}
}

func TestBackfillStopsWhenBuildIsCancelled(t *testing.T) {
	svc, spec, runtime := newBackfillService(&primaryHistoryBackfillEngine{})
	runtime.phase = viewPhaseEmpty
	runtime.publishReadStateLocked()
	reader := &pagedRangeReader{rows: []*pb.Row{historyRow("A", "2026-08-18T00:00:00Z", "")}}
	if err := svc.backfillInitialIndex(context.Background(), runtime, spec, backfillSchema(), MaintenanceOptions{PrimaryRange: reader}, 2); !errors.Is(err, errViewBuildCancelled) {
		t.Fatalf("err=%v", err)
	}
}

func TestBackfillAcceptsHistoryBelowTarget(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	svc, spec, runtime := newBackfillService(engine)
	reader := &pagedRangeReader{rows: []*pb.Row{historyRow("BTC-USDT", "2026-08-18T00:00:00Z", "venue:binance")}}
	if err := svc.backfillInitialIndex(context.Background(), runtime, spec, backfillSchema(), MaintenanceOptions{PrimaryRange: reader}, 1000); err != nil {
		t.Fatalf("历史不足时也应建成：%v", err)
	}
	if engine.writeRows != 1 {
		t.Fatalf("engine_rows=%d", engine.writeRows)
	}
}

// 派生输出（例如因子结果）在第一个周期到来之前 Primary 里没有行：View 从空开始。
func TestBackfillMayStartEmpty(t *testing.T) {
	svc, spec, runtime := newBackfillService(&primaryHistoryBackfillEngine{})
	if err := svc.backfillInitialIndex(context.Background(), runtime, spec, backfillSchema(), MaintenanceOptions{PrimaryRange: &pagedRangeReader{}}, 2); err != nil {
		t.Fatal(err)
	}
}

// 记录型 View 没有可读的 Primary 历史，从空开始，不需要读取客户端。
func TestRecordViewStartsEmpty(t *testing.T) {
	svc, spec, runtime := newBackfillService(&primaryHistoryBackfillEngine{})
	spec.DataKind, spec.Freq = pb.DataKind_DATA_KIND_RECORD, ""
	if err := svc.backfillInitialIndex(context.Background(), runtime, spec, backfillSchema(), MaintenanceOptions{}, 0); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillRequestLimiterSerializesStorageRequests(t *testing.T) {
	limiter := newBackfillRequestLimiter(10 * time.Millisecond)
	ctx := context.Background()
	if err := limiter.wait(ctx); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := limiter.wait(ctx); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 8*time.Millisecond {
		t.Fatalf("second request started after %s, want at least 8ms", elapsed)
	}
}
