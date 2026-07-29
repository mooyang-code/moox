package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
)

type inventoryReconcilerStub struct {
	due       bool
	refreshes int
	err       error
}

func (s *inventoryReconcilerStub) Due(time.Time) bool { return s.due }
func (s *inventoryReconcilerStub) Refresh(context.Context) error {
	s.refreshes++
	return s.err
}

type metricsReporterStub struct{ calls int }

func (s *metricsReporterStub) Handle(context.Context) error {
	s.calls++
	return nil
}

func TestBuildSchedulerTaskUsesRealtimeHalfOpenRange(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "x", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"bias"}, ParamsJSON: `{}`,
		LookbackRows: 100, Status: domain.FactorStatusEnabled,
	}))
	at := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	end := at.Add(3*time.Minute + time.Nanosecond)
	task, ok, err := buildSchedulerTask(context.Background(), db.Factors(), t.TempDir(), trigger.Task{
		SpaceID: "crypto", SourceDataset: "bars", TargetDataset: "bars_factor",
		SubjectID: "BTC", Freq: "1m", StartTime: at, EndTime: end, FactorIDs: []string{"bias"},
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, at, task.StartTime)
	require.Equal(t, end, task.EndTime)
	require.Equal(t, scheduler.DeterministicTaskID(task), task.TaskID)
}

func TestDisabledFactorMakesRealtimeTaskNoop(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "x", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"bias"}, ParamsJSON: `{}`,
		LookbackRows: 100, Status: domain.FactorStatusDisabled,
	}))
	_, ok, err := buildSchedulerTask(context.Background(), db.Factors(), t.TempDir(), trigger.Task{
		SpaceID: "crypto", SourceDataset: "bars", SubjectID: "BTC", Freq: "1m",
		StartTime: time.Now(), EndTime: time.Now().Add(time.Nanosecond), FactorIDs: []string{"bias"},
	})
	require.NoError(t, err)
	require.False(t, ok)
}

func TestRealtimeConsumerReadyWhenEventBusDisabled(t *testing.T) {
	cfg := Default()
	cfg.EventBus.URLs = nil
	require.True(t, realtimeConsumerReady(cfg, nil))
}

func TestMetricsTimerRefreshFailureDoesNotBlockReporter(t *testing.T) {
	inventory := &inventoryReconcilerStub{due: true, err: errors.New("refresh failed")}
	reporter := &metricsReporterStub{}
	handler := metricsTimerHandler(inventory, reporter, time.Now)

	require.NoError(t, handler(context.Background()))
	require.Equal(t, 1, inventory.refreshes)
	require.Equal(t, 1, reporter.calls)
}

func TestFactorAuthInfoSignsPrimaryRequestFromRuntimeSecret(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", " primary-secret ")
	auth := factorAuthInfo()
	require.Equal(t, "moox-factor", auth.GetAppId())
	require.Equal(t, mooxsecurity.HMACSHA256Hex(" primary-secret ", []byte("moox-factor")), auth.GetAppKey())
}
