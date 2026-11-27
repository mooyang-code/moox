package view

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type queryEngine struct {
	mu        sync.Mutex
	spec      viewindex.QuerySpec
	rows      []*pb.RowFieldValues
	stats     viewindex.ViewIndexStats
	statErr   error
	calls     int
	statCalls int
	writeErrs map[string]error
	writes    map[string]int
}

type existenceQueryEngine struct {
	*queryEngine
	exists      bool
	existsErr   error
	existsCalls int
}

func (e *existenceQueryEngine) Exists(ctx context.Context, _ string) (bool, error) {
	if ctx != nil && ctx.Err() != nil {
		return false, ctx.Err()
	}
	e.existsCalls++
	return e.exists, e.existsErr
}

func (*queryEngine) Engine() string { return "query-test" }
func (*queryEngine) Prepare(context.Context, string, viewindex.ViewIndexSchema) error {
	return nil
}
func (e *queryEngine) Write(_ context.Context, indexID string, _ viewindex.ViewIndexWriteBatch) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.writes == nil {
		e.writes = map[string]int{}
	}
	e.writes[indexID]++
	return e.writeErrs[indexID]
}
func (e *queryEngine) Query(_ context.Context, _ string, spec viewindex.QuerySpec) ([]*pb.RowFieldValues, int64, error) {
	e.mu.Lock()
	e.calls++
	e.spec = spec
	rows := e.rows
	e.mu.Unlock()
	return rows, int64(len(rows)), nil
}
func (e *queryEngine) Stat(context.Context, string) (viewindex.ViewIndexStats, error) {
	e.mu.Lock()
	e.statCalls++
	stats, statErr := e.stats, e.statErr
	e.mu.Unlock()
	return stats, statErr
}
func (*queryEngine) Remove(context.Context, string) error { return nil }

func TestPrimaryPointReadChunkSizeStaysUnderPebbleBudget(t *testing.T) {
	if got := primaryPointReadChunkSize(6, 400); got != 400 {
		t.Fatalf("small batch chunk=%d, want 400", got)
	}
	if got := primaryPointReadChunkSize(250, 2000); got != 400 {
		t.Fatalf("wide schema chunk=%d, want 400", got)
	}
	if got := primaryPointReadChunkSize(1, 20000); got != 512 {
		t.Fatalf("chunk=%d, want 512", got)
	}
}

func TestLiveIndexWriteContextKeepsDeadlineButHonorsCancel(t *testing.T) {
	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	writeCtx, writeCancel, err := liveIndexWriteContext(expired)
	if err != nil {
		t.Fatalf("deadline context: %v", err)
	}
	defer writeCancel()
	if writeCtx.Err() != nil {
		t.Fatal("live write context inherited an expired delivery deadline")
	}

	stopped, stop := context.WithCancel(context.Background())
	stop()
	if _, _, err := liveIndexWriteContext(stopped); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error=%v, want context.Canceled", err)
	}
}

func TestActiveViewDatasetFreshnessTracksSubjectsAndDoesNotRollback(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := observability.NewViewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	engine := &existenceQueryEngine{queryEngine: &queryEngine{}, exists: true}
	svc := queryTestService(engine, true)
	svc.metrics = metrics
	spec := freshnessSpec()

	newer := "2026-09-08T10:05:00Z"
	rows := []*pb.RowFieldUpsert{
		viewFreshnessRow("BTC-USDT", "1m", "venue:binance", "2026-09-08T10:04:00Z"),
		viewFreshnessRow("BTC-USDT", "1m", "venue:binance", newer),
		viewFreshnessRow("SOL-USDT", "1m", "venue:binance", "2026-09-08T09:50:00Z"),
		viewFreshnessRow("ETH-USDT", "5m", "", newer),
	}
	svc.observeViewWatermark(spec, rows)
	// SOL trails the View's latest 1m bar by more than one bar plus the lag
	// tolerance; BTC is current.
	assertViewSubjects(t, registry, "1m", 2, map[string]string{"SOL-USDT": "2026-09-08T09:50:00Z"})
	assertViewSubjects(t, registry, "5m", 1, map[string]string{})

	svc.observeViewWatermark(spec, []*pb.RowFieldUpsert{viewFreshnessRow("BTC-USDT", "1m", "venue:binance", "2026-09-08T10:03:00Z")})
	assertViewSubjects(t, registry, "1m", 2, map[string]string{"SOL-USDT": "2026-09-08T09:50:00Z"})
}

// 没有标的的行不计入。
func TestViewDatasetFreshnessIgnoresMissingIdentity(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := observability.NewViewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	svc := queryTestService(&existenceQueryEngine{queryEngine: &queryEngine{}, exists: true}, true)
	svc.metrics = metrics
	svc.observeViewWatermark(freshnessSpec(), []*pb.RowFieldUpsert{viewFreshnessRow("", "1m", "venue:binance", "2026-09-08T10:05:00Z")})
	assertNoViewSubjects(t, registry)
}

// freshnessSpec 是 market_prices 数据集的 1m 时序声明。
func freshnessSpec() *viewSpec {
	return timeSeriesSpec("space", "market_prices", "1m", "close")
}

func viewFreshnessRow(subject, frequency, seriesTag, dataTime string) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "market_prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, Freq: frequency, SeriesTag: seriesTag, DataTime: dataTime}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}},
	}
}

