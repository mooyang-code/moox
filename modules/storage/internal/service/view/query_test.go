package view

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
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

func (e *existenceQueryEngine) Exists(context.Context, string) (bool, error) {
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

func TestLiveIndexReadyUsesLightweightExistenceCheck(t *testing.T) {
	engine := &existenceQueryEngine{queryEngine: &queryEngine{}, exists: true}
	svc, _ := queryTestService(engine, true)
	ready, err := svc.liveIndexReady(context.Background(), "prices-index")
	if err != nil || !ready {
		t.Fatalf("liveIndexReady ready=%v err=%v", ready, err)
	}
	if engine.existsCalls != 1 || engine.statCalls != 0 {
		t.Fatalf("exists calls=%d stat calls=%d, want 1 and 0", engine.existsCalls, engine.statCalls)
	}
}

func TestLiveWritePreservesRowInReplacementWhenExistingActiveIsUnwritable(t *testing.T) {
	activeErr := errors.New("active index is corrupt")
	engine := &existenceQueryEngine{
		queryEngine: &queryEngine{writeErrs: map[string]error{"prices-index": activeErr}},
		exists:      true,
	}
	svc, _ := queryTestService(engine, true)
	key := viewRef{spaceID: "space", viewID: "prices"}
	svc.views[key].next = "prices-next"
	svc.views[key].status = "building"
	svc.indexEngine["prices-next"] = "query-test"
	svc.schemas["prices-index"] = viewindex.ViewIndexSchema{
		SpaceID: "space", ViewID: "prices", PrimaryDatasetID: "market",
		Columns: []*pb.ViewColumn{{OriginId: "market.close", ColumnName: "close"}},
	}
	svc.schemas["prices-next"] = svc.schemas["prices-index"]
	svc.indexView = map[string]viewRef{"prices-index": key, "prices-next": key}
	svc.byData = map[datasetRef]map[string]struct{}{
		{spaceID: "space", datasetID: "market"}: {"prices-index": {}, "prices-next": {}},
	}

	err := svc.applyDatasetEvent(context.Background(), "space", "market", []*pb.RowFieldUpsert{{
		Key: timeSeriesTestRowKey("venue:binance"),
		Fields: []*pb.FieldValue{{
			FieldId: "close",
			Value:   &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}},
		}},
	}})
	if !errors.Is(err, activeErr) {
		t.Fatalf("apply error=%v, want active failure", err)
	}
	if engine.writes["prices-index"] != 1 || engine.writes["prices-next"] != 1 {
		t.Fatalf("writes=%v, want one active attempt and one replacement write", engine.writes)
	}
}

func TestActiveViewDatasetFreshnessTracksSubjectsAndDoesNotRollback(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := observability.NewViewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	engine := &existenceQueryEngine{queryEngine: &queryEngine{}, exists: true}
	svc, _ := queryTestService(engine, true)
	configureDatasetFreshnessView(svc, metrics, "prices-index", "")

	newer := "2026-09-08T10:05:00Z"
	rows := []*pb.RowFieldUpsert{
		viewFreshnessRow("BTC-USDT", "1m", "venue:binance", "2026-09-08T10:04:00Z"),
		viewFreshnessRow("BTC-USDT", "1m", "venue:binance", newer),
		viewFreshnessRow("ETH-USDT", "5m", "", newer),
	}
	if err := svc.applyDatasetEvent(context.Background(), "space", "market_prices", rows); err != nil {
		t.Fatal(err)
	}
	assertViewDatasetMetric(t, registry, "BTC-USDT", "1m", "venue:binance", newer)
	assertViewDatasetMetric(t, registry, "ETH-USDT", "5m", "default", newer)

	if err := svc.applyDatasetEvent(context.Background(), "space", "market_prices", []*pb.RowFieldUpsert{viewFreshnessRow("BTC-USDT", "1m", "venue:binance", "2026-09-08T10:03:00Z")}); err != nil {
		t.Fatal(err)
	}
	assertViewDatasetMetric(t, registry, "BTC-USDT", "1m", "venue:binance", newer)
}

