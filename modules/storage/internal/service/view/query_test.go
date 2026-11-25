package view

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type queryEngine struct {
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
	if e.writes == nil {
		e.writes = map[string]int{}
	}
	e.writes[indexID]++
	return e.writeErrs[indexID]
}
func (e *queryEngine) Query(_ context.Context, _ string, spec viewindex.QuerySpec) ([]*pb.RowFieldValues, int64, error) {
	e.calls++
	e.spec = spec
	return e.rows, int64(len(e.rows)), nil
}
func (e *queryEngine) Stat(context.Context, string) (viewindex.ViewIndexStats, error) {
	e.statCalls++
	return e.stats, e.statErr
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
	svc.runtimeOf(marketRef).revision.Store(7)
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
