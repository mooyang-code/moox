package merge

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/merge/internal/domain"
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
	require.Equal(t, []string{"BTC-USDT"}, reports.markers[0].UniverseSubjectIDs)
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

func TestMergeAssemblerFreezesInnerCollectorUniverse(t *testing.T) {
	assembler, commits := openAssembler(t, domain.MergeModeSystem)
	ledger, reports := openPeriodLedger(t)
	assembler.SetPeriodLedger(ledger)
	period := time.Date(2026, 9, 15, 6, 48, 0, 0, time.UTC)
	require.NoError(t, assembler.NoteCollectorCompleted(context.Background(), "dataset_binance_spot_kline_1m", period, []string{"BTC-USDT", "ETH-USDT"}))
	require.NoError(t, assembler.NoteCollectorCompleted(context.Background(), "dataset_binance_swap_kline_1m", period, []string{"BTC-USDT"}))
	btc := mergeKey("BTC-USDT", period)
	eth := mergeKey("ETH-USDT", period)
	require.NoError(t, assembler.ApplyArrival(context.Background(), btc, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), btc, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), eth, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), eth, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Equal(t, []string{stableCommitID(btc)}, commits.ids, "spot-only symbols must not enter the inner mdataset universe")
	periodKey := PeriodKey{DatasetID: btc.DatasetID, SnapshotID: btc.SnapshotID, Frequency: btc.Frequency, PeriodTime: period}
	acceptedETH, err := ledger.Accepts(context.Background(), periodKey, "ETH-USDT")
	require.NoError(t, err)
	require.False(t, acceptedETH)
	require.NoError(t, ledger.Finalize(context.Background(), periodKey, period.Add(3*time.Minute)))
	require.Len(t, reports.markers, 1)
	require.Equal(t, "complete", reports.markers[0].Status)
	require.Equal(t, []string{"BTC-USDT"}, reports.markers[0].UniverseSubjectIDs)
	require.Empty(t, reports.markers[0].FailedSubjects)
}

func TestMergeAssemblerFreezesInnerMembershipUniverse(t *testing.T) {
	assembler, commits := openAssembler(t, domain.MergeModeSystem)
	assembler.def.UniverseSource = domain.UniverseSourceMembership
	ledger, reports := openPeriodLedger(t)
	assembler.SetPeriodLedger(ledger)
	assembler.SetSubjectLister(mapSubjectLister{
		"dataset_binance_spot_kline_1m": {"BTC-USDT-SPOT", "ETH-USDT"},
		"dataset_binance_swap_kline_1m": {"BTC-USDT-SWAP"},
	})
	period := time.Date(2026, 9, 15, 6, 48, 0, 0, time.UTC)
	btc := mergeKey("BTC-USDT", period)
	eth := mergeKey("ETH-USDT", period)
	require.NoError(t, assembler.ApplyArrival(context.Background(), btc, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), btc, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), eth, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), eth, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Equal(t, []string{stableCommitID(btc)}, commits.ids, "membership freeze must inner-join ListDatasetSubjects")
	periodKey := PeriodKey{DatasetID: btc.DatasetID, SnapshotID: btc.SnapshotID, Frequency: btc.Frequency, PeriodTime: period}
	require.NoError(t, ledger.Finalize(context.Background(), periodKey, period.Add(3*time.Minute)))
	require.Len(t, reports.markers, 1)
	require.Equal(t, "complete", reports.markers[0].Status)
	require.Equal(t, []string{"BTC-USDT"}, reports.markers[0].UniverseSubjectIDs)
}

func TestMergeAssemblerCommitsFrozenUniverseAfterPeriodClose(t *testing.T) {
	assembler, commits := openAssembler(t, domain.MergeModeSystem)
	ledger, reports := openPeriodLedger(t)
	assembler.SetPeriodLedger(ledger)
	period := time.Date(2026, 9, 15, 1, 39, 0, 0, time.UTC)
	require.NoError(t, assembler.NoteCollectorCompleted(context.Background(), "dataset_binance_spot_kline_1m", period, []string{"BTC-USDT"}))
	require.NoError(t, assembler.NoteCollectorCompleted(context.Background(), "dataset_binance_swap_kline_1m", period, []string{"BTC-USDT"}))
	periodKey := PeriodKey{DatasetID: "mdataset_binance_kline_1m", SnapshotID: "snap-1", Frequency: "1m", PeriodTime: period}
	require.NoError(t, ledger.Finalize(context.Background(), periodKey, period.Add(3*time.Minute)))
	require.Len(t, reports.markers, 1)
	require.Equal(t, []string{"BTC-USDT"}, reports.markers[0].FailedSubjects)

	key := mergeKey("BTC-USDT", period)
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.Equal(t, []string{stableCommitID(key)}, commits.ids)
	require.Len(t, reports.markers, 1, "late CommitInput must not rewrite MergePeriodCompleted")
}

func TestMergeAssemblerCanonicalizesSpotSwapSubjectSuffixes(t *testing.T) {
	assembler, commits := openAssembler(t, domain.MergeModeSystem)
	period := time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC)
	require.NoError(t, assembler.ApplyArrival(context.Background(), mergeKey("BTC-USDT-SPOT", period), "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.Empty(t, commits.ids)
	require.NoError(t, assembler.ApplyArrival(context.Background(), mergeKey("BTC-USDT-SWAP", period), "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	want := mergeKey("BTC-USDT", period)
	require.Equal(t, []string{stableCommitID(want)}, commits.ids)
}

func TestMergeAssemblerReplaysPeriodReceiptAfterCommitRecorded(t *testing.T) {
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
	require.Len(t, commits.ids, 1)
	require.NoError(t, store.db.Exec("DELETE FROM t_merge_period_subjects").Error)
	require.NoError(t, assembler.commitIfReady(context.Background(), key))
	require.Len(t, commits.ids, 1, "already committed rows must not CommitInput again")
	periodKey := PeriodKey{DatasetID: key.DatasetID, SnapshotID: key.SnapshotID, Frequency: key.Frequency, PeriodTime: period}
	subjects, err := ledger.loadSubjects(context.Background(), periodKey)
	require.NoError(t, err)
	require.Len(t, subjects, 1)
	require.Equal(t, "success", subjects[0].State)
	require.Equal(t, "test-node", subjects[0].NodeID)
	require.Equal(t, "test-store", subjects[0].StoreID)
	require.EqualValues(t, 1, subjects[0].Sequence)
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

type mapSubjectLister map[string][]string

func (m mapSubjectLister) ListActiveDatasetSubjects(_ context.Context, _, datasetID string) ([]string, error) {
	return append([]string(nil), m[datasetID]...), nil
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
