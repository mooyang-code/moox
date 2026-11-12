package primarystore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type commitFunc func(context.Context, pebble.DatasetPeriodExpectation, []pebble.TimeSeriesBatchItem) (pebble.DatasetPeriodResult, error)

// commitMetricsService 的周期提交由测试决定存储层返回什么。
func commitMetricsService(t *testing.T, commit commitFunc) (*Service, *recordingDatasetMetrics) {
	t.Helper()
	metrics := &recordingDatasetMetrics{}
	svc, err := New(Options{
		Store:          &hookStore{Store: openTestStore(t), commit: commit},
		DatasetMetrics: metrics,
		Snapshot: func() metadata.RequestSnapshot {
			return collectorPeriodMetadataSnapshot("crypto", "market_kline", "1m")
		},
		Authorizer: func(*pb.AuthInfo) error { return nil },
	})
	require.NoError(t, err)
	return svc, metrics
}

func commitRequest(times ...time.Time) *pb.PrimaryCommitTimeSeriesBatchReq {
	items := make([]*pb.TimeSeriesBatchRow, 0, len(times))
	for i, at := range times {
		items = append(items, &pb.TimeSeriesBatchRow{SeriesIndex: uint32(i), Row: &pb.RowFieldUpsert{
			Key: &pb.RowKey{SpaceId: "crypto", DatasetId: "market_kline", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
				SubjectId: "BTC-USDT", Freq: "1m", DataTime: at.UTC().Format(time.RFC3339Nano),
			}}},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}},
		}})
	}
	return &pb.PrimaryCommitTimeSeriesBatchReq{
		AuthInfo:    &pb.AuthInfo{AppId: "collector", AppKey: "caller-key"},
		Expectation: &pb.DatasetPeriodExpectation{SpaceId: "crypto", DatasetId: "market_kline", Frequency: "1m", PeriodTime: 1, SeriesHash: "h", ExpectedCount: 1},
		Items:       items,
	}
}

func TestPeriodCommitAdvancesTheDatasetOutputWatermark(t *testing.T) {
	svc, metrics := commitMetricsService(t, func(context.Context, pebble.DatasetPeriodExpectation, []pebble.TimeSeriesBatchItem) (pebble.DatasetPeriodResult, error) {
		return pebble.DatasetPeriodResult{Status: "waiting"}, nil
	})
	t0 := time.Date(2026, 10, 14, 1, 58, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	rsp, err := svc.CommitTimeSeriesBatch(context.Background(), commitRequest(t0, t1))
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetKeys(), 2)

	require.Len(t, metrics.observations, 1)
	got := metrics.observations[0]
	require.Equal(t, "success", got.Result)
	require.Equal(t, "1m", got.Key.Freq)
	require.Equal(t, uint64(2), got.Rows)
	require.True(t, got.OutputWatermark.Equal(t1), "watermark=%v want=%v", got.OutputWatermark, t1)
}

func TestPeriodCommitFailureIsRecordedAsErrorWithoutAWatermark(t *testing.T) {
	cases := map[string]error{
		"internal error": errors.New("boom"),
		"rejected":       pebble.ValidationErrorFor("bad"),
	}
	for name, failure := range cases {
		t.Run(name, func(t *testing.T) {
			svc, metrics := commitMetricsService(t, func(context.Context, pebble.DatasetPeriodExpectation, []pebble.TimeSeriesBatchItem) (pebble.DatasetPeriodResult, error) {
				return pebble.DatasetPeriodResult{}, failure
			})
			rsp, err := svc.CommitTimeSeriesBatch(context.Background(), commitRequest(time.Date(2026, 10, 14, 1, 58, 0, 0, time.UTC)))
			require.NoError(t, err)
			require.NotEqual(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
			require.Len(t, metrics.observations, 1)
			require.Equal(t, "error", metrics.observations[0].Result)
			require.True(t, metrics.observations[0].OutputWatermark.IsZero())
		})
	}
}

type recordedRejections struct{ keys []string }

func (r *recordedRejections) ObserveDeadlineRejection(spaceID, datasetID, frequency string, _ time.Time) {
	r.keys = append(r.keys, spaceID+"/"+datasetID+"/"+frequency)
}

// 存储层拒绝“周期建立前截止已过”时记录截止拒绝；其他失败不记。
func TestPeriodCommitRecordsDeadlineRejections(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"deadline passed": {pebble.PeriodDeadlinePassedError{Deadline: 1792572005}, 1},
		"other rejection": {pebble.ValidationErrorFor("series hash a does not match the effective snapshot b for this period"), 0},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := commitMetricsService(t, func(context.Context, pebble.DatasetPeriodExpectation, []pebble.TimeSeriesBatchItem) (pebble.DatasetPeriodResult, error) {
				return pebble.DatasetPeriodResult{}, tc.err
			})
			rejections := &recordedRejections{}
			svc.rejection = rejections
			rsp, err := svc.CommitTimeSeriesBatch(context.Background(), commitRequest(time.Date(2026, 10, 14, 1, 58, 0, 0, time.UTC)))
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
			require.Len(t, rejections.keys, tc.want)
		})
	}
}
