package merge

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/merge/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestDatasetPipeline(t *testing.T) {
	t.Run("two_sources_two_subjects", testDatasetPipelineTwoSources)
	t.Run("source_timeout_degraded", testDatasetPipelineTimeout)
	t.Run("kill_and_recover", testDatasetPipelineRestart)
}

func testDatasetPipelineTwoSources(t *testing.T) {
	assembler, commits := openAssembler(t, domain.MergeModeSystem)
	period := time.Date(2026, 9, 14, 1, 0, 0, 0, time.UTC)
	btc := mergeKey("BTC-USDT", period)
	eth := mergeKey("ETH-USDT", period)
	require.NoError(t, assembler.ApplyArrival(context.Background(), btc, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), eth, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.Empty(t, commits.ids)
	require.NoError(t, assembler.ApplyArrival(context.Background(), btc, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), eth, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Len(t, commits.ids, 2)
	require.True(t, commits.rows[0].ready && commits.rows[1].ready)
}

func testDatasetPipelineTimeout(t *testing.T) {
	dir := t.TempDir()
	assembler, commits := openAssemblerAt(t, dir, domain.MergeModeSystem)
	reports := new(pipelineReporter)
	periods := openPeriodLedgerAt(t, dir, reports)
	assembler.SetPeriodLedger(periods)
	period := time.Date(2026, 9, 14, 1, 1, 0, 0, time.UTC)
	key := mergeKey("BTC-USDT", period)
	periodKey := PeriodKey{DatasetID: key.DatasetID, SnapshotID: key.SnapshotID, Frequency: key.Frequency, PeriodTime: period}
	require.NoError(t, periods.Freeze(context.Background(), periodKey, []string{"BTC-USDT", "ETH-USDT"}, period.Add(time.Minute)))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Len(t, commits.ids, 1)
	require.NoError(t, periods.Finalize(context.Background(), periodKey, period.Add(2*time.Minute)))
	require.Len(t, reports.markers, 1)
	require.Equal(t, "degraded", reports.markers[0].Status)
	require.Equal(t, []string{"ETH-USDT"}, reports.markers[0].FailedSubjects)
	accepted, err := periods.Accepts(context.Background(), periodKey, "ETH-USDT")
	require.NoError(t, err)
	require.True(t, accepted, "late dual-source rows may still CommitInput after merge report")
	eth := mergeKey("ETH-USDT", period)
	require.NoError(t, assembler.ApplyArrival(context.Background(), eth, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), eth, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Len(t, commits.ids, 2)
	require.Len(t, reports.markers, 1)
	require.Equal(t, []string{"ETH-USDT"}, reports.markers[0].FailedSubjects)
}

func testDatasetPipelineRestart(t *testing.T) {
	dir := t.TempDir()
	first, _ := openAssemblerAt(t, dir, domain.MergeModeSystem)
	period := time.Date(2026, 9, 14, 1, 2, 0, 0, time.UTC)
	key := mergeKey("BTC-USDT", period)
	require.NoError(t, first.ApplyArrival(context.Background(), key, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	second, commits := openAssemblerAt(t, dir, domain.MergeModeSystem)
	require.NoError(t, second.ApplyArrival(context.Background(), key, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Len(t, commits.ids, 1)
}

type pipelineReporter struct {
	markers []PeriodMarker
}

func (r *pipelineReporter) Report(_ context.Context, marker PeriodMarker) error {
	r.markers = append(r.markers, marker)
	return nil
}
