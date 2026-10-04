package writer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/report"
)

// Scheduler exposes bounded run-once Archive maintenance operations.
type Scheduler struct {
	Writer          *Writer
	PendingRows     int
	DedupeRetention time.Duration
	Now             func() time.Time
	ModuleMetrics   *report.ModuleMetrics
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
	inputAt, _ := s.Writer.LatestInputTime(ctx, pendingRows)
	if s.ModuleMetrics != nil && !inputAt.IsZero() {
		_ = s.ModuleMetrics.AdvanceInputWatermark("materialize", "archive-materialize", inputAt)
	}
	writeErr := s.Writer.WriteDirty(ctx, pendingRows)
	_, pruneErr := s.Writer.PruneMessageReceipts(ctx, now.Add(-retention))
	err := errors.Join(writeErr, pruneErr)
	result := "success"
	if err != nil {
		result = "error"
	}
	if s.ModuleMetrics != nil {
		_ = s.ModuleMetrics.ObserveRun("materialize", result, "archive-materialize", now)
	}
	if s.ModuleMetrics != nil && err == nil && !inputAt.IsZero() {
		_ = s.ModuleMetrics.AdvanceWatermark("materialize", "archive-materialize", inputAt)
	}
	return err
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
