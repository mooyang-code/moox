package merge

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestMergeAssemblerKeepsCommitReceiptsArrivingBeforeCollectorFreeze(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(Options{Path: dir + "/merge.db"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	commits := &commitRecorder{}
	assembler, err := NewAssembler(store, spotSwapDefinition(domain.MergeModeSystem), commits)
	require.NoError(t, err)
	reports := &reportRecorder{}
	ledger, err := NewPeriodLedger(store, reports)
	require.NoError(t, err)
	assembler.SetPeriodLedger(ledger)
	period := time.Date(2026, 9, 13, 16, 0, 0, 0, time.UTC)
	key := mergeKey("BTC-USDT", period)
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Equal(t, []string{stableCommitID(key)}, commits.ids)
	require.NoError(t, assembler.NoteCollectorCompleted(context.Background(), "dataset_binance_spot_kline_1m", period, []string{"BTC-USDT"}))
	require.NoError(t, assembler.NoteCollectorCompleted(context.Background(), "dataset_binance_swap_kline_1m", period, []string{"BTC-USDT"}))
	periodKey := PeriodKey{DatasetID: key.DatasetID, SnapshotID: key.SnapshotID, Frequency: key.Frequency, PeriodTime: period}
	require.NoError(t, ledger.Finalize(context.Background(), periodKey, period.Add(3*time.Minute)))
	require.Len(t, reports.markers, 1)
	require.Equal(t, "complete", reports.markers[0].Status)
	require.Equal(t, []string{"BTC-USDT"}, reports.markers[0].ExpectedSubjectIDs)
	require.Empty(t, reports.markers[0].FailedSubjects)
	require.Equal(t, []WriteReceipt{{
		CommitID: stableCommitID(key), NodeID: "test-node", StoreID: "test-store", Sequence: 1,
	}}, reports.markers[0].Positions)
}

func TestMergeAssemblerFreezesFromCollectorUniverseAfterPeriodEnd(t *testing.T) {
	assembler, commits := openAssembler(t, domain.MergeModeSystem)
	ledger, reports := openPeriodLedger(t)
	assembler.SetPeriodLedger(ledger)
	period := time.Date(2026, 9, 13, 16, 0, 0, 0, time.UTC)
	require.NoError(t, assembler.NoteCollectorCompleted(context.Background(), "dataset_binance_spot_kline_1m", period, []string{"BTC-USDT", "ETH-USDT"}))
	require.Empty(t, reports.markers)
	require.NoError(t, assembler.NoteCollectorCompleted(context.Background(), "dataset_binance_swap_kline_1m", period, []string{"BTC-USDT"}))
	key := mergeKey("BTC-USDT", period)
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Equal(t, []string{stableCommitID(key)}, commits.ids)
	late := mergeKey("SOL-USDT", period)
	require.NoError(t, assembler.ApplyArrival(context.Background(), late, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), late, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Equal(t, []string{stableCommitID(key)}, commits.ids, "subjects outside the frozen collector universe must be dropped")
	deadline, err := periodFreezeDeadline(period, "1m")
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 9, 13, 16, 3, 0, 0, time.UTC), deadline)
	hourDeadline, err := periodFreezeDeadline(period, "1h")
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 9, 13, 17, 2, 0, 0, time.UTC), hourDeadline)
}

func TestMergeAssemblerWaitsForAllSourcesBeforeCommit(t *testing.T) {
	assembler, commits := openAssembler(t, domain.MergeModeSystem)
	key := mergeKey("BTC-USDT", time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.Empty(t, commits.ids)
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Equal(t, []string{stableCommitID(key)}, commits.ids)
	require.True(t, commits.rows[0].ready)
	require.Contains(t, commits.rows[0].fields, "dataset_binance_spot_kline_1m__open")
	require.Contains(t, commits.rows[0].fields, "dataset_binance_swap_kline_1m__close")
}

func TestMergeAssemblerDoesNotBlockOtherSubjects(t *testing.T) {
	assembler, commits := openAssembler(t, domain.MergeModeSystem)
	btc := mergeKey("BTC-USDT", time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC))
	eth := mergeKey("ETH-USDT", time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC))
	require.NoError(t, assembler.ApplyArrival(context.Background(), eth, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), btc, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), btc, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Equal(t, []string{stableCommitID(btc)}, commits.ids)
}

