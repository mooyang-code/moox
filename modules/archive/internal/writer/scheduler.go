package writer

import (
	"context"
	"time"
)

// Scheduler periodically materializes dirty partitions and flushes once more on shutdown.
// It is intentionally small: the journal remains the source of truth for retries and recovery.
type Scheduler struct {
	Writer          *Writer
	Interval        time.Duration
	PendingRows     int
	ShutdownTimeout time.Duration
	DedupeRetention time.Duration
}

func (s Scheduler) Run(ctx context.Context) error {
	if s.Writer == nil {
		return context.Canceled
	}
	if s.Interval <= 0 {
		s.Interval = 10 * time.Minute
	}
	if s.PendingRows <= 0 {
		s.PendingRows = 10000
	}
	if s.ShutdownTimeout <= 0 {
		s.ShutdownTimeout = 2 * time.Minute
	}
	if s.DedupeRetention <= 0 {
		s.DedupeRetention = 168 * time.Hour
	}
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), s.ShutdownTimeout)
			err := s.Writer.WriteDirty(flushCtx, s.PendingRows)
			cancel()
			return err
		case <-ticker.C:
			if err := s.Writer.WriteDirty(ctx, s.PendingRows); err != nil {
				return err
			}
			if _, err := s.Writer.PruneMessageReceipts(ctx, time.Now().UTC().Add(-s.DedupeRetention)); err != nil {
				return err
			}
		}
	}
}
