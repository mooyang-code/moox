package pipeline

import (
	"context"
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
		var lastErr error
		for attempt := 1; attempt <= retries; attempt++ {
			if err := ctx.Err(); err != nil {
				lastErr = err
				break
			}
			lastErr = r.store.WriteRows(ctx, plan.Set.SpaceID, plan.Set.ResultDatasetID, commitID, batch)
			if lastErr == nil {
				break
			}
		}
		if lastErr != nil {
			return fmt.Errorf("%w: write factor result rows %d:%d after %d attempts: %v", storageio.ErrInfra, start, end, retries, lastErr)
		}
	}
	return nil
}
