package inputcache

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCacheCapacityPolicyDelaysFirstCheck(t *testing.T) {
	cfg := DefaultConfig()
	require.Equal(t, 2233*time.Second, cfg.CheckInterval)
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	calls := 0
	job, err := NewMaintenanceJob(cfg, func() time.Time { return now }, func(context.Context) error {
		calls++
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, job.Handle(context.Background()))
	require.Zero(t, calls)
	now = now.Add(cfg.CheckInterval)
	require.NoError(t, job.Handle(context.Background()))
	require.Equal(t, 1, calls)
}

func TestCacheCapacityPolicySharesBudgetAcrossDatasets(t *testing.T) {
	m := testManager(t)
	m.cfg.MaxBytes = 1
	ctx := context.Background()
	a, err := m.Get(ctx, SourceKey{"crypto", "mdataset_a"}, "schema-1", []Column{{"id", "VARCHAR"}}, []string{"id"})
	require.NoError(t, err)
	b, err := m.Get(ctx, SourceKey{"crypto", "mdataset_b"}, "schema-1", []Column{{"id", "VARCHAR"}}, []string{"id"})
	require.NoError(t, err)
	require.NoError(t, a.Fill(func(db *Database, _ uint64) error {
		return db.Upsert(ctx, [][]any{{"BTC"}, {"ETH"}}, time.Now())
	}))
	require.NoError(t, b.Fill(func(db *Database, _ uint64) error {
		return db.Upsert(ctx, [][]any{{"SOL"}, {"XRP"}}, time.Now())
	}))
	result, err := m.Maintain(ctx)
	require.NoError(t, err)
	require.True(t, result.PauseWrites)
	require.Greater(t, result.Bytes, m.cfg.MaxBytes)
}

func TestCacheCapacityPolicyStopsWhenNRowsStillExceedBytes(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.MaxBytes = 1
	cfg.RebuildKeepRows = 1
	g, err := NewGeneration(ctx, cfg.Dir, []Column{{"id", "VARCHAR"}}, []string{"id"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, g.Close()) })
	require.NoError(t, g.Use(func(db *Database, _ uint64) error {
		return db.Upsert(ctx, [][]any{{"BTC"}, {"ETH"}}, time.Now())
	}))
	first, err := maintainCapacity(ctx, cfg, []*Generation{g}, func(string) (uint64, error) { return 1 << 60, nil })
	require.NoError(t, err)
	require.Equal(t, 1, first.Rebuilt)
	require.True(t, first.PauseWrites)
	second, err := maintainCapacity(ctx, cfg, []*Generation{g}, func(string) (uint64, error) { return 1 << 60, nil })
	require.NoError(t, err)
	require.Zero(t, second.Rebuilt)
	require.True(t, second.PauseWrites)
}

func TestCacheCapacityPolicyKeepsOldReadersOnLiveGeneration(t *testing.T) {
	ctx := context.Background()
	g, err := NewGeneration(ctx, t.TempDir(), []Column{{"id", "VARCHAR"}}, []string{"id"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, g.Close()) })
	require.NoError(t, g.Use(func(db *Database, _ uint64) error {
		return db.Upsert(ctx, [][]any{{"BTC"}}, time.Now())
	}))
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- g.Use(func(db *Database, _ uint64) error {
			close(entered)
			<-release
			var id string
			err := db.db.QueryRowContext(ctx, "SELECT id FROM cached_rows").Scan(&id)
			require.Equal(t, "BTC", id)
			return err
		})
	}()
	<-entered
	require.ErrorIs(t, g.Use(func(*Database, uint64) error { return nil }), ErrCacheBusy)
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, g.Use(func(db *Database, _ uint64) error {
		var id string
		err := db.db.QueryRowContext(ctx, "SELECT id FROM cached_rows").Scan(&id)
		require.Equal(t, "BTC", id)
		return err
	}))
}

func TestCacheCapacityPolicyRecoversAfterCancelledRebuild(t *testing.T) {
	ctx := context.Background()
	g, err := NewGeneration(ctx, t.TempDir(), []Column{{"id", "VARCHAR"}}, []string{"id"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, g.Close()) })
	require.NoError(t, g.Use(func(db *Database, _ uint64) error {
		return db.Upsert(ctx, [][]any{{"BTC"}}, time.Now())
	}))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.Error(t, g.Rebuild(cancelled, 1))
	require.NoError(t, g.Use(func(db *Database, epoch uint64) error {
		require.EqualValues(t, 1, epoch)
		var id string
		err := db.db.QueryRowContext(ctx, "SELECT id FROM cached_rows").Scan(&id)
		require.Equal(t, "BTC", id)
		return err
	}))
	require.NoError(t, g.Rebuild(ctx, 1))
}

func TestCacheCapacityPolicyReadsDoNotRefreshEvictionTime(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := CreateDatabase(ctx, dir+"/rows.duckdb", []Column{{"id", "VARCHAR"}, {"data_time", "TIMESTAMP_NS"}}, []string{"id", "data_time"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	older := time.Date(2026, 9, 13, 4, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)
	require.NoError(t, db.Upsert(ctx, [][]any{{"BTC", older}}, older))
	require.NoError(t, db.Upsert(ctx, [][]any{{"ETH", newer}}, newer))
	_, err = db.ReadWindow(ctx, WindowQuery{TimeColumn: "data_time", Through: newer, Lookback: 2})
	require.NoError(t, err)
	next, err := db.Rebuild(ctx, dir+"/kept.duckdb", 1)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, next.Close()) })
	var id string
	require.NoError(t, next.db.QueryRowContext(ctx, "SELECT id FROM cached_rows").Scan(&id))
	require.Equal(t, "ETH", id)
}
