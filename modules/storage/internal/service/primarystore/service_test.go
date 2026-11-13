package primarystore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPrimaryWritesAndRequiresAuth(t *testing.T) {
	svc, err := New(Options{Snapshot: activeRequestSnapshot, Store: openTestStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	rsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "ignored"}, Rows: []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.2}}}}}}}))
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	bad, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{Rows: []*pb.RowFieldUpsert{{Key: key}}}))
	if err != nil || bad.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("bad rsp=%v err=%v", bad, err)
	}
}

// 元数据里没有或已停用的数据集，读写都在到达存储层之前被拒绝。
func TestPrimaryRejectsMissingOrDisabledDataset(t *testing.T) {
	for name, dataset := range map[string]*pb.Dataset{
		"missing":  nil,
		"disabled": {SpaceId: "s", DatasetId: "d", Status: "disabled"},
	} {
		t.Run(name, func(t *testing.T) {
			store := newHookStore(t)
			store.upsert = func(context.Context, string, string, string, []*pb.RowFieldUpsert) (bool, error) {
				t.Fatal("write reached the store")
				return false, nil
			}
			svc, err := New(Options{Store: store, Snapshot: func() metadata.RequestSnapshot { return singleDatasetSnapshot{dataset: dataset} }})
			require.NoError(t, err)
			key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
			rsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{AuthInfo: &pb.AuthInfo{AppId: "caller"}, Rows: []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{{FieldId: "v", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "x"}}}}}}}))
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
			// 读取按错误类别返回：不存在是 NOT_FOUND，已停用是参数错误，都不走框架错误。
			read, err := svc.ReadRows(context.Background(), &pb.PrimaryReadRowsReq{AuthInfo: &pb.AuthInfo{AppId: "caller"}, Keys: []*pb.RowKey{key}, ColumnNames: []string{"v"}})
			require.NoError(t, err)
			want := pb.ErrorCode_NOT_FOUND
			if dataset != nil {
				want = pb.ErrorCode_INVALID_PARAM
			}
			require.Equal(t, want, read.GetRetInfo().GetCode())
		})
	}
}

func TestPrimaryWriteMethodsStillAllowInternalCallers(t *testing.T) {
	store := openTestStore(t)
	svc, err := New(Options{Store: store, Snapshot: func() metadata.RequestSnapshot { return markerRoutingSnapshot{} }})
	if err != nil {
		t.Fatal(err)
	}
	row := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "allowed"}}}},
	}
	upsert, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "collector"}, Rows: []*pb.RowFieldUpsert{row},
	}))
	if err != nil || upsert.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("upsert rsp=%v err=%v", upsert, err)
	}
	completed, err := svc.ReportDatasetPeriodCompleted(context.Background(), &pb.ReportDatasetPeriodCompletedReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "space", Marker: &pb.DatasetPeriodCompletedMarker{
			DatasetId: "factor-data", Frequency: "1m", PeriodTime: 1786032000, Status: "complete", UniverseSubjectIds: []string{"BTC"},
			CauseEventId: "source-ready", CompletedAt: timestamppb.New(time.Unix(1786032001, 0)),
		},
	})
	if err != nil || completed.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("completed rsp=%v err=%v", completed, err)
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1, "Outbox 只有周期完成，行写入不进 Outbox")
}

type factorResultSnapshot struct{}

type markerRoutingSnapshot struct{}

func (markerRoutingSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	attrs := map[string]string{}
	switch datasetID {
	case "collector-data":
		attrs = map[string]string{"owner_module": "collector", "dataset_role": "raw_collection"}
	case "resample-data":
		attrs = map[string]string{"owner_module": "collector", "dataset_role": "kline_resample_result"}
	case "factor-data":
		attrs = map[string]string{"owner_module": "factor", "dataset_role": "factor_result"}
	}
	return &pb.Dataset{SpaceId: spaceID, DatasetId: datasetID, Status: "active", Alignment: attrs["alignment"], Attributes: attrs}, true
}