// assertViewSubjects checks the market_prices summary for one frequency: the
// tracked subject count and the named lagging subjects with their last bar.
func assertViewSubjects(t *testing.T, registry *prometheus.Registry, frequency string, subjects int, lagging map[string]string) {
	t.Helper()
	scope := map[string]string{"space_id": "space", "dataset_id": "market_prices", "freq": frequency}
	gotSubjects, gotLagging := -1.0, -1.0
	gotNames := map[string]string{}
	for _, family := range mustGather(t, registry) {
		for _, metric := range family.GetMetric() {
			labels := make(map[string]string, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			subject := labels["subject_id"]
			delete(labels, "subject_id")
			if !reflect.DeepEqual(labels, scope) {
				continue
			}
			switch family.GetName() {
			case "moox_storage_view_output_subjects":
				gotSubjects = metric.GetGauge().GetValue()
			case "moox_storage_view_output_lagging_subjects":
				gotLagging = metric.GetGauge().GetValue()
			case "moox_storage_view_output_lagging_subject_data_time_seconds":
				gotNames[subject] = time.Unix(int64(metric.GetGauge().GetValue()), 0).UTC().Format(time.RFC3339)
			}
		}
	}
	if gotSubjects != float64(subjects) || gotLagging != float64(len(lagging)) || !reflect.DeepEqual(gotNames, lagging) {
		t.Fatalf("freq=%s subjects=%v lagging=%v names=%v, want %d %d %v", frequency, gotSubjects, gotLagging, gotNames, subjects, len(lagging), lagging)
	}
}

func assertNoViewSubjects(t *testing.T, registry *prometheus.Registry) {
	t.Helper()
	for _, family := range mustGather(t, registry) {
		if family.GetName() == "moox_storage_view_output_subjects" && len(family.GetMetric()) != 0 {
			t.Fatalf("unexpected view subject summary: %v", family)
		}
	}
}

func mustGather(t *testing.T, registry *prometheus.Registry) []*dto.MetricFamily {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	return families
}

func TestQueryTimeSeriesRowsPreservesSelectorPresenceAndExactResultTags(t *testing.T) {
	engine := &queryEngine{}
	svc := queryTestService(engine, true)

	cases := []struct {
		name     string
		tag      *string
		wantTags []string
	}{
		{name: "absent", wantTags: []string{"", "venue:binance", "venue:okx"}},
		{name: "present empty", tag: stringPointer(""), wantTags: []string{""}},
		{name: "present value", tag: stringPointer("venue:okx"), wantTags: []string{"venue:okx"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine.rows = rowsForTags(tc.wantTags)
			rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
				SpaceId:   "space",
				DatasetId: "market",
				Selectors: []*pb.TimeSeriesSelector{{
					SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m", SeriesTag: tc.tag,
				}},
			})
			if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
				t.Fatalf("query rsp=%v err=%v", rsp, err)
			}
			if len(engine.spec.Keys) != 0 || len(engine.spec.Selectors) != 1 {
				t.Fatalf("range selector was downgraded to exact keys: %+v", engine.spec)
			}
			if got := engine.spec.Selectors[0].SeriesTag; !sameOptionalString(got, tc.tag) {
				t.Fatalf("series tag presence lost: got=%v want=%v", got, tc.tag)
			}
			if len(rsp.GetRows()) != len(tc.wantTags) {
				t.Fatalf("rows=%d want=%d", len(rsp.GetRows()), len(tc.wantTags))
			}
			for i, row := range rsp.GetRows() {
				if got := row.GetKey().GetSeriesTag(); got != tc.wantTags[i] {
					t.Fatalf("row %d tag=%q want=%q", i, got, tc.wantTags[i])
				}
			}
		})
	}
}

func TestQueryTimeSeriesRowsValidatesSelectorScope(t *testing.T) {
	engine := &queryEngine{}
	svc := queryTestService(engine, true)
	cases := []struct {
		name     string
		selector *pb.TimeSeriesSelector
	}{
		{name: "nil selector"},
		{name: "missing subject", selector: &pb.TimeSeriesSelector{SpaceId: "space", DatasetId: "market", Freq: "1m"}},
		{name: "wrong space", selector: &pb.TimeSeriesSelector{SpaceId: "other", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
		{name: "wrong dataset", selector: &pb.TimeSeriesSelector{SpaceId: "space", DatasetId: "other", SubjectId: "BTC-USDT", Freq: "1m"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
				SpaceId: "space", DatasetId: "market",
				Selectors: []*pb.TimeSeriesSelector{tc.selector},
			})
			if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
				t.Fatalf("rsp=%v err=%v", rsp, err)
			}
		})
	}
	if engine.calls != 0 {
		t.Fatalf("invalid selectors reached engine %d times", engine.calls)
	}
}

