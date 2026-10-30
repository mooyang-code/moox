package view

import (
	"context"
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
	queryRows  []*pb.RowFieldValues
}

func (*primaryHistoryBackfillEngine) Engine() string { return "duckdb" }
func (*primaryHistoryBackfillEngine) Prepare(context.Context, string, viewindex.ViewIndexSchema) error {
	return nil
}
func (e *primaryHistoryBackfillEngine) Write(_ context.Context, _ string, batch viewindex.ViewIndexWriteBatch) error {
	e.writeRows += len(batch.RowWrites)
	return nil
}
func (e *primaryHistoryBackfillEngine) Query(context.Context, string, viewindex.QuerySpec) ([]*pb.RowFieldValues, int64, error) {
	e.queryCalls++
	return e.queryRows, int64(len(e.queryRows)), nil
}
func (*primaryHistoryBackfillEngine) Stat(context.Context, string) (viewindex.ViewIndexStats, error) {
	return viewindex.ViewIndexStats{Exists: true}, nil
}
func (*primaryHistoryBackfillEngine) Remove(context.Context, string) error { return nil }

// pagedRangeReader 模拟 Primary 的「每序列最近 N 根」分页读取：把预置的行按页返回，并记录每次请求。
type pagedRangeReader struct {
	rows     []*pb.TimeSeriesRow
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

type primaryHistoryFieldReader struct{}

func (*primaryHistoryFieldReader) ReadRows(context.Context, *pb.PrimaryReadRowsReq, ...client.Option) (*pb.PrimaryReadRowsRsp, error) {
	return &pb.PrimaryReadRowsRsp{RetInfo: successRetInfo()}, nil
}

func closeColumns() []*pb.ViewColumn {
	return []*pb.ViewColumn{{ColumnName: "close", OriginId: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
}

func historyRow(subject, at, tag string) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{
		Key:    &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "market", SubjectId: subject, Freq: "1m", DataTime: at, SeriesTag: tag},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}},
	}
}

func newBackfillService(engine *primaryHistoryBackfillEngine, view *pb.View, activeID string) *Service {
	next := view.GetViewId() + "-b"
	runtime := &viewRuntime{next: next}
	indexEngine := map[string]string{next: "duckdb"}
	schemas := map[string]viewindex.ViewIndexSchema{next: {SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), PrimaryDatasetID: view.GetDatasetId(), Engine: "duckdb", ViewVersion: 1, SchemaHash: "schema", Columns: closeColumns()}}
	if activeID != "" {
		runtime.active = activeID
		indexEngine[activeID] = "duckdb"
		schemas[activeID] = schemas[next]
	}
	return &Service{
		engines:        map[string]viewindex.Engine{"duckdb": engine},
		indexEngine:    indexEngine,
		schemas:        schemas,
		views:          map[viewRef]*viewRuntime{{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}: runtime},
		catalogViews:   map[viewRef]*pb.View{{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}: view},
		metadataClient: &maintenanceMetadata{view: view},
	}
}

func TestPeriodBackfillReadsLatestBarsPerSeriesFromPrimaryPageByPage(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	view := &pb.View{SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "market", Freq: "1m"}
	svc := newBackfillService(engine, view, "prices-a")
	reader := &pagedRangeReader{pageSize: 2, rows: []*pb.TimeSeriesRow{
		historyRow("BTC-USDT", "2026-08-18T00:01:00Z", "venue:binance"), historyRow("BTC-USDT", "2026-08-18T00:00:00Z", "venue:binance"),
		historyRow("BTC-USDT", "2026-08-18T00:01:00Z", "venue:okx"), historyRow("BTC-USDT", "2026-08-18T00:00:00Z", "venue:okx"),
		historyRow("ETH-USDT", "2026-08-18T00:01:00Z", ""),
	}}
	written, err := svc.backfillViewWithReader(context.Background(), "space", "prices", 100, &primaryHistoryFieldReader{}, reader, 2)
	if err != nil {
		t.Fatalf("period backfill: %v", err)
	}
	if written != 5 || engine.writeRows != 5 {
		t.Fatalf("written=%d engine_rows=%d, want every row of every page", written, engine.writeRows)
	}
	if engine.queryCalls != 0 {
		t.Fatalf("active index was queried %d times; period rebuild must read Primary directly", engine.queryCalls)
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
		// 每页行数取调用方给的 batchSize（线上即 backfill_page_size），不能被常量覆盖。
		if req.GetPage().GetSize() != 100 {
			t.Fatalf("第 %d 次请求的页大小 = %d，应为调用方给的 100", i, req.GetPage().GetSize())
		}
	}
}

func TestPeriodBackfillFailsWholeBuildWhenAPageFails(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	view := &pb.View{SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "market", Freq: "1m"}
	svc := newBackfillService(engine, view, "")
	reader := &pagedRangeReader{pageSize: 1, failPage: 2, rows: []*pb.TimeSeriesRow{
		historyRow("A", "2026-08-18T00:00:00Z", ""), historyRow("B", "2026-08-18T00:00:00Z", ""), historyRow("C", "2026-08-18T00:00:00Z", ""),
	}}
	if _, err := svc.backfillViewWithReader(context.Background(), "space", "prices", 100, &primaryHistoryFieldReader{}, reader, 1); err == nil {
		t.Fatal("a failed page must fail the rebuild instead of silently skipping subjects")
	}
}

