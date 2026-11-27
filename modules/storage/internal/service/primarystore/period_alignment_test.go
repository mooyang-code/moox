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

func TestDatasetPeriodEndAtSessionDailyMarkets(t *testing.T) {
	snap := attrsSnapshot{
		"cn_daily":       {"alignment": "session", "calendar": "cn_stock"},
		"hk_daily":       {"alignment": "session", "calendar": "hk_stock"},
		"us_daily":       {"alignment": "session", "calendar": "us_stock"},
		"crypto_daily":   {"alignment": "epoch_utc"},
		"unknown_cal":    {"alignment": "session", "calendar": "unknown_market"},
		"space_inferred": {"alignment": "session"},
	}
	ctx := metadata.WithRequestSnapshot(context.Background(), snap)

	tests := []struct {
		name      string
		spaceID   string
		datasetID string
		freq      string
		period    time.Time
		wantEnd   time.Time // Zero 表示返回 0（由 Pebble 沿用 frequency.BarEnd）
	}{
		{
			name:      "cn_stock 正常交易日 15:00 CST = 07:00Z",
			spaceID:   "stockcn",
			datasetID: "cn_daily",
			freq:      "1d",
			period:    time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC),
		},
		{
			name:      "hk_stock 正常交易日 16:00 HKT = 08:00Z",
			spaceID:   "stockhk",
			datasetID: "hk_daily",
			freq:      "1d",
			period:    time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC),
		},
		{
			name:      "hk_stock 圣诞前夕半日市 12:00 HKT = 04:00Z",
			spaceID:   "stockhk",
			datasetID: "hk_daily",
			freq:      "1d",
			period:    time.Date(2026, 12, 24, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 12, 24, 4, 0, 0, 0, time.UTC),
		},
		{
			name:      "us_stock 夏令时正常交易日 16:00 EDT = 20:00Z",
			spaceID:   "stockus",
			datasetID: "us_daily",
			freq:      "1d",
			period:    time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC),
		},
		{
			name:      "us_stock 冬令时正常交易日 16:00 EST = 21:00Z",
			spaceID:   "stockus",
			datasetID: "us_daily",
			freq:      "1d",
			period:    time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 11, 25, 21, 0, 0, 0, time.UTC),
		},
		{
			name:      "us_stock 感恩节次日半日市 13:00 EST = 18:00Z",
			spaceID:   "stockus",
			datasetID: "us_daily",
			freq:      "1d",
			period:    time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 11, 27, 18, 0, 0, 0, time.UTC),
		},
		{
			name:      "cn_stock 超出日历覆盖范围回退到常规收盘 15:00 CST = 07:00Z",
			spaceID:   "stockcn",
			datasetID: "cn_daily",
			freq:      "1d",
			period:    time.Date(2099, 6, 15, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2099, 6, 15, 7, 0, 0, 0, time.UTC),
		},
		{
			name:      "session 缺省 calendar 时按 space_id 推断 A 股市场",
			spaceID:   "stockcn",
			datasetID: "space_inferred",
			freq:      "1d",
			period:    time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC),
		},
		{
			name:      "crypto epoch_utc 1d 返回 0（由存储层按 +24h 计算）",
			spaceID:   "crypto",
			datasetID: "crypto_daily",
			freq:      "1d",
			period:    time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "session 日内周期 1h 返回 0（由存储层按 frequency.BarEnd 计算）",
			spaceID:   "stockcn",
			datasetID: "cn_daily",
			freq:      "1h",
			period:    time.Date(2026, 10, 21, 1, 30, 0, 0, time.UTC),
		},
		{
			name:      "未登记 calendar 返回 0（不误覆盖）",
			spaceID:   "stockcn",
			datasetID: "unknown_cal",
			freq:      "1d",
			period:    time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "非 UTC 零点 1d 键返回 0",
			spaceID:   "stockcn",
			datasetID: "cn_daily",
			freq:      "1d",
			period:    time.Date(2026, 10, 21, 1, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := datasetPeriodEndAt(ctx, &pb.DatasetPeriodExpectation{
				SpaceId:    tc.spaceID,
				DatasetId:  tc.datasetID,
				Frequency:  tc.freq,
				PeriodTime: tc.period.Unix(),
			})
			if tc.wantEnd.IsZero() {
				require.Zero(t, got)
			} else {
				require.Equal(t, tc.wantEnd.Unix(), got)
			}
		})
	}
}

func TestCommitTimeSeriesBatchPassesSessionDailyPeriodEndAtToStore(t *testing.T) {
	periodDay := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	wantClose := time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC).Unix() // A 股 15:00 CST

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
			PeriodTime: periodDay.Unix(), SeriesHash: "h1", ExpectedCount: 2,
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
	require.Equal(t, wantClose, captured.PeriodEndAt)
	require.Equal(t, int64(300), captured.AllowedLatenessSeconds)
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