func TestQueryTimeSeriesRowsCompletenessRequiresRowsStatsCoverageAndActiveView(t *testing.T) {
	validStats := viewindex.ViewIndexStats{
		Exists: true, EntryCount: 1,
		IndexedFrom: "2026-07-29T00:00:00Z",
		IndexedTo:   "2026-07-29T00:01:00Z",
	}
	cases := []struct {
		name     string
		active   bool
		rows     []*pb.RowFieldValues
		stats    viewindex.ViewIndexStats
		statErr  error
		complete bool
		noRange  bool
	}{
		{name: "valid coverage", active: true, rows: []*pb.RowFieldValues{{Key: timeSeriesTestRowKey("")}}, stats: validStats, complete: true},
		{name: "empty rows", active: true, stats: validStats, complete: true},
		{name: "stat failure", active: true, rows: []*pb.RowFieldValues{{Key: timeSeriesTestRowKey("")}}, stats: validStats, statErr: errors.New("stat failed")},
		{name: "no valid coverage", active: true, rows: []*pb.RowFieldValues{{Key: timeSeriesTestRowKey("")}}, noRange: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := &queryEngine{rows: tc.rows, stats: tc.stats, statErr: tc.statErr}
			svc := queryTestService(engine, tc.active)
			req := &pb.QueryTimeSeriesRowsReq{
				SpaceId: "space", DatasetId: "market",
				Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
				TimeRange: &pb.TimeRange{StartTime: "2026-07-29T00:00:00Z", EndTime: "2026-07-29T00:01:00Z"},
			}
			if tc.noRange {
				req.TimeRange = nil
			}
			rsp, err := svc.QueryTimeSeriesRows(context.Background(), req)
			if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
				t.Fatalf("rsp=%v err=%v", rsp, err)
			}
			if rsp.GetComplete() != tc.complete {
				t.Fatalf("complete=%v want=%v rsp=%v", rsp.GetComplete(), tc.complete, rsp)
			}
			if tc.name == "valid coverage" && (rsp.GetMinDataTime() != validStats.IndexedFrom || rsp.GetMaxDataTime() != validStats.IndexedTo) {
				t.Fatalf("coverage fields not returned: %v", rsp)
			}
		})
	}
	// 没有活动索引：不可读，调用方等待。
	svc := queryTestService(&queryEngine{stats: validStats}, false)
	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
		SpaceId: "space", DatasetId: "market",
		Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
		TimeRange: &pb.TimeRange{StartTime: "2026-07-29T00:00:00Z", EndTime: "2026-07-29T00:01:00Z"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_READY {
		t.Fatalf("没有活动索引 rsp=%v err=%v，想要 VIEW_NOT_READY", rsp, err)
	}
}

func TestQueryTimeSeriesRowsRejectsActiveIndexMismatchBeforeQuery(t *testing.T) {
	engine := &queryEngine{}
	svc := queryTestService(engine, true)
	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
		SpaceId: "space", DatasetId: "market", Pin: &pb.IndexVersion{IndexId: "prices-old"},
		Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
		TimeRange: &pb.TimeRange{StartTime: "2026-07-29T00:00:00Z", EndTime: "2026-07-29T00:01:00Z"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_READY {
		t.Fatalf("rsp=%v err=%v, want VIEW_NOT_READY", rsp, err)
	}
	if engine.calls != 0 {
		t.Fatalf("engine query calls=%d, want 0 on active-index mismatch", engine.calls)
	}
}

func TestQueryTimeSeriesRowsRejectsInPlaceRevisionMismatch(t *testing.T) {
	engine := &queryEngine{}
	svc := queryTestService(engine, true)
	svc.runtimeOf(marketRef).revision.Store(8)
	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
		SpaceId: "space", DatasetId: "market", Pin: &pb.IndexVersion{IndexId: marketIndexID, Revision: 6},
		Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
		TimeRange: &pb.TimeRange{StartTime: "2026-07-29T00:00:00Z", EndTime: "2026-07-29T00:01:00Z"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_READY {
		t.Fatalf("rsp=%v err=%v, want VIEW_NOT_READY", rsp, err)
	}
	if engine.calls != 0 {
		t.Fatalf("engine query calls=%d, want 0 on in-place revision mismatch", engine.calls)
	}
}

func TestQueryTimeSeriesRowsUsesRuntimeStatsForNoTotal(t *testing.T) {
	stats := viewindex.ViewIndexStats{
		Exists: true, EntryCount: 1,
		IndexedFrom: "2026-07-29T00:00:00Z",
		IndexedTo:   "2026-07-29T00:01:00Z",
	}
	engine := &queryEngine{rows: []*pb.RowFieldValues{{Key: timeSeriesTestRowKey("")}}, stats: stats}
	svc := queryTestService(engine, true)
	runtime := svc.runtimeOf(marketRef)
	runtime.statsIndexID = marketIndexID
	runtime.stats = stats
	runtime.publishReadStateLocked()

	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
		SpaceId: "space", DatasetId: "market", TotalMode: pb.TotalMode_NONE,
		Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
		TimeRange: &pb.TimeRange{StartTime: "2026-07-29T00:00:00Z", EndTime: "2026-07-29T00:01:00Z"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
	if engine.statCalls != 0 {
		t.Fatalf("total_mode=NONE called Stat %d times", engine.statCalls)
	}
	if !rsp.GetComplete() || rsp.GetMaxDataTime() != stats.IndexedTo {
		t.Fatalf("runtime stats were not returned: %v", rsp)
	}
}

func TestQueryTimeSeriesRowsRefreshesStatsWhenCompletenessIsRequested(t *testing.T) {
	stats := viewindex.ViewIndexStats{
		Exists: true, EntryCount: 1,
		IndexedFrom: "2026-07-29T00:00:00Z",
		IndexedTo:   "2026-07-29T00:01:00Z",
	}
	engine := &queryEngine{
		rows:    []*pb.RowFieldValues{{Key: timeSeriesTestRowKey("")}},
		stats:   stats,
		statErr: errors.New("stat failed"),
	}
	svc := queryTestService(engine, true)
	runtime := svc.runtimeOf(marketRef)
	runtime.statsIndexID = marketIndexID
	runtime.stats = stats
	runtime.publishReadStateLocked()

	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
		SpaceId: "space", DatasetId: "market",
		Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
		TimeRange: &pb.TimeRange{StartTime: "2026-07-29T00:00:00Z", EndTime: "2026-07-29T00:01:00Z"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
	if engine.statCalls != 1 || rsp.GetComplete() || rsp.GetMaxDataTime() != "" {
		t.Fatalf("completeness query used stale runtime stats: %v", rsp)
	}
}

func TestQueryTimeSeriesRowsCoverageUsesHalfOpenNanosecondBoundary(t *testing.T) {
	cases := []struct {
		name     string
		rng      *pb.TimeRange
		complete bool
	}{
		{
			name: "one nanosecond past inclusive max",
			rng: &pb.TimeRange{
				StartTime: "2026-07-29T00:00:00Z",
				EndTime:   "2026-07-29T00:00:00.000000001Z",
			},
			complete: true,
		},
		{
			name: "two nanoseconds past inclusive max",
			rng: &pb.TimeRange{
				StartTime: "2026-07-29T00:00:00Z",
				EndTime:   "2026-07-29T00:00:00.000000002Z",
			},
		},
		{
			name: "missing end",
			rng:  &pb.TimeRange{StartTime: "2026-07-29T00:00:00Z"},
		},
		{
			name: "missing start",
			rng:  &pb.TimeRange{EndTime: "2026-07-29T00:00:00.000000001Z"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := &queryEngine{
				rows: []*pb.RowFieldValues{{Key: timeSeriesTestRowKey("")}},
				stats: viewindex.ViewIndexStats{
					Exists: true, EntryCount: 1,
					IndexedFrom: "2026-07-29T00:00:00Z",
					IndexedTo:   "2026-07-29T00:00:00Z",
				},
			}
			svc := queryTestService(engine, true)
			rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
				SpaceId: "space", DatasetId: "market",
				Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
				TimeRange: tc.rng,
			})
			if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
				t.Fatalf("rsp=%v err=%v", rsp, err)
			}
			if rsp.GetComplete() != tc.complete {
				t.Fatalf("complete=%v want=%v rsp=%v", rsp.GetComplete(), tc.complete, rsp)
			}
		})
	}
}

// marketRef 是查询测试里的 market 数据集；marketIndexID 是它的索引标识。
var (
	marketRef     = datasetRef{spaceID: "space", datasetID: "market"}
	marketIndexID = viewindex.IndexID("space", "market")
)

func queryTestService(engine viewindex.Engine, active bool) *Service {
	svc := newTestService(map[string]viewindex.Engine{"duckdb": engine})
	runtime := testRuntime(svc, timeSeriesSpec("space", "market", "1m", "close"))
	if active {
		attachTestIndex(runtime, viewindex.ViewIndexSchema{SpaceID: "space", DatasetID: "market"})
	}
	return svc
}

func timeSeriesTestRowKey(tag string) *pb.RowKey {
	return &pb.RowKey{
		SpaceId: "space", DatasetId: "market",
		Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-29T00:00:00Z", SeriesTag: tag,
		}},
	}
}

