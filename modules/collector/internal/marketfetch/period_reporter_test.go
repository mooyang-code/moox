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
	payloads []*storageeventpb.DatasetPeriodCollected
}

func (f *periodReporterFake) ReportDatasetPeriodCollected(_ context.Context, _ string, payload *storageeventpb.DatasetPeriodCollected) error {
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
		Tasks:      []domain.PeriodTaskSeed{{TaskID: "task-btc", SubjectID: "BTC-USDT", FunctionName: "fetch-1", WriteSource: "scf:fetch-1"}},
	})
	require.NoError(t, err)
	require.NoError(t, s.PeriodReadiness().MarkSubjectSuccess(context.Background(), domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}, "BTC-USDT", "fetch-1", "scf:fetch-1", period))
	fake := &periodReporterFake{}
	reporter := NewPeriodReporter(s.PeriodReadiness(), fake, "crypto", time.Hour)
	reporter.now = func() time.Time { return period.Add(10 * time.Second) }
	require.NoError(t, reporter.Flush(context.Background()))
	require.Len(t, fake.payloads, 1)
	first := fake.payloads[0].GetCollectedAt().AsTime()
	require.NoError(t, reporter.Flush(context.Background()))
	require.Len(t, fake.payloads, 1)
	require.Equal(t, first, fake.payloads[0].GetCollectedAt().AsTime())
}

func TestPeriodReporterRequiresStorageAndSchema(t *testing.T) {
	s, err := store.Open(&store.Options{Path: t.TempDir() + "/collector.db"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	require.Error(t, NewPeriodReporter(s.PeriodReadiness(), nil, "crypto", time.Hour).Flush(context.Background()))
}
