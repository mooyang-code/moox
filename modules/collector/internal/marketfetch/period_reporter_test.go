package marketfetch

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
)

type periodReporterFake struct {
	payloads []*storageeventpb.CollectorPeriodCompleted
}

func (f *periodReporterFake) ReportCollectorPeriodCompleted(_ context.Context, _ string, payload *storageeventpb.CollectorPeriodCompleted) error {
	f.payloads = append(f.payloads, payload)
	return nil
}

func TestPeriodReporterPersistsPayloadBeforeRetry(t *testing.T) {
	s, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	period := time.Date(2026, 8, 9, 12, 2, 0, 0, time.UTC)
	_, err = s.PeriodReadiness().EnsurePeriod(context.Background(), domain.PeriodSeed{
		PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period},
		DeadlineAt: period.Add(time.Minute),
		Tasks:      []domain.PeriodTaskSeed{{InstanceID: "task-btc", WriteTargetID: "task-btc", SubjectID: "BTC-USDT", FunctionName: "fetch-1", WriteSource: "scf:fetch-1"}},
	})
	require.NoError(t, err)
	require.NoError(t, s.PeriodReadiness().MarkSubjectSuccess(context.Background(), domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}, "BTC-USDT", "fetch-1", "scf:fetch-1", period))
	fake := &periodReporterFake{}
	reporter := NewPeriodReporter(s.PeriodReadiness(), fake, "crypto")
	reporter.now = func() time.Time { return period.Add(10 * time.Second) }
	require.NoError(t, reporter.Flush(context.Background()))
	require.Len(t, fake.payloads, 1)
	first := fake.payloads[0].GetCollectedAt().AsTime()
	require.NoError(t, reporter.Flush(context.Background()))
	require.Len(t, fake.payloads, 1)
	require.Equal(t, first, fake.payloads[0].GetCollectedAt().AsTime())
}

func TestPeriodReportedUniverseIsDistinctSubjects(t *testing.T) {
	s, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	period := time.Date(2026, 10, 4, 12, 2, 0, 0, time.UTC)
	key := domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}
	_, err = s.PeriodReadiness().EnsurePeriod(context.Background(), domain.PeriodSeed{
		PeriodKey:  key,
		DeadlineAt: period.Add(time.Minute),
		Tasks: []domain.PeriodTaskSeed{
			{InstanceID: "spot", WriteTargetID: "spot", SubjectID: "BTCUSDT", SeriesTag: "venue:binance"},
			{InstanceID: "perpetual", WriteTargetID: "perpetual", SubjectID: "BTCUSDT", SeriesTag: "venue:binance|market:perpetual"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, s.PeriodReadiness().MarkSubjectSuccess(context.Background(), domain.PeriodKey{
		SpaceID: key.SpaceID, DatasetID: key.DatasetID, Frequency: key.Frequency, PeriodTime: key.PeriodTime, SeriesTag: "venue:binance",
	}, "BTCUSDT", "", "", period.Add(10*time.Second)))
	fake := &periodReporterFake{}
	reporter := NewPeriodReporter(s.PeriodReadiness(), fake, "crypto")
	reporter.now = func() time.Time { return period.Add(time.Minute) }
	require.NoError(t, reporter.Flush(context.Background()))
	require.Len(t, fake.payloads, 1)
	require.Equal(t, []string{"BTCUSDT"}, fake.payloads[0].GetUniverseSubjectIds())
	require.Equal(t, []string{"BTCUSDT"}, fake.payloads[0].GetFailedSubjects())
}

func TestPeriodReporterRebuildsPayloadWhenSubjectIdsMissing(t *testing.T) {
	s, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	period := time.Date(2026, 8, 9, 12, 2, 0, 0, time.UTC)
	key := domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}
	readinessID, err := s.PeriodReadiness().EnsurePeriod(context.Background(), domain.PeriodSeed{
		PeriodKey:  key,
		DeadlineAt: period.Add(time.Minute),
		Tasks:      []domain.PeriodTaskSeed{{InstanceID: "task-btc", WriteTargetID: "task-btc", SubjectID: "BTC-USDT", FunctionName: "fetch-1", WriteSource: "scf:fetch-1"}},
	})
	require.NoError(t, err)
	require.NoError(t, s.PeriodReadiness().MarkSubjectSuccess(context.Background(), key, "BTC-USDT", "fetch-1", "scf:fetch-1", period))
	require.NoError(t, s.PeriodReadiness().PersistPayload(context.Background(), readinessID, `{"datasetId":"bars","frequency":"1m","periodTime":1,"status":"complete"}`))
	fake := &periodReporterFake{}
	reporter := NewPeriodReporter(s.PeriodReadiness(), fake, "crypto")
	reporter.now = func() time.Time { return period.Add(10 * time.Second) }
	require.NoError(t, reporter.Flush(context.Background()))
	require.Len(t, fake.payloads, 1)
	require.Equal(t, []string{"BTC-USDT"}, fake.payloads[0].GetUniverseSubjectIds())
}

func TestPeriodReporterRequiresStorageAndSchema(t *testing.T) {
	s, err := store.Open(&store.Options{Path: t.TempDir() + "/collector.db"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	require.Error(t, NewPeriodReporter(s.PeriodReadiness(), nil, "crypto").Flush(context.Background()))
}

func TestPeriodReporterIsolatesSpaceBacklog(t *testing.T) {
	s, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	period := time.Date(2026, 8, 9, 12, 2, 0, 0, time.UTC)
	for _, spaceID := range []string{"crypto", "stockcn"} {
		key := domain.PeriodKey{SpaceID: spaceID, DatasetID: "bars", Frequency: "1m", PeriodTime: period}
		_, err = s.PeriodReadiness().EnsurePeriod(context.Background(), domain.PeriodSeed{
			PeriodKey: key, DeadlineAt: period.Add(time.Minute),
			Tasks: []domain.PeriodTaskSeed{{InstanceID: "task-" + spaceID, WriteTargetID: "task-" + spaceID, SubjectID: "subject-" + spaceID, FunctionName: "fetch-1", WriteSource: "scf:fetch-1"}},
		})
		require.NoError(t, err)
		require.NoError(t, s.PeriodReadiness().MarkSubjectSuccess(context.Background(), key, "subject-"+spaceID, "fetch-1", "scf:fetch-1", period))
	}

	cryptoFake := &periodReporterFake{}
	stockFake := &periodReporterFake{}
	cryptoReporter := NewPeriodReporter(s.PeriodReadiness(), cryptoFake, "crypto")
	stockReporter := NewPeriodReporter(s.PeriodReadiness(), stockFake, "stockcn")
	cryptoReporter.now = func() time.Time { return period.Add(10 * time.Second) }
	stockReporter.now = cryptoReporter.now
	require.NoError(t, cryptoReporter.Flush(context.Background()))
	require.Len(t, cryptoFake.payloads, 1)
	require.Empty(t, stockFake.payloads)
	require.NoError(t, stockReporter.Flush(context.Background()))
	require.Len(t, stockFake.payloads, 1)
}