func rowsForTags(tags []string) []*pb.RowFieldValues {
	rows := make([]*pb.RowFieldValues, 0, len(tags))
	for _, tag := range tags {
		rows = append(rows, &pb.RowFieldValues{Key: timeSeriesTestRowKey(tag)})
	}
	return rows
}

func stringPointer(value string) *string { return &value }

func sameOptionalString(left, right *string) bool {
	return (left == nil && right == nil) ||
		(left != nil && right != nil && reflect.DeepEqual(*left, *right))
}

// 策略用“只带时间范围、不命中任何行”的查询读取覆盖范围与每序列保留根数：TotalMode=NONE 只返回已缓存的统计，
// FORCE_EXACT 现算并缓存，之后的 NONE 查询也能拿到。
func TestQueryTimeSeriesRowsServesCoverageProbe(t *testing.T) {
	engine := &queryEngine{stats: viewindex.ViewIndexStats{Exists: true, EntryCount: 1, IndexedFrom: "2026-07-29T00:00:00Z", IndexedTo: "2026-07-29T00:01:00Z", RowsPerSeries: 5000}}
	svc := queryTestService(engine, true)
	svc.seriesBars = 5000
	probe := func(mode pb.TotalMode) *pb.QueryTimeSeriesRowsRsp {
		t.Helper()
		rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
			SpaceId: "space", DatasetId: "market", Limit: 1, TotalMode: mode,
			TimeRange: &pb.TimeRange{StartTime: "1970-01-01T00:00:00Z", EndTime: "1970-01-01T00:00:00.000000001Z"},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("rsp=%v err=%v", rsp, err)
		}
		return rsp
	}
	if rsp := probe(pb.TotalMode_NONE); rsp.GetMinDataTime() != "" || rsp.GetRetainedRowsPerSeries() != 0 {
		t.Fatalf("统计未缓存时 NONE 不应现算覆盖范围，保留行数为未知(0)：%v", rsp)
	}
	if rsp := probe(pb.TotalMode_FORCE_EXACT); rsp.GetMinDataTime() != "2026-07-29T00:00:00Z" || rsp.GetMaxDataTime() != "2026-07-29T00:01:00Z" || rsp.GetRetainedRowsPerSeries() != 5000 {
		t.Fatalf("FORCE_EXACT 应现算覆盖范围与保留行数：%v", rsp)
	}
	if rsp := probe(pb.TotalMode_NONE); rsp.GetMinDataTime() != "2026-07-29T00:00:00Z" || rsp.GetRetainedRowsPerSeries() != 5000 {
		t.Fatalf("现算后的统计应被缓存：%v", rsp)
	}
	// 调小配置立即生效；调大配置在索引重建之前不会提高对外承诺；旧索引（没有记录）为未知。
	svc.seriesBars = 3000
	if rsp := probe(pb.TotalMode_NONE); rsp.GetRetainedRowsPerSeries() != 3000 {
		t.Fatalf("调小配置应立即生效：%v", rsp)
	}
	svc.seriesBars = 8000
	if rsp := probe(pb.TotalMode_NONE); rsp.GetRetainedRowsPerSeries() != 5000 {
		t.Fatalf("调大配置在索引重建前不应提高保留行数：%v", rsp)
	}
	engine.stats.RowsPerSeries = 0
	svc.cacheActiveIndexStats(svc.runtimeOf(marketRef), marketIndexID, engine.stats)
	if rsp := probe(pb.TotalMode_NONE); rsp.GetRetainedRowsPerSeries() != 0 {
		t.Fatalf("旧索引没有记录保留行数时应为未知：%v", rsp)
	}
}

