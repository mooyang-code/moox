package scheduler

import (
	"context"
	"time"
)

// RunCleaner is implemented by repositories that can delete old run rows.
type RunCleaner interface {
	DeleteRunsBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

// CleanupRuns deletes run records older than the configured retention period.
func CleanupRuns(ctx context.Context, cleaner RunCleaner, now time.Time, retentionDays int) (int64, error) {
	if cleaner == nil || retentionDays <= 0 {
		return 0, nil
	}
	return cleaner.DeleteRunsBefore(ctx, now.AddDate(0, 0, -retentionDays))
}
