package marketfetch

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
)

func TestCollectorCompletedAllSuccessIsComplete(t *testing.T) {
	reporter, fake, period := openCollectorCompletedReporter(t, []string{"BTC-USDT", "ETH-USDT"}, nil)
	require.NoError(t, reporter.Flush(context.Background()))
	require.Len(t, fake.payloads, 1)
	payload := fake.payloads[0]
	require.Equal(t, "complete", payload.GetStatus())
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, payload.GetUniverseSubjectIds())
	require.Empty(t, payload.GetFailedSubjects())
	require.Equal(t, period.Unix(), payload.GetPeriodTime())
	require.NotEmpty(t, payload.GetBatchId())
}

func TestCollectorCompletedTimeoutKeepsFullSubjectSet(t *testing.T) {
	reporter, fake, _ := openCollectorCompletedReporter(t, []string{"BTC-USDT", "ETH-USDT"}, []string{"BTC-USDT"})
	require.NoError(t, reporter.Flush(context.Background()))
	require.Len(t, fake.payloads, 1)
	payload := fake.payloads[0]
	require.Equal(t, "degraded", payload.GetStatus())
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, payload.GetUniverseSubjectIds())
	require.Equal(t, []string{"ETH-USDT"}, payload.GetFailedSubjects())
}

func TestCollectorCompletedRestartDoesNotChangeFrozenSet(t *testing.T) {
	db := openCollectorCompletedStore(t)
	period := time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC)
	key := domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}
	_, err := db.PeriodReadiness().EnsurePeriod(context.Background(), domain.PeriodSeed{
		PeriodKey: key, DeadlineAt: period.Add(time.Minute),
		Tasks: []domain.PeriodTaskSeed{
			{TaskID: "task-btc", SubjectID: "BTC-USDT", FunctionName: "fetch-1", WriteSource: "scf:fetch-1"},
			{TaskID: "task-eth", SubjectID: "ETH-USDT", FunctionName: "fetch-1", WriteSource: "scf:fetch-1"},
		},
	})
	require.NoError(t, err)
	_, err = db.PeriodReadiness().EnsurePeriod(context.Background(), domain.PeriodSeed{
		PeriodKey: key, DeadlineAt: period.Add(time.Minute),
		Tasks: []domain.PeriodTaskSeed{
			{TaskID: "task-btc", SubjectID: "BTC-USDT", FunctionName: "fetch-1", WriteSource: "scf:fetch-1"},
			{TaskID: "task-eth", SubjectID: "ETH-USDT", FunctionName: "fetch-1", WriteSource: "scf:fetch-1"},
			{TaskID: "task-sol", SubjectID: "SOL-USDT", FunctionName: "fetch-1", WriteSource: "scf:fetch-1"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, db.PeriodReadiness().MarkSubjectSuccess(context.Background(), key, "BTC-USDT", "fetch-1", "scf:fetch-1", period))
	require.NoError(t, db.PeriodReadiness().MarkSubjectSuccess(context.Background(), key, "ETH-USDT", "fetch-1", "scf:fetch-1", period))
	fake := &periodReporterFake{}
	reporter := NewPeriodReporter(db.PeriodReadiness(), fake, "crypto", time.Hour)
	reporter.now = func() time.Time { return period.Add(10 * time.Second) }
	require.NoError(t, reporter.Flush(context.Background()))
	require.Len(t, fake.payloads, 1)
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, fake.payloads[0].GetUniverseSubjectIds())
}

func TestCollectorCompletedDuplicateReportDoesNotCreateNewBatch(t *testing.T) {
	reporter, fake, _ := openCollectorCompletedReporter(t, []string{"BTC-USDT"}, nil)
	require.NoError(t, reporter.Flush(context.Background()))
	require.NoError(t, reporter.Flush(context.Background()))
	require.Len(t, fake.payloads, 1)
	firstBatch := fake.payloads[0].GetBatchId()
	require.NotEmpty(t, firstBatch)
	require.Equal(t, firstBatch, fake.payloads[0].GetBatchId())
}

func TestCollectorCompletedStripsSpotAndSwapSuffix(t *testing.T) {
	require.Equal(t, "0G-USDT", marketdata.CanonicalCryptoSubjectID("0G-USDT-SPOT"))
	require.Equal(t, "BTC-USDT", marketdata.CanonicalCryptoSubjectID("BTC-USDT-SWAP"))
	require.Equal(t, "ETH-USDT", marketdata.CanonicalCryptoSubjectID("ETH-USDT"))
	require.Equal(t, "SOL-USDT", marketdata.CanonicalCryptoSubjectID("sol-usdt-spot"))
}

func TestCollectorCompletedUsesStorageWritePositions(t *testing.T) {
	db := openCollectorCompletedStore(t)
	period := time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC)
	key := domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}
	_, err := db.PeriodReadiness().EnsurePeriod(context.Background(), domain.PeriodSeed{
		PeriodKey: key, DeadlineAt: period.Add(time.Minute),
		Tasks: []domain.PeriodTaskSeed{{TaskID: "task-btc", SubjectID: "BTC-USDT", FunctionName: "fetch-1", WriteSource: "scf:fetch-1"}},
	})
	require.NoError(t, err)
	service := NewPeriodReadinessService(db.TaskInstances(), db.PeriodReadiness(), time.Second)
	require.NoError(t, service.ApplyRows(context.Background(), &storageeventpb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: "bars", WriteSource: "scf:fetch-1",
		SourceNodeId: "dn-1", SourceStoreId: "store-a", SourceSequence: 42,
		Rows: []*storageeventpb.RowUpsert{{Key: &storageeventpb.RowKey{Kind: &storageeventpb.RowKey_TimeSeries{TimeSeries: &storageeventpb.TimeSeriesRowKey{
			SubjectId: "BTC-USDT", Freq: "1m", DataTime: period.Format(time.RFC3339Nano),
		}}}}},
	}))
	fake := &periodReporterFake{}
	reporter := NewPeriodReporter(db.PeriodReadiness(), fake, "crypto", time.Hour)
	reporter.now = func() time.Time { return period.Add(10 * time.Second) }
	require.NoError(t, reporter.Flush(context.Background()))
	require.Len(t, fake.payloads, 1)
	positions := fake.payloads[0].GetCommittedPositions()
	require.Len(t, positions, 1)
	require.Equal(t, "dn-1", positions[0].GetNodeId())
	require.Equal(t, "store-a", positions[0].GetStoreId())
	require.Equal(t, uint64(42), positions[0].GetSequence())
}

func openCollectorCompletedStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	return db
}

func openCollectorCompletedReporter(t *testing.T, subjects, succeed []string) (*PeriodReporter, *periodReporterFake, time.Time) {
	t.Helper()
	db := openCollectorCompletedStore(t)
	period := time.Date(2026, 9, 13, 16, 5, 0, 0, time.UTC)
	key := domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}
	tasks := make([]domain.PeriodTaskSeed, 0, len(subjects))
	for _, subject := range subjects {
		tasks = append(tasks, domain.PeriodTaskSeed{
			TaskID: "task-" + subject, SubjectID: subject, FunctionName: "fetch-1", WriteSource: "scf:fetch-1",
		})
	}
	_, err := db.PeriodReadiness().EnsurePeriod(context.Background(), domain.PeriodSeed{
		PeriodKey: key, DeadlineAt: period.Add(time.Minute), Tasks: tasks,
	})
	require.NoError(t, err)
	if succeed == nil {
		succeed = subjects
	}
	for _, subject := range succeed {
		require.NoError(t, db.PeriodReadiness().MarkSubjectSuccess(context.Background(), key, subject, "fetch-1", "scf:fetch-1", period))
	}
	fake := &periodReporterFake{}
	reporter := NewPeriodReporter(db.PeriodReadiness(), fake, "crypto", time.Hour)
	reporter.now = func() time.Time { return period.Add(2 * time.Minute) }
	return reporter, fake, period
}