type metadataCountingEngine struct {
	*queryEngine
	statMetaCalls int
	onQuery       func()
}

func (e *metadataCountingEngine) StatMetadata(context.Context, string) (viewindex.ViewIndexStats, error) {
	e.statMetaCalls++
	return viewindex.ViewIndexStats{Exists: true, UpdatedAt: "2026-11-27T00:00:00Z"}, nil
}

func (e *metadataCountingEngine) Query(ctx context.Context, indexID string, spec viewindex.QuerySpec) ([]*pb.RowFieldValues, int64, error) {
	if e.onQuery != nil {
		e.onQuery()
	}
	return e.queryEngine.Query(ctx, indexID, spec)
}

// QueryTimeSeriesRows 直接读内存原子 revision，不调用 StatMetadata；
// 初始 revision 为非零偶数且跨重建单调递增，能检测首次写入前的多页翻页撕裂读与查询期间并发写入/摘除。
func TestQueryTimeSeriesRowsUsesMonotonicAtomicRevisionWithoutStatMetadata(t *testing.T) {
	// 1. 新建运行时初始化非零偶数且单调递增（步长 >= 2）。
	r1 := newViewRuntime(timeSeriesSpec("space", "market", "1m", "close"), &queryEngine{})
	r2 := newViewRuntime(timeSeriesSpec("space", "market", "1m", "close"), &queryEngine{})
	v1, v2 := r1.revision.Load(), r2.revision.Load()
	if v1 < 2 || v1&1 != 0 || v2&1 != 0 || v2 < v1+2 {
		t.Fatalf("初始修订号应为非零偶数且步长至少为 2：r1=%d r2=%d", v1, v2)
	}

	engine := &metadataCountingEngine{queryEngine: &queryEngine{rows: []*pb.RowFieldValues{{Key: timeSeriesTestRowKey("")}}}}
	svc := queryTestService(engine, true)
	req := func(pin *pb.IndexVersion) *pb.QueryTimeSeriesRowsReq {
		return &pb.QueryTimeSeriesRowsReq{
			SpaceId: "space", DatasetId: "market", TotalMode: pb.TotalMode_NONE, Pin: pin,
			Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
		}
	}

	// 2. 启动后首次写入前：Pin.Revision == 0 跳过校验并返回非零偶数修订号，且完全不调 StatMetadata。
	page1, err := svc.QueryTimeSeriesRows(context.Background(), req(&pb.IndexVersion{IndexId: marketIndexID, Revision: 0}))
	if err != nil || page1.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("page1=%v err=%v", page1, err)
	}
	rev1 := page1.GetIndex().GetRevision()
	if rev1 == 0 || rev1&1 != 0 {
		t.Fatalf("首次查询应返回非零偶数 revision，实际=%d", rev1)
	}
	if engine.statMetaCalls != 0 {
		t.Fatalf("QueryTimeSeriesRows 不应调用 StatMetadata，实际调用 %d 次", engine.statMetaCalls)
	}

	// 3. 无写入时带相同 Pin 翻页成功。
	page2OK, err := svc.QueryTimeSeriesRows(context.Background(), req(&pb.IndexVersion{IndexId: marketIndexID, Revision: rev1}))
	if err != nil || page2OK.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || page2OK.GetIndex().GetRevision() != rev1 {
		t.Fatalf("page2OK=%v err=%v", page2OK, err)
	}

	// 4. 发生一次写入后，旧 Pin 翻页被拒绝 VIEW_NOT_READY 且不进入引擎查询。
	callsBefore := engine.calls
	if err := svc.writeIndex(context.Background(), svc.runtimeOf(marketRef), viewindex.ViewIndexWriteBatch{
		RowWrites: []viewindex.RowWrite{{Key: viewindex.RowKey{Key: timeSeriesTestRowKey("")}}},
	}); err != nil {
		t.Fatal(err)
	}
	page2Stale, err := svc.QueryTimeSeriesRows(context.Background(), req(&pb.IndexVersion{IndexId: marketIndexID, Revision: rev1}))
	if err != nil || page2Stale.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_READY {
		t.Fatalf("写入后旧 Pin 应返回 VIEW_NOT_READY：rsp=%v err=%v", page2Stale, err)
	}
	if engine.calls != callsBefore {
		t.Fatalf("Pin 修订号不匹配时不应进入引擎查询")
	}

	// 5. 查询执行期间发生并发写入：查询结束后检测到修订号变化，返回 VIEW_NOT_READY。
	rt := svc.runtimeOf(marketRef)
	engine.onQuery = func() {
		engine.onQuery = nil
		if err := svc.writeIndex(context.Background(), rt, viewindex.ViewIndexWriteBatch{
			RowWrites: []viewindex.RowWrite{{Key: viewindex.RowKey{Key: timeSeriesTestRowKey("")}}},
		}); err != nil {
			t.Errorf("concurrent writeIndex: %v", err)
		}
	}
	torn, err := svc.QueryTimeSeriesRows(context.Background(), req(nil))
	if err != nil || torn.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_READY {
		t.Fatalf("查询期间并发写入应返回 VIEW_NOT_READY：rsp=%v err=%v", torn, err)
	}

	// 6. 查询执行期间活动索引被清空/摘除：查询结束后检测到修订号变化，返回 VIEW_NOT_READY。
	engine.onQuery = func() {
		engine.onQuery = nil
		rt.mu.Lock()
		rt.clearActiveLocked()
		rt.mu.Unlock()
	}
	detached, err := svc.QueryTimeSeriesRows(context.Background(), req(nil))
	if err != nil || detached.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_READY {
		t.Fatalf("查询期间索引被摘除应返回 VIEW_NOT_READY：rsp=%v err=%v", detached, err)
	}
	if engine.statMetaCalls != 0 {
		t.Fatalf("整套查询流程都不应调用 StatMetadata，实际调用 %d 次", engine.statMetaCalls)
	}
}

