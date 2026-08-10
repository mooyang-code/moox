package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"path/filepath"
)

func TestPeriodReadinessServiceUsesRowDataTime(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	ctx := context.Background()
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", TaskID: "task-btc", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", FunctionName: "fetch-1",
	}, {
		SpaceID: "crypto", TaskID: "task-btc-duplicate", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", FunctionName: "fetch-2",
	}}))
	service := NewPeriodReadinessService(db.TaskInstances(), db.PeriodReadiness(), time.Second)
	period := time.Date(2026, 8, 9, 12, 3, 0, 0, time.UTC)
	require.NoError(t, service.EnsureCurrentAndNext(ctx, "crypto", time.Date(2026, 8, 9, 12, 3, 45, 0, time.UTC)))
	require.NoError(t, service.ApplyRows(ctx, &storagepb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: "bars", WriteSource: "scf:fetch-2",
		Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: period.Format(time.RFC3339Nano)}}}}},
	}))
	// The next period is pre-seeded, so a row arriving at the boundary is not
	// acknowledged against a missing parent before the next timer tick.
	nextPeriod := period.Add(time.Minute)
	require.NoError(t, service.ApplyRows(ctx, &storagepb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: "bars", WriteSource: "scf:fetch-2",
		Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: nextPeriod.Format(time.RFC3339Nano)}}}}},
	}))
	reports, err := db.PeriodReadiness().FinalizeDue(ctx, period.Add(time.Minute+2*time.Second), 10)
	require.NoError(t, err)
	var found bool
	found = false
	for _, report := range reports {
		if report.Readiness.PeriodTime.Equal(period) {
			found = true
			require.Equal(t, domain.PeriodStatusComplete, report.Readiness.Status)
		}
	}
	require.True(t, found)
	found = false
	for _, report := range reports {
		if report.Readiness.PeriodTime.Equal(nextPeriod) {
			found = true
			require.Equal(t, domain.PeriodStatusComplete, report.Readiness.Status)
		}
	}
	require.True(t, found)
}