func (markerRoutingSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (factorResultSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	return &pb.Dataset{
		SpaceId: spaceID, DatasetId: datasetID, Status: "active",
		Attributes: map[string]string{"owner_module": "factor", "dataset_role": "factor_result"},
	}, true
}

func (factorResultSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

type periodAuthorizationSnapshot struct {
	datasets map[routeKey]*pb.Dataset
}

func collectorPeriodMetadataSnapshot(spaceID, datasetID, frequency string) metadata.RequestSnapshot {
	return periodAuthorizationSnapshot{datasets: map[routeKey]*pb.Dataset{
		{spaceID: spaceID, datasetID: datasetID}: {
			SpaceId: spaceID, DatasetId: datasetID, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Status: "active",
			Freq: frequency, Attributes: map[string]string{"owner_module": "collector", "dataset_role": "raw_collection"},
		},
	}}
}

func (s periodAuthorizationSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	dataset, ok := s.datasets[routeKey{spaceID: spaceID, datasetID: datasetID}]
	return dataset, ok
}

func (periodAuthorizationSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func TestCommitTimeSeriesBatchWriteAuthorization(t *testing.T) {
	runPeriodAuthorizationCases(t, "commit", func(svc *Service, auth *pb.AuthInfo, exp *pb.DatasetPeriodExpectation) *pb.RetInfo {
		row := &pb.RowFieldUpsert{Key: &pb.RowKey{
			SpaceId: exp.GetSpaceId(), DatasetId: exp.GetDatasetId(),
			Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: exp.GetFrequency(), DataTime: "2026-09-30T00:00:00Z"}},
		}}
		rsp, err := svc.CommitTimeSeriesBatch(context.Background(), &pb.PrimaryCommitTimeSeriesBatchReq{
			AuthInfo: auth, Expectation: exp, Items: []*pb.TimeSeriesBatchRow{{SeriesIndex: 0, Row: row}},
		})
		if err != nil {
			t.Fatalf("CommitTimeSeriesBatch error: %v", err)
		}
		return rsp.GetRetInfo()
	})
}

func runPeriodAuthorizationCases(t *testing.T, method string, invoke func(*Service, *pb.AuthInfo, *pb.DatasetPeriodExpectation) *pb.RetInfo) {
	t.Helper()
	validDataset := func() *pb.Dataset {
		return &pb.Dataset{
			SpaceId: "space", DatasetId: "dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1h", Status: "active",
			Attributes: map[string]string{"owner_module": "collector", "dataset_role": "raw_collection"},
		}
	}
	withAttrs := func(kind pb.DataKind, freq string, attrs map[string]string) *pb.Dataset {
		return &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataKind: kind, Freq: freq, Status: "active", Attributes: attrs}
	}
	tests := []struct {
		name            string
		appID           string
		dataset         *pb.Dataset
		includeDataset  bool
		includeSnapshot bool
		frequency       string
		authorizeErr    error
		wantCode        pb.ErrorCode
	}{
		{name: "collector owner", appID: "collector", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_SUCCESS},
		{name: "any authorized caller and any role: SCF canary read-only caller", appID: "scf-market-canary", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_SUCCESS},
		{name: "any authorized caller and any role: Factor caller", appID: "moox-factor", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_SUCCESS},
		{name: "any authorized caller and any role: other module caller", appID: "admin", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_SUCCESS},
		{name: "any authorized caller and any role: Factor-owned Dataset", appID: "collector", dataset: withAttrs(pb.DataKind_DATA_KIND_TIME_SERIES, "1h", map[string]string{"owner_module": "factor", "dataset_role": "factor_result"}), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_SUCCESS},
		{name: "any authorized caller and any role: missing Dataset owner", appID: "collector", dataset: withAttrs(pb.DataKind_DATA_KIND_TIME_SERIES, "1h", map[string]string{"dataset_role": "raw_collection"}), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_SUCCESS},
		{name: "any authorized caller and any role: wrong Dataset role", appID: "collector", dataset: withAttrs(pb.DataKind_DATA_KIND_TIME_SERIES, "1h", map[string]string{"owner_module": "collector", "dataset_role": "factor_result"}), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_SUCCESS},
		{name: "snapshot Dataset identity mismatch", appID: "collector", dataset: &pb.Dataset{SpaceId: "other-space", DatasetId: "dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1h", Status: "active"}, includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "record Dataset kind", appID: "collector", dataset: withAttrs(pb.DataKind_DATA_KIND_RECORD, "1h", nil), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "invalid declared frequency", appID: "collector", dataset: withAttrs(pb.DataKind_DATA_KIND_TIME_SERIES, "forever", nil), includeDataset: true, includeSnapshot: true, frequency: "forever", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "frequency alias is not canonical", appID: "collector", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "1H", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "unknown Dataset", appID: "collector", dataset: validDataset(), includeDataset: false, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "missing metadata snapshot", appID: "collector", dataset: validDataset(), includeDataset: true, includeSnapshot: false, frequency: "1h", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "HMAC rejects before metadata", appID: "collector", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "1h", authorizeErr: errors.New("invalid HMAC"), wantCode: pb.ErrorCode_NO_PERMISSION},
	}
	for _, tc := range tests {
		t.Run(method+"/"+tc.name, func(t *testing.T) {
			var commits, authorizerCalls, snapshotCalls int
			store := newHookStore(t)
			store.commit = func(context.Context, pebble.DatasetPeriodExpectation, []pebble.TimeSeriesBatchItem) (pebble.DatasetPeriodResult, error) {
				commits++
				return pebble.DatasetPeriodResult{Status: "waiting"}, nil
			}
			svc, err := New(Options{
				Store: store,
				Snapshot: func() metadata.RequestSnapshot {
					snapshotCalls++
					if !tc.includeSnapshot {
						return nil
					}
					datasets := map[routeKey]*pb.Dataset{}
					if tc.includeDataset {
						datasets[routeKey{spaceID: "space", datasetID: "dataset"}] = tc.dataset
					}
					return periodAuthorizationSnapshot{datasets: datasets}
				},
				Authorizer: func(auth *pb.AuthInfo) error {
					authorizerCalls++
					if auth == nil || auth.GetAppId() == "" || auth.GetAppKey() == "" {
						return errors.New("auth_info is invalid")
					}
					return tc.authorizeErr
				},
			})
			require.NoError(t, err)
			expectation := &pb.DatasetPeriodExpectation{SpaceId: "space", DatasetId: "dataset", Frequency: tc.frequency, PeriodTime: 123, SeriesHash: "hash", ExpectedCount: 1}
			rsp := invoke(svc, &pb.AuthInfo{AppId: tc.appID, AppKey: "caller-key"}, expectation)
			require.Equal(t, tc.wantCode, rsp.GetCode(), "ret_info=%v", rsp)
			require.Equal(t, 1, authorizerCalls, "ordinary HMAC authorization must run first")
			wantSnapshotCalls := 1
			if tc.authorizeErr != nil {
				wantSnapshotCalls = 0
			}
			require.Equal(t, wantSnapshotCalls, snapshotCalls, "unexpected request metadata snapshot access")
			if tc.wantCode == pb.ErrorCode_SUCCESS {
				require.Equal(t, 1, commits)
			} else {
				require.Zero(t, commits, "unauthorized period RPC reached the Store")
			}
		})
	}
}