func TestViewDatasetFreshnessIgnoresReplacementFailuresAndMissingIdentity(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := observability.NewViewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	engine := &existenceQueryEngine{queryEngine: &queryEngine{writeErrs: map[string]error{"prices-next": errors.New("replacement failed")}}, exists: true}
	svc, _ := queryTestService(engine, false)
	configureDatasetFreshnessView(svc, metrics, "prices-next", "prices-next")
	if err := svc.applyDatasetEvent(context.Background(), "space", "market_prices", []*pb.RowFieldUpsert{viewFreshnessRow("BTC-USDT", "1m", "venue:binance", "2026-09-08T10:05:00Z")}); err == nil {
		t.Fatal("replacement write unexpectedly succeeded")
	}
	assertViewDatasetInputMetric(t, registry, "BTC-USDT", "1m", "venue:binance", "2026-09-08T10:05:00Z")
	assertNoViewDatasetOutputMetric(t, registry)

	engine.writeErrs = nil
	engine.writeErrs = map[string]error{"prices-index": errors.New("active failed")}
	configureDatasetFreshnessView(svc, metrics, "prices-index", "")
	if err := svc.applyDatasetEvent(context.Background(), "space", "market_prices", []*pb.RowFieldUpsert{viewFreshnessRow("BTC-USDT", "1m", "venue:binance", "2026-09-08T10:05:00Z")}); err == nil {
		t.Fatal("active write unexpectedly succeeded")
	}
	assertViewDatasetInputMetric(t, registry, "BTC-USDT", "1m", "venue:binance", "2026-09-08T10:05:00Z")
	assertNoViewDatasetOutputMetric(t, registry)

	engine.writeErrs = nil
	configureDatasetFreshnessView(svc, metrics, "prices-index", "")
	if err := svc.applyDatasetEvent(context.Background(), "space", "market_prices", []*pb.RowFieldUpsert{viewFreshnessRow("", "1m", "venue:binance", "2026-09-08T10:05:00Z")}); err != nil {
		t.Fatal(err)
	}
	assertNoEmptySubjectDatasetMetric(t, registry)
}

func configureDatasetFreshnessView(svc *Service, metrics *observability.ViewMetrics, indexID, nextID string) {
	key := viewRef{spaceID: "space", viewID: "prices_view"}
	svc.metrics = metrics
	svc.catalogViews = map[viewRef]*pb.View{key: {SpaceId: "space", ViewId: "prices_view", FilterJson: `{"freq":"1m"}`}}
	svc.views[key] = &viewRuntime{active: "prices-index", next: nextID, status: "active"}
	if indexID == nextID {
		svc.views[key].active = ""
		svc.views[key].status = "building"
	}
	svc.indexView = map[string]viewRef{"prices-index": key, indexID: key}
	svc.indexEngine[indexID] = "query-test"
	svc.schemas[indexID] = viewindex.ViewIndexSchema{SpaceID: "space", ViewID: "prices_view", PrimaryDatasetID: "market_prices", Columns: []*pb.ViewColumn{{OriginId: "market_prices.close", ColumnName: "close"}}}
	svc.byData = map[datasetRef]map[string]struct{}{{spaceID: "space", datasetID: "market_prices"}: {indexID: {}}}
}

func viewFreshnessRow(subject, frequency, seriesTag, dataTime string) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "market_prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, Freq: frequency, SeriesTag: seriesTag, DataTime: dataTime}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}},
	}
}