type seqlockHookEngine struct {
	*queryEngine
	onWrite  func() error
	onQuery  func()
	onExtend func() error
	qCalls   atomic.Int64
}

func (e *seqlockHookEngine) Write(ctx context.Context, indexID string, batch viewindex.ViewIndexWriteBatch) error {
	if e.onWrite != nil {
		return e.onWrite()
	}
	return e.queryEngine.Write(ctx, indexID, batch)
}

func (e *seqlockHookEngine) Query(ctx context.Context, indexID string, spec viewindex.QuerySpec) ([]*pb.RowFieldValues, int64, error) {
	e.qCalls.Add(1)
	if e.onQuery != nil {
		e.onQuery()
	}
	return e.queryEngine.Query(ctx, indexID, spec)
}

func (e *seqlockHookEngine) ExtendColumns(context.Context, string, viewindex.ViewIndexSchema, viewindex.ViewIndexSchema) error {
	if e.onExtend != nil {
		return e.onExtend()
	}
	return nil
}

// 验证 Seqlock 奇偶协议：
// 1. 引擎写入进行中（奇数 revision）直接返回 VIEW_NOT_READY 且不进入引擎查询；
// 2. writeIndex / ExtendColumns 失败后 revision 恢复偶数不卡死；
// 3. 索引切换路径保持偶数递增并与 gate 下的奇偶不变式正交；
// 4. 查询期间写入刚开始尚未结束（仍处于奇数）即可被检出撕裂读。
func TestViewRevisionSeqlockWriteInProgressFailureAndSwitching(t *testing.T) {
	engine := &seqlockHookEngine{queryEngine: &queryEngine{rows: []*pb.RowFieldValues{{Key: timeSeriesTestRowKey("")}}}}
	svc := queryTestService(engine, true)
	rt := svc.runtimeOf(marketRef)
	queryReq := func() *pb.QueryTimeSeriesRowsReq {
		return &pb.QueryTimeSeriesRowsReq{
			SpaceId: "space", DatasetId: "market", TotalMode: pb.TotalMode_NONE,
			Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
		}
	}
	sampleBatch := viewindex.ViewIndexWriteBatch{
		RowWrites: []viewindex.RowWrite{{Key: viewindex.RowKey{Key: timeSeriesTestRowKey("")}}},
	}

	// 1. 写入进行中：阻塞在 engine.Write 内部，revision 必须为奇数，查询直接返回 VIEW_NOT_READY 且不调 engine.Query。
	before := rt.revision.Load()
	if before == 0 || before&1 != 0 {
		t.Fatalf("初始 revision 应为非零偶数，实际=%d", before)
	}
	inWrite := make(chan struct{})
	releaseWrite := make(chan struct{})
	engine.onWrite = func() error {
		close(inWrite)
		<-releaseWrite
		return nil
	}
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- svc.writeIndex(context.Background(), rt, sampleBatch)
	}()
	<-inWrite
	if mid := rt.revision.Load(); mid != before+1 || mid&1 != 1 {
		t.Fatalf("写入进行中 revision 应为奇数 %d，实际=%d", before+1, mid)
	}
	rspMid, err := svc.QueryTimeSeriesRows(context.Background(), queryReq())
	if err != nil || rspMid.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_READY {
		t.Fatalf("写入进行中查询应直接返回 VIEW_NOT_READY：rsp=%v err=%v", rspMid, err)
	}
	if engine.qCalls.Load() != 0 {
		t.Fatalf("奇数 revision 拒绝时不应调用 engine.Query，实际=%d", engine.qCalls.Load())
	}
	close(releaseWrite)
	if err := <-writeDone; err != nil {
		t.Fatalf("writeIndex 返回错误：%v", err)
	}
	if after := rt.revision.Load(); after != before+2 || after&1 != 0 {
		t.Fatalf("写入完成后 revision 应恢复偶数 %d，实际=%d", before+2, after)
	}
	rspAfter, err := svc.QueryTimeSeriesRows(context.Background(), queryReq())
	if err != nil || rspAfter.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || rspAfter.GetIndex().GetRevision() != before+2 {
		t.Fatalf("写入结束后查询应恢复成功：rsp=%v err=%v", rspAfter, err)
	}

	// 2. writeIndex 失败：进入时置奇，返回错误后恢复偶数不卡死。
	before = rt.revision.Load()
	var sawOddInWriteErr bool
	engine.onWrite = func() error {
		sawOddInWriteErr = rt.revision.Load() == before+1
		return errors.New("模拟写入失败")
	}
	if err := svc.writeIndex(context.Background(), rt, sampleBatch); err == nil {
		t.Fatal("期望 writeIndex 返回错误")
	}
	if !sawOddInWriteErr {
		t.Fatal("engine.Write 执行期间 revision 应为奇数")
	}
	if after := rt.revision.Load(); after != before+2 || after&1 != 0 {
		t.Fatalf("writeIndex 失败后 revision 应恢复偶数 %d，实际=%d", before+2, after)
	}
	if rsp, err := svc.QueryTimeSeriesRows(context.Background(), queryReq()); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("writeIndex 失败恢复后查询应成功：rsp=%v err=%v", rsp, err)
	}

	// 3. ExtendColumns 失败与成功：期间为奇数，返回后均恢复偶数。
	baseSchema := viewindex.ViewIndexSchema{
		SpaceID: "space", DatasetID: "market", ViewVersion: 1,
		Columns: []*viewindex.Column{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, SortOrder: 0}},
	}
	baseSchema.SchemaHash = viewindex.HashViewIndexSchema(baseSchema)
	rt.mu.Lock()
	rt.schema = baseSchema
	rt.publishReadStateLocked()
	rt.mu.Unlock()
	extendedSpec := timeSeriesSpec("space", "market", "1m", "close", "volume")

	before = rt.revision.Load()
	var sawOddInExtend bool
	engine.onExtend = func() error {
		sawOddInExtend = rt.revision.Load() == before+1
		return errors.New("模拟加列失败")
	}
	if _, err := svc.extendViewSchema(context.Background(), extendedSpec); err == nil {
		t.Fatal("期望 extendViewSchema 返回错误")
	}
	if !sawOddInExtend {
		t.Fatal("ExtendColumns 执行期间 revision 应为奇数")
	}
	if after := rt.revision.Load(); after != before+2 || after&1 != 0 {
		t.Fatalf("ExtendColumns 失败后 revision 应恢复偶数 %d，实际=%d", before+2, after)
	}
	if rsp, err := svc.QueryTimeSeriesRows(context.Background(), queryReq()); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ExtendColumns 失败后查询应成功：rsp=%v err=%v", rsp, err)
	}

	before = rt.revision.Load()
	sawOddInExtend = false
	engine.onExtend = func() error {
		sawOddInExtend = rt.revision.Load() == before+1
		return nil
	}
	if ver, err := svc.extendViewSchema(context.Background(), extendedSpec); err != nil || ver != 2 {
		t.Fatalf("ExtendColumns 成功路径失败：ver=%d err=%v", ver, err)
	}
	if !sawOddInExtend {
		t.Fatal("ExtendColumns 成功路径执行期间 revision 应为奇数")
	}
	if after := rt.revision.Load(); after != before+2 || after&1 != 0 {
		t.Fatalf("ExtendColumns 成功后 revision 应为偶数 %d，实际=%d", before+2, after)
	}

	// 4. 查询期间写入开始但尚未提交（仍停在奇数态）：查询结束后 100% 检出撕裂。
	inWrite2 := make(chan struct{})
	releaseWrite2 := make(chan struct{})
	writeDone2 := make(chan error, 1)
	engine.onWrite = func() error {
		close(inWrite2)
		<-releaseWrite2
		return nil
	}
	engine.onQuery = func() {
		engine.onQuery = nil
		go func() {
			writeDone2 <- svc.writeIndex(context.Background(), rt, sampleBatch)
		}()
		<-inWrite2
	}
	rspMidWrite, err := svc.QueryTimeSeriesRows(context.Background(), queryReq())
	close(releaseWrite2)
	if werr := <-writeDone2; werr != nil {
		t.Fatalf("并发 writeIndex 失败：%v", werr)
	}
	if err != nil || rspMidWrite.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_READY {
		t.Fatalf("查询期间写刚开始（尚未完成提交）也必须返回 VIEW_NOT_READY：rsp=%v err=%v", rspMidWrite, err)
	}
	if rt.revision.Load()&1 != 0 {
		t.Fatalf("并发写结束后 revision 应恢复偶数，实际=%d", rt.revision.Load())
	}

	// 5. 索引切换路径（clearActiveLocked / prepareIndex / attachActiveLocked / buildIndex）均保持偶数递增，
	// 且在写入进行中（奇数）发生索引切换（+2）时奇偶不变式完全正交。
	engine.onWrite = nil
	engine.onExtend = nil
	checkEvenStep := func(name string, fn func()) {
		t.Helper()
		prev := rt.revision.Load()
		fn()
		curr := rt.revision.Load()
		if curr <= prev || curr&1 != 0 {
			t.Fatalf("%s 应保持偶数严格递增：prev=%d curr=%d", name, prev, curr)
		}
	}
	checkEvenStep("clearActiveLocked", func() {
		rt.mu.Lock()
		rt.clearActiveLocked()
		rt.mu.Unlock()
	})
	checkEvenStep("attachActiveLocked", func() {
		rt.mu.Lock()
		attachActiveLocked(rt, extendedSpec, rt.schema)
		rt.mu.Unlock()
	})
	checkEvenStep("prepareIndex", func() {
		if _, err := svc.prepareIndex(context.Background(), rt, func(r *viewRuntime) (viewindex.ViewIndexSchema, error) {
			return r.schema, nil
		}); err != nil {
			t.Fatalf("prepareIndex: %v", err)
		}
	})
	checkEvenStep("buildIndex", func() {
		if _, err := svc.buildIndex(context.Background(), MaintenanceOptions{PrimaryRange: &pagedRangeReader{}}, rt, extendedSpec, func(r *viewRuntime) (viewindex.ViewIndexSchema, error) {
			return r.schema, nil
		}); err != nil {
			t.Fatalf("buildIndex: %v", err)
		}
	})

	// 奇偶正交性验证：gate 内 beginEngineWrite() 置奇时，mu 下 clearActiveLocked() 加 2 仍保持奇数，endWrite() 后准确回到偶数。
	before = rt.revision.Load()
	endWrite := rt.beginEngineWrite()
	if rt.revision.Load() != before+1 {
		t.Fatalf("beginEngineWrite 后应为 %d，实际=%d", before+1, rt.revision.Load())
	}
	rt.mu.Lock()
	rt.clearActiveLocked()
	rt.mu.Unlock()
	if mid := rt.revision.Load(); mid != before+3 || mid&1 != 1 {
		t.Fatalf("写进行中发生索引切换后应保持奇数 %d，实际=%d", before+3, mid)
	}
	endWrite()
	if after := rt.revision.Load(); after != before+4 || after&1 != 0 {
		t.Fatalf("写完成后应恢复偶数 %d，实际=%d", before+4, after)
	}
}

