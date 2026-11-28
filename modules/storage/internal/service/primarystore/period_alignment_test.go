package primarystore

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestValidateSnapshotAlignmentFollowsDatasetAlignment(t *testing.T) {
	ctx := metadata.WithRequestSnapshot(context.Background(), attrsSnapshot{
		"epoch":   {"owner_module": "collector", "dataset_role": "raw_collection"},
		"session": {"owner_module": "collector", "dataset_role": "kline_resample_result", "alignment": "session"},
	})
	open := time.Date(2026, 10, 21, 1, 30, 0, 0, time.UTC).Unix() // 北京时间 09:30
	request := func(dataset, frequency string, at int64) *pb.PrimaryRegisterSeriesSnapshotReq {
		return &pb.PrimaryRegisterSeriesSnapshotReq{SpaceId: "space", DatasetId: dataset, Frequency: frequency, EffectiveFrom: at}
	}
	// epoch 数据集：1h 必须落在整点，09:30 开盘（01:30Z）不行。
	require.ErrorContains(t, validateSnapshotAlignment(ctx, request("epoch", "1h", open)), "aligned")
	require.NoError(t, validateSnapshotAlignment(ctx, request("epoch", "1h", open-30*60)))
	// session 数据集：桶从开盘起切分，只要求整分钟，由 DataNode 校验。
	require.NoError(t, validateSnapshotAlignment(ctx, request("session", "1h", open)))
	// 日线及以上的边界取决于时区，这一层不检查。
	require.NoError(t, validateSnapshotAlignment(ctx, request("epoch", "1d", open)))
}

// 周期的真实结束时刻只取写入方声明的值：重采样结果带桶的收盘时刻；直采日线（下一交易日早上才采集）不带，
// Storage 不能按数据集的日历自行推成收盘时刻，否则直采日线到达时早已过截止。
func TestCommitTimeSeriesBatchUsesDeclaredPeriodEndAt(t *testing.T) {
	periodDay := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	closeAt := time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC).Unix() // A 股 15:00 CST

	for _, tc := range []struct {
		name     string
		declared int64
	}{
		{name: "重采样结果声明收盘时刻", declared: closeAt},
		{name: "直采日线不声明，沿用整周期长度", declared: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var captured pebble.DatasetPeriodExpectation
			store := &commitCaptureStore{
				commitBatchFn: func(_ context.Context, exp pebble.DatasetPeriodExpectation, _ []pebble.TimeSeriesBatchItem) (pebble.DatasetPeriodResult, error) {
					captured = exp
					return pebble.DatasetPeriodResult{Status: "waiting", AcceptedSeriesIndexes: []uint32{0}}, nil
				},
			}
			snapshot := fixedDatasetSnapshot{
				dataset: &pb.Dataset{
					SpaceId: "stockcn", DatasetId: "dataset_kline_1d", Status: "active",
					DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1d",
					Alignment: "session", AllowedLatenessSeconds: 300,
					Attributes: map[string]string{"calendar": "cn_stock"},
				},
			}
			svc, err := New(Options{
				Store:    store,
				Snapshot: func() metadata.RequestSnapshot { return snapshot },
			})
			require.NoError(t, err)

			rsp, err := svc.CommitTimeSeriesBatch(context.Background(), &pb.PrimaryCommitTimeSeriesBatchReq{
				Expectation: &pb.DatasetPeriodExpectation{
					SpaceId: "stockcn", DatasetId: "dataset_kline_1d", Frequency: "1d",
					PeriodTime: periodDay.Unix(), SeriesHash: "h1", ExpectedCount: 2, PeriodEndAt: tc.declared,
				},
				Items: []*pb.TimeSeriesBatchRow{{
					SeriesIndex: 0,
					Row: &pb.RowFieldUpsert{
						Key: &pb.RowKey{
							SpaceId: "stockcn", DatasetId: "dataset_kline_1d",
							Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
								SubjectId: "SH600519", Freq: "1d", DataTime: periodDay.Format(time.RFC3339Nano), SeriesTag: "default",
							}},
						},
						Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1500}}}},
					},
				}},
			})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
			require.Equal(t, tc.declared, captured.PeriodEndAt)
			require.Equal(t, int64(300), captured.AllowedLatenessSeconds)
		})
	}
}

type commitCaptureStore struct {
	Store
	commitBatchFn func(context.Context, pebble.DatasetPeriodExpectation, []pebble.TimeSeriesBatchItem) (pebble.DatasetPeriodResult, error)
}

func (s *commitCaptureStore) CommitTimeSeriesBatch(ctx context.Context, exp pebble.DatasetPeriodExpectation, items []pebble.TimeSeriesBatchItem) (pebble.DatasetPeriodResult, error) {
	return s.commitBatchFn(ctx, exp, items)
}

type fixedDatasetSnapshot struct {
	dataset *pb.Dataset
}

func (s fixedDatasetSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	if s.dataset == nil || s.dataset.GetSpaceId() != spaceID || s.dataset.GetDatasetId() != datasetID {
		return nil, false
	}
	return s.dataset, true
}

func (fixedDatasetSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}