func assertViewDatasetMetric(t *testing.T, registry *prometheus.Registry, subject, frequency, seriesTag, dataTime string) {
	t.Helper()
	want := map[string]string{"space_id": "space", "view_id": "prices_view", "dataset_id": "market_prices", "subject_id": subject, "freq": frequency, "series_tag": seriesTag}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "moox_storage_view_dataset_output_last_data_time_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := make(map[string]string, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if reflect.DeepEqual(labels, want) {
				got := time.Unix(int64(metric.GetGauge().GetValue()), 0).UTC().Format(time.RFC3339)
				if got != dataTime {
					t.Fatalf("subject=%s freshness=%s, want %s", subject, got, dataTime)
				}
				return
			}
		}
	}
	t.Fatalf("view dataset metric labels=%v not found", want)
}

func assertNoViewDatasetMetric(t *testing.T, registry *prometheus.Registry) {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if (family.GetName() == "moox_storage_view_dataset_input_last_data_time_seconds" || family.GetName() == "moox_storage_view_dataset_output_last_data_time_seconds") && len(family.GetMetric()) != 0 {
			t.Fatalf("unexpected view dataset metrics: %v", family)
		}
	}
}

func assertViewDatasetInputMetric(t *testing.T, registry *prometheus.Registry, subject, frequency, seriesTag, dataTime string) {
	t.Helper()
	want := map[string]string{"space_id": "space", "view_id": "prices_view", "dataset_id": "market_prices", "subject_id": subject, "freq": frequency, "series_tag": seriesTag}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "moox_storage_view_dataset_input_last_data_time_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := make(map[string]string, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if reflect.DeepEqual(labels, want) {
				got := time.Unix(int64(metric.GetGauge().GetValue()), 0).UTC().Format(time.RFC3339)
				if got != dataTime {
					t.Fatalf("subject=%s input freshness=%s, want %s", subject, got, dataTime)
				}
				return
			}
		}
	}
	t.Fatalf("view dataset input metric labels=%v not found", want)
}

func assertNoViewDatasetOutputMetric(t *testing.T, registry *prometheus.Registry) {
	t.Helper()
	for _, family := range mustGather(t, registry) {
		if family.GetName() == "moox_storage_view_dataset_output_last_data_time_seconds" && len(family.GetMetric()) != 0 {
			t.Fatalf("unexpected view dataset output metrics: %v", family)
		}
	}
}

func assertNoEmptySubjectDatasetMetric(t *testing.T, registry *prometheus.Registry) {
	t.Helper()
	for _, family := range mustGather(t, registry) {
		if family.GetName() != "moox_storage_view_dataset_input_last_data_time_seconds" && family.GetName() != "moox_storage_view_dataset_output_last_data_time_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "subject_id" && label.GetValue() == "" {
					t.Fatalf("unexpected empty subject metric: %v", family)
				}
			}
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