func TestMergeAssemblerRecoversPendingSourceAfterRestart(t *testing.T) {
	dir := t.TempDir()
	first, _ := openAssemblerAt(t, dir, domain.MergeModeSystem)
	key := mergeKey("BTC-USDT", time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC))
	require.NoError(t, first.ApplyArrival(context.Background(), key, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	second, commits := openAssemblerAt(t, dir, domain.MergeModeSystem)
	require.NoError(t, second.ApplyArrival(context.Background(), key, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Equal(t, []string{stableCommitID(key)}, commits.ids)
}

func TestMergeAssemblerCommitIsIdempotent(t *testing.T) {
	assembler, commits := openAssembler(t, domain.MergeModeSystem)
	key := mergeKey("BTC-USDT", time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Equal(t, []string{stableCommitID(key)}, commits.ids)
}

func TestMergeAssemblerIgnoresIncompleteSourceFields(t *testing.T) {
	assembler, commits := openAssembler(t, domain.MergeModeSystem)
	key := mergeKey("BTC-USDT", time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_spot_kline_1m", map[string]float64{"open": 1}))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Empty(t, commits.ids)
}

func TestMergeAssemblerCustomModeDoesNotCommit(t *testing.T) {
	assembler, commits := openAssembler(t, domain.MergeModeCustom)
	key := mergeKey("BTC-USDT", time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Empty(t, commits.ids)
}

func completeKlineFields(prefix string) map[string]float64 {
	_ = prefix
	return map[string]float64{"open": 1, "high": 2, "low": 0.5, "close": 1.5, "volume": 10, "quote_volume": 20, "trade_num": 3}
}

func mergeKey(subject string, period time.Time) RowKey {
	return RowKey{
		DatasetID: "mdataset_binance_kline_1m", SnapshotID: "snap-1", SubjectID: subject,
		Frequency: "1m", PeriodTime: period.UTC(), SeriesTag: "default",
	}
}

func openAssembler(t *testing.T, mode string) (*Assembler, *commitRecorder) {
	t.Helper()
	return openAssemblerAt(t, t.TempDir(), mode)
}

func openAssemblerAt(t *testing.T, dir, mode string) (*Assembler, *commitRecorder) {
	t.Helper()
	ledger, err := Open(Options{Path: dir + "/merge.db"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ledger.Close() })
	commits := &commitRecorder{}
	assembler, err := NewAssembler(ledger, spotSwapDefinition(mode), commits)
	require.NoError(t, err)
	return assembler, commits
}

func spotSwapDefinition(mode string) domain.MergedDataset {
	spot := "dataset_binance_spot_kline_1m"
	swap := "dataset_binance_swap_kline_1m"
	fields := []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num"}
	def := domain.MergedDataset{
		DatasetID: "mdataset_binance_kline_1m", SpaceID: "crypto", Frequency: "1m", MergeMode: mode,
		ConfigSnapshotID: "snap-1",
		KeyContract: domain.KeyContract{
			SubjectID: "subject_id", Frequency: "frequency", PeriodTime: "period_time", SeriesTag: "series_tag", PeriodBoundary: "close",
		},
		Sources: []domain.SourceDatasetRef{
			{DatasetID: spot, Frequency: "1m", PeriodBoundary: "close", Fields: append([]string(nil), fields...)},
			{DatasetID: swap, Frequency: "1m", PeriodBoundary: "close", Fields: append([]string(nil), fields...)},
		},
	}
	for _, source := range def.Sources {
		for _, field := range source.Fields {
			def.FieldMappings = append(def.FieldMappings, domain.FieldMapping{
				SourceDatasetID: source.DatasetID, SourceField: field, TargetField: domain.MappedSourceField(source.DatasetID, field),
			})
		}
	}
	return def
}

type commitRecorder struct {
	ids  []string
	rows []committedRow
}

type committedRow struct {
	ready  bool
	fields map[string]float64
}

func (c *commitRecorder) CommitInput(_ context.Context, commitID string, _ RowKey, fields map[string]float64, ready bool) (WriteReceipt, error) {
	copied := make(map[string]float64, len(fields))
	for name, value := range fields {
		copied[name] = value
	}
	c.ids = append(c.ids, commitID)
	c.rows = append(c.rows, committedRow{ready: ready, fields: copied})
	return WriteReceipt{CommitID: commitID, NodeID: "test-node", StoreID: "test-store", Sequence: uint64(len(c.ids))}, nil
}
