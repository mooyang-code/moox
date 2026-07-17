package writer

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Scheduler exposes bounded run-once Archive maintenance operations.
type Scheduler struct {
	Writer          *Writer
	PendingRows     int
	DedupeRetention time.Duration
	Now             func() time.Time
}

// MaterializeOnce writes dirty partitions and prunes expired message receipts.
func (s Scheduler) MaterializeOnce(ctx context.Context) error {
	if s.Writer == nil {
		return fmt.Errorf("archive writer is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	pendingRows := s.PendingRows
	if pendingRows <= 0 {
		pendingRows = 10000
	}
	retention := s.DedupeRetention
	if retention <= 0 {
		retention = 168 * time.Hour
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	writeErr := s.Writer.WriteDirty(ctx, pendingRows)
	_, pruneErr := s.Writer.PruneMessageReceipts(ctx, now.Add(-retention))
	return errors.Join(writeErr, pruneErr)
}

// FlushOnShutdown writes remaining dirty partitions without pruning receipts.
func (s Scheduler) FlushOnShutdown(ctx context.Context) error {
	if s.Writer == nil {
		return fmt.Errorf("archive writer is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	pendingRows := s.PendingRows
	if pendingRows <= 0 {
		pendingRows = 10000
	}
	return s.Writer.WriteDirty(ctx, pendingRows)
}