func TestReplacementFailurePersistenceRetriesBeforeAcknowledgingRedelivery(t *testing.T) {
	engine := &existenceQueryEngine{queryEngine: &queryEngine{writeErrs: map[string]error{"prices-b": errors.New("replacement write failed")}}, exists: true}
	svc, _ := queryTestService(engine, false)
	svc.engines["query-test"] = engine
	key := viewRef{spaceID: "space", viewID: "prices"}
	svc.views[key] = &viewRuntime{}
	svc.indexView = map[string]viewRef{"prices-a": key, "prices-b": key}
	svc.indexEngine = map[string]string{"prices-a": "query-test", "prices-b": "query-test"}
	svc.schemas["prices-a"] = viewindex.ViewIndexSchema{SpaceID: "space", ViewID: "prices", PrimaryDatasetID: "market", Columns: []*pb.ViewColumn{{OriginId: "market.close", ColumnName: "close"}}}
	svc.schemas["prices-b"] = svc.schemas["prices-a"]
	svc.byData = map[datasetRef]map[string]struct{}{
		{spaceID: "space", datasetID: "market"}: {"prices-a": {}, "prices-b": {}},
	}
	metadata := &maintenanceMetadata{view: &pb.View{SpaceId: "space", ViewId: "prices", ActiveIndexId: "prices-a"}, failErr: errors.New("metadata temporarily unavailable")}
	runtime := svc.views[key]
	runtime.active = "prices-a"
	runtime.next = "prices-b"
	runtime.status = "building"
	runtime.buildID = "build-1"
	runtime.ownerID = "owner-1"
	runtime.metadata = metadata
	runtime.metadataAuth = &pb.AuthInfo{AppId: "view"}
	row := &pb.RowFieldUpsert{
		Key:    timeSeriesTestRowKey("venue:binance"),
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}},
	}

	if err := svc.applyDatasetEvent(context.Background(), "space", "market", []*pb.RowFieldUpsert{row}); err == nil {
		t.Fatal("first replacement failure was ACKed")
	}
	if !runtime.buildFailed || runtime.next != "prices-b" || metadata.failCalls != 1 {
		t.Fatalf("after transient metadata failure: failed=%v next=%q fail_calls=%d", runtime.buildFailed, runtime.next, metadata.failCalls)
	}
	engine.queryEngine.writeErrs["prices-b"] = nil
	if err := svc.applyDatasetEvent(context.Background(), "space", "market", []*pb.RowFieldUpsert{row}); err == nil {
		t.Fatal("redelivery after persisting failure was ACKed")
	}
	if runtime.buildFailed || runtime.next != "" || metadata.failCalls != 2 {
		t.Fatalf("failed replacement was not cleared: failed=%v next=%q fail_calls=%d", runtime.buildFailed, runtime.next, metadata.failCalls)
	}
	if err := svc.applyDatasetEvent(context.Background(), "space", "market", []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatalf("post-cleanup redelivery did not ACK: %v", err)
	}
}

func TestFailedBuildCleanupDoesNotRemoveReusedSlotGeneration(t *testing.T) {
	engine := &queryEngine{}
	svc, _ := queryTestService(engine, false)
	key := viewRef{spaceID: "space", viewID: "prices"}
	svc.indexView = make(map[string]viewRef)
	svc.indexEngine = make(map[string]string)
	svc.schemas = make(map[string]viewindex.ViewIndexSchema)
	svc.indexGeneration = make(map[string]uint64)
	svc.indexView["prices-b"] = key
	svc.indexEngine["prices-b"] = "query-test"
	svc.schemas["prices-b"] = viewindex.ViewIndexSchema{SpaceID: "space", ViewID: "prices", PrimaryDatasetID: "market"}
	svc.indexGeneration["prices-b"] = 2
	svc.removeFailedBuildAtGeneration(context.Background(), "prices-b", 1)
	if _, ok := svc.indexView["prices-b"]; !ok {
		t.Fatal("stale cleanup removed a newer slot generation")
	}
}

func TestQueryTimeSeriesRowsPreservesSelectorPresenceAndExactResultTags(t *testing.T) {
	engine := &queryEngine{}
	svc, auth := queryTestService(engine, true)

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
				AuthInfo: auth,
				SpaceId:  "space",
				ViewId:   "prices",
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
	svc, auth := queryTestService(engine, true)
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
				AuthInfo: auth, SpaceId: "space", ViewId: "prices",
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
		{name: "no active view", rows: []*pb.RowFieldValues{{Key: timeSeriesTestRowKey("")}}, stats: validStats},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := &queryEngine{rows: tc.rows, stats: tc.stats, statErr: tc.statErr}
			svc, auth := queryTestService(engine, tc.active)
			req := &pb.QueryTimeSeriesRowsReq{
				AuthInfo: auth, SpaceId: "space", ViewId: "prices",
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
			if tc.name == "valid coverage" && (rsp.GetServedIndexedFrom() != validStats.IndexedFrom || rsp.GetServedIndexedTo() != validStats.IndexedTo) {
				t.Fatalf("coverage fields not returned: %v", rsp)
			}
		})
	}
}

