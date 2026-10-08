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

type primaryHistoryRangeReader struct {
	rows      []*pb.TimeSeriesRow
	selectors []*pb.TimeSeriesSelector
}

type primaryHistoryFieldReader struct{}

type backfillSubjectCatalogMetadata struct {
	maintenanceMetadata
	subjects []*pb.Subject
}

func (m *backfillSubjectCatalogMetadata) ListSubjects(context.Context, *pb.ListSubjectsReq, ...client.Option) (*pb.ListSubjectsRsp, error) {
	return &pb.ListSubjectsRsp{RetInfo: successRetInfo(), Subjects: m.subjects, PageResult: &pb.PageResult{HasMore: false}}, nil
}

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
		SpaceId:   "space",
		ViewId:    "prices",
		Engine:    "duckdb",
		DatasetId: "market",
		Freq:      "1m",
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

	written, err := svc.backfillViewWithReader(context.Background(), "space", "prices", 100, &primaryHistoryFieldReader{}, reader, 2, defaultMaxHistoryScanRows)
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

func TestBackfillSubjectMatchesInstrumentMarket(t *testing.T) {
	tests := []struct {
		name    string
		subject *pb.Subject
		market  string
		want    bool
	}{
		{name: "instrument attribute", subject: &pb.Subject{SubjectId: "BTC-USDT-SPOT", Attributes: map[string]string{"instrument_type": "spot"}}, market: "spot", want: true},
		{name: "opposite instrument", subject: &pb.Subject{SubjectId: "BTC-USDT-SWAP", Attributes: map[string]string{"instrument_type": "swap"}}, market: "spot", want: false},
		{name: "legacy id fallback", subject: &pb.Subject{SubjectId: "BTC-USDT-SWAP"}, market: "swap", want: true},
		{name: "unknown market is excluded", subject: &pb.Subject{SubjectId: "BTC-USDT"}, market: "spot", want: false},
		{name: "empty market accepts subject", subject: &pb.Subject{SubjectId: "BTC-USDT"}, market: "", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backfillSubjectMatchesMarket(tt.subject, tt.market); got != tt.want {
				t.Fatalf("backfillSubjectMatchesMarket(%q, %q) = %v, want %v", tt.subject.GetSubjectId(), tt.market, got, tt.want)
			}
		})
	}
}

type historySubjectsReader struct {
	primaryHistoryRangeReader
	subjects []string
	requests []*pb.ListHistorySubjectsReq
}

func (r *historySubjectsReader) ListHistorySubjects(_ context.Context, req *pb.ListHistorySubjectsReq, _ ...client.Option) (*pb.ListHistorySubjectsRsp, error) {
	r.requests = append(r.requests, req)
	return &pb.ListHistorySubjectsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, SubjectIds: r.subjects}, nil
}

func TestCapacityMaintenanceRequiresKnownSubjects(t *testing.T) {
	tests := []struct {
		name     string
		metadata MetadataClient
		reader   TimeSeriesRangeReader
		wantOK   bool
		wantWhy  string
	}{
		{name: "metadata client without subject catalog", metadata: &maintenanceMetadata{}, wantWhy: "subject_catalog_unavailable"},
		{name: "empty subject catalog", metadata: &backfillSubjectCatalogMetadata{}, wantWhy: "subject_catalog_unavailable"},
		{name: "usable subject catalog", metadata: &backfillSubjectCatalogMetadata{subjects: []*pb.Subject{{SubjectId: "BTC-USDT-SPOT", Attributes: map[string]string{"instrument_type": "spot"}}}}, wantOK: true},
		{name: "Primary lists the subjects", metadata: &maintenanceMetadata{}, reader: &historySubjectsReader{subjects: []string{"svc-a"}}, wantOK: true},
		{name: "Primary has no subjects", metadata: &maintenanceMetadata{}, reader: &historySubjectsReader{}, wantWhy: "subject_catalog_empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &Service{metadataClient: tt.metadata}
			ok, reason := svc.capacityMaintenanceSubjectsReady(context.Background(), tt.reader, nil, &pb.View{SpaceId: "crypto", DatasetId: "dataset_binance_kline_1m", Freq: "1m"})
			if ok != tt.wantOK || reason != tt.wantWhy {
				t.Fatalf("capacityMaintenanceSubjectsReady() = (%v, %q), want (%v, %q)", ok, reason, tt.wantOK, tt.wantWhy)
			}
		})
	}
}

