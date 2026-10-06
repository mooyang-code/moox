package bootstrap

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

// Collector shares one SQLite connection between the market scheduler,
// completion consumers, RPC handlers, and maintenance. Every cleanup step
// therefore runs as a sequence of short transactions so the connection is
// released between pages instead of being held for a whole pass.
const (
	maintenancePageRows        = 1000
	maintenanceBatchPageRows   = 250
	executionCleanupWindowRows = 2000
)

// drainMaintenancePages repeats a bounded cleanup page until the budget is
// spent, a page comes back short, or the pass context ends.
func drainMaintenancePages(ctx context.Context, budget, pageRows int, page func(context.Context, int) (int64, error)) (int64, error) {
	var deleted int64
	for remaining := budget; remaining > 0 && ctx.Err() == nil; {
		limit := min(pageRows, remaining)
		n, err := page(ctx, limit)
		deleted += n
		if err != nil {
			return deleted, err
		}
		remaining -= int(n)
		if n < int64(limit) {
			break
		}
	}
	return deleted, nil
}

// drainMaintenancePagePairs is drainMaintenancePages for cleanup steps that
// delete two related row kinds (for example FetchBatch items and parents).
func drainMaintenancePagePairs(ctx context.Context, budgetA, budgetB, pageA, pageB int, page func(context.Context, int, int) (int64, int64, error)) (int64, int64, error) {
	var deletedA, deletedB int64
	for remainingA, remainingB := budgetA, budgetB; (remainingA > 0 || remainingB > 0) && ctx.Err() == nil; {
		limitA, limitB := max(min(pageA, remainingA), 0), max(min(pageB, remainingB), 0)
		a, b, err := page(ctx, limitA, limitB)
		deletedA, deletedB = deletedA+a, deletedB+b
		if err != nil {
			return deletedA, deletedB, err
		}
		remainingA, remainingB = remainingA-int(a), remainingB-int(b)
		// A kind is finished once its budget is spent or its page came back short.
		if (remainingA <= 0 || a < int64(limitA)) && (remainingB <= 0 || b < int64(limitB)) {
			break
		}
	}
	return deletedA, deletedB, nil
}

type executionRetentionStore interface {
	CleanupScheduledWriteTargetsWindow(ctx context.Context, spaceID string, cutoff time.Time, after store.RetentionCursor, window int) (int64, store.RetentionCursor, error)
	CleanupScheduledInstancesWindow(ctx context.Context, spaceID string, cutoff time.Time, after store.RetentionCursor, window int) (int64, store.RetentionCursor, error)
}

// executionSweep remembers where each keyset sweep stopped, so rows that must
// be kept are skipped once per sweep rather than rescanned on every pass.
type executionSweep struct {
	targets, instances store.RetentionCursor
}

// sweepScheduledExecutionDetails alternates write-target and instance windows
// until both sweeps reach the cutoff in this pass, the deletion caps are
// spent, or the deadline passes. The cursors persist across passes.
func sweepScheduledExecutionDetails(
	ctx context.Context,
	repo executionRetentionStore,
	sweep *executionSweep,
	spaceID string,
	cutoff, deadline time.Time,
	targetCap, instanceCap int,
) (targets, instances int64, err error) {
	targetsDone, instancesDone := targetCap <= 0, instanceCap <= 0
	for !(targetsDone && instancesDone) && ctx.Err() == nil && time.Now().Before(deadline) {
		if !targetsDone {
			n, next, windowErr := repo.CleanupScheduledWriteTargetsWindow(ctx, spaceID, cutoff, sweep.targets, executionCleanupWindowRows)
			if windowErr != nil {
				return targets, instances, windowErr
			}
			targets, sweep.targets = targets+n, next
			targetsDone = next.IsZero() || targets >= int64(targetCap)
		}
		if !instancesDone && ctx.Err() == nil {
			n, next, windowErr := repo.CleanupScheduledInstancesWindow(ctx, spaceID, cutoff, sweep.instances, executionCleanupWindowRows)
			if windowErr != nil {
				return targets, instances, windowErr
			}
			instances, sweep.instances = instances+n, next
			instancesDone = next.IsZero() || instances >= int64(instanceCap)
		}
	}
	return targets, instances, nil
}
