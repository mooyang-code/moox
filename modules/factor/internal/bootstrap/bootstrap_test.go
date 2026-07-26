package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func TestBuildSchedulerTaskUsesRealtimeHalfOpenRange(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	require.NoError(t, db.Factors().Upsert(context.Background(), domain.FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "x", SourceHash: "hash",
		Periods: []int{20}, LookbackBars: 100, Status: domain.FactorStatusEnabled,
	}))
	at := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	task, ok, err := buildSchedulerTask(context.Background(), db.Factors(), t.TempDir(), trigger.Task{
		SpaceID: "crypto", SourceDataset: "bars", TargetDataset: "bars_factor",
		SubjectID: "BTC", Freq: "1m", BarTime: at, FactorIDs: []string{"bias"},
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, at, task.StartTime)
	require.Equal(t, at.Add(time.Nanosecond), task.EndTime)
}

func TestDisabledFactorMakesRealtimeTaskNoop(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	require.NoError(t, db.Factors().Upsert(context.Background(), domain.FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "x", SourceHash: "hash",
		Periods: []int{20}, LookbackBars: 100, Status: domain.FactorStatusDisabled,
	}))
	_, ok, err := buildSchedulerTask(context.Background(), db.Factors(), t.TempDir(), trigger.Task{
		SpaceID: "crypto", SourceDataset: "bars", SubjectID: "BTC", Freq: "1m",
		BarTime: time.Now(), FactorIDs: []string{"bias"},
	})
	require.NoError(t, err)
	require.False(t, ok)
}

func TestRealtimeConsumerReadyWhenEventBusDisabled(t *testing.T) {
	cfg := Default()
	cfg.EventBus.URLs = nil
	require.True(t, realtimeConsumerReady(cfg, nil))
}