func TestPeriodBackfillRequiresPrimaryReaderForNewTimeSeriesView(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	view := &pb.View{SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "market", Freq: "1m"}
	svc := newBackfillService(engine, view, "")
	if _, err := svc.backfillViewWithReader(context.Background(), "space", "prices", 100, nil, nil, 2); err == nil {
		t.Fatal("new time-series View without Primary reader was accepted")
	}
}

func TestPeriodBackfillActivatesWithAvailableHistoryBelowTarget(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	view := &pb.View{SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "market", Freq: "1m"}
	svc := newBackfillService(engine, view, "")
	reader := &pagedRangeReader{rows: []*pb.TimeSeriesRow{historyRow("BTC-USDT", "2026-08-18T00:00:00Z", "venue:binance")}}
	written, err := svc.backfillViewWithReader(context.Background(), "space", "prices", 100, &primaryHistoryFieldReader{}, reader, 1000)
	if err != nil {
		t.Fatalf("partial period backfill: %v", err)
	}
	if written != 1 || engine.writeRows != 1 {
		t.Fatalf("written=%d engine_rows=%d, want one available row", written, engine.writeRows)
	}
}

func TestPrimaryOnlyViewMayStartEmptyBeforeFirstOutput(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	view := &pb.View{SpaceId: "space", ViewId: "factor-result-view", Engine: "duckdb", DatasetId: "market", Freq: "1m", Attributes: map[string]string{"rebuild_source": "primary"}}
	svc := newBackfillService(engine, view, "")
	written, err := svc.backfillViewWithReader(context.Background(), "space", view.ViewId, 100, &primaryHistoryFieldReader{}, nil, 2)
	if err != nil {
		t.Fatalf("empty factor result backfill: %v", err)
	}
	runtime := svc.views[viewRef{spaceID: "space", viewID: view.ViewId}]
	if written != 0 || runtime.status != "ready" {
		t.Fatalf("written=%d status=%q, want empty ready build", written, runtime.status)
	}
}

func TestPrimaryOnlyViewBackfillsExistingPrimaryOutput(t *testing.T) {
	activeKey := timeSeriesTestRowKey("venue:binance")
	activeKey.DatasetId = "market"
	activeKey.GetTimeSeries().DataTime = "2026-08-18T00:01:00Z"
	engine := &primaryHistoryBackfillEngine{queryRows: []*pb.RowFieldValues{{Key: activeKey}}}
	view := &pb.View{SpaceId: "space", ViewId: "factor-result-view", Engine: "duckdb", DatasetId: "market", Freq: "1m", Attributes: map[string]string{"rebuild_source": "primary"}}
	svc := newBackfillService(engine, view, "factor-result-view-a")
	reader := &pagedRangeReader{rows: []*pb.TimeSeriesRow{
		historyRow("BTC-USDT", "2026-08-18T00:01:00Z", "venue:binance"), historyRow("BTC-USDT", "2026-08-18T00:00:00Z", "venue:binance"),
	}}
	written, err := svc.backfillViewWithReader(context.Background(), "space", view.ViewId, 100, &primaryHistoryFieldReader{}, reader, 2)
	if err != nil {
		t.Fatalf("factor result history backfill: %v", err)
	}
	if written != 2 || engine.writeRows != 2 || engine.queryCalls != 0 {
		t.Fatalf("written=%d engine_rows=%d query_calls=%d, want authoritative Primary rebuild", written, engine.writeRows, engine.queryCalls)
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

func TestBackfillSortsUseCompleteTimeSeriesIdentity(t *testing.T) {
	got := backfillSorts("duckdb")
	want := []*pb.SortSpec{
		{FieldName: "data_time"},
		{FieldName: "subject_id"},
		{FieldName: "freq"},
		{FieldName: "series_tag"},
		{FieldName: "record_id"},
		{FieldName: "version"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backfill sorts=%v want=%v", got, want)
	}
}

func TestProjectBackfillFieldsUsesNextSchemaShape(t *testing.T) {
	active := viewindex.ViewIndexSchema{Columns: []*pb.ViewColumn{
		{ColumnName: "close", OriginId: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "old", OriginId: "old", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}}
	next := viewindex.ViewIndexSchema{Columns: []*pb.ViewColumn{
		{ColumnName: "close", OriginId: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "old", OriginId: "new", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}}
	fields := []*pb.FieldValue{
		{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}},
		{FieldId: "old", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}},
		{FieldId: "removed", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 3}}},
	}
	got := projectBackfillFields(fields, active, next)
	if len(got) != 1 || got[0].GetFieldId() != "close" {
		t.Fatalf("projected fields=%v, want only unchanged close", got)
	}
}