func TestPeriodBackfillRequiresPrimaryReaderForNewTimeSeriesView(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	view := &pb.View{SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "market", Freq: "1m"}
	svc := &Service{
		engines:      map[string]viewindex.Engine{"duckdb": engine},
		indexEngine:  map[string]string{"prices-b": "duckdb"},
		schemas:      map[string]viewindex.ViewIndexSchema{"prices-b": {SpaceID: "space", ViewID: "prices", PrimaryDatasetID: "market", Engine: "duckdb", ViewVersion: 1, SchemaHash: "schema"}},
		views:        map[viewRef]*viewRuntime{{spaceID: "space", viewID: "prices"}: {next: "prices-b"}},
		catalogViews: map[viewRef]*pb.View{{spaceID: "space", viewID: "prices"}: view},
	}
	if _, err := svc.backfillViewWithReader(context.Background(), "space", "prices", 100, nil, nil, 2, defaultMaxHistoryScanRows); err == nil {
		t.Fatal("new time-series View without Primary reader was accepted")
	}
}

func TestPeriodBackfillActivatesWithAvailableHistoryBelowTarget(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	view := &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "market",
		Freq: "1m",
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
	written, err := svc.backfillViewWithReader(context.Background(), "space", "prices", 100, &primaryHistoryFieldReader{}, reader, 1000, defaultMaxHistoryScanRows)
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
		SpaceId:    "space",
		ViewId:     "factor-result-view",
		Engine:     "duckdb",
		DatasetId:  "factor-results",
		Freq:       "1m",
		Attributes: map[string]string{"dataset_role": "factor_result"},
	}
	runtime := &viewRuntime{next: "factor-result-view-b"}
	svc := &Service{
		engines:      map[string]viewindex.Engine{"duckdb": engine},
		indexEngine:  map[string]string{"factor-result-view-b": "duckdb"},
		schemas:      map[string]viewindex.ViewIndexSchema{"factor-result-view-b": {SpaceID: "space", ViewID: view.ViewId, PrimaryDatasetID: view.DatasetId, Engine: "duckdb", ViewVersion: 1, SchemaHash: "schema"}},
		views:        map[viewRef]*viewRuntime{{spaceID: "space", viewID: view.ViewId}: runtime},
		catalogViews: map[viewRef]*pb.View{{spaceID: "space", viewID: view.ViewId}: view},
	}
	written, err := svc.backfillViewWithReader(context.Background(), "space", view.ViewId, 100, &primaryHistoryFieldReader{}, nil, 2, defaultMaxHistoryScanRows)
	if err != nil {
		t.Fatalf("empty factor result backfill: %v", err)
	}
	if written != 0 || runtime.status != "ready" {
		t.Fatalf("written=%d status=%q, want empty ready build", written, runtime.status)
	}
}

func TestFactorResultViewBackfillsExistingPrimaryOutput(t *testing.T) {
	activeKey := timeSeriesTestRowKey("venue:binance")
	activeKey.DatasetId = "factor-results"
	activeKey.GetTimeSeries().DataTime = "2026-08-18T00:01:00Z"
	engine := &primaryHistoryBackfillEngine{queryRows: []*pb.RowFieldValues{{Key: activeKey}}}
	view := &pb.View{
		SpaceId:    "space",
		ViewId:     "factor-result-view",
		Engine:     "duckdb",
		DatasetId:  "factor-results",
		Freq:       "1m",
		Attributes: map[string]string{"dataset_role": "factor_result"},
	}
	runtime := &viewRuntime{active: "factor-result-view-a", next: "factor-result-view-b"}
	rangeReader := &primaryHistoryRangeReader{rows: []*pb.TimeSeriesRow{{Key: &pb.TimeSeriesKey{
		SpaceId: "space", DatasetId: "factor-results", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-18T00:00:00Z", SeriesTag: "venue:binance",
	}}, {Key: &pb.TimeSeriesKey{
		SpaceId: "space", DatasetId: "factor-results", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-18T00:01:00Z", SeriesTag: "venue:binance",
	}}}}
	svc := &Service{
		engines:      map[string]viewindex.Engine{"duckdb": engine},
		indexEngine:  map[string]string{"factor-result-view-a": "duckdb", "factor-result-view-b": "duckdb"},
		schemas:      map[string]viewindex.ViewIndexSchema{"factor-result-view-a": {SpaceID: "space", ViewID: view.ViewId, PrimaryDatasetID: view.DatasetId, Engine: "duckdb", ViewVersion: 1, SchemaHash: "schema"}, "factor-result-view-b": {SpaceID: "space", ViewID: view.ViewId, PrimaryDatasetID: view.DatasetId, Engine: "duckdb", ViewVersion: 1, SchemaHash: "schema"}},
		views:        map[viewRef]*viewRuntime{{spaceID: "space", viewID: view.ViewId}: runtime},
		catalogViews: map[viewRef]*pb.View{{spaceID: "space", viewID: view.ViewId}: view},
	}
	written, err := svc.backfillViewWithReader(context.Background(), "space", view.ViewId, 100, &primaryHistoryFieldReader{}, rangeReader, 2, defaultMaxHistoryScanRows)
	if err != nil {
		t.Fatalf("factor result history backfill: %v", err)
	}
	if written != 2 || engine.writeRows != 2 || engine.queryCalls != 0 {
		t.Fatalf("written=%d engine_rows=%d query_calls=%d, want authoritative Primary rebuild", written, engine.writeRows, engine.queryCalls)
	}
}