type seqlockConcurrentEngine struct {
	verA atomic.Uint64
	verB atomic.Uint64
}

func (*seqlockConcurrentEngine) Engine() string { return "seqlock-concurrent" }
func (*seqlockConcurrentEngine) Prepare(context.Context, string, viewindex.ViewIndexSchema) error {
	return nil
}
func (*seqlockConcurrentEngine) Stat(context.Context, string) (viewindex.ViewIndexStats, error) {
	return viewindex.ViewIndexStats{Exists: true}, nil
}
func (*seqlockConcurrentEngine) Remove(context.Context, string) error { return nil }

// Write 将两阶段状态更新拆开并主动 yield，放大写中途与提交前后的并发撕裂窗口。
func (e *seqlockConcurrentEngine) Write(_ context.Context, _ string, _ viewindex.ViewIndexWriteBatch) error {
	next := e.verA.Load() + 1
	e.verA.Store(next)
	runtime.Gosched()
	e.verB.Store(next)
	return nil
}

// Query 读两阶段状态并主动 yield，将 (a, b) 编码进 SeriesTag 供外层校验是否出现撕裂读。
func (e *seqlockConcurrentEngine) Query(_ context.Context, _ string, _ viewindex.QuerySpec) ([]*pb.RowFieldValues, int64, error) {
	a := e.verA.Load()
	runtime.Gosched()
	b := e.verB.Load()
	return []*pb.RowFieldValues{{Key: timeSeriesTestRowKey(fmt.Sprintf("%d:%d", a, b))}}, 1, nil
}

