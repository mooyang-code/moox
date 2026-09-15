package merge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/merge/internal/domain"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
)

func TestMergeAssemblerConsumesCollectorMarkersToFreeze(t *testing.T) {
	assembler, commits := openAssembler(t, domain.MergeModeSystem)
	ledger, _ := openPeriodLedger(t)
	assembler.SetPeriodLedger(ledger)
	handler := NewRowHandler(assembler)
	handler.Now = mergeTestNow
	period := time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC)
	require.NoError(t, handler.HandleCollectorCompleted(context.Background(), &storagepb.CollectorPeriodCompleted{
		DatasetId: "dataset_binance_spot_kline_1m", Frequency: "1m", PeriodTime: period.Unix(),
		UniverseSubjectIds: []string{"BTC-USDT"},
	}))
	require.NoError(t, handler.HandleCollectorCompleted(context.Background(), &storagepb.CollectorPeriodCompleted{
		DatasetId: "dataset_binance_swap_kline_1m", Frequency: "1m", PeriodTime: period.Unix(),
		UniverseSubjectIds: []string{"BTC-USDT"},
	}))
	require.NoError(t, handler.HandleDatasetRows(context.Background(), sourceRows("dataset_binance_spot_kline_1m", "BTC-USDT", period, completeKlineFields("spot"))))
	require.NoError(t, handler.HandleDatasetRows(context.Background(), sourceRows("dataset_binance_swap_kline_1m", "BTC-USDT", period, completeKlineFields("swap"))))
	require.Equal(t, []string{stableCommitID(mergeKey("BTC-USDT", period))}, commits.ids)
	require.NoError(t, handler.HandleDatasetRows(context.Background(), sourceRows("dataset_binance_spot_kline_1m", "ETH-USDT", period, completeKlineFields("spot"))))
	require.NoError(t, handler.HandleDatasetRows(context.Background(), sourceRows("dataset_binance_swap_kline_1m", "ETH-USDT", period, completeKlineFields("swap"))))
	require.Equal(t, []string{stableCommitID(mergeKey("BTC-USDT", period))}, commits.ids)
}

func TestMergeAssemblerConsumesSourceRowsAndCommitsOnce(t *testing.T) {
	handler, commits := openRowHandler(t, domain.MergeModeSystem)
	period := time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC)
	require.NoError(t, handler.HandleDatasetRows(context.Background(), sourceRows("dataset_binance_spot_kline_1m", "BTC-USDT", period, completeKlineFields("spot"))))
	require.Empty(t, commits.ids)
	require.NoError(t, handler.HandleDatasetRows(context.Background(), sourceRows("dataset_binance_swap_kline_1m", "BTC-USDT", period, completeKlineFields("swap"))))
	require.Equal(t, []string{stableCommitID(mergeKey("BTC-USDT", period))}, commits.ids)
	require.Contains(t, commits.rows[0].fields, "dataset_binance_spot_kline_1m__open")
}

func TestMergeAssemblerIgnoresUnrelatedDataset(t *testing.T) {
	handler, commits := openRowHandler(t, domain.MergeModeSystem)
	period := time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC)
	require.NoError(t, handler.HandleDatasetRows(context.Background(), sourceRows("dataset_other_kline_1m", "BTC-USDT", period, completeKlineFields("spot"))))
	require.Empty(t, commits.ids)
}

func TestMergeAssemblerIgnoresFactorPatchWriteKind(t *testing.T) {
	handler, commits := openRowHandler(t, domain.MergeModeSystem)
	period := time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC)
	payload := sourceRows("dataset_binance_spot_kline_1m", "BTC-USDT", period, completeKlineFields("spot"))
	payload.WriteKind = writeKindFactorPatch
	require.NoError(t, handler.HandleDatasetRows(context.Background(), payload))
	require.NoError(t, handler.HandleDatasetRows(context.Background(), sourceRows("dataset_binance_swap_kline_1m", "BTC-USDT", period, completeKlineFields("swap"))))
	require.Empty(t, commits.ids)
}

func TestMergeAssemblerDoesNotAckWhenApplyFails(t *testing.T) {
	handler, _ := openRowHandler(t, domain.MergeModeSystem)
	period := time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC)
	payload := sourceRows("dataset_binance_spot_kline_1m", "BTC-USDT", period, completeKlineFields("spot"))
	payload.Rows[0].Key.GetTimeSeries().DataTime = "not-a-time"
	require.Error(t, handler.HandleDatasetRows(context.Background(), payload))
}

func TestMergeRunUntilCancelledRestartsAfterError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runs atomic.Int32
	var logged atomic.Int32
	err := runUntilCancelled(ctx, 10*time.Millisecond, func(context.Context) error {
		if runs.Add(1) == 1 {
			return errors.New("apply handler result: context deadline exceeded")
		}
		cancel()
		return nil
	}, func(error) {
		logged.Add(1)
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, runs.Load(), int32(2))
	require.Equal(t, int32(1), logged.Load())
}

func TestMergeRowHandlerSkipsExpiredArrivals(t *testing.T) {
	handler, commits := openRowHandler(t, domain.MergeModeSystem)
	period := time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC)
	handler.Now = func() time.Time { return period.Add(2 * time.Minute) }
	require.NoError(t, handler.HandleDatasetRows(context.Background(), sourceRows("dataset_binance_spot_kline_1m", "BTC-USDT", period, completeKlineFields("spot"))))
	require.NoError(t, handler.HandleDatasetRows(context.Background(), sourceRows("dataset_binance_swap_kline_1m", "BTC-USDT", period, completeKlineFields("swap"))))
	require.Empty(t, commits.ids)
}

func openRowHandler(t *testing.T, mode string) (*RowHandler, *commitRecorder) {
	t.Helper()
	assembler, commits := openAssembler(t, mode)
	handler := NewRowHandler(assembler)
	handler.Now = mergeTestNow
	return handler, commits
}

func mergeTestNow() time.Time {
	return time.Date(2026, 9, 13, 16, 6, 0, 0, time.UTC)
}

func sourceRows(datasetID, subject string, period time.Time, fields map[string]float64) *storagepb.DatasetRowsUpserted {
	values := make([]*storagepb.FieldValue, 0, len(fields))
	for name, value := range fields {
		values = append(values, &storagepb.FieldValue{
			FieldId: name,
			Value:   &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}},
		})
	}
	return &storagepb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: datasetID,
		Rows: []*storagepb.RowUpsert{{
			Key: &storagepb.RowKey{
				SpaceId: "crypto", DatasetId: datasetID,
				Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
					SubjectId: subject, Freq: "1m", DataTime: period.UTC().Format(time.RFC3339Nano), SeriesTag: "default",
				}},
			},
			Fields: values,
		}},
	}
}
