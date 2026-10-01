package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestPeriodStorageReconcilerProbesSnapshotIdentityAndDeletesOnlyConfirmedTerminal(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	period := now.Add(-31 * 24 * time.Hour)
	snapshot := reconcilerSnapshot("crypto", "bars", "1h", period, "BTC-USDT", "ETH-USDT")
	stored, _, err := db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
	require.NoError(t, err)
	client := &periodStatusStub{status: func(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
		require.Equal(t, "crypto", expectation.GetSpaceId())
		require.Equal(t, "bars", expectation.GetDatasetId())
		require.Equal(t, "1h", expectation.GetFrequency())
		require.Equal(t, period.Unix(), expectation.GetPeriodTime())
		require.Equal(t, stored.SeriesHash, expectation.GetSeriesHash())
		require.Equal(t, stored.ExpectedCount, expectation.GetExpectedCount())
		require.Zero(t, expectation.GetDeadlineAt(), "status probes do not invent a deadline")
		require.Empty(t, expectation.GetSeriesSnapshot(), "status probes use the persisted snapshot identity, not a reconstructed series list")
		return reconcilerStateForSnapshot(stored, domain.PeriodStatusComplete), nil
	}}
	reconciler := NewPeriodStorageReconciler(db.PeriodSeriesSnapshot(), db.PeriodStorageStates(), client, "crypto")
	deleted, err := reconciler.Reconcile(ctx, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	require.Equal(t, 1, client.calls)
	_, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, stored.Key)
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = db.PeriodStorageStates().GetPeriodStorageState(ctx, stored.Key)
	require.NoError(t, err)
	require.False(t, found)
}

func TestPeriodStorageReconcilerDefersWaitingUnknownAndErrorStates(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	periods := []time.Time{now.Add(-33 * 24 * time.Hour), now.Add(-32 * 24 * time.Hour), now.Add(-31 * 24 * time.Hour)}
	snapshots := make([]domain.PeriodSeriesSnapshot, 0, len(periods))
	for i, period := range periods {
		snapshot := reconcilerSnapshot("crypto", "bars", "1m", period, fmt.Sprintf("S%d", i))
		stored, _, err := db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
		require.NoError(t, err)
		snapshots = append(snapshots, stored)
	}
	client := &periodStatusStub{status: func(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
		period := time.Unix(expectation.GetPeriodTime(), 0).UTC()
		switch period {
		case periods[0]:
			return domain.PeriodStorageState{}, errors.New("status unavailable")
		case periods[1]:
			return reconcilerStateForSnapshot(snapshots[1], domain.PeriodStatusWaiting), nil
		default:
			return domain.PeriodStorageState{}, errors.New("NOT_FOUND")
		}
	}}
	reconciler := NewPeriodStorageReconciler(db.PeriodSeriesSnapshot(), db.PeriodStorageStates(), client, "crypto")
	deleted, err := reconciler.Reconcile(ctx, now)
	require.Error(t, err, "Storage query failures are reported for operator visibility")
	require.Zero(t, deleted)
	for index, snapshot := range snapshots {
		_, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, snapshot.Key)
		require.NoError(t, err)
		require.True(t, found, "unconfirmed candidate %d remains", index)
	}
	state, found, err := db.PeriodStorageStates().GetPeriodStorageState(ctx, snapshots[1].Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, domain.PeriodStatusWaiting, state.Status)
}

func TestPeriodStorageReconcilerAdvancesPastPersistentFirstThousandFailures(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	now := base.Add(32 * 24 * time.Hour)
	const candidateCount = 1001
	var finalSnapshot domain.PeriodSeriesSnapshot
	for index := 0; index < candidateCount; index++ {
		period := base.Add(time.Duration(index) * time.Minute)
		snapshot := reconcilerSnapshot("crypto", "bars", "1m", period, fmt.Sprintf("S%04d", index))
		stored, _, err := db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
		require.NoError(t, err)
		if index == candidateCount-1 {
			finalSnapshot = stored
		}
	}
	client := &periodStatusStub{status: func(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
		if expectation.GetPeriodTime() != finalSnapshot.Key.PeriodTime.Unix() {
			return domain.PeriodStorageState{}, errors.New("persistent NOT_FOUND")
		}
		return reconcilerStateForSnapshot(finalSnapshot, domain.PeriodStatusDegraded), nil
	}}
	reconciler := NewPeriodStorageReconciler(db.PeriodSeriesSnapshot(), db.PeriodStorageStates(), client, "crypto")
	deleted, err := reconciler.Reconcile(ctx, now)
	require.Error(t, err)
	require.Zero(t, deleted)
	require.Equal(t, 1000, client.calls)
	_, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, finalSnapshot.Key)
	require.NoError(t, err)
	require.True(t, found, "the 1001st candidate is intentionally deferred to the next round")

	deleted, err = reconciler.Reconcile(ctx, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	require.Equal(t, 1001, client.calls)
	_, found, err = db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, finalSnapshot.Key)
	require.NoError(t, err)
	require.False(t, found, "the next keyset page reaches and reclaims the terminal candidate")
}

type periodStatusStub struct {
	calls  int
	status func(context.Context, *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error)
}

func (s *periodStatusStub) GetDatasetPeriodStatus(ctx context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
	s.calls++
	return s.status(ctx, expectation)
}

var _ PeriodStorageStatusClient = (*periodStatusStub)(nil)

func reconcilerSnapshot(spaceID, datasetID, frequency string, period time.Time, subjects ...string) domain.PeriodSeriesSnapshot {
	entries := make([]domain.PeriodSeriesSnapshotEntry, 0, len(subjects))
	keys := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		tag := "venue:binance|market:spot|source:spot_http"
		key := domain.CanonicalSeriesKey("binance", "spot_http", "spot", subject, tag)
		entries = append(entries, domain.PeriodSeriesSnapshotEntry{
			SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: period,
			SeriesIndex: uint32(len(entries)), SeriesKey: key, SubjectID: subject, Provider: "binance", SourceID: "spot_http",
			MarketType: "spot", ProviderSymbol: subject, SeriesTag: tag,
		})
		keys = append(keys, key)
	}
	hash := domain.SeriesSetHash(keys)
	for i := range entries {
		entries[i].SeriesHash = hash
		entries[i].ExpectedCount = len(entries)
	}
	return domain.PeriodSeriesSnapshot{
		Key:        domain.PeriodKey{SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: period},
		SeriesHash: hash, ExpectedCount: uint32(len(entries)), Entries: entries,
	}
}

func reconcilerStateForSnapshot(snapshot domain.PeriodSeriesSnapshot, status string) domain.PeriodStorageState {
	return domain.PeriodStorageState{
		Key: snapshot.Key, SeriesHash: snapshot.SeriesHash, ExpectedCount: snapshot.ExpectedCount,
		DeadlineAt: snapshot.Key.PeriodTime.Add(time.Minute), Status: status, ConfirmedAt: snapshot.Key.PeriodTime.Add(time.Hour),
	}
}