func TestMarshalTimeSeriesHistoryCursorConvertsReadKeyToRowKey(t *testing.T) {
	readKey := &pb.TimeSeriesKey{
		SpaceId: "crypto", DatasetId: "dataset_binance_kline_1m",
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

type latestPerSeriesReader struct {
	historySubjectsReader
	reads []*pb.ReadTimeSeriesRowsReq
}

func (r *latestPerSeriesReader) ReadTimeSeriesRows(_ context.Context, req *pb.ReadTimeSeriesRowsReq, _ ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	r.reads = append(r.reads, req)
	var rows []*pb.TimeSeriesRow
	for _, row := range r.rows {
		if row.GetKey().GetSubjectId() == req.GetSelectors()[0].GetSubjectId() {
			rows = append(rows, row)
		}
	}
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: successRetInfo(), Rows: rows, PageResult: &pb.PageResult{HasMore: false}}, nil
}

func TestBackfillWithoutCatalogReadsLatestBarsOfEverySubjectPrimaryLists(t *testing.T) {
	engine := &primaryHistoryBackfillEngine{}
	view := &pb.View{SpaceId: "mooxsys", ViewId: "metrics", Engine: "duckdb", DatasetId: "service_metrics", Freq: "30s"}
	svc := &Service{
		engines:      map[string]viewindex.Engine{"duckdb": engine},
		indexEngine:  map[string]string{"metrics-b": "duckdb"},
		schemas:      map[string]viewindex.ViewIndexSchema{"metrics-b": {SpaceID: "mooxsys", ViewID: "metrics", PrimaryDatasetID: "service_metrics", Engine: "duckdb", ViewVersion: 1, SchemaHash: "schema"}},
		views:        map[viewRef]*viewRuntime{{spaceID: "mooxsys", viewID: "metrics"}: {next: "metrics-b"}},
		catalogViews: map[viewRef]*pb.View{{spaceID: "mooxsys", viewID: "metrics"}: view},
		// The Dataset has no subject catalog.
		metadataClient: &maintenanceMetadata{view: view},
	}
	row := func(subject, at string) *pb.TimeSeriesRow {
		return &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{SpaceId: "mooxsys", DatasetId: "service_metrics", SubjectId: subject, Freq: "30s", DataTime: at}}
	}
	reader := &latestPerSeriesReader{historySubjectsReader: historySubjectsReader{subjects: []string{"svc-a", "svc-b"}}}
	reader.rows = []*pb.TimeSeriesRow{
		row("svc-a", "2026-10-08T00:01:00Z"), row("svc-a", "2026-10-08T00:00:30Z"), row("svc-a", "2026-10-08T00:00:00Z"),
		row("svc-b", "2026-10-08T00:01:00Z"),
	}
	written, err := svc.backfillViewWithReader(context.Background(), "mooxsys", "metrics", 100, &primaryHistoryFieldReader{}, reader, 2, defaultMaxHistoryScanRows)
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.requests) != 1 || reader.requests[0].GetFreq() != "30s" {
		t.Fatalf("history subject listing = %v", reader.requests)
	}
	if len(reader.reads) != 2 {
		t.Fatalf("Primary reads = %d, want one per subject", len(reader.reads))
	}
	for _, read := range reader.reads {
		if read.GetLatestPerSeries() != 2 || len(read.GetAfterKey()) != 0 || read.GetTimeRange() != nil {
			t.Fatalf("read %v must ask for the latest 2 bars without paging", read)
		}
	}
	if written != 3 || engine.writeRows != 3 {
		t.Fatalf("written=%d rows=%d, want 2 bars of svc-a and the 1 of svc-b", written, engine.writeRows)
	}
}
