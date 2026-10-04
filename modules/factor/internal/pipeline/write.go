package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

func (r *Runner) Write(ctx context.Context, plan Plan, target time.Time, rows []storageio.ResultRow, batchSize int) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("factor Storage store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(rows) == 0 {
		return nil
	}
	if batchSize <= 0 {
		batchSize = r.cfg.WriteBatchRows
	}
	if batchSize <= 0 {
		batchSize = 1000
	}
	retries := r.cfg.WriteRetries
	if retries <= 0 {
		retries = 3
	}
	for start := 0; start < len(rows); start += batchSize {
		end := start + batchSize
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[start:end]
		commitID := storageio.CommitID(plan.Set.SetID, target.Unix(), batch)
		if err := r.writeBatch(ctx, plan, commitID, batch, retries); err != nil {
			return fmt.Errorf("write factor result rows %d:%d: %w", start, end, err)
		}
	}
	return nil
}

// writeBatch retries only transient failures. A permanent Storage rejection
// (for example a column missing from the result dataset) would fail identically
// on every attempt, so it is returned without the ErrInfra marker and the caller
// does not redeliver the period indefinitely.
func (r *Runner) writeBatch(ctx context.Context, plan Plan, commitID string, batch []storageio.ResultRow, retries int) error {
	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", storageio.ErrInfra, err)
		}
		lastErr = r.store.WriteRows(ctx, plan.Set.SpaceID, plan.Set.ResultDatasetID, commitID, batch)
		if lastErr == nil {
			return nil
		}
		if !errors.Is(lastErr, storageio.ErrInfra) {
			return lastErr
		}
		if attempt == retries || r.cfg.WriteRetryBackoff <= 0 {
			continue
		}
		timer := time.NewTimer(time.Duration(attempt) * r.cfg.WriteRetryBackoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w: %v", storageio.ErrInfra, ctx.Err())
		case <-timer.C:
		}
	}
	return fmt.Errorf("after %d attempts: %w", retries, lastErr)
}
