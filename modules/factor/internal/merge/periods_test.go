package merge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMergePeriodLedgerKeepsFrozenUniverseOnPartialTimeout(t *testing.T) {
	ledger, reports := openPeriodLedger(t)
	key := periodKey(time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC))
	expected := []string{"AAA-USDT", "BBB-USDT", "CCC-USDT", "DDD-USDT", "EEE-USDT"}
	require.NoError(t, ledger.Freeze(context.Background(), key, expected, time.Date(2026, 9, 13, 16, 7, 0, 0, time.UTC)))
	require.NoError(t, ledger.NoteCommit(context.Background(), key, "AAA-USDT", testReceipt(1)))
	require.NoError(t, ledger.NoteCommit(context.Background(), key, "BBB-USDT", testReceipt(2)))
	require.NoError(t, ledger.NoteCommit(context.Background(), key, "CCC-USDT", testReceipt(3)))
	require.NoError(t, ledger.Finalize(context.Background(), key, time.Date(2026, 9, 13, 16, 8, 0, 0, time.UTC)))
	require.Len(t, reports.markers, 1)
	marker := reports.markers[0]
	require.Equal(t, "degraded", marker.Status)
	require.Equal(t, expected, marker.ExpectedSubjectIDs)
	require.Equal(t, []string{"DDD-USDT", "EEE-USDT"}, marker.FailedSubjects)
	require.NotEmpty(t, marker.Positions)
}

func TestMergePeriodLedgerRejectsLateArrivalAfterClose(t *testing.T) {
	ledger, reports := openPeriodLedger(t)
	key := periodKey(time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC))
	require.NoError(t, ledger.Freeze(context.Background(), key, []string{"AAA-USDT", "BBB-USDT"}, time.Date(2026, 9, 13, 16, 7, 0, 0, time.UTC)))
	require.NoError(t, ledger.NoteCommit(context.Background(), key, "AAA-USDT", testReceipt(1)))
	require.NoError(t, ledger.Finalize(context.Background(), key, time.Date(2026, 9, 13, 16, 8, 0, 0, time.UTC)))
	accepted, err := ledger.Accepts(context.Background(), key, "BBB-USDT")
	require.NoError(t, err)
	require.False(t, accepted)
	require.NoError(t, ledger.NoteCommit(context.Background(), key, "BBB-USDT", testReceipt(9)))
	require.Len(t, reports.markers, 1)
	require.Equal(t, []string{"BBB-USDT"}, reports.markers[0].FailedSubjects)
}

func TestMergePeriodLedgerRetriesFailedReport(t *testing.T) {
	dir := t.TempDir()
	reports := &reportRecorder{failTimes: 1}
	ledger := openPeriodLedgerAt(t, dir, reports)
	key := periodKey(time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC))
	require.NoError(t, ledger.Freeze(context.Background(), key, []string{"AAA-USDT"}, time.Date(2026, 9, 13, 16, 7, 0, 0, time.UTC)))
	require.NoError(t, ledger.NoteCommit(context.Background(), key, "AAA-USDT", testReceipt(1)))
	require.Error(t, ledger.Finalize(context.Background(), key, time.Date(2026, 9, 13, 16, 8, 0, 0, time.UTC)))
	require.Empty(t, reports.markers)
	retried := openPeriodLedgerAt(t, dir, reports)
	require.NoError(t, retried.Finalize(context.Background(), key, time.Date(2026, 9, 13, 16, 8, 0, 0, time.UTC)))
	require.Len(t, reports.markers, 1)
	require.Equal(t, "complete", reports.markers[0].Status)
}

func TestMergePeriodLedgerEndsEmptyUniverseWithoutFakeInput(t *testing.T) {
	ledger, reports := openPeriodLedger(t)
	key := periodKey(time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC))
	require.NoError(t, ledger.Freeze(context.Background(), key, nil, time.Date(2026, 9, 13, 16, 7, 0, 0, time.UTC)))
	require.NoError(t, ledger.Finalize(context.Background(), key, time.Date(2026, 9, 13, 16, 8, 0, 0, time.UTC)))
	require.Len(t, reports.markers, 1)
	require.Equal(t, "complete", reports.markers[0].Status)
	require.Empty(t, reports.markers[0].ExpectedSubjectIDs)
	require.Empty(t, reports.markers[0].FailedSubjects)
	require.Empty(t, reports.markers[0].Positions)
}

func TestMergePeriodLedgerCollectorCompleteIsNotMergeComplete(t *testing.T) {
	ledger, reports := openPeriodLedger(t)
	key := periodKey(time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC))
	require.NoError(t, ledger.Freeze(context.Background(), key, []string{"AAA-USDT"}, time.Date(2026, 9, 13, 16, 7, 0, 0, time.UTC)))
	require.NoError(t, ledger.NoteCollectorCompleted(context.Background(), key.DatasetID, "dataset_binance_spot_kline_1m", key.PeriodTime, []string{"AAA-USDT"}))
	require.NoError(t, ledger.NoteCollectorCompleted(context.Background(), key.DatasetID, "dataset_binance_swap_kline_1m", key.PeriodTime, []string{"AAA-USDT"}))
	require.Empty(t, reports.markers)
}

func TestMergePeriodLedgerDoesNotWriteMissingSubjects(t *testing.T) {
	assembler, commits := openAssembler(t, "system")
	ledger, _ := openPeriodLedger(t)
	key := periodKey(time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC))
	require.NoError(t, ledger.Freeze(context.Background(), key, []string{"BTC-USDT", "ETH-USDT"}, time.Date(2026, 9, 13, 16, 7, 0, 0, time.UTC)))
	row := mergeKey("BTC-USDT", key.PeriodTime)
	require.NoError(t, assembler.ApplyArrival(context.Background(), row, "dataset_binance_spot_kline_1m", completeKlineFields("spot")))
	require.NoError(t, assembler.ApplyArrival(context.Background(), row, "dataset_binance_swap_kline_1m", completeKlineFields("swap")))
	require.NoError(t, ledger.NoteCommit(context.Background(), key, "BTC-USDT", testReceipt(1)))
	require.NoError(t, ledger.Finalize(context.Background(), key, time.Date(2026, 9, 13, 16, 8, 0, 0, time.UTC)))
	require.Equal(t, []string{stableCommitID(row)}, commits.ids)
}

func periodKey(at time.Time) PeriodKey {
	return PeriodKey{DatasetID: "mdataset_binance_kline_1m", SnapshotID: "snap-1", Frequency: "1m", PeriodTime: at.UTC()}
}

func testReceipt(seq uint64) WriteReceipt {
	return WriteReceipt{CommitID: "commit", NodeID: "node", StoreID: "store", Sequence: seq}
}

func openPeriodLedger(t *testing.T) (*PeriodLedger, *reportRecorder) {
	t.Helper()
	reports := &reportRecorder{}
	return openPeriodLedgerAt(t, t.TempDir(), reports), reports
}

func openPeriodLedgerAt(t *testing.T, dir string, reports PeriodReporter) *PeriodLedger {
	t.Helper()
	store, err := Open(Options{Path: dir + "/merge.db"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ledger, err := NewPeriodLedger(store, reports)
	require.NoError(t, err)
	return ledger
}

type reportRecorder struct {
	failTimes int
	markers   []PeriodMarker
}

func (r *reportRecorder) Report(_ context.Context, marker PeriodMarker) error {
	if r.failTimes > 0 {
		r.failTimes--
		return errors.New("report failed")
	}
	r.markers = append(r.markers, marker)
	return nil
}