func TestPrimaryWritesSameDatasetInDifferentSpacesSeparately(t *testing.T) {
	var written []string
	store := newHookStore(t)
	store.upsert = func(ctx context.Context, spaceID, datasetID, commitID string, rows []*pb.RowFieldUpsert) (bool, error) {
		written = append(written, spaceID+"/"+datasetID)
		return store.Store.UpsertFields(ctx, spaceID, datasetID, commitID, rows)
	}
	svc, err := New(Options{Snapshot: activeRequestSnapshot, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	row := func(space, record string) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{
			Key:    &pb.RowKey{SpaceId: space, DatasetId: "shared", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}},
			Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: record}}}},
		}
	}
	// 一次请求只写一个数据集：两个空间里的同名数据集各写一次。
	for _, item := range [][2]string{{"space-a", "a"}, {"space-b", "b"}} {
		rsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
			AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
			Rows:     []*pb.RowFieldUpsert{row(item[0], item[1])},
		}))
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("write %s rsp=%v err=%v", item[0], rsp, err)
		}
	}
	if !reflect.DeepEqual(written, []string{"space-a/shared", "space-b/shared"}) {
		t.Fatalf("written=%v", written)
	}
}

type recordingDatasetMetrics struct {
	observations []report.DatasetObservation
}

func (m *recordingDatasetMetrics) ObserveRun(observation report.DatasetObservation) error {
	m.observations = append(m.observations, observation)
	return nil
}

