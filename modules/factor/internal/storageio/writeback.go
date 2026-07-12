package storageio

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// WriteFactorPatch writes returned factor tail values as Storage column patches.
func (c *Client) WriteFactorPatch(ctx context.Context, task *engine.FactorTask, frame *engine.DataFrame, result *engine.FactorResult) error {
	if task == nil || frame == nil || result == nil {
		return fmt.Errorf("task, frame and result are required")
	}
	maxTail := 0
	for _, col := range result.Columns {
		if col.Tail > maxTail {
			maxTail = col.Tail
		}
	}
	if maxTail <= 0 || len(frame.DataTimes) == 0 {
		return nil
	}
	start := len(frame.DataTimes) - maxTail
	if start < 0 {
		start = 0
	}
	rows := make([]*storagepb.TimeSeriesRow, 0, len(frame.DataTimes)-start)
	computedAt := SnapshotComputedAt()
	for rowIdx := start; rowIdx < len(frame.DataTimes); rowIdx++ {
		row := &storagepb.TimeSeriesRow{
			Key: &storagepb.TimeSeriesKey{
				SpaceId:   task.SpaceID,
				DatasetId: task.TargetDataset,
				SubjectId: task.SubjectID,
				Freq:      task.Freq,
				DataTime:  frame.DataTimes[rowIdx].UTC().Format(time.RFC3339),
			},
			Attributes: map[string]string{
				"factor.parent_task_id": task.TaskID,
				"factor.snapshot_hash":  task.SnapshotHash,
				"factor.computed_at":    computedAt,
			},
		}
		for columnName, col := range result.Columns {
			value, ok := valueForFrameRow(rowIdx, len(frame.DataTimes), col)
			if !ok || value == nil {
				continue
			}
			floatValue, ok := asFloat64(value)
			if !ok {
				return fmt.Errorf("factor column %s returned non-numeric value %T", columnName, value)
			}
			row.Columns = append(row.Columns, doubleField(columnName, floatValue))
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	rsp, err := c.access.WriteTimeSeriesRows(ctx, &storagepb.WriteTimeSeriesRowsReq{AuthInfo: c.auth, Rows: rows})
	if err != nil {
		return fmt.Errorf("write factor patch: %w", err)
	}
	return ensureStorageOK("write factor patch", rsp.GetRetInfo())
}

func valueForFrameRow(rowIdx int, frameRows int, col engine.FactorColumnResult) (any, bool) {
	colStart := frameRows - col.Tail
	if colStart < 0 {
		colStart = 0
	}
	if rowIdx < colStart {
		return nil, false
	}
	valueIdx := rowIdx - colStart
	if valueIdx < 0 || valueIdx >= len(col.Values) {
		return nil, false
	}
	return col.Values[valueIdx], true
}

func asFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	default:
		return 0, false
	}
}