// 并发读写压测（配合 -race）：验证任何返回 SUCCESS 的查询绝不会读到撕裂状态 (verA != verB) 或奇数修订号。
func TestViewRevisionSeqlockConcurrentReadWriteNeverReturnsTornSnapshot(t *testing.T) {
	engine := &seqlockConcurrentEngine{}
	svc := queryTestService(engine, true)
	rt := svc.runtimeOf(marketRef)
	sampleBatch := viewindex.ViewIndexWriteBatch{
		RowWrites: []viewindex.RowWrite{{Key: viewindex.RowKey{Key: timeSeriesTestRowKey("")}}},
	}
	req := &pb.QueryTimeSeriesRowsReq{
		SpaceId: "space", DatasetId: "market", TotalMode: pb.TotalMode_NONE,
		Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
	}

	const totalWrites = 300
	var done atomic.Bool
	var okCount, retryCount atomic.Int64
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !done.Load() {
				rsp, err := svc.QueryTimeSeriesRows(context.Background(), req)
				if err != nil {
					t.Errorf("QueryTimeSeriesRows error: %v", err)
					return
				}
				switch rsp.GetRetInfo().GetCode() {
				case pb.ErrorCode_VIEW_NOT_READY:
					retryCount.Add(1)
				case pb.ErrorCode_SUCCESS:
					okCount.Add(1)
					rev := rsp.GetIndex().GetRevision()
					if rev == 0 || rev&1 != 0 {
						t.Errorf("SUCCESS 查询返回了非法修订号：%d", rev)
						return
					}
					if len(rsp.GetRows()) != 1 {
						t.Errorf("期望 1 行，实际=%d", len(rsp.GetRows()))
						return
					}
					tag := rsp.GetRows()[0].GetKey().GetSeriesTag()
					var a, b uint64
					if _, scanErr := fmt.Sscanf(tag, "%d:%d", &a, &b); scanErr != nil || a != b {
						t.Errorf("检测到并发撕裂读！tag=%q revision=%d", tag, rev)
						return
					}
				default:
					t.Errorf("未预期的响应码：%v", rsp.GetRetInfo())
					return
				}
			}
		}()
	}

	for range totalWrites {
		if err := svc.writeIndex(context.Background(), rt, sampleBatch); err != nil {
			t.Fatalf("writeIndex: %v", err)
		}
		runtime.Gosched()
	}
	done.Store(true)
	wg.Wait()

	if rev := rt.revision.Load(); rev&1 != 0 {
		t.Fatalf("全部写入完成后 revision 应为偶数，实际=%d", rev)
	}
	if okCount.Load() == 0 && retryCount.Load() == 0 {
		t.Fatal("并发读 goroutine 未执行查询")
	}
	finalRsp, err := svc.QueryTimeSeriesRows(context.Background(), req)
	if err != nil || finalRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("写入结束后最终查询失败：rsp=%v err=%v", finalRsp, err)
	}
	wantTag := fmt.Sprintf("%d:%d", totalWrites, totalWrites)
	if got := finalRsp.GetRows()[0].GetKey().GetSeriesTag(); got != wantTag {
		t.Fatalf("最终快照不一致：got=%q want=%q", got, wantTag)
	}
}
