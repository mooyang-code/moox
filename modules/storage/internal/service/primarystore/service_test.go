package primarystore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

// 元数据里没有或已停用的数据集，读写都在到达存储层之前被拒绝。
func TestPrimaryRejectsMissingOrDisabledDataset(t *testing.T) {
	for name, tc := range map[string]struct {
		dataset  *pb.Dataset
		wantCode pb.ErrorCode
	}{
		"missing":  {nil, pb.ErrorCode_NOT_FOUND},
		"disabled": {&pb.Dataset{SpaceId: "s", DatasetId: "d", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "disabled"}, pb.ErrorCode_INVALID_PARAM},
	} {
		t.Run(name, func(t *testing.T) {
			store := newHookStore(t)
			store.upsert = func(context.Context, string, string, []*pb.Row) error {
				t.Fatal("write reached the store")
				return nil
			}
			var datasets []*pb.Dataset
			if tc.dataset != nil {
				datasets = []*pb.Dataset{tc.dataset}
			}
			cache := newTestDatasetCache(t, datasets, []*pb.DatasetColumn{{
				SpaceId: "s", DatasetId: "d", ColumnName: "v", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active",
			}})
			svc, err := New(Options{Store: store, Datasets: cache})
			require.NoError(t, err)
			key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
			rsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{Rows: []*pb.Row{{Key: key, Fields: []*pb.FieldValue{{FieldId: "v", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "x"}}}}}}}))
			require.NoError(t, err)
			require.Equal(t, tc.wantCode, rsp.GetRetInfo().GetCode())
			// 读取同样按错误类别返回：不存在是 NOT_FOUND，已停用是参数错误，都不走框架错误。
			read, err := svc.ReadRows(context.Background(), &pb.PrimaryReadRowsReq{Keys: []*pb.RowKey{key}, ColumnNames: []string{"v"}})
			require.NoError(t, err)
			require.Equal(t, tc.wantCode, read.GetRetInfo().GetCode())
		})
	}
}

func TestPrimaryWriteMethodsStillAllowInternalCallers(t *testing.T) {
	store := openTestStore(t)
	cache := newTestDatasetCache(t, []*pb.Dataset{
		{SpaceId: "space", DatasetId: "dataset", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"},
		{SpaceId: "space", DatasetId: "factor-data", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Frequency: "1m", Status: "active", Attributes: map[string]string{"owner_module": "factor", "dataset_role": "factor_result"}},
	}, []*pb.DatasetColumn{
		{SpaceId: "space", DatasetId: "dataset", ColumnName: "value", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active"},
	})
	svc, err := New(Options{Store: store, Datasets: cache})
	if err != nil {
		t.Fatal(err)
	}
	row := &pb.Row{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "allowed"}}}},
	}
	upsert, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
		Rows: []*pb.Row{row},
	}))
	if err != nil || upsert.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("upsert rsp=%v err=%v", upsert, err)
	}
	completed, err := svc.ReportDatasetPeriodCompleted(context.Background(), &pb.ReportDatasetPeriodCompletedReq{
		SpaceId: "space", Marker: &pb.DatasetPeriodCompletedMarker{
			DatasetId: "factor-data", Frequency: "1m", PeriodTime: 1786032000, Status: "complete", UniverseSubjectIds: []string{"BTC"},
			CauseEventId: "source-ready", CompletedAt: time.Unix(1786032001, 0).UnixMilli(),
		},
	})
	if err != nil || completed.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("completed rsp=%v err=%v", completed, err)
	}
	page, err := store.ReadMarkerChanges(context.Background(), 0, 100)
	require.NoError(t, err)
	require.Len(t, page.Markers, 1, "待转发的只有周期完成，行写入不产生事件")
}

