package view

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type queryEngine struct {
	spec      viewindex.QuerySpec
	rows      []*pb.RowFieldValues
	stats     viewindex.ViewIndexStats
	statErr   error
	calls     int
	statCalls int
}

func (*queryEngine) Engine() string { return "query-test" }
func (*queryEngine) Prepare(context.Context, string, viewindex.ViewIndexSchema) error {
	return nil
}
func (*queryEngine) Write(context.Context, string, viewindex.ViewIndexWriteBatch) error {
	return nil
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
		{name: "empty rows", active: true, stats: validStats},
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