func TestPrimaryObservesCommittedTimeSeriesWatermarkByFrequency(t *testing.T) {
	metrics := &recordingDatasetMetrics{}
	svc, err := New(Options{Snapshot: activeRequestSnapshot, Store: openTestStore(t), DatasetMetrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	row := func(freq string, at time.Time) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{
			Key: &pb.RowKey{
				SpaceId: "crypto", DatasetId: "market_kline",
				Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
					SubjectId: "BTC-USDT", Freq: freq, DataTime: at.UTC().Format(time.RFC3339Nano),
				}},
			},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{
				Value: &pb.TypedValue_DoubleValue{DoubleValue: 1},
			}}},
		}
	}
	t1003 := time.Date(2026, 7, 28, 10, 3, 0, 0, time.UTC)
	t1005 := t1003.Add(2 * time.Minute)
	rsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "test", AppKey: "test"},
		Rows:     []*pb.RowFieldUpsert{row("1m", t1003), row("1m", t1005), row("5m", t1003)},
	}))
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	if len(metrics.observations) != 2 {
		t.Fatalf("observations=%v", metrics.observations)
	}
	got := map[string]report.DatasetObservation{}
	for _, observation := range metrics.observations {
		got[observation.Key.Freq] = observation
	}
	if got["1m"].Rows != 2 || !got["1m"].OutputWatermark.Equal(t1005) || got["1m"].Result != "success" {
		t.Fatalf("1m observation=%+v", got["1m"])
	}
	if got["5m"].Rows != 1 || !got["5m"].OutputWatermark.Equal(t1003) || got["5m"].Result != "success" {
		t.Fatalf("5m observation=%+v", got["5m"])
	}
}

func TestPrimaryWriteFailureReportsErrorWithoutWatermark(t *testing.T) {
	store := newHookStore(t)
	store.upsert = func(context.Context, string, string, string, []*pb.RowFieldUpsert) (bool, error) {
		return false, errors.New("write failed")
	}
	metrics := &recordingDatasetMetrics{}
	svc, err := New(Options{Snapshot: activeRequestSnapshot, Store: store, DatasetMetrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 28, 10, 5, 0, 0, time.UTC)
	rsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "test", AppKey: "test"},
		Rows: []*pb.RowFieldUpsert{{
			Key: &pb.RowKey{
				SpaceId: "crypto", DatasetId: "market_kline",
				Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
					SubjectId: "BTC-USDT", Freq: "1m", DataTime: at.Format(time.RFC3339Nano),
				}},
			},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{
				Value: &pb.TypedValue_DoubleValue{DoubleValue: 1},
			}}},
		}}}))
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INNER_ERR {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	if len(metrics.observations) != 1 {
		t.Fatalf("observations=%v", metrics.observations)
	}
	observation := metrics.observations[0]
	if observation.Result != "error" || !observation.OutputWatermark.IsZero() {
		t.Fatalf("observation=%+v", observation)
	}
}