func TestCommitTimeSeriesBatchValidatesPeriodDataset(t *testing.T) {
	runPeriodValidationCases(t, "commit", func(svc *Service, exp *pb.DatasetPeriodExpectation) *pb.RetInfo {
		row := &pb.Row{Key: &pb.RowKey{
			SpaceId: exp.GetSpaceId(), DatasetId: exp.GetDatasetId(),
			Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Frequency: exp.GetFrequency(), DataTime: "2026-09-30T00:00:00Z"}},
		}}
		rsp, err := svc.CommitTimeSeriesBatch(context.Background(), &pb.PrimaryCommitTimeSeriesBatchReq{
			Expectation: exp, Items: []*pb.TimeSeriesBatchRow{{SeriesIndex: 0, Row: row}},
		})
		if err != nil {
			t.Fatalf("CommitTimeSeriesBatch error: %v", err)
		}
		return rsp.GetRetInfo()
	})
}

func TestRegisterSeriesSnapshotValidatesPeriodDataset(t *testing.T) {
	runPeriodValidationCases(t, "register", func(svc *Service, exp *pb.DatasetPeriodExpectation) *pb.RetInfo {
		rsp, err := svc.RegisterSeriesSnapshot(context.Background(), &pb.PrimaryRegisterSeriesSnapshotReq{
			SpaceId: exp.GetSpaceId(), DatasetId: exp.GetDatasetId(), Frequency: exp.GetFrequency(),
			EffectiveFrom: 3600,
			Series:        []*pb.SeriesSnapshotEntry{{SubjectId: "BTC-USDT"}},
		})
		if err != nil {
			t.Fatalf("RegisterSeriesSnapshot error: %v", err)
		}
		return rsp.GetRetInfo()
	})
}

func runPeriodValidationCases(t *testing.T, method string, invoke func(*Service, *pb.DatasetPeriodExpectation) *pb.RetInfo) {
	t.Helper()
	validDataset := func() *pb.Dataset {
		return &pb.Dataset{
			SpaceId: "space", DatasetId: "dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Frequency: "1h", Status: "active",
			Attributes: map[string]string{"owner_module": "collector", "dataset_role": "raw_collection"},
		}
	}
	withAttrs := func(kind pb.DataKind, freq string, attrs map[string]string) *pb.Dataset {
		return &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataKind: kind, Frequency: freq, Status: "active", Attributes: attrs}
	}
	disabledDataset := func() *pb.Dataset {
		ds := validDataset()
		ds.Status = "disabled"
		return ds
	}
	tests := []struct {
		name            string
		dataset         *pb.Dataset
		includeDataset  bool
		includeSnapshot bool
		frequency       string
		wantCode        pb.ErrorCode
	}{
		{name: "collector owner", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_SUCCESS},
		{name: "any role: Factor-owned Dataset", dataset: withAttrs(pb.DataKind_DATA_KIND_TIME_SERIES, "1h", map[string]string{"owner_module": "factor", "dataset_role": "factor_result"}), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_SUCCESS},
		{name: "any role: missing Dataset owner", dataset: withAttrs(pb.DataKind_DATA_KIND_TIME_SERIES, "1h", map[string]string{"dataset_role": "raw_collection"}), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_SUCCESS},
		{name: "any role: wrong Dataset role", dataset: withAttrs(pb.DataKind_DATA_KIND_TIME_SERIES, "1h", map[string]string{"owner_module": "collector", "dataset_role": "factor_result"}), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_SUCCESS},
		{name: "disabled Dataset", dataset: disabledDataset(), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_INVALID_PARAM},
		{name: "record Dataset kind", dataset: withAttrs(pb.DataKind_DATA_KIND_RECORD, "1h", nil), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_INVALID_PARAM},
		{name: "invalid declared frequency", dataset: withAttrs(pb.DataKind_DATA_KIND_TIME_SERIES, "forever", nil), includeDataset: true, includeSnapshot: true, frequency: "forever", wantCode: pb.ErrorCode_INVALID_PARAM},
		{name: "frequency alias is not canonical", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "1H", wantCode: pb.ErrorCode_INVALID_PARAM},
		{name: "frequency does not match declaration", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "5m", wantCode: pb.ErrorCode_INVALID_PARAM},
		{name: "unknown Dataset", dataset: validDataset(), includeDataset: false, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_NOT_FOUND},
		{name: "missing metadata snapshot", dataset: validDataset(), includeDataset: true, includeSnapshot: false, frequency: "1h", wantCode: pb.ErrorCode_INNER_ERR},
	}
	for _, tc := range tests {
		t.Run(method+"/"+tc.name, func(t *testing.T) {
			var writes int
			store := newHookStore(t)
			store.commit = func(context.Context, pebble.DatasetPeriodExpectation, []pebble.TimeSeriesBatchItem) (pebble.DatasetPeriodResult, error) {
				writes++
				return pebble.DatasetPeriodResult{Status: "waiting"}, nil
			}
			store.registerSnapshot = func(context.Context, string, string, string, []seriesset.Series, int64) (pebble.RegisterSnapshotResult, error) {
				writes++
				return pebble.RegisterSnapshotResult{SeriesHash: "hash", ExpectedCount: 1}, nil
			}
			var cache *metacache.DatasetCache
			if tc.includeSnapshot {
				var datasets []*pb.Dataset
				if tc.includeDataset {
					datasets = []*pb.Dataset{tc.dataset}
				}
				cache = newTestDatasetCache(t, datasets, nil)
			}
			svc, err := New(Options{Store: store, Datasets: cache})
			require.NoError(t, err)
			expectation := &pb.DatasetPeriodExpectation{SpaceId: "space", DatasetId: "dataset", Frequency: tc.frequency, PeriodTime: 123, SeriesHash: "hash", ExpectedCount: 1}
			rsp := invoke(svc, expectation)
			require.Equal(t, tc.wantCode, rsp.GetCode(), "ret_info=%v", rsp)
			if tc.wantCode == pb.ErrorCode_SUCCESS {
				require.Equal(t, 1, writes)
			} else {
				require.Zero(t, writes, "invalid period RPC reached the Store")
			}
		})
	}
}

