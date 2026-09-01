package view

import (
	"context"
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type primaryHistoryBackfillEngine struct {
	queryCalls int
	writeRows  int
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
	return nil, 0, nil
}
func (*primaryHistoryBackfillEngine) Stat(context.Context, string) (viewindex.ViewIndexStats, error) {
	return viewindex.ViewIndexStats{Exists: true}, nil
}
func (*primaryHistoryBackfillEngine) Remove(context.Context, string) error { return nil }

type primaryHistoryRangeReader struct {
	rows      []*pb.TimeSeriesRow
	selectors []*pb.TimeSeriesSelector
}

type primaryHistoryFieldReader struct{}

func (*primaryHistoryFieldReader) ReadFields(context.Context, *pb.PrimaryReadFieldsReq, ...client.Option) (*pb.PrimaryReadFieldsRsp, error) {
	return &pb.PrimaryReadFieldsRsp{RetInfo: successRetInfo()}, nil
}

func (r *primaryHistoryRangeReader) ReadTimeSeriesRows(_ context.Context, req *pb.ReadTimeSeriesRowsReq, _ ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	r.selectors = req.GetSelectors()
	return &pb.ReadTimeSeriesRowsRsp{
		RetInfo:    successRetInfo(),
		Rows:       r.rows,
		PageResult: &pb.PageResult{HasMore: false},
	}, nil
}

func TestPeriodBackfillUsesPrimaryInsteadOfCopyingActiveAndReportsRowsWritten(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	view := &pb.View{
		SpaceId:          "space",
		ViewId:           "prices",
		Engine:           "duckdb",
		PrimaryDatasetId: "market",
		FilterJson:       `{"freq":"1m"}`,
	}
	metadata := &maintenanceMetadata{view: view}
	svc := &Service{
		engines:        map[string]viewindex.Engine{"duckdb": engine},
		indexEngine:    map[string]string{"prices-a": "duckdb", "prices-b": "duckdb"},
		schemas:        map[string]viewindex.ViewIndexSchema{"prices-b": {SpaceID: "space", ViewID: "prices", PrimaryDatasetID: "market", Engine: "duckdb", ViewVersion: 1, SchemaHash: "schema"}},
		views:          map[viewRef]*viewRuntime{{spaceID: "space", viewID: "prices"}: {active: "prices-a", next: "prices-b"}},
		catalogViews:   map[viewRef]*pb.View{{spaceID: "space", viewID: "prices"}: view},
		metadataClient: metadata,
	}
	reader := &primaryHistoryRangeReader{rows: []*pb.TimeSeriesRow{
		{Key: &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-18T00:01:00Z", SeriesTag: "venue:binance"}},
		{Key: &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-18T00:00:00Z", SeriesTag: "venue:binance"}},
		{Key: &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-18T00:01:00Z", SeriesTag: "venue:okx"}},
		{Key: &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-18T00:00:00Z", SeriesTag: "venue:okx"}},
	}}

	written, err := svc.backfillViewWithReader(context.Background(), "space", "prices", 100, &primaryHistoryFieldReader{}, reader, 0, 2, defaultMaxHistoryScanRows)
	if err != nil {
		t.Fatalf("period backfill: %v", err)
	}
	if written != 4 || engine.writeRows != 4 {
		t.Fatalf("written=%d engine_rows=%d, want two rows per series tag", written, engine.writeRows)
	}
	if engine.queryCalls != 0 {
		t.Fatalf("active index was queried %d times; period rebuild must read Primary directly", engine.queryCalls)
	}
	if len(reader.selectors) != 0 {
		t.Fatalf("period rebuild trusted subject bindings: selectors=%v", reader.selectors)
	}
}

func TestPeriodBackfillRequiresPrimaryReaderForNewTimeSeriesView(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	view := &pb.View{SpaceId: "space", ViewId: "prices", Engine: "duckdb", PrimaryDatasetId: "market", FilterJson: `{"freq":"1m"}`}
	svc := &Service{
		engines:      map[string]viewindex.Engine{"duckdb": engine},
		indexEngine:  map[string]string{"prices-b": "duckdb"},
		schemas:      map[string]viewindex.ViewIndexSchema{"prices-b": {SpaceID: "space", ViewID: "prices", PrimaryDatasetID: "market", Engine: "duckdb", ViewVersion: 1, SchemaHash: "schema"}},
		views:        map[viewRef]*viewRuntime{{spaceID: "space", viewID: "prices"}: {next: "prices-b"}},
		catalogViews: map[viewRef]*pb.View{{spaceID: "space", viewID: "prices"}: view},
	}
	if _, err := svc.backfillViewWithReader(context.Background(), "space", "prices", 100, nil, nil, 0, 2, defaultMaxHistoryScanRows); err == nil {
		t.Fatal("new time-series View without Primary reader was accepted")
	}
}

func TestPeriodBackfillActivatesWithAvailableHistoryBelowTarget(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	view := &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "duckdb", PrimaryDatasetId: "market",
		FilterJson: `{"freq":"1m"}`,
	}
	svc := &Service{
		engines:      map[string]viewindex.Engine{"duckdb": engine},
		indexEngine:  map[string]string{"prices-b": "duckdb"},
		schemas:      map[string]viewindex.ViewIndexSchema{"prices-b": {SpaceID: "space", ViewID: "prices", PrimaryDatasetID: "market", Engine: "duckdb", ViewVersion: 1, SchemaHash: "schema"}},
		views:        map[viewRef]*viewRuntime{{spaceID: "space", viewID: "prices"}: {next: "prices-b"}},
		catalogViews: map[viewRef]*pb.View{{spaceID: "space", viewID: "prices"}: view},
	}
	reader := &primaryHistoryRangeReader{rows: []*pb.TimeSeriesRow{{Key: &pb.TimeSeriesKey{
		SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-18T00:00:00Z", SeriesTag: "venue:binance",
	}}}}
	written, err := svc.backfillViewWithReader(context.Background(), "space", "prices", 100, &primaryHistoryFieldReader{}, reader, 0, 1000, defaultMaxHistoryScanRows)
	if err != nil {
		t.Fatalf("partial period backfill: %v", err)
	}
	if written != 1 || engine.writeRows != 1 {
		t.Fatalf("written=%d engine_rows=%d, want one available row", written, engine.writeRows)
	}
}

func TestFactorResultViewMayStartEmptyBeforeFirstFactorPeriod(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	view := &pb.View{
		SpaceId:          "space",
		ViewId:           "factor-result-view",
		Engine:           "duckdb",
		PrimaryDatasetId: "factor-results",
		FilterJson:       `{"freq":"1m"}`,
		Attributes:       map[string]string{"dataset_role": "factor_result"},
	}
	runtime := &viewRuntime{next: "factor-result-view-b"}
	svc := &Service{
		engines:      map[string]viewindex.Engine{"duckdb": engine},
		indexEngine:  map[string]string{"factor-result-view-b": "duckdb"},
		schemas:      map[string]viewindex.ViewIndexSchema{"factor-result-view-b": {SpaceID: "space", ViewID: view.ViewId, PrimaryDatasetID: view.PrimaryDatasetId, Engine: "duckdb", ViewVersion: 1, SchemaHash: "schema"}},
		views:        map[viewRef]*viewRuntime{{spaceID: "space", viewID: view.ViewId}: runtime},
		catalogViews: map[viewRef]*pb.View{{spaceID: "space", viewID: view.ViewId}: view},
	}
	written, err := svc.backfillViewWithReader(context.Background(), "space", view.ViewId, 100, &primaryHistoryFieldReader{}, nil, 0, 2, defaultMaxHistoryScanRows)
	if err != nil {
		t.Fatalf("empty factor result backfill: %v", err)
	}
	if written != 0 || runtime.status != "ready" {
		t.Fatalf("written=%d status=%q, want empty ready build", written, runtime.status)
	}
}

func TestMarshalTimeSeriesHistoryCursorConvertsReadKeyToRowKey(t *testing.T) {
	readKey := &pb.TimeSeriesKey{
		SpaceId: "crypto", DatasetId: "binance_spot_kline_1m",
		SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-18T10:00:00Z", SeriesTag: "default",
	}

	cursor, err := marshalTimeSeriesHistoryCursor(readKey)
	if err != nil {
		t.Fatalf("marshal cursor: %v", err)
	}
	var rowKey pb.RowKey
	if err := proto.Unmarshal(cursor, &rowKey); err != nil {
		t.Fatalf("unmarshal cursor: %v", err)
	}
	if got := rowKey.GetSpaceId(); got != readKey.GetSpaceId() {
		t.Fatalf("space id = %q, want %q", got, readKey.GetSpaceId())
	}
	if got := rowKey.GetDatasetId(); got != readKey.GetDatasetId() {
		t.Fatalf("dataset id = %q, want %q", got, readKey.GetDatasetId())
	}
	if got := rowKey.GetTimeSeries(); got == nil || got.GetSubjectId() != readKey.GetSubjectId() || got.GetDataTime() != readKey.GetDataTime() {
		t.Fatalf("time-series cursor = %+v, want key %+v", got, readKey)
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
		{ColumnName: "close", OriginId: "prices.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "old", OriginId: "prices.old", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}}
	next := viewindex.ViewIndexSchema{Columns: []*pb.ViewColumn{
		{ColumnName: "close", OriginId: "prices.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "old", OriginId: "prices.new", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
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

func TestBackfillTimeRangesUseViewRetention(t *testing.T) {
	ranges := backfillTimeRanges(&pb.View{KeepDuration: "72h"}, 0)
	if len(ranges) < 72*12 {
		t.Fatalf("backfill ranges = %d, want at least %d", len(ranges), 72*12)
	}
	start, err := time.Parse(time.RFC3339Nano, ranges[0].GetStartTime())
	if err != nil {
		t.Fatalf("parse start time: %v", err)
	}
	startAfter := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Minute)
	if start.Before(startAfter.Add(-time.Second)) || start.After(startAfter.Add(time.Second)) {
		t.Fatalf("start time %s is not near the 72h retention boundary", start)
	}
	if ranges[0].GetEndTime() == "" {
		t.Fatal("first range end time is empty")
	}
}

func TestBackfillTimeRangeSkipsPermanentRetention(t *testing.T) {
	got := backfillTimeRanges(&pb.View{KeepDuration: "0"}, 0)
	if len(got) != 1 || got[0] != nil {
		t.Fatalf("permanent retention ranges = %+v, want one nil range", got)
	}
}

func TestBackfillTimeRangesHonorMinimumLookback(t *testing.T) {
	ranges := backfillTimeRanges(&pb.View{KeepDuration: "1h"}, 2*time.Hour)
	if len(ranges) < 2*12 {
		t.Fatalf("backfill ranges = %d, want at least %d", len(ranges), 2*12)
	}
	start, err := time.Parse(time.RFC3339Nano, ranges[0].GetStartTime())
	if err != nil {
		t.Fatal(err)
	}
	boundary := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Minute)
	if start.Before(boundary.Add(-time.Second)) || start.After(boundary.Add(time.Second)) {
		t.Fatalf("start time %s is not near minimum lookback boundary", start)
	}
}

func TestRebuildLookbackRequiresCoverageFromWallClock(t *testing.T) {
	now := time.Now().UTC()
	if err := validateRebuildLookback(viewindex.ViewIndexStats{Exists: true, IndexedFrom: now.Add(-3 * time.Hour).Format(time.RFC3339Nano), IndexedTo: now.Format(time.RFC3339Nano)}, time.Hour); err != nil {
		t.Fatalf("coverage should satisfy lookback: %v", err)
	}
	if err := validateRebuildLookback(viewindex.ViewIndexStats{Exists: true, IndexedFrom: now.Add(-30 * time.Minute).Format(time.RFC3339Nano), IndexedTo: now.Format(time.RFC3339Nano)}, time.Hour); err == nil {
		t.Fatal("insufficient coverage must not be activatable")
	}
}

func TestPeriodCoverageGapsIgnoresSeriesWithoutPrimaryBars(t *testing.T) {
	expected := map[string]struct{}{
		"BTC-USDT\x001m": {},
		"NEW-USDT\x001m": {},
	}
	counts := map[string]uint64{
		"BTC-USDT\x001m": 1000,
	}
	partial, empty := periodCoverageGaps(expected, counts, 1000)
	if len(partial) != 0 {
		t.Fatalf("partial series = %v, want none", partial)
	}
	if !reflect.DeepEqual(empty, []string{"NEW-USDT\x001m"}) {
		t.Fatalf("empty series = %v, want [NEW-USDT\\x001m]", empty)
	}
}

func TestPeriodCoverageGapsReportsPartiallyBackfilledSeries(t *testing.T) {
	expected := map[string]struct{}{
		"BTC-USDT\x001m": {},
		"ETH-USDT\x001m": {},
	}
	counts := map[string]uint64{
		"BTC-USDT\x001m": 999,
	}
	partial, empty := periodCoverageGaps(expected, counts, 1000)
	if !reflect.DeepEqual(partial, []string{"BTC-USDT\x001m"}) {
		t.Fatalf("partial series = %v, want [BTC-USDT\\x001m]", partial)
	}
	if !reflect.DeepEqual(empty, []string{"ETH-USDT\x001m"}) {
		t.Fatalf("empty series = %v, want [ETH-USDT\\x001m]", empty)
	}
}

func TestPeriodSeriesIdentitySeparatesSeriesTags(t *testing.T) {
	if periodSeriesIdentity("BTC-USDT", "1m", "venue:binance") == periodSeriesIdentity("BTC-USDT", "1m", "venue:okx") {
		t.Fatal("different series tags shared one period budget key")
	}
}

func TestFormatPeriodSeriesKeyUsesReadableIdentity(t *testing.T) {
	if got := formatPeriodSeriesKey("BTC-USDT\x001m"); got != "BTC-USDT/1m" {
		t.Fatalf("formatted series = %q, want BTC-USDT/1m", got)
	}
}

func TestBuildPeriodHistorySelectorsLeavesEmptyDatasetBindingsUnfiltered(t *testing.T) {
	selectors, expected := buildPeriodHistorySelectors("crypto", "binance_spot_kline_1m", "1m", nil)
	if len(selectors) != 0 || len(expected) != 0 {
		t.Fatalf("empty bindings selectors=%v expected=%v, want unfiltered scan", selectors, expected)
	}
	selectors, expected = buildPeriodHistorySelectors("crypto", "binance_spot_kline_1m", "1m", []string{"BTC-USDT"})
	if len(selectors) != 1 || len(expected) != 1 || selectors[0].GetSubjectId() != "BTC-USDT" {
		t.Fatalf("bound selectors=%v expected=%v, want one selector", selectors, expected)
	}
}