func TestPrimaryDoesNotObserveRecordDataset(t *testing.T) {
	metrics := &recordingDatasetMetrics{}
	svc, err := New(Options{Snapshot: activeRequestSnapshot, Store: openTestStore(t), DatasetMetrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "test", AppKey: "test"},
		Rows: []*pb.RowFieldUpsert{{
			Key: &pb.RowKey{
				SpaceId: "crypto", DatasetId: "symbols",
				Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "BTC-USDT", Version: "1"}},
			},
			Fields: []*pb.FieldValue{{FieldId: "symbol", Value: &pb.TypedValue{
				Value: &pb.TypedValue_StringValue{StringValue: "BTC-USDT"},
			}}},
		}},
	}))
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	if len(metrics.observations) != 0 {
		t.Fatalf("record dataset observations=%v", metrics.observations)
	}
}

func TestPrimaryReadPreservesRequestOrderAcrossDatasets(t *testing.T) {
	store := newHookStore(t)
	store.readRows = func(_ context.Context, keys []*pb.RowKey, _, _ []string) ([]*pb.RowFieldValues, error) {
		rows := make([]*pb.RowFieldValues, 0, len(keys))
		for _, key := range keys {
			rows = append(rows, &pb.RowFieldValues{Key: key})
		}
		return rows, nil
	}
	svc, err := New(Options{Snapshot: activeRequestSnapshot, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	key := func(dataset, record string) *pb.RowKey {
		return &pb.RowKey{SpaceId: "s", DatasetId: dataset, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}}
	}
	keys := []*pb.RowKey{key("b", "first"), key("a", "second"), key("b", "third")}
	rsp, err := svc.ReadRows(context.Background(), &pb.PrimaryReadRowsReq{
		AuthInfo:    &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Keys:        keys,
		ColumnNames: []string{"value"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("read rsp=%v err=%v", rsp, err)
	}
	for i, row := range rsp.GetRows() {
		if row.GetKey().GetRecord().GetRecordId() != keys[i].GetRecord().GetRecordId() {
			t.Fatalf("row %d=%v want=%v", i, row.GetKey(), keys[i])
		}
	}
}

func TestPrimaryReadRowsUsesOneSnapshotAcrossDatasetGroups(t *testing.T) {
	provider := &mutableSnapshotProvider{current: &testRequestSnapshot{generation: "generation-a"}}
	var generations []string
	store := newHookStore(t)
	store.readRows = func(ctx context.Context, keys []*pb.RowKey, _, _ []string) ([]*pb.RowFieldValues, error) {
		snapshot, ok := metadata.RequestSnapshotFromContext(ctx).(*testRequestSnapshot)
		if !ok || snapshot == nil {
			t.Fatalf("request snapshot missing for %s", keys[0].GetDatasetId())
		}
		generations = append(generations, snapshot.generation)
		return []*pb.RowFieldValues{{Key: keys[0]}}, nil
	}
	svc, err := New(Options{Store: store, Snapshot: provider.RequestSnapshot})
	if err != nil {
		t.Fatal(err)
	}
	key := func(dataset, record string) *pb.RowKey {
		return &pb.RowKey{SpaceId: "space", DatasetId: dataset, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}}
	}
	rsp, err := svc.ReadRows(context.Background(), &pb.PrimaryReadRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Keys:     []*pb.RowKey{key("dataset-a", "a"), key("dataset-b", "b")}, ColumnNames: []string{"value"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("read rsp=%v err=%v", rsp, err)
	}
	if provider.calls != 1 {
		t.Fatalf("snapshot provider calls=%d want 1", provider.calls)
	}
	if !reflect.DeepEqual(generations, []string{"generation-a", "generation-a"}) {
		t.Fatalf("generations=%v", generations)
	}
}

func TestPrimaryRejectsRecordWriteWithoutVersion(t *testing.T) {
	store := newHookStore(t)
	store.upsert = func(context.Context, string, string, string, []*pb.RowFieldUpsert) (bool, error) {
		t.Fatal("write should not reach the store")
		return false, nil
	}
	svc, err := New(Options{Snapshot: activeRequestSnapshot, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Rows: []*pb.RowFieldUpsert{{
			Key:    &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r"}}},
			Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "x"}}}},
		}},
	}))
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func TestPrimaryReadRowsReturnsResolvedLatestRecordVersion(t *testing.T) {
	svc, err := New(Options{Snapshot: activeRequestSnapshot, Store: openTestStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1", "2"} {
		key := &pb.RowKey{SpaceId: "space", DatasetId: "records", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: version}}}
		rsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
			AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
			Rows: []*pb.RowFieldUpsert{{
				Key: key,
				Fields: []*pb.FieldValue{{
					FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: version}},
				}},
			}},
		}))
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("write version=%s rsp=%v err=%v", version, rsp, err)
		}
	}
	rsp, err := svc.ReadRows(context.Background(), &pb.PrimaryReadRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Keys: []*pb.RowKey{{
			SpaceId: "space", DatasetId: "records",
			Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r"}},
		}},
		ColumnNames: []string{"value"},
	})
	if err != nil || len(rsp.GetRows()) != 1 || rsp.GetRows()[0].GetKey().GetRecord().GetVersion() != "2" {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func TestPrimaryReadRowsRoundTripsThroughPebble(t *testing.T) {
	svc, err := New(Options{Snapshot: activeRequestSnapshot, Store: openTestStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	key := &pb.TimeSeriesKey{
		SpaceId: "crypto", DatasetId: "dataset_spot_kline_1h", SubjectId: "BTC-USDT", Freq: "1h",
		DataTime: "2026-07-29T15:00:00Z", SeriesTag: "venue:binance",
	}
	rowKey := timeSeriesRowKey(key)
	writeRsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Rows: []*pb.RowFieldUpsert{{
			Key: rowKey,
			Fields: []*pb.FieldValue{{
				FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 101.25}},
			}},
		}},
	}))
	if err != nil || writeRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", writeRsp, err)
	}
	readRsp, err := svc.ReadRows(context.Background(), &pb.PrimaryReadRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Keys:     []*pb.RowKey{rowKey}, ColumnNames: []string{"close"},
	})
	if err != nil || readRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS ||
		len(readRsp.GetRows()) != 1 || readRsp.GetRows()[0].GetKey().GetTimeSeries().GetSeriesTag() != "venue:binance" ||
		len(readRsp.GetRows()[0].GetFields()) != 1 ||
		readRsp.GetRows()[0].GetFields()[0].GetValue().GetDoubleValue() != 101.25 {
		t.Fatalf("read rsp=%v err=%v", readRsp, err)
	}
}

type mutableSnapshotProvider struct {
	current metadata.RequestSnapshot
	calls   int
}

func (p *mutableSnapshotProvider) RequestSnapshot() metadata.RequestSnapshot {
	p.calls++
	snapshot := p.current
	p.current = &testRequestSnapshot{generation: "generation-b"}
	return snapshot
}

type testRequestSnapshot struct {
	activeSnapshot
	generation string
}

type attrsSnapshot map[string]map[string]string

func (s attrsSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	attrs, ok := s[datasetID]
	if !ok {
		return nil, false
	}
	return &pb.Dataset{SpaceId: spaceID, DatasetId: datasetID, Status: "active", Alignment: attrs["alignment"], Attributes: attrs}, true
}

func (attrsSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func timeSeriesRowKey(key *pb.TimeSeriesKey) *pb.RowKey {
	return &pb.RowKey{
		SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(),
		Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), DataTime: key.GetDataTime(), SeriesTag: key.GetSeriesTag(),
		}},
	}
}