func TestPrimaryWritesSameDatasetInDifferentSpacesSeparately(t *testing.T) {
	var written []string
	store := newHookStore(t)
	store.upsert = func(ctx context.Context, spaceID, datasetID string, rows []*pb.Row) error {
		written = append(written, spaceID+"/"+datasetID)
		return store.Store.UpsertFields(ctx, spaceID, datasetID, rows)
	}
	cache := newTestDatasetCache(t, []*pb.Dataset{
		{SpaceId: "space-a", DatasetId: "shared", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"},
		{SpaceId: "space-b", DatasetId: "shared", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"},
	}, []*pb.DatasetColumn{
		{SpaceId: "space-a", DatasetId: "shared", ColumnName: "value", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active"},
		{SpaceId: "space-b", DatasetId: "shared", ColumnName: "value", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active"},
	})
	svc, err := New(Options{Datasets: cache, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	row := func(space, record string) *pb.Row {
		return &pb.Row{
			Key:    &pb.RowKey{SpaceId: space, DatasetId: "shared", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}},
			Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: record}}}},
		}
	}
	// 一次请求只写一个数据集：两个空间里的同名数据集各写一次。
	for _, item := range [][2]string{{"space-a", "a"}, {"space-b", "b"}} {
		rsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
			Rows: []*pb.Row{row(item[0], item[1])},
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
	cache := newTestDatasetCache(t, []*pb.Dataset{
		{SpaceId: "crypto", DatasetId: "market_kline_1m", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Frequency: "1m", Status: "active"},
		{SpaceId: "crypto", DatasetId: "market_kline_5m", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Frequency: "5m", Status: "active"},
	}, []*pb.DatasetColumn{
		{SpaceId: "crypto", DatasetId: "market_kline_1m", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
		{SpaceId: "crypto", DatasetId: "market_kline_5m", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
	})
	svc, err := New(Options{Datasets: cache, Store: openTestStore(t), DatasetMetrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	row := func(datasetID, freq string, at time.Time) *pb.Row {
		return &pb.Row{
			Key: &pb.RowKey{
				SpaceId: "crypto", DatasetId: datasetID,
				Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
					SubjectId: "BTC-USDT", Frequency: freq, DataTime: at.UTC().Format(time.RFC3339Nano),
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
		Rows: []*pb.Row{row("market_kline_1m", "1m", t1003), row("market_kline_1m", "1m", t1005)},
	}))
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write 1m rsp=%v err=%v", rsp, err)
	}
	rsp, err = svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
		Rows: []*pb.Row{row("market_kline_5m", "5m", t1003)},
	}))
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write 5m rsp=%v err=%v", rsp, err)
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
	store.upsert = func(context.Context, string, string, []*pb.Row) error {
		return errors.New("write failed")
	}
	metrics := &recordingDatasetMetrics{}
	cache := newTestDatasetCache(t, []*pb.Dataset{
		{SpaceId: "crypto", DatasetId: "market_kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Frequency: "1m", Status: "active"},
	}, []*pb.DatasetColumn{
		{SpaceId: "crypto", DatasetId: "market_kline", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
	})
	svc, err := New(Options{Datasets: cache, Store: store, DatasetMetrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 28, 10, 5, 0, 0, time.UTC)
	rsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
		Rows: []*pb.Row{{
			Key: &pb.RowKey{
				SpaceId: "crypto", DatasetId: "market_kline",
				Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
					SubjectId: "BTC-USDT", Frequency: "1m", DataTime: at.Format(time.RFC3339Nano),
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
	cache := newTestDatasetCache(t, []*pb.Dataset{
		{SpaceId: "crypto", DatasetId: "symbols", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"},
	}, []*pb.DatasetColumn{
		{SpaceId: "crypto", DatasetId: "symbols", ColumnName: "symbol", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active"},
	})
	svc, err := New(Options{Datasets: cache, Store: openTestStore(t), DatasetMetrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
		Rows: []*pb.Row{{
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
	store.readRows = func(_ context.Context, keys []*pb.RowKey, _, _ []string) ([]*pb.Row, error) {
		rows := make([]*pb.Row, 0, len(keys))
		for _, key := range keys {
			rows = append(rows, &pb.Row{Key: key})
		}
		return rows, nil
	}
	cache := newTestDatasetCache(t, []*pb.Dataset{
		{SpaceId: "s", DatasetId: "a", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"},
		{SpaceId: "s", DatasetId: "b", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"},
	}, nil)
	svc, err := New(Options{Datasets: cache, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	key := func(dataset, record string) *pb.RowKey {
		return &pb.RowKey{SpaceId: "s", DatasetId: dataset, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}}
	}
	keys := []*pb.RowKey{key("b", "first"), key("a", "second"), key("b", "third")}
	rsp, err := svc.ReadRows(context.Background(), &pb.PrimaryReadRowsReq{
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

// 一次 ReadRows 请求只取一次快照：读完第一组后缓存刷新把第二组数据集停用，本次请求仍沿用同一快照完成，下一次请求才看到停用。
func TestPrimaryReadRowsUsesOneSnapshotAcrossDatasetGroups(t *testing.T) {
	cache, source := newMutableDatasetCache(t, []*pb.Dataset{
		{SpaceId: "space", DatasetId: "dataset-a", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"},
		{SpaceId: "space", DatasetId: "dataset-b", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"},
	}, nil)
	var readOrder []string
	store := newHookStore(t)
	store.readRows = func(ctx context.Context, keys []*pb.RowKey, _, _ []string) ([]*pb.Row, error) {
		readOrder = append(readOrder, keys[0].GetDatasetId())
		if keys[0].GetDatasetId() == "dataset-a" {
			source.set([]*pb.Dataset{
				{SpaceId: "space", DatasetId: "dataset-a", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"},
				{SpaceId: "space", DatasetId: "dataset-b", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "disabled"},
			}, nil)
			require.NoError(t, cache.Refresh(ctx))
		}
		return []*pb.Row{{Key: keys[0]}}, nil
	}
	svc, err := New(Options{Store: store, Datasets: cache})
	require.NoError(t, err)
	key := func(dataset, record string) *pb.RowKey {
		return &pb.RowKey{SpaceId: "space", DatasetId: dataset, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}}
	}
	rsp, err := svc.ReadRows(context.Background(), &pb.PrimaryReadRowsReq{
		Keys: []*pb.RowKey{key("dataset-a", "a"), key("dataset-b", "b")}, ColumnNames: []string{"value"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, []string{"dataset-a", "dataset-b"}, readOrder)

	next, err := svc.ReadRows(context.Background(), &pb.PrimaryReadRowsReq{
		Keys: []*pb.RowKey{key("dataset-b", "b")}, ColumnNames: []string{"value"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, next.GetRetInfo().GetCode())
}

func TestPrimaryRejectsRecordWriteWithoutVersion(t *testing.T) {
	store := newHookStore(t)
	store.upsert = func(context.Context, string, string, []*pb.Row) error {
		t.Fatal("write should not reach the store")
		return nil
	}
	cache := newTestDatasetCache(t, []*pb.Dataset{
		{SpaceId: "s", DatasetId: "d", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"},
	}, []*pb.DatasetColumn{
		{SpaceId: "s", DatasetId: "d", ColumnName: "value", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active"},
	})
	svc, err := New(Options{Datasets: cache, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
		Rows: []*pb.Row{{
			Key:    &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r"}}},
			Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "x"}}}},
		}},
	}))
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func TestPrimaryReadRowsReturnsResolvedLatestRecordVersion(t *testing.T) {
	cache := newTestDatasetCache(t, []*pb.Dataset{
		{SpaceId: "space", DatasetId: "records", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"},
	}, []*pb.DatasetColumn{
		{SpaceId: "space", DatasetId: "records", ColumnName: "value", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active"},
	})
	svc, err := New(Options{Datasets: cache, Store: openTestStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1", "2"} {
		key := &pb.RowKey{SpaceId: "space", DatasetId: "records", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: version}}}
		rsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
			Rows: []*pb.Row{{
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
	cache := newTestDatasetCache(t, []*pb.Dataset{
		{SpaceId: "crypto", DatasetId: "dataset_spot_kline_1h", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Frequency: "1h", Status: "active"},
	}, []*pb.DatasetColumn{
		{SpaceId: "crypto", DatasetId: "dataset_spot_kline_1h", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
	})
	svc, err := New(Options{Datasets: cache, Store: openTestStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	rowKey := timeSeriesRowKey("crypto", "dataset_spot_kline_1h", "BTC-USDT", "1h", "2026-07-29T15:00:00Z", "venue:binance")
	writeRsp, err := svc.UpsertFields(context.Background(), fillDataset(&pb.PrimaryUpsertFieldsReq{
		Rows: []*pb.Row{{
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
		Keys: []*pb.RowKey{rowKey}, ColumnNames: []string{"close"},
	})
	if err != nil || readRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS ||
		len(readRsp.GetRows()) != 1 || readRsp.GetRows()[0].GetKey().GetTimeSeries().GetSeriesTag() != "venue:binance" ||
		len(readRsp.GetRows()[0].GetFields()) != 1 ||
		readRsp.GetRows()[0].GetFields()[0].GetValue().GetDoubleValue() != 101.25 {
		t.Fatalf("read rsp=%v err=%v", readRsp, err)
	}
}

func timeSeriesRowKey(spaceID, datasetID, subjectID, freq, dataTime, seriesTag string) *pb.RowKey {
	return &pb.RowKey{
		SpaceId: spaceID, DatasetId: datasetID,
		Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: subjectID, Frequency: freq, DataTime: dataTime, SeriesTag: seriesTag,
		}},
	}
}
