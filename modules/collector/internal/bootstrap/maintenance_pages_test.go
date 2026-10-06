package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

func TestDrainMaintenancePagesStopsOnShortPageOrBudget(t *testing.T) {
	var limits []int
	deleted, err := drainMaintenancePages(context.Background(), 2500, 1000, func(_ context.Context, limit int) (int64, error) {
		limits = append(limits, limit)
		return int64(limit), nil
	})
	require.NoError(t, err)
	require.EqualValues(t, 2500, deleted)
	require.Equal(t, []int{1000, 1000, 500}, limits, "each page is its own short transaction")

	limits = nil
	deleted, err = drainMaintenancePages(context.Background(), 5000, 1000, func(_ context.Context, limit int) (int64, error) {
		limits = append(limits, limit)
		return 10, nil
	})
	require.NoError(t, err)
	require.EqualValues(t, 10, deleted)
	require.Len(t, limits, 1, "a short page means nothing more is eligible")
}

func TestDrainMaintenancePagePairsContinuesWhileEitherKindIsFull(t *testing.T) {
	calls := 0
	items, batches, err := drainMaintenancePagePairs(context.Background(), 3000, 300, 1000, 250, func(_ context.Context, itemLimit, batchLimit int) (int64, int64, error) {
		calls++
		return int64(itemLimit), int64(min(batchLimit, 10)), nil
	})
	require.NoError(t, err)
	require.EqualValues(t, 3000, items)
	require.EqualValues(t, 30, batches)
	require.Equal(t, 3, calls)
}

type fakeExecutionRetention struct {
	targetWindows, instanceWindows int
	windowsPerSweep                int
}

func (f *fakeExecutionRetention) window(count *int, after store.RetentionCursor) (int64, store.RetentionCursor, error) {
	*count++
	if after.ID+1 >= int64(f.windowsPerSweep) {
		return 1, store.RetentionCursor{}, nil
	}
	return 1, store.RetentionCursor{MTime: "t", ID: after.ID + 1}, nil
}

func (f *fakeExecutionRetention) CleanupScheduledWriteTargetsWindow(_ context.Context, _ string, _ time.Time, after store.RetentionCursor, _ int) (int64, store.RetentionCursor, error) {
	return f.window(&f.targetWindows, after)
}

func (f *fakeExecutionRetention) CleanupScheduledInstancesWindow(_ context.Context, _ string, _ time.Time, after store.RetentionCursor, _ int) (int64, store.RetentionCursor, error) {
	return f.window(&f.instanceWindows, after)
}

func TestSweepScheduledExecutionDetailsResumesAcrossPasses(t *testing.T) {
	repo := &fakeExecutionRetention{windowsPerSweep: 5}
	sweep := &executionSweep{}
	cutoff := time.Now().Add(-24 * time.Hour)

	targets, instances, err := sweepScheduledExecutionDetails(context.Background(), repo, sweep, "crypto", cutoff, time.Now().Add(time.Minute), 3, 3)
	require.NoError(t, err)
	require.EqualValues(t, 3, targets, "the deletion cap ends this pass")
	require.EqualValues(t, 3, instances)
	require.Equal(t, int64(3), sweep.targets.ID, "the cursor is kept for the next pass")

	targets, _, err = sweepScheduledExecutionDetails(context.Background(), repo, sweep, "crypto", cutoff, time.Now().Add(time.Minute), 100, 100)
	require.NoError(t, err)
	require.EqualValues(t, 2, targets, "the next pass resumes where the previous one stopped and closes the sweep")
	require.True(t, sweep.targets.IsZero(), "a closed sweep restarts from the oldest row")

	before := repo.targetWindows
	_, _, err = sweepScheduledExecutionDetails(context.Background(), repo, sweep, "crypto", cutoff, time.Now().Add(-time.Second), 100, 100)
	require.NoError(t, err)
	require.Equal(t, before, repo.targetWindows, "no window starts after the deadline")
}

func TestExecutionSweepDeadlineSharesRemainingTimeAcrossSpaces(t *testing.T) {
	now := time.Now()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(20*time.Second))
	defer cancel()
	require.Equal(t, now.Add(7500*time.Millisecond), executionSweepDeadline(ctx, now, 5*time.Second, 2))
	require.Equal(t, now.Add(15*time.Second), executionSweepDeadline(ctx, now, 5*time.Second, 1))
	require.Equal(t, now, executionSweepDeadline(ctx, now, 30*time.Second, 1), "no time is left after the reserve")
}
