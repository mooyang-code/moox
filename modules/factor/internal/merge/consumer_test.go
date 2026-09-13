package merge

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
)

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

func openRowHandler(t *testing.T, mode string) (*RowHandler, *commitRecorder) {
	t.Helper()
	assembler, commits := openAssembler(t, mode)
	return NewRowHandler(assembler), commits
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