func TestQueryTimeSeriesRowsRejectsActiveIndexMismatchBeforeQuery(t *testing.T) {
	engine := &queryEngine{}
	svc, auth := queryTestService(engine, true)
	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
		AuthInfo: auth, SpaceId: "space", ViewId: "prices", ExpectedActiveIndexId: "prices-old",
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
	svc, auth := queryTestService(engine, true)
	svc.indexRevision = map[string]uint64{"prices-index": 7}
	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
		AuthInfo: auth, SpaceId: "space", ViewId: "prices", ExpectedActiveIndexId: "prices-index", ExpectedActiveIndexRevision: 6,
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
	svc, auth := queryTestService(engine, true)
	runtime := svc.views[viewRef{spaceID: "space", viewID: "prices"}]
	runtime.statsIndexID = "prices-index"
	runtime.stats = stats

	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
		AuthInfo: auth, SpaceId: "space", ViewId: "prices", TotalMode: pb.TotalMode_NONE,
		Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
		TimeRange: &pb.TimeRange{StartTime: "2026-07-29T00:00:00Z", EndTime: "2026-07-29T00:01:00Z"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
	if engine.statCalls != 0 {
		t.Fatalf("total_mode=NONE called Stat %d times", engine.statCalls)
	}
	if !rsp.GetComplete() || rsp.GetServedIndexedTo() != stats.IndexedTo {
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
	svc, auth := queryTestService(engine, true)
	runtime := svc.views[viewRef{spaceID: "space", viewID: "prices"}]
	runtime.statsIndexID = "prices-index"
	runtime.stats = stats

	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
		AuthInfo: auth, SpaceId: "space", ViewId: "prices",
		Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
		TimeRange: &pb.TimeRange{StartTime: "2026-07-29T00:00:00Z", EndTime: "2026-07-29T00:01:00Z"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
	if engine.statCalls != 1 || rsp.GetComplete() || rsp.GetServedIndexedTo() != "" {
		t.Fatalf("completeness query used stale runtime stats: %v", rsp)
	}
}

func TestQueryTimeSeriesRowsActiveIndexRemainsCompleteDuringRebuild(t *testing.T) {
	engine := &queryEngine{
		rows: []*pb.RowFieldValues{{Key: timeSeriesTestRowKey("")}},
		stats: viewindex.ViewIndexStats{
			Exists: true, EntryCount: 1,
			IndexedFrom: "2026-07-29T00:00:00Z",
			IndexedTo:   "2026-07-29T00:00:00Z",
		},
	}
	svc, auth := queryTestService(engine, true)
	runtime := svc.views[viewRef{spaceID: "space", viewID: "prices"}]
	runtime.status = "building"
	runtime.next = "prices-next"

	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
		AuthInfo: auth, SpaceId: "space", ViewId: "prices",
		Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
		TimeRange: &pb.TimeRange{
			StartTime: "2026-07-29T00:00:00Z",
			EndTime:   "2026-07-29T00:00:00.000000001Z",
		},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
	if !rsp.GetComplete() {
		t.Fatalf("active index became incomplete while next index builds: %v", rsp)
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
			svc, auth := queryTestService(engine, true)
			rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
				AuthInfo: auth, SpaceId: "space", ViewId: "prices",
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

func queryTestService(engine viewindex.Engine, active bool) (*Service, *pb.AuthInfo) {
	const secret = "view-secret"
	svc := &Service{
		authSecret:  secret,
		engines:     map[string]viewindex.Engine{"query-test": engine},
		indexEngine: map[string]string{"prices-index": "query-test", "prices": "query-test"},
		schemas: map[string]viewindex.ViewIndexSchema{
			"prices-index": {SpaceID: "space", ViewID: "prices", PrimaryDatasetID: "market"},
			"prices":       {SpaceID: "space", ViewID: "prices", PrimaryDatasetID: "market"},
		},
		views: map[viewRef]*viewRuntime{},
	}
	if active {
		svc.views[viewRef{spaceID: "space", viewID: "prices"}] = &viewRuntime{active: "prices-index", status: "active"}
	}
	auth := &pb.AuthInfo{AppId: "query-test", AppKey: datanode.ServiceAuthKey(secret, "query-test")}
	return svc, auth
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
